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
	Note    string `json:"note"`
	Hint    string `json:"hint"`
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
	// A mention is not dispatch: it asks for an answer, and answering does not
	// hand over the face it was asked on. The face here is also not ready yet
	// (its own dependency is open), so there is nothing to take anyway.
	if w.Claimed != nil && *w.Claimed {
		t.Error("a mention must not claim the face it was asked on")
	}
	if w.Note == "" {
		t.Error("when claim=1 turns out not to be a dispatch, the response should say so")
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

// A session that has judged an item not-its-to-take needs a way to move on.
// Without it the same item is offered forever and the only escape is to stop
// asking — which looks like "no work" and hides the rest of the queue.
func TestNextCanSkipTasks(t *testing.T) {
	h := newHarness(t)
	_, keys := setupRelay(t, h)

	// Three independent tasks, all addressed to the same role.
	mk := func(title string) string {
		var out struct {
			Task struct {
				Code string `json:"code"`
			} `json:"task"`
		}
		h.do("POST", "/api/v1/tasks", map[string]any{
			"title": title,
			"sides": []map[string]any{{"key": "fix", "assignee_role": "backend"}},
		}, &out, http.StatusCreated)
		return out.Task.Code
	}
	a, b, c := mk("任务A"), mk("任务B"), mk("任务C")

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		w := h.nextAs(keys["backend"], "?exclude="+a+","+b)
		if w.Work == nil {
			t.Fatalf("round %d: expected %s once the first two were excluded", i, c)
		}
		seen[w.Work.Task.Code] = true
	}

	w := h.nextAs(keys["backend"], "?exclude="+a)
	if w.Work == nil {
		t.Fatal("excluding one of three should still leave work")
	}
	if w.Work.Task.Code == a {
		t.Errorf("excluded task %s was still offered", a)
	}

	w = h.nextAs(keys["backend"], "?exclude="+a+","+b+","+c)
	if w.Work != nil && (w.Work.Task.Code == a || w.Work.Task.Code == b || w.Work.Task.Code == c) {
		t.Errorf("all three were excluded but %s came back", w.Work.Task.Code)
	}
}

// A burst of same-role sessions must fan out across the queue, not pile onto
// the head of it.
//
// The naive shape — pick one candidate, then try to claim it — makes every
// concurrent caller see the same head: one wins, the rest get "someone else
// took it" and have to ask again to discover the next. Twelve racers against
// twelve available faces should end with twelve distinct owners.
func TestConcurrentDispatchFansOut(t *testing.T) {
	h := newHarness(t)
	_, keys := setupRelay(t, h)

	const n = 12
	codes := make([]string, n)
	for i := 0; i < n; i++ {
		var out struct {
			Task struct {
				Code string `json:"code"`
			} `json:"task"`
		}
		h.do("POST", "/api/v1/tasks", map[string]any{
			"title": "并行任务",
			"sides": []map[string]any{{"key": "fix", "assignee_role": "backend"}},
		}, &out, http.StatusCreated)
		codes[i] = out.Task.Code
	}

	var wg sync.WaitGroup
	got := make([]string, n)
	claimed := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := h.nextAs(keys["backend"], "?claim=1")
			if r.Work != nil {
				got[i] = r.Work.Task.Code
			}
			claimed[i] = r.Claimed != nil && *r.Claimed
		}(i)
	}
	wg.Wait()

	seen := map[string]int{}
	for i, c := range got {
		if claimed[i] && c != "" {
			seen[c]++
		}
	}
	for code, count := range seen {
		if count > 1 {
			t.Errorf("%s was dispatched to %d sessions at once", code, count)
		}
	}
	if len(seen) != n {
		t.Errorf("expected all %d faces to be dispatched, got %d", n, len(seen))
	}
}
