# 契约稳定性

这份文档说明**哪些东西是对外承诺**。开源之后，别人会在它上面写工具、写集成、
写 agent，他们需要知道什么可以依赖、什么会变。

判断标准很简单：**一个不是你写的客户端，会读到什么？** 那个东西就是契约。

---

## 稳定：改动需要走弃用周期

### HTTP API 的形状

| 内容 | 承诺 |
|---|---|
| 路径 | `/api/v1/...` 下的路径不会被重命名或挪走；新增只会**加**新的 |
| 方法与语义 | `GET` 永远无副作用；写操作只在 `POST` / `PATCH` / `DELETE` |
| JSON 字段名 | 已有字段不改名、不改类型。新增字段是允许的，客户端**必须容忍未知字段** |
| 时间格式 | RFC3339（`2026-09-22T08:15:03.514Z`），一律 UTC |
| 时间戳含义 | 服务端时间，单调递增；不要拿客户端时钟做比较 |

### 枚举值

```
kind          bug | feature | chore | research | review | incident
task status   inbox | ready | doing | blocked | review | done | archived
side status   todo | doing | blocked | done
priority      P0 | P1 | P2 | P3
report type   progress | blocker | decision | handoff | result | question | finding
```

这些值**只会增加，不会改义**。具体地说：

- 加一个新的 report type 是允许的 —— 客户端读到未知 type 应该**原样显示**，
  而不是崩掉或丢掉。
- `done` 永远是「完成」，不会哪天变成「已归档」。
- 前端/agent 不要硬编码「所有可能的值」来做校验，用
  `GET /api/v1/schema` 拿当前词表。

### 错误格式

```json
{
  "error": "segment_not_found",
  "message": "任务 KP-12 没有分段 \"accepance\"",
  "hint": "单段取全文：kp task seg KP-12 <key>",
  "did_you_mean": "acceptance",
  "options": ["context","goal","deliverable","acceptance","interface","files"],
  "docs": "/api/v1/llms.txt"
}
```

- **`error` 是稳定机器码**，可以 switch 它。加新码是允许的。
- `message` / `hint` 是给人看的中文散文，**不保证逐字稳定**，别 grep 它。
- 已有码**不会改义**。`task_not_found` 永远是「这个 code 找不到任务」。

### 事件

事件类型（`task.created`、`report.created`、`side.unblocked` …）是 webhook 和
SSE 的公共词表。规则：

- 已有类型不改名。
- 新增类型会在 `/api/v1/webhooks` 的 `event_types` 和 `/api/v1/schema` 里列出。
- `payload` 里的字段可能新增；订阅方应该按 key 读，不要按位置。
- **订阅是按前缀匹配的**：订阅 `task` 等于 `task.*`。所以往 `task.` 下加新事件
  会给现有订阅方多发东西 —— 这是有意为之，也是唯一的「破坏性」风险，
  发生时会写在 CHANGELOG 里。

### 语义保证（这些是行为承诺，不是格式承诺）

| 保证 | 含义 |
|---|---|
| Report 只增 | 时间线不会被重排、不会被删。编辑会打 `edited_at`，创建时间不动 |
| 骨架分段永远可取 | 七个 key 在任务创建时就存在，永远不会 404 |
| side 完成后自动解封下游 | 依赖它的面会离开 blocked，并发 `side.unblocked` |
| 最后一个面完成自动收尾 | 任务变 `done`，事件 `reason=all_sides_done` |
| 认领是原子的 | 同角色多会话抢同一个面，只有一个拿到（条件 UPDATE） |
| key 只在创建/轮换时明文出现一次 | 服务端只存 SHA-256 |

---

## 会变：不要依赖

| 内容 | 为什么 |
|---|---|
| `message` / `hint` 的措辞 | 会随文案改进变化 |
| `GET /api/v1/llms.txt` 的正文 | 会随功能更新重写，那是给人/模型读的散文 |
| 看板的 HTML / CSS | 没有前端契约，UI 随时会改 |
| 数据库表结构 | 内部实现。要数据就调 API |
| 分页的具体行为 | 现在是 `limit`/`offset`，将来可能加游标 —— 但旧参数会继续工作 |
| 日志格式 | 随时会变 |

---

## 演进方式

- **加东西**：加端点、加字段、加枚举值、加错误码 —— 随时可以做，不需要弃用周期。
  客户端的义务是**容忍未知**。
- **改东西**：先加新的，标记旧的 deprecated，至少在文档里挂一个版本，再删。
- **坏东西**（安全漏洞）：可以立即改，会在 CHANGELOG 里说明。

预发布阶段（`0.x`）保留在必要时直接改的权利，但每次都会记在 CHANGELOG。

---

## 客户端怎么写才不会被将来的改动打到

```bash
# 1. 用机器码判断，不要匹配文案
if [ "$(echo "$ERR" | jq -r .error)" = "segment_not_found" ]; then ...

# 2. 枚举从 schema 拿，不要自己写死
curl -s $KP/api/v1/schema | jq -r '.enums.report_type[]'

# 3. 读事件按 key，不要按位置
jq -r '.payload.side' <<< "$EVENT"

# 4. 时间和客户端时钟无关，别做「客户端时间 - 服务端时间」的假设
```

`GET /api/v1/llms.txt` 和 `GET /api/v1/schema` 是运行时权威 —— 客户端应该在启动
时读一次（或缓存一个版本），而不是把本文档抄一份进代码。
