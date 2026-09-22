package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/light/keypoint-notify/internal/model"
)

// Verbosef writes a diagnostic line when KEYPOINT_VERBOSE is set. The server
// never logs request bodies, only method/path/status.
func Verbosef(format string, args ...any) {
	if os.Getenv("KEYPOINT_VERBOSE") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "[keypoint] "+format+"\n", args...)
}

// ---------------------------------------------------------------------------
// Authentication
// ---------------------------------------------------------------------------

// keyFromRequest extracts an API key from the Authorization header, the
// X-API-Key header, or the UI session cookie. All three spellings are accepted
// because LLM-generated clients reach for whichever one they saw most
// recently, and the browser cannot set headers on a navigation.
func keyFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if after, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
		if strings.HasPrefix(h, "kp_") {
			return strings.TrimSpace(h)
		}
	}
	if k := r.Header.Get("X-API-Key"); k != "" {
		return strings.TrimSpace(k)
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}

func contextWithIdentity(r *http.Request, idn model.Identity) context.Context {
	return context.WithValue(r.Context(), identityKey, idn)
}

// requireAuth rejects unauthenticated requests with an error that explains how
// to authenticate, including the bootstrap path when the system is still empty.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := keyFromRequest(r)
		if key == "" {
			n, _ := s.St.CountIdentities()
			e := NewError(http.StatusUnauthorized, "missing_api_key", "缺少 API key")
			if n == 0 {
				e.Hint = "系统还没有任何身份。先运行 `kp init`，或 POST /api/v1/bootstrap 创建第一个身份"
			} else {
				e.Hint = "请求头需要 Authorization: Bearer kp_... 或 X-API-Key: kp_...；本地配置可用 `kp whoami` 查看"
			}
			respondError(w, e)
			return
		}
		idn, err := s.St.IdentityByKey(key)
		if err != nil {
			respondError(w, NewError(http.StatusUnauthorized, "invalid_api_key", "API key 无效或已失效").
				WithHint("key 可能已被轮换（轮换会让旧 key 立即失效）。重新运行 `kp init` 绑定").
				WithOptions("Authorization", []string{"Bearer kp_..."}))
			return
		}
		next.ServeHTTP(w, r.WithContext(contextWithIdentity(r, idn)))
	})
}

// allowSelfOrAdmin guards identity-level writes: an identity may always edit
// itself, and anyone holding the admin role may edit anyone. This is the only
// role-based authorization rule in the system, which keeps it auditable.
func (s *Server) allowSelfOrAdmin(caller model.Identity, targetID string) *APIError {
	if caller.ID == targetID || caller.HasRole("admin") {
		return nil
	}
	return NewError(http.StatusForbidden, "forbidden",
		"只能修改自己的身份；修改他人需要 admin 角色").
		WithHint("当前身份 %q 持有角色 %s", caller.Name, strings.Join(caller.Roles, ", "))
}

// ---------------------------------------------------------------------------
// Session (browser convenience)
// ---------------------------------------------------------------------------

// The UI is served to a browser that cannot set an Authorization header on a
// navigation, so POST /api/v1/session exchanges a key for an HttpOnly cookie.
// The key is verified before the cookie is minted and the cookie value is the
// key itself: the same secret, no weaker, but never readable by JavaScript.

const sessionCookie = "kp_session"

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key string `json:"key"`
	}
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	in.Key = strings.TrimSpace(in.Key)
	idn, err := s.St.IdentityByKey(in.Key)
	if err != nil {
		respondError(w, NewError(http.StatusUnauthorized, "invalid_api_key", "API key 无效").
			WithHint("确认复制完整；轮换过 key 的话旧值已失效"))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    in.Key,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure whenever the request arrived over TLS — which is the case
		// behind the Cloudflare tunnel — and off for plain localhost so the
		// cookie is not silently dropped during local use.
		Secure:  r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		Expires: time.Now().Add(90 * 24 * time.Hour),
	})
	writeOK(w, map[string]any{"identity": idn, "ok": true})
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
	writeOK(w, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// Bootstrap
// ---------------------------------------------------------------------------

// bootstrapMu serialises first-run identity creation so two concurrent clients
// cannot both claim the initial admin key.
var bootstrapMu sync.Mutex

// handleBootstrap claims the owner identity.
//
// Routed but narrow: it works while the system is empty, and — importantly —
// also when the system has identities but no *enabled admin* left. That second
// case is a real lockout: every privileged endpoint requires admin, nothing in
// the API can grant admin, and losing the last admin's key would otherwise
// brick the instance with no way back short of editing the database by hand.
//
// It is not a hole: reaching it still requires network access to the server,
// which is the same trust level as owning the data directory.
func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  string   `json:"name"`
		Kind  string   `json:"kind"`
		Roles []string `json:"roles"`
	}
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()

	n, err := s.St.CountIdentities()
	if err != nil {
		respondError(w, err)
		return
	}
	admins, err := s.St.AdminCount()
	if err != nil {
		respondError(w, err)
		return
	}
	recovery := n > 0 && admins == 0
	if n > 0 && !recovery {
		respondError(w, NewError(http.StatusConflict, "already_bootstrapped",
			"系统已初始化过，bootstrap 只对空系统（或没有可用 admin 的系统）开放").
			WithHint("已有身份的话用 `kp identity create`（需要 admin）或 POST /api/v1/identities").
			WithOptions("alternative", []string{"kp init", "POST /api/v1/session"}))
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = "owner"
	}
	if in.Kind == "" {
		in.Kind = model.KindHuman
	}
	roles := in.Roles
	if len(roles) == 0 {
		roles = []string{"admin", "member", "backend", "frontend", "review", "qa", "ops", "design"}
	}
	idn, key, err := s.St.CreateIdentity(in.Name, in.Kind, roles, "admin")
	if err != nil {
		respondError(w, err)
		return
	}
	resp := map[string]any{
		"identity": idn,
		"api_key":  key,
		"warning":  "这个 key 只显示这一次，请立刻保存",
		"next":     []string{"kp init --key " + key, "或写进 ~/.keypoint/config.json"},
	}
	if recovery {
		resp["recovery"] = true
		resp["note"] = "系统里已经没有任何可用的 admin，这是一次恢复性初始化。新身份拿到了 admin。"
	}
	writeJSON(w, http.StatusCreated, resp)
}
