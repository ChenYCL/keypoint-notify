package httpapi

import (
	"context"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/light/keypoint-notify/internal/model"
	"github.com/light/keypoint-notify/internal/store"
)

// Server wires the store to an HTTP handler.
type Server struct {
	St      *store.Store
	Version string
	Web     fs.FS // embedded UI, may be nil in tests
	started time.Time
}

// New builds a server around a store. webFS is the UI bundle served at "/".
func New(st *store.Store, version string, webFS fs.FS) *Server {
	return &Server{St: st, Version: version, Web: webFS, started: time.Now()}
}

// Handler returns the fully-routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// --- unauthenticated -------------------------------------------------
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("POST /api/v1/bootstrap", s.handleBootstrap)
	mux.HandleFunc("POST /api/v1/session", s.handleSession)
	mux.HandleFunc("DELETE /api/v1/session", s.handleSessionDelete)
	mux.HandleFunc("GET /api/v1/llms.txt", s.handleLLMsTxt)
	mux.HandleFunc("GET /install.sh", s.handleInstallScript)
	mux.HandleFunc("GET /kp", s.handleBinary)
	mux.HandleFunc("GET /api/v1/skill", s.handleSkill)
	mux.HandleFunc("GET /skill", s.handleSkill)
	mux.HandleFunc("GET /skill/{path...}", s.handleSkill)
	mux.HandleFunc("GET /api/v1/schema", s.handleSchema)

	// --- authenticated ---------------------------------------------------
	auth := s.requireAuth
	mux.Handle("GET /api/v1/whoami", auth(http.HandlerFunc(s.handleWhoami)))
	mux.Handle("GET /api/v1/agent-prompt", auth(http.HandlerFunc(s.handleAgentPrompt)))
	mux.Handle("POST /api/v1/agent-prompt", auth(http.HandlerFunc(s.handleAgentPrompt)))

	mux.Handle("GET /api/v1/tasks", auth(http.HandlerFunc(s.handleTaskList)))
	mux.Handle("POST /api/v1/tasks", auth(http.HandlerFunc(s.handleTaskCreate)))
	mux.Handle("GET /api/v1/tasks/{code}", auth(http.HandlerFunc(s.handleTaskGet)))
	mux.Handle("PATCH /api/v1/tasks/{code}", auth(http.HandlerFunc(s.handleTaskPatch)))
	mux.Handle("DELETE /api/v1/tasks/{code}", auth(http.HandlerFunc(s.handleTaskDelete)))
	mux.Handle("GET /api/v1/tasks/{code}/pack", auth(http.HandlerFunc(s.handleTaskPack)))
	mux.Handle("GET /api/v1/tasks/{code}/segments", auth(http.HandlerFunc(s.handleSegmentList)))
	mux.Handle("POST /api/v1/tasks/{code}/segments", auth(http.HandlerFunc(s.handleSegmentUpsert)))
	mux.Handle("GET /api/v1/tasks/{code}/segments/{key}", auth(http.HandlerFunc(s.handleSegmentGet)))
	mux.Handle("DELETE /api/v1/tasks/{code}/segments/{key}", auth(http.HandlerFunc(s.handleSegmentDelete)))
	mux.Handle("GET /api/v1/tasks/{code}/sides", auth(http.HandlerFunc(s.handleSideList)))
	mux.Handle("POST /api/v1/tasks/{code}/sides", auth(http.HandlerFunc(s.handleSideCreate)))
	mux.Handle("PATCH /api/v1/tasks/{code}/sides/{key}", auth(http.HandlerFunc(s.handleSidePatch)))
	mux.Handle("DELETE /api/v1/tasks/{code}/sides/{key}", auth(http.HandlerFunc(s.handleSideDelete)))
	mux.Handle("POST /api/v1/tasks/{code}/sides/{key}/claim", auth(http.HandlerFunc(s.handleSideClaim)))
	mux.Handle("GET /api/v1/tasks/{code}/reports", auth(http.HandlerFunc(s.handleReportList)))
	mux.Handle("POST /api/v1/tasks/{code}/reports", auth(http.HandlerFunc(s.handleReportCreate)))
	mux.Handle("GET /api/v1/tasks/{code}/events", auth(http.HandlerFunc(s.handleEventList)))

	mux.Handle("GET /api/v1/reports", auth(http.HandlerFunc(s.handleReportListAll)))
	mux.Handle("GET /api/v1/inbox", auth(http.HandlerFunc(s.handleInbox)))
	mux.Handle("POST /api/v1/inbox/read", auth(http.HandlerFunc(s.handleInboxRead)))
	mux.Handle("GET /api/v1/events", auth(http.HandlerFunc(s.handleEventList)))
	mux.Handle("GET /api/v1/stream", auth(http.HandlerFunc(s.handleStream)))
	mux.Handle("GET /api/v1/me/board", auth(http.HandlerFunc(s.handleMyBoard)))
	mux.Handle("GET /api/v1/me/next", auth(http.HandlerFunc(s.handleNext)))

	mux.Handle("POST /api/v1/files", auth(http.HandlerFunc(s.handleFileUpload)))
	mux.Handle("GET /api/v1/files/{id}", auth(http.HandlerFunc(s.handleFileGet)))
	mux.Handle("POST /api/v1/tasks/{code}/files", auth(http.HandlerFunc(s.handleFileUploadToTask)))

	mux.Handle("GET /api/v1/identities", auth(http.HandlerFunc(s.handleIdentityList)))
	mux.Handle("POST /api/v1/identities", auth(http.HandlerFunc(s.handleIdentityCreate)))
	mux.Handle("PATCH /api/v1/identities/{id}", auth(http.HandlerFunc(s.handleIdentityPatch)))
	mux.Handle("POST /api/v1/identities/{id}/rotate", auth(http.HandlerFunc(s.handleIdentityRotate)))

	mux.Handle("GET /api/v1/roles", auth(http.HandlerFunc(s.handleRoleList)))
	mux.Handle("POST /api/v1/roles", auth(http.HandlerFunc(s.handleRoleUpsert)))
	mux.Handle("DELETE /api/v1/roles/{key}", auth(http.HandlerFunc(s.handleRoleDelete)))

	mux.Handle("GET /api/v1/webhooks", auth(http.HandlerFunc(s.handleWebhookList)))
	mux.Handle("POST /api/v1/webhooks", auth(http.HandlerFunc(s.handleWebhookUpsert)))
	mux.Handle("DELETE /api/v1/webhooks/{id}", auth(http.HandlerFunc(s.handleWebhookDelete)))

	// --- UI --------------------------------------------------------------
	mux.HandleFunc("GET /", s.handleUI)

	return s.withCORS(s.withLogging(mux))
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

type ctxKey int

const identityKey ctxKey = iota

// IdentityFrom pulls the authenticated identity out of a request context.
func IdentityFrom(ctx context.Context) (model.Identity, bool) {
	v, ok := ctx.Value(identityKey).(model.Identity)
	return v, ok
}

// identity returns the caller, panicking only if the auth middleware was
// bypassed — which would be a routing bug, not a user error.
func identity(r *http.Request) model.Identity {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		panic("handler reached without auth middleware")
	}
	return id
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		// Health checks and SSE ticks are noise; everything else is worth a line.
		if r.URL.Path == "/api/v1/health" {
			return
		}
		Verbosef("%s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// withCORS keeps browser-based tools on other origins usable. The API is
// token-authenticated and never cookie-only for cross-origin calls, so a
// permissive origin policy does not hand out authority.
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, X-API-Key, Content-Type, Idempotency-Key")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// Flush lets SSE responses through the recorder.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	n, _ := s.St.CountIdentities()
	admins, _ := s.St.AdminCount()
	writeOK(w, map[string]any{
		"status":       "ok",
		"version":      s.Version,
		"uptime_s":     int(time.Since(s.started).Seconds()),
		"bootstrapped": n > 0,
		// Zero admins on a bootstrapped system means nobody can administer it.
		// A client that notices should offer the recovery path instead of
		// leaving the operator to discover the lockout one 403 at a time.
		"admins":      admins,
		"server_time": time.Now().UTC().Format(time.RFC3339),
		"docs":        "/api/v1/llms.txt",
	})
}

// parseCSV splits a comma-separated query parameter.
func parseCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func atoiOr(s string, def int) int {
	n := 0
	if s == "" {
		return def
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return def
	}
	return n
}
