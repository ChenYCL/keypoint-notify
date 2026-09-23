// Package client is the CLI's HTTP client for the Keypoint Notify API.
package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChenYCL/keypoint-notify/internal/config"
)

// Client talks to one server as one identity.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// New builds a client from a loaded config.
func New(cfg *config.Config) *Client {
	return &Client{
		BaseURL: strings.TrimRight(cfg.Server, "/"),
		APIKey:  cfg.APIKey,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// Error is a server-side API error, decoded from the standard error shape.
// Keeping the structured fields (rather than just the message) is what lets
// the CLI print the hint and the "did you mean" line, which is the whole point
// of that shape.
type Error struct {
	Status     int      `json:"-"`
	Code       string   `json:"error"`
	Message    string   `json:"message"`
	Hint       string   `json:"hint"`
	DidYouMean string   `json:"did_you_mean"`
	Options    []string `json:"options"`
	Field      string   `json:"field"`
	Docs       string   `json:"docs"`
}

func (e *Error) Error() string { return e.Message }

// Pretty renders the error the way a person wants to read it on a terminal.
func (e *Error) Pretty() string {
	var b strings.Builder
	fmt.Fprintf(&b, "✗ %s: %s\n", e.Code, e.Message)
	if e.DidYouMean != "" {
		fmt.Fprintf(&b, "  你是指：%s\n", e.DidYouMean)
	}
	if e.Hint != "" {
		fmt.Fprintf(&b, "  → %s\n", e.Hint)
	}
	if len(e.Options) > 0 && len(e.Options) <= 12 {
		fmt.Fprintf(&b, "  可选值：%s\n", strings.Join(e.Options, ", "))
	}
	return b.String()
}

// ConnectionError means the request never reached the server.
type ConnectionError struct {
	URL string
	Err error
}

func (e *ConnectionError) Error() string {
	return fmt.Sprintf("连不上 %s: %v", e.URL, e.Err)
}

// Hint suggests the usual fixes for an unreachable server.
func (e *ConnectionError) Hint() string {
	switch {
	case strings.Contains(e.URL, "127.0.0.1") || strings.Contains(e.URL, "localhost"):
		return "服务没起来？在数据目录里跑 `kp serve`，或先 `kp init` 换一个地址"
	default:
		return "检查网络/隧道；`kp config get server` 看当前地址，`kp config set server <url>` 改"
	}
}

// do performs a request and decodes a JSON response into out (which may be nil).
func (c *Client) do(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		switch v := body.(type) {
		case io.Reader:
			reader = v
		case []byte:
			reader = bytes.NewReader(v)
		case string:
			reader = strings.NewReader(v)
		default:
			data, err := json.Marshal(body)
			if err != nil {
				return err
			}
			reader = bytes.NewReader(data)
		}
	}
	req, err := http.NewRequest(method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &ConnectionError{URL: c.BaseURL + path, Err: err}
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		apiErr := &Error{Status: resp.StatusCode}
		if err := json.Unmarshal(data, apiErr); err != nil || apiErr.Code == "" {
			apiErr.Code = fmt.Sprintf("http_%d", resp.StatusCode)
			apiErr.Message = strings.TrimSpace(string(data))
			if apiErr.Message == "" {
				apiErr.Message = http.StatusText(resp.StatusCode)
			}
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	if raw, ok := out.(*string); ok {
		*raw = string(data)
		return nil
	}
	if raw, ok := out.(*[]byte); ok {
		*raw = data
		return nil
	}
	return json.Unmarshal(data, out)
}

// Get performs a GET and decodes JSON.
func (c *Client) Get(path string, out any) error { return c.do(http.MethodGet, path, nil, out) }

// Post performs a POST with a JSON body.
func (c *Client) Post(path string, body any, out any) error {
	return c.do(http.MethodPost, path, body, out)
}

// Patch performs a PATCH with a JSON body.
func (c *Client) Patch(path string, body any, out any) error {
	return c.do(http.MethodPatch, path, body, out)
}

// Delete performs a DELETE.
func (c *Client) Delete(path string, out any) error {
	return c.do(http.MethodDelete, path, nil, out)
}

// GetText fetches a text/markdown/plain response verbatim — used for /pack and
// single segments, where the body IS the payload and JSON would only get in
// the way.
func (c *Client) GetText(path string) (string, error) {
	var s string
	if err := c.do(http.MethodGet, path, nil, &s); err != nil {
		return "", err
	}
	return s, nil
}

// Q builds a query string, skipping empty values so callers can pass
// conditionally-set variables without branching.
func Q(pairs ...string) string {
	if len(pairs)%2 != 0 {
		panic("client.Q: odd number of arguments")
	}
	vals := url.Values{}
	for i := 0; i < len(pairs); i += 2 {
		if v := strings.TrimSpace(pairs[i+1]); v != "" {
			vals.Set(pairs[i], v)
		}
	}
	if len(vals) == 0 {
		return ""
	}
	return "?" + vals.Encode()
}

// Upload posts files as multipart/form-data and returns the raw response.
func (c *Client) Upload(path string, files []string, extra map[string]string) (map[string]any, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		part, err := w.CreateFormFile("file", filepath.Base(f))
		if err != nil {
			fh.Close()
			return nil, err
		}
		if _, err := io.Copy(part, fh); err != nil {
			fh.Close()
			return nil, err
		}
		fh.Close()
	}
	for k, v := range extra {
		_ = w.WriteField(k, v)
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &ConnectionError{URL: c.BaseURL + path, Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		apiErr := &Error{Status: resp.StatusCode}
		if err := json.Unmarshal(data, apiErr); err != nil || apiErr.Code == "" {
			apiErr.Code = fmt.Sprintf("http_%d", resp.StatusCode)
			apiErr.Message = strings.TrimSpace(string(data))
		}
		return nil, apiErr
	}
	out := map[string]any{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// IsNotFound reports whether err is a 404 from the server.
func IsNotFound(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}
