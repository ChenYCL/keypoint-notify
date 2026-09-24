// Package pack renders a task into a self-contained context bundle.
//
// This is the piece that makes the system usable by an agent that has never
// seen the task before: one request returns everything needed to start work,
// including the contract for reporting back. The rendering is deterministic so
// that identical state produces identical bytes — which is what makes it safe
// to diff, cache, or paste into a prompt.
package pack

import (
	"fmt"
	"strings"

	"github.com/ChenYCL/keypoint-notify/internal/model"
)

// Options controls what a pack includes.
type Options struct {
	// SideKey focuses the pack on one work face. When empty the pack covers the
	// whole task and every side's segments are listed in full.
	SideKey string
	// MaxChars caps the rendered markdown. Zero means the default (12000).
	// Bodies are truncated at segment boundaries and every truncation is
	// announced, never silent.
	MaxChars int
	// Reports is how many recent timeline entries to append. -1 disables.
	Reports int
	// IncludeEmpty keeps skeleton segments that have no body yet, as explicit
	// "（待补）" placeholders. Off by default so an empty task does not look
	// like it has content.
	IncludeEmpty bool
}

// DefaultMaxChars is the pack size cap when the caller does not set one.
const DefaultMaxChars = 12000

// Bundle is the structured form of a pack.
type Bundle struct {
	Task      model.Task      `json:"task"`
	Focus     string          `json:"focus,omitempty"` // "" or "side:<key>"
	FocusSide *model.Side     `json:"focus_side,omitempty"`
	Segments  []model.Segment `json:"segments"`
	Reports   []model.Report  `json:"reports,omitempty"`
	Contract  Contract        `json:"contract"`
	Truncated bool            `json:"truncated"`
	Dropped   []string        `json:"dropped,omitempty"` // segment keys left out
	Remaining int             `json:"remaining_chars"`
}

// Contract tells the receiver how to close the loop. It is rendered into the
// markdown tail so that pasting the pack into a fresh session carries the
// reporting instructions with it.
type Contract struct {
	TaskCode string   `json:"task_code"`
	SideKey  string   `json:"side_key,omitempty"`
	Role     string   `json:"role,omitempty"`
	Commands []string `json:"commands"`
}

// Build assembles a bundle from an already-loaded task.
func Build(t model.Task, reports []model.Report, actorRole string, opt Options) Bundle {
	if opt.MaxChars <= 0 {
		opt.MaxChars = DefaultMaxChars
	}
	b := Bundle{Task: t, Reports: reports, Remaining: opt.MaxChars}

	var focus *model.Side
	if opt.SideKey != "" {
		for i := range t.Sides {
			if t.Sides[i].Key == opt.SideKey || t.Sides[i].ID == opt.SideKey {
				focus = &t.Sides[i]
				break
			}
		}
	}
	if focus != nil {
		b.Focus = "side:" + focus.Key
		b.FocusSide = focus
	}

	// Segment selection order: task-level, then the focused side (or every side
	// when unfocused). Ordering matters because truncation eats from the tail,
	// and the task-level skeleton is what a receiver most needs first.
	segs := make([]model.Segment, 0, len(t.Segments))
	segs = append(segs, t.Segments...)
	if focus != nil {
		segs = append(segs, focus.Segments...)
	} else {
		for _, sd := range t.Sides {
			segs = append(segs, sd.Segments...)
		}
	}

	// Reserve room for the parts that are not segments: header, sides table and
	// the contract tail. Without this the contract gets truncated off the end,
	// which is the one thing that must always survive.
	overhead := len(renderHeader(t)) + len(renderSides(t, focus)) + len(renderContract(t, focus, actorRole)) + 512
	budget := opt.MaxChars - overhead
	if budget < 500 {
		budget = 500
	}

	used := 0
	for _, sg := range segs {
		body := strings.TrimSpace(sg.Body)
		if body == "" && !opt.IncludeEmpty {
			continue
		}
		label := "[" + sg.Key + "] " + sg.Title
		if sg.SideKey != "" || sg.SideID != "" {
			label += "  (" + sideKeyOf(t, sg.SideID) + ")"
		}
		cost := len(label) + len(body) + 16
		if used+cost > budget {
			// Fit a truncated head of this segment, then stop entirely.
			room := budget - used - len(label) - 64
			if room > 200 {
				sg.Body = truncate(body, room) + "\n\n…（本段被截断，取全文：`kp task seg " + t.Code + " " + sg.Key + "`）"
				b.Truncated = true
				b.Segments = append(b.Segments, sg)
				used = budget
				continue
			}
			b.Truncated = true
			b.Dropped = append(b.Dropped, sg.Key)
			continue
		}
		b.Segments = append(b.Segments, sg)
		used += cost
	}
	b.Remaining = opt.MaxChars - used - overhead
	if b.Remaining < 0 {
		b.Remaining = 0
	}

	// Whose voice the contract speaks in. A pack is usually generated for
	// someone else, so the receiver's role wins, then the task's owner, and the
	// caller's own role is only the last resort.
	role := ""
	if focus != nil {
		role = focus.AssigneeRole
	}
	if role == "" {
		role = t.OwnerRole
	}
	if role == "" {
		role = actorRole
	}
	b.Contract = buildContract(t, focus, role)
	return b
}

func sideKeyOf(t model.Task, sideID string) string {
	for _, sd := range t.Sides {
		if sd.ID == sideID {
			return sd.Key
		}
	}
	return "?"
}

func buildContract(t model.Task, focus *model.Side, role string) Contract {
	c := Contract{TaskCode: t.Code, Role: role}
	if focus != nil {
		c.SideKey = focus.Key
	}
	c.Commands = buildContractCommands(t, focus, role)
	return c
}

// stringOr returns v, or def when v is blank.
func stringOr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	// Prefer breaking on a line boundary so code blocks are not split mid-line.
	if i := strings.LastIndexByte(cut, '\n'); i > max/2 {
		cut = cut[:i]
	}
	return cut
}

// ---------------------------------------------------------------------------
// Markdown rendering
// ---------------------------------------------------------------------------

// Markdown renders the bundle as a single paste-ready document.
func Markdown(b Bundle, opt Options) string {
	var sb strings.Builder
	sb.WriteString(renderHeader(b.Task))
	sb.WriteString(renderIndex(b))
	sb.WriteString(renderSides(b.Task, b.FocusSide))
	sb.WriteString(renderSegments(b))
	sb.WriteString(renderReports(b.Reports))
	if len(b.Task.Attachments) > 0 {
		sb.WriteString(renderAttachments(b.Task.Attachments))
	}
	sb.WriteString(renderContract(b.Task, b.FocusSide, b.Contract.Role))
	if b.Truncated {
		fmt.Fprintf(&sb, "\n> ⚠️ 内容超出 %d 字符上限，已截断。遗漏分段：%s\n> 用 `kp task seg %s <key>` 取任意一段全文。\n",
			opt.MaxChars, strings.Join(b.Dropped, ", "), b.Task.Code)
	}
	return sb.String()
}

func renderHeader(t model.Task) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s · %s\n\n", t.Code, t.Title)
	bits := []string{stringOr(t.Kind, "feature"), stringOr(t.Priority, "P2"), statusLabel(t.Status)}
	if t.OwnerRole != "" {
		bits = append(bits, "负责角色 @"+t.OwnerRole)
	}
	if t.OwnerIdentity != "" {
		bits = append(bits, "负责身份 "+t.OwnerIdentity)
	}
	if len(t.Labels) > 0 {
		bits = append(bits, "标签 "+strings.Join(t.Labels, "/"))
	}
	fmt.Fprintf(&sb, "> %s · 更新于 %s\n", strings.Join(bits, " · "), t.UpdatedAt.Format("2006-01-02 15:04"))
	if strings.TrimSpace(t.Summary) != "" {
		fmt.Fprintf(&sb, ">\n> **一句话**：%s\n", strings.TrimSpace(t.Summary))
	}
	if len(t.Links) > 0 {
		sb.WriteString(">\n")
		for _, l := range t.Links {
			fmt.Fprintf(&sb, "> 🔗 %s: %s%s\n", l.Kind, l.URL, noteSuffix(l.Note))
		}
	}
	return sb.String()
}

func noteSuffix(note string) string {
	if strings.TrimSpace(note) == "" {
		return ""
	}
	return " — " + note
}

func statusLabel(s string) string {
	switch s {
	case model.StatusInbox:
		return "待整理(inbox)"
	case model.StatusReady:
		return "可开工(ready)"
	case model.StatusDoing:
		return "进行中(doing)"
	case model.StatusBlocked:
		return "阻塞(blocked)"
	case model.StatusReview:
		return "待审查(review)"
	case model.StatusDone:
		return "已完成(done)"
	case model.StatusArchived:
		return "已归档(archived)"
	}
	return s
}

// renderIndex is the one-line map of what segments exist. It costs a few
// tokens and saves an agent from fetching the task twice.
func renderIndex(b Bundle) string {
	keys := []string{}
	for _, sg := range b.Task.Segments {
		if strings.TrimSpace(sg.Body) == "" && !hasBody(sg) {
			keys = append(keys, sg.Key+":"+sg.Title+"(空)")
			continue
		}
		keys = append(keys, sg.Key+":"+sg.Title)
	}
	for _, sd := range b.Task.Sides {
		if b.FocusSide != nil && sd.ID != b.FocusSide.ID {
			continue
		}
		for _, sg := range sd.Segments {
			keys = append(keys, sd.Key+"/"+sg.Key+":"+sg.Title)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	return "\n**分段索引**：`" + strings.Join(keys, "` · `") + "`\n\n单段取用：`kp task seg " + b.Task.Code + " <key>`\n"
}

func hasBody(sg model.Segment) bool { return strings.TrimSpace(sg.Body) != "" }

func renderSides(t model.Task, focus *model.Side) string {
	if len(t.Sides) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## 工作面（sides）\n\n")
	sb.WriteString("| side | 状态 | 负责角色 | 负责身份 | 依赖 | 分支 |\n|---|---|---|---|---|---|\n")
	for _, sd := range t.Sides {
		mark := ""
		if focus != nil && sd.ID == focus.ID {
			mark = "**→ "
		}
		deps := "—"
		if len(sd.Deps) > 0 {
			deps = strings.Join(sd.Deps, ", ")
		}
		fmt.Fprintf(&sb, "| %s%s%s | %s | %s | %s | %s | %s |\n",
			mark, sd.Key, closeBold(mark), sideStatusLabel(sd.Status),
			orDash(sd.AssigneeRole, true), orDash(sd.AssigneeIdentity, false), deps, orDash(sd.Branch, false))
	}
	if focus != nil && len(focus.Deps) > 0 {
		fmt.Fprintf(&sb, "\n> 本工作面依赖：%s。开工前先确认它们的状态。\n", strings.Join(focus.Deps, ", "))
	}
	return sb.String()
}

func closeBold(mark string) string {
	if mark == "" {
		return ""
	}
	return "**"
}

func orDash(v string, at bool) string {
	if strings.TrimSpace(v) == "" {
		return "—"
	}
	if at {
		return "@" + v
	}
	return v
}

func sideStatusLabel(s string) string {
	switch s {
	case model.SideTodo:
		return "未开始(todo)"
	case model.SideDoing:
		return "进行中(doing)"
	case model.SideBlocked:
		return "阻塞(blocked)"
	case model.SideDone:
		return "已完成(done)"
	}
	return s
}

func renderSegments(b Bundle) string {
	if len(b.Segments) == 0 {
		return "\n_（本任务暂无分段内容）_\n"
	}
	var sb strings.Builder
	lastSide := "\x00"
	for _, sg := range b.Segments {
		sideKey := ""
		if sg.SideID != "" {
			sideKey = sideKeyOf(b.Task, sg.SideID)
		}
		if sideKey != lastSide {
			if sideKey != "" {
				fmt.Fprintf(&sb, "\n## —— side: %s ——\n", sideKey)
			}
			lastSide = sideKey
		}
		title := sg.Title
		if title == "" {
			title = sg.Key
		}
		fmt.Fprintf(&sb, "\n### [%s] %s\n\n", sg.Key, title)
		body := strings.TrimSpace(sg.Body)
		if body == "" {
			sb.WriteString("_（待补）_\n")
			continue
		}
		sb.WriteString(body)
		sb.WriteString("\n")
		for _, att := range sg.Attachments {
			if att.IsImage {
				fmt.Fprintf(&sb, "\n![%s](%s)\n", att.Name, att.URL)
			} else {
				fmt.Fprintf(&sb, "\n📎 [%s](%s)\n", att.Name, att.URL)
			}
		}
	}
	return sb.String()
}

func renderReports(reports []model.Report) string {
	if len(reports) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## 最近上报（时间线，旧→新）\n")
	// Reports arrive newest-first from the store; the timeline reads better
	// oldest-first.
	for i := len(reports) - 1; i >= 0; i-- {
		r := reports[i]
		who := r.IdentityName
		if who == "" {
			who = r.IdentityID
		}
		scope := ""
		if r.SideKey != "" {
			scope = " @side:" + r.SideKey
		}
		fmt.Fprintf(&sb, "\n**[%s] %s · %s%s** — %s\n",
			reportTypeLabel(r.Type), who, orDefault(r.Role, "member"), scope,
			r.CreatedAt.Format("01-02 15:04"))
		if body := strings.TrimSpace(r.Body); body != "" {
			for _, line := range strings.Split(body, "\n") {
				fmt.Fprintf(&sb, "> %s\n", line)
			}
		}
		for _, sg := range r.Segments {
			fmt.Fprintf(&sb, "> **[%s] %s**\n", sg.Key, sg.Title)
			for _, line := range strings.Split(strings.TrimRight(sg.Body, "\n"), "\n") {
				fmt.Fprintf(&sb, "> %s\n", line)
			}
		}
		for _, att := range r.Attachments {
			// Images render as images so a model reading the pack can actually
			// look at the evidence, not just learn that a file exists.
			if att.IsImage {
				fmt.Fprintf(&sb, "> ![%s](%s)\n", att.Name, att.URL)
			} else {
				fmt.Fprintf(&sb, "> 📎 [%s](%s)\n", att.Name, att.URL)
			}
		}
	}
	return sb.String()
}

func reportTypeLabel(t string) string {
	switch t {
	case model.ReportProgress:
		return "进展"
	case model.ReportBlocker:
		return "阻塞"
	case model.ReportDecision:
		return "决策"
	case model.ReportHandoff:
		return "交接"
	case model.ReportResult:
		return "结果"
	case model.ReportQuestion:
		return "提问"
	case model.ReportFinding:
		return "论点"
	}
	return t
}

func renderAttachments(atts []model.Attachment) string {
	var sb strings.Builder
	sb.WriteString("\n## 附件\n\n")
	for _, att := range atts {
		kind := "📎"
		if att.IsImage {
			kind = "🖼"
		}
		fmt.Fprintf(&sb, "- %s [%s](%s) · %s · %s\n", kind, att.Name, att.URL,
			att.MIME, humanSize(att.Size))
	}
	return sb.String()
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func renderContract(t model.Task, focus *model.Side, role string) string {
	var sb strings.Builder
	sb.WriteString("\n---\n\n## 交付契约（承接方必读）\n\n")
	if focus != nil {
		fmt.Fprintf(&sb, "你正在承接 **%s** 的工作面 **%s**", t.Code, focus.Key)
	} else {
		fmt.Fprintf(&sb, "你正在处理任务 **%s**", t.Code)
	}
	if role != "" {
		fmt.Fprintf(&sb, "，以角色 **@%s**", role)
	}
	sb.WriteString(" 的身份推进。\n\n")
	sb.WriteString("**边界**：只改本工作面相关的代码/配置；需要动到别的面，先上报交接，不要顺手改。\n\n")
	sb.WriteString("**完成后**（不要静默结束）：\n\n```bash\n")
	for _, c := range buildContractCommands(t, focus, role) {
		sb.WriteString(c + "\n")
	}
	sb.WriteString("```\n")
	if focus != nil && len(focus.Deps) > 0 {
		fmt.Fprintf(&sb, "\n**依赖**：本面依赖 %s，若其未完成，先上报 `--type blocker`。\n",
			strings.Join(focus.Deps, ", "))
	}
	return sb.String()
}

func buildContractCommands(t model.Task, focus *model.Side, role string) []string {
	sideArg := ""
	if focus != nil {
		sideArg = " --side " + focus.Key
	}
	out := []string{
		fmt.Sprintf("kp report %s%s --type result -m \"改动摘要 + 验证方式 + 遗留风险\"", t.Code, sideArg),
		fmt.Sprintf("kp report %s%s --type blocker -m \"卡在哪、需要什么\" --mention @%s", t.Code, sideArg, orDefault(role, "member")),
		fmt.Sprintf("kp report %s%s --type question -m \"要确认的点\"", t.Code, sideArg),
		fmt.Sprintf("kp report %s%s --type result --attach 截图.png -m \"带证据\"", t.Code, sideArg),
		fmt.Sprintf("kp task pack %s%s   # 之后有更新就重跑这条拿最新上下文", t.Code, sideArg),
	}
	return out
}
