# Keypoint Notify — 设计

> 2026-09-22。这份文档记录**为什么这么设计**。代码告诉你是什么，这里告诉你为什么。

## 一句话

把"人/agent 在 Claude Code 里干的活"变成结构化、可查询、可分发的任务；
把任务的某个**工作面**连同一整套上下文打包交给另一个 agent，对方一条命令就能开工。

## 谁在用

| 角色 | 入口 | 关心什么 |
|---|---|---|
| 人 | 浏览器看板 `/` | 任务全局、点按钮复制、管身份和 key |
| 发起方 agent | `kp` CLI + skill | "把这次会话的上下文变成任务并指派出去" |
| 承接方 agent | `kp task pack` | "我是谁、要干什么、怎么算完、完了怎么上报" |
| 其他系统 | HTTP API + webhook | 查询、订阅事件、接 CI |

## 为什么不是别的形态

被否掉的方案，以及原因：

- **纯 markdown + git**：没有并发写、没有查询、没有权限、没有通知。多人多 agent 一起用会打架。
- **直接上 Jira/Plane 这类现成项管**：它们的模型是"人给人派活"，没有"角色可改绑"、
  没有"给 agent 的上下文包"、没有 `/pack` 这种一次调用拿全上下文的能力。
  拿它们当底座要改的地方比从零写还多。
- **MCP server 而不是 CLI**：MCP 体验更好，但只能在支持 MCP 的客户端用，
  而且每个仓库都要注册一次。CLI + skill 到处能跑，`curl` 也能跑。
  MCP 可以以后加，CLI 是必须有的那一层。
- **Postgres**：这个体量（个人/小团队，几千个任务）SQLite 完全够，而且单文件、
  零运维、备份就是拷两个东西。真需要再加。
- **多租户 + 细粒度权限**：不是这个系统的目标。所有身份都能读全部任务；
  只有 admin 能做管理动作。这条线画得很清楚，是为了保持模型可解释。

## 领域模型

```
Identity (身份) ──── API key
  ├ kind: human | agent
  ├ roles: [role_key]        ← 可随时改绑，key 不变
  └ active_role              ← 决定写入归属

Role (角色)                  路由地址，不是职级
  backend / frontend / review / qa / ops / design / member（内置，可加）

Task (任务)  KP-<n>           短代号，稳定、可粘贴
  ├ kind / priority / status / owner_role / owner_identity
  ├ segments: [Segment]      固定骨架 7 个 + 自由扩展
  ├ sides: [Side]            并行工作面
  └ reports: 时间线（只增）

Segment (分段)               ★ "可复制"的最小单位
  ├ key                      goal / acceptance / 踩坑记录 …
  ├ scope: task 级 或 side 级
  └ body (markdown)

Side (工作面)                ★ 一个任务的并行工作片
  ├ key (api / ui / review)
  ├ assignee_role / assignee_identity
  ├ status / deps[]          依赖其它 side，成环会被拒
  └ segments: [Segment]      这一片自己的细节

Report (上报)                只增时间线
  ├ type: progress|blocker|decision|handoff|result|question
  ├ side_id / mentions[] / attachments[]
  └ 副作用可预期：blocker → side 变 blocked，progress → todo 变 doing

Event (事件)                 一切变更的事实，驱动收件箱/webhook/SSE
```

### 三个关键取舍

**1. 分段是固定骨架 + 自由扩展，不是纯自由 markdown。**
纯自由 markdown 写起来爽，但 agent 拿不到稳定字段：这次叫"目标"，下次叫"goal"，
再下次叫"要做什么"。固定 7 个 key 在任务创建时就存在（可以是空的），
所以 `?key=acceptance` 永远能问，不会 404。
自由分段补足表达力（"踩坑记录"、"数据样本"），中文 key 保留。

**2. side 是一等公民，不是标签。**
"指定 side assign"这个需求，如果 side 只是一条"谁负责什么"的备注，
那两边工作量一大，详情就全挤在一个大块里，没法分开交接。
做成带独立分段、独立状态、独立依赖的工作面之后，
`kp task pack KP-12 --side ui` 才能给出**只属于 ui 的**上下文包。

**3. Report 只增不改。**
时间线是可以信任的前提。编辑会打 `edited_at`，但创建时间不动，顺序不动。
这让"谁在什么时候说了什么"永远可回溯。

---

## 身份模型：角色可改绑

API key 绑**身份**，身份持**角色**。这是"角色+apikey绑定身份（可修改）"的落地：

```bash
kp identity create alice --roles frontend          # 发一个 key
kp identity set-roles alice backend,review         # key 没变，能力变了
kp identity set-roles alice review                 # 收窄
kp identity rotate alice                           # 换 key（旧的立即失效）
```

为什么不让 key 直接绑死角色：一个人/一个 agent 会话经常会"以不同身份做事"，
绑死意味着管一堆 key。为什么不把角色做成纯标签：那样权限和通知都无从谈起
——"通知持有 review 角色的人"是核心机制。

**写入归属**：默认用身份的 `active_role`；单次写入可以覆盖
（`--role` / JSON 里的 `"role"`），但覆盖只在该身份持有的角色里生效。
不在持有的角色里就回退到 active_role 并留一条日志——不报错，
因为"身份被改绑"和"这次调用还写着旧角色"是很常见的组合，不该让写入失败。

**权限**只有一条规则：**admin 能做管理动作**（建身份、改他人角色、删任务、配 webhook）。
其余所有身份读写全部任务。这是给协作团队用的工具，不是多租户平台。

---

## API：为 LLM 而设计

用户明确要求"LLM 友好"。具体落在九件事上：

1. **`GET /tasks/{code}/pack`** —— 一次调用拿到开工所需的全部上下文。
   这是整个设计存在的理由。承接 agent 的 bootstrap 就是一次 HTTP 请求。
2. **`/api/v1/llms.txt`** —— 服务端自带的纯文本 API 说明书。模型读一次就懂，
   不用读源码、不用人写 prompt。同一份内容还有 `/api/v1/schema` 的 JSON 形式。
3. **稳定的短标识**：`KP-12`、段 key `acceptance`、side key `ui`。
   内部 UUID 不出现在任何人类/模型要写的地方。
4. **错误带恢复线索**：
   ```json
   {"error":"segment_not_found","message":"...","hint":"单段取全文：...",
    "did_you_mean":"acceptance","options":["context","goal",...]}
   ```
   读到就能改参数重试，不用放弃也不用猜。`near_miss_skeleton_key` 专门拦
   "把 `acceptance` 拼成 `accepance` 于是悄悄新建了一个重复分段"这类静默错误。
5. **默认 md，备选 json**：`pack` 默认返回 markdown，因为消费者是 prompt。
   要程序化处理时 `format=json`。
6. **`view=lite` / `--lite`**：砍掉分段正文，省 token。
7. **`max_chars` + 显式截断**：截断会说明遗漏了哪些段、怎么取全文。绝不静默截断。
8. **时间参数接受人话**：`7d` / `24h` / `2026-09-01` / RFC3339 都行。
   模型不用算时间戳。
9. **cursor 语义单一**：`GET /events` 返回 `cursor`，下次原样传回。
   循环就是 `cursor = resp.cursor`。

### pack 里最有价值的一段：交付契约

每份 pack 的末尾：

```
## 交付契约（承接方必读）

你正在承接 KP-12 的工作面 ui，以角色 @frontend 的身份推进。

边界：只改本工作面相关的代码/配置；需要动到别的面，先上报交接，不要顺手改。

完成后（不要静默结束）：
  kp report KP-12 --side ui --type result -m "改动摘要 + 验证方式 + 遗留风险"
  kp report KP-12 --side ui --type blocker -m "卡在哪、需要什么" --mention @frontend
  kp task pack KP-12 --side ui   # 之后有更新就重跑这条拿最新上下文
```

这段让"承接方做完会主动上报"成为**上下文的一部分**，而不是指望对方记得。
闭环不是靠流程约束，是靠把下一步印在纸上。

**角色的选择顺序**：focus side 的 assignee_role → 任务的 owner_role → 调用者自己的角色。
因为 pack 通常是**给别人**生成的，说承接方的语言比说自己的语言有用。

---

## 通知

三个方向，覆盖不同消费者：

| 方向 | 机制 | 消费者 |
|---|---|---|
| 站内 | 事件 → 通知行 → `/inbox` + 顶端未读徽章 | 人、agent 轮询 |
| 出站 | 事件 → webhook 投递（HMAC 签名） | Slack / 飞书 / n8n / 自建 |
| 拉取 | `/api/v1/events?since=<cursor>` / SSE `/stream` | agent、脚本 |

**收件人怎么定**：显式 `mentions` ∪ 任务 watchers ∪ 任务 owner ∪ 该 side 的 assignee
（角色会展开成所有持有者）。**发起人永远不通知自己。**

**webhook 投递用持久游标，不用内存扇出。** 代价是每 tick 一次查询；
换来的是重启不丢事件、订阅方宕机一小时之后还能收到漏掉的。
新订阅的游标在**创建那一刻**播种（不是首次投递时）——否则"加完订阅立刻发生的事件"
会被静默吃掉。这个是实测踩出来的。

---

## 存储

SQLite（`modernc.org/sqlite`，**纯 Go，CGO_ENABLED=0**，所以是真的单二进制）。

- 时间戳一律 INTEGER unix 毫秒：范围查询和 `since` 过滤走索引。
- 枚举列是普通 TEXT，在 Go 里校验：加一个状态应该是改代码，不是改 schema。
- `segments.side_id` 用 `''` 而不是 NULL——SQLite 把 NULL 当作互不相等，
  UNIQUE 约束会失效。
- 附件**内容寻址**：`blobs/<sha256前两位>/<sha256>`。重复上传同一个截图零成本，
  `rsync -a` 就是正确的增量备份。
- 写事务里**不借第二个连接**（`identityByNameTx` 而不是 `IdentityByName`）：
  连接池只有 4 个，事务里再借会死锁。这条有注释标着，别改回去。

---

## 分层

```
cmd/keypoint            main，只有 os.Exit(cli.Run(...))
internal/cli            命令分发 + 各子命令
internal/client         CLI 用的 HTTP 客户端
internal/config         ~/.keypoint/config.json
internal/httpapi        路由、中间件、handlers、错误模型
internal/pack           ★ 上下文包渲染（唯一的"呈现"逻辑所在）
internal/webhook        出站投递
internal/store          ★ 唯一知道 SQL 的包
internal/model          领域类型（JSON tag 即公共契约）
internal/webui          内嵌看板（embed）
```

依赖方向单向向下。`pack` 和 `store` 都不知道 HTTP 存在。

**为什么 pack 单独成包**：它是"给 agent 看的渲染"，会被 HTTP handler 用，
也可能被将来的 MCP server 用。放 httpapi 里就绑死了。而且它的截断、
契约生成、索引渲染都有独立的可测逻辑。

---

## Web UI

单 HTML + 原生 JS + CSS，`embed` 进二进制，**没有构建步骤**。

- 任务优先：打开就是看板，6 列 = 6 个状态，可拖拽改状态。
- **每个分段卡片右上角有自己的复制按钮**（"复制" / "复制为 prompt"）——
  "支持分段复制任务细节详情"这条需求的落地点。
- 任务页顶部"复制开工包"，直接把 pack 全文进剪贴板。
- 登录用 `POST /session` 把 key 换成 HttpOnly cookie：浏览器导航带不了
  Authorization 头，而 cookie 值就是 key 本身——同样的秘密，但不暴露给 JS。

---

## 已知的边界

诚实记录没做的事：

- **没有 schema 迁移框架**。加表用 `CREATE TABLE IF NOT EXISTS` 自动补；
  加列要手工 `ALTER TABLE`。当前规模不值得引入。
- **没有分页游标，只有 offset**。任务量大到需要游标分页时再加。
- **单写者**。SQLite + WAL，4 个连接。并发写入靠 `busy_timeout` 排队。
  这个体量够用。
- **没有全文索引**。`q=` 是 `LIKE %...%`，跨 segments.body。上千任务没问题，
  上万条会慢。真需要时上 FTS5。
- **pack 的 `max_chars` 按字节而非 token**。字节是保守估计（中文 3 字节/字，
  token 更少），宁可多留余量。
- **没有 MCP server**。故意先不做，见上面"为什么不是别的形态"。
