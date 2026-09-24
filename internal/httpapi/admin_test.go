package httpapi_test

import (
	"net/http"
	"strings"
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

// Deleting a task must not leave inbox entries that link to it.
//
// Seen on UAT: the admin's inbox still listed "KP-14 进展 · …" after KP-14 was
// deleted; clicking it opened the task page, which sat on "加载中…" with a
// task_not_found toast. Only the deletion notice should survive, and it should
// not link anywhere.
func TestDeletedTaskLeavesNoDeadInboxLinks(t *testing.T) {
	h := newHarness(t)

	var other struct {
		APIKey string `json:"api_key"`
	}
	h.do("POST", "/api/v1/identities", map[string]any{
		"name": "reporter", "kind": "agent", "roles": []string{"backend"},
	}, &other, http.StatusCreated)

	code := h.newTask("会被删掉的任务", nil) // admin owns it, so admin is notified
	h.doAs(other.APIKey, "POST", "/api/v1/tasks/"+code+"/reports",
		map[string]any{"type": "progress", "body": "进展"}, nil, http.StatusCreated)

	type inbox struct {
		Items []struct {
			TaskCode string `json:"task_code"`
			URL      string `json:"url"`
			Title    string `json:"title"`
		} `json:"items"`
	}
	var before inbox
	h.do("GET", "/api/v1/inbox", nil, &before, http.StatusOK)
	if len(before.Items) == 0 {
		t.Fatal("setup: the owner should have been notified of the report")
	}

	h.do("DELETE", "/api/v1/tasks/"+code, nil, nil, http.StatusOK)

	var after inbox
	h.do("GET", "/api/v1/inbox", nil, &after, http.StatusOK)
	for _, it := range after.Items {
		if it.TaskCode != code {
			continue
		}
		if it.URL != "" {
			t.Errorf("inbox entry %q still links to the deleted task (%s)", it.Title, it.URL)
		}
		if !strings.Contains(it.Title, "删除") {
			t.Errorf("only the deletion notice should survive, found %q", it.Title)
		}
	}
}
