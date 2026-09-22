// Package cli implements the `kp` command.
//
// Two audiences drive the design. People get short tables and a `--help` that
// shows the next thing to type. Agents get `--json` everywhere, stable exit
// codes, and errors that carry a hint rather than just a message — the same
// contract the HTTP API offers, so a model that learned one has learned both.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/light/keypoint-notify/internal/client"
	"github.com/light/keypoint-notify/internal/config"
)

// Exit codes, stable enough for a script to branch on.
const (
	ExitOK     = 0
	ExitError  = 1
	ExitUsage  = 2
	ExitAuth   = 3
	ExitNoConf = 4
	ExitServer = 5
)

// Version is stamped at build time via -ldflags.
var Version = "dev"

// globalOpts are the flags accepted before the command name.
type globalOpts struct {
	json    bool
	verbose bool
	server  string
	key     string
	role    string
}

// Run dispatches a command line and returns the process exit code.
//
// Global flags may appear before the command (`kp --json task list`); the
// command's own flags come after it. Splitting on the first bare word keeps the
// two grammars from fighting over the same argument.
func Run(args []string) int {
	head, tail := splitAtFirstNonFlag(args)
	g, err := parseGlobal(head)
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return ExitUsage
	}
	if len(tail) == 0 {
		printTopHelp()
		return ExitUsage
	}
	cmd, rest := tail[0], tail[1:]

	switch cmd {
	case "help", "-h", "--help":
		printTopHelp()
		return ExitOK
	case "version", "-v", "--version":
		fmt.Printf("kp %s\n", Version)
		return ExitOK
	case "init":
		return cmdInit(rest)
	case "serve":
		return cmdServe(rest)
	case "config":
		return cmdConfig(rest)
	case "docs":
		return cmdDocs(rest, g)
	}

	a, code := newApp(g)
	if a == nil {
		return code
	}

	switch cmd {
	case "whoami":
		return a.whoami()
	case "board":
		return a.board()
	case "task", "tasks":
		return a.task(rest)
	case "report", "rep":
		return a.report(rest)
	case "inbox":
		return a.inbox(rest)
	case "attach":
		return a.attach(rest)
	case "events":
		return a.events(rest)
	case "role", "roles":
		return a.role(rest)
	case "identity", "id":
		return a.identity(rest)
	case "hook", "webhook":
		return a.hook(rest)
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n\n", cmd)
		printTopHelp()
		return ExitUsage
	}
}

// ---------------------------------------------------------------------------
// App plumbing
// ---------------------------------------------------------------------------

type app struct {
	cfg     *config.Config
	cl      *client.Client
	jsonOut bool
	verbose bool
}

func parseGlobal(args []string) (globalOpts, error) {
	fs := flag.NewFlagSet("kp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {}
	var g globalOpts
	fs.BoolVar(&g.json, "json", false, "输出 JSON（给程序/agent 用）")
	fs.BoolVar(&g.verbose, "v", false, "打印请求细节")
	fs.StringVar(&g.server, "server", "", "覆盖服务端地址（也可用 KEYPOINT_SERVER）")
	fs.StringVar(&g.key, "key", "", "覆盖 API key（也可用 KEYPOINT_API_KEY）")
	fs.StringVar(&g.role, "role", "", "覆盖本次调用使用的角色")
	if err := fs.Parse(args); err != nil {
		return g, fmt.Errorf("全局参数错误: %w", err)
	}
	return g, nil
}

// newApp loads the local config and builds a client. It returns nil plus an
// exit code when the caller cannot proceed.
func newApp(g globalOpts) (*app, int) {
	cfg, err := config.Require()
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return nil, ExitNoConf
	}
	if g.server != "" {
		cfg.Server = strings.TrimRight(g.server, "/")
	}
	if g.key != "" {
		cfg.APIKey = g.key
	}
	if g.role != "" {
		cfg.ActiveRole = g.role
	}
	if g.verbose {
		os.Setenv("KEYPOINT_VERBOSE", "1")
	}
	return &app{cfg: cfg, cl: client.New(cfg), jsonOut: g.json, verbose: g.verbose}, ExitOK
}

// splitAtFirstNonFlag separates leading flags from the subcommand remainder.
// Without this, `kp --json task show KP-1 --side ui` would hand `--side ui` to
// the global parser and fail.
func splitAtFirstNonFlag(args []string) (head, tail []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		if strings.Contains(a, "=") {
			i++
			continue
		}
		switch a {
		case "-json", "--json", "-v", "-verbose":
			i++
		default:
			// A flag that takes a value.
			i += 2
		}
	}
	if i > len(args) {
		i = len(args)
	}
	return args[:i], args[i:]
}

func (a *app) out(v any) {
	if a.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
	}
}

func (a *app) text(s string) {
	if !a.jsonOut {
		fmt.Println(s)
	}
}

// fail prints an error the way the API shaped it, and picks an exit code from
// the HTTP status so scripts can distinguish "you typed it wrong" from "the
// server is down".
func (a *app) fail(err error) int {
	var apiErr *client.Error
	if errors.As(err, &apiErr) {
		if a.jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"ok": false, "error": apiErr.Code, "message": apiErr.Message,
				"hint": apiErr.Hint, "did_you_mean": apiErr.DidYouMean,
				"options": apiErr.Options, "status": apiErr.Status,
			})
			if apiErr.Status == 401 || apiErr.Status == 403 {
				return ExitAuth
			}
			return ExitError
		}
		fmt.Fprint(os.Stderr, apiErr.Pretty())
		if apiErr.Status == 401 || apiErr.Status == 403 {
			return ExitAuth
		}
		return ExitError
	}
	var connErr *client.ConnectionError
	if errors.As(err, &connErr) {
		fmt.Fprintf(os.Stderr, "✗ %s\n  → %s\n", connErr.Error(), connErr.Hint())
		return ExitServer
	}
	fmt.Fprintln(os.Stderr, "✗", err)
	return ExitError
}

func (a *app) usage(msg, hint string) int {
	fmt.Fprintf(os.Stderr, "✗ %s\n", msg)
	if hint != "" {
		fmt.Fprintf(os.Stderr, "  → %s\n", hint)
	}
	return ExitUsage
}

// requireArgs enforces an arity contract with a message that says what to type.
func (a *app) requireArgs(args []string, n int, usage, hint string) bool {
	if len(args) < n {
		a.usage("参数不足："+usage, hint)
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Small formatting helpers
// ---------------------------------------------------------------------------

func truncateRunes(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func padRunes(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[:n])
	}
	return s + strings.Repeat(" ", n-len(r))
}

// table renders simple aligned columns. Column widths are computed in runes so
// that CJK titles line up with ASCII ones.
func table(headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len([]rune(h))
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) {
				if n := len([]rune(cell)); n > widths[i] {
					widths[i] = n
				}
			}
		}
	}
	var b strings.Builder
	for i, h := range headers {
		b.WriteString(padRunes(h, widths[i]+2))
	}
	b.WriteString("\n")
	for _, row := range rows {
		for i := range headers {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			b.WriteString(padRunes(cell, widths[i]+2))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Help
// ---------------------------------------------------------------------------

func printTopHelp() {
	fmt.Print(`kp — Keypoint Notify 客户端

用法:
  kp <命令> [参数]
  kp <命令> --help          看某个命令的详细用法

常用（人和 agent 都用）:
  kp init                   初始化：连服务器、建身份、写配置
  kp board                  我手上的工作面 + 我负责的任务
  kp whoami                 我是谁、有什么角色、多少未读
  kp task list              列任务（--assigned me / --role / --status / --q）
  kp task new               建任务（从当前上下文抽骨架分段）
  kp task show KP-12        看任务全貌
  kp task pack KP-12        取"开工上下文包"——可直接粘进模型
  kp task seg KP-12 goal    取单段正文（可复制的最小单位）
  kp report KP-12 -m "..."  上报进展/阻塞/结果
  kp inbox                  收件箱

拆分与指派:
  kp task side add KP-12 ui --role frontend --deps api
  kp task side assign KP-12 ui --role frontend
  kp task side ls KP-12

管理:
  kp serve                  在本机起服务器
  kp identity ls|create|rotate|set-roles
  kp role ls                角色表（--holders 看谁持有）
  kp hook ls|add|rm         出站 webhook
  kp events --follow        事件流
  kp config get server      看/改本地配置
  kp docs                   打印 /api/v1/llms.txt（完整 API 说明）

全局参数:
  --json                    机器可读输出
  --server URL              覆盖服务端（或 KEYPOINT_SERVER）
  --key kp_...              覆盖 API key（或 KEYPOINT_API_KEY）
  --role <角色>             本次调用以哪个角色归属

给 agent 的建议路径:
  1) kp whoami                       确认身份与角色
  2) kp board                        找工作面
  3) kp task pack KP-12 --side ui    拿上下文开工
  4) kp report KP-12 --side ui --type result -m "..."   收工上报
`)
}

func printTaskHelp() {
	fmt.Print(`kp task — 任务

  list                     列表：--status --kind --priority --role --assigned me
                           --q --since --limit --group status --lite
  show <code>              全貌：--side <key> --reports <n>
  pack <code>              开工上下文包 ★
                           --side <key> --max-chars N --reports N --format md|json
                           --out <file>
  seg <code> <key>         取单段正文：--prompt（包一层"请基于它工作"的上下文）
  seg set <code> <key>     写分段：--title --file - --append --side <key>
  side ls <code>           --assignable（只看还没指派的）
  side add <code> <key>    --title --role --identity --deps a,b --repo --branch
  side assign <code> <key> --role --identity --status --deps
  side rm <code> <key>
  status <code> <新状态>    改任务状态
  edit <code>              --title --summary --priority --kind --label a,b
  new                      建任务，见 ` + "`kp task new --help`" + `
  rm <code>                删任务（需 admin）
`)
}

func printReportHelp() {
	fmt.Print(`kp report — 上报

  kp report <code> -m "文本" [--type T] [--side K] [--status S]
                   [--mention @a,b] [--attach f1,f2] [--priority P1]
  kp report <code> --from-json -        ← 从 stdin 读结构化 JSON（agent 用）

type: progress | blocker | decision | handoff | result | question
      blocker 且带 --side 时，那个工作面自动变 blocked
      progress/result 时，todo 自动变 doing
      --status 会同时把任务本身的状态改掉

--from-json 的 JSON 形状（未知字段会报错，别多写）:
  {
    "type": "blocker",
    "side_key": "ui",
    "body": "…",
    "segments": [{"key":"repro","title":"复现步骤","body":"…"}],
    "mentions": ["@backend","review"],
    "attachments": ["fil_xxx"],
    "status": "blocked",
    "priority": "P1"
  }
`)
}

// intersperse reorders arguments so flags may appear after positional ones.
//
// The stdlib flag package stops parsing at the first non-flag token, which
// would make `kp task pack KP-2 --side ui` silently ignore --side — the
// natural way to type the command. Rewriting the argument list before Parse
// makes position and flag order independent.
func intersperse(fs *flag.FlagSet, args []string) []string {
	isBool := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			isBool[f.Name] = true
		}
	})
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && !isBool[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positionals = append(positionals, a)
	}
	return append(flags, positionals...)
}
