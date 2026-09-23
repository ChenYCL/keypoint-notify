package pack

import (
	"strings"
	"testing"

	"github.com/ChenYCL/keypoint-notify/internal/model"
)

func sampleTask() model.Task {
	return model.Task{
		Code: "KP-7", Title: "验证码倒计时错位",
		Kind: model.TaskBug, Priority: "P1", Status: model.StatusDoing,
		OwnerRole: "backend",
		Segments: []model.Segment{
			{Key: "goal", Title: "目标", Body: "切后台回来倒计时准确"},
			{Key: "context", Title: "背景", Body: ""},
		},
		Sides: []model.Side{
			{ID: "s1", Key: "api", Title: "后端", Status: model.SideDone, AssigneeRole: "backend"},
			{ID: "s2", Key: "ui", Title: "前端", Status: model.SideBlocked, AssigneeRole: "frontend",
				Deps: []string{"api"},
				Segments: []model.Segment{
					{Key: "交互说明", Title: "交互说明", Body: "重新可见时重拉"},
				}},
		},
	}
}

// The contract speaks for whoever is going to pick the work up, not for the
// caller who generated the pack.
func TestContractRolePreference(t *testing.T) {
	ui := sampleTask().Sides[1]
	tests := []struct {
		name      string
		focus     *model.Side
		actorRole string
		want      string
	}{
		{"focused side's role wins", &ui, "admin", "frontend"},
		{"task owner when unfocused", nil, "admin", "backend"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := sampleTask()
			opt := Options{}
			if tc.focus != nil {
				opt.SideKey = tc.focus.Key
			}
			b := Build(task, nil, tc.actorRole, opt)
			if b.Contract.Role != tc.want {
				t.Errorf("want role %q, got %q", tc.want, b.Contract.Role)
			}
		})
	}

	// With no owner role anywhere, the caller's own role is the last resort.
	task := sampleTask()
	task.OwnerRole = ""
	b := Build(task, nil, "review", Options{})
	if b.Contract.Role != "review" {
		t.Errorf("fallback should be the caller's role, got %q", b.Contract.Role)
	}
}

func TestFocusedPackExcludesSiblings(t *testing.T) {
	task := sampleTask()
	b := Build(task, nil, "", Options{SideKey: "ui"})
	md := Markdown(b, Options{SideKey: "ui"})

	if !strings.Contains(md, "交互说明") {
		t.Error("focused pack must include the focused side's segments")
	}
	if !strings.Contains(md, "| **→ ui**") {
		t.Error("the focused side should be marked in the table")
	}
	for _, sg := range b.Segments {
		if sg.Key == "接口契约" {
			t.Error("focused pack leaked a sibling side's segment")
		}
	}
}

// Empty skeleton segments are noise until they have content.
func TestEmptySegmentsOmittedByDefault(t *testing.T) {
	task := sampleTask()
	withEmpty := Build(task, nil, "", Options{IncludeEmpty: true})
	without := Build(task, nil, "", Options{})

	if len(withEmpty.Segments) != len(without.Segments)+1 {
		t.Fatalf("IncludeEmpty should add exactly the empty segment: %d vs %d",
			len(withEmpty.Segments), len(without.Segments))
	}
	for _, sg := range without.Segments {
		if strings.TrimSpace(sg.Body) == "" {
			t.Errorf("segment %q is empty but was included", sg.Key)
		}
	}
}

// Truncation must never eat the contract: it is the one part that has to
// survive for the receiver to know how to close the loop.
func TestTruncationKeepsContractAndAnnouncesItself(t *testing.T) {
	task := sampleTask()
	task.Segments = append(task.Segments, model.Segment{
		Key: "长文", Title: "长文", Body: strings.Repeat("细节。", 3000),
	})
	opt := Options{MaxChars: 1500}
	b := Build(task, nil, "", opt)
	if !b.Truncated {
		t.Fatal("expected truncation with a 1500-char cap and a 9000-char segment")
	}
	md := Markdown(b, opt)
	if !strings.Contains(md, "已截断") {
		t.Error("truncation must be announced, never silent")
	}
	if !strings.Contains(md, "## 交付契约") {
		t.Error("the contract must survive truncation")
	}
	if !strings.Contains(md, "kp task seg KP-7") {
		t.Error("the truncation notice should say how to fetch the full segment")
	}
}

func TestSlugKeepsCJK(t *testing.T) {
	cases := map[string]string{
		"goal":           "goal",
		"Acceptance":     "acceptance",
		"踩坑记录":           "踩坑记录",
		"踩坑 记录":          "踩坑-记录",
		"deploy / notes": "deploy-notes",
		"  --a--b--  ":   "a-b",
	}
	for in, want := range cases {
		if got := model.Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
