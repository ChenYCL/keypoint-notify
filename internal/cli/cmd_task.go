package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/light/keypoint-notify/internal/client"
	"github.com/light/keypoint-notify/internal/model"
)

// ---------------------------------------------------------------------------
// whoami / board
// ---------------------------------------------------------------------------

func (a *app) whoami() int {
	var r struct {
		Identity struct {
			ID         string   `json:"id"`
			Name       string   `json:"name"`
			Kind       string   `json:"kind"`
			Roles      []string `json:"roles"`
			ActiveRole string   `json:"active_role"`
			KeyPrefix  string   `json:"key_prefix"`
		} `json:"identity"`
		Role         string   `json:"role"`
		Unread       int      `json:"unread"`
		OpenSides    int      `json:"open_sides"`
		Capabilities []string `json:"capabilities"`
		EntryPoints  []string `json:"entry_points"`
	}
	if err := a.cl.Get("/api/v1/whoami", &r); err != nil {
		return a.fail(err)
	}
	if a.jsonOut {
		a.out(r)
		return ExitOK
	}
	fmt.Printf("身份   %s (%s)\n", r.Identity.Name, r.Identity.Kind)
	fmt.Printf("角色   %s   激活：@%s\n", strings.Join(r.Identity.Roles, ", "), r.Role)
	fmt.Printf("key    %s\n", r.Identity.KeyPrefix)
	fmt.Printf("未读   %d    我的工作面 %d 个\n", r.Unread, r.OpenSides)
	fmt.Printf("服务端 %s\n", a.cfg.Server)
	fmt.Println("\n下一步：")
	for _, e := range r.EntryPoints {
		fmt.Println("  " + e)
	}
	return ExitOK
}

func (a *app) board() int {
	var r struct {
		Identity struct {
			Name string `json:"name"`
		} `json:"identity"`
		Role       string       `json:"role"`
		Unread     int          `json:"unread"`
		MySides    []model.Side `json:"my_sides"`
		MyTasks    []model.Task `json:"my_tasks"`
		NextAction []string     `json:"next_actions"`
	}
	if err := a.cl.Get("/api/v1/me/board", &r); err != nil {
		return a.fail(err)
	}
	if a.jsonOut {
		a.out(r)
		return ExitOK
	}
	fmt.Printf("%s  @%s   未读 %d\n\n", r.Identity.Name, r.Role, r.Unread)
	if len(r.MySides) == 0 {
		fmt.Println("手上没有工作面。")
	} else {
		rows := [][]string{}
		for _, s := range r.MySides {
			deps := strings.Join(s.Deps, ",")
			rows = append(rows, []string{s.Key, s.Status, s.Title, deps})
		}
		fmt.Print(table([]string{"SIDE", "状态", "标题", "依赖"}, rows))
	}
	if len(r.MyTasks) > 0 {
		fmt.Println("\n我负责的任务：")
		rows := [][]string{}
		for _, t := range r.MyTasks {
			rows = append(rows, []string{t.Code, t.Status, t.Priority, truncateRunes(t.Title, 46)})
		}
		fmt.Print(table([]string{"CODE", "状态", "P", "标题"}, rows))
	}
	if len(r.NextAction) > 0 {
		fmt.Println()
		for _, h := range r.NextAction {
			fmt.Println("→ " + h)
		}
	}
	return ExitOK
}

// ---------------------------------------------------------------------------
// kp task ...
// ---------------------------------------------------------------------------

func (a *app) task(args []string) int {
	if len(args) == 0 {
		printTaskHelp()
		return ExitOK
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		return a.taskList(rest)
	case "show", "get":
		return a.taskShow(rest)
	case "pack":
		return a.taskPack(rest)
	case "seg", "segment":
		return a.taskSegment(rest)
	case "side", "sides":
		return a.taskSide(rest)
	case "status", "st":
		return a.taskStatus(rest)
	case "edit":
		return a.taskEdit(rest)
	case "new", "create", "add":
		return a.taskNew(rest)
	case "rm", "del", "delete":
		return a.taskDelete(rest)
	case "help", "-h", "--help":
		printTaskHelp()
		return ExitOK
	default:
		return a.usage("未知子命令 task "+sub, "试试 kp task --help")
	}
}

func (a *app) taskList(args []string) int {
	fs := flag.NewFlagSet("kp task list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	status := fs.String("status", "", "逗号分隔：inbox,ready,doing,blocked,review,done")
	kind := fs.String("kind", "", "bug,feature,chore,research,review,incident")
	priority := fs.String("priority", "", "P0,P1,P2,P3")
	role := fs.String("role", "", "只看指派给这个角色的")
	assigned := fs.String("assigned", "", "me（指派给我的），或身份名")
	query := fs.String("q", "", "全文搜 code/标题/摘要/分段")
	since := fs.String("since", "", "7d / 2026-09-01 / RFC3339")
	limit := fs.Int("limit", 50, "最多返回多少条")
	group := fs.Bool("group", false, "按状态分组输出")
	lite := fs.Bool("lite", false, "精简字段（省 token）")
	archived := fs.Bool("archived", false, "包含已归档")
	fs.Usage = func() { printTaskHelp() }
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		printTaskHelp()
		return ExitOK
	}
	if err := a.parseSub(fs, args); err != nil {
		return ExitUsage
	}
	path := "/api/v1/tasks" + client.Q(
		"status", *status, "kind", *kind, "priority", *priority,
		"role", *role, "assigned", *assigned, "q", *query, "since", *since,
		"limit", itoaClamp(*limit), "archived", boolQ(*archived),
		"view", map[bool]string{true: "lite", false: ""}[*lite],
		"group", map[bool]string{true: "status", false: ""}[*group],
	)
	var raw map[string]any
	if err := a.cl.Get(path, &raw); err != nil {
		return a.fail(err)
	}
	if a.jsonOut {
		a.out(raw)
		return ExitOK
	}

	if *group {
		cols, _ := raw["columns"].(map[string]any)
		order := []string{"inbox", "ready", "doing", "blocked", "review", "done"}
		for _, st := range order {
			items, _ := cols[st].([]any)
			if len(items) == 0 {
				continue
			}
			fmt.Printf("\n▌%s (%d)\n", statusLabelCN(st), len(items))
			rows := [][]string{}
			for _, it := range items {
				m, _ := it.(map[string]any)
				rows = append(rows, []string{
					str(m["code"]), str(m["priority"]), str(m["kind"]),
					truncateRunes(str(m["title"]), 52),
					str(m["owner_role"]),
					fmtWhenShort(str(m["updated_at"])),
				})
			}
			fmt.Print(table([]string{"CODE", "P", "类型", "标题", "角色", "更新"}, rows))
		}
		fmt.Println()
		return ExitOK
	}

	items, _ := raw["tasks"].([]any)
	if len(items) == 0 {
		fmt.Println("没有匹配的任务。")
		return ExitOK
	}
	rows := [][]string{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		rows = append(rows, []string{
			str(m["code"]), str(m["status"]), str(m["priority"]), str(m["kind"]),
			truncateRunes(str(m["title"]), 50), str(m["owner_role"]),
			fmtWhenShort(str(m["updated_at"])),
		})
	}
	fmt.Print(table([]string{"CODE", "状态", "P", "类型", "标题", "角色", "更新"}, rows))
	if tr, ok := raw["truncated"].(bool); ok && tr {
		fmt.Println("\n（还有更多，用 --limit 或 --offset 翻页）")
	}
	fmt.Printf("\n共 %v 条。看详情：kp task show %s\n", raw["count"], str(firstCode(items)))
	return ExitOK
}

func firstCode(items []any) any {
	if len(items) == 0 {
		return "KP-1"
	}
	m, _ := items[0].(map[string]any)
	return m["code"]
}

func statusLabelCN(s string) string {
	switch s {
	case model.StatusInbox:
		return "待整理"
	case model.StatusReady:
		return "可开工"
	case model.StatusDoing:
		return "进行中"
	case model.StatusBlocked:
		return "阻塞"
	case model.StatusReview:
		return "待审查"
	case model.StatusDone:
		return "已完成"
	case model.StatusArchived:
		return "已归档"
	}
	return s
}

func (a *app) taskShow(args []string) int {
	fs := flag.NewFlagSet("kp task show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	side := fs.String("side", "", "只聚焦某个工作面")
	reports := fs.Int("reports", 5, "带上最近 N 条上报")
	if err := a.parseSub(fs, args); err != nil {
		return ExitUsage
	}
	code := fs.Arg(0)
	if code == "" {
		return a.usage("用法：kp task show <code>", "例如 kp task show KP-12")
	}
	path := "/api/v1/tasks/" + code + client.Q("reports", itoaClamp(*reports))
	var raw map[string]any
	if err := a.cl.Get(path, &raw); err != nil {
		return a.fail(err)
	}
	task, _ := raw["task"].(map[string]any)
	target := raw
	if task != nil {
		target = task
	}
	if a.jsonOut {
		a.out(raw)
		return ExitOK
	}
	printTaskHuman(target, raw["reports"], *side)
	return ExitOK
}

func printTaskHuman(t map[string]any, reports any, focusSide string) {
	fmt.Printf("%s · %s\n", str(t["code"]), str(t["title"]))
	meta := []string{str(t["kind"]), str(t["priority"]), statusLabelCN(str(t["status"]))}
	if r := str(t["owner_role"]); r != "" {
		meta = append(meta, "@"+r)
	}
	if r := str(t["owner_identity"]); r != "" {
		meta = append(meta, r)
	}
	fmt.Printf("  %s   更新于 %s\n", strings.Join(meta, " · "), str(t["updated_at"]))
	if s := str(t["summary"]); s != "" {
		fmt.Printf("  %s\n", s)
	}

	if sides, ok := t["sides"].([]any); ok && len(sides) > 0 {
		fmt.Println("\n工作面：")
		rows := [][]string{}
		for _, sv := range sides {
			s, _ := sv.(map[string]any)
			deps := "—"
			if d, ok := s["deps"].([]any); ok && len(d) > 0 {
				parts := make([]string, 0, len(d))
				for _, x := range d {
					parts = append(parts, str(x))
				}
				deps = strings.Join(parts, ",")
			}
			rows = append(rows, []string{
				str(s["key"]), sideStatusCN(str(s["status"])),
				orDashStr(str(s["assignee_role"]), "@"), orDashStr(str(s["assignee_identity"]), ""),
				deps, str(s["title"]),
			})
		}
		fmt.Print(table([]string{"SIDE", "状态", "角色", "身份", "依赖", "标题"}, rows))
	}

	// Segment bodies: task-level always, plus the focused side's when asked.
	fmt.Println("\n分段：")
	printSegments(t["segments"], "")
	if focusSide != "" {
		for _, sv := range anySlice(t["sides"]) {
			s, _ := sv.(map[string]any)
			if str(s["key"]) != focusSide {
				continue
			}
			fmt.Printf("\n--- side: %s 的分段 ---\n", focusSide)
			printSegments(s["segments"], focusSide)
		}
	} else if sides, ok := t["sides"].([]any); ok {
		for _, sv := range sides {
			s, _ := sv.(map[string]any)
			if segs, ok := s["segments"].([]any); ok && len(segs) > 0 {
				fmt.Printf("\n--- side: %s ---\n", str(s["key"]))
				printSegments(s["segments"], str(s["key"]))
			}
		}
	}

	if reps, ok := reports.([]any); ok && len(reps) > 0 {
		fmt.Println("\n最近上报：")
		for i := len(reps) - 1; i >= 0; i-- {
			r, _ := reps[i].(map[string]any)
			fmt.Printf("  [%s] %s %s  %s\n", str(r["type"]), str(r["identity_name"]),
				orDashStr(str(r["side_key"]), "side:"), fmtWhenShort(str(r["created_at"])))
			for _, line := range strings.Split(strings.TrimRight(str(r["body"]), "\n"), "\n") {
				fmt.Printf("      %s\n", line)
			}
		}
	}
	fmt.Printf("\n开工：kp task pack %s%s\n", str(t["code"]),
		map[bool]string{true: " --side " + focusSide, false: ""}[focusSide != ""])
}

func printSegments(v any, sideKey string) {
	segs, _ := v.([]any)
	shown := 0
	for _, gv := range segs {
		g, _ := gv.(map[string]any)
		body := str(g["body"])
		if strings.TrimSpace(body) == "" {
			continue
		}
		shown++
		fmt.Printf("\n  [%s] %s\n", str(g["key"]), str(g["title"]))
		for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
			fmt.Printf("      %s\n", line)
		}
	}
	if shown == 0 {
		fmt.Println("  （还没有内容；用 kp task seg set <code> <key> --file - 写入）")
	}
}

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func orDashStr(s, prefix string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return prefix + s
}

// ---------------------------------------------------------------------------
// pack / seg
// ---------------------------------------------------------------------------

func (a *app) taskPack(args []string) int {
	fs := flag.NewFlagSet("kp task pack", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	side := fs.String("side", "", "聚焦某个工作面")
	maxChars := fs.Int("max-chars", 0, "渲染上限，默认 12000")
	reports := fs.Int("reports", 5, "带上最近 N 条上报；-1 不带")
	format := fs.String("format", "md", "md 或 json")
	out := fs.String("out", "", "写到文件而不是 stdout")
	empty := fs.Bool("empty", false, "带上还没写内容的固定分段")
	fs.Usage = func() {
		fmt.Print(`kp task pack <code> — 取"开工上下文包"

  kp task pack KP-12                        整任务的包
  kp task pack KP-12 --side ui              只取 ui 工作面的包
  kp task pack KP-12 --side ui | pbcopy     直接进剪贴板，粘进 Claude Code
  kp task pack KP-12 --format json          结构化（给程序用）
  kp task pack KP-12 --max-chars 4000       控制体积

包里包含：任务头、分段索引、工作面表、各段正文、最近上报、附件链接，
以及**交付契约**——告诉承接方做完该怎么上报。
`)
	}
	if err := a.parseSub(fs, args); err != nil {
		return ExitUsage
	}
	code := fs.Arg(0)
	if code == "" {
		return a.usage("用法：kp task pack <code>", "例如 kp task pack KP-12 --side ui")
	}
	path := "/api/v1/tasks/" + code + "/pack" + client.Q(
		"side", *side, "max_chars", itoaClamp(*maxChars), "reports", itoaClamp(*reports),
		"format", *format, "empty", boolQ(*empty))

	var body string
	if *format == "json" {
		var raw map[string]any
		if err := a.cl.Get(path, &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		data, _ := json.MarshalIndent(raw, "", "  ")
		body = string(data)
	} else {
		text, err := a.cl.GetText(path)
		if err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(map[string]any{"task": code, "side": *side, "format": "md", "markdown": text})
			return ExitOK
		}
		body = text
	}

	if *out != "" {
		if err := os.WriteFile(*out, []byte(body), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "✗ 写文件失败:", err)
			return ExitError
		}
		fmt.Fprintf(os.Stderr, "✓ 已写入 %s（%d 字符）\n", *out, len(body))
		return ExitOK
	}
	fmt.Print(body)
	return ExitOK
}

func (a *app) taskSegment(args []string) int {
	if len(args) == 0 {
		fmt.Print(`kp task seg — 分段

  kp task seg KP-12 goal                 取单段正文（纯文本，可复制）
  kp task seg KP-12 goal --prompt        包一层"请基于它工作"的上下文
  kp task seg KP-12 goal | pbcopy
  kp task seg set KP-12 goal --file -    从 stdin 写入（Append 用 --append）
  kp task seg set KP-12 踩坑记录 --title "踩坑记录" -m "..."

固定骨架 key：context goal deliverable constraint acceptance interface files
`)
		return ExitOK
	}
	if args[0] == "set" {
		return a.segmentSet(args[1:])
	}
	fs := flag.NewFlagSet("kp task seg", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	prompt := fs.Bool("prompt", false, "包一层可粘贴的上下文")
	format := fs.String("format", "", "json 看结构化字段")
	if err := a.parseSub(fs, args); err != nil {
		return ExitUsage
	}
	code, key := fs.Arg(0), fs.Arg(1)
	if code == "" || key == "" {
		return a.usage("用法：kp task seg <code> <key>", "例如 kp task seg KP-12 goal")
	}
	q := client.Q("format", *format)
	if *prompt {
		q = client.Q("format", "prompt")
	}
	if *format == "json" {
		var raw map[string]any
		if err := a.cl.Get("/api/v1/tasks/"+code+"/segments/"+key+q, &raw); err != nil {
			return a.fail(err)
		}
		a.out(raw)
		if !a.jsonOut {
			data, _ := json.MarshalIndent(raw, "", "  ")
			fmt.Println(string(data))
		}
		return ExitOK
	}
	text, err := a.cl.GetText("/api/v1/tasks/" + code + "/segments/" + key + q)
	if err != nil {
		return a.fail(err)
	}
	if a.jsonOut {
		a.out(map[string]any{"task": code, "key": key, "text": text})
		return ExitOK
	}
	fmt.Print(text)
	return ExitOK
}

func (a *app) segmentSet(args []string) int {
	fs := flag.NewFlagSet("kp task seg set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	title := fs.String("title", "", "分段标题（自由分段用）")
	sideKey := fs.String("side", "", "写到某个工作面下（骨架 key 不能写到 side）")
	format := fs.String("format", "md", "md / text / code")
	file := fs.String("file", "", "从文件读正文；连字符 - 表示 stdin")
	msg := fs.String("m", "", "直接给正文")
	appendMode := fs.Bool("append", false, "追加到现有正文而不是替换")
	if err := a.parseSub(fs, args); err != nil {
		return ExitUsage
	}
	code, key := fs.Arg(0), fs.Arg(1)
	if code == "" || key == "" {
		return a.usage("用法：kp task seg set <code> <key> [-m 正文 | --file -]",
			"例如 echo '修复方案' | kp task seg set KP-12 goal --file -")
	}
	body := *msg
	switch {
	case *file == "-":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return a.fail(err)
		}
		body = string(data)
	case *file != "":
		data, err := os.ReadFile(*file)
		if err != nil {
			fmt.Fprintln(os.Stderr, "✗ 读文件失败:", err)
			return ExitError
		}
		body = string(data)
	}
	if body == "" && *file == "" && *msg == "" {
		return a.usage("没有正文", "用 -m \"...\"、--file <path> 或 --file - 从 stdin 读")
	}
	var raw map[string]any
	err := a.cl.Post("/api/v1/tasks/"+code+"/segments", map[string]any{
		"key": key, "title": *title, "body": body,
		"side_key": *sideKey, "format": *format, "append": *appendMode,
	}, &raw)
	if err != nil {
		return a.fail(err)
	}
	if a.jsonOut {
		a.out(raw)
		return ExitOK
	}
	fmt.Printf("✓ %s [%s] 已保存（%d 字符）\n", code, key, len([]rune(body)))
	return ExitOK
}

// ---------------------------------------------------------------------------
// sides
// ---------------------------------------------------------------------------

func (a *app) taskSide(args []string) int {
	if len(args) == 0 {
		fmt.Print(`kp task side — 工作面

  kp task side ls KP-12 [--assignable]
  kp task side add KP-12 ui --title "前端交互" --role frontend --deps api
  kp task side assign KP-12 ui --role frontend [--identity alice] [--status doing]
  kp task side assign KP-12 ui --unassign
  kp task side rm KP-12 ui

工作面 = 一个任务的并行工作片。每片有自己的负责人、状态、依赖和分段；
assign 之后对方用 ` + "`kp task pack KP-12 --side ui`" + ` 就能独立开工。
`)
		return ExitOK
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "ls", "list":
		fs := flag.NewFlagSet("kp task side ls", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		assignable := fs.Bool("assignable", false, "只看还没指派的")
		if err := a.parseSub(fs, rest); err != nil {
			return ExitUsage
		}
		code := fs.Arg(0)
		if code == "" {
			return a.usage("用法：kp task side ls <code>", "")
		}
		var raw map[string]any
		if err := a.cl.Get("/api/v1/tasks/"+code+"/sides"+client.Q("assignable", boolQ(*assignable)), &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		rows := [][]string{}
		for _, sv := range anySlice(raw["sides"]) {
			s, _ := sv.(map[string]any)
			deps := []string{}
			for _, d := range anySlice(s["deps"]) {
				deps = append(deps, str(d))
			}
			rows = append(rows, []string{
				str(s["key"]), sideStatusCN(str(s["status"])),
				orDashStr(str(s["assignee_role"]), "@"), orDashStr(str(s["assignee_identity"]), ""),
				orDashStr(strings.Join(deps, ","), ""), str(s["title"]),
			})
		}
		if len(rows) == 0 {
			fmt.Println("没有工作面。")
			return ExitOK
		}
		fmt.Print(table([]string{"SIDE", "状态", "角色", "身份", "依赖", "标题"}, rows))
		return ExitOK

	case "add":
		fs := flag.NewFlagSet("kp task side add", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		title := fs.String("title", "", "显示名")
		role := fs.String("role", "", "指派给角色")
		identity := fs.String("identity", "", "指派给具体身份名")
		deps := fs.String("deps", "", "依赖的 side key，逗号分隔")
		repo := fs.String("repo", "", "仓库")
		branch := fs.String("branch", "", "分支")
		if err := a.parseSub(fs, rest); err != nil {
			return ExitUsage
		}
		code, key := fs.Arg(0), fs.Arg(1)
		if code == "" || key == "" {
			return a.usage("用法：kp task side add <code> <key>", "例如 kp task side add KP-12 ui --role frontend")
		}
		var raw map[string]any
		if err := a.cl.Post("/api/v1/tasks/"+code+"/sides", map[string]any{
			"key": key, "title": *title, "assignee_role": *role, "assignee_identity": *identity,
			"deps": splitCSV(*deps), "repo": *repo, "branch": *branch,
		}, &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		fmt.Printf("✓ 已加工作面 %s\n", key)
		if b, ok := raw["blocked_by"].([]any); ok && len(b) > 0 {
			parts := []string{}
			for _, x := range b {
				parts = append(parts, str(x))
			}
			fmt.Printf("  注意：依赖未完成 → %s\n", strings.Join(parts, ", "))
		}
		if p, ok := raw["pack"].(string); ok {
			fmt.Printf("  开工包：%s\n", p)
		}
		return ExitOK

	case "assign":
		fs := flag.NewFlagSet("kp task side assign", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		role := fs.String("role", "", "指派给角色")
		identity := fs.String("identity", "", "指派给具体身份名")
		status := fs.String("status", "", "todo/doing/blocked/done")
		deps := fs.String("deps", "", "覆盖依赖列表，逗号分隔")
		unassign := fs.Bool("unassign", false, "取消指派")
		if err := a.parseSub(fs, rest); err != nil {
			return ExitUsage
		}
		code, key := fs.Arg(0), fs.Arg(1)
		if code == "" || key == "" {
			return a.usage("用法：kp task side assign <code> <key> [--role R|--identity I]", "")
		}
		payload := map[string]any{"unassign": *unassign}
		if *role != "" {
			payload["assignee_role"] = *role
		}
		if *identity != "" {
			payload["assignee_identity"] = *identity
		}
		if *status != "" {
			payload["status"] = *status
		}
		if *deps != "" {
			payload["deps"] = splitCSV(*deps)
		}
		var raw map[string]any
		if err := a.cl.Patch("/api/v1/tasks/"+code+"/sides/"+key, payload, &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		s, _ := raw["side"].(map[string]any)
		fmt.Printf("✓ %s/%s → %s  @%s %s\n", code, key, sideStatusCN(str(s["status"])),
			str(s["assignee_role"]), str(s["assignee_identity"]))
		if b, ok := raw["blocked_by"].([]any); ok && len(b) > 0 {
			parts := []string{}
			for _, x := range b {
				parts = append(parts, str(x))
			}
			fmt.Printf("  依赖未完成：%s\n", strings.Join(parts, ", "))
		}
		return ExitOK

	case "rm", "del":
		if len(rest) < 2 {
			return a.usage("用法：kp task side rm <code> <key>", "")
		}
		var raw map[string]any
		if err := a.cl.Delete("/api/v1/tasks/"+rest[0]+"/sides/"+rest[1], &raw); err != nil {
			return a.fail(err)
		}
		a.out(raw)
		if !a.jsonOut {
			fmt.Printf("✓ 已删除 %s/%s\n", rest[0], rest[1])
		}
		return ExitOK
	default:
		return a.usage("未知子命令 task side "+sub, "kp task side 看用法")
	}
}

func sideStatusCN(s string) string {
	switch s {
	case model.SideTodo:
		return "未开始"
	case model.SideDoing:
		return "进行中"
	case model.SideBlocked:
		return "阻塞"
	case model.SideDone:
		return "已完成"
	}
	return s
}

// ---------------------------------------------------------------------------
// status / edit / new / rm
// ---------------------------------------------------------------------------

func (a *app) taskStatus(args []string) int {
	if len(args) < 2 {
		return a.usage("用法：kp task status <code> <新状态>",
			"状态："+strings.Join([]string{model.StatusInbox, model.StatusReady, model.StatusDoing,
				model.StatusBlocked, model.StatusReview, model.StatusDone, model.StatusArchived}, " "))
	}
	var raw map[string]any
	if err := a.cl.Patch("/api/v1/tasks/"+args[0], map[string]any{"status": args[1]}, &raw); err != nil {
		return a.fail(err)
	}
	if a.jsonOut {
		a.out(raw)
		return ExitOK
	}
	t, _ := raw["task"].(map[string]any)
	fmt.Printf("✓ %s → %s\n", str(t["code"]), statusLabelCN(str(t["status"])))
	return ExitOK
}

func (a *app) taskEdit(args []string) int {
	fs := flag.NewFlagSet("kp task edit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	title := fs.String("title", "", "标题")
	summary := fs.String("summary", "", "一句话摘要")
	kind := fs.String("kind", "", "bug/feature/chore/research/review/incident")
	priority := fs.String("priority", "", "P0..P3")
	role := fs.String("role", "", "负责角色")
	labels := fs.String("label", "", "标签，逗号分隔")
	if err := a.parseSub(fs, args); err != nil {
		return ExitUsage
	}
	code := fs.Arg(0)
	if code == "" {
		return a.usage("用法：kp task edit <code> [--title ... --priority P1]", "")
	}
	payload := map[string]any{}
	if *title != "" {
		payload["title"] = *title
	}
	if *summary != "" {
		payload["summary"] = *summary
	}
	if *kind != "" {
		payload["kind"] = *kind
	}
	if *priority != "" {
		payload["priority"] = *priority
	}
	if *role != "" {
		payload["owner_role"] = *role
	}
	if *labels != "" {
		payload["labels"] = splitCSV(*labels)
	}
	if len(payload) == 0 {
		return a.usage("没有任何要改的字段", "用 --title/--summary/--kind/--priority/--role/--label")
	}
	var raw map[string]any
	if err := a.cl.Patch("/api/v1/tasks/"+code, payload, &raw); err != nil {
		return a.fail(err)
	}
	if a.jsonOut {
		a.out(raw)
		return ExitOK
	}
	fmt.Printf("✓ %s 已更新：%v\n", code, raw["changed"])
	return ExitOK
}

// taskNew accepts either flags or a JSON body on stdin.
//
// The stdin path is the one the skill drives: a model that has just read a
// session can emit the whole task — skeleton segments, sides, assignments — as
// one document instead of assembling a long command line.
func (a *app) taskNew(args []string) int {
	fs := flag.NewFlagSet("kp task new", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	title := fs.String("title", "", "标题（必填，除非用 --from-json）")
	summary := fs.String("summary", "", "一句话摘要")
	kind := fs.String("kind", "feature", "bug/feature/chore/research/review/incident")
	priority := fs.String("priority", "P2", "P0..P3")
	status := fs.String("status", "", "初始状态，默认 inbox")
	role := fs.String("role", "", "负责角色，默认你的激活角色")
	identity := fs.String("identity", "", "负责身份，默认你自己")
	labels := fs.String("label", "", "标签，逗号分隔")
	repo := fs.String("repo", "", "关联仓库/PR/文档 URL")
	segments := fs.String("seg", "", "分段，形如 goal=目标文本；可重复用 \n 分隔多条")
	context := fs.String("context", "", "背景（等价 --seg context=...）")
	goal := fs.String("goal", "", "目标")
	deliverable := fs.String("deliverable", "", "交付物")
	constraint := fs.String("constraint", "", "约束")
	acceptance := fs.String("acceptance", "", "验收标准")
	iface := fs.String("interface", "", "接口/契约")
	files := fs.String("files", "", "相关文件")
	fromJSON := fs.String("from-json", "", "从 JSON 读整个请求体；连字符 - 表示 stdin")
	dryRun := fs.Bool("dry-run", false, "只打印将发送的 JSON，不提交")

	fs.Usage = func() {
		fmt.Print(`kp task new — 建任务

  kp task new --title "登录页验证码倒计时错位" --kind bug --priority P1 \
      --goal "修复切后台后倒计时错位" --acceptance "切后台 60s 回来仍准确"

  kp task new --from-json -            ← agent 用：从 stdin 读完整 JSON

--from-json 的 JSON 形状（与 POST /api/v1/tasks 一致）:
  {
    "title": "…", "kind": "bug", "priority": "P1",
    "summary": "一句话",
    "owner_role": "backend",
    "labels": ["auth"],
    "links": [{"kind":"repo","url":"https://..."}],
    "segments": {"context":"…","goal":"…","deliverable":"…",
                 "constraint":"…","acceptance":"…","interface":"…","files":"…",
                 "踩坑记录":"自由分段"},
    "sides": [
      {"key":"api","title":"接口","assignee_role":"backend",
       "segments":{"接口契约":"…"}},
      {"key":"ui","title":"前端","assignee_role":"frontend","deps":["api"],
       "status":"todo"}
    ],
    "watchers": ["alice"],
    "notify": ["@review"]
  }

固定骨架分段 key 建议至少填 goal 与 acceptance —— 没有验收标准的任务，
承接方只能猜。
`)
	}
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fs.Usage()
		return ExitOK
	}
	if err := a.parseSub(fs, args); err != nil {
		return ExitUsage
	}

	var payload map[string]any

	if *fromJSON != "" {
		var data []byte
		var err error
		if *fromJSON == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(*fromJSON)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "✗ 读 JSON 失败:", err)
			return ExitError
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			fmt.Fprintf(os.Stderr, "✗ JSON 解析失败: %v\n", err)
			fmt.Fprintln(os.Stderr, "  → 严格模式：字段名拼错会直接报错。字段表见 docs/api.md")
			return ExitError
		}
	} else {
		if *title == "" {
			return a.usage("--title 必填", "或改用 --from-json - 从 stdin 读完整 JSON")
		}
		payload = map[string]any{
			"title": *title, "kind": *kind, "priority": *priority,
			"summary": *summary, "owner_role": *role, "owner_identity": *identity,
			"labels": splitCSV(*labels),
		}
		if *status != "" {
			payload["status"] = *status
		}
		if *repo != "" {
			payload["links"] = []map[string]string{{"kind": "url", "url": *repo}}
		}
		segs := map[string]string{}
		for k, v := range map[string]string{
			"context": *context, "goal": *goal, "deliverable": *deliverable,
			"constraint": *constraint, "acceptance": *acceptance,
			"interface": *iface, "files": *files,
		} {
			if strings.TrimSpace(v) != "" {
				segs[k] = v
			}
		}
		// --seg allows free-form extras and is repeatable via newlines.
		for _, line := range strings.Split(*segments, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				fmt.Fprintf(os.Stderr, "✗ --seg 格式应为 key=正文，收到 %q\n", line)
				return ExitUsage
			}
			segs[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		if len(segs) > 0 {
			payload["segments"] = segs
		}
		if w := fs.Arg(0); w != "" {
			payload["watchers"] = splitCSV(w)
		}
	}

	if *dryRun {
		data, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Printf("POST %s/api/v1/tasks\n%s\n", a.cfg.Server, string(data))
		return ExitOK
	}

	var raw map[string]any
	if err := a.cl.Post("/api/v1/tasks", payload, &raw); err != nil {
		return a.fail(err)
	}
	if a.jsonOut {
		a.out(raw)
		return ExitOK
	}
	t, _ := raw["task"].(map[string]any)
	code := str(t["code"])
	fmt.Printf("✓ 已创建 %s · %s\n", code, str(t["title"]))
	if sides := anySlice(t["sides"]); len(sides) > 0 {
		for _, sv := range sides {
			s, _ := sv.(map[string]any)
			fmt.Printf("    side %-10s @%s\n", str(s["key"]), str(s["assignee_role"]))
		}
	}
	fmt.Printf("\n开工包：kp task pack %s\n", code)
	return ExitOK
}

func (a *app) taskDelete(args []string) int {
	if len(args) < 1 {
		return a.usage("用法：kp task rm <code>", "会连带删除工作面、分段和上报")
	}
	var raw map[string]any
	if err := a.cl.Delete("/api/v1/tasks/"+args[0], &raw); err != nil {
		return a.fail(err)
	}
	a.out(raw)
	if !a.jsonOut {
		fmt.Printf("✓ 已删除 %s\n", args[0])
	}
	return ExitOK
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", x)
	}
}

func boolQ(b bool) string {
	if b {
		return "1"
	}
	return ""
}

func itoaClamp(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

// fmtWhenShort renders a timestamp for a terminal table: relative for recent
// things, absolute once "3 days ago" stops being useful.
func fmtWhenShort(iso string) string {
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		t, err = time.Parse(time.RFC3339, iso)
		if err != nil {
			return ""
		}
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return t.Local().Format("01-02")
}
