package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/light/keypoint-notify/internal/store"
)

// ---------------------------------------------------------------------------
// GET /api/v1/events   and   GET /api/v1/tasks/{code}/events
// ---------------------------------------------------------------------------

// handleEventList is the pull side of the event stream. A polling agent passes
// back the `cursor` it received last time; the response always carries the next
// one, so a loop is `cursor = resp.cursor` with no bookkeeping.
func (s *Server) handleEventList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.EventFilter{
		Since:  parseInt64(q.Get("since")),
		Types:  parseCSV(q.Get("type")),
		Limit:  atoiOr(q.Get("limit"), 100),
		TaskID: "",
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	if code := r.PathValue("code"); code != "" {
		t, err := s.St.GetTask(code)
		if err != nil {
			respondError(w, s.taskNotFound(code, err))
			return
		}
		f.TaskID = t.ID
	} else if taskCode := q.Get("task"); taskCode != "" {
		t, err := s.St.GetTask(taskCode)
		if err != nil {
			respondError(w, s.taskNotFound(taskCode, err))
			return
		}
		f.TaskID = t.ID
	}

	// Omitting `since` means "start from now" — a fresh client should not be
	// handed the entire history by accident. Passing an explicit `since=0` is
	// different: event ids start at 1, so 0 unambiguously means "from the
	// beginning" and is honoured as a real cursor.
	if !q.Has("since") && q.Get("backlog") != "1" {
		head, _ := s.St.LatestEventID()
		writeOK(w, map[string]any{
			"count": 0, "events": []any{}, "cursor": head,
			"note": "未带 since：已同步到当前事件位点。要历史请带 backlog=1，或从 since=<cursor> 开始",
		})
		return
	}

	events, err := s.St.ListEvents(f)
	if err != nil {
		respondError(w, err)
		return
	}
	// The cursor is the newest id in the page, not the last element: a backlog
	// page arrives newest-first, so the tail is the *oldest* event and handing
	// it back would make the client re-read the whole page forever.
	cursor, _ := s.St.LatestEventID()
	for _, ev := range events {
		if ev.ID > cursor {
			cursor = ev.ID
		}
	}
	writeOK(w, map[string]any{
		"count": len(events), "events": events, "cursor": cursor,
		"has_more": len(events) == f.Limit,
	})
}

func parseInt64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// ---------------------------------------------------------------------------
// GET /api/v1/stream   — Server-Sent Events
// ---------------------------------------------------------------------------

// handleStream pushes events as they appear.
//
// It polls the events table rather than fanning out from an in-process hub on
// purpose: the cursor is durable, so a client that reconnects with
// Last-Event-ID resumes exactly where it left off, and a server restart loses
// nothing. At this system's scale the extra query per tick is irrelevant.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, NewError(http.StatusInternalServerError, "no_streaming",
			"当前连接不支持流式响应"))
		return
	}
	since := parseInt64(r.URL.Query().Get("since"))
	if v := r.Header.Get("Last-Event-ID"); v != "" && since == 0 {
		since = parseInt64(v)
	}
	if since == 0 {
		since, _ = s.St.LatestEventID()
	}
	types := parseCSV(r.URL.Query().Get("type"))
	taskFilter := ""
	if code := r.URL.Query().Get("task"); code != "" {
		if t, err := s.St.GetTask(code); err == nil {
			taskFilter = t.ID
		}
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, ": keypoint stream open, cursor=%d\n\n", since)
	flusher.Flush()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-ticker.C:
			events, err := s.St.ListEvents(store.EventFilter{
				Since: since, Types: types, TaskID: taskFilter, Limit: 100, OrderAsc: true,
			})
			if err != nil {
				Verbosef("stream query failed: %v", err)
				return
			}
			for _, ev := range events {
				payload, _ := json.Marshal(ev)
				fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, payload)
				since = ev.ID
			}
			if len(events) > 0 {
				flusher.Flush()
			}
		}
	}
}
