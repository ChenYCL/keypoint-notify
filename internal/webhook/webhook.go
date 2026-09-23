// Package webhook delivers task events to outbound HTTP subscribers.
//
// Delivery is driven by a durable per-subscriber cursor over the events table
// rather than an in-memory fan-out. That costs one query per tick and buys two
// things worth more: a restart loses no events, and a subscriber that was down
// for an hour still receives what it missed.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ChenYCL/keypoint-notify/internal/model"
	"github.com/ChenYCL/keypoint-notify/internal/store"
)

// Dispatcher polls the event log and POSTs matching events to subscribers.
type Dispatcher struct {
	St       *store.Store
	Client   *http.Client
	Interval time.Duration
	// OnError is called for delivery failures so the host can log them.
	OnError func(format string, args ...any)
}

// New builds a dispatcher with sane defaults.
func New(st *store.Store, onError func(string, ...any)) *Dispatcher {
	return &Dispatcher{
		St:       st,
		Client:   &http.Client{Timeout: 10 * time.Second},
		Interval: 3 * time.Second,
		OnError:  onError,
	}
}

// Run polls until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	interval := d.Interval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) {
	hooks, err := d.St.ListWebhooks()
	if err != nil {
		d.logf("list webhooks: %v", err)
		return
	}
	for _, h := range hooks {
		if !h.Enabled {
			continue
		}
		if err := d.deliverPending(ctx, h); err != nil {
			d.logf("webhook %s: %v", h.URL, err)
		}
	}
}

func (d *Dispatcher) deliverPending(ctx context.Context, h model.Webhook) error {
	raw, err := d.St.GetMeta(store.WebhookCursorKey(h.ID))
	if err != nil {
		return err
	}
	since, _ := strconv.ParseInt(raw, 10, 64)
	if since == 0 {
		// A new subscriber starts from "now": replaying all history into a
		// freshly configured endpoint is never what the operator meant.
		head, err := d.St.LatestEventID()
		if err != nil {
			return err
		}
		return d.St.SetMeta(store.WebhookCursorKey(h.ID), strconv.FormatInt(head, 10))
	}

	events, err := d.St.ListEvents(store.EventFilter{
		Since: since, Types: h.Events, Limit: 100, OrderAsc: true,
	})
	if err != nil {
		return err
	}
	for _, ev := range events {
		if err := d.post(ctx, h, ev); err != nil {
			// Record the miss but still advance: one broken subscriber must not
			// stall the whole delivery stream forever.
			_ = d.St.RecordWebhookResult(h.ID, 0, err.Error())
			d.logf("webhook %s event %d failed: %v", h.URL, ev.ID, err)
		} else {
			_ = d.St.RecordWebhookResult(h.ID, http.StatusOK, "")
		}
		since = ev.ID
		if err := d.St.SetMeta(store.WebhookCursorKey(h.ID), strconv.FormatInt(since, 10)); err != nil {
			return err
		}
	}
	return nil
}

type envelope struct {
	ID        int64          `json:"id"`
	Type      string         `json:"type"`
	ActorID   string         `json:"actor_id,omitempty"`
	ActorName string         `json:"actor_name,omitempty"`
	TaskID    string         `json:"task_id,omitempty"`
	TaskCode  string         `json:"task_code,omitempty"`
	SideID    string         `json:"side_id,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	// UIURL is the deep link into the board, so a chat integration can render
	// a clickable card without knowing the front-end routing.
	UIURL string `json:"ui_url,omitempty"`
}

func (d *Dispatcher) post(ctx context.Context, h model.Webhook, ev model.Event) error {
	env := envelope{
		ID: ev.ID, Type: ev.Type, ActorID: ev.ActorID, ActorName: ev.ActorName,
		TaskID: ev.TaskID, TaskCode: ev.TaskCode, SideID: ev.SideID,
		Payload: ev.Payload, CreatedAt: ev.CreatedAt,
	}
	if ev.TaskCode != "" {
		env.UIURL = "/t/" + ev.TaskCode
	}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}

	var lastErr error
	// Three attempts with a short backoff: long enough to ride out a restart,
	// short enough not to back up the cursor.
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "keypoint-notify/1")
		req.Header.Set("X-KP-Event", ev.Type)
		req.Header.Set("X-KP-Event-Id", strconv.FormatInt(ev.ID, 10))
		req.Header.Set("X-KP-Delivery-Attempt", strconv.Itoa(attempt+1))
		if h.Secret != "" {
			req.Header.Set("X-KP-Signature", Sign(h.Secret, body))
		}
		resp, err := d.Client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			// A 4xx will not succeed on retry; surface it and move on.
			return fmt.Errorf("subscriber returned %d", resp.StatusCode)
		}
		lastErr = fmt.Errorf("subscriber returned %d", resp.StatusCode)
	}
	return lastErr
}

// Sign computes the value of the X-KP-Signature header: "sha256=<hex hmac>".
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a signature produced by Sign, in constant time.
func Verify(secret string, body []byte, header string) bool {
	want := Sign(secret, body)
	return hmac.Equal([]byte(want), []byte(strings.TrimSpace(header)))
}

func (d *Dispatcher) logf(format string, args ...any) {
	if d.OnError != nil {
		d.OnError(format, args...)
	}
}
