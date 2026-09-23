package httpapi

import (
	"net/http"
	"strings"

	"github.com/ChenYCL/keypoint-notify/internal/model"
	"github.com/ChenYCL/keypoint-notify/internal/store"
)

// ---------------------------------------------------------------------------
// POST /api/v1/tasks/{code}/reports  — the 上报 entry point
// ---------------------------------------------------------------------------

type reportCreateReq struct {
	Type        string          `json:"type"`
	Priority    string          `json:"priority"`
	Body        string          `json:"body"`
	SideKey     string          `json:"side_key"`
	Segments    []model.Segment `json:"segments"`
	Mentions    []string        `json:"mentions"`
	Attachments []string        `json:"attachments"`
	Notify      []string        `json:"notify"`
	Role        string          `json:"role"`
	// Status, when set, also moves the task. Reporting "I'm done with this
	// side" and leaving the task in `doing` is the most common bookkeeping
	// drift, so one call can do both.
	Status string `json:"status"`
}

func (s *Server) handleReportCreate(w http.ResponseWriter, r *http.Request) {
	var in reportCreateReq
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	code := r.PathValue("code")
	t, err := s.St.GetTask(code)
	if err != nil {
		respondError(w, s.taskNotFound(code, err))
		return
	}
	if err := validateEnum(w, "report_type", in.Type, reportTypeOptions()); err != nil {
		return
	}
	if in.Priority != "" {
		if err := validateEnum(w, "priority", in.Priority, model.Priorities); err != nil {
			return
		}
	}
	if in.Status != "" {
		if err := validateEnum(w, "status", in.Status, []string{model.StatusInbox, model.StatusReady,
			model.StatusDoing, model.StatusBlocked, model.StatusReview, model.StatusDone}); err != nil {
			return
		}
	}
	actor := identity(r)
	role := s.effectiveRole(actor, in.Role)

	rep, err := s.St.CreateReport(store.CreateReportInput{
		TaskCode:    t.Code,
		SideKey:     in.SideKey,
		Type:        in.Type,
		Priority:    in.Priority,
		Body:        in.Body,
		Segments:    in.Segments,
		Mentions:    in.Mentions,
		Attachments: in.Attachments,
		ActorID:     actor.ID,
		ActorName:   actor.Name,
		Role:        role,
	})
	if err != nil {
		respondError(w, err)
		return
	}

	// Mentions drive notifications; the report body is the notification text.
	notify := append([]string{}, in.Mentions...)
	notify = append(notify, in.Notify...)

	t2, _ := s.St.GetTask(t.Code)
	ev, err := s.St.Emit(store.EmitInput{
		Type: store.EvReportCreated, Actor: actor, Task: &t2, SideID: rep.SideID,
		Kind:   "report",
		Title:  reportHeadline(t.Code, rep),
		Notify: notify,
		Payload: map[string]any{
			"report_id": rep.ID, "type": rep.Type, "side": rep.SideKey,
			"role": role, "mentions": rep.Mentions,
		},
	})
	if err != nil {
		respondError(w, err)
		return
	}

	resp := map[string]any{"ok": true, "report": rep, "event_id": ev.ID}
	if in.Status != "" && in.Status != t2.Status {
		updated, changed, uerr := s.St.UpdateTask(t2.Code, store.UpdateTaskInput{Status: &in.Status}, actor.Name)
		if uerr != nil {
			respondError(w, uerr)
			return
		}
		_, _ = s.St.Emit(store.EmitInput{
			Type: store.EvTaskStatus, Actor: actor, Task: &updated, Kind: "status",
			Title:   updated.Code + " 状态 " + t2.Status + " → " + updated.Status,
			Payload: map[string]any{"changed": changed},
		})
		resp["task"] = updated
		resp["task_status_changed"] = true
	}
	if len(in.Mentions) > 0 {
		resp["notified"] = rep.Mentions
	}
	writeJSON(w, http.StatusCreated, resp)
}

// reportHeadline is the one-line inbox text for a report.
func reportHeadline(code string, rep model.Report) string {
	icon := map[string]string{
		model.ReportProgress: "进展",
		model.ReportBlocker:  "🚧 阻塞",
		model.ReportDecision: "决策",
		model.ReportHandoff:  "交接",
		model.ReportResult:   "✅ 结果",
		model.ReportQuestion: "❓ 提问",
	}[rep.Type]
	if icon == "" {
		icon = rep.Type
	}
	side := ""
	if rep.SideKey != "" {
		side = "(" + rep.SideKey + ")"
	}
	body := strings.TrimSpace(rep.Body)
	if len([]rune(body)) > 60 {
		body = string([]rune(body)[:60]) + "…"
	}
	body = strings.ReplaceAll(body, "\n", " ")
	who := rep.IdentityName
	if who == "" {
		who = "unknown"
	}
	return code + side + " " + icon + " · " + who + "：" + body
}

// ---------------------------------------------------------------------------
// GET reports
// ---------------------------------------------------------------------------

func (s *Server) handleReportList(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if _, err := s.St.GetTask(code); err != nil {
		respondError(w, s.taskNotFound(code, err))
		return
	}
	s.writeReportList(w, r, store.ReportFilter{
		TaskCode: code,
		SideKey:  strings.TrimSpace(r.URL.Query().Get("side")),
		Type:     parseCSV(r.URL.Query().Get("type")),
		Limit:    atoiOr(r.URL.Query().Get("limit"), 50),
		Offset:   atoiOr(r.URL.Query().Get("offset"), 0),
	})
}

func (s *Server) handleReportListAll(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ReportFilter{
		TaskCode: strings.TrimSpace(q.Get("task")),
		SideKey:  strings.TrimSpace(q.Get("side")),
		Type:     parseCSV(q.Get("type")),
		Limit:    atoiOr(q.Get("limit"), 50),
		Offset:   atoiOr(q.Get("offset"), 0),
	}
	if v := q.Get("since"); v != "" {
		t, err := parseTimeParam(v)
		if err != nil {
			respondError(w, err)
			return
		}
		f.Since = t
	}
	s.writeReportList(w, r, f)
}

func (s *Server) writeReportList(w http.ResponseWriter, r *http.Request, f store.ReportFilter) {
	reports, err := s.St.ListReports(f)
	if err != nil {
		respondError(w, err)
		return
	}
	resp := map[string]any{"count": len(reports), "reports": reports}
	if f.TaskCode != "" {
		resp["pack_hint"] = "kp task pack " + f.TaskCode
	}
	writeOK(w, resp)
}

// ---------------------------------------------------------------------------
// GET /api/v1/inbox, POST /api/v1/inbox/read
// ---------------------------------------------------------------------------

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	actor := identity(r)
	unreadOnly := r.URL.Query().Get("unread") == "1" || r.URL.Query().Get("unread") == "true"
	items, err := s.St.Inbox(store.InboxFilter{
		IdentityID: actor.ID,
		UnreadOnly: unreadOnly,
		Limit:      atoiOr(r.URL.Query().Get("limit"), 50),
	})
	if err != nil {
		respondError(w, err)
		return
	}
	unread, err := s.St.UnreadCount(actor.ID)
	if err != nil {
		respondError(w, err)
		return
	}
	writeOK(w, map[string]any{
		"identity": actor.Name, "role": actor.ActiveRole,
		"unread": unread, "count": len(items), "items": items,
	})
}

func (s *Server) handleInboxRead(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	actor := identity(r)
	n, err := s.St.MarkRead(actor.ID, in.IDs)
	if err != nil {
		respondError(w, err)
		return
	}
	unread, _ := s.St.UnreadCount(actor.ID)
	writeOK(w, map[string]any{
		"ok": true, "marked": n, "unread": unread,
		"note": func() string {
			if len(in.IDs) == 0 {
				return "ids 为空：已标记全部未读为已读"
			}
			return ""
		}(),
	})
}
