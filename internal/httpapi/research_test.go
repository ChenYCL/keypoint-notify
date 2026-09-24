package httpapi_test

import (
	"net/http"
	"testing"
)

type researchResp struct {
	Items []struct {
		Code          string   `json:"code"`
		Question      string   `json:"question"`
		Options       []string `json:"options"`
		Findings      int      `json:"findings"`
		OpenQuestions int      `json:"open_questions"`
		Decisions     int      `json:"decisions"`
		State         string   `json:"state"`
	} `json:"items"`
	Open []struct {
		TaskCode string `json:"task_code"`
		Body     string `json:"body"`
	} `json:"open_questions"`
}

// A research task moves from 待定 to 已定论 only when a decision is written after
// its questions; and questions on ordinary tasks show up in 待定 too, because
// "needs someone to decide" is not limited to research.
func TestResearchOpenUntilDecided(t *testing.T) {
	h := newHarness(t)
	rs := h.newTask("推送用 SSE 还是长轮询", map[string]any{
		"kind":     "research",
		"segments": map[string]string{"goal": "推送用 SSE 还是长轮询？"},
	})
	h.do("POST", "/api/v1/tasks/"+rs+"/segments",
		map[string]any{"key": "option-a", "title": "方案 A", "body": "A：SSE"}, nil, http.StatusOK)
	h.do("POST", "/api/v1/tasks/"+rs+"/reports",
		map[string]any{"type": "finding", "body": "支持 B：阻塞 25s 不会超时"}, nil, http.StatusCreated)
	h.do("POST", "/api/v1/tasks/"+rs+"/reports",
		map[string]any{"type": "question", "body": "要不要支持离线补发？"}, nil, http.StatusCreated)

	plain := h.newTask("普通任务", nil)
	h.do("POST", "/api/v1/tasks/"+plain+"/reports",
		map[string]any{"type": "question", "body": "接口字段叫什么？"}, nil, http.StatusCreated)

	var r researchResp
	h.do("GET", "/api/v1/research", nil, &r, http.StatusOK)
	if len(r.Items) != 1 {
		t.Fatalf("want 1 research item, got %d", len(r.Items))
	}
	it := r.Items[0]
	if it.State != "open" || it.Findings != 1 || it.OpenQuestions != 1 || it.Question != "推送用 SSE 还是长轮询？" {
		t.Errorf("before a decision: %+v", it)
	}
	if len(it.Options) != 1 || it.Options[0] != "方案 A" {
		t.Errorf("options should list the option segments, got %v", it.Options)
	}
	if len(r.Open) != 2 {
		t.Errorf("both questions (research and plain task) should be open, got %+v", r.Open)
	}

	h.do("POST", "/api/v1/tasks/"+rs+"/reports",
		map[string]any{"type": "decision", "body": "定论：选 B"}, nil, http.StatusCreated)
	h.do("GET", "/api/v1/research", nil, &r, http.StatusOK)
	if r.Items[0].State != "decided" || r.Items[0].OpenQuestions != 0 || r.Items[0].Decisions != 1 {
		t.Errorf("after the decision: %+v", r.Items[0])
	}
	if len(r.Open) != 1 || r.Open[0].TaskCode != plain {
		t.Errorf("only the plain task's question should remain open, got %+v", r.Open)
	}

	// A new question after the decision re-opens it.
	h.do("POST", "/api/v1/tasks/"+rs+"/reports",
		map[string]any{"type": "question", "body": "那重连策略呢？"}, nil, http.StatusCreated)
	h.do("GET", "/api/v1/research", nil, &r, http.StatusOK)
	if r.Items[0].State != "open" {
		t.Errorf("a question after the decision should re-open the research, got %+v", r.Items[0])
	}

	// Closing the task takes its questions off the list.
	h.do("PATCH", "/api/v1/tasks/"+plain, map[string]any{"status": "done"}, nil, http.StatusOK)
	h.do("GET", "/api/v1/research", nil, &r, http.StatusOK)
	for _, q := range r.Open {
		if q.TaskCode == plain {
			t.Error("a done task's questions should no longer be listed as open")
		}
	}
}
