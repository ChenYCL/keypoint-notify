package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func sleepSeconds(n int) { time.Sleep(time.Duration(n) * time.Second) }

// ---------------------------------------------------------------------------
// kp role
// ---------------------------------------------------------------------------

func (a *app) role(args []string) int {
	if len(args) == 0 {
		fmt.Print(`kp role — 角色

  kp role ls              角色表
  kp role ls --holders    角色 → 谁持有（指派前查这个）
  kp role ls --keys       只要 key 列表（脚本/agent 用）
  kp role add <key> --name 显示名 --desc 说明
  kp role rm <key>        删除自定义角色（内置不可删）

角色是"路由地址"，不是职级：任务和工作面按角色指派，谁持有这个角色谁就接。
内置：member backend frontend review qa ops design
`)
		return ExitOK
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "ls", "list":
		fs := flag.NewFlagSet("kp role ls", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		holders := fs.Bool("holders", false, "带上持有者")
		keys := fs.Bool("keys", false, "只输出 key 列表")
		if err := a.parseSub(fs, rest); err != nil {
			return ExitUsage
		}
		var raw map[string]any
		path := "/api/v1/roles"
		switch {
		case *keys:
			path += "?keys=1"
		case *holders:
			path += "?holders=1"
		}
		if err := a.cl.Get(path, &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		if *keys {
			for _, k := range anySlice(raw["keys"]) {
				fmt.Println(str(k))
			}
			return ExitOK
		}
		holderMap, _ := raw["holders"].(map[string]any)
		rows := [][]string{}
		for _, rv := range anySlice(raw["roles"]) {
			r, _ := rv.(map[string]any)
			h := "—"
			if hs, ok := holderMap[str(r["key"])].([]any); ok && len(hs) > 0 {
				parts := make([]string, 0, len(hs))
				for _, x := range hs {
					parts = append(parts, str(x))
				}
				h = strings.Join(parts, ",")
			}
			builtin := ""
			if b, _ := r["builtin"].(bool); b {
				builtin = "内置"
			}
			rows = append(rows, []string{str(r["key"]), str(r["name"]), h, builtin, truncateRunes(str(r["description"]), 40)})
		}
		fmt.Print(table([]string{"KEY", "名称", "持有者", "", "说明"}, rows))
		return ExitOK

	case "add", "set":
		fs := flag.NewFlagSet("kp role add", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		name := fs.String("name", "", "显示名")
		desc := fs.String("desc", "", "说明：这个角色接什么活")
		if err := a.parseSub(fs, rest); err != nil {
			return ExitUsage
		}
		key := fs.Arg(0)
		if key == "" {
			return a.usage("用法：kp role add <key> [--name 显示名 --desc 说明]", "")
		}
		var raw map[string]any
		if err := a.cl.Post("/api/v1/roles", map[string]any{
			"key": key, "name": orElseStr(*name, key), "description": *desc,
		}, &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		fmt.Printf("✓ 角色 @%s 已就绪\n", key)
		return ExitOK

	case "rm", "del":
		if len(rest) < 1 {
			return ExitUsage
		}
		var raw map[string]any
		if err := a.cl.Delete("/api/v1/roles/"+rest[0], &raw); err != nil {
			return a.fail(err)
		}
		a.out(raw)
		if !a.jsonOut {
			fmt.Printf("✓ 已删除角色 %s\n", rest[0])
		}
		return ExitOK
	default:
		return a.usage("未知子命令 role "+sub, "kp role 看用法")
	}
}

// queryIf returns a query suffix (leading "?") when cond holds.
//
// The leading "?" is part of the contract: callers append this straight onto a
// path, and a version that returned a bare `keys=1` silently produced
// `/api/v1/roleskeys=1` — a 404 that looks like a missing endpoint rather than a
// malformed URL.
func queryIf(cond bool, q string) string {
	if cond && q != "" {
		return "?" + q
	}
	return ""
}

func orElseStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// ---------------------------------------------------------------------------
// kp identity
// ---------------------------------------------------------------------------

func (a *app) identity(args []string) int {
	if len(args) == 0 {
		fmt.Print(`kp identity — 身份与 API key

  kp identity ls                        所有身份及其角色
  kp identity create <name> [--kind agent --roles backend,qa]
                                        新建身份，返回一次性 key
  kp identity set-roles <name> a,b,c    改绑角色（key 不变）  ← 需 admin
  kp identity use <name>                切换本机身份（改 active_role 归属）
  kp identity rotate <name>             轮换 key（旧的立即失效）
  kp identity disable/enable <name>

角色可以随时改绑而不动 key —— 这是"角色+apikey 绑定身份（可修改）"的落地方式。
`)
		return ExitOK
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "ls", "list":
		names := false
		if len(rest) > 0 && (rest[0] == "--names" || rest[0] == "-names") {
			names = true
		}
		var raw map[string]any
		path := "/api/v1/identities"
		if names {
			path += "?names=1"
		}
		if err := a.cl.Get(path, &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		if names {
			for _, n := range anySlice(raw["names"]) {
				fmt.Println(str(n))
			}
			return ExitOK
		}
		rows := [][]string{}
		for _, iv := range anySlice(raw["identities"]) {
			id, _ := iv.(map[string]any)
			roles := []string{}
			for _, r := range anySlice(id["roles"]) {
				roles = append(roles, str(r))
			}
			state := ""
			if d, _ := id["disabled"].(bool); d {
				state = "已停用"
			}
			me := ""
			if a.cfg.Identity == str(id["name"]) {
				me = "← 本机"
			}
			rows = append(rows, []string{
				str(id["name"]), str(id["kind"]), strings.Join(roles, ","),
				str(id["key_prefix"]), state, me,
			})
		}
		fmt.Print(table([]string{"身份", "类型", "角色", "key", "", ""}, rows))
		return ExitOK

	case "create", "add":
		fs := flag.NewFlagSet("kp identity create", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		kind := fs.String("kind", "agent", "human 或 agent")
		roles := fs.String("roles", "member", "逗号分隔")
		active := fs.String("active", "", "激活角色，默认第一个")
		if err := a.parseSub(fs, rest); err != nil {
			return ExitUsage
		}
		name := fs.Arg(0)
		if name == "" {
			return a.usage("用法：kp identity create <name> [--kind agent --roles backend,qa]", "")
		}
		var raw map[string]any
		if err := a.cl.Post("/api/v1/identities", map[string]any{
			"name": name, "kind": *kind, "roles": splitCSV(*roles), "active_role": *active,
		}, &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		key := str(raw["api_key"])
		idn, _ := raw["identity"].(map[string]any)
		var granted []string
		for _, r := range anySlice(idn["roles"]) {
			granted = append(granted, str(r))
		}
		fmt.Printf("✓ 身份 %s 已创建（角色 %s）\n\n", name, strings.Join(granted, ", "))
		fmt.Println("把下面这整段发给对方，对方一条命令就能接入：")
		fmt.Println()
		fmt.Println("  ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈")
		fmt.Println("  你被拉进了一个 Keypoint 协作中枢（任务 / 上报 / 交接）。")
		fmt.Println()
		fmt.Println("  一条命令接入：")
		fmt.Printf("    kp init --server %s --key %s --yes\n", a.cfg.Server, key)
		fmt.Println()
		fmt.Printf("  你的身份是 %s，角色 @%s。\n", name, strings.Join(granted, "、@"))
		fmt.Println("  接入后：")
		fmt.Println("    kp board                     我手上有什么")
		fmt.Println("    kp next --wait 30 --claim    等活（会阻塞；有活就返回完整开工包）")
		fmt.Println("    kp docs                      完整说明（也可以直接喂给模型）")
		fmt.Println("  ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈")
		fmt.Println()
		fmt.Println("  ⚠️  key 只显示这一次，上面那段请立刻发出去；丢了只能 kp identity rotate")
		return ExitOK

	case "set-roles", "roles":
		fs := flag.NewFlagSet("kp identity set-roles", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		active := fs.String("active", "", "同时改激活角色")
		if err := a.parseSub(fs, rest); err != nil {
			return ExitUsage
		}
		name := fs.Arg(0)
		list := fs.Arg(1)
		if name == "" || list == "" {
			return a.usage("用法：kp identity set-roles <name> <role1,role2>", "例如 kp identity set-roles alice backend,review")
		}
		id, err := a.findIdentity(name)
		if err != nil {
			return a.fail(err)
		}
		payload := map[string]any{"roles": splitCSV(list)}
		if *active != "" {
			payload["active_role"] = *active
		}
		var raw map[string]any
		if err := a.cl.Patch("/api/v1/identities/"+id, payload, &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		fmt.Printf("✓ %s 的角色 → %s（key 未变）\n", name, list)
		return ExitOK

	case "use":
		if len(rest) < 1 {
			return a.usage("用法：kp identity use <name> [role]", "切换本机以哪个身份/角色工作")
		}
		id, err := a.findIdentity(rest[0])
		if err != nil {
			return a.fail(err)
		}
		payload := map[string]any{}
		if len(rest) > 1 {
			payload["active_role"] = rest[1]
		}
		var raw map[string]any
		if err := a.cl.Patch("/api/v1/identities/"+id, payload, &raw); err != nil {
			return a.fail(err)
		}
		idn, _ := raw["identity"].(map[string]any)
		a.cfg.Identity = str(idn["name"])
		a.cfg.ActiveRole = str(idn["active_role"])
		if err := a.cfg.Save(); err != nil {
			fmt.Fprintln(os.Stderr, "✗ 写配置失败:", err)
			return ExitError
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		fmt.Printf("✓ 本机身份 → %s，激活角色 @%s\n", a.cfg.Identity, a.cfg.ActiveRole)
		return ExitOK

	case "rotate":
		if len(rest) < 1 {
			return a.usage("用法：kp identity rotate <name>", "旧 key 会立即失效")
		}
		id, err := a.findIdentity(rest[0])
		if err != nil {
			return a.fail(err)
		}
		var raw map[string]any
		if err := a.cl.Post("/api/v1/identities/"+id+"/rotate", map[string]any{}, &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		fmt.Printf("✓ 新 API key：%s\n", str(raw["api_key"]))
		fmt.Println("  旧 key 已失效。用它的会话需要重新 kp init。")
		if a.cfg.Identity == rest[0] {
			a.cfg.APIKey = str(raw["api_key"])
			if err := a.cfg.Save(); err == nil {
				fmt.Println("  （本机就是它，已自动更新本地配置）")
			}
		}
		return ExitOK

	case "invite":
		if len(rest) < 1 {
			return a.usage("用法：kp identity invite <name>", "重印发人用的接入说明（不含 key）")
		}
		var raw map[string]any
		if err := a.cl.Get("/api/v1/identities", &raw); err != nil {
			return a.fail(err)
		}
		for _, iv := range anySlice(raw["identities"]) {
			id, _ := iv.(map[string]any)
			if str(id["name"]) != rest[0] {
				continue
			}
			var hats []string
			for _, r := range anySlice(id["roles"]) {
				hats = append(hats, "@"+str(r))
			}
			fmt.Printf("  你被拉进了一个 Keypoint 协作中枢（任务 / 上报 / 交接）。\n\n")
			fmt.Printf("  用管理员给你的那条 kp init 命令接入，你的身份是 %s，角色 %s。\n\n", rest[0], strings.Join(hats, " "))
			fmt.Println("  接入后：")
			fmt.Println("    kp board                     我手上有什么")
			fmt.Println("    kp next --wait 30 --claim    等活（会阻塞；有活就返回完整开工包）")
			fmt.Println("    kp docs                      完整说明（也可以直接喂给模型）")
			return ExitOK
		}
		return a.fail(fmt.Errorf("没有身份 %q", rest[0]))

	case "disable", "enable":
		if len(rest) < 1 {
			return ExitUsage
		}
		id, err := a.findIdentity(rest[0])
		if err != nil {
			return a.fail(err)
		}
		var raw map[string]any
		if err := a.cl.Patch("/api/v1/identities/"+id, map[string]any{"disabled": sub == "disable"}, &raw); err != nil {
			return a.fail(err)
		}
		a.out(raw)
		if !a.jsonOut {
			fmt.Printf("✓ %s 已%s\n", rest[0], map[bool]string{true: "停用", false: "启用"}[sub == "disable"])
		}
		return ExitOK
	default:
		return a.usage("未知子命令 identity "+sub, "kp identity 看用法")
	}
}

// findIdentity resolves a name to its id, with a suggestion on a near miss.
func (a *app) findIdentity(name string) (string, error) {
	var raw map[string]any
	if err := a.cl.Get("/api/v1/identities", &raw); err != nil {
		return "", err
	}
	names := []string{}
	for _, iv := range anySlice(raw["identities"]) {
		id, _ := iv.(map[string]any)
		n := str(id["name"])
		if n == name {
			return str(id["id"]), nil
		}
		names = append(names, n)
	}
	msg := fmt.Sprintf("没有身份 %q", name)
	if sug := nearestString(name, names); sug != "" {
		msg += "（你是指 " + sug + "？）"
	}
	if len(names) > 0 {
		msg += "\n  已有： " + strings.Join(names, ", ")
	}
	return "", fmt.Errorf("%s", msg)
}

func nearestString(input string, candidates []string) string {
	best, bestDist := "", 1<<30
	for _, c := range candidates {
		d := editDistance(strings.ToLower(input), strings.ToLower(c))
		if d < bestDist {
			best, bestDist = c, d
		}
	}
	if bestDist > 2 {
		return ""
	}
	return best
}

func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = minInt(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

func minInt(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// ---------------------------------------------------------------------------
// kp hook
// ---------------------------------------------------------------------------

func (a *app) hook(args []string) int {
	if len(args) == 0 {
		fmt.Print(`kp hook — 出站 webhook

  kp hook ls                            列出订阅与事件词表
  kp hook add <url> [--secret S --events report.created,side.assigned]
  kp hook rm <id>

投递带 HMAC 签名头 X-KP-Signature: sha256=<hex>，密钥就是你配的 secret。
事件类型见 kp hook ls 的输出。可在网页 /admin 里配同样的东西。
`)
		return ExitOK
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "ls", "list":
		var raw map[string]any
		if err := a.cl.Get("/api/v1/webhooks", &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		hooks := anySlice(raw["webhooks"])
		if len(hooks) == 0 {
			fmt.Println("还没有 webhook。")
		} else {
			rows := [][]string{}
			for _, hv := range hooks {
				h, _ := hv.(map[string]any)
				events := []string{}
				for _, e := range anySlice(h["events"]) {
					events = append(events, str(e))
				}
				rows = append(rows, []string{
					str(h["id"]), str(h["url"]),
					orElseStr(strings.Join(events, ","), "全部"),
					str(h["last_status"]),
				})
			}
			fmt.Print(table([]string{"ID", "URL", "事件", "上次状态"}, rows))
		}
		fmt.Println("\n事件类型：")
		for _, e := range anySlice(raw["event_types"]) {
			fmt.Println("  " + str(e))
		}
		fmt.Println("\n签名：" + str(raw["signature"]))
		return ExitOK

	case "add", "set":
		fs := flag.NewFlagSet("kp hook add", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		secret := fs.String("secret", "", "HMAC 密钥")
		events := fs.String("events", "", "订阅事件，逗号分隔；留空=全部")
		disable := fs.Bool("disable", false, "先建但停用")
		if err := a.parseSub(fs, rest); err != nil {
			return ExitUsage
		}
		url := fs.Arg(0)
		if url == "" {
			return a.usage("用法：kp hook add <url> [--secret S --events a,b]", "")
		}
		var raw map[string]any
		if err := a.cl.Post("/api/v1/webhooks", map[string]any{
			"url": url, "secret": *secret, "events": splitCSV(*events), "enabled": !*disable,
		}, &raw); err != nil {
			return a.fail(err)
		}
		if a.jsonOut {
			a.out(raw)
			return ExitOK
		}
		h, _ := raw["webhook"].(map[string]any)
		fmt.Printf("✓ webhook %s → %s\n", str(h["id"]), url)
		return ExitOK

	case "rm", "del":
		if len(rest) < 1 {
			return ExitUsage
		}
		var raw map[string]any
		if err := a.cl.Delete("/api/v1/webhooks/"+rest[0], &raw); err != nil {
			return a.fail(err)
		}
		a.out(raw)
		if !a.jsonOut {
			fmt.Printf("✓ 已删除 %s\n", rest[0])
		}
		return ExitOK
	default:
		return a.usage("未知子命令 hook "+sub, "kp hook 看用法")
	}
}
