package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ChenYCL/keypoint-notify/internal/httpapi"
	"github.com/ChenYCL/keypoint-notify/internal/skill"
)

// Watching a task routes its activity into the watcher's inbox, and unwatching
// stops it. This is what `kp watch` + `kp wait` build "tell me when it moves" on.
func TestWatchRoutesActivityToInbox(t *testing.T) {
	h := newHarness(t)
	var fe, be struct {
		APIKey string `json:"api_key"`
	}
	h.do("POST", "/api/v1/identities", map[string]any{"name": "fe", "roles": []string{"frontend"}}, &fe, http.StatusCreated)
	h.do("POST", "/api/v1/identities", map[string]any{"name": "be", "roles": []string{"backend"}}, &be, http.StatusCreated)
	code := h.newTask("上游接口", nil)

	unread := func() int {
		var r struct {
			Unread int `json:"unread"`
		}
		h.doAs(fe.APIKey, "GET", "/api/v1/inbox?unread=1", nil, &r, http.StatusOK)
		return r.Unread
	}

	h.doAs(fe.APIKey, "POST", "/api/v1/tasks/"+code+"/watch", nil, nil, http.StatusOK)
	before := unread()
	h.doAs(be.APIKey, "POST", "/api/v1/tasks/"+code+"/reports", map[string]any{"body": "接口定了"}, nil, http.StatusCreated)
	if unread() <= before {
		t.Fatal("a watcher should be notified of activity on the task")
	}

	h.doAs(fe.APIKey, "DELETE", "/api/v1/tasks/"+code+"/watch", nil, nil, http.StatusOK)
	before = unread()
	h.doAs(be.APIKey, "POST", "/api/v1/tasks/"+code+"/reports", map[string]any{"body": "又改了"}, nil, http.StatusCreated)
	if unread() != before {
		t.Error("after unwatch the task's activity should stop arriving")
	}
}

// peek=1 must not advance the caller's stored next-cursor. `kp wait` peeks on
// the session's behalf; if it consumed the cursor, the mention that woke the
// session would be gone by the time the session asked kp next.
func TestPeekDoesNotConsumeTheCursor(t *testing.T) {
	h := newHarness(t)
	_, keys := setupRelay(t, h)
	code := h.newTask("有人要问 review", nil)
	h.do("POST", "/api/v1/tasks/"+code+"/reports", map[string]any{
		"type": "question", "body": "这样改可以吗", "mentions": []string{"@review"},
	}, nil, http.StatusCreated)

	peeked := h.nextAs(keys["review"], "?wait=1&peek=1")
	if peeked.Work == nil || peeked.Work.Reason != "mention" {
		t.Fatalf("peek should see the new mention, got %+v", peeked.Work)
	}
	after := h.nextAs(keys["review"], "?wait=1")
	if after.Work == nil || after.Work.Reason != "mention" {
		t.Errorf("the mention was swallowed by the peek: %+v", after.Work)
	}
}

// Releasing a claim clears only the claimant: the face stays routed to its role
// so another session of that role can take it. (`--unassign` clears both, and
// then nobody is ever offered the face.)
func TestReleaseKeepsTheRole(t *testing.T) {
	h := newHarness(t)
	_, keys := setupRelay(t, h)
	code := h.newTask("可以放手的活", map[string]any{
		"segments": map[string]string{"goal": "g"},
		"sides":    []map[string]any{{"key": "api", "assignee_role": "backend"}},
	})
	h.doAs(keys["backend"], "POST", "/api/v1/tasks/"+code+"/sides/api/claim", nil, nil, http.StatusOK)

	var raw struct {
		Side struct {
			AssigneeRole     string `json:"assignee_role"`
			AssigneeIdentity string `json:"assignee_identity"`
			Status           string `json:"status"`
		} `json:"side"`
	}
	h.doAs(keys["backend"], "PATCH", "/api/v1/tasks/"+code+"/sides/api",
		map[string]any{"assignee_identity": "", "status": "todo"}, &raw, http.StatusOK)
	if raw.Side.AssigneeIdentity != "" || raw.Side.AssigneeRole != "backend" || raw.Side.Status != "todo" {
		t.Fatalf("release should leave @backend / todo / no claimant, got %+v", raw.Side)
	}
	again := h.nextAs(keys["backend"], "?task="+code+"&claim=1")
	if again.Work == nil {
		t.Error("a released face should be offered to its role again")
	}
}

// The command skills are served next to the playbook, and the old unprefixed
// paths still mean the playbook.
func TestSkillIndexAndCommandSkills(t *testing.T) {
	httpapi.SetSkillFS(skill.FS)
	t.Cleanup(func() { httpapi.SetSkillFS(nil) })
	h := newHarness(t)

	var idx struct {
		Main   string              `json:"main"`
		Skills map[string][]string `json:"skills"`
	}
	body := h.text("/skill/index.json", http.StatusOK)
	if err := json.Unmarshal([]byte(body), &idx); err != nil {
		t.Fatalf("index.json: %v\n%s", err, body)
	}
	for _, want := range []string{"keypoint-notify", "kp-next", "kp-done", "kp-watch", "kp-loop"} {
		if _, ok := idx.Skills[want]; !ok {
			t.Errorf("index is missing %s", want)
		}
	}
	if !strings.Contains(h.text("/skill/kp-next/SKILL.md", http.StatusOK), "name: kp-next") {
		t.Error("/skill/kp-next/SKILL.md should serve the command skill")
	}
	if !strings.Contains(h.text("/skill/SKILL.md", http.StatusOK), "name: \"keypoint-notify\"") {
		t.Error("/skill/SKILL.md should still serve the playbook for older clients")
	}
	if !strings.Contains(h.text("/skill/reference/commands.md", http.StatusOK), "kp") {
		t.Error("/skill/reference/commands.md should still resolve into the playbook")
	}
}
