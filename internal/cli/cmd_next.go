package cli

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/ChenYCL/keypoint-notify/internal/client"
)

// ---------------------------------------------------------------------------
// kp next — the consume side of the loop
// ---------------------------------------------------------------------------

// nextCmd is what a collaborating session runs in its loop. One call answers
// "is there work for me, why, and what do I need to start" — the agent does not
// have to poll events and re-derive whose turn it is.
func (a *app) next(args []string) int {
	fs := flag.NewFlagSet("kp next", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	wait := fs.Int("wait", 0, "没有活时最多等多少秒（长轮询），上限 60")
	since := fs.String("since", "", "从哪个事件游标之后找；上一轮返回的 cursor")
	claim := fs.Bool("claim", false, "拿到工作面时原子认领，避免同角色会话重复捡")
	side := fs.String("side", "", "只看某个工作面")
	task := fs.String("task", "", "只看某个任务")
	exclude := fs.String("exclude", "", "跳过的任务号，逗号分隔（这次不想接的）")
	maxChars := fs.Int("max-chars", 0, "上下文包上限，默认 12000")
	reports := fs.Int("reports", 3, "附带最近 N 条上报")
	cursorOnly := fs.Bool("cursor-only", false, "只打印游标（脚本/循环里取用）")
	JSON := fs.Bool("json", false, "结构化输出")
	fs.Usage = func() {
		fmt.Print(`kp next — 有没有轮到我干的活

  kp next                        立即看一眼，没有就返回"没有"
  kp next --wait 30              没有活时挂起最多 30 秒（长轮询，推荐）
  kp next --claim                拿到工作面就原子认领，同角色的别人不会再捡走
  kp next --since 128            接着上一轮的游标继续
  kp next --exclude KP-14        跳过这个（这次不想接的，可逗号分隔多个）
  kp next --json                 结构化：{work, pack, cursor}
  kp next --cursor-only          只输出游标

它把四步合成一步：判断有没有属于我的活 → 说清为什么是我 → 连带完整开工包。
reason 的取值：

  mention    有人在某个上报里 @ 了我，等着我回应
  unblocked  我负责的工作面，依赖刚完成，解封了
  assigned   指派给我角色的工作面，依赖就绪且还没人认领
  owned      我是任务负责人，任务有新动静

给 agent 的循环长这样（每轮一次请求，而不是每秒一次）：

  CURSOR=$(kp next --cursor-only)
  while true; do
    OUT=$(kp next --wait 30 --claim --since "$CURSOR")
    CURSOR=$(kp next --cursor-only)
    [ "$OUT" = "（没有属于你的活）" ] && continue
    # 把 $OUT 交给模型 / 按它开工
    ...
    kp report <code> --side <side> --type result -m "…"
  done
`)
	}
	if err := a.parseSub(fs, args); err != nil {
		return ExitUsage
	}
	if *JSON {
		a.jsonOut = true
	}

	path := "/api/v1/me/next" + client.Q(
		"wait", itoaOrEmpty(*wait),
		"since", *since,
		"side", *side,
		"task", *task,
		"exclude", *exclude,
		"max_chars", itoaOrEmpty(*maxChars),
		"reports", itoaOrEmpty(*reports),
		"claim", boolQ(*claim),
	)
	if *cursorOnly {
		var out struct {
			Cursor int64 `json:"cursor"`
		}
		if err := a.cl.Get(path+"&format=json", &out); err != nil {
			return a.fail(err)
		}
		fmt.Println(out.Cursor)
		return ExitOK
	}

	if a.jsonOut {
		var raw map[string]any
		if err := a.cl.Get(path+"&format=json", &raw); err != nil {
			return a.fail(err)
		}
		a.out(raw)
		return ExitOK
	}

	text, err := a.cl.GetText(path)
	if err != nil {
		return a.fail(err)
	}
	fmt.Print(text)
	if !strings.HasPrefix(text, "（没有属于你的活）") {
		fmt.Fprintf(os.Stderr, "\n--- 收工要上报：kp report <code> --side <side> --type result -m \"...\"\n")
	}
	return ExitOK
}

func itoaOrEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

// ---------------------------------------------------------------------------
// kp loop — 把 next 包成一个循环
// ---------------------------------------------------------------------------

// loopCmd is the human-watchable form of the agent loop: it polls, prints each
// work item as it appears, and optionally hands the pack to a command.
//
// It exists so the collaboration can be demonstrated and debugged without
// writing a shell loop by hand — and so a session that is not itself doing the
// polling can still watch the traffic.
func (a *app) loop(args []string) int {
	fs := flag.NewFlagSet("kp loop", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	wait := fs.Int("wait", 20, "每轮最多等多少秒")
	max := fs.Int("max", 0, "处理多少条后停止；0 = 一直跑")
	run := fs.String("run", "", "把开工包喂给这个命令（$KP_PACK 环境变量也可用）")
	claim := fs.Bool("claim", true, "自动认领拿到的工作面")
	side := fs.String("side", "", "只看某个工作面")
	task := fs.String("task", "", "只看某个任务")
	exclude := fs.String("exclude", "", "跳过的任务号，逗号分隔（这次不想接的）")
	interval := fs.Int("interval", 1, "两条之间的最小间隔秒数")
	agent := fs.String("agent", "", "claude | kimi：每件活交给一个新的无头会话去做")
	agentArgs := fs.String("agent-args", "", "覆盖 --agent 的默认参数（高级）")
	fs.Usage = func() {
		fmt.Print(`kp loop — 一直等活，每件活交给一个会话去做

  kp loop --agent claude          无人值守：每件活起一个 claude -p，做完 kp done 交棒
  kp loop --agent kimi            同上，用 kimi -p
  kp loop                         只打印开工包（自己看 / 调试）
  kp loop --run '<命令>'          开工包放在 $KP_PACK 和 stdin 里交给任意命令
  kp loop --max 3                 处理三件就退出
  kp loop --task KP-1 --side ui   只盯一个工作面

在要干活的仓库目录里启动：会话就在这个目录里改代码。
--agent claude 默认参数是 --permission-mode acceptEdits --allowedTools Bash，
即可以改文件、跑命令而不用人点确认 —— 给 bot 身份用，别用管理员身份跑。
Ctrl-C 退出。认领是原子的：同角色开几个 loop 不会抢到同一件。
`)
	}
	if err := a.parseSub(fs, args); err != nil {
		return subExit(err)
	}
	if *agent != "" && *run != "" {
		return a.usage("--agent 和 --run 只能选一个", "--agent 是 --run 的预设")
	}
	if *agent != "" {
		if _, err := agentCommand(*agent, "", *agentArgs); err != nil {
			return a.usage(err.Error(), "可选：--agent claude | --agent kimi")
		}
	}

	cursor := ""
	handled := 0
	fmt.Fprintf(os.Stderr, "kp loop 启动（身份 %s，角色 %s）—— Ctrl-C 退出\n",
		a.cfg.Identity, a.cfg.ActiveRole)

	for {
		// One request per round. This used to ask twice — JSON to learn whether
		// there was work, then markdown to print it — and with claim=1 both
		// requests dispatched: `kp loop --max 1` could take two faces and hand
		// only the second one to --run, leaving the first owned by nobody who
		// was working on it.
		path := "/api/v1/me/next" + client.Q(
			"wait", itoaOrEmpty(*wait), "since", cursor,
			"side", *side, "task", *task, "exclude", *exclude, "claim", boolQ(*claim),
		)
		text, err := a.cl.GetText(path)
		if err != nil {
			return a.fail(err)
		}
		next, hasWork := nextCursor(text)
		if next != "" {
			cursor = next
		}
		if !hasWork {
			continue
		}
		handled++
		fmt.Printf("\n════ 第 %d 条 ════\n%s", handled, text)

		switch {
		case *agent != "":
			cmd, _ := agentCommand(*agent, text, *agentArgs)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintln(os.Stderr, "✗ 会话失败:", err)
			}
		case *run != "":
			if err := runWithPack(*run, text); err != nil {
				fmt.Fprintln(os.Stderr, "✗ 命令失败:", err)
			}
		}
		if *max > 0 && handled >= *max {
			fmt.Fprintf(os.Stderr, "\n处理了 %d 条，按 --max 退出\n", handled)
			return ExitOK
		}
		if *interval > 0 {
			time.Sleep(time.Duration(*interval) * time.Second)
		}
	}
}

// nextCursor reads the markdown form of /me/next: whether it carries work, and
// the cursor to continue from. Both shapes put the cursor on the first line —
// `<!-- keypoint next · reason=… · cursor=N -->` or `（没有属于你的活）\ncursor=N`.
func nextCursor(text string) (cursor string, hasWork bool) {
	hasWork = !strings.HasPrefix(text, "（没有属于你的活）")
	if m := cursorRe.FindStringSubmatch(text); m != nil {
		cursor = m[1]
	}
	return cursor, hasWork
}

var cursorRe = regexp.MustCompile(`cursor=(\d+)`)

// runWithPack executes a command with the pack on stdin and in $KP_PACK.
func runWithPack(command, packText string) error {
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdin = bytes.NewBufferString(packText)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "KP_PACK="+packText)
	return cmd.Run()
}

// ---------------------------------------------------------------------------
// kp claim — 直接认领一个工作面
// ---------------------------------------------------------------------------

func (a *app) claim(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(`kp claim — 原子认领一个工作面

  kp claim KP-12 ui

认领后这个面记在你名下，同角色的其他会话不会再捡走（服务端用条件 UPDATE
保证只有一个成功）。只对「指派给你的角色 / 指派给你的身份」生效 ——
被 mention 不等于接管别人的面。

已被拿走时退出码非 0，并提示先上报交接。
`)
		return ExitOK
	}
	args, err := a.bareArgs("kp claim", args)
	if err != nil {
		return subExit(err)
	}
	if len(args) < 2 {
		return a.usage("用法：kp claim <code> <side>", "认领后会记在你名下，同角色的其他会话不会再捡走")
	}
	var raw map[string]any
	if err := a.cl.Post("/api/v1/tasks/"+args[0]+"/sides/"+args[1]+"/claim", map[string]any{}, &raw); err != nil {
		return a.fail(err)
	}
	if a.jsonOut {
		a.out(raw)
		return ExitOK
	}
	if ok, _ := raw["claimed"].(bool); ok {
		fmt.Printf("✓ %s/%s 已认领\n", args[0], args[1])
		fmt.Printf("  开工包：%v\n", raw["hint"])
	} else {
		fmt.Printf("✗ %s/%s 已被别人认领\n", args[0], args[1])
		fmt.Printf("  %v\n", raw["note"])
		return ExitError
	}
	return ExitOK
}

// agentPrompt is what an unattended session is told before its pack. It is
// short on purpose: the pack carries the context and the delivery contract;
// this only fixes the three things unattended sessions got wrong in rehearsal —
// taking over a face they were only asked about, stopping at "reported" without
// closing the face, and wandering on to a second item.
const agentPreamble = `你是 Keypoint 协作里的一个无人值守会话。下面是刚分派给你的一件活（已认领）。

1. 先看「为什么是你」：reason=mention 只是有人问你，回答它（kp report <任务号> --type question 或 decision），不要接手那个工作面。
2. 否则就把活真正做完：读代码、改、跑测试。上下文里没有的信息写「（待确认：…）」，不要编。
3. 做完：kp done <任务号> <工作面> -m "改了什么 / 怎么验证 / 遗留风险" —— 这一步会解封下游，不做别人就一直等。
   卡住：kp report <任务号> --side <工作面> --type blocker -m "卡在哪、需要什么" --mention @能解决的角色
4. 只处理这一件，做完就结束。

`

// agentCommand builds the headless session for one work item. The pack goes in
// as the prompt argument and stdin is left empty, so the CLI does not read the
// pack a second time from a pipe.
func agentCommand(kind, pack, override string) (*exec.Cmd, error) {
	prompt := agentPreamble + pack
	var name string
	var args []string
	switch kind {
	case "claude":
		name = "claude"
		args = []string{"-p", prompt, "--permission-mode", "acceptEdits", "--allowedTools", "Bash"}
	case "kimi":
		// kimi -p approves tool calls on its own; --yolo is rejected with -p.
		name = "kimi"
		args = []string{"-p", prompt}
	default:
		return nil, fmt.Errorf("不认识的 --agent %q", kind)
	}
	if strings.TrimSpace(override) != "" {
		args = append([]string{"-p", prompt}, strings.Fields(override)...)
	}
	if _, err := exec.LookPath(name); err != nil {
		return nil, fmt.Errorf("找不到 %s 命令（没装，或不在 PATH 里）", name)
	}
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "KP_PACK="+pack)
	return cmd, nil
}
