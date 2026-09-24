package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ChenYCL/keypoint-notify/internal/model"
)

// multiFlag collects a repeatable string flag (--option A --option B).
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ", ") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

const researchHelp = `kp research — 调研：方案、论点、待定、定论

  kp research                              待定的问题 + 所有调研（= kp research ls）
  kp research new "要回答的问题" --option "A：…" --option "B：…" [--role backend]
  kp research note KP-12 -m "论点 / 发现（带证据）"
  kp research ask  KP-12 -m "拿不准的点" --mention @backend
  kp research decide KP-12 -m "定论：选 A，因为…" [--close]

调研就是 kind=research 的任务：目标写问题，每个方案一段；论点记成 finding 上报，
拿不准的记成 question，定下来记成 decision。一个 question 在同一任务里出现更晚的
decision 就算有定论 —— 所以「待定」清单清空的办法只有一个：把结论写下来。
任何任务里的 question 都会进「待定」，不只是调研任务。
`

func (a *app) research(args []string) int {
	if len(args) == 0 {
		return a.researchList(nil)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "ls", "list":
		return a.researchList(rest)
	case "new", "add":
		return a.researchNew(rest)
	case "note", "finding":
		return a.researchReport("note", model.ReportFinding, rest)
	case "ask", "question":
		return a.researchReport("ask", model.ReportQuestion, rest)
	case "decide", "decision":
		return a.researchReport("decide", model.ReportDecision, rest)
	case "-h", "--help", "help":
		fmt.Print(researchHelp)
		return ExitOK
	}
	return a.usage("未知子命令 research "+sub, "kp research --help")
}

func (a *app) researchList(args []string) int {
	fs := flag.NewFlagSet("kp research ls", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Print(researchHelp) }
	if err := a.parseSub(fs, args); err != nil {
		return subExit(err)
	}
	var r struct {
		Items []struct {
			Code, Title, Status, Question, State string
			Options                              []string
			Findings                             int
			OpenQuestions                        int `json:"open_questions"`
			Decisions                            int
			LastDecision                         *model.Report `json:"last_decision"`
		} `json:"items"`
		Open []struct {
			model.Report
			TaskCode  string `json:"task_code"`
			TaskTitle string `json:"task_title"`
		} `json:"open_questions"`
	}
	var raw map[string]any
	if a.jsonOut {
		if err := a.cl.Get("/api/v1/research", &raw); err != nil {
			return a.fail(err)
		}
		a.out(raw)
		return ExitOK
	}
	if err := a.cl.Get("/api/v1/research", &r); err != nil {
		return a.fail(err)
	}

	fmt.Printf("待定（需要定论） %d\n", len(r.Open))
	if len(r.Open) == 0 {
		fmt.Println("  （没有悬着的问题）")
	}
	for _, q := range r.Open {
		who := q.IdentityName
		if len(q.Mentions) > 0 {
			who += " → @" + strings.Join(q.Mentions, " @")
		}
		fmt.Printf("  %-6s %s  %s\n         %s\n", q.TaskCode, truncateRunes(q.TaskTitle, 24), who,
			truncateRunes(strings.ReplaceAll(q.Body, "\n", " "), 70))
	}

	fmt.Printf("\n调研 %d\n", len(r.Items))
	if len(r.Items) == 0 {
		fmt.Println("  （还没有。kp research new \"要回答的问题\" --option \"A：…\" --option \"B：…\"）")
	}
	for _, it := range r.Items {
		state := "待定"
		if it.State == "decided" {
			state = "已定论"
		}
		fmt.Printf("  %-6s [%s] %s\n", it.Code, state, truncateRunes(it.Question, 60))
		if len(it.Options) > 0 {
			fmt.Printf("         方案：%s\n", strings.Join(it.Options, " · "))
		}
		fmt.Printf("         论点 %d · 待定 %d · 定论 %d\n", it.Findings, it.OpenQuestions, it.Decisions)
		if it.LastDecision != nil {
			body := strings.TrimSpace(strings.ReplaceAll(it.LastDecision.Body, "\n", " "))
			for _, p := range []string{"定论：", "定论:"} {
				body = strings.TrimPrefix(body, p)
			}
			fmt.Printf("         最近定论：%s\n", truncateRunes(body, 60))
		}
	}
	return ExitOK
}

func (a *app) researchNew(args []string) int {
	fs := flag.NewFlagSet("kp research new", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var options multiFlag
	fs.Var(&options, "option", "一个方案，可重复：--option \"A：用 SSE\" --option \"B：长轮询\"")
	ctx := fs.String("context", "", "背景：为什么要调研、已知约束")
	role := fs.String("role", "", "谁来调研（角色）")
	priority := fs.String("priority", "P2", "P0..P3")
	fs.Usage = func() { fmt.Print(researchHelp) }
	if err := a.parseSub(fs, args); err != nil {
		return subExit(err)
	}
	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		return a.usage("用法：kp research new \"要回答的问题\" --option \"A：…\" --option \"B：…\"", "")
	}
	body := map[string]any{
		"title": truncateRunes(question, 60), "kind": model.TaskResearch, "priority": *priority,
		"segments": map[string]string{"goal": question, "context": *ctx},
	}
	if *role != "" {
		body["owner_role"] = *role
	}
	var created struct {
		Task struct {
			Code string `json:"code"`
		} `json:"task"`
	}
	if err := a.cl.Post("/api/v1/tasks", body, &created); err != nil {
		return a.fail(err)
	}
	code := created.Task.Code
	for i, opt := range options {
		title := fmt.Sprintf("方案 %c", 'A'+i)
		if head, _, ok := strings.Cut(opt, "："); ok && len([]rune(head)) <= 12 {
			title = "方案 " + strings.TrimSpace(head)
		} else if head, _, ok := strings.Cut(opt, ":"); ok && len([]rune(head)) <= 12 {
			title = "方案 " + strings.TrimSpace(head)
		}
		if err := a.cl.Post("/api/v1/tasks/"+code+"/segments", map[string]any{
			"key": fmt.Sprintf("option-%c", 'a'+i), "title": title, "body": opt,
		}, nil); err != nil {
			return a.fail(err)
		}
	}
	fmt.Printf("✓ 调研 %s：%s\n", code, truncateRunes(question, 50))
	if len(options) > 0 {
		fmt.Printf("  %d 个方案已写成分段\n", len(options))
	}
	fmt.Printf("  记论点：kp research note %s -m \"…\"\n  拿不准：kp research ask %s -m \"…\" --mention @角色\n  定下来：kp research decide %s -m \"定论：…\" --close\n", code, code, code)
	return ExitOK
}

func (a *app) researchReport(verb, typ string, args []string) int {
	fs := flag.NewFlagSet("kp research "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	msg := fs.String("m", "", "正文")
	mention := fs.String("mention", "", "@谁（身份名或角色，逗号分隔）")
	side := fs.String("side", "", "记在哪个工作面上（可选）")
	closeTask := fs.Bool("close", false, "定论后把调研任务置为完成（只对 decide）")
	fs.Usage = func() { fmt.Print(researchHelp) }
	if err := a.parseSub(fs, args); err != nil {
		return subExit(err)
	}
	code := fs.Arg(0)
	body := readBody(*msg)
	if code == "" || body == "" {
		return a.usage(fmt.Sprintf("用法：kp research %s KP-12 -m \"…\"", verb), "")
	}
	payload := map[string]any{"type": typ, "body": body}
	if *mention != "" {
		payload["mentions"] = splitCSV(*mention)
	}
	if *side != "" {
		payload["side_key"] = *side
	}
	if err := a.cl.Post("/api/v1/tasks/"+code+"/reports", payload, nil); err != nil {
		return a.fail(err)
	}
	label := map[string]string{model.ReportFinding: "论点", model.ReportQuestion: "待定问题", model.ReportDecision: "定论"}[typ]
	fmt.Printf("✓ %s 记了一条%s\n", code, label)
	if typ == model.ReportDecision {
		fmt.Println("  这个任务里在它之前的问题都算定了")
		if *closeTask {
			if err := a.cl.Patch("/api/v1/tasks/"+code, map[string]any{"status": model.StatusDone}, nil); err != nil {
				return a.fail(err)
			}
			fmt.Printf("  %s 已完成\n", code)
		}
	}
	return ExitOK
}
