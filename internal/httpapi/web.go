package httpapi

import (
	"io/fs"
	"net/http"
	"strings"
)

// handleUI serves the embedded single-page board.
//
// There is no build step and no bundler: the UI is a handful of files read
// straight from the binary's embedded filesystem. That is a deliberate trade —
// the board is a view over the same JSON API every other client uses, so
// keeping it dependency-free is worth more than a component framework.
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if s.Web == nil {
		writeText(w, http.StatusNotFound, "text/plain",
			"UI not embedded in this build.\nAPI docs: /api/v1/llms.txt\n")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" || path == "index.html" {
		s.serveIndex(w, r)
		return
	}

	// Never let an API-shaped path fall through to the SPA shell. A client that
	// probes /api/v1/... or /skill/... and gets 200 + HTML will conclude the
	// endpoint exists and try to parse it; an honest 404 tells it the truth.
	// This is how a stale server got mistaken for a current one during a deploy.
	if isMachinePath(path) {
		http.NotFound(w, r)
		return
	}

	data, err := fs.ReadFile(s.Web, path)
	if err != nil {
		// Unknown paths are SPA routes (/t/KP-12, /inbox, /admin): hand back
		// the shell and let the client router decide. A request that looks like
		// an asset but is not embedded gets an honest 404 instead of HTML.
		if looksLikeAsset(path) || isMachinePath(path) {
			http.NotFound(w, r)
			return
		}
		s.serveIndex(w, r)
		return
	}
	w.Header().Set("Content-Type", contentTypeFor(path))
	// The assets ship inside the binary, so they only change when the binary
	// does — but during development the binary is rebuilt constantly, so a
	// short revalidate beats a stale board.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(s.Web, "index.html")
	if err != nil {
		writeText(w, http.StatusInternalServerError, "text/plain", "index.html missing from build\n")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

func looksLikeAsset(path string) bool {
	i := strings.LastIndexByte(path, '.')
	if i < 0 {
		return false
	}
	switch path[i:] {
	case ".js", ".css", ".png", ".jpg", ".jpeg", ".svg", ".ico", ".json", ".woff2", ".map", ".webmanifest":
		return true
	}
	return false
}

func contentTypeFor(path string) string {
	switch {
	case strings.HasSuffix(path, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(path, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(path, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(path, ".json"), strings.HasSuffix(path, ".webmanifest"):
		return "application/json; charset=utf-8"
	case strings.HasSuffix(path, ".png"):
		return "image/png"
	case strings.HasSuffix(path, ".jpg"), strings.HasSuffix(path, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(path, ".ico"):
		return "image/x-icon"
	}
	return "application/octet-stream"
}

// isMachinePath reports whether a path is meant for a program rather than a
// browser. These must never be answered with the SPA shell: a 200 with an HTML
// body is indistinguishable from success to `curl -f`, to a health probe, or to
// an agent deciding whether an endpoint exists.
func isMachinePath(path string) bool {
	return strings.HasPrefix(path, "api/") || strings.HasPrefix(path, "skill/") ||
		path == "api" || path == "skill"
}
