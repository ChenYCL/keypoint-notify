package httpapi

import (
	"net/http"
	"strings"

	"github.com/light/keypoint-notify/internal/model"
	"github.com/light/keypoint-notify/internal/store"
)

// ---------------------------------------------------------------------------
// GET /api/v1/tasks/{code}/segments
// ---------------------------------------------------------------------------

func (s *Server) handleSegmentList(w http.ResponseWriter, r *http.Request) {
	t, err := s.St.GetTask(r.PathValue("code"))
	if err != nil {
		respondError(w, s.taskNotFound(r.PathValue("code"), err))
		return
	}
	segs := append([]model.Segment{}, t.Segments...)
	// Side segments come along under side:<key> so one request still answers
	// "what is in this task", while each segment stays individually addressable.
	for _, sd := range t.Sides {
		for _, sg := range sd.Segments {
			sg.SideKey = sd.Key
			segs = append(segs, sg)
		}
	}
	if r.URL.Query().Get("nonempty") == "1" {
		filtered := segs[:0]
		for _, sg := range segs {
			if strings.TrimSpace(sg.Body) != "" {
				filtered = append(filtered, sg)
			}
		}
		segs = filtered
	}
	writeOK(w, map[string]any{
		"task": t.Code, "count": len(segs), "segments": segs,
		"hint": "取单段纯文本：kp task seg " + t.Code + " <key>",
	})
}

// ---------------------------------------------------------------------------
// GET /api/v1/tasks/{code}/segments/{key}
// ---------------------------------------------------------------------------

// handleSegmentGet returns one segment as plain text by default. This is the
// endpoint behind the UI's per-segment copy button and behind `kp task seg`:
// the body is the payload, not a JSON envelope, so `curl ... | pbcopy` works.
func (s *Server) handleSegmentGet(w http.ResponseWriter, r *http.Request) {
	code, key := r.PathValue("code"), r.PathValue("key")
	t, err := s.St.GetTask(code)
	if err != nil {
		respondError(w, s.taskNotFound(code, err))
		return
	}
	sg, err := s.St.Segment(t.ID, key)
	if err != nil {
		respondError(w, s.segmentNotFound(t, key))
		return
	}
	sg.Attachments, _ = s.St.FilesByScope("segment", sg.ID)

	switch r.URL.Query().Get("format") {
	case "json":
		writeOK(w, sg)
	case "prompt":
		// "prompt" wraps the segment in just enough framing to be pasted into a
		// fresh session on its own.
		writeText(w, http.StatusOK, "text/markdown", segmentAsPrompt(t, sg))
	default:
		writeText(w, http.StatusOK, "text/markdown", renderSegmentText(t, sg))
	}
}

func (s *Server) segmentNotFound(t model.Task, key string) error {
	keys, _ := s.St.SegmentKeys(t.ID)
	e := NewError(http.StatusNotFound, "segment_not_found",
		"任务 %s 没有分段 %q", t.Code, key).
		WithHint("单段取全文：kp task seg %s <key>；列全部：kp task show %s", t.Code, t.Code)
	if len(keys) > 0 {
		e.WithOptions("available", keys)
		if sug := nearest(key, keys); sug != "" {
			e.WithSuggest(sug)
		}
	}
	return e
}

// renderSegmentText is the canonical single-segment rendering: a short
// provenance header, then the body verbatim.
func renderSegmentText(t model.Task, sg model.Segment) string {
	var sb strings.Builder
	scope := ""
	if sg.SideKey != "" {
		scope = " · side:" + sg.SideKey
	}
	sb.WriteString("<!-- " + t.Code + " · [" + sg.Key + "] " + sg.Title + scope + " -->\n")
	sb.WriteString(strings.TrimRight(sg.Body, "\n"))
	sb.WriteString("\n")
	return sb.String()
}

func segmentAsPrompt(t model.Task, sg model.Segment) string {
	var sb strings.Builder
	sb.WriteString("以下是任务 " + t.Code + "（" + t.Title + "）的「" + sg.Title + "」分段内容。")
	sb.WriteString("请基于它继续工作，完成后用 `kp report " + t.Code + " --type result -m \"...\"` 上报。\n\n")
	sb.WriteString(strings.TrimRight(sg.Body, "\n"))
	sb.WriteString("\n")
	return sb.String()
}

// ---------------------------------------------------------------------------
// POST/DELETE segments
// ---------------------------------------------------------------------------

type segmentUpsertReq struct {
	Key     string   `json:"key"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	SideKey string   `json:"side_key"`
	Format  string   `json:"format"`
	Append  bool     `json:"append"`
	Notify  []string `json:"notify"`
}

func (s *Server) handleSegmentUpsert(w http.ResponseWriter, r *http.Request) {
	var in segmentUpsertReq
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	if strings.TrimSpace(in.Key) == "" && strings.TrimSpace(in.Title) == "" {
		respondError(w, NewError(http.StatusBadRequest, "missing_key",
			"需要 key 或 title 作为分段标识").
			WithHint("固定骨架分段的 key 有：%s；自由分段用 title，会自动生成 key",
				strings.Join(model.SkeletonKeys, ", ")).
			WithOptions("skeleton_keys", model.SkeletonKeys))
		return
	}
	code := r.PathValue("code")
	t, err := s.St.GetTask(code)
	if err != nil {
		respondError(w, s.taskNotFound(code, err))
		return
	}
	actor := identity(r)
	key := in.Key
	if strings.TrimSpace(key) == "" {
		key = model.Slug(in.Title)
	}
	// A near-miss on a skeleton key is almost always a typo, not an intent to
	// create a new segment: `accepatnce` should not silently become a new
	// section next to `acceptance`.
	if in.SideKey == "" && !model.IsSkeletonKey(key) {
		if sug := nearest(key, model.SkeletonKeys); sug != "" {
			respondError(w, NewError(http.StatusBadRequest, "near_miss_skeleton_key",
				"%q 与固定分段 %q 只差一点，按现状会新建一个重复分段", key, sug).
				WithSuggest(sug).
				WithHint("确认要用固定分段请改成 %q；确实要新建自由分段请显式传 side_key 或改一个明显不同的 key", sug).
				WithOptions("skeleton_keys", model.SkeletonKeys))
			return
		}
	}

	sg, err := s.St.UpsertSegment(t.ID, store.UpsertSegmentInput{
		Key: key, SideKey: in.SideKey, Title: in.Title, Body: in.Body,
		Format: in.Format, Append: in.Append,
	}, actor.Name)
	if err != nil {
		respondError(w, err)
		return
	}
	t2, _ := s.St.GetTask(t.Code)
	ev, err := s.St.Emit(store.EmitInput{
		Type: store.EvSegmentSet, Actor: actor, Task: &t2, Kind: "segment",
		Title:   t.Code + " 分段 [" + sg.Key + "] 已更新",
		Notify:  in.Notify,
		Payload: map[string]any{"segment": sg.Key, "side": sg.SideKey, "empty": sg.Empty},
	})
	if err != nil {
		respondError(w, err)
		return
	}
	writeOK(w, map[string]any{"ok": true, "segment": sg, "event_id": ev.ID})
}

func (s *Server) handleSegmentDelete(w http.ResponseWriter, r *http.Request) {
	code, key := r.PathValue("code"), r.PathValue("key")
	t, err := s.St.GetTask(code)
	if err != nil {
		respondError(w, s.taskNotFound(code, err))
		return
	}
	sg, err := s.St.Segment(t.ID, key)
	if err != nil {
		respondError(w, s.segmentNotFound(t, key))
		return
	}
	if err := s.St.DeleteSegment(t.ID, key); err != nil {
		respondError(w, err)
		return
	}
	actor := identity(r)
	t2, _ := s.St.GetTask(t.Code)
	ev, _ := s.St.Emit(store.EmitInput{
		Type: store.EvSegmentDeleted, Actor: actor, Task: &t2, Kind: "segment",
		Title:   t.Code + " 分段 [" + sg.Key + "] 已清空/删除",
		Payload: map[string]any{"segment": sg.Key, "was_skeleton": sg.Kind == "skeleton"},
	})
	resp := map[string]any{"ok": true, "deleted": sg.Key}
	if sg.Kind == "skeleton" {
		resp["note"] = "固定分段不会被删除，只清空正文（key 保持可取）"
	}
	if ev.ID != 0 {
		resp["event_id"] = ev.ID
	}
	writeOK(w, resp)
}
