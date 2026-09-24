package httpapi_test

import (
	"net/http"
	"testing"
)

// Management actions must be admin-only, as SECURITY.md promises.
//
// They were not: any key — including the non-admin ones handed to agents —
// could delete another identity's task, add or remove roles, and point a
// webhook at an arbitrary URL. Found while setting up the intended layout of
// one admin key for management and a frontend key for daily work: the frontend
// key could do all three.
func TestManagementRequiresAdmin(t *testing.T) {
	h := newHarness(t)

	var fe struct {
		APIKey string `json:"api_key"`
	}
	h.do("POST", "/api/v1/identities", map[string]any{
		"name": "fe-only", "kind": "human", "roles": []string{"frontend"},
	}, &fe, http.StatusCreated)
	code := h.newTask("admin 建的任务", nil)

	forbidden := []struct {
		name, method, path string
		body               any
	}{
		{"delete task", "DELETE", "/api/v1/tasks/" + code, nil},
		{"add role", "POST", "/api/v1/roles", map[string]any{"key": "sneaky"}},
		{"delete role", "DELETE", "/api/v1/roles/sneaky", nil},
		{"add webhook", "POST", "/api/v1/webhooks", map[string]any{"url": "http://example.invalid/hook"}},
		{"delete webhook", "DELETE", "/api/v1/webhooks/whk_nope", nil},
	}
	for _, c := range forbidden {
		var e struct {
			Error string `json:"error"`
			Hint  string `json:"hint"`
		}
		h.doAs(fe.APIKey, c.method, c.path, c.body, &e, http.StatusForbidden)
		if e.Error != "forbidden" || e.Hint == "" {
			t.Errorf("%s: want forbidden with a hint, got %+v", c.name, e)
		}
	}

	// Ordinary work is untouched: the frontend key still reads and writes tasks.
	h.doAs(fe.APIKey, "POST", "/api/v1/tasks/"+code+"/reports",
		map[string]any{"type": "progress", "body": "照常干活"}, nil, http.StatusCreated)

	// And admin can still do all of it.
	h.do("POST", "/api/v1/roles", map[string]any{"key": "agent-dev", "name": "Agent 开发"}, nil, http.StatusOK)
	h.do("DELETE", "/api/v1/roles/agent-dev", nil, nil, http.StatusOK)
	h.do("DELETE", "/api/v1/tasks/"+code, nil, nil, http.StatusOK)
}
