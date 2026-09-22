package httpapi

import (
	"net/http"
	"strings"

	"github.com/light/keypoint-notify/internal/model"
	"github.com/light/keypoint-notify/internal/store"
)

// ---------------------------------------------------------------------------
// GET /api/v1/tasks/{code}/sides
// ---------------------------------------------------------------------------

func (s *Server) handleSideList(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	t, err := s.St.GetTask(code)
	if err != nil {
		respondError(w, s.taskNotFound(code, err))
		return
	}
	sides := t.Sides
	if r.URL.Query().Get("assignable") == "1" {
		// Only sides with no owner yet — the dispatcher's "what still needs a
		// home" view.
		filtered := make([]model.Side, 0, len(sides))
		for _, sd := range sides {
			if sd.AssigneeRole == "" && sd.AssigneeIdentity == "" {
				filtered = append(filtered, sd)
			}
		}
		sides = filtered
	}
	writeOK(w, map[string]any{"task": t.Code, "count": len(sides), "sides": sides})
}

// ---------------------------------------------------------------------------
// POST /api/v1/tasks/{code}/sides
// ---------------------------------------------------------------------------

func (s *Server) handleSideCreate(w http.ResponseWriter, r *http.Request) {
	var in sideCreateReq
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	if strings.TrimSpace(in.Key) == "" && strings.TrimSpace(in.Title) == "" {
		respondError(w, NewError(http.StatusBadRequest, "missing_side_key",
			"需要 key 或 title 作为工作面标识").
			WithHint("惯例：api / ui / review / test / ops —— 一个任务的工作面通常 2-4 个，别拆太碎"))
		return
	}
	code := r.PathValue("code")
	t, err := s.St.GetTask(code)
	if err != nil {
		respondError(w, s.taskNotFound(code, err))
		return
	}
	actor := identity(r)
	side, err := s.St.AddSide(t.ID, store.CreateSideInput{
		Key: in.Key, Title: in.Title,
		AssigneeRole: in.AssigneeRole, AssigneeIdentity: in.AssigneeIdentity,
		Status: in.Status, Deps: in.Deps, Repo: in.Repo, Branch: in.Branch,
		Segments: in.Segments,
	}, actor.Name)
	if err != nil {
		respondError(w, err)
		return
	}
	t2, _ := s.St.GetTask(t.Code)
	title := t.Code + " 新增工作面 " + side.Key
	if side.AssigneeRole != "" {
		title += " → @" + side.AssigneeRole
	}
	ev, err := s.St.Emit(store.EmitInput{
		Type: store.EvSideCreated, Actor: actor, Task: &t2, SideID: side.ID, Kind: "side",
		Title:  title,
		Notify: in.Notify,
		Payload: map[string]any{
			"side": side.Key, "assignee_role": side.AssigneeRole, "deps": side.Deps,
		},
	})
	if err != nil {
		respondError(w, err)
		return
	}
	resp := map[string]any{"ok": true, "side": side, "event_id": ev.ID}
	if blocked := blockedBy(side, t2); len(blocked) > 0 {
		resp["blocked_by"] = blocked
	}
	resp["pack"] = "kp task pack " + t.Code + " --side " + side.Key
	writeJSON(w, http.StatusCreated, resp)
}

// ---------------------------------------------------------------------------
// PATCH /api/v1/tasks/{code}/sides/{key}
// ---------------------------------------------------------------------------

func (s *Server) handleSidePatch(w http.ResponseWriter, r *http.Request) {
	code, key := r.PathValue("code"), r.PathValue("key")
	t, err := s.St.GetTask(code)
	if err != nil {
		respondError(w, s.taskNotFound(code, err))
		return
	}
	before, err := s.St.Side(t.ID, key)
	if err != nil {
		respondError(w, s.sideNotFound(t, key))
		return
	}

	var in struct {
		Title            *string   `json:"title"`
		AssigneeRole     *string   `json:"assignee_role"`
		AssigneeIdentity *string   `json:"assignee_identity"`
		Status           *string   `json:"status"`
		Deps             *[]string `json:"deps"`
		Repo             *string   `json:"repo"`
		Branch           *string   `json:"branch"`
		Unassign         bool      `json:"unassign"`
		Notify           []string  `json:"notify"`
	}
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	if in.Status != nil {
		if err := validateEnum(w, "side_status", *in.Status,
			[]string{model.SideTodo, model.SideDoing, model.SideBlocked, model.SideDone}); err != nil {
			return
		}
	}
	actor := identity(r)
	side, err := s.St.UpdateSide(t.ID, before.Key, store.UpdateSideInput{
		Title: in.Title, AssigneeRole: in.AssigneeRole, AssigneeIdentity: in.AssigneeIdentity,
		Status: in.Status, Deps: in.Deps, Repo: in.Repo, Branch: in.Branch,
		ClearAssignee: in.Unassign,
	}, actor.Name)
	if err != nil {
		respondError(w, err)
		return
	}

	evType := store.EvSideUpdated
	title := t.Code + " 工作面 " + side.Key + " 已更新"
	if side.AssigneeRole != before.AssigneeRole || side.AssigneeIdentity != before.AssigneeIdentity {
		evType = store.EvSideAssigned
		title = t.Code + " · " + side.Key + " 指派给 @" + orElse(side.AssigneeRole, "?")
		if side.AssigneeIdentity != "" {
			title += "（" + side.AssigneeIdentity + "）"
		}
	} else if side.Status != before.Status {
		title = t.Code + " · " + side.Key + " 状态 " + before.Status + " → " + side.Status
	}
	t2, _ := s.St.GetTask(t.Code)
	ev, err := s.St.Emit(store.EmitInput{
		Type: evType, Actor: actor, Task: &t2, SideID: side.ID, Kind: "side", Title: title,
		Notify: in.Notify,
		Payload: map[string]any{
			"side": side.Key, "status": side.Status,
			"assignee_role": side.AssigneeRole, "assignee_identity": side.AssigneeIdentity,
			"deps": side.Deps,
		},
	})
	if err != nil {
		respondError(w, err)
		return
	}
	blocked := blockedBy(side, t2)
	resp := map[string]any{"ok": true, "side": side, "event_id": ev.ID}
	if len(blocked) > 0 {
		resp["blocked_by"] = blocked
	}
	writeOK(w, resp)
}

// blockedBy lists dependencies of a side that are not done yet. It is advisory
// only: the API never refuses work because a dependency is open, it just says
// so, since real teams start early on purpose.
func blockedBy(sd model.Side, t model.Task) []string {
	open := []string{}
	for _, dep := range sd.Deps {
		for _, other := range t.Sides {
			if other.Key == dep && other.Status != model.SideDone {
				open = append(open, dep+"("+other.Status+")")
			}
		}
	}
	return open
}

func (s *Server) sideNotFound(t model.Task, key string) error {
	keys := make([]string, 0, len(t.Sides))
	for _, sd := range t.Sides {
		keys = append(keys, sd.Key)
	}
	e := NewError(http.StatusNotFound, "side_not_found", "任务 %s 没有工作面 %q", t.Code, key)
	if len(keys) > 0 {
		e.WithOptions("available", keys)
		if sug := nearest(key, keys); sug != "" {
			e.WithSuggest(sug)
		}
		e.WithHint("看某个工作面：kp task pack %s --side <key>", t.Code)
	} else {
		e.WithHint("该任务还没有工作面。用 `kp task side add %s <key> --role <role>` 拆一个", t.Code)
	}
	return e
}

// ---------------------------------------------------------------------------
// DELETE
// ---------------------------------------------------------------------------

func (s *Server) handleSideDelete(w http.ResponseWriter, r *http.Request) {
	code, key := r.PathValue("code"), r.PathValue("key")
	t, err := s.St.GetTask(code)
	if err != nil {
		respondError(w, s.taskNotFound(code, err))
		return
	}
	sd, err := s.St.Side(t.ID, key)
	if err != nil {
		respondError(w, s.sideNotFound(t, key))
		return
	}
	if err := s.St.DeleteSide(t.ID, key); err != nil {
		respondError(w, err)
		return
	}
	actor := identity(r)
	t2, _ := s.St.GetTask(t.Code)
	ev, _ := s.St.Emit(store.EmitInput{
		Type: store.EvSideDeleted, Actor: actor, Task: &t2, Kind: "side",
		Title:   t.Code + " 工作面 " + sd.Key + " 已删除",
		Payload: map[string]any{"side": sd.Key},
	})
	writeOK(w, map[string]any{"ok": true, "deleted": sd.Key, "event_id": ev.ID})
}

// ---------------------------------------------------------------------------
// shared
// ---------------------------------------------------------------------------

func requireNonEmpty(w http.ResponseWriter, field, value string) bool {
	if strings.TrimSpace(value) == "" {
		respondError(w, NewError(http.StatusBadRequest, "missing_"+field, "%s 不能为空", field).
			WithOptions(field, []string{"<非空字符串>"}))
		return false
	}
	return true
}
