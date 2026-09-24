package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ChenYCL/keypoint-notify/internal/client"
	"github.com/ChenYCL/keypoint-notify/internal/config"
	"github.com/ChenYCL/keypoint-notify/internal/model"
)

// cmdInit connects this machine to a server and stores the resulting identity
// in ~/.keypoint/config.json. It is the one command that must work before any
// configuration exists, so it talks to the API with a bare client.
func cmdInit(args []string) int {
	fs := flag.NewFlagSet("kp init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "服务端地址，例如 https://keypoint.example.com")
	key := fs.String("key", "", "已有的 API key（服务端已初始化时用）")
	name := fs.String("name", "", "本机身份名，默认 <用户名>-<主机名>")
	kind := fs.String("kind", "agent", "human 或 agent")
	roles := fs.String("roles", "", "逗号分隔的角色，默认沿用服务端默认")
	yes := fs.Bool("yes", false, "不提问，接受默认值")
	fs.Usage = func() {
		fmt.Print(`kp init — 初始化本机配置

  kp init                                  连本机 127.0.0.1:8787，自动建身份
  kp init --server https://kp.example.com  连远程；已有系统时需要 --key
  kp init --server URL --key kp_xxx        纯非交互（agent / CI 用）
  kp init --name ci-runner --kind agent --roles backend,qa

配置文件写在 ~/.keypoint/config.json（0600）。用 KEYPOINT_HOME 可以换目录，
这样一台机器可以同时持有多套身份。
`)
	}
	if err := fs.Parse(intersperse(fs, args)); err != nil {
		return ExitUsage
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return ExitError
	}

	// Initialising over an existing binding silently replaces whose identity
	// this machine is — which is exactly what happens when someone means to add
	// a second identity and forgets KEYPOINT_HOME. Nothing is blocked (that
	// would be worse), but the change is announced and the old value is kept.
	previous, hadConfig := *cfg, config.Exists()
	if hadConfig && previous.APIKey != "" {
		if *key != "" && *key != previous.APIKey {
			fmt.Fprintf(os.Stderr, "⚠ %s 里已经有一个身份（%s）\n", config.Path(), orUnknown(previous.Identity))
			fmt.Fprintf(os.Stderr, "  这次会用新 key 覆盖它。想同时保留两个身份：\n")
			fmt.Fprintf(os.Stderr, "    KEYPOINT_HOME=~/.keypoint-%s kp init ...\n", orUnknown(previous.Identity))
		} else if *key == "" && *server != "" && !*yes {
			// Interactive run against a server that may differ from the stored one.
			fmt.Fprintf(os.Stderr, "⚠ 已有配置：%s（身份 %s）\n", previous.Server, orUnknown(previous.Identity))
		}
	}

	if *server != "" {
		cfg.Server = strings.TrimRight(*server, "/")
	}
	if *key != "" {
		cfg.APIKey = *key
	}

	api := client.New(cfg)

	// 1. Is the server reachable, and does it already have identities?
	var health struct {
		Status       string `json:"status"`
		Bootstrapped bool   `json:"bootstrapped"`
		Version      string `json:"version"`
		Admins       int    `json:"admins"`
	}
	if err := api.Get("/api/v1/health", &health); err != nil {
		var connErr *client.ConnectionError
		if errors.As(err, &connErr) {
			fmt.Fprintf(os.Stderr, "✗ %s\n", connErr.Error())
			fmt.Fprintf(os.Stderr, "  → %s\n", connErr.Hint())
			return ExitServer
		}
		return (&app{cl: api}).fail(err)
	}
	fmt.Printf("✓ 服务端 %s 可达（version %s，已初始化=%v）\n", cfg.Server, health.Version, health.Bootstrapped)

	// 2. No identities yet? Claim the first one.
	if cfg.APIKey == "" && !health.Bootstrapped {
		n := *name
		if n == "" {
			n = defaultIdentityName()
			if !*yes {
				n = prompt("身份名", n)
			}
		}
		k := *kind
		if !*yes {
			k = prompt("类型（human/agent）", k)
		}
		if k != model.KindHuman && k != model.KindAgent {
			k = model.KindAgent
		}
		body := map[string]any{"name": n, "kind": k}
		if *roles != "" {
			body["roles"] = splitCSV(*roles)
		}
		var boot struct {
			APIKey   string         `json:"api_key"`
			Identity model.Identity `json:"identity"`
			Recovery bool           `json:"recovery"`
			Note     string         `json:"note"`
		}
		if err := api.Post("/api/v1/bootstrap", body, &boot); err != nil {
			return (&app{cl: api}).fail(err)
		}
		if boot.Recovery {
			fmt.Println("⚠ 这是一次恢复性初始化：该服务端已经没有任何可用的 admin 身份。")
			fmt.Println("  常见原因是最后一个 admin 的 key 丢了（例如配置被覆盖）。新身份已拿到 admin。")
		}
		cfg.APIKey = boot.APIKey
		cfg.Identity = boot.Identity.Name
		cfg.ActiveRole = boot.Identity.ActiveRole
		fmt.Printf("✓ 已创建身份 %q（角色：%s）\n", boot.Identity.Name, strings.Join(boot.Identity.Roles, ", "))
	}

	// 3. Still no key? The server is initialized, so it has to come from a human.
	if cfg.APIKey == "" {
		if health.Admins == 0 {
			fmt.Fprintln(os.Stderr, "⚠ 这个服务端已经没有任何可用的 admin —— 常规管理操作全都做不了。")
			fmt.Fprintf(os.Stderr, "  运维这台机器的人可以认领：curl -X POST %s/api/v1/bootstrap \\\n", cfg.Server)
			fmt.Fprintln(os.Stderr, "    -H 'Content-Type: application/json' -d '{\"name\":\"<名字>\",\"kind\":\"human\"}'")
		}
		if *yes {
			fmt.Fprintln(os.Stderr, "✗ 服务端已初始化，需要 --key kp_... 才能继续（--yes 不会去猜 key）")
			return ExitUsage
		}
		if health.Admins == 0 {
			fmt.Println("⚠ 这个服务端已经没有任何可用的 admin —— 常规管理操作全都做不了。")
			fmt.Println("  如果你就是运维这台机器的人，可以直接认领：")
			fmt.Printf("    curl -X POST %s/api/v1/bootstrap -H 'Content-Type: application/json' \\\n", cfg.Server)
			fmt.Println("      -d '{\"name\":\"<你的名字>\",\"kind\":\"human\"}'")
			fmt.Println()
		}
		fmt.Println("服务端已初始化。需要一个 API key 才能接入：")
		fmt.Println("  · 在已登录的网页 /admin 页面里新建身份或轮换 key")
		fmt.Println("  · 或让持有 key 的人执行 `kp identity create --name <你>` 并把 key 给你")
		cfg.APIKey = strings.TrimSpace(prompt("粘贴 API key", ""))
		if cfg.APIKey == "" {
			fmt.Fprintln(os.Stderr, "✗ 没有 key，放弃")
			return ExitUsage
		}
	}

	// 4. Verify the key and remember who we are.
	api.APIKey = cfg.APIKey
	var who struct {
		Identity model.Identity `json:"identity"`
		Role     string         `json:"role"`
		Unread   int            `json:"unread"`
	}
	if err := api.Get("/api/v1/whoami", &who); err != nil {
		return (&app{cl: api}).fail(err)
	}
	cfg.Identity = who.Identity.Name
	cfg.ActiveRole = who.Role
	if hadConfig && previous.APIKey != "" && previous.APIKey != cfg.APIKey {
		backup := config.Path() + ".bak-" + time.Now().Format("20060102-150405")
		if data, rerr := os.ReadFile(config.Path()); rerr == nil {
			if werr := os.WriteFile(backup, data, 0o600); werr == nil {
				fmt.Fprintf(os.Stderr, "  旧配置已备份到 %s\n", backup)
			}
		}
	}
	if err := cfg.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "✗ 写配置失败:", err)
		return ExitError
	}

	fmt.Printf("✓ 已连接为 %q（角色：%s，激活：%s，未读 %d）\n",
		who.Identity.Name, strings.Join(who.Identity.Roles, ", "), who.Role, who.Unread)
	fmt.Printf("✓ 配置写入 %s\n\n", config.Path())
	fmt.Println("下一步：")
	fmt.Println("  kp board                 我手上有哪些工作面")
	fmt.Println("  kp task list             看全部任务")
	fmt.Println("  kp task new --help       怎么建任务")
	fmt.Println("  kp docs                  完整 API 说明（给模型读的版本）")
	return ExitOK
}

// ---------------------------------------------------------------------------
// config
// ---------------------------------------------------------------------------

func cmdConfig(args []string) int {
	if len(args) == 0 {
		fmt.Print(`kp config — 本地配置（~/.keypoint/config.json）

  kp config show            打印当前配置（key 打码）
  kp config get server      取单个字段
  kp config set server URL  改字段（server / api_key / active_role / identity / public_url）
  kp config set public_url https://kp.example.com
                            接入说明里给别人的地址（管理员在服务端本机连 127.0.0.1 时用）
  kp config path            配置文件路径
  kp config env             打印可导出的环境变量
`)
		return ExitOK
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return ExitError
	}
	switch args[0] {
	case "show":
		masked := *cfg
		if masked.APIKey != "" {
			masked.APIKey = maskKey(masked.APIKey)
		}
		data, _ := json.MarshalIndent(masked, "", "  ")
		fmt.Println(string(data))
		fmt.Printf("\n配置文件：%s\n", config.Path())
		return ExitOK
	case "path":
		fmt.Println(config.Path())
		return ExitOK
	case "env":
		fmt.Printf("export KEYPOINT_SERVER=%q\nexport KEYPOINT_API_KEY=%q\nexport KEYPOINT_ROLE=%q\n",
			cfg.Server, maskKey(cfg.APIKey), cfg.ActiveRole)
		return ExitOK
	case "get":
		if len(args) < 2 {
			return ExitUsage
		}
		switch args[1] {
		case "server":
			fmt.Println(cfg.Server)
		case "api_key":
			fmt.Println(maskKey(cfg.APIKey))
		case "identity":
			fmt.Println(cfg.Identity)
		case "active_role":
			fmt.Println(cfg.ActiveRole)
		case "public_url":
			fmt.Println(cfg.PublicURL)
		default:
			fmt.Fprintf(os.Stderr, "✗ 未知字段 %q（server / api_key / identity / active_role / public_url）\n", args[1])
			return ExitUsage
		}
		return ExitOK
	case "set":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "用法：kp config set <字段> <值>")
			return ExitUsage
		}
		switch args[1] {
		case "server":
			cfg.Server = strings.TrimRight(args[2], "/")
		case "api_key":
			cfg.APIKey = strings.TrimSpace(args[2])
		case "identity":
			cfg.Identity = args[2]
		case "active_role":
			cfg.ActiveRole = args[2]
		case "public_url":
			cfg.PublicURL = strings.TrimRight(args[2], "/")
		default:
			fmt.Fprintf(os.Stderr, "✗ 未知字段 %q\n", args[1])
			return ExitUsage
		}
		if err := cfg.Save(); err != nil {
			fmt.Fprintln(os.Stderr, "✗", err)
			return ExitError
		}
		fmt.Printf("✓ %s 已更新\n", args[1])
		return ExitOK
	default:
		fmt.Fprintf(os.Stderr, "✗ 未知子命令 %q\n", args[0])
		return ExitUsage
	}
}

// cmdDocs prints the server's own API manual, so an agent can read the live
// contract rather than a copy that might be stale.
func cmdDocs(args []string, g globalOpts) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return ExitError
	}
	if g.server != "" {
		cfg.Server = strings.TrimRight(g.server, "/")
	}
	if g.key != "" {
		cfg.APIKey = g.key
	}
	api := client.New(cfg)
	body, err := api.GetText("/api/v1/llms.txt")
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗ 取 /api/v1/llms.txt 失败:", err)
		fmt.Fprintln(os.Stderr, "  → 服务端可能太旧；也可以直接看仓库里的 docs/api.md")
		return ExitServer
	}
	if len(args) > 0 && args[0] == "schema" {
		var schema string
		if err := api.Get("/api/v1/schema", &schema); err == nil {
			fmt.Println(schema)
			return ExitOK
		}
	}
	fmt.Print(body)
	return ExitOK
}

// ---------------------------------------------------------------------------
// prompt helpers
// ---------------------------------------------------------------------------

func prompt(label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return def
	}
	v := strings.TrimSpace(sc.Text())
	if v == "" {
		return def
	}
	return v
}

func defaultIdentityName() string {
	user := os.Getenv("USER")
	if user == "" {
		user = "user"
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return user
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	return user + "-" + host
}

func splitCSV(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.TrimPrefix(p, "@"))
		}
	}
	return out
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未知"
	}
	return s
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 10 {
		return k
	}
	return k[:10] + "…"
}

// ---------------------------------------------------------------------------
// kp install — 装环境：把 skill 从服务端拉到本地
// ---------------------------------------------------------------------------

// cmdInstall pulls the agent skill from a running server and installs it where
// Claude Code will find it.
//
// The skill also ships inside the binary, and `kp skill install` uses that copy
// — but a session on another machine that only has a key and a URL needs the
// networked path: connect, pull, install, and be told what to do next.
func cmdInstall(args []string, g globalOpts) int {
	fs := flag.NewFlagSet("kp install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "装到哪；- 表示打印到 stdout（默认按 --target 自动选）")
	target := fs.String("target", "auto", "给哪个 CLI 装：auto|claude|codex|gemini|opencode|kimi|agents|all")
	from := fs.String("from", "", "从哪个服务端拉（默认用本地配置里的）")
	key := fs.String("key", "", "API key（首次接入时配合 --from 使用）")
	only := fs.Bool("only-skill", false, "只装 skill，不检查接入")
	fs.Usage = func() {
		fmt.Print(`kp install — 装环境

  kp install                             自动探测本机装了哪些 CLI，各装一份
  kp install --target claude             只给 Claude Code 装
  kp install --target codex,gemini       给多个装
  kp install --target agents             装成项目里的 AGENTS.md（任何 CLI 都读）
  kp install --from URL --key kp_xxx     首次接入：连上去 + 装
  kp install --dir -                     只打印，不落盘

各 CLI 的约定不一样，装的位置也不同：

  claude    ~/.claude/skills/keypoint-notify/SKILL.md   带 frontmatter 的技能目录
  codex     ~/.codex/AGENTS.md                          追加一节
  gemini    ~/.gemini/GEMINI.md                         追加一节
  opencode  ~/.config/opencode/AGENTS.md                追加一节
  kimi      ~/.kimi-code/skills/keypoint-notify/        技能目录（Kimi Code 认 SKILL.md）
  agents    ./AGENTS.md（当前目录，跟仓库走）            追加一节

auto（默认）会探测哪些目录存在，存在就装。没探测到就提示你用
--target 指定，而不是默默什么都不做。

skill 内容从**服务端**拉（GET /skill/SKILL.md），所以对方拿到的永远是
这个服务端当前版本的行为手册，不是随二进制发的旧副本。
`)
	}
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fs.Usage()
		return ExitOK
	}
	if err := fs.Parse(intersperse(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.Usage()
			return ExitOK
		}
		return ExitUsage
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return ExitError
	}
	if *from != "" {
		cfg.Server = strings.TrimRight(*from, "/")
	}
	if *key != "" {
		cfg.APIKey = *key
	}
	api := client.New(cfg)

	// 1. 先确认能连上、key 有效 —— 装一个连不上的 skill 没有意义
	var who struct {
		Identity model.Identity `json:"identity"`
		Role     string         `json:"role"`
	}
	if !*only {
		if err := api.Get("/api/v1/whoami", &who); err != nil {
			var ce *client.ConnectionError
			if errors.As(err, &ce) {
				fmt.Fprintf(os.Stderr, "✗ %s\n  → %s\n", ce.Error(), ce.Hint())
				return ExitServer
			}
			return (&app{cl: api, cfg: cfg}).fail(err)
		}
		fmt.Printf("✓ 已接入 %s（@%s）@ %s\n", who.Identity.Name, who.Role, cfg.Server)
	}

	// 2. 把 skill 拉下来：一个行为手册（keypoint-notify）+ 若干斜杠命令（kp-next …）
	contents, extras, err := fetchSkills(api)
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		fmt.Fprintln(os.Stderr, "  → 服务端可能太旧，没有 /skill 端点。升级服务端后再装")
		return ExitError
	}

	if *dir == "-" {
		fmt.Print(contents["SKILL.md"])
		return ExitOK
	}

	// 显式 --dir 优先，其次 --target，否则自动探测
	if *dir != "" {
		if err := writeSkillDir(*dir, contents); err != nil {
			fmt.Fprintln(os.Stderr, "✗", err)
			return ExitError
		}
		fmt.Printf("✓ skill 已装到 %s（%d 个文件，来自 %s）\n", *dir, len(contents), cfg.Server)
	} else {
		installed, skipped, err := installForTargets(*target, contents, extras, cfg.Server)
		if err != nil {
			fmt.Fprintln(os.Stderr, "✗", err)
			return ExitError
		}
		if len(installed) == 0 {
			fmt.Fprintln(os.Stderr, "✗ 没有装到任何地方。")
			fmt.Fprintln(os.Stderr, "  本机没探测到已知 CLI 的配置目录。指定一个：")
			fmt.Fprintln(os.Stderr, "    kp install --target claude      ~/.claude/skills/")
			fmt.Fprintln(os.Stderr, "    kp install --target agents      当前目录的 AGENTS.md（任何 CLI 都读）")
			fmt.Fprintln(os.Stderr, "    kp install --dir <路径>          装到任意位置")
			return ExitError
		}
		for _, line := range installed {
			fmt.Println("✓ " + line)
		}
		for _, line := range skipped {
			fmt.Println("· " + line)
		}
	}

	// 3. 把接入信息写进配置，顺带告诉对方下一步
	if err := cfg.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "✗ 写配置失败:", err)
		return ExitError
	}
	fmt.Printf("✓ 配置已写入 %s\n\n", config.Path())
	fmt.Println("现在可以：")
	fmt.Println("  在 Claude Code 里说「我手上有什么」「记个任务」「上报一下」")
	fmt.Println("  或直接在终端：")
	fmt.Println("    kp next --wait 30 --claim     阻塞等活（有活就返回完整开工包）")
	fmt.Println("    kp loop                       一直等活，拿到就打印")
	fmt.Println("    kp loop --run 'claude -p \"$KP_PACK\"'   自动喂给一个新会话")
	return ExitOK
}

// ---------------------------------------------------------------------------
// 各家 CLI 的指令文件约定
// ---------------------------------------------------------------------------

// skillTargets maps a CLI name to where it expects to find standing
// instructions. Only Claude Code has a real skill format (a directory with
// frontmatter); everyone else reads a single markdown file, so for them the
// skill body is appended into a delimited section that can be replaced on
// re-install without touching the rest of the file.
var skillTargets = map[string]struct {
	Path  string // "" means relative to cwd
	Label string
}{
	"claude":   {"", "Claude Code（~/.claude/skills/keypoint-notify/）"},
	"codex":    {"~/.codex/AGENTS.md", "Codex CLI（~/.codex/AGENTS.md）"},
	"gemini":   {"~/.gemini/GEMINI.md", "Gemini CLI（~/.gemini/GEMINI.md）"},
	"opencode": {"~/.config/opencode/AGENTS.md", "opencode（~/.config/opencode/AGENTS.md）"},
	"kimi":     {"", "Kimi Code（~/.kimi-code/skills/keypoint-notify/）"},
	"agents":   {"AGENTS.md", "当前目录 AGENTS.md（跟仓库走，任何 CLI 都读）"},
}

// sectionMarkers delimit the injected block so a re-install replaces it instead
// of stacking a second copy — users keep their own content in these files.
const (
	sectionStart = "<!-- keypoint:start -->"
	sectionEnd   = "<!-- keypoint:end -->"
)

// detectTargets returns the target names whose config directory already exists,
// which is the honest signal that the CLI is installed on this machine. Claude
// Code is a special case: its skills directory is created on first use, so the
// presence of ~/.claude is what we look for.
func detectTargets(home string) []string {
	probes := map[string]string{
		"claude":   filepath.Join(home, ".claude"),
		"codex":    filepath.Join(home, ".codex"),
		"gemini":   filepath.Join(home, ".gemini"),
		"opencode": filepath.Join(home, ".config", "opencode"),
		"kimi":     filepath.Join(home, ".kimi-code"),
	}
	out := []string{}
	for _, name := range []string{"claude", "codex", "gemini", "opencode", "kimi"} {
		if st, err := os.Stat(probes[name]); err == nil && st.IsDir() {
			out = append(out, name)
		}
	}
	return out
}

// installForTargets installs the skill for each requested CLI.
func installForTargets(spec string, contents map[string]string, extras map[string]map[string]string, server string) (installed, skipped []string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, fmt.Errorf("找不到家目录: %w", err)
	}

	var names []string
	switch spec {
	case "", "auto":
		names = detectTargets(home)
		if len(names) == 0 {
			return nil, nil, nil
		}
	case "all":
		for n := range skillTargets {
			names = append(names, n)
		}
		sort.Strings(names)
	default:
		for _, part := range strings.Split(spec, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, ok := skillTargets[part]; !ok {
				return nil, nil, fmt.Errorf("未知 target %q（可选：claude codex gemini opencode kimi agents）", part)
			}
			names = append(names, part)
		}
	}

	for _, name := range names {
		t := skillTargets[name]
		// Claude Code and Kimi Code both load a real SKILL.md directory, so they
		// get the full skill tree rather than an appended AGENTS.md section.
		// Kimi Code keeps its data in ~/.kimi-code/ (the older ~/.kimi/ belonged
		// to the archived kimi-cli and is not read any more).
		if dir, ok := skillDirTargets(home)[name]; ok {
			if err := writeSkillDir(dir, contents); err != nil {
				return installed, skipped, err
			}
			// The command skills sit next to the playbook, one directory each,
			// which is what makes them show up as /kp-next (Claude Code) and
			// /skill:kp-next (Kimi Code).
			for cmd, files := range extras {
				if err := writeSkillDir(filepath.Join(filepath.Dir(dir), cmd), files); err != nil {
					return installed, skipped, err
				}
			}
			label := t.Label
			if len(extras) > 0 {
				label += fmt.Sprintf(" + %d 个斜杠命令", len(extras))
			}
			installed = append(installed, label)
			continue
		}
		path := t.Path
		if strings.HasPrefix(path, "~/") {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
		if err := writeSection(path, contents["SKILL.md"], server); err != nil {
			return installed, skipped, err
		}
		installed = append(installed, t.Label)
	}
	return installed, skipped, nil
}

// writeSkillDir writes the full skill tree, replacing any previous install.
func writeSkillDir(dir string, contents map[string]string) error {
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("清不掉旧目录: %w", err)
	}
	for f, body := range contents {
		path := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return fmt.Errorf("写 %s 失败: %w", f, err)
		}
	}
	return nil
}

// writeSection injects the skill into a single-file convention (AGENTS.md and
// friends), replacing the previous block if one is there and leaving everything
// else in the file untouched.
func writeSection(path, body, server string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(existing)

	block := sectionStart + "\n" +
		"<!-- 由 `kp install` 生成，来自 " + server + "；重新安装会替换本节，手工改动会丢 -->\n\n" +
		body + "\n" + sectionEnd

	if i := strings.Index(text, sectionStart); i >= 0 {
		if j := strings.Index(text[i:], sectionEnd); j >= 0 {
			text = text[:i] + block + text[i+j+len(sectionEnd):]
		} else {
			text = text[:i] + block
		}
	} else if strings.TrimSpace(text) == "" {
		text = block + "\n"
	} else {
		text = strings.TrimRight(text, "\n") + "\n\n" + block + "\n"
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// skillDirTargets are the CLIs that read a SKILL.md directory natively.
func skillDirTargets(home string) map[string]string {
	return map[string]string{
		"claude": filepath.Join(home, ".claude", "skills", "keypoint-notify"),
		"kimi":   filepath.Join(home, ".kimi-code", "skills", "keypoint-notify"),
	}
}

// fetchSkills pulls the playbook and the command skills from the server.
//
// Newer servers publish /skill/index.json; older ones only serve the playbook
// at fixed paths, and for those the playbook alone is installed — the command
// skills are a convenience, not something to fail an install over.
func fetchSkills(api *client.Client) (main map[string]string, extras map[string]map[string]string, err error) {
	var idx struct {
		Main   string              `json:"main"`
		Skills map[string][]string `json:"skills"`
	}
	if err := api.Get("/skill/index.json", &idx); err != nil || len(idx.Skills) == 0 {
		main = map[string]string{}
		for _, f := range []string{"SKILL.md", "reference/commands.md", "reference/api.md", "reference/recipes.md"} {
			body, err := api.GetText("/skill/" + f)
			if err != nil {
				return nil, nil, fmt.Errorf("拉取 /skill/%s 失败：%w", f, err)
			}
			main[f] = body
		}
		return main, nil, nil
	}
	extras = map[string]map[string]string{}
	for name, files := range idx.Skills {
		got := map[string]string{}
		for _, f := range files {
			body, err := api.GetText("/skill/" + name + "/" + f)
			if err != nil {
				return nil, nil, fmt.Errorf("拉取 /skill/%s/%s 失败：%w", name, f, err)
			}
			got[f] = body
		}
		if name == idx.Main {
			main = got
		} else {
			extras[name] = got
		}
	}
	if main["SKILL.md"] == "" {
		return nil, nil, fmt.Errorf("服务端的 skill 索引里没有行为手册 %q", idx.Main)
	}
	return main, extras, nil
}
