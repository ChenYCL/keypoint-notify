package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ChenYCL/keypoint-notify/internal/client"
)

// ---------------------------------------------------------------------------
// kp report
// ---------------------------------------------------------------------------

// report is the write that closes the loop the pack opens: whoever picked up a
// work face says something back. It accepts either flags for a person typing
// quickly or a JSON document from stdin for an agent that already has the whole
// shape in hand.
func (a *app) report(args []string) int {
	fs := flag.NewFlagSet("kp report", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	typ := fs.String("type", "progress", "progress/blocker/decision/handoff/result/question")
	side := fs.String("side", "", "归属于哪个工作面")
	status := fs.String("status", "", "同时把任务状态改成这个")
	priority := fs.String("priority", "", "P0..P3")
	mention := fs.String("mention", "", "@谁 或角色，逗号分隔")
	attach := fs.String("attach", "", "附件文件路径，逗号分隔")
	role := fs.String("role", "", "以哪个角色归属（默认你的激活角色）")
	msg := fs.String("m", "", "正文；可重复")
	fromJSON := fs.String("from-json", "", "从 JSON 读整个请求体；- 表示 stdin")
	dryRun := fs.Bool("dry-run", false, "只打印将发送的内容")
	fs.Usage = func() { printReportHelp() }
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		printReportHelp()
		return ExitOK
	}
	if err := a.parseSub(fs, args); err != nil {
		if errors.Is(err, errHelp) {
			return ExitOK
		}
		return ExitUsage
	}
	code := fs.Arg(0)
	if code == "" && *fromJSON == "" {
		return a.usage("用法：kp report <code> [-m 正文] [--type T]", "kp report 看完整用法")
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
			return ExitError
		}
	} else {
		body := *msg
		if body == "" {
			// No -m: read quietly from stdin when it is a pipe, so
			// `echo ... | kp report KP-12` works like a Unix tool.
			if stat, err := os.Stdin.Stat(); err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
				data, _ := io.ReadAll(os.Stdin)
				body = string(data)
			}
		}
		if strings.TrimSpace(body) == "" && *attach == "" {
			return a.usage("没有正文", "用 -m \"...\"、管道喂 stdin，或 --from-json -")
		}
		payload = map[string]any{"type": *typ, "body": strings.TrimSpace(body)}
		if *side != "" {
			payload["side_key"] = *side
		}
		if *status != "" {
			payload["status"] = *status
		}
		if *priority != "" {
			payload["priority"] = *priority
		}
		if *mention != "" {
			payload["mentions"] = splitCSV(*mention)
		}
		if *role != "" {
			payload["role"] = *role
		}
		if *attach != "" {
			ids, err := a.uploadFiles(splitCSV(*attach), code, *side)
			if err != nil {
				return a.fail(err)
			}
			payload["attachments"] = ids
		}
	}

	if *dryRun {
		data, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Printf("POST %s/api/v1/tasks/%s/reports\n%s\n", a.cfg.Server, code, string(data))
		return ExitOK
	}

	var raw map[string]any
	if err := a.cl.Post("/api/v1/tasks/"+code+"/reports", payload, &raw); err != nil {
		return a.fail(err)
	}
	if a.jsonOut {
		a.out(raw)
		return ExitOK
	}
	r, _ := raw["report"].(map[string]any)
	fmt.Printf("✓ 已上报 %s [%s]", code, str(r["type"]))
	if s := str(r["side_key"]); s != "" {
		fmt.Printf(" side:%s", s)
	}
	fmt.Println()
	if n, ok := raw["notified"].([]any); ok && len(n) > 0 {
		parts := []string{}
		for _, x := range n {
			parts = append(parts, "@"+str(x))
		}
		fmt.Printf("  已通知：%s\n", strings.Join(parts, " "))
	}
	if b, ok := raw["task_status_changed"].(bool); ok && b {
		t, _ := raw["task"].(map[string]any)
		fmt.Printf("  任务状态 → %s\n", statusLabelCN(str(t["status"])))
	}
	return ExitOK
}

// maxUpload is the client-side mirror of the server's limit. Checking locally
// turns "wait for 33 MB to upload, then get rejected" into an immediate error.
const maxUpload = 32 << 20

// checkUploadSizes rejects oversized files before the bytes are read.
//
// Without it, a too-large attachment costs a full round trip and comes back as
// a server-side parse error, which reads like a syntax mistake rather than a
// size one.
func checkUploadSizes(paths []string) error {
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("附件 %s 读不到：%w", p, err)
		}
		if info.Size() > maxUpload {
			return fmt.Errorf("附件 %s 有 %.1f MB，超过 %d MB 上限；更大的内容改用链接（任务 links 字段，或分段正文里的 URL）",
				p, float64(info.Size())/(1<<20), maxUpload>>20)
		}
	}
	return nil
}

// uploadFiles streams the given paths to the server and returns their ids.
func (a *app) uploadFiles(paths []string, taskCode, sideKey string) ([]string, error) {
	if err := checkUploadSizes(paths); err != nil {
		return nil, err
	}
	extra := map[string]string{}
	if sideKey != "" {
		extra["side_key"] = sideKey
	}
	path := "/api/v1/files"
	if taskCode != "" {
		path = "/api/v1/tasks/" + taskCode + "/files"
	}
	resp, err := a.cl.Upload(path, paths, extra)
	if err != nil {
		return nil, err
	}
	files, _ := resp["files"].([]any)
	out := make([]string, 0, len(files))
	for _, fv := range files {
		f, _ := fv.(map[string]any)
		if id := str(f["id"]); id != "" {
			out = append(out, id)
		}
	}
	if !a.jsonOut {
		for i, id := range out {
			fmt.Fprintf(os.Stderr, "  附件 %s → %s\n", paths[i], id)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// kp attach
// ---------------------------------------------------------------------------

func (a *app) attach(args []string) int {
	fs := flag.NewFlagSet("kp attach", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	task := fs.String("task", "", "挂到某个任务上")
	side := fs.String("side", "", "挂到某个工作面")
	fs.Usage = func() {
		fmt.Print(`kp attach — 上传附件（截图、日志、图）

  kp attach shot.png                     上传，返回 id / url / markdown 片段
  kp attach a.png b.jpg --task KP-12     直接挂到任务上
  kp attach shot.png --task KP-12 --side ui

返回的 markdown 片段可以直接贴进分段正文或上报里。
`)
	}
	if err := a.parseSub(fs, args); err != nil {
		if errors.Is(err, errHelp) {
			return ExitOK
		}
		return ExitUsage
	}
	files := fs.Args()
	if len(files) == 0 {
		fs.Usage()
		return ExitUsage
	}
	if err := checkUploadSizes(files); err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return ExitError
	}
	extra := map[string]string{}
	if *side != "" {
		extra["side_key"] = *side
	}
	path := "/api/v1/files"
	if *task != "" {
		path = "/api/v1/tasks/" + *task + "/files"
	}
	resp, err := a.cl.Upload(path, files, extra)
	if err != nil {
		return a.fail(err)
	}
	if a.jsonOut {
		a.out(resp)
		return ExitOK
	}
	for _, fv := range anySlice(resp["files"]) {
		f, _ := fv.(map[string]any)
		fmt.Printf("✓ %s\n   id  %s\n   url %s\n", str(f["name"]), str(f["id"]), str(f["url"]))
	}
	if md, ok := resp["markdown"].([]any); ok && len(md) > 0 {
		fmt.Println("\n贴进正文用：")
		for _, m := range md {
			fmt.Println("  " + str(m))
		}
	}
	return ExitOK
}

// ---------------------------------------------------------------------------
// kp inbox
// ---------------------------------------------------------------------------

func (a *app) inbox(args []string) int {
	fs := flag.NewFlagSet("kp inbox", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	unread := fs.Bool("unread", false, "只看未读")
	readAll := fs.Bool("read-all", false, "全部标记为已读")
	read := fs.String("read", "", "标记指定 id（逗号分隔）为已读")
	limit := fs.Int("limit", 30, "最多几条")
	fs.Usage = func() {
		fmt.Print(`kp inbox — 收件箱

  kp inbox                最近 30 条
  kp inbox --unread       只看未读
  kp inbox --read-all     全部标记已读
  kp inbox --read ntf_x,ntf_y

被 @ 的时候、你负责的任务有变化的时候，会出现在这里。
`)
	}
	if err := a.parseSub(fs, args); err != nil {
		if errors.Is(err, errHelp) {
			return ExitOK
		}
		return ExitUsage
	}
	if *readAll || *read != "" {
		ids := []string{}
		if *read != "" {
			ids = splitCSV(*read)
		}
		var raw map[string]any
		if err := a.cl.Post("/api/v1/inbox/read", map[string]any{"ids": ids}, &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		fmt.Printf("✓ 已标记 %v 条为已读，剩 %v 条未读\n", raw["marked"], raw["unread"])
		return ExitOK
	}

	var raw map[string]any
	path := "/api/v1/inbox" + client.Q("unread", boolQ(*unread), "limit", itoaClamp(*limit))
	if err := a.cl.Get(path, &raw); err != nil {
		return a.fail(err)
	}
	if a.jsonOut {
		a.out(raw)
		return ExitOK
	}
	items := anySlice(raw["items"])
	if len(items) == 0 {
		fmt.Printf("收件箱是空的（未读 %v）\n", raw["unread"])
		return ExitOK
	}
	fmt.Printf("未读 %v 条\n\n", raw["unread"])
	for _, iv := range items {
		n, _ := iv.(map[string]any)
		mark := "  "
		if n["read_at"] == nil {
			mark = "● "
		}
		fmt.Printf("%s%s\n    %s · %s\n", mark, str(n["title"]), str(n["kind"]),
			fmtWhenShort(str(n["created_at"])))
	}
	fmt.Println("\n全部已读：kp inbox --read-all")
	return ExitOK
}

// ---------------------------------------------------------------------------
// kp events
// ---------------------------------------------------------------------------

func (a *app) events(args []string) int {
	fs := flag.NewFlagSet("kp events", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	since := fs.String("since", "", "从哪个 cursor 开始（上一轮的 cursor 字段）")
	typ := fs.String("type", "", "事件类型前缀，如 report、task.")
	task := fs.String("task", "", "只看某个任务")
	limit := fs.Int("limit", 50, "最多几条")
	backlog := fs.Bool("backlog", false, "首次不带 since 时补发历史")
	follow := fs.Bool("follow", false, "持续跟随（轮询）")
	fs.Usage = func() {
		fmt.Print(`kp events — 事件流

  kp events                        同步到当前位点，返回 cursor
  kp events --backlog              先补历史
  kp events --since 128            从 cursor 128 之后拉
  kp events --task KP-12           只看某个任务
  kp events --follow               持续跟随（Ctrl-C 退出）

响应里的 cursor 下次原样传回 --since 即可。事件也是 webhook 的同一份数据。
`)
	}
	if err := a.parseSub(fs, args); err != nil {
		if errors.Is(err, errHelp) {
			return ExitOK
		}
		return ExitUsage
	}

	cursor := *since
	for {
		var raw map[string]any
		path := "/api/v1/events" + client.Q(
			"since", cursor, "type", *typ, "task", *task,
			"limit", itoaClamp(*limit), "backlog", boolQ(*backlog))
		if err := a.cl.Get(path, &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
		} else {
			for _, ev := range anySlice(raw["events"]) {
				e, _ := ev.(map[string]any)
				fmt.Printf("%s  %-22s %s  %s\n",
					padRunes(str(e["id"]), 6), str(e["type"]),
					fmtWhenShort(str(e["created_at"])), str(e["actor_name"]))
				if p, ok := e["payload"].(map[string]any); ok && len(p) > 0 {
					keys := sortedKeys(p)
					parts := []string{}
					for _, k := range keys {
						parts = append(parts, k+"="+str(p[k]))
					}
					fmt.Printf("        %s\n", truncateRunes(strings.Join(parts, " "), 110))
				}
			}
		}
		cursor = str(raw["cursor"])
		if !*follow {
			if !a.jsonOut {
				fmt.Printf("\ncursor=%s（下次：kp events --since %s）\n", cursor, cursor)
			}
			return ExitOK
		}
		*backlog = false
		sleepSeconds(3)
	}
}
