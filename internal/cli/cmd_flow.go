package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ChenYCL/keypoint-notify/internal/client"
	"github.com/ChenYCL/keypoint-notify/internal/model"
)

// ---------------------------------------------------------------------------
// Workflow shortcuts
//
// Each of these is something people (and agents) were already doing with two
// or three longer commands, and getting half-right: reporting a result but
// never marking the face done, so the next role waited forever; clearing the
// assignee and the role together when they only meant to hand the claim back.
// One verb per step of the workflow, doing the whole step.
// ---------------------------------------------------------------------------

// done: report the result and close the face in one go.
func (a *app) done(args []string) int {
	fs := flag.NewFlagSet("kp done", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	msg := fs.String("m", "", "结果：改了什么 + 怎么验证的 + 遗留风险")
	fs.Usage = func() {
		fmt.Print(`kp done — 完结：上报结果 + 把工作面置为完成

  kp done KP-12 ui -m "改了什么 / 怎么验证 / 遗留风险"
  kp done KP-12 -m "..."          只有一个面在你名下时可省略面
  echo "..." | kp done KP-12 ui    正文也可以从 stdin 来

置完成会自动解封依赖它的下游（它们的会话会被唤醒）；最后一个面完成时
任务自动收尾。任务没有工作面时，直接把任务置为完成。
`)
	}
	if err := a.parseSub(fs, args); err != nil {
		return subExit(err)
	}
	code, sideKey := fs.Arg(0), fs.Arg(1)
	if code == "" {
		return a.usage("用法：kp done <code> [side] -m \"...\"", "kp done --help 看例子")
	}
	body := readBody(*msg)
	if body == "" {
		return a.usage("没有结果说明", "用 -m \"改了什么 / 怎么验证 / 遗留风险\"，别人要靠它接着干")
	}

	t, err := a.getTask(code)
	if err != nil {
		return a.fail(err)
	}
	if sideKey == "" && len(t.Sides) > 0 {
		sideKey, err = a.pickMySide(t)
		if err != nil {
			return a.fail(err)
		}
	}

	payload := map[string]any{"type": model.ReportResult, "body": body}
	if sideKey != "" {
		payload["side_key"] = sideKey
	}
	if err := a.cl.Post("/api/v1/tasks/"+t.Code+"/reports", payload, nil); err != nil {
		return a.fail(err)
	}

	if sideKey == "" {
		if err := a.cl.Patch("/api/v1/tasks/"+t.Code, map[string]any{"status": model.StatusDone}, nil); err != nil {
			return a.fail(err)
		}
		fmt.Printf("✓ %s 已上报结果并完结\n", t.Code)
		return ExitOK
	}
	if err := a.cl.Patch("/api/v1/tasks/"+t.Code+"/sides/"+sideKey,
		map[string]any{"status": model.SideDone}, nil); err != nil {
		return a.fail(err)
	}

	after, err := a.getTask(t.Code)
	if err != nil {
		fmt.Printf("✓ %s/%s 已上报结果并置为完成\n", t.Code, sideKey)
		return ExitOK
	}
	fmt.Printf("✓ %s/%s 已上报结果并置为完成\n", t.Code, sideKey)
	for _, sd := range after.Sides {
		if containsStr(sd.Deps, sideKey) && sd.Status != model.SideDone && sd.Status != model.SideBlocked {
			fmt.Printf("  → 下游 %s（@%s）已解封\n", sd.Key, sd.AssigneeRole)
		}
	}
	if after.Status == model.StatusDone {
		fmt.Printf("  → 这是最后一个面，%s 已自动收尾\n", t.Code)
	}
	return ExitOK
}

// release: hand a claimed face back to its role, keeping the routing.
func (a *app) release(args []string) int {
	fs := flag.NewFlagSet("kp release", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	msg := fs.String("m", "", "为什么放手（会作为交接上报留在时间线上）")
	fs.Usage = func() {
		fmt.Print(`kp release — 放弃认领：把工作面还给它的角色

  kp release KP-12 ui -m "今天做不完，卡在接口还没定"

只清掉认领人，角色保留 —— 同角色的其他会话下一次 kp next 就能接走。
和 kp task side assign --unassign 不同，后者连角色一起清掉，面就没人收了。
`)
	}
	if err := a.parseSub(fs, args); err != nil {
		return subExit(err)
	}
	code, sideKey := fs.Arg(0), fs.Arg(1)
	if code == "" || sideKey == "" {
		return a.usage("用法：kp release <code> <side> [-m 原因]", "")
	}
	t, err := a.getTask(code)
	if err != nil {
		return a.fail(err)
	}
	var owner string
	found := false
	for _, sd := range t.Sides {
		if sd.Key == sideKey {
			owner, found = sd.AssigneeIdentity, true
		}
	}
	if !found {
		return a.usage("没有工作面 "+sideKey, "kp task side ls "+t.Code+" 看有哪些")
	}
	if owner == "" {
		fmt.Printf("· %s/%s 本来就没人认领，不用放手\n", t.Code, sideKey)
		return ExitOK
	}
	if owner != a.cfg.Identity {
		return a.usage(fmt.Sprintf("%s/%s 在 %s 名下，不是你的", t.Code, sideKey, owner),
			"要接手先跟对方说：kp report "+t.Code+" --side "+sideKey+" --type handoff -m ... --mention "+owner)
	}
	reason := strings.TrimSpace(*msg)
	if reason == "" {
		reason = "放弃认领，交还给角色"
	}
	if err := a.cl.Post("/api/v1/tasks/"+code+"/reports", map[string]any{
		"type": model.ReportHandoff, "body": reason, "side_key": sideKey,
	}, nil); err != nil {
		return a.fail(err)
	}
	var raw map[string]any
	if err := a.cl.Patch("/api/v1/tasks/"+code+"/sides/"+sideKey, map[string]any{
		"assignee_identity": "", "status": model.SideTodo,
	}, &raw); err != nil {
		return a.fail(err)
	}
	sd, _ := raw["side"].(map[string]any)
	fmt.Printf("✓ %s/%s 已放手，交还 @%s（同角色的会话可以接走）\n", code, sideKey, str(sd["assignee_role"]))
	return ExitOK
}

// cancel: stop a task without deleting it (deleting needs admin).
func (a *app) cancel(args []string) int {
	fs := flag.NewFlagSet("kp cancel", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	msg := fs.String("m", "", "取消原因（会记在时间线上）")
	fs.Usage = func() {
		fmt.Print(`kp cancel — 取消任务：记下原因并归档

  kp cancel KP-12 -m "需求撤了"

归档后不再出现在看板和 kp next 里，时间线保留，随时可以
kp task status KP-12 inbox 拿回来。真要删除需要 admin：kp task rm。
`)
	}
	if err := a.parseSub(fs, args); err != nil {
		return subExit(err)
	}
	code := fs.Arg(0)
	if code == "" {
		return a.usage("用法：kp cancel <code> -m \"原因\"", "")
	}
	reason := strings.TrimSpace(*msg)
	if reason == "" {
		return a.usage("没有取消原因", "用 -m \"...\"，接手过的人需要知道为什么停")
	}
	if err := a.cl.Post("/api/v1/tasks/"+code+"/reports", map[string]any{
		"type": model.ReportDecision, "body": "取消：" + reason,
	}, nil); err != nil {
		return a.fail(err)
	}
	if err := a.cl.Patch("/api/v1/tasks/"+code, map[string]any{"status": model.StatusArchived}, nil); err != nil {
		return a.fail(err)
	}
	fmt.Printf("✓ %s 已取消（归档）。恢复：kp task status %s inbox\n", code, code)
	return ExitOK
}

// watch / unwatch: follow a task you neither own nor work on.
func (a *app) watch(args []string, on bool) int {
	verb := map[bool]string{true: "watch", false: "unwatch"}[on]
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Printf(`kp %s — %s一个任务的动态

  kp %s KP-12

订阅后这个任务的每条动态都进你的收件箱；配合 kp wait，
有新通知就会把等待中的会话叫醒。
`, verb, map[bool]string{true: "订阅", false: "退订"}[on], verb)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	method := a.cl.Post
	if !on {
		method = func(path string, _ any, out any) error { return a.cl.Delete(path, out) }
	}
	for _, code := range args {
		if err := method("/api/v1/tasks/"+code+"/watch", map[string]any{}, nil); err != nil {
			return a.fail(err)
		}
		fmt.Printf("✓ %s %s\n", map[bool]string{true: "已订阅", false: "已退订"}[on], code)
	}
	return ExitOK
}

// wait: block until something new is addressed to me, then exit.
//
// This is what lets an agent session be *woken* rather than poll: run it in
// the background, and its exit is the notification. It watches two things —
// work routed to me (the same answer as kp next) and new unread inbox entries
// (which also covers tasks I merely watch). It peeks: it never advances my
// stored next-cursor, so the session's own `kp next` still sees the mention
// that woke it.
func (a *app) wait(args []string) int {
	fs := flag.NewFlagSet("kp wait", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	timeout := fs.Int("timeout", 600, "最多等多少秒，到点还没动静就退出")
	task := fs.String("task", "", "只盯某个任务")
	fs.Usage = func() {
		fmt.Print(`kp wait — 等到有新活或新通知就退出

  kp wait                    最多等 10 分钟
  kp wait --timeout 3600     最多等 1 小时
  kp wait --task KP-12       只盯一个任务

输出第一行是给程序看的：
  KP-WAIT: work           有轮到你的新活（后面是开工包）
  KP-WAIT: notification   收件箱有新通知（后面是标题）
  KP-WAIT: timeout        到点了，什么都没来

不认领、不动游标：之后照常 kp next --claim 接活。
在 Claude Code 里放后台跑（它退出时会话会被叫醒），就是「订阅通知、来了就开始」。
`)
	}
	if err := a.parseSub(fs, args); err != nil {
		return subExit(err)
	}

	baseline, err := a.unreadCount()
	if err != nil {
		return a.fail(err)
	}
	deadline := time.Now().Add(time.Duration(*timeout) * time.Second)
	cursor := ""
	for {
		left := int(time.Until(deadline).Seconds())
		if left <= 0 {
			fmt.Printf("KP-WAIT: timeout\n（等了 %d 秒，没有新活，也没有新通知）\n", *timeout)
			return ExitOK
		}
		round := minInt(25, left, 25)
		text, err := a.cl.GetText("/api/v1/me/next" + client.Q(
			"wait", fmt.Sprint(round), "since", cursor, "task", *task, "peek", "1"))
		if err != nil {
			return a.fail(err)
		}
		next, hasWork := nextCursor(text)
		if next != "" {
			cursor = next
		}
		if hasWork {
			fmt.Print("KP-WAIT: work\n" + text)
			return ExitOK
		}
		if titles, n, err := a.newUnread(baseline); err == nil && n > baseline {
			fmt.Printf("KP-WAIT: notification\n收件箱有 %d 条新通知：\n", n-baseline)
			for _, t := range titles {
				fmt.Println("  · " + t)
			}
			fmt.Println("\n看全部：kp inbox --unread")
			return ExitOK
		}
	}
}

// ---------------------------------------------------------------------------

// getTask fetches a task; GET /api/v1/tasks/{code} returns the task itself at
// the top level.
func (a *app) getTask(code string) (model.Task, error) {
	var t model.Task
	if err := a.cl.Get("/api/v1/tasks/"+code, &t); err != nil {
		return t, err
	}
	if t.Code == "" {
		return t, fmt.Errorf("服务端没返回任务 %s", code)
	}
	return t, nil
}

// pickMySide finds the one unfinished face on t that is mine, so `kp done
// KP-12` works without naming it. Ambiguity is an error with the choices
// listed, never a guess.
func (a *app) pickMySide(t model.Task) (string, error) {
	var mine, open []string
	for _, sd := range t.Sides {
		if sd.Status == model.SideDone {
			continue
		}
		open = append(open, sd.Key)
		if sd.AssigneeIdentity == a.cfg.Identity {
			mine = append(mine, sd.Key)
		}
	}
	switch {
	case len(mine) == 1:
		return mine[0], nil
	case len(open) == 1:
		return open[0], nil
	case len(open) == 0:
		return "", fmt.Errorf("%s 的工作面都已完成", t.Code)
	}
	return "", errors.New(t.Code + " 有多个没完成的面（" + strings.Join(open, "、") +
		"），指明是哪个：kp done " + t.Code + " <side> -m ...")
}

func (a *app) unreadCount() (int, error) {
	_, n, err := a.newUnread(0)
	return n, err
}

// newUnread returns the newest unread titles and the total unread count.
func (a *app) newUnread(baseline int) ([]string, int, error) {
	var raw struct {
		Unread int `json:"unread"`
		Items  []struct {
			Title string `json:"title"`
		} `json:"items"`
	}
	limit := 5
	if err := a.cl.Get(fmt.Sprintf("/api/v1/inbox?unread=1&limit=%d", limit), &raw); err != nil {
		return nil, 0, err
	}
	var titles []string
	for i, it := range raw.Items {
		if i >= raw.Unread-baseline {
			break
		}
		titles = append(titles, it.Title)
	}
	return titles, raw.Unread, nil
}

// readBody takes -m, or stdin when it is a pipe.
func readBody(m string) string {
	if strings.TrimSpace(m) != "" {
		return strings.TrimSpace(m)
	}
	if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice == 0 {
		data, _ := io.ReadAll(os.Stdin)
		return strings.TrimSpace(string(data))
	}
	return ""
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
