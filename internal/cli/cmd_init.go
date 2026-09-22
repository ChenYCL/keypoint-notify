package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/light/keypoint-notify/internal/client"
	"github.com/light/keypoint-notify/internal/config"
	"github.com/light/keypoint-notify/internal/model"
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
		}
		if err := api.Post("/api/v1/bootstrap", body, &boot); err != nil {
			return (&app{cl: api}).fail(err)
		}
		cfg.APIKey = boot.APIKey
		cfg.Identity = boot.Identity.Name
		cfg.ActiveRole = boot.Identity.ActiveRole
		fmt.Printf("✓ 已创建身份 %q（角色：%s）\n", boot.Identity.Name, strings.Join(boot.Identity.Roles, ", "))
	}

	// 3. Still no key? The server is initialized, so it has to come from a human.
	if cfg.APIKey == "" {
		if *yes {
			fmt.Fprintln(os.Stderr, "✗ 服务端已初始化，需要 --key kp_... 才能继续（--yes 不会去猜 key）")
			return ExitUsage
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
  kp config set server URL  改字段（server / api_key / active_role / identity）
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
		default:
			fmt.Fprintf(os.Stderr, "✗ 未知字段 %q（server / api_key / identity / active_role）\n", args[1])
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

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 10 {
		return k
	}
	return k[:10] + "…"
}
