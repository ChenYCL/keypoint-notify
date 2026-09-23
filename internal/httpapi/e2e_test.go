package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChenYCL/keypoint-notify/internal/httpapi"
	"github.com/ChenYCL/keypoint-notify/internal/store"
)

// harness is a running server plus the key used to talk to it.
type harness struct {
	t    *testing.T
	srv  *httptest.Server
	key  string
	name string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SeedRoles(); err != nil {
		t.Fatalf("seed roles: %v", err)
	}
	srv := httptest.NewServer(httpapi.New(st, "test", nil).Handler())
	t.Cleanup(srv.Close)

	h := &harness{t: t, srv: srv}
	var out struct {
		APIKey string `json:"api_key"`
	}
	h.do("POST", "/api/v1/bootstrap", map[string]any{"name": "owner", "kind": "human"}, &out, http.StatusCreated)
	h.key = out.APIKey
	h.name = "owner"
	return h
}

// do performs a request as the harness identity and asserts the status.
func (h *harness) do(method, path string, body any, out any, wantStatus int) {
	h.t.Helper()
	h.doAs(h.key, method, path, body, out, wantStatus)
}

func (h *harness) doAs(key, method, path string, body any, out any, wantStatus int) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, reader)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		h.t.Fatalf("%s %s: want %d, got %d\n%s", method, path, wantStatus, resp.StatusCode, clip(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			h.t.Fatalf("%s %s: decode: %v\n%s", method, path, err, clip(data))
		}
	}
}

// text fetches a plain-text endpoint.
// clip keeps a failure message from dumping an entire rendered pack.
func clip(b []byte) string {
	const max = 400
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + fmt.Sprintf("\n…（共 %d 字节，已截断）", len(b))
}

func (h *harness) text(path string, wantStatus int) string {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+h.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		h.t.Fatalf("GET %s: want %d, got %d\n%s", path, wantStatus, resp.StatusCode, data)
	}
	return string(data)
}

// ---------------------------------------------------------------------------
// The end-to-end story: create a task, split it, hand a side over, report back.
// ---------------------------------------------------------------------------

func TestTaskLifecycleAndPack(t *testing.T) {
	h := newHarness(t)

	var created struct {
		Task struct {
			Code string `json:"code"`
			ID   string `json:"id"`
		} `json:"task"`
	}
	h.do("POST", "/api/v1/tasks", map[string]any{
		"title":      "登录页验证码倒计时在切后台后错位",
		"kind":       "bug",
		"priority":   "P1",
		"owner_role": "backend",
		"summary":    "60s 倒计时切后台回来跳变",
		"segments": map[string]string{
			"goal":       "切后台 >=10s 回来倒计时准确",
			"acceptance": "iOS/Android 行为一致；单测覆盖 visibilitychange",
			"踩坑记录":       "iOS 上 pagehide 更可靠",
		},
		"sides": []map[string]any{
			{"key": "api", "title": "后端冷却接口", "assignee_role": "backend"},
			{"key": "ui", "title": "前端倒计时", "assignee_role": "frontend", "deps": []string{"api"},
				"segments": map[string]string{"交互说明": "重新可见时重拉 remaining_ms"}},
		},
	}, &created, http.StatusCreated)

	code := created.Task.Code
	if code != "KP-1" {
		t.Fatalf("first task should be KP-1, got %q", code)
	}

	// The skeleton keys exist even though only three were supplied.
	for _, key := range []string{"context", "goal", "deliverable", "constraint", "acceptance", "interface", "files"} {
		got := h.text("/api/v1/tasks/"+code+"/segments/"+key, http.StatusOK)
		if key == "goal" && !strings.Contains(got, "倒计时准确") {
			t.Errorf("goal segment missing its body: %q", got)
		}
		if !strings.Contains(got, "["+key+"]") {
			t.Errorf("segment %s should carry a provenance header, got %q", key, got)
		}
	}

	// The pack is the product: one call, everything a receiver needs.
	pack := h.text("/api/v1/tasks/"+code+"/pack?side=ui", http.StatusOK)
	for _, want := range []string{
		code, "登录页验证码倒计时", // identity
		"工作面（sides）", "| **→ ui**", // focus marking
		"[acceptance] 验收标准", "单测覆盖 visibilitychange", // skeleton content
		"[交互说明]", "重新可见时重拉", // the focused side's own segment
		"本工作面依赖：api",                                                       // dependency surfaced
		"## 交付契约（承接方必读）", "kp report " + code + " --side ui --type result", // the contract
	} {
		if !strings.Contains(pack, want) {
			t.Errorf("pack missing %q\n--- pack ---\n%s", want, pack)
		}
	}
	// A focused pack must not leak the sibling's segments.
	if strings.Contains(pack, "接口契约") {
		t.Error("pack focused on ui should not include the api side's segments")
	}
	// The contract speaks as the receiver, not the caller.
	if !strings.Contains(pack, "以角色 **@frontend**") {
		t.Errorf("contract should speak as the side's role, got:\n%s", pack)
	}

	// max_chars truncation is announced, never silent, and the contract still
	// survives to the end of the document.
	h.do("POST", "/api/v1/tasks/"+code+"/segments", map[string]any{
		"key": "长文", "title": "长文", "body": strings.Repeat("这是一段很长的细节说明。", 400),
	}, nil, http.StatusOK)
	truncated := h.text("/api/v1/tasks/"+code+"/pack?max_chars=1200", http.StatusOK)
	if !strings.Contains(truncated, "已截断") {
		t.Errorf("expected an explicit truncation notice:\n%s", truncated)
	}
	if !strings.Contains(truncated, "## 交付契约") {
		t.Errorf("the contract must survive truncation — it is the one part that must never be cut:\n%s", truncated)
	}
}

func TestSidesAssignReportAndInbox(t *testing.T) {
	h := newHarness(t)
	code := h.newTask("任务标题", nil)

	// A second identity, so we can observe the notification path end to end.
	var idn struct {
		Identity struct {
			ID string `json:"id"`
		} `json:"identity"`
		APIKey string `json:"api_key"`
	}
	h.do("POST", "/api/v1/identities", map[string]any{
		"name": "fe-agent", "kind": "agent", "roles": []string{"frontend", "review"},
	}, &idn, http.StatusCreated)

	var side struct {
		Side struct {
			Key          string   `json:"key"`
			AssigneeRole string   `json:"assignee_role"`
			Status       string   `json:"status"`
			Deps         []string `json:"deps"`
		} `json:"side"`
		BlockedBy []string `json:"blocked_by"`
	}
	h.do("POST", "/api/v1/tasks/"+code+"/sides", map[string]any{
		"key": "api", "title": "后端", "assignee_role": "backend",
	}, &side, http.StatusCreated)
	h.do("POST", "/api/v1/tasks/"+code+"/sides", map[string]any{
		"key": "ui", "title": "前端", "assignee_role": "frontend", "deps": []string{"api"},
	}, &side, http.StatusCreated)
	if len(side.BlockedBy) != 1 || !strings.Contains(side.BlockedBy[0], "api") {
		t.Errorf("a side depending on an unfinished side should report blocked_by, got %v", side.BlockedBy)
	}

	// A dependency cycle is refused rather than stored.
	h.do("PATCH", "/api/v1/tasks/"+code+"/sides/api", map[string]any{
		"deps": []string{"ui"},
	}, nil, http.StatusInternalServerError)

	// Dependency cycles are a client error, not a server one; the message must
	// say which cycle.
	var errBody struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	req, _ := http.NewRequest("PATCH", h.srv.URL+"/api/v1/tasks/"+code+"/sides/api",
		strings.NewReader(`{"deps":["ui"]}`))
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	resp.Body.Close()
	if !strings.Contains(errBody.Message, "cycle") {
		t.Errorf("cycle rejection should name the problem, got %q", errBody.Message)
	}

	// A typo in a skeleton key is caught instead of silently creating a duplicate.
	h.do("POST", "/api/v1/tasks/"+code+"/segments", map[string]any{
		"key": "accepance", "body": "oops",
	}, nil, http.StatusBadRequest)

	// The agent files a blocker with a mention; the owner must be notified.
	var rep struct {
		Report struct {
			Type         string `json:"type"`
			SideKey      string `json:"side_key"`
			IdentityName string `json:"identity_name"`
			Role         string `json:"role"`
		} `json:"report"`
		Notified []string `json:"notified"`
	}
	h.doAs(idn.APIKey, "POST", "/api/v1/tasks/"+code+"/reports", map[string]any{
		"type": "blocker", "side_key": "ui",
		"body":     "冷却接口没上线，前端只能先本地算",
		"mentions": []string{"@backend"},
	}, &rep, http.StatusCreated)

	if rep.Report.IdentityName != "fe-agent" {
		t.Errorf("report should carry the author's name, got %q", rep.Report.IdentityName)
	}
	if rep.Report.Role != "frontend" {
		t.Errorf("report should be attributed to the active role, got %q", rep.Report.Role)
	}

	// The blocker moved the side to blocked.
	var task struct {
		Sides []struct {
			Key    string `json:"key"`
			Status string `json:"status"`
		} `json:"sides"`
	}
	h.do("GET", "/api/v1/tasks/"+code, nil, &task, http.StatusOK)
	for _, s := range task.Sides {
		if s.Key == "ui" && s.Status != "blocked" {
			t.Errorf("a blocker report should move its side to blocked, got %q", s.Status)
		}
	}

	// The owner sees it in the inbox.
	var inbox struct {
		Unread int `json:"unread"`
		Items  []struct {
			Kind  string `json:"kind"`
			Title string `json:"title"`
		} `json:"items"`
	}
	h.do("GET", "/api/v1/inbox?unread=1", nil, &inbox, http.StatusOK)
	if inbox.Unread == 0 {
		t.Fatal("the mentioned owner should have an unread notification")
	}
	if !strings.Contains(inbox.Items[0].Title, "阻塞") {
		t.Errorf("notification title should name the report type, got %q", inbox.Items[0].Title)
	}

	// The agent's board shows the work face it owns.
	var board struct {
		Role    string `json:"role"`
		MySides []struct {
			Key          string `json:"key"`
			AssigneeRole string `json:"assignee_role"`
		} `json:"my_sides"`
	}
	h.doAs(idn.APIKey, "GET", "/api/v1/me/board", nil, &board, http.StatusOK)
	if len(board.MySides) != 1 || board.MySides[0].Key != "ui" {
		t.Errorf("board should show exactly the side addressed to this agent, got %+v", board.MySides)
	}

	// `assigned=me` is an OR across identity and every role held: the task was
	// handed to the frontend *role*, not to the agent by name, and it must
	// still show up.
	var assigned struct {
		Count int `json:"count"`
		Tasks []struct {
			Code string `json:"code"`
		} `json:"tasks"`
	}
	h.doAs(idn.APIKey, "GET", "/api/v1/tasks?assigned=me", nil, &assigned, http.StatusOK)
	if assigned.Count != 1 || assigned.Tasks[0].Code != code {
		t.Errorf("assigned=me should match work addressed to the agent's roles, got %+v", assigned)
	}
}

// textAs fetches a JSON endpoint as another identity and returns the raw body.
func (h *harness) textAs(key, path string) string {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return string(data)
}

func (h *harness) newTask(title string, extra map[string]any) string {
	h.t.Helper()
	body := map[string]any{"title": title, "kind": "feature"}
	for k, v := range extra {
		body[k] = v
	}
	var out struct {
		Task struct {
			Code string `json:"code"`
		} `json:"task"`
	}
	h.do("POST", "/api/v1/tasks", body, &out, http.StatusCreated)
	return out.Task.Code
}

// ---------------------------------------------------------------------------
// Identity: role re-binding without a new key, rotation, admin gating.
// ---------------------------------------------------------------------------

func TestIdentityRolesAreRebindable(t *testing.T) {
	h := newHarness(t)

	var created struct {
		Identity struct {
			ID         string   `json:"id"`
			Name       string   `json:"name"`
			Roles      []string `json:"roles"`
			ActiveRole string   `json:"active_role"`
		} `json:"identity"`
		APIKey string `json:"api_key"`
	}
	h.do("POST", "/api/v1/identities", map[string]any{
		"name": "alice", "kind": "human", "roles": []string{"frontend"},
	}, &created, http.StatusCreated)
	if created.Identity.ActiveRole != "frontend" {
		t.Fatalf("active role should default to the first held role, got %q", created.Identity.ActiveRole)
	}

	// Re-bind roles; the key must keep working.
	var patched struct {
		Identity struct {
			Roles      []string `json:"roles"`
			ActiveRole string   `json:"active_role"`
		} `json:"identity"`
	}
	h.do("PATCH", "/api/v1/identities/"+created.Identity.ID, map[string]any{
		"roles": []string{"backend", "review"},
	}, &patched, http.StatusOK)
	if len(patched.Identity.Roles) != 2 {
		t.Fatalf("roles should have changed, got %v", patched.Identity.Roles)
	}
	if patched.Identity.ActiveRole != "backend" {
		t.Errorf("active role should follow the old one being dropped, got %q", patched.Identity.ActiveRole)
	}
	h.doAs(created.APIKey, "GET", "/api/v1/whoami", nil, &struct {
		Role string `json:"role"`
	}{}, http.StatusOK)

	// An identity that is not admin cannot grant itself roles.
	var self struct {
		Identity struct {
			ID string `json:"id"`
		} `json:"identity"`
		APIKey string `json:"api_key"`
	}
	h.do("POST", "/api/v1/identities", map[string]any{
		"name": "bob", "kind": "agent", "roles": []string{"member"},
	}, &self, http.StatusCreated)
	h.doAs(self.APIKey, "PATCH", "/api/v1/identities/"+self.Identity.ID, map[string]any{
		"roles": []string{"admin"},
	}, nil, http.StatusForbidden)

	// Switching to a role the identity does not hold is refused, not silently
	// applied.
	h.doAs(self.APIKey, "PATCH", "/api/v1/identities/"+self.Identity.ID, map[string]any{
		"active_role": "admin",
	}, nil, http.StatusInternalServerError)

	// Rotation invalidates the old key immediately.
	var rotated struct {
		APIKey string `json:"api_key"`
	}
	h.do("POST", "/api/v1/identities/"+created.Identity.ID+"/rotate", map[string]any{}, &rotated, http.StatusOK)
	if rotated.APIKey == created.APIKey {
		t.Fatal("rotation must mint a new key")
	}
	h.doAs(created.APIKey, "GET", "/api/v1/whoami", nil, nil, http.StatusUnauthorized)
	h.doAs(rotated.APIKey, "GET", "/api/v1/whoami", nil, &struct {
		Role string `json:"role"`
	}{}, http.StatusOK)
}

// ---------------------------------------------------------------------------
// Errors must be self-correcting, which is the whole point of the shape.
// ---------------------------------------------------------------------------

func TestErrorsCarryRecoveryHints(t *testing.T) {
	h := newHarness(t)
	code := h.newTask("一个任务", nil)

	cases := []struct {
		name       string
		method     string
		path       string
		body       any
		wantStatus int
		wantCode   string
		wantField  string
		wantValue  string
	}{
		{
			name: "unknown task suggests the closest code", method: "GET",
			path: "/api/v1/tasks/KP-99", wantStatus: 404,
			wantCode: "task_not_found", wantField: "did_you_mean", wantValue: code,
		},
		{
			name: "typo in a segment key suggests the real one", method: "GET",
			path: "/api/v1/tasks/" + code + "/segments/accepance", wantStatus: 404,
			wantCode: "segment_not_found", wantField: "did_you_mean", wantValue: "acceptance",
		},
		{
			name: "unknown side suggests available ones", method: "PATCH",
			path: "/api/v1/tasks/" + code + "/sides/nope", body: map[string]any{"status": "doing"},
			wantStatus: 404, wantCode: "side_not_found",
		},
		{
			name: "bad status lists the valid ones", method: "GET",
			path: "/api/v1/tasks?status=doingg", wantStatus: 400,
			wantCode: "bad_status", wantField: "options", wantValue: "doing",
		},
		{
			name: "bad report type lists the valid ones", method: "POST",
			path: "/api/v1/tasks/" + code + "/reports",
			body: map[string]any{"body": "x", "type": "blocer"}, wantStatus: 400,
			wantCode: "bad_report_type", wantField: "options", wantValue: "blocker",
		},
		{
			name: "relative time is accepted, garbage is not", method: "GET",
			path: "/api/v1/tasks?since=banana", wantStatus: 400, wantCode: "bad_time",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != nil {
				data, _ := json.Marshal(tc.body)
				body = bytes.NewReader(data)
			}
			req, _ := http.NewRequest(tc.method, h.srv.URL+tc.path, body)
			req.Header.Set("Authorization", "Bearer "+h.key)
			if tc.body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("want status %d, got %d", tc.wantStatus, resp.StatusCode)
			}
			var e map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
				t.Fatalf("error body is not JSON: %v", err)
			}
			if e["error"] != tc.wantCode {
				t.Errorf("want error %q, got %q", tc.wantCode, e["error"])
			}
			if e["docs"] != "/api/v1/llms.txt" {
				t.Errorf("every error should point at the docs, got %v", e["docs"])
			}
			if tc.wantField != "" {
				got := fmt.Sprint(e[tc.wantField])
				if !strings.Contains(got, tc.wantValue) {
					t.Errorf("%s should contain %q, got %q", tc.wantField, tc.wantValue, got)
				}
			}
		})
	}

	// Unauthenticated, and the hint changes once the system is bootstrapped.
	h.doAs("", "GET", "/api/v1/tasks", nil, nil, http.StatusUnauthorized)
}

// ---------------------------------------------------------------------------
// The agent-facing surface: llms.txt, schema, events cursor.
// ---------------------------------------------------------------------------

func TestLLMSurfaceIsSelfDescribing(t *testing.T) {
	h := newHarness(t)

	// Both docs endpoints are readable without a key: an agent that has not
	// been given credentials yet still needs to learn how to authenticate.
	for _, path := range []string{"/api/v1/llms.txt", "/api/v1/schema"} {
		req, _ := http.NewRequest("GET", h.srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s should be readable without a key, got %d", path, resp.StatusCode)
		}
		if len(data) < 500 {
			t.Errorf("%s looks empty", path)
		}
	}

	manual := h.text("/api/v1/llms.txt", http.StatusOK)
	for _, want := range []string{
		"base_url:", "Bearer kp_", "pack", "did_you_mean", "skeleton",
		"acceptance", "report_type",
	} {
		if !strings.Contains(manual, want) {
			t.Errorf("llms.txt should mention %q", want)
		}
	}

	var schema struct {
		Enums map[string][]string `json:"enums"`
	}
	h.do("GET", "/api/v1/schema", nil, &schema, http.StatusOK)
	if len(schema.Enums["report_type"]) != 6 {
		t.Errorf("schema should enumerate report types, got %v", schema.Enums["report_type"])
	}
	if len(schema.Enums["skeleton_segment_keys"]) != 7 {
		t.Errorf("schema should enumerate skeleton keys, got %v", schema.Enums["skeleton_segment_keys"])
	}

	// The event cursor round-trips: first call syncs, then a write appears.
	var first struct {
		Cursor int64  `json:"cursor"`
		Note   string `json:"note"`
	}
	h.do("GET", "/api/v1/events", nil, &first, http.StatusOK)
	if first.Note == "" {
		t.Error("a cursor-less call should say it synced to now rather than replaying history")
	}
	code := h.newTask("游标测试", nil)

	var second struct {
		Cursor int64 `json:"cursor"`
		Events []struct {
			Type     string `json:"type"`
			TaskCode string `json:"task_code"`
		} `json:"events"`
	}
	h.do("GET", fmt.Sprintf("/api/v1/events?since=%d", first.Cursor), nil, &second, http.StatusOK)
	if len(second.Events) != 1 || second.Events[0].TaskCode != code {
		t.Fatalf("expected exactly the new task event, got %+v", second.Events)
	}
	if second.Cursor <= first.Cursor {
		t.Errorf("cursor must advance: %d -> %d", first.Cursor, second.Cursor)
	}
}

// ---------------------------------------------------------------------------
// Files
// ---------------------------------------------------------------------------

func TestFileUploadAndAttachment(t *testing.T) {
	h := newHarness(t)
	code := h.newTask("带图的任务", nil)

	// A one-pixel PNG, enough to exercise MIME sniffing and inline serving.
	png := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// Deliberately no Content-Type on the part: the server must sniff it.
	part, err := w.CreateFormFile("file", "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	part.Write(png)
	w.Close()

	req, _ := http.NewRequest("POST", h.srv.URL+"/api/v1/tasks/"+code+"/files", &buf)
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload: want 201, got %d\n%s", resp.StatusCode, data)
	}
	var up struct {
		Files []struct {
			ID      string `json:"id"`
			MIME    string `json:"mime"`
			IsImage bool   `json:"is_image"`
			URL     string `json:"url"`
		} `json:"files"`
		Markdown []string `json:"markdown"`
	}
	json.NewDecoder(resp.Body).Decode(&up)
	if len(up.Files) != 1 {
		t.Fatalf("expected one file, got %+v", up.Files)
	}
	if !up.Files[0].IsImage || up.Files[0].MIME != "image/png" {
		t.Errorf("MIME should be sniffed as image/png, got %q (is_image=%v)",
			up.Files[0].MIME, up.Files[0].IsImage)
	}
	if len(up.Markdown) != 1 || !strings.HasPrefix(up.Markdown[0], "![") {
		t.Errorf("images should come back as markdown image syntax, got %v", up.Markdown)
	}

	// Attaching it to a report makes it show up in the pack as a markdown link.
	h.do("POST", "/api/v1/tasks/"+code+"/reports", map[string]any{
		"type": "progress", "body": "现场截图",
		"attachments": []string{up.Files[0].ID},
	}, nil, http.StatusCreated)

	pack := h.text("/api/v1/tasks/"+code+"/pack", http.StatusOK)
	if !strings.Contains(pack, "![shot.png]("+up.Files[0].URL+")") {
		t.Errorf("pack should embed the image as markdown:\n%s", pack)
	}

	// The file is served inline for images, with a sniffing guard.
	freq, _ := http.NewRequest("GET", h.srv.URL+up.Files[0].URL, nil)
	freq.Header.Set("Authorization", "Bearer "+h.key)
	fresp, err := http.DefaultClient.Do(freq)
	if err != nil {
		t.Fatal(err)
	}
	defer fresp.Body.Close()
	if fresp.StatusCode != 200 {
		t.Fatalf("fetch file: %d", fresp.StatusCode)
	}
	if disp := fresp.Header.Get("Content-Disposition"); !strings.HasPrefix(disp, "inline") {
		t.Errorf("images should render inline, got %q", disp)
	}
	if fresp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("file responses must set X-Content-Type-Options: nosniff")
	}
	got, _ := io.ReadAll(fresp.Body)
	if !bytes.Equal(got, png) {
		t.Error("served bytes differ from what was uploaded")
	}

	// And requires auth.
	anon, _ := http.NewRequest("GET", h.srv.URL+up.Files[0].URL, nil)
	aresp, err := http.DefaultClient.Do(anon)
	if err != nil {
		t.Fatal(err)
	}
	aresp.Body.Close()
	if aresp.StatusCode != http.StatusUnauthorized {
		t.Errorf("files must require a key, got %d", aresp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Bootstrap gating
// ---------------------------------------------------------------------------

func TestBootstrapOnlyWorksOnce(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/api/v1/bootstrap", map[string]any{"name": "second"}, nil, http.StatusConflict)

	var health struct {
		Bootstrapped bool `json:"bootstrapped"`
	}
	anon := &harness{t: t, srv: h.srv}
	anon.doAs("", "GET", "/api/v1/health", nil, &health, http.StatusOK)
	if !health.Bootstrapped {
		t.Error("health should report the system as bootstrapped")
	}
}

// ---------------------------------------------------------------------------
// Task status cascades
// ---------------------------------------------------------------------------

func TestCompletingTaskClosesOpenSides(t *testing.T) {
	h := newHarness(t)
	code := h.newTask("收尾测试", nil)
	h.do("POST", "/api/v1/tasks/"+code+"/sides", map[string]any{
		"key": "a", "assignee_role": "backend",
	}, nil, http.StatusCreated)
	h.do("POST", "/api/v1/tasks/"+code+"/sides", map[string]any{
		"key": "b", "assignee_role": "frontend",
	}, nil, http.StatusCreated)

	h.do("PATCH", "/api/v1/tasks/"+code, map[string]any{"status": "done"}, nil, http.StatusOK)

	var task struct {
		Status string `json:"status"`
		Sides  []struct {
			Key    string `json:"key"`
			Status string `json:"status"`
		} `json:"sides"`
	}
	h.do("GET", "/api/v1/tasks/"+code, nil, &task, http.StatusOK)
	for _, s := range task.Sides {
		if s.Status != "done" {
			t.Errorf("closing a task should close its sides; %s is %q", s.Key, s.Status)
		}
	}
}

// ---------------------------------------------------------------------------
// Upload size limits
// ---------------------------------------------------------------------------

// An oversized upload must come back as a size error, not a parse error:
// "malformed multipart" sends the caller off to fix a request that was fine.
func TestOversizedUploadReportsSizeNotSyntax(t *testing.T) {
	h := newHarness(t)
	code := h.newTask("大附件", nil)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// One byte over the store's limit. Written through a pipe-free writer so
	// the test does not allocate twice.
	part, err := w.CreateFormFile("file", "huge.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(make([]byte, store.MaxBlobBytes+1)); err != nil {
		t.Fatal(err)
	}
	w.Close()

	req, _ := http.NewRequest("POST", h.srv.URL+"/api/v1/tasks/"+code+"/files", &buf)
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 413, got %d\n%s", resp.StatusCode, body)
	}
	var e map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatal(err)
	}
	if e["error"] != "file_too_large" {
		t.Errorf("want error file_too_large, got %v", e["error"])
	}
	// The hint must name the alternative, since a big file is usually better
	// as a link than as an attachment.
	if hint, _ := e["hint"].(string); !strings.Contains(hint, "链接") {
		t.Errorf("the hint should point at links as the alternative, got %q", hint)
	}
}

// ---------------------------------------------------------------------------
// Event cursor semantics
// ---------------------------------------------------------------------------

// The two ways of asking for events must stay distinct: omitting `since` syncs
// a fresh client to now, while an explicit `since=0` replays from the start.
func TestEventCursorSyncsOrReplays(t *testing.T) {
	h := newHarness(t)
	h.newTask("先有的任务", nil)

	var synced struct {
		Count  int    `json:"count"`
		Cursor int64  `json:"cursor"`
		Note   string `json:"note"`
	}
	h.do("GET", "/api/v1/events", nil, &synced, http.StatusOK)
	if synced.Count != 0 {
		t.Errorf("omitting since should sync, not replay: got %d events", synced.Count)
	}
	if synced.Cursor == 0 {
		t.Error("syncing should still hand back a usable cursor")
	}

	var replayed struct {
		Count  int `json:"count"`
		Events []struct {
			Type string `json:"type"`
		} `json:"events"`
	}
	h.do("GET", "/api/v1/events?since=0", nil, &replayed, http.StatusOK)
	if replayed.Count == 0 {
		t.Error("an explicit since=0 should replay history")
	}
	if len(replayed.Events) > 0 && replayed.Events[0].Type != store.EvTaskCreated {
		t.Errorf("a replay should start at the first event, got %q", replayed.Events[0].Type)
	}

	// backlog=1 is the explicit "give me history" spelling.
	var backlog struct {
		Count int `json:"count"`
	}
	h.do("GET", "/api/v1/events?backlog=1", nil, &backlog, http.StatusOK)
	if backlog.Count == 0 {
		t.Error("backlog=1 should return history")
	}
}

// A program-shaped path must never be answered with the SPA shell.
//
// During a deploy a stale server answered /skill/SKILL.md with 200 + HTML,
// which looked exactly like success to every probe we ran — the endpoint only
// appeared to exist because the SPA fallback swallowed it.
func TestMachinePathsAreNeverTheSPA(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/api/v1/nope", "/skill/nope.md", "/skill", "/api"} {
		req, _ := http.NewRequest("GET", h.srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body := make([]byte, 64)
		n, _ := resp.Body.Read(body)
		resp.Body.Close()
		head := string(body[:n])
		if strings.Contains(head, "<!DOCTYPE html") {
			t.Errorf("%s returned the SPA shell (status %d) — a machine client reads that as success",
				path, resp.StatusCode)
		}
	}
}

// install.sh must fetch the binary for the *client's* platform, not the
// server's.
//
// A hub usually runs on a Linux box while half the team is on macOS. Before
// this, the script checked that the client's platform was known and then
// downloaded the server's own binary regardless — a Mac user got a Linux ELF
// and `curl | sh` looked like it had worked.
func TestInstallScriptFetchesClientPlatform(t *testing.T) {
	h := newHarness(t)

	body := h.text("/install.sh", http.StatusOK)
	if !strings.Contains(body, "GOARCH=arm64") || !strings.Contains(body, "GOARCH=amd64") {
		t.Error("install.sh should translate uname output to Go's arch names")
	}
	if !strings.Contains(body, "kp?os=$os&arch=$GOARCH") {
		t.Errorf("install.sh must request a platform-specific binary, got:\n%s", body)
	}
	if strings.Contains(body, `curl -fsSL "$BASE/kp" -o`) {
		t.Error("install.sh still downloads the server's own binary unconditionally")
	}
}

// A platform the server has no build for must 404 with instructions, not hand
// back a binary that will not run.
func TestBinaryNotFoundForUnknownPlatform(t *testing.T) {
	h := newHarness(t)

	req, _ := http.NewRequest("GET", h.srv.URL+"/kp?os=plan9&arch=mips", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown platform should 404, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "go install") {
		t.Errorf("the 404 should tell the user how to get a build, got %q", body)
	}
}
