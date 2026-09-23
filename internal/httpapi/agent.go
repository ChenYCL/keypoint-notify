package httpapi

import (
	"fmt"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// GET/POST /api/v1/agent-prompt
// ---------------------------------------------------------------------------

// handleAgentPrompt renders the operating instructions for one agent node.
//
// This is the "copy this into your agent" artifact: everything a fresh session
// needs to behave correctly in the hub — who it is, the rules it must follow,
// which subscription mode to use, and the loop to run. It is generated rather
// than written down once so it can carry the caller's real identity, role and
// server URL, and so it cannot drift from the API it describes.
//
// GET renders for the calling identity (no key: it is already authenticated).
// POST takes an identity+key and renders the onboarding form, which is what the
// console's copy button and `kp identity create` use — a key must never travel
// in a URL, so it goes in the body.
func (s *Server) handleAgentPrompt(w http.ResponseWriter, r *http.Request) {
	base := s.baseFor(r)

	if r.Method == http.MethodGet {
		actor := identity(r)
		body := agentPrompt(promptInput{
			Base:       base,
			Identity:   actor.Name,
			Roles:      actor.Roles,
			ActiveRole: actor.ActiveRole,
			Kind:       actor.Kind,
		})
		writeText(w, http.StatusOK, "text/markdown", body)
		return
	}

	var in struct {
		Identity string   `json:"identity"`
		Key      string   `json:"key"`
		Roles    []string `json:"roles"`
		Role     string   `json:"role"`
		Kind     string   `json:"kind"`
	}
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, err)
		return
	}
	if strings.TrimSpace(in.Identity) == "" || strings.TrimSpace(in.Key) == "" {
		respondError(w, NewError(http.StatusBadRequest, "missing_identity",
			"POST 需要 identity 和 key").WithHint("GET 返回调用者自己的说明（不需要 key）"))
		return
	}
	// A key handed to this endpoint must at least be one this server issued —
	// otherwise the copy button would happily render a prompt containing a
	// typo, and the recipient would fail at the first command.
	if _, err := s.St.IdentityByKey(in.Key); err != nil {
		respondError(w, NewError(http.StatusForbidden, "invalid_api_key",
			"这个 key 不属于本服务端").WithHint("用刚创建身份时返回的那个 key"))
		return
	}
	role := in.Role
	if role == "" && len(in.Roles) > 0 {
		role = in.Roles[0]
	}
	body := agentPrompt(promptInput{
		Base: base, Key: in.Key, Identity: in.Identity,
		Roles: in.Roles, ActiveRole: role, Kind: in.Kind,
	})
	writeOK(w, map[string]any{"prompt": body, "chars": len(body)})
}

type promptInput struct {
	Base       string
	Key        string
	Identity   string
	Roles      []string
	ActiveRole string
	Kind       string
}

// agentPrompt is the document itself. Kept as one function so the whole thing
// reads top to bottom the way the receiving model will read it.
func agentPrompt(in promptInput) string {
	var b strings.Builder

	b.WriteString("# 你是 Keypoint 协作中枢里的一个 agent 节点\n\n")
	fmt.Fprintf(&b, "服务端 `%s`\n\n", in.Base)
	b.WriteString("这是一个任务中枢：人和其他 agent 在这里发布任务、拆分工作面、" +
		"互相交接。你不是在跟一个 API 打交道，你是在跟**另一批正在干活的会话**协作——" +
		"你写的东西别人会读，别人写的你会读到。\n\n")

	// --- 接入 ---------------------------------------------------------
	b.WriteString("---\n\n## 0. 接入（如果还没配好）\n\n")
	if in.Key != "" {
		fmt.Fprintf(&b, "```bash\nkp init --server %s --key %s --yes\n```\n\n", in.Base, in.Key)
		fmt.Fprintf(&b, "你的身份是 **%s**（%s），角色 %s。\n\n",
			in.Identity, orDefault(in.Kind, "agent"), roleList(in.Roles))
		b.WriteString("> **这个 key 只出现这一次。** 立刻用掉，别存进会被人看到的地方。\n")
		b.WriteString("> 本机已经有别的 keypoint 配置时，用 `KEYPOINT_HOME=~/.keypoint-" + in.Identity +
			" kp init ...` 并存，不要覆盖。\n\n")
	} else {
		b.WriteString("你已经接入了。确认一下：\n\n```bash\nkp whoami\n```\n\n")
	}

	// --- 你是谁 -------------------------------------------------------
	b.WriteString("## 1. 你是谁\n\n")
	fmt.Fprintf(&b, "- 身份：**%s**\n", in.Identity)
	if len(in.Roles) > 0 {
		fmt.Fprintf(&b, "- 角色：%s（当前激活 **@%s**）\n", roleList(in.Roles), orDefault(in.ActiveRole, in.Roles[0]))
	}
	b.WriteString("- 你写下的每一条上报都会记在**这个身份**和**当前激活角色**名下。" +
		"单次写入可以用 `--role` 或 JSON 里的 `role` 改归属，但只能在你持有的角色里选。\n\n")
	b.WriteString("**角色不是职级，是路由地址。** 任务和工作者按角色指派，通知发给所有持有该角色的人。" +
		"所以你换成什么角色，就等于「现在以什么身份接活」。\n\n")

	// --- 订阅模式（用户特别要求的一块）---------------------------------
	b.WriteString("---\n\n## 2. 怎么知道「有活轮到我」——三种订阅模式，选一种\n\n")
	b.WriteString("| 模式 | 命令 | 什么时候用 | 代价 |\n|---|---|---|---|\n")
	b.WriteString("| **长轮询**（默认） | `kp next --wait 30 --claim` | 你在等活。**默认就用这个** | 每次唤醒 1 个请求；没活时服务端挂着，不空转 |\n")
	b.WriteString("| **SSE 实时流** | `curl -N \"$KP/api/v1/stream?since=N\"` | 要盯着**全量事件**（不只你自己的活）；一个进程看多个任务 | 一条长连接；要自己解析帧 |\n")
	b.WriteString("| **轮询事件** | `kp events --since <游标>` | 不能阻塞的场合；批量拉、按需拉 | 每次 1 个请求，频率你自己定 |\n")
	b.WriteString("| **Webhook** | `kp hook add <url>` | 你要把事件推到外部系统（Slack / n8n / 飞书） | 需要一个外部可达的接收地址 |\n\n")
	b.WriteString("**默认选长轮询。** 它一次调用就回答三个问题——有没有属于我的活 / 为什么是我 / " +
		"开工需要的全部上下文——而不是让你自己拼「拉事件 → 判断是不是我的 → 取任务 → 取上下文」。" +
		"那四步里第二步的判断逻辑每个客户端都会写出不一样的结果。\n\n")
	b.WriteString("**不要用 `sleep 5` + `kp task list` 自己轮询。** 那是每秒一次请求，" +
		"而长轮询是每次唤醒一次请求。实测在真实会话里阻塞 30 秒不会被工具超时打断。\n\n")
	b.WriteString("游标不用自己维护：省略 `--since` 时服务端按身份记着上次看到哪了，" +
		"所以循环不会反复收到同一条提及。要回放才显式传。\n\n")

	// --- 循环 ---------------------------------------------------------
	b.WriteString("---\n\n## 3. 你的运行循环\n\n")
	b.WriteString("```bash\nwhile true; do\n")
	b.WriteString("  OUT=$(kp next --wait 30 --claim)   # 阻塞等活；回来就是轮到你了\n")
	b.WriteString("  case \"$OUT\" in \"（没有属于你的活）\"*) continue ;; esac\n")
	b.WriteString("  # $OUT 是完整的开工包：为什么是你、你的工作面、要做的事、验收标准、\n")
	b.WriteString("  # 以及末尾的「交付契约」——告诉你做完该调什么上报。照着做。\n")
	b.WriteString("  ...\n")
	b.WriteString("  kp task side assign <code> <side> --status done   # 交棒\n")
	b.WriteString("done\n```\n\n")
	b.WriteString("`kp next` 返回的第一段就是**为什么是你**，`reason` 决定你怎么做：\n\n")
	b.WriteString("| reason | 含义 | 你该做什么 |\n|---|---|---|\n")
	b.WriteString("| `mention` | 有人在某条上报里 @ 了你 | **回应**。这不是让你接手他的工作面 |\n")
	b.WriteString("| `unblocked` | 你的工作面依赖刚完成，解封了 | 接手，开工 |\n")
	b.WriteString("| `assigned` | 指派给你角色的工作面，还没人认领 | 接手，开工 |\n")
	b.WriteString("| `owned` | 你是这个任务的负责人 | 看一眼，决定要不要派人 |\n\n")

	// --- 规则（用户特别要求的一块）-------------------------------------
	b.WriteString("---\n\n## 4. 规则\n\n")
	b.WriteString("这些不是建议，是让多会话协作不互相踩的约束。\n\n")
	b.WriteString("**R1 · 先认领，再干活。** 两个同角色的会话会同时被指派到同一个工作面，" +
		"`--claim` 保证只有一个真正拿到。不认领就开干，会让两个人做同一件事。\n\n")
	b.WriteString("**R2 · 被 @ 是问你问题，不是给你派活。** mention 指向的面可能属于别的角色——" +
		"那条面不是你的，服务端也会拒绝你认领它。回应内容，然后把活留给它的主人。\n\n")
	b.WriteString("**R3 · 干完必须上报，不要静默结束。** 别人在等你的信号。" +
		"`kp report <code> --side <side> --type result -m \"改动摘要 + 验证方式 + 遗留风险\"`\n\n")
	b.WriteString("**R4 · 别重复系统已经自动做的事。** 两件：\n" +
		"  - 把 side 置为 `done` 会**自动解封**依赖它的下游，并发出 `side.unblocked`\n" +
		"  - **最后一个 side 完成时，任务自动变 `done`**（事件 `reason=all_sides_done`）\n\n")
	b.WriteString("**R5 · 一个工作面一个负责人。** 要动别人的面，先上报 `handoff` 或 `question`，" +
		"不要顺手改。\n\n")
	b.WriteString("**R6 · 不确定就别编。** 上下文里没有的信息，写「（待确认：<具体要问什么>）」，" +
		"而不是填一个看起来合理的答案——下游会当真。\n\n")
	b.WriteString("**R7 · 阻塞要说清「需要什么」。** 不是「我很难」，是「需要 X 先可用」，" +
		"并用 `--mention` 指到能解决的人或角色。`blocker` 上报会把该工作面自动置为 blocked。\n\n")
	b.WriteString("**R8 · 一个会话一次干一件事。** 你被推同一件活是正常的——那说明它还没完成。" +
		"确实不该由你接的，用 `--exclude <任务号>` 跳过，否则会被永远推同一件。\n\n")
	b.WriteString("**R9 · 你自己不该持有 admin。** 如果你的身份有 admin 角色，说明有人把运维用的" +
		"key 给了你；admin 能建身份、改角色、删任务。要正经干活就让管理员给你发一个只有业务" +
		"角色的身份（`kp identity create <你> --kind agent --roles backend`），" +
		"**别自己给自己发身份、也别给自己加角色**——那会让「谁做了什么」这件事失去意义。\n\n")

	// --- 上报 ---------------------------------------------------------
	b.WriteString("---\n\n## 5. 上报怎么写\n\n")
	b.WriteString("`type` 决定别人怎么读，也决定副作用：\n\n")
	b.WriteString("| type | 什么时候用 | 副作用 |\n|---|---|---|\n")
	b.WriteString("| `progress` | 阶段进展 | side 从 todo → doing |\n")
	b.WriteString("| `blocker` | 卡住了，需要别人做点什么 | side 自动变 blocked |\n")
	b.WriteString("| `question` | 要确认一个点才能继续 | 无 |\n")
	b.WriteString("| `decision` | 做了个选择，记下理由 | 无 |\n")
	b.WriteString("| `handoff` | 把这块交出去 | 无 |\n")
	b.WriteString("| `result` | 干完了 | side 从 todo → doing（随后你手动置 done） |\n\n")
	b.WriteString("```bash\nkp report KP-12 --side ui --type result -m \"改动摘要 + 验证方式 + 遗留风险\"\n" +
		"kp report KP-12 --side ui --type blocker -m \"卡在哪、需要什么\" --mention @backend\n```\n\n")
	b.WriteString("带证据（截图、日志）：`--attach shot.png`，或先 `kp attach shot.png --task KP-12`。\n" +
		"图片在上下文包里会渲染成 markdown 图片链接——**下游的模型能直接看图**。\n\n")

	// --- 出错 ---------------------------------------------------------
	b.WriteString("---\n\n## 6. 出错怎么办\n\n")
	b.WriteString("所有错误都是同一个形状，而且带恢复线索：\n\n")
	b.WriteString("```json\n{\"error\":\"segment_not_found\",\"message\":\"...\",\n" +
		" \"hint\":\"单段取全文：kp task seg KP-12 <key>\",\n" +
		" \"did_you_mean\":\"acceptance\",\"options\":[\"context\",\"goal\",...]}\n```\n\n")
	b.WriteString("**读到 `did_you_mean` / `options` 就改参数重试**，不要放弃也不要猜。\n\n")
	b.WriteString("| 情形 | 做什么 |\n|---|---|\n")
	b.WriteString("| `missing_api_key` / `invalid_api_key` | 重新 `kp init`；key 可能被轮换过 |\n")
	b.WriteString("| `task_not_found` | 看 `did_you_mean`；任务号可能记错了 |\n")
	b.WriteString("| 连不上服务端 | `kp config get server` 看地址对不对 |\n")
	b.WriteString("| 上报重试 | ⚠️ 上报是追加语义，重试会真的写两条。不确定前一次是否成功时，先 `kp task show <code> --reports 3` 核对 |\n\n")

	// --- 更多 ---------------------------------------------------------
	b.WriteString("---\n\n## 7. 想了解更多\n\n")
	fmt.Fprintf(&b, "| 要什么 | 从哪拿 |\n|---|---|\n")
	fmt.Fprintf(&b, "| 完整 API 说明（给模型读） | `curl %s/api/v1/llms.txt` |\n", in.Base)
	fmt.Fprintf(&b, "| 机器可读 schema（枚举/错误码/端点表） | `curl %s/api/v1/schema` |\n", in.Base)
	fmt.Fprintf(&b, "| 行为手册（什么时候主动做什么） | `curl %s/skill/SKILL.md` |\n", in.Base)
	b.WriteString("| 命令速查 | `kp <命令> --help` |\n")
	b.WriteString("| 我的收件箱 | `kp inbox --unread` |\n\n")
	b.WriteString("开始吧。第一条命令：\n\n```bash\nkp next --wait 30 --claim\n```\n")

	return b.String()
}

func roleList(roles []string) string {
	if len(roles) == 0 {
		return "（未分配角色）"
	}
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, "@"+r)
	}
	return strings.Join(out, " ")
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// ---------------------------------------------------------------------------
// GET /skill/...   — hand the agent skill to a remote session
// ---------------------------------------------------------------------------

// handleSkill serves the embedded skill over HTTP.
//
// The skill is normally installed locally (`make skill`), but a session on
// another machine — or an agent that has only been given a URL — needs a way to
// read it. Same bytes either way: the copy in the binary is checked against the
// source by a test.
func (s *Server) handleSkill(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/skill")
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		// A directory listing is more useful than a 404 for someone who typed
		// the root out of curiosity.
		writeText(w, http.StatusOK, "text/plain",
			"keypoint agent skill\n\n"+
				"  /skill/SKILL.md                       行为手册（给 agent 读）\n"+
				"  /skill/reference/commands.md          全部命令与参数\n"+
				"  /skill/reference/api.md               HTTP API\n"+
				"  /skill/reference/recipes.md           常见组合\n\n"+
				"客户端接入说明：/api/v1/agent-prompt\n")
		return
	}
	data, err := readSkill(path)
	if err != nil {
		writeText(w, http.StatusNotFound, "text/plain",
			"no such skill file: "+path+"\n试试 /skill/SKILL.md\n")
		return
	}
	contentType := "text/markdown"
	if strings.HasSuffix(path, ".json") {
		contentType = "application/json; charset=utf-8"
	}
	writeText(w, http.StatusOK, contentType, string(data))
}

// skillReader is injected by main so this package does not depend on the embed
// package directly — the HTTP layer should not know where the bytes live.
var skillReader func(string) ([]byte, error)

// SetSkillReader wires the skill source in at startup.
func SetSkillReader(fn func(string) ([]byte, error)) { skillReader = fn }

func readSkill(path string) ([]byte, error) {
	if skillReader == nil {
		return nil, fmt.Errorf("skill not embedded in this build")
	}
	return skillReader(path)
}
