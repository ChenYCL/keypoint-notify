package httpapi_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// The collaboration loop: three role-sessions relaying one task.
// ---------------------------------------------------------------------------

// setupRelay builds a task with three chained work faces and returns its code
// plus one key per role.
func setupRelay(t *testing.T, h *harness) (code string, keys map[string]string) {
	t.Helper()
	keys = map[string]string{}
	for _, r := range []string{"backend", "frontend", "review"} {
		var out struct {
			APIKey string `json:"api_key"`
		}
		h.do("POST", "/api/v1/identities", map[string]any{
			"name": r + "-sess", "kind": "agent", "roles": []string{r},
		}, &out, http.StatusCreated)
		keys[r] = out.APIKey
	}
	var created struct {
		Task struct {
			Code string `json:"code"`
		} `json:"task"`
	}
	h.do("POST", "/api/v1/tasks", map[string]any{
		"title": "彩排：倒计时端到端修复", "kind": "bug",
		"sides": []map[string]any{
			{"key": "api", "assignee_role": "backend"},
			{"key": "ui", "assignee_role": "frontend", "deps": []string{"api"}},
			{"key": "review", "assignee_role": "review", "deps": []string{"ui"}},
		},
	}, &created, http.StatusCreated)
	return created.Task.Code, keys
}

type nextResp struct {
	Work *struct {
		Reason string `json:"reason"`
		Task   struct {
			Code string `json:"code"`
		} `json:"task"`
		Side *struct {
			Key              string `json:"key"`
			Status           string `json:"status"`
			AssigneeRole     string `json:"assignee_role"`
			AssigneeIdentity string `json:"assignee_identity"`
		} `json:"side"`
	} `json:"work"`
	Cursor  int64  `json:"cursor"`
	Claimed *bool  `json:"claimed"`
	Note    string `json:"hint"`
}

func (h *harness) nextAs(key, query string) nextResp {
	h.t.Helper()
	if !strings.Contains(query, "format=") {
		query += "&format=json"
	}
	var out nextResp
	h.doAs(key, "GET", "/api/v1/me/next"+query, nil, &out, http.StatusOK)
	return out
}

// The relay: each role session must be handed its own work face, in order, and
// only after its dependency is done.
func TestRelayHandsOffThroughRoles(t *testing.T) {
	h := newHarness(t)
	code, keys := setupRelay(t, h)

	// Backend goes first: its face has no dependencies.
	w := h.nextAs(keys["backend"], "?task="+code+"&claim=1")
	if w.Work == nil || w.Work.Side == nil || w.Work.Side.Key != "api" {
		t.Fatalf("backend should be handed the api face, got %+v", w.Work)
	}
	if w.Work.Reason != "assigned" {
		t.Errorf("want reason=assigned, got %q", w.Work.Reason)
	}
	if w.Claimed == nil || !*w.Claimed {
		t.Error("claim=1 on one's own work face should succeed")
	}

	// Frontend is blocked until api finishes — it must not be offered ui yet.
	if fw := h.nextAs(keys["frontend"], "?task="+code); fw.Work != nil {
		t.Errorf("frontend must not be offered work while api is unfinished, got %+v", fw.Work)
	}

	// Hand off.
	h.doAs(keys["backend"], "PATCH", "/api/v1/tasks/"+code+"/sides/api",
		map[string]any{"status": "done"}, nil, http.StatusOK)

	// Now the frontend session is eligible, and the reason says why it was
	// woken rather than just "here is some work".
	fw := h.nextAs(keys["frontend"], "?task="+code+"&claim=1")
	if fw.Work == nil || fw.Work.Side == nil || fw.Work.Side.Key != "ui" {
		t.Fatalf("frontend should now be handed the ui face, got %+v", fw.Work)
	}
	if fw.Claimed == nil || !*fw.Claimed {
		t.Error("frontend should be able to claim its own face")
	}

	// Review still waits on ui.
	if rw := h.nextAs(keys["review"], "?task="+code); rw.Work != nil {
		t.Errorf("review must wait for ui, got %+v", rw.Work)
	}

	h.doAs(keys["frontend"], "PATCH", "/api/v1/tasks/"+code+"/sides/ui",
		map[string]any{"status": "done"}, nil, http.StatusOK)

	rw := h.nextAs(keys["review"], "?task="+code+"&claim=1")
	if rw.Work == nil || rw.Work.Side == nil || rw.Work.Side.Key != "review" {
		t.Fatalf("review should be handed its face after ui lands, got %+v", rw.Work)
	}
}

// A mention asks for an answer; it must not hand over someone else's work face.
func TestMentionDoesNotTransferOwnership(t *testing.T) {
	h := newHarness(t)
	code, keys := setupRelay(t, h)

	// Backend asks the review role a question on review's own face.
	h.doAs(keys["backend"], "POST", "/api/v1/tasks/"+code+"/reports", map[string]any{
		"type": "question", "side_key": "review", "mentions": []string{"review"},
		"body": "要不要一起升 API 版本号？",
	}, nil, http.StatusCreated)

	w := h.nextAs(keys["review"], "?task="+code+"&claim=1")
	if w.Work == nil {
		t.Fatal("a mention must surface as work for the mentioned role")
	}
	if w.Work.Reason != "mention" {
		t.Errorf("want reason=mention, got %q", w.Work.Reason)
	}
	// The face being mentioned is review's own, so claiming it is legitimate.
	if w.Claimed == nil || !*w.Claimed {
		t.Error("the mentioned role owns that face; claiming it should succeed")
	}

	// A backend session polling with claim=1 may legitimately take its *own*
	// face — what it must never do is end up owning review's.
	b := h.nextAs(keys["backend"], "?task="+code+"&claim=1")
	if b.Work == nil {
		t.Fatal("backend has its own api face, it should be offered")
	}
	if b.Work.Side != nil && b.Work.Side.Key == "review" {
		t.Fatal("backend must not be offered review's face")
	}

	// The ownership check has to be on the stored state, not on what the caller
	// claimed: a backend session claiming its own api face is correct, and the
	// two look identical from the response alone.
	var state struct {
		Sides []struct {
			Key              string `json:"key"`
			AssigneeIdentity string `json:"assignee_identity"`
			AssigneeRole     string `json:"assignee_role"`
		} `json:"sides"`
	}
	h.do("GET", "/api/v1/tasks/"+code, nil, &state, http.StatusOK)
	for _, sd := range state.Sides {
		if sd.Key == "review" && sd.AssigneeIdentity == "backend-sess" {
			t.Error("review's work face must not be owned by a backend session")
		}
		if sd.Key == "api" && sd.AssigneeIdentity == "backend-sess" && sd.AssigneeRole != "backend" {
			t.Errorf("api should have stayed on the backend role, got %q", sd.AssigneeRole)
		}
	}
}

// The stored cursor is what stops a polling loop from being handed the same
// item forever when the caller forgets to thread it.
func TestNextCursorAdvancesWithoutCallerHelp(t *testing.T) {
	h := newHarness(t)
	code, keys := setupRelay(t, h)

	h.doAs(keys["backend"], "POST", "/api/v1/tasks/"+code+"/reports", map[string]any{
		"type": "question", "mentions": []string{"review"}, "body": "第一次提问",
	}, nil, http.StatusCreated)

	first := h.nextAs(keys["review"], "?task="+code)
	if first.Work == nil || first.Work.Reason != "mention" {
		t.Fatalf("expected the mention first, got %+v", first.Work)
	}
	// Same call again, no cursor passed: the mention is behind us now, so what
	// comes back is the standing assignment rather than the same message.
	second := h.nextAs(keys["review"], "?task="+code)
	if second.Work != nil && second.Work.Reason == "mention" {
		t.Error("the stored cursor should have moved past an already-delivered mention")
	}
}

// Two sessions of one role polling at the same moment must not both walk away
// believing the work is theirs.
func TestConcurrentClaimIsAtomic(t *testing.T) {
	h := newHarness(t)
	code, keys := setupRelay(t, h)

	const racers = 16
	var wg sync.WaitGroup
	results := make([]nextResp, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = h.nextAs(keys["backend"], "?task="+code+"&claim=1")
		}(i)
	}
	wg.Wait()

	wins := 0
	for _, r := range results {
		if r.Claimed != nil && *r.Claimed {
			wins++
		}
	}
	// The race is for the same face; a claimant that lost still gets the work
	// item (it may have been the one that created it), so the invariant that
	// matters is "exactly one owner", not "exactly one non-nil response".
	if wins != 1 {
		t.Errorf("exactly one session may claim a face, got %d", wins)
	}
	var side struct {
		Sides []struct {
			Key              string `json:"key"`
			AssigneeIdentity string `json:"assignee_identity"`
		} `json:"sides"`
	}
	h.do("GET", "/api/v1/tasks/"+code, nil, &side, http.StatusOK)
	for _, s := range side.Sides {
		if s.Key == "api" && strings.TrimSpace(s.AssigneeIdentity) == "" {
			t.Error("after a successful claim the face must carry an owner")
		}
	}
}

// Finishing the last work face has to close the task. Nothing else in the
// system can notice: faces that are done stop matching any query for
// outstanding work, so a task left open at that moment stays open forever.
func TestLastFaceClosesTheTask(t *testing.T) {
	h := newHarness(t)
	code, keys := setupRelay(t, h)

	var task struct {
		Status string `json:"status"`
	}
	status := func() string {
		h.do("GET", "/api/v1/tasks/"+code, nil, &task, http.StatusOK)
		return task.Status
	}

	if got := status(); got == "done" {
		t.Fatal("a task with unfinished faces must not be done")
	}

	// Walk the relay to completion.
	for _, r := range []struct{ role, side string }{
		{"backend", "api"}, {"frontend", "ui"}, {"review", "review"},
	} {
		if s := status(); s == "done" {
			t.Fatalf("task closed early, before %s finished", r.side)
		}
		h.doAs(keys[r.role], "PATCH", "/api/v1/tasks/"+code+"/sides/"+r.side,
			map[string]any{"status": "done"}, nil, http.StatusOK)
	}
	if got := status(); got != "done" {
		t.Errorf("the last face finishing should close the task, got %q", got)
	}

	// The closure must be legible after the fact, not a silent state change.
	var events struct {
		Events []struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		} `json:"events"`
	}
	h.do("GET", "/api/v1/events?task="+code+"&backlog=1", nil, &events, http.StatusOK)
	found := false
	for _, e := range events.Events {
		if e.Payload["reason"] == "all_sides_done" {
			found = true
		}
	}
	if !found {
		t.Error("the automatic closure should appear in the timeline with its reason")
	}
}

// A task with no work faces is not "all done" — it was never broken down, and
// closing it would hide it from the board.
func TestTaskWithoutFacesIsNotAutoClosed(t *testing.T) {
	h := newHarness(t)
	code := h.newTask("没有拆面的任务", nil)

	var task struct {
		Status string `json:"status"`
	}
	h.do("GET", "/api/v1/tasks/"+code, nil, &task, http.StatusOK)
	if task.Status == "done" {
		t.Errorf("a task with no faces should stay open, got %q", task.Status)
	}
}
