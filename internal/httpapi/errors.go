// Package httpapi exposes the Keypoint Notify HTTP surface.
//
// Two audiences share one API: humans (the embedded UI, the CLI) and language
// models reading a task's context. That shapes the error model below — every
// failure carries a machine-readable code, a human sentence, and a suggested
// next action, because a model that gets `segment_not_found` with a list of
// valid keys can recover on its own, while a model that gets "400 Bad Request"
// cannot.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/light/keypoint-notify/internal/store"
)

// APIError is the single error shape every endpoint returns.
type APIError struct {
	Status     int      `json:"-"`
	Code       string   `json:"error"`
	Message    string   `json:"message"`
	Hint       string   `json:"hint,omitempty"`
	DidYouMean string   `json:"did_you_mean,omitempty"`
	Options    []string `json:"options,omitempty"`
	Field      string   `json:"field,omitempty"`
	Docs       string   `json:"docs,omitempty"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// NewError builds an error with a status and a stable code.
func NewError(status int, code, format string, args ...any) *APIError {
	return &APIError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithHint attaches the "what to do next" line.
func (e *APIError) WithHint(format string, args ...any) *APIError {
	e.Hint = fmt.Sprintf(format, args...)
	return e
}

// WithOptions lists the accepted values, which is what makes a validation
// failure self-correcting for a model caller.
func (e *APIError) WithOptions(field string, opts []string) *APIError {
	e.Field = field
	e.Options = opts
	return e
}

// WithSuggest records the nearest valid value to what the caller typed.
func (e *APIError) WithSuggest(s string) *APIError {
	e.DidYouMean = s
	return e
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeText(w http.ResponseWriter, status int, contentType, body string) {
	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func writeOK(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, v) }

// respondError translates any error into the APIError shape.
func respondError(w http.ResponseWriter, err error) {
	var api *APIError
	if errors.As(err, &api) {
		if api.Docs == "" {
			api.Docs = "/api/v1/llms.txt"
		}
		writeJSON(w, api.Status, api)
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, &APIError{
			Code: "not_found", Message: err.Error(), Docs: "/api/v1/llms.txt",
		})
	case errors.Is(err, store.ErrConflict):
		writeJSON(w, http.StatusConflict, &APIError{
			Code: "conflict", Message: err.Error(), Docs: "/api/v1/llms.txt",
		})
	case errors.Is(err, store.ErrTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, &APIError{
			Code: "file_too_large", Message: err.Error(),
			Hint: "附件上限 32MB；更大的内容请改用 link（任务链接字段或分段里的 URL）",
		})
	default:
		writeJSON(w, http.StatusInternalServerError, &APIError{
			Code: "internal_error", Message: err.Error(),
			Hint: "这是服务端问题，可重试；持续失败请查服务日志",
		})
	}
}

// decodeJSON reads a JSON body into v, rejecting unknown fields so that a typo
// in a field name fails loudly instead of silently doing nothing.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return NewError(http.StatusBadRequest, "invalid_json", "请求体不是合法 JSON: %v", err).
			WithHint("字段名拼错也会报这个错；完整字段表见 /api/v1/llms.txt")
	}
	return nil
}

// levenshtein returns the edit distance between two strings, used for
// "did_you_mean" suggestions.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// nearest picks the closest candidate to input, or "" when nothing is close
// enough to be worth suggesting.
func nearest(input string, candidates []string) string {
	best, bestDist := "", 1<<30
	lower := strings.ToLower(input)
	for _, c := range candidates {
		d := levenshtein(lower, strings.ToLower(c))
		if d < bestDist {
			best, bestDist = c, d
		}
	}
	// Allow roughly one edit per three characters.
	if bestDist > 1+len([]rune(lower))/3 {
		return ""
	}
	return best
}
