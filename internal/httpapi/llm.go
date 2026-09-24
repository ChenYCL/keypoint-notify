package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/ChenYCL/keypoint-notify/internal/model"
	"github.com/ChenYCL/keypoint-notify/internal/pack"
	"github.com/ChenYCL/keypoint-notify/internal/store"
)

// ---------------------------------------------------------------------------
// GET /api/v1/whoami
// ---------------------------------------------------------------------------

// handleWhoami is the handshake every agent should perform first: it answers
// "who am I, what roles do I hold, and what should I do next" in one call, and
// its `capabilities` list is what tells a model which endpoints are worth
// reaching for.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	actor := identity(r)
	unread, err := s.St.UnreadCount(actor.ID)
	if err != nil {
		respondError(w, err)
		return
	}
	sides, err := s.St.SidesForRole(actor.ActiveRole, "")
	if err != nil {
		respondError(w, err)
		return
	}
	writeOK(w, map[string]any{
		"identity":     actor,
		"role":         actor.ActiveRole,
		"unread":       unread,
		"open_sides":   len(sides),
		"capabilities": whoamiCapabilities(actor),
		"entry_points": []string{
			"GET /api/v1/me/board            我手上有什么",
			"GET /api/v1/inbox?unread=1      未读上报",
			"GET /api/v1/tasks?assigned=me   指派给我的任务",
			"GET /api/v1/tasks/{code}/pack   开工上下文包（可直接喂给模型）",
			"POST /api/v1/tasks/{code}/reports  上报进展/阻塞/结果",
			"GET /api/v1/llms.txt            完整 API 说明",
		},
		"note": "写操作会以你的 active_role 归属；要在某次写入里用别的角色，传 \"role\": \"<key>\"",
	})
}

func whoamiCapabilities(actor model.Identity) []string {
	caps := []string{"task:read", "task:write", "report:create", "file:upload"}
	if actor.HasRole("admin") {
		caps = append(caps, "identity:admin", "role:admin", "webhook:admin", "task:delete")
	}
	return caps
}

// ---------------------------------------------------------------------------
// GET /api/v1/tasks/{code}/pack
// ---------------------------------------------------------------------------

// handleTaskPack is the endpoint the whole design exists to make possible: one
// call returns a paste-ready context bundle covering the task, the requested
// work face, the recent timeline, and the contract for reporting back.
//
//	A coding agent's entire bootstrap is:
//	    curl -H "Authorization: Bearer $KP_KEY" "$KP/pack?..."
func (s *Server) handleTaskPack(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := r.PathValue("code")
	t, err := s.St.GetTask(code)
	if err != nil {
		respondError(w, s.taskNotFound(code, err))
		return
	}

	opt := pack.Options{
		SideKey:      strings.TrimSpace(q.Get("side")),
		MaxChars:     atoiOr(q.Get("max_chars"), pack.DefaultMaxChars),
		Reports:      atoiOr(q.Get("reports"), 5),
		IncludeEmpty: q.Get("empty") == "1" || q.Get("empty") == "true",
	}
	if q.Get("reports") == "0" || q.Get("reports") == "none" {
		opt.Reports = 0
	}
	if opt.SideKey != "" {
		if _, err := s.St.Side(t.ID, opt.SideKey); err != nil {
			respondError(w, s.sideNotFound(t, opt.SideKey))
			return
		}
	}

	var reports []model.Report
	if opt.Reports > 0 {
		reports, err = s.St.ListReports(store.ReportFilter{TaskCode: t.Code, Limit: opt.Reports})
		if err != nil {
			respondError(w, err)
			return
		}
	}

	actor := identity(r)
	role := actor.ActiveRole
	if opt.SideKey != "" {
		if sd, err := s.St.Side(t.ID, opt.SideKey); err == nil && sd.AssigneeRole != "" {
			role = sd.AssigneeRole
		}
	}
	b := pack.Build(t, reports, role, opt)

	if q.Get("format") == "json" {
		writeOK(w, b)
		return
	}
	// Markdown is the default because its consumer is a prompt, and a prompt
	// wants prose with headings rather than a JSON envelope to unwrap.
	writeText(w, http.StatusOK, "text/markdown", pack.Markdown(b, opt))
}

// ---------------------------------------------------------------------------
// GET /api/v1/llms.txt
// ---------------------------------------------------------------------------

// handleLLMsTxt serves a plain-text API manual, in the emerging /llms.txt
// convention. It is deliberately static prose rather than generated JSON: a
// model reads it once at bootstrap and then knows which endpoints to hit,
// which enums exist, and how errors are shaped — without reading source.
func (s *Server) handleLLMsTxt(w http.ResponseWriter, r *http.Request) {
	writeText(w, http.StatusOK, "text/plain", llmsTxt(s.baseFor(r)))
}

func (s *Server) baseFor(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "127.0.0.1:8787"
	}
	return scheme + "://" + host
}

func llmsTxt(base string) string {
	return fmt.Sprintf(`# Keypoint Notify — API 说明（给 LLM / agent 读的版本）

base_url: %s
auth:     Authorization: Bearer kp_...    （或 X-API-Key: kp_...）
docs:     本文档是机器可读的权威说明；/api/v1/schema 是同一份内容的 JSON 形式。

## 先拿这几样（全部免鉴权，除非注明）

  /api/v1/llms.txt        ← 你正在读的这份：API 说明
  /api/v1/schema          同一份内容的 JSON 形式（枚举 / 错误码 / 端点表）
  /skill/SKILL.md         行为手册：什么时候该主动做什么、怎么从会话里抽任务
  /skill/reference/*.md   命令速查 / HTTP API / 常见配方
  /skill/index.json       全部 skill（行为手册 + /kp-xxx 斜杠命令）及文件清单
  /api/v1/agent-prompt    运行说明（需带 key）：你是谁 + 规则 + 订阅模式 + 运行循环
                          这是「粘给一个 agent 就能让它上手」的那一份，
                          内容按调用者的真实身份和角色生成。

想最快让一个 agent 动起来：把 /api/v1/agent-prompt 的输出整个喂给它。

## 这个系统是干什么的

一个任务中枢。人用浏览器看板；Claude Code 之类的 agent 通过 HTTP API
上报进展、拉取任务上下文、认领工作面。核心概念：

  Task     任务，短代号 KP-<n>，有 status / priority / kind / owner_role
  Segment  分段，任务的细节切片，每段有稳定 key，可单独取用（这是"可复制"的单位）
  Side     工作面，一个任务可并行拆成几块（api / ui / review ...），
           每块有自己的负责人角色、状态、依赖和分段
  Report   上报，只增不改的时间线条目：progress | blocker | decision | handoff | result | question
  Identity 身份，一个 API key 对应一个身份；身份持有若干 role，可改绑、可轮换 key
  Event    事件，一切变更都产生事件；驱动收件箱、webhook、SSE

## 最重要的一条（协作循环）：先问 me/next

一个会话在循环里干活时，不要自己拼「拉事件 → 判断是不是我的 → 取任务 → 取 pack」。
一条调用就够了：

  GET /api/v1/me/next?wait=30&claim=1&task=KP-12&side=ui&max_chars=12000

它回答三件事：**有没有属于我的活 / 为什么是我 / 开工需要的全部上下文**。

参数：
  wait=<秒>     没有活时在服务端挂起多久（长轮询，上限 60）。0 = 立刻返回
                wait>0 时只为**新的东西**醒来：已经在你名下、之后没变化的工作面，
                和你负责、之后没动静的任务，都不会让它立刻返回 —— 所以提完问题
                等回答时，它真的会挂着等，而不是把你自己的面反复还给你。
                新会话想看手上已认领的：wait=0，或 GET /api/v1/me/board
  claim=1       拿到属于我的工作面就原子认领 —— 同角色的多个会话只有一个能抢到
  since=<游标>  显式指定从哪之后看。**省略时用你上次调用存下的游标**，
                所以循环里不需要自己带着游标跑；不带 since 也不会反复收到同一条提及
  peek=1        只看不动：不推进你存下的游标。替会话「盯着」的程序（kp wait）用它，
                这样把会话叫醒的那条提及，会话自己再问时还在
  task= side=   收窄范围
  exclude=KP-1, KP-2   跳过这些任务。一个会话判定某件活不该由它接时用它，
                       否则会被永远推同一件（这是唯一的逃逸阀）
  max_chars=    pack 体积上限
  format=md|json

reason 的取值决定了你该怎么做：

  mention    有人在某个上报里 @ 了我，在等我回应。**不是让我接手他的工作面**
  unblocked  我的工作面依赖刚完成，解封了（它有 deps，且都已 done）
  assigned   指派给我角色的工作面（没有依赖），还没人认领
  owned      我是任务负责人，任务有新动静

被 blocker 上报置为 blocked 的工作面不会被当成活派出去 —— 它在等的东西
依赖图不知道。等提问方被回答（mention）后自己改回 todo/doing，或它的依赖完成。

响应是 markdown（默认）或 JSON，开头有一段「为什么是你」，然后是完整开工包
（含交付契约）。JSON 形状：{work:{reason,explanation,task,side,dependents}, cursor, claimed, pack}

**协作的闭环**：交棒不需要谁去通知谁 —— 把一个 side 置为 done，系统会自己
找到依赖它的下游 side，解封并产生 side.unblocked 事件，下游会话的长轮询
立刻被唤醒。下游看到的是「为什么被叫醒」+ 完整上下文，不是一条干巴巴的通知。

**两件会自动发生的事**（别重复做，也别指望靠轮询去发现）：

  1. side 完成后，依赖它的下游 side 自动解封，并发出 side.unblocked
  2. **最后一个 side 完成时，任务自动变成 done**，并写一条
     task.status_changed，payload.reason = "all_sides_done"

第 2 条容易被忽略：面全做完之后，它们不再匹配任何"找未完成工作"的查询，
任务会永远挂在 open 状态。所以最后一面落地的同一事务里就收尾了。
没有工作面的任务不会被自动关闭 —— 那是"从来没拆过"，不是"做完了"。

单个工作面也可以直接认领：

  POST /api/v1/tasks/{code}/sides/{key}/claim

放手（还给角色，别人能接）：PATCH /api/v1/tasks/{code}/sides/{key}
  {"assignee_identity": "", "status": "todo"} —— 只清认领人，角色保留。
  别用 unassign:true，那会连角色一起清掉，面就没人会收到了。

订阅一个任务的动态（进你的收件箱）：
  POST /api/v1/tasks/{code}/watch     DELETE 同一路径退订

**只认领属于你的工作面。** 被 mention 不等于接管别人的面 —— 那是在问你问题，
不是把活给你。服务端也会拒绝：认领只对「指派给你的角色 / 指派给你的身份」
生效，别的面返回 claimed=false。

## agent 循环长什么样（照抄即可）

（shell，每轮一次请求，不是每秒一次：没有活时服务端挂着，有活立刻返回）

    while true; do
      OUT=$(kp next --wait 30 --claim)          # 阻塞等；回来就是轮到你了
      case "$OUT" in
        "（没有属于你的活）"*) continue ;;       # 超时无活，下一轮
      esac
      # $OUT 是完整的开工包，里面已经说明了：为什么是你、你的工作面、
      # 要做的事、以及做完该调什么上报。直接交给模型。
      ...
      kp done <code> <side> -m "改了什么 / 怎么验证 / 遗留风险"   # 上报 + 置完成 = 交棒
    done

不想自己写循环：kp loop --agent claude（或 --agent kimi）每件活起一个新会话，
做完自动 kp done。

CLI 的工作流短命令（每条都是一整步）：
  kp next --claim            接一件活
  kp report KP-12 -m "…"     中途上报（--type blocker|question|decision）
  kp done KP-12 ui -m "…"    完结：result 上报 + 面置 done
  kp release KP-12 ui        放手：还给角色
  kp cancel KP-12 -m "…"     取消：记原因并归档
  kp wait                    有新活/新通知才退出，第一行 KP-WAIT: work|notification|timeout
  kp watch KP-12             订阅一个任务

装了斜杠命令（kp install）的话，Claude Code 里是 /kp-next /kp-done /kp-report
/kp-new /kp-cancel /kp-loop /kp-watch，Kimi Code 里是 /skill:kp-next 这样。
清单：GET /skill 或 /skill/index.json

不需要自己维护游标：省略 since 时服务端按身份记着上次看到哪了，
所以循环不会反复收到同一条提及。要回放才显式传 since。

实测：这条命令在真实的 Claude Code 会话里阻塞 25 秒不会被工具超时打断，
所以 wait 给 30 是安全的。

## 另一条路：自己拿 pack

要做任何一个任务，第一步永远是：

  GET /api/v1/tasks/{code}/pack?side={side_key}&format=md

返回一份自包含的 markdown：任务头、分段索引、工作面表、每段正文、最近上报、
附件链接，以及**交付契约**（完成后该调哪个接口上报）。把这段文本直接作为
prompt 上下文交给模型即可开工。

参数：
  side=<key>        聚焦某个工作面（省略则给全任务）
  max_chars=<n>     上限，默认 12000；超限会截断并在文末说明遗漏了哪些段
  reports=<n>       附带最近 n 条上报，默认 5；0 = 不带
  format=md|json    md（默认）给模型看；json 给程序用
  empty=1           带上还没写内容的固定分段（默认省略）

## 恢复：没有可用的 admin 时

最后一个 admin 的 key 丢了（配置被覆盖、误删、机器重装），系统里所有管理
操作都会 403，而 API 里没有任何途径授予 admin。这时：

    POST /api/v1/bootstrap  {"name":"<你的名字>","kind":"human"}

会重新开放一次 —— 系统有身份但**一个启用的 admin 都没有**时，它新建一个
拿 admin 的身份，响应里带 recovery: true。GET /api/v1/health 的 admins
字段可以提前发现这个状态。

这不是后门：能调这个端点就等于能访问那台机器，和能直接改数据文件是同一个
信任级别。公网部署请务必配合 Cloudflare Access 之类的入口鉴权。

## 固定分段 key（骨架）

  context      背景
  goal         目标
  deliverable  交付物
  constraint   约束
  acceptance   验收标准
  interface    接口/契约
  files        相关文件

这些 key 在任务创建时就存在（可能为空），所以永远可以用 key 取，不必先列。
自由分段自己起 key（中文会保留，空格转成 -）。取单段：

  GET /api/v1/tasks/{code}/segments/{key}            → 纯文本
  GET /api/v1/tasks/{code}/segments/{key}?format=prompt  → 带上下文的可粘贴片段

## 读写端点

读（全部支持 JSON，默认过滤为空即列出）：
  GET  /api/v1/whoami                          我是谁、我有什么角色、有多少未读
  GET  /api/v1/me/next?wait=30&claim=1          ★ 轮到我干的活（长轮询 + 完整上下文）
  GET  /api/v1/me/board                        我手上的工作面+我负责的任务
  GET  /api/v1/inbox?unread=1                  我的未读上报
  GET  /api/v1/tasks?assigned=me               指派给我的
  GET  /api/v1/tasks?role=backend&status=doing,blocked&since=7d&q=验证码&limit=20
  GET  /api/v1/tasks/{code}                    单任务全量（含分段、工作面、附件）
  GET  /api/v1/tasks/{code}/pack               开工上下文包 ★
  GET  /api/v1/tasks/{code}/segments           全部分段
  GET  /api/v1/tasks/{code}/segments/{key}     单段正文
  GET  /api/v1/tasks/{code}/sides              工作面列表
  GET  /api/v1/tasks/{code}/reports?limit=20   时间线
  GET  /api/v1/events?since=<cursor>           事件流（轮询）
  GET  /api/v1/stream?since=<cursor>           SSE
  GET  /api/v1/roles?keys=1                    合法角色 key 列表
  GET  /api/v1/roles?holders=1                 角色 → 谁持有
  GET  /api/v1/identities?names=1              可指派身份名列表
  GET  /api/v1/files/{id}                      附件（图片 inline 预览）

写：
  POST   /api/v1/tasks                         建任务（可一次带 segments + sides）
  PATCH  /api/v1/tasks/{code}                  改元数据
  DELETE /api/v1/tasks/{code}                  删任务（需 admin）
  POST   /api/v1/tasks/{code}/segments         写/追加分段 {"key":"goal","body":"..."}
  DELETE /api/v1/tasks/{code}/segments/{key}
  POST   /api/v1/tasks/{code}/sides            加工作面 {"key":"ui","assignee_role":"frontend","deps":["api"]}
  PATCH  /api/v1/tasks/{code}/sides/{key}      改工作面（指派/状态/依赖）
  DELETE /api/v1/tasks/{code}/sides/{key}
  POST   /api/v1/tasks/{code}/reports          上报 ★
  POST   /api/v1/files                         multipart 上传，字段名 file
  POST   /api/v1/tasks/{code}/files            直接挂到任务上
  POST   /api/v1/inbox/read                    {"ids":[...]}，空数组=全部已读

## 建任务怎么调

  POST /api/v1/tasks          （CLI：kp task new --from-json -，同一个形状）
  {
    "title": "登录页验证码倒计时切后台后错位",      // 必填
    "kind": "bug",                 // bug|feature|chore|research|review|incident
    "priority": "P1",              // P0..P3
    "summary": "一句话",
    "owner_role": "pm",            // 负责角色；owner_identity 指定到人
    "labels": ["auth"],
    "links": [{"kind": "repo", "url": "https://..."}],
    "segments": {                  // ★ 分段都放这里，不是顶层字段
      "context": "…", "goal": "…", "deliverable": "…", "constraint": "…",
      "acceptance": "…", "interface": "…", "files": "…",
      "踩坑记录": "自由分段，中文 key 保留"
    },
    "sides": [                     // 工作面：各自指派、各自有依赖
      {"key": "api", "title": "后端接口", "assignee_role": "backend",
       "segments": {"做法提示": "…"}},
      {"key": "ui", "title": "前端", "assignee_role": "frontend", "deps": ["api"]}
    ],
    "watchers": ["alice"],
    "notify": ["@review"]
  }

严格模式：拼错的字段名会报 invalid_json，响应里 field 是那个字段、
did_you_mean 是它该在的位置（比如 "acceptance" → segments.acceptance），
options 是顶层可用的字段。

## 上报怎么调

  POST /api/v1/tasks/KP-12/reports
  {
    "type": "blocker",            // progress|blocker|decision|handoff|result|question
    "side_key": "ui",             // 可选：归属于某个工作面
    "body": "iOS 上 visibilitychange 不触发，需要改成 pagehide",
    "segments": [                 // 可选：结构化片段
      {"key": "repro", "title": "复现步骤", "body": "1. ...\n2. ..."}
    ],
    "mentions": ["@backend", "review"],   // 身份名或角色 key，都会被通知
    "attachments": ["fil_xxx"],           // 先 POST /api/v1/files 拿到的 id
    "status": "blocked"                   // 可选：同时把任务状态也改掉
  }

副作用（可预期，不要重复做）：
  - type=blocker 且带 side_key  → 该工作面状态自动变 blocked
  - type=progress/result 且该面还是 todo → 自动变 doing
  - mentions 里的人会收到站内未读；任务 watcher 和该面负责人也会收到

## 枚举

kind:      bug | feature | chore | research | review | incident
status:    inbox | ready | doing | blocked | review | done | archived
side 状态:  todo | doing | blocked | done
priority:  P0 | P1 | P2 | P3
report:    progress | blocker | decision | handoff | result | question
event:     %s

## 错误怎么长

所有错误都是同一个形状，并且**带恢复线索**：

  {
    "error": "segment_not_found",
    "message": "任务 KP-12 没有分段 \"accepance\"",
    "hint": "单段取全文：kp task seg KP-12 <key>；列全部：kp task show KP-12",
    "did_you_mean": "acceptance",
    "options": ["context","goal","deliverable","acceptance","interface","files"],
    "docs": "/api/v1/llms.txt"
  }

常见 error 值：missing_api_key / invalid_api_key / task_not_found /
segment_not_found / side_not_found / file_not_found / near_miss_skeleton_key /
bad_status / bad_report_type / bad_priority / bad_time / conflict / forbidden /
file_too_large / invalid_json

读到 did_you_mean / options 就照着改参数重试，不要放弃也不要猜。

## 时间参数

since / updated_since 接受：RFC3339、日期（2026-09-01）、相对量（7d / 24h / 90m / 30s）。

## 幂等与重试

写操作都可以安全重试：URL 里的 {code} 是稳定的，重复创建同名任务会返回
conflict 而不是建出两条。上报是追加语义，重试会真的写两条——不确定前一次
是否成功时，先 GET /api/v1/tasks/{code}/reports?limit=3 看一眼再决定。

## 建议的 agent 循环

1. GET /whoami                      → 确认身份、角色、未读数
2. GET /me/board                    → 拿到手上的工作面
3. GET /tasks/{code}/pack?side=X    → 拿上下文包，开工
4. （干活）
5. POST /tasks/{code}/reports       → 上报结果 / 阻塞 / 提问
6. GET /events?since=<cursor>       → 保持轮询，拿别人的回应

## 网页界面

  %s/            看板（任务优先）
  %s/t/KP-12     任务详情；每个分段卡片右上有独立复制按钮
  %s/inbox       收件箱
  %s/admin       身份 / 角色 / webhook 管理
`,
		base, strings.Join(store.AllEventTypes, " | "),
		base, base, base, base)
}

// ---------------------------------------------------------------------------
// GET /api/v1/schema
// ---------------------------------------------------------------------------

// handleSchema returns the same contract as llms.txt in JSON, for callers that
// would rather parse than read prose. It is generated from the same constants
// the handlers validate against, so it cannot drift.
func (s *Server) handleSchema(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]any{
		"base_url": s.baseFor(r),
		"auth": map[string]any{
			"headers": []string{"Authorization: Bearer kp_...", "X-API-Key: kp_..."},
			"how_to_get_a_key": []string{
				"空系统：POST /api/v1/bootstrap {\"name\":\"...\"}",
				"已有系统：由 admin POST /api/v1/identities，或用 `kp init`",
			},
		},
		"concepts": map[string]string{
			"task":     "任务，短代号 KP-<n>",
			"segment":  "任务的细节切片，稳定 key，可单独取用（可复制的单位）",
			"side":     "工作面，一个任务并行拆成若干块，各带负责人/状态/依赖/分段",
			"report":   "上报，只增时间线",
			"identity": "API key 对应的身份，持有若干 role",
			"event":    "变更事件，驱动收件箱/webhook/SSE",
		},
		"enums": map[string][]string{
			"kind":                  taskKindOptions(),
			"task_status":           {model.StatusInbox, model.StatusReady, model.StatusDoing, model.StatusBlocked, model.StatusReview, model.StatusDone, model.StatusArchived},
			"side_status":           {model.SideTodo, model.SideDoing, model.SideBlocked, model.SideDone},
			"priority":              model.Priorities,
			"report_type":           reportTypeOptions(),
			"event_type":            store.AllEventTypes,
			"skeleton_segment_keys": model.SkeletonKeys,
		},
		"skeleton_titles": model.SkeletonTitles,
		"error_shape": map[string]any{
			"fields": []string{"error", "message", "hint", "did_you_mean", "options", "field", "docs"},
			"note":   "error 是稳定机器码；did_you_mean/options 用于自我纠正后重试",
			"codes": []string{"missing_api_key", "invalid_api_key", "task_not_found",
				"segment_not_found", "side_not_found", "file_not_found",
				"near_miss_skeleton_key", "bad_status", "bad_report_type", "bad_priority",
				"bad_time", "conflict", "forbidden", "file_too_large", "invalid_json",
				"already_bootstrapped", "missing_api_key"},
		},
		"time_params": map[string]any{
			"accepted": []string{"RFC3339", "YYYY-MM-DD", "相对量 7d/24h/90m/30s"},
			"used_by":  []string{"since", "updated_since"},
		},
		"endpoints": []map[string]any{
			{"method": "GET", "path": "/api/v1/whoami", "desc": "身份、角色、未读数、入口提示"},
			{"method": "GET", "path": "/api/v1/me/board", "desc": "我手上的工作面 / 我负责的任务"},
			{"method": "GET", "path": "/api/v1/me/next", "params": []string{"wait", "claim=1", "since", "task", "side", "max_chars", "format=md|json"}, "desc": "★ 轮到我干的活：为什么是我 + 完整开工包（长轮询）"},
			{"method": "POST", "path": "/api/v1/tasks/{code}/sides/{key}/claim", "desc": "原子认领一个工作面，同角色会话只有一个成功"},
			{"method": "GET", "path": "/api/v1/inbox", "params": []string{"unread=1", "limit"}, "desc": "我的收件箱"},
			{"method": "POST", "path": "/api/v1/inbox/read", "desc": "标记已读，空 ids = 全部"},
			{"method": "GET", "path": "/api/v1/tasks", "params": []string{"assigned=me", "role", "status", "kind", "priority", "label", "q", "since", "updated_since", "view=lite", "group=status", "limit", "offset", "archived"}, "desc": "任务查询"},
			{"method": "POST", "path": "/api/v1/tasks", "desc": "建任务（可带 segments/sides/watchers/notify）"},
			{"method": "GET", "path": "/api/v1/tasks/{code}", "desc": "单任务全量"},
			{"method": "PATCH", "path": "/api/v1/tasks/{code}", "desc": "改元数据（status 变 done 会关闭所有工作面）"},
			{"method": "DELETE", "path": "/api/v1/tasks/{code}", "desc": "删任务（admin）"},
			{"method": "GET", "path": "/api/v1/tasks/{code}/pack", "params": []string{"side", "max_chars", "reports", "format=md|json", "empty"}, "desc": "★ 开工上下文包"},
			{"method": "GET", "path": "/api/v1/tasks/{code}/segments", "desc": "全部分段"},
			{"method": "POST", "path": "/api/v1/tasks/{code}/segments", "desc": "写/追加分段 {key,title,body,side_key,format,append}"},
			{"method": "GET", "path": "/api/v1/tasks/{code}/segments/{key}", "params": []string{"format=text|json|prompt"}, "desc": "单段正文"},
			{"method": "DELETE", "path": "/api/v1/tasks/{code}/segments/{key}", "desc": "清空/删除分段（骨架只清空）"},
			{"method": "GET", "path": "/api/v1/tasks/{code}/sides", "params": []string{"assignable=1"}, "desc": "工作面列表"},
			{"method": "POST", "path": "/api/v1/tasks/{code}/sides", "desc": "加工作面"},
			{"method": "PATCH", "path": "/api/v1/tasks/{code}/sides/{key}", "desc": "改工作面（指派/状态/依赖，unassign=true 取消指派）"},
			{"method": "DELETE", "path": "/api/v1/tasks/{code}/sides/{key}", "desc": "删工作面"},
			{"method": "GET", "path": "/api/v1/tasks/{code}/reports", "params": []string{"side", "type", "limit", "offset"}, "desc": "任务时间线"},
			{"method": "POST", "path": "/api/v1/tasks/{code}/reports", "desc": "★ 上报"},
			{"method": "GET", "path": "/api/v1/reports", "params": []string{"task", "type", "since", "limit"}, "desc": "跨任务上报流"},
			{"method": "GET", "path": "/api/v1/tasks/{code}/events", "desc": "单任务事件"},
			{"method": "GET", "path": "/api/v1/events", "params": []string{"since", "type", "limit", "backlog=1"}, "desc": "事件流（带 cursor）"},
			{"method": "GET", "path": "/api/v1/stream", "params": []string{"since", "type", "task"}, "desc": "SSE 实时流"},
			{"method": "POST", "path": "/api/v1/files", "desc": "multipart 上传（字段 file），返回 id+url+markdown"},
			{"method": "GET", "path": "/api/v1/files/{id}", "desc": "取附件；图片 inline"},
			{"method": "GET", "path": "/api/v1/roles", "params": []string{"keys=1", "holders=1"}, "desc": "角色"},
			{"method": "GET", "path": "/api/v1/identities", "params": []string{"names=1"}, "desc": "身份"},
			{"method": "POST", "path": "/api/v1/identities", "desc": "建身份（admin），返回一次性 key"},
			{"method": "PATCH", "path": "/api/v1/identities/{id}", "desc": "改名字/角色/激活角色/停用（改角色需 admin）"},
			{"method": "POST", "path": "/api/v1/identities/{id}/rotate", "desc": "轮换 key，旧的立即失效"},
			{"method": "GET", "path": "/api/v1/webhooks", "desc": "webhook 列表 + 事件词表"},
			{"method": "POST", "path": "/api/v1/webhooks", "desc": "建/改 webhook"},
			{"method": "DELETE", "path": "/api/v1/webhooks/{id}", "desc": "删 webhook"},
		},
		"agent_loop": []string{
			"GET /whoami",
			"GET /me/next?wait=30&claim=1   ← 阻塞等活；回来了就是轮到你了",
			"（干活，上下文在响应里）",
			"POST /tasks/{code}/reports     ← 完成后上报；置 side 为 done 会自动唤醒下游",
			"回到第二步",
		},
		"gotchas": []string{
			"重试上报会写两条：不确定时先 GET reports?limit=1 核对",
			"任务 status 变 done/archived 会把所有未完成工作面置为 done",
			"骨架分段不能被删除，只能清空——key 永远可取",
			"上传后要显式带 attachments:[id] 或把 markdown 贴进分段正文，否则附件只是挂任务上",
			"key 只在创建/轮换时显示一次；丢了只能轮换",
		},
	})
}
