package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/ChenYCL/keypoint-notify/internal/model"
	"github.com/ChenYCL/keypoint-notify/internal/store"
)

// ---------------------------------------------------------------------------
// GET /api/v1/tasks
// ---------------------------------------------------------------------------

// handleTaskList is the workhorse query endpoint. Every filter is optional and
// they compose: `?role=backend&status=doing,blocked&since=2026-09-01`.
func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.TaskFilter{
		Status:          parseCSV(q.Get("status")),
		Kind:            parseCSV(q.Get("kind")),
		Priority:        parseCSV(q.Get("priority")),
		Label:           parseCSV(q.Get("label")),
		Roles:           parseCSV(q.Get("role")),
		Query:           strings.TrimSpace(q.Get("q")),
		IncludeArchived: q.Get("archived") == "1" || q.Get("archived") == "true",
		Limit:           atoiOr(q.Get("limit"), 50),
		Offset:          atoiOr(q.Get("offset"), 0),
	}
	if f.Limit > 500 {
		f.Limit = 500
	}

	// `assigned=me` means "work addressed to me, by identity or by any role I
	// hold" — the single most useful query, so it gets a shorthand. The role
	// half matters: a task handed to the frontend role is addressed to whoever
	// holds that role, even before a specific identity is named.
	switch q.Get("assigned") {
	case "me":
		idn := identity(r)
		f.AssignedTo = &store.Assignee{Identity: idn.Name, Roles: idn.Roles}
	case "":
		// no constraint
	default:
		f.AssignedTo = &store.Assignee{Identity: strings.TrimPrefix(q.Get("assigned"), "@")}
	}

	if v := q.Get("since"); v != "" {
		t, err := parseTimeParam(v)
		if err != nil {
			respondError(w, NewError(http.StatusBadRequest, "bad_time",
				"since=%q 不是合法时间", v).
				WithHint("接受 RFC3339（2026-09-01T00:00:00Z）或日期（2026-09-01），也接受相对时间 7d/24h/90m"))
			return
		}
		f.Since = t
	}
	if v := q.Get("updated_since"); v != "" {
		t, err := parseTimeParam(v)
		if err != nil {
			respondError(w, NewError(http.StatusBadRequest, "bad_time",
				"updated_since=%q 不是合法时间", v))
			return
		}
		f.UpdatedSince = t
	}

	for _, sv := range f.Status {
		if !model.ValidStatus(sv) {
			e := NewError(http.StatusBadRequest, "bad_status", "未知状态 %q", sv).
				WithOptions("status", []string{model.StatusInbox, model.StatusReady, model.StatusDoing,
					model.StatusBlocked, model.StatusReview, model.StatusDone, model.StatusArchived})
			if sug := nearest(sv, []string{model.StatusInbox, model.StatusReady, model.StatusDoing,
				model.StatusBlocked, model.StatusReview, model.StatusDone, model.StatusArchived}); sug != "" {
				e.WithSuggest(sug)
			}
			respondError(w, e)
			return
		}
	}

	tasks, err := s.St.ListTasks(f)
	if err != nil {
		respondError(w, err)
		return
	}

	if q.Get("view") == "lite" {
		for i := range tasks {
			tasks[i].Segments = nil
			for j := range tasks[i].Sides {
				tasks[i].Sides[j].Segments = nil
			}
			tasks[i].Attachments = nil
		}
	}

	// A board grouped by status is what the UI wants; a flat list is what a
	// script wants. `group=status` gives the former without a second request.
	if q.Get("group") == "status" {
		grouped := map[string][]model.Task{}
		for _, st := range []string{model.StatusInbox, model.StatusReady, model.StatusDoing,
			model.StatusBlocked, model.StatusReview, model.StatusDone} {
			grouped[st] = []model.Task{}
		}
		for _, t := range tasks {
			grouped[t.Status] = append(grouped[t.Status], t)
		}
		writeOK(w, map[string]any{"count": len(tasks), "columns": grouped})
		return
	}

	resp := map[string]any{"count": len(tasks), "tasks": tasks}
	if len(tasks) == f.Limit && f.Limit > 0 {
		resp["truncated"] = true
		resp["next"] = map[string]any{
			"hint":   "还有更多结果，用 offset 翻页",
			"offset": f.Offset + f.Limit,
			"query":  r.URL.Path + "?" + bumpOffset(q, f.Offset+f.Limit),
		}
	}
	writeOK(w, resp)
}

func bumpOffset(q map[string][]string, next int) string {
	parts := []string{}
	for k, vs := range q {
		if k == "offset" || len(vs) == 0 {
			continue
		}
		parts = append(parts, k+"="+vs[0])
	}
	parts = append(parts, "offset="+itoa(next))
	return strings.Join(parts, "&")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// parseTimeParam accepts RFC3339, a bare date, or a relative duration like 7d.
func parseTimeParam(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t, nil
	}
	if d, err := parseRelative(v); err == nil {
		return time.Now().UTC().Add(-d), nil
	}
	return time.Time{}, &APIError{Code: "bad_time", Message: v}
}

// parseRelative understands the "7d / 24h / 90m / 30s" shorthand that people
// (and models) reach for instead of computing a timestamp.
func parseRelative(v string) (time.Duration, error) {
	if len(v) < 2 {
		return 0, &APIError{Code: "bad_time", Message: v}
	}
	unit := v[len(v)-1]
	n := atoiOr(v[:len(v)-1], -1)
	if n < 0 {
		return 0, &APIError{Code: "bad_time", Message: v}
	}
	switch unit {
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'm':
		return time.Duration(n) * time.Minute, nil
	case 's':
		return time.Duration(n) * time.Second, nil
	}
	return 0, &APIError{Code: "bad_time", Message: v}
}

// ---------------------------------------------------------------------------
// POST /api/v1/tasks
// ---------------------------------------------------------------------------

type taskCreateReq struct {
	Title         string            `json:"title"`
	Kind          string            `json:"kind"`
	Priority      string            `json:"priority"`
	Status        string            `json:"status"`
	Summary       string            `json:"summary"`
	OwnerRole     string            `json:"owner_role"`
	OwnerIdentity string            `json:"owner_identity"`
	Labels        []string          `json:"labels"`
	Links         []model.Link      `json:"links"`
	Segments      map[string]string `json:"segments"`
	Sides         []sideCreateReq   `json:"sides"`
	Watchers      []string          `json:"watchers"`
	Notify        []string          `json:"notify"`
	// Role overrides the caller's active role for attribution of this write.
	Role string `json:"role"`
}

type sideCreateReq struct {
	Key              string            `json:"key"`
	Title            string            `json:"title"`
	AssigneeRole     string            `json:"assignee_role"`
	AssigneeIdentity string            `json:"assignee_identity"`
	Status           string            `json:"status"`
	Deps             []string          `json:"deps"`
	Repo             string            `json:"repo"`
	Branch           string            `json:"branch"`
	Segments         map[string]string `json:"segments"`
	Notify           []string          `json:"notify"`
}

func (s *Server) handleTaskCreate(w http.ResponseWriter, r *http.Request) {
	var in taskCreateReq
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	actor := identity(r)
	role := s.effectiveRole(actor, in.Role)

	for k := range in.Segments {
		if k == "" {
			respondError(w, NewError(http.StatusBadRequest, "bad_segment_key", "segments 里有空 key"))
			return
		}
	}
	if err := validateEnum(w, "kind", in.Kind, taskKindOptions()); err != nil {
		return
	}

	task, err := s.St.CreateTask(store.CreateTaskInput{
		Title:         in.Title,
		Kind:          in.Kind,
		Priority:      in.Priority,
		Status:        in.Status,
		Summary:       in.Summary,
		OwnerRole:     orElse(in.OwnerRole, role),
		OwnerIdentity: in.OwnerIdentity,
		Labels:        in.Labels,
		Links:         in.Links,
		Segments:      in.Segments,
		CreatedBy:     actor.Name,
		Sides:         toSideInputs(in.Sides),
	})
	if err != nil {
		respondError(w, err)
		return
	}
	if task.OwnerIdentity == "" {
		if u, _, uerr := s.St.UpdateTask(task.Code, store.UpdateTaskInput{OwnerIdentity: &actor.Name}, actor.Name); uerr == nil {
			task = u
		}
	}
	for _, wname := range in.Watchers {
		if idn, err := s.St.IdentityByName(strings.TrimPrefix(wname, "@")); err == nil {
			_ = s.St.AddWatcher(task.ID, idn.ID)
		}
	}
	_ = s.St.AddWatcher(task.ID, actor.ID)

	ev, err := s.St.Emit(store.EmitInput{
		Type:   store.EvTaskCreated,
		Actor:  actor,
		Task:   &task,
		Kind:   "task",
		Title:  "新任务 " + task.Code + " · " + task.Title,
		Notify: in.Notify,
		Payload: map[string]any{
			"code": task.Code, "kind": task.Kind, "priority": task.Priority,
			"owner_role": task.OwnerRole, "sides": sideKeys(task.Sides),
		},
	})
	if err != nil {
		respondError(w, err)
		return
	}
	Verbosef("task created %s by %s", task.Code, actor.Name)

	task, _ = s.St.GetTask(task.Code)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "task": task, "event_id": ev.ID})
}

func toSideInputs(in []sideCreateReq) []store.CreateSideInput {
	out := make([]store.CreateSideInput, 0, len(in))
	for _, sd := range in {
		out = append(out, store.CreateSideInput{
			Key: sd.Key, Title: sd.Title,
			AssigneeRole: sd.AssigneeRole, AssigneeIdentity: sd.AssigneeIdentity,
			Status: sd.Status, Deps: sd.Deps, Repo: sd.Repo, Branch: sd.Branch,
			Segments: sd.Segments,
		})
	}
	return out
}

func sideKeys(sides []model.Side) []string {
	out := make([]string, 0, len(sides))
	for _, sd := range sides {
		out = append(out, sd.Key)
	}
	return out
}

// ---------------------------------------------------------------------------
// GET/PATCH/DELETE /api/v1/tasks/{code}
// ---------------------------------------------------------------------------

func (s *Server) handleTaskGet(w http.ResponseWriter, r *http.Request) {
	t, err := s.St.GetTask(r.PathValue("code"))
	if err != nil {
		respondError(w, s.taskNotFound(r.PathValue("code"), err))
		return
	}
	// `since` on a single task answers "has anything changed since I last
	// looked?" without re-fetching the whole thing.
	if v := r.URL.Query().Get("reports"); v != "" {
		reports, err := s.St.ListReports(store.ReportFilter{TaskCode: t.Code, Limit: atoiOr(v, 10)})
		if err != nil {
			respondError(w, err)
			return
		}
		writeOK(w, map[string]any{"task": t, "reports": reports})
		return
	}
	writeOK(w, t)
}

func (s *Server) taskNotFound(codeOrID string, err error) error {
	e := NewError(http.StatusNotFound, "task_not_found", "没有任务 %q", codeOrID)
	if suggestions, lerr := s.St.ListTasks(store.TaskFilter{Limit: 200, IncludeArchived: true}); lerr == nil {
		codes := make([]string, 0, len(suggestions))
		byCode := map[string]string{}
		for _, t := range suggestions {
			codes = append(codes, t.Code)
			byCode[t.Code] = t.Title
		}
		if sug := nearest(codeOrID, codes); sug != "" {
			e.WithSuggest(sug)
			e.Message += "（你是指 " + sug + " · " + byCode[sug] + "？）"
		}
		e.WithOptions("available", codes)
		if len(codes) > 0 {
			e.WithHint("列出全部：kp task list")
		}
	}
	return e
}

func (s *Server) handleTaskPatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title         *string       `json:"title"`
		Kind          *string       `json:"kind"`
		Priority      *string       `json:"priority"`
		Status        *string       `json:"status"`
		Summary       *string       `json:"summary"`
		OwnerRole     *string       `json:"owner_role"`
		OwnerIdentity *string       `json:"owner_identity"`
		Labels        *[]string     `json:"labels"`
		Links         *[]model.Link `json:"links"`
		Role          string        `json:"role"`
		Notify        []string      `json:"notify"`
	}
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	actor := identity(r)
	before, err := s.St.GetTask(r.PathValue("code"))
	if err != nil {
		respondError(w, s.taskNotFound(r.PathValue("code"), err))
		return
	}
	task, changed, err := s.St.UpdateTask(before.Code, store.UpdateTaskInput{
		Title: in.Title, Kind: in.Kind, Priority: in.Priority, Status: in.Status,
		Summary: in.Summary, OwnerRole: in.OwnerRole, OwnerIdentity: in.OwnerIdentity,
		Labels: in.Labels, Links: in.Links,
	}, actor.Name)
	if err != nil {
		respondError(w, err)
		return
	}
	if len(changed) == 0 {
		writeOK(w, map[string]any{"ok": true, "task": task, "changed": []string{},
			"note": "没有字段发生变化"})
		return
	}

	evType := store.EvTaskUpdated
	kind := "task"
	title := task.Code + " 有更新"
	if in.Status != nil && *in.Status != before.Status {
		evType = store.EvTaskStatus
		kind = "status"
		title = task.Code + " 状态 " + before.Status + " → " + task.Status
	}
	ev, err := s.St.Emit(store.EmitInput{
		Type: evType, Actor: actor, Task: &task, Kind: kind, Title: title,
		Notify:  in.Notify,
		Payload: map[string]any{"changed": changed, "from_status": before.Status, "to_status": task.Status},
	})
	if err != nil {
		respondError(w, err)
		return
	}
	writeOK(w, map[string]any{"ok": true, "task": task, "changed": changed, "event_id": ev.ID})
}

func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	actor := identity(r)
	if !requireAdmin(w, actor, "删除任务", "只是不想要了可以归档：kp task status <code> archived") {
		return
	}
	t, err := s.St.GetTask(r.PathValue("code"))
	if err != nil {
		respondError(w, s.taskNotFound(r.PathValue("code"), err))
		return
	}
	if _, err := s.St.Emit(store.EmitInput{
		Type: store.EvTaskDeleted, Actor: actor, Task: &t,
		Kind: "task", Title: "任务已删除 " + t.Code,
	}); err != nil {
		respondError(w, err)
		return
	}
	if err := s.St.DeleteTask(t.Code); err != nil {
		respondError(w, err)
		return
	}
	writeOK(w, map[string]any{"ok": true, "deleted": t.Code})
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// effectiveRole decides which role a write is attributed to: the caller's
// explicit override when they hold it, otherwise their active role.
func (s *Server) effectiveRole(actor model.Identity, override string) string {
	override = strings.TrimSpace(override)
	if override != "" {
		if actor.HasRole(override) {
			return override
		}
		// Not held: fall back rather than fail. An agent that names the role it
		// is acting as should not be blocked by an identity re-binding.
		Verbosef("identity %s does not hold role %q; using %q", actor.Name, override, actor.ActiveRole)
	}
	if actor.ActiveRole != "" {
		return actor.ActiveRole
	}
	if len(actor.Roles) > 0 {
		return actor.Roles[0]
	}
	return "member"
}

func orElse(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func validateEnum(w http.ResponseWriter, field, value string, allowed []string) error {
	if value == "" {
		return nil
	}
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	e := NewError(http.StatusBadRequest, "bad_"+field, "未知 %s %q", field, value).
		WithOptions(field, allowed)
	if sug := nearest(value, allowed); sug != "" {
		e.WithSuggest(sug)
	}
	respondError(w, e)
	return e
}

func taskKindOptions() []string {
	return []string{model.TaskBug, model.TaskFeature, model.TaskChore, model.TaskResearch,
		model.TaskReview, model.TaskIncident}
}

func reportTypeOptions() []string {
	return []string{model.ReportProgress, model.ReportBlocker, model.ReportDecision,
		model.ReportHandoff, model.ReportResult, model.ReportQuestion, model.ReportFinding}
}

// handleMyBoard answers "what is on my plate right now" in one request: open
// sides assigned to me or my roles, plus tasks I own, plus my unread count.
func (s *Server) handleMyBoard(w http.ResponseWriter, r *http.Request) {
	actor := identity(r)
	role := actor.ActiveRole

	openSides, err := s.St.SidesForRole("", actor.Name)
	if err != nil {
		respondError(w, err)
		return
	}
	byRole, err := s.St.SidesForRole(role, "")
	if err != nil {
		respondError(w, err)
		return
	}
	owned, err := s.St.ListTasks(store.TaskFilter{Identity: actor.Name, Limit: 50})
	if err != nil {
		respondError(w, err)
		return
	}
	unread, err := s.St.UnreadCount(actor.ID)
	if err != nil {
		respondError(w, err)
		return
	}
	roleTasks, err := s.St.ListTasks(store.TaskFilter{Roles: []string{role}, Limit: 50})
	if err != nil {
		respondError(w, err)
		return
	}

	writeOK(w, map[string]any{
		"identity":     actor,
		"role":         role,
		"unread":       unread,
		"my_sides":     dedupeSides(append(openSides, byRole...)),
		"my_tasks":     owned,
		"role_tasks":   roleTasks,
		"next_actions": boardHints(role, openSides, unread),
	})
}

func dedupeSides(in []model.Side) []model.Side {
	seen := map[string]bool{}
	out := []model.Side{}
	for _, sd := range in {
		if seen[sd.ID] {
			continue
		}
		seen[sd.ID] = true
		out = append(out, sd)
	}
	return out
}

func boardHints(role string, sides []model.Side, unread int) []string {
	hints := []string{}
	if unread > 0 {
		hints = append(hints, "有未读上报，先看 `kp inbox`")
	}
	blocked := 0
	for _, sd := range sides {
		if sd.Status == model.SideBlocked {
			blocked++
		}
	}
	if blocked > 0 {
		hints = append(hints, "有 "+itoa(blocked)+" 个工作面处于阻塞，用 `kp task show <code> --side <key>` 看细节")
	}
	if len(sides) > 0 {
		hints = append(hints, "开工：`kp task pack "+sides[0].TaskID+" --side "+sides[0].Key+"`")
	}
	return hints
}

// handleWatch subscribes (POST) or unsubscribes (DELETE) the caller to a task.
//
// Watchers get every event on the task in their inbox — the way to follow work
// you do not own and are not assigned to, e.g. the upstream face you are
// waiting on. `kp wait` then turns a new inbox entry into a wake-up.
func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request) {
	actor := identity(r)
	t, err := s.St.GetTask(r.PathValue("code"))
	if err != nil {
		respondError(w, s.taskNotFound(r.PathValue("code"), err))
		return
	}
	watching := r.Method == http.MethodPost
	if watching {
		err = s.St.AddWatcher(t.ID, actor.ID)
	} else {
		err = s.St.RemoveWatcher(t.ID, actor.ID)
	}
	if err != nil {
		respondError(w, err)
		return
	}
	writeOK(w, map[string]any{"ok": true, "task": t.Code, "watching": watching})
}
