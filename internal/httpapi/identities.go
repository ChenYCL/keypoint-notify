package httpapi

import (
	"net/http"
	"strings"

	"github.com/ChenYCL/keypoint-notify/internal/model"
	"github.com/ChenYCL/keypoint-notify/internal/store"
)

// ---------------------------------------------------------------------------
// Identities
// ---------------------------------------------------------------------------

func (s *Server) handleIdentityList(w http.ResponseWriter, r *http.Request) {
	ids, err := s.St.ListIdentities()
	if err != nil {
		respondError(w, err)
		return
	}
	// `names=1` is the shape an agent needs when it wants to assign work:
	// a flat list of addressable names, not full records.
	if r.URL.Query().Get("names") == "1" {
		names := make([]string, 0, len(ids))
		for _, idn := range ids {
			names = append(names, idn.Name)
		}
		writeOK(w, map[string]any{"count": len(names), "names": names})
		return
	}
	writeOK(w, map[string]any{"count": len(ids), "identities": ids})
}

func (s *Server) handleIdentityCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string   `json:"name"`
		Kind       string   `json:"kind"`
		Roles      []string `json:"roles"`
		ActiveRole string   `json:"active_role"`
	}
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	caller := identity(r)
	if !caller.HasRole("admin") {
		respondError(w, NewError(http.StatusForbidden, "forbidden",
			"新建身份需要 admin 角色").
			WithHint("当前身份 %q 持有角色 %s。可以改自己的角色吗？不能——权限提升需要 admin", caller.Name, strings.Join(caller.Roles, ", ")))
		return
	}
	if !requireNonEmpty(w, "name", in.Name) {
		return
	}
	idn, key, err := s.St.CreateIdentity(in.Name, in.Kind, in.Roles, in.ActiveRole)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true, "identity": idn, "api_key": key,
		"warning": "这个 key 只显示这一次，请立刻交给使用者",
	})
}

// handleIdentityPatch is how role re-binding happens: PATCH roles:[...] and the
// identity's capabilities change without touching its API key, so no agent has
// to be re-provisioned when the org chart moves.
func (s *Server) handleIdentityPatch(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("id")
	caller := identity(r)
	if apiErr := s.allowSelfOrAdmin(caller, target); apiErr != nil {
		respondError(w, apiErr)
		return
	}
	var in struct {
		Name       *string   `json:"name"`
		Kind       *string   `json:"kind"`
		Roles      *[]string `json:"roles"`
		ActiveRole *string   `json:"active_role"`
		Disabled   *bool     `json:"disabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	// Only an admin may change roles or disable an identity: otherwise any
	// identity could grant itself every role.
	if (in.Roles != nil || in.Disabled != nil) && !caller.HasRole("admin") {
		respondError(w, NewError(http.StatusForbidden, "forbidden",
			"改角色或停用身份需要 admin 角色").
			WithHint("自己改自己的 active_role（在已有角色之间切换）不需要 admin；增减角色需要"))
		return
	}
	idn, err := s.St.UpdateIdentity(target, in.Name, in.Kind, in.Roles, in.ActiveRole, in.Disabled)
	if err != nil {
		respondError(w, err)
		return
	}
	_, _ = s.St.Emit(store.EmitInput{
		Type: store.EvIdentityUpdate, Actor: caller, Kind: "identity",
		Title:   "身份 " + idn.Name + " 已更新（角色：" + strings.Join(idn.Roles, ", ") + "）",
		Payload: map[string]any{"identity": idn.Name, "roles": idn.Roles, "active_role": idn.ActiveRole},
	})
	writeOK(w, map[string]any{"ok": true, "identity": idn})
}

func (s *Server) handleIdentityRotate(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("id")
	caller := identity(r)
	if apiErr := s.allowSelfOrAdmin(caller, target); apiErr != nil {
		respondError(w, apiErr)
		return
	}
	key, err := s.St.RotateKey(target)
	if err != nil {
		respondError(w, err)
		return
	}
	writeOK(w, map[string]any{
		"ok": true, "api_key": key,
		"warning": "旧 key 已立即失效；用这个新 key 重新运行 `kp init --key <key>` 或更新 ~/.keypoint/config.json",
	})
}

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

func (s *Server) handleRoleList(w http.ResponseWriter, r *http.Request) {
	roles, err := s.St.ListRoles()
	if err != nil {
		respondError(w, err)
		return
	}
	// `keys=1` gives the flat vocabulary an agent needs to validate a target
	// before it writes an assignment.
	if r.URL.Query().Get("keys") == "1" {
		keys := make([]string, 0, len(roles))
		for _, role := range roles {
			keys = append(keys, role.Key)
		}
		writeOK(w, map[string]any{"count": len(keys), "keys": keys})
		return
	}
	if r.URL.Query().Get("holders") == "1" {
		ids, err := s.St.ListIdentities()
		if err != nil {
			respondError(w, err)
			return
		}
		holders := map[string][]string{}
		for _, role := range roles {
			holders[role.Key] = []string{}
		}
		for _, idn := range ids {
			for _, rk := range idn.Roles {
				holders[rk] = append(holders[rk], idn.Name)
			}
		}
		writeOK(w, map[string]any{"roles": roles, "holders": holders})
		return
	}
	writeOK(w, map[string]any{"count": len(roles), "roles": roles})
}

func (s *Server) handleRoleUpsert(w http.ResponseWriter, r *http.Request) {
	var in model.Role
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	if !requireNonEmpty(w, "key", in.Key) {
		return
	}
	in.Builtin = false // the API never flips a role into builtin
	if err := s.St.UpsertRole(in); err != nil {
		respondError(w, err)
		return
	}
	roles, _ := s.St.ListRoles()
	for _, role := range roles {
		if role.Key == in.Key {
			writeOK(w, map[string]any{"ok": true, "role": role})
			return
		}
	}
	writeOK(w, map[string]any{"ok": true, "role": in})
}

func (s *Server) handleRoleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.St.DeleteRole(r.PathValue("key")); err != nil {
		if strings.Contains(err.Error(), "builtin") {
			respondError(w, NewError(http.StatusForbidden, "builtin_role",
				"%q 是内置角色，不能删除", r.PathValue("key")).
				WithHint("内置角色是路由地址，删掉会让已有指派悬空。不想要就留着不用"))
			return
		}
		respondError(w, err)
		return
	}
	writeOK(w, map[string]any{"ok": true, "deleted": r.PathValue("key")})
}

// ---------------------------------------------------------------------------
// Webhooks
// ---------------------------------------------------------------------------

func (s *Server) handleWebhookList(w http.ResponseWriter, r *http.Request) {
	hooks, err := s.St.ListWebhooks()
	if err != nil {
		respondError(w, err)
		return
	}
	for i := range hooks {
		// Secrets are write-only in listings: a reader learns that one is set,
		// not what it is.
		if hooks[i].Secret != "" {
			hooks[i].Secret = "***"
		}
	}
	writeOK(w, map[string]any{
		"count": len(hooks), "webhooks": hooks,
		"event_types": store.AllEventTypes,
		"signature":   "X-KP-Signature: sha256=<hex hmac of the raw body with the webhook secret>",
	})
}

func (s *Server) handleWebhookUpsert(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      string   `json:"id"`
		URL     string   `json:"url"`
		Secret  string   `json:"secret"`
		Events  []string `json:"events"`
		Enabled *bool    `json:"enabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	if !requireNonEmpty(w, "url", in.URL) {
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	hook, err := s.St.UpsertWebhook(in.ID, in.URL, in.Secret, in.Events, enabled)
	if err != nil {
		respondError(w, err)
		return
	}
	if hook.Secret != "" {
		hook.Secret = "***"
	}
	writeOK(w, map[string]any{"ok": true, "webhook": hook})
}

func (s *Server) handleWebhookDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.St.DeleteWebhook(r.PathValue("id")); err != nil {
		respondError(w, err)
		return
	}
	writeOK(w, map[string]any{"ok": true, "deleted": r.PathValue("id")})
}
