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

	"github.com/ChenYCL/keypoint-notify/internal/client"
	"github.com/ChenYCL/keypoint-notify/internal/config"
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
	// Asking for help is not an error. flag.Parse reports a bare -h/--help as
	// ErrHelp, which used to surface as "✗ 全局参数错误: flag: help requested"
	// and exit 2 — a red error message in response to the most benign input
	// there is.
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "-h" || a == "--help" {
			printTopHelp()
			return ExitOK
		}
		if len(a) > 1 && a[0] != '-' {
			break // reached the subcommand; let it handle its own --help
		}
	}

	head, tail := splitAtFirstNonFlag(args)
	g, err := parseGlobal(head)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printTopHelp()
			return ExitOK
		}
		fmt.Fprintln(os.Stderr, "✗", err)
		fmt.Fprintln(os.Stderr, "  → kp --help 看完整用法")
		return ExitUsage
	}
	if len(tail) == 0 {
		// No command at all: show the help, but exit non-zero because nothing
		// was accomplished. Scripts check the code; people read the text.
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
		if len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help") {
			fmt.Print(usageFor("docs"))
			return ExitOK
		}
		return cmdDocs(rest, g)
	case "install":
		return cmdInstall(rest, g)
	}

	// Help is documentation: it must not require a configured identity, a
	// reachable server, or any other precondition. Someone who has not
	// installed anything yet is exactly who needs it most. So when the user is
	// asking for help, build an app with an empty config (no key, no server
	// contact) rather than failing at config load.
	helpOnly := wantsHelp(rest)

	a, code := newAppWith(g, helpOnly)
	if a == nil {
		return code
	}

	// `kp <cmd> --help` for commands that take no flags of their own. The
	// per-command help texts are the same ones shown on a bare `kp <cmd>`, so
	// there is nothing extra to maintain — and it means help never falls
	// through to a network call.
	if helpOnly {
		if print := helpFor(cmd); print != nil {
			print()
			return ExitOK
		}
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
	case "next":
		return a.next(rest)
	case "loop":
		return a.loop(rest)
	case "claim":
		return a.claim(rest)
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
func newApp(g globalOpts) (*app, int) { return newAppWith(g, false) }

// newAppWith is newApp with an escape hatch: tolerant=true accepts a missing or
// broken config so a help request can still be answered. The client it returns
// cannot reach anything, which is fine — the caller is only going to print
// documentation.
func newAppWith(g globalOpts, tolerant bool) (*app, int) {
	cfg, err := config.Require()
	if err != nil {
		if !tolerant {
			fmt.Fprintln(os.Stderr, "✗", err)
			return nil, ExitNoConf
		}
		cfg = &config.Config{Server: config.DefaultServer}
		if loaded, lerr := config.Load(); lerr == nil {
			cfg = loaded
		}
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
  kp next --wait 30         有没有轮到我干的活（长轮询，含完整上下文）
  kp loop                   一直等活，拿到就打印
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
  kp install                装 skill 到 ~/.claude/skills（从服务端拉）

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

// parseSub parses a subcommand's own flags *plus* the global ones that make
// sense to repeat there.
//
// Everyone types `kp task list --json`, not `kp --json task list`, and the
// reference docs recommend the former — so a subcommand flag set that rejects
// --json is a broken promise. --server and --key are accepted here too, but
// only when the subcommand does not already define them (kp init does).
//
// --role is deliberately NOT bound here: on `kp task list` it already means
// "filter by this role", which is the more common meaning at that position.
// Use `kp --role X task list` to act as another role.
// errHelp means "the user asked for help" — not a failure, but not
// "carry on and do the work" either. Callers turn it into a zero exit.
var errHelp = errors.New("help requested")

func (a *app) parseSub(fs *flag.FlagSet, args []string) error {
	var jsonOut *bool
	var server, key *string
	if fs.Lookup("json") == nil {
		jsonOut = fs.Bool("json", false, "输出 JSON（给程序/agent 用）")
	}
	if fs.Lookup("v") == nil {
		fs.Bool("v", false, "打印请求细节")
	}
	if fs.Lookup("server") == nil {
		server = fs.String("server", "", "覆盖服务端地址（也可用 KEYPOINT_SERVER）")
	}
	if fs.Lookup("key") == nil {
		key = fs.String("key", "", "覆盖 API key（也可用 KEYPOINT_API_KEY）")
	}
	if err := fs.Parse(intersperse(fs, args)); err != nil {
		// -h/--help is not a parse failure, but flag.Parse reports it as one.
		// Returning it would surface as "未知命令" or a red error for the most
		// benign input a user can type.
		if errors.Is(err, flag.ErrHelp) {
			return errHelp
		}
		return err
	}
	if jsonOut != nil && *jsonOut {
		a.jsonOut = true
	}
	if server != nil && *server != "" {
		a.cfg.Server = strings.TrimRight(*server, "/")
		a.cl.BaseURL = a.cfg.Server
	}
	if key != nil && *key != "" {
		a.cfg.APIKey = *key
		a.cl.APIKey = *key
	}
	return nil
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

// wantsHelp reports whether the remaining arguments contain -h/--help before
// any positional argument that would change the meaning of the command.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// helpFor returns the function that prints a command's help, for the commands
// whose help is unconditional (they take no flags of their own). Commands with
// a flag set handle --help themselves via flag.ErrHelp.
func helpFor(cmd string) func() {
	switch cmd {
	case "task", "tasks":
		return printTaskHelp
	case "report", "rep":
		return printReportHelp
	case "whoami", "board", "next", "loop", "claim", "inbox", "events",
		"attach", "install", "docs", "config", "role", "roles", "identity",
		"id", "hook", "webhook":
		return func() { fmt.Print(usageFor(cmd)) }
	}
	return nil
}

// usageFor holds the one-line usage for commands that only have a bare form.
// These are short on purpose: the full text lives in the command's own --help
// path, and this is the fallback for "I typed --help and nothing was configured".
func usageFor(cmd string) string {
	switch cmd {
	case "whoami":
		return "kp whoami\n\n  身份、角色、未读、能力、入口提示。\n"
	case "board":
		return "kp board\n\n  我手上的工作面 + 我负责的任务 + 下一步建议。\n"
	case "next":
		return "kp next [--wait 30] [--claim] [--task KP-12] [--side ui] [--json]\n\n" +
			"  轮到我干的活：为什么是我 + 完整开工包。没有活时服务端挂起。\n" +
			"  详见 kp next --help（完整用法见 /skill/reference/commands.md）\n"
	case "loop":
		return "kp loop [--wait 20] [--max N] [--run '<cmd>'] [--task KP-12]\n\n" +
			"  一直等活，拿到就打印（或喂给 --run 指定的命令）。\n"
	case "claim":
		return "kp claim <code> <side>\n\n  原子认领一个工作面；已被拿走则退出码非 0。\n"
	case "inbox":
		return "kp inbox [--unread] [--read-all] [--read id1,id2] [--limit N]\n\n  收件箱。\n"
	case "events":
		return "kp events [--since <游标>] [--type report] [--task KP-12] [--follow]\n\n  事件流。\n"
	case "attach":
		return "kp attach <file>... [--task KP-12] [--side ui]\n\n  上传附件。\n"
	case "install":
		return "kp install [--target claude,codex,...] [--from URL] [--key kp_xxx] [--dir <路径>]\n\n" +
			"  从服务端拉 skill 装到本机认识的 CLI 里。\n"
	case "docs":
		return "kp docs\n\n  打印 /api/v1/llms.txt（完整 API 说明）。\n"
	case "config":
		return "kp config show | get <字段> | set <字段> <值> | path | env\n"
	case "role", "roles":
		return "kp role ls [--holders] [--keys] | add <key> | rm <key>\n"
	case "identity", "id":
		return "kp identity ls | create <name> | set-roles <name> <roles> | rotate <name> | disable|enable <name>\n"
	case "hook", "webhook":
		return "kp hook ls | add <url> [--secret S] [--events a,b] | rm <id>\n"
	}
	return "kp " + cmd + "\n"
}
