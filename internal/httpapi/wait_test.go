package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ChenYCL/keypoint-notify/internal/httpapi"
)

// Found by running four role sessions against a real server: these are the
// behaviours a waiting session depends on, and each one was wrong.

// actLater performs a request from another goroutine after a delay, the way a
// second session acts while the first is blocked in a long poll.
func (h *harness) actLater(delay time.Duration, key, method, path string, body any) {
	time.AfterFunc(delay, func() {
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, h.srv.URL+path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			h.t.Errorf("%s %s: %v", method, path, err)
			return
		}
		resp.Body.Close()
	})
}

// A session that holds an unfinished face and is waiting for an answer must
// actually wait. It used to get its own face straight back on every call, so
// `kp next --wait 30` spun — and an agent re-read the whole pack each time.
func TestWaitBlocksWhileHoldingOwnFace(t *testing.T) {
	h := newHarness(t)
	code, keys := setupRelay(t, h)

	if w := h.nextAs(keys["backend"], "?task="+code+"&claim=1"); w.Work == nil || w.Claimed == nil || !*w.Claimed {
		t.Fatalf("backend should claim api first, got %+v", w.Work)
	}

	start := time.Now()
	w := h.nextAs(keys["backend"], "?task="+code+"&claim=1&wait=2")
	if w.Work != nil {
		t.Fatalf("nothing new happened; the wait should time out empty, got reason=%s", w.Work.Reason)
	}
	if elapsed := time.Since(start); elapsed < 1500*time.Millisecond {
		t.Errorf("the long poll returned after %v instead of waiting", elapsed)
	}

	// The answer arrives while it waits — that is what should wake it.
	h.actLater(300*time.Millisecond, keys["review"], "POST", "/api/v1/tasks/"+code+"/reports",
		map[string]any{"type": "decision", "body": "用方案 B", "mentions": []string{"backend"}})
	w = h.nextAs(keys["backend"], "?task="+code+"&claim=1&wait=5")
	if w.Work == nil || w.Work.Reason != "mention" {
		t.Fatalf("the mention should wake the waiting session, got %+v", w.Work)
	}

	// Without wait the question is "what is on my plate", and the face is.
	w = h.nextAs(keys["backend"], "?task="+code)
	if w.Work == nil || w.Work.Side == nil || w.Work.Side.Key != "api" {
		t.Errorf("a plain call should still show the face I hold, got %+v", w.Work)
	}
}

// Same for a task owner: an open task is always "mine", and a waiting owner
// used to be handed it back immediately, forever.
func TestWaitBlocksForOwnerUntilActivity(t *testing.T) {
	h := newHarness(t)
	_, keys := setupRelay(t, h)

	// An owner holding no work role, so "owned" is the only thing it can get.
	var pm struct {
		APIKey string `json:"api_key"`
	}
	h.do("POST", "/api/v1/identities", map[string]any{"name": "pm-sess", "kind": "agent", "roles": []string{"member"}}, &pm, http.StatusCreated)
	var created struct {
		Task struct {
			Code string `json:"code"`
		} `json:"task"`
	}
	h.doAs(pm.APIKey, "POST", "/api/v1/tasks", map[string]any{
		"title": "负责人等动静", "sides": []map[string]any{{"key": "api", "assignee_role": "backend"}},
	}, &created, http.StatusCreated)
	code := created.Task.Code

	if w := h.nextAs(pm.APIKey, "?task="+code); w.Work == nil || w.Work.Reason != "owned" {
		t.Fatalf("first look: the owner should see its task, got %+v", w.Work)
	}
	start := time.Now()
	if w := h.nextAs(pm.APIKey, "?task="+code+"&wait=2"); w.Work != nil {
		t.Fatalf("no activity since the last look; want an empty wait, got reason=%s", w.Work.Reason)
	}
	if time.Since(start) < 1500*time.Millisecond {
		t.Error("the owner's long poll did not wait")
	}

	h.actLater(300*time.Millisecond, keys["backend"], "POST", "/api/v1/tasks/"+code+"/reports",
		map[string]any{"type": "progress", "side_key": "api", "body": "开工"})
	w := h.nextAs(pm.APIKey, "?task="+code+"&wait=5")
	if w.Work == nil || w.Work.Reason != "owned" {
		t.Errorf("activity on an owned task should wake the owner, got %+v", w.Work)
	}
}

// "unblocked" is documented as "a dependency of your face just finished". Faces
// with deps are created `todo`, so the old test — "was its status blocked" —
// never fired for them: the relay's frontend was woken with reason=assigned.
func TestFaceWithFinishedDepsIsUnblocked(t *testing.T) {
	h := newHarness(t)
	code, keys := setupRelay(t, h)

	if w := h.nextAs(keys["backend"], "?task="+code+"&claim=1"); w.Work == nil || w.Work.Reason != "assigned" {
		t.Fatalf("a face without deps is plainly assigned, got %+v", w.Work)
	}
	h.doAs(keys["backend"], "PATCH", "/api/v1/tasks/"+code+"/sides/api",
		map[string]any{"status": "done"}, nil, http.StatusOK)

	w := h.nextAs(keys["frontend"], "?task="+code+"&claim=1")
	if w.Work == nil || w.Work.Side == nil || w.Work.Side.Key != "ui" {
		t.Fatalf("frontend should get ui once api is done, got %+v", w.Work)
	}
	if w.Work.Reason != "unblocked" {
		t.Errorf("ui waited on api; want reason=unblocked, got %q", w.Work.Reason)
	}
}

// A face someone claimed ahead of time must still wake its owner when its
// dependency finishes — releasing it has to count as a change.
func TestPreclaimedFaceWakesWhenReleased(t *testing.T) {
	h := newHarness(t)
	code, keys := setupRelay(t, h)

	var claim struct {
		Claimed bool `json:"claimed"`
	}
	h.doAs(keys["frontend"], "POST", "/api/v1/tasks/"+code+"/sides/ui/claim", map[string]any{}, &claim, http.StatusOK)
	if !claim.Claimed {
		t.Fatal("frontend should be able to claim ui in advance")
	}
	h.nextAs(keys["frontend"], "?task="+code) // store a cursor, as a loop would

	h.actLater(300*time.Millisecond, keys["backend"], "PATCH", "/api/v1/tasks/"+code+"/sides/api",
		map[string]any{"status": "done"})
	w := h.nextAs(keys["frontend"], "?task="+code+"&claim=1&wait=5")
	if w.Work == nil || w.Work.Side == nil || w.Work.Side.Key != "ui" || w.Work.Reason != "unblocked" {
		t.Fatalf("finishing api should wake the pre-claimed ui, got %+v", w.Work)
	}
}

// A blocker report parks a face on something the dependency graph does not
// track. It used to be handed straight back out as work — to its own reporter
// among others — with the explanation "your dependency finished".
func TestBlockedFaceIsNotDispatched(t *testing.T) {
	h := newHarness(t)
	code, keys := setupRelay(t, h)

	h.doAs(keys["backend"], "PATCH", "/api/v1/tasks/"+code+"/sides/api",
		map[string]any{"status": "done"}, nil, http.StatusOK)
	h.nextAs(keys["frontend"], "?task="+code+"&claim=1")
	h.doAs(keys["frontend"], "POST", "/api/v1/tasks/"+code+"/reports", map[string]any{
		"type": "blocker", "side_key": "ui", "body": "设计稿缺暗色", "mentions": []string{"review"},
	}, nil, http.StatusCreated)

	if w := h.nextAs(keys["frontend"], "?task="+code); w.Work != nil && w.Work.Side != nil && w.Work.Side.Key == "ui" {
		t.Errorf("a blocked face must not come back as work, got reason=%s", w.Work.Reason)
	}

	// Unblocking is explicit: set it back and it is work again.
	h.doAs(keys["frontend"], "PATCH", "/api/v1/tasks/"+code+"/sides/ui",
		map[string]any{"status": "todo"}, nil, http.StatusOK)
	if w := h.nextAs(keys["frontend"], "?task="+code); w.Work == nil || w.Work.Side == nil || w.Work.Side.Key != "ui" {
		t.Errorf("once back to todo the face is work again, got %+v", w.Work)
	}
}

// Strict decoding is only useful if the error says how to fix the body. An
// agent put skeleton segments at the top level and "role" on its sides, got
// `unknown field "acceptance"`, and never recovered.
func TestUnknownFieldSaysWhereItBelongs(t *testing.T) {
	h := newHarness(t)

	type apiErr struct {
		Error      string   `json:"error"`
		DidYouMean string   `json:"did_you_mean"`
		Field      string   `json:"field"`
		Options    []string `json:"options"`
		Hint       string   `json:"hint"`
	}
	var e apiErr
	h.do("POST", "/api/v1/tasks", map[string]any{"title": "x", "acceptance": "能跑"}, &e, http.StatusBadRequest)
	if e.Error != "invalid_json" {
		t.Errorf("the code is a stability contract; want invalid_json, got %q", e.Error)
	}
	if e.DidYouMean != "segments.acceptance" || e.Field != "acceptance" {
		t.Errorf("want did_you_mean=segments.acceptance field=acceptance, got %q / %q", e.DidYouMean, e.Field)
	}
	if !strings.Contains(strings.Join(e.Options, ","), "segments") {
		t.Errorf("options should list the accepted fields, got %v", e.Options)
	}

	e = apiErr{}
	h.do("POST", "/api/v1/tasks", map[string]any{"title": "x", "constraints": "不许加依赖"}, &e, http.StatusBadRequest)
	if e.DidYouMean != "segments.constraint" {
		t.Errorf("a near miss of a skeleton key should point at it, got %q", e.DidYouMean)
	}

	e = apiErr{}
	h.do("POST", "/api/v1/tasks", map[string]any{"title": "x",
		"sides": []map[string]any{{"key": "api", "role": "backend"}}}, &e, http.StatusBadRequest)
	if !strings.Contains(e.DidYouMean, "assignee_role") {
		t.Errorf("\"role\" on a side should suggest assignee_role, got %q", e.DidYouMean)
	}
}

// The manual `kp docs` prints must show how to create a task, not only how to
// report on one.
func TestLLMSurfaceShowsTaskCreateBody(t *testing.T) {
	h := newHarness(t)
	body := h.text("/api/v1/llms.txt", http.StatusOK)
	for _, want := range []string{"建任务怎么调", `"segments": {`, `"assignee_role": "backend"`} {
		if !strings.Contains(body, want) {
			t.Errorf("llms.txt should contain %q", want)
		}
	}
}

// A server with no bin/ directory beside it — the Docker image, a plain
// `make install` — must still serve clients on its own platform. install.sh
// always asks with ?os=&arch=, and that used to 404 for everyone.
func TestBinaryForServersOwnPlatformNeedsNoBinDir(t *testing.T) {
	h := newHarness(t)
	self := filepath.Join(t.TempDir(), "kp")
	if err := os.WriteFile(self, []byte("#!/bin/sh\necho kp\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	httpapi.SetBinaryProvider(func() (string, error) { return self, nil })
	t.Cleanup(func() { httpapi.SetBinaryProvider(nil) })

	resp, err := http.Get(h.srv.URL + "/kp?os=" + runtime.GOOS + "&arch=" + runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the server's own platform should be served from its own binary, got %d", resp.StatusCode)
	}

	resp, err = http.Get(h.srv.URL + "/kp?os=plan9&arch=mips")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("other platforms still need bin/, want 404, got %d", resp.StatusCode)
	}
}

// Apple Silicon behind a Rosetta shell reports x86_64 from uname.
func TestInstallScriptDetectsAppleSiliconUnderRosetta(t *testing.T) {
	h := newHarness(t)
	body := h.text("/install.sh", http.StatusOK)
	if !strings.Contains(body, "hw.optional.arm64") {
		t.Error("install.sh should ask the hardware, not uname, whether a Mac is arm64")
	}
}
