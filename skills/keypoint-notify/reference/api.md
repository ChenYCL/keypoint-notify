# Keypoint Notify HTTP API

给不是 Claude Code 的 agent、脚本、CI 用。

**服务端自己就提供一份权威说明**——永远优先读它而不是这份可能过期的副本：

```bash
curl -H "Authorization: Bearer $KP_KEY" $KP/api/v1/llms.txt     # 纯文本，给模型读
curl -H "Authorization: Bearer $KP_KEY" $KP/api/v1/schema       # JSON，给程序读
```

---

## 认证

```http
Authorization: Bearer kp_xxxxxxxxxxxx
X-API-Key: kp_xxxxxxxxxxxx
```

两种都收。一个 key 绑一个**身份**；身份持有若干**角色**，可以随时改绑（key 不变）。

首次初始化（空系统）：

```bash
curl -X POST $KP/api/v1/bootstrap \
  -H 'Content-Type: application/json' \
  -d '{"name":"owner","kind":"human"}'
# → {"identity":{...},"api_key":"kp_...","warning":"这个 key 只显示这一次"}
```

---

## 核心流程

### 1. 拿开工上下文（最重要的一条）

```http
GET /api/v1/tasks/{code}/pack?side=ui&format=md&max_chars=12000&reports=5
```

返回自包含的 markdown：任务头、分段索引、工作面表、分段正文、最近上报、
附件链接，以及**交付契约**（做完该调什么上报）。

参数：

| 参数 | 默认 | 说明 |
|---|---|---|
| `side` | — | 聚焦某个工作面 |
| `max_chars` | 12000 | 超限会截断，并在文末列出遗漏了哪些段 |
| `reports` | 5 | 附带最近 n 条上报；`0` 不带 |
| `format` | `md` | `md` 给模型看；`json` 给程序用 |
| `empty` | — | `1` 时带上还没写内容的固定分段 |

### 2. 等活（协作循环）

```http
GET /api/v1/me/next?wait=30&claim=1&task=KP-12&side=ui
```

一条调用回答：有没有属于我的活 / 为什么是我 / 完整开工包。

| 参数 | 说明 |
|---|---|
| `wait` | 没活时服务端挂起秒数（长轮询，上限 60） |
| `claim=1` | 拿到属于我的工作面就原子认领 |
| `since` | 显式游标；**省略则用该身份上次存下的** |
| `task` / `side` | 收窄 |
| `format` | `md`（默认）/ `json` |

`reason`：`mention`｜`unblocked`｜`assigned`｜`owned`。
只认领属于调用者的工作面 —— 被 mention 不等于接管别人的面。

```http
POST /api/v1/tasks/{code}/sides/{key}/claim   原子认领，同角色只有一个成功
```

### 3. 上报

```http
POST /api/v1/tasks/{code}/reports
Content-Type: application/json

{
  "type": "blocker",
  "side_key": "ui",
  "body": "被 api 挡住：冷却接口没上线",
  "segments": [{"key":"阻塞点","title":"阻塞点","body":"..."}],
  "mentions": ["@backend", "review"],
  "attachments": ["fil_xxx"],
  "status": "blocked"
}
```

### 4. 查询

```http
GET /api/v1/tasks?assigned=me
GET /api/v1/tasks?role=backend&status=doing,blocked&since=7d&q=验证码&limit=20
GET /api/v1/tasks?group=status&view=lite
GET /api/v1/whoami
GET /api/v1/me/board
GET /api/v1/inbox?unread=1
GET /api/v1/events?since=<cursor>
GET /api/v1/stream?since=<cursor>          SSE
```

时间参数接受 RFC3339、`2026-09-01`、相对量 `7d` / `24h` / `90m` / `30s`。

---

## 端点表

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/health` | 存活 + 是否已初始化（**免鉴权**） |
| GET | `/api/v1/llms.txt` | API 说明书（**免鉴权**） |
| GET | `/api/v1/schema` | 机器可读 schema（**免鉴权**） |
| POST | `/api/v1/bootstrap` | 建第一个身份（**仅空系统可用**） |
| POST | `/api/v1/session` | 用 key 换浏览器 cookie |
| DELETE | `/api/v1/session` | 登出 |
| GET | `/api/v1/whoami` | 身份/角色/未读/能力 |
| GET | `/api/v1/me/board` | 我手上的工作面 + 我负责的任务 |
| GET | `/api/v1/inbox` | `?unread=1&limit=N` |
| POST | `/api/v1/inbox/read` | `{"ids":[...]}`，空数组 = 全部已读 |
| GET | `/api/v1/tasks` | 查询（见下） |
| POST | `/api/v1/tasks` | 建任务 |
| GET | `/api/v1/tasks/{code}` | 单任务全量 |
| PATCH | `/api/v1/tasks/{code}` | 改元数据 |
| DELETE | `/api/v1/tasks/{code}` | 删任务（admin） |
| GET | `/api/v1/tasks/{code}/pack` | ★ 开工上下文包 |
| GET | `/api/v1/tasks/{code}/segments` | 全部分段 |
| POST | `/api/v1/tasks/{code}/segments` | 写/追加分段 |
| GET | `/api/v1/tasks/{code}/segments/{key}` | `?format=text\|json\|prompt` |
| DELETE | `/api/v1/tasks/{code}/segments/{key}` | 清空/删除 |
| GET | `/api/v1/tasks/{code}/sides` | `?assignable=1` 只看未指派 |
| POST | `/api/v1/tasks/{code}/sides` | 加工作面 |
| PATCH | `/api/v1/tasks/{code}/sides/{key}` | 指派/状态/依赖 |
| DELETE | `/api/v1/tasks/{code}/sides/{key}` | 删工作面 |
| GET | `/api/v1/tasks/{code}/reports` | 任务时间线 |
| POST | `/api/v1/tasks/{code}/reports` | ★ 上报 |
| GET | `/api/v1/tasks/{code}/events` | 单任务事件 |
| GET | `/api/v1/reports` | 跨任务上报流 |
| GET | `/api/v1/events` | `?since=<cursor>&type=&task=&backlog=1` |
| GET | `/api/v1/stream` | SSE |
| POST | `/api/v1/files` | multipart 上传，字段名 `file` |
| POST | `/api/v1/tasks/{code}/files` | 上传并挂到任务 |
| GET | `/api/v1/files/{id}` | 取附件（图片 inline） |
| GET | `/api/v1/roles` | `?keys=1` / `?holders=1` |
| POST | `/api/v1/roles` | 建/改自定义角色 |
| DELETE | `/api/v1/roles/{key}` | 删自定义角色 |
| GET | `/api/v1/identities` | `?names=1` |
| POST | `/api/v1/identities` | 建身份（admin），返回一次性 key |
| PATCH | `/api/v1/identities/{id}` | 改名/角色/激活角色/停用（改角色需 admin） |
| POST | `/api/v1/identities/{id}/rotate` | 轮换 key |
| GET | `/api/v1/webhooks` | 列表 + 事件词表 |
| POST | `/api/v1/webhooks` | 建/改 |
| DELETE | `/api/v1/webhooks/{id}` | 删 |

### `/api/v1/tasks` 查询参数

`assigned=me|<身份名>`、`role=`、`status=`、`kind=`、`priority=`、`label=`、
`q=`（全文搜 code/标题/摘要/分段正文）、`since=`、`updated_since=`、
`view=lite`、`group=status`、`limit=`、`offset=`、`archived=1`。

---

## 枚举

```
kind          bug | feature | chore | research | review | incident
task status   inbox | ready | doing | blocked | review | done | archived
side status   todo | doing | blocked | done
priority      P0 | P1 | P2 | P3
report type   progress | blocker | decision | handoff | result | question
```

事件类型（webhook / SSE 用）：

```
task.created  task.updated  task.status_changed  task.deleted
segment.upserted  segment.deleted
side.created  side.updated  side.assigned  side.deleted
report.created  file.uploaded  identity.updated
```

---

## 错误格式

所有错误都是同一个形状，而且**带恢复线索**：

```json
{
  "error": "segment_not_found",
  "message": "任务 KP-12 没有分段 \"accepance\"",
  "hint": "单段取全文：kp task seg KP-12 <key>；列全部：kp task show KP-12",
  "did_you_mean": "acceptance",
  "options": ["context","goal","deliverable","acceptance","interface","files"],
  "docs": "/api/v1/llms.txt"
}
```

**读到 `did_you_mean` / `options` 就改参数重试**，不要放弃也不要猜。

| 状态 | error |
|---|---|
| 400 | `invalid_json` `bad_status` `bad_report_type` `bad_priority` `bad_time` `near_miss_skeleton_key` `missing_key` `no_file` `bad_multipart` |
| 401 | `missing_api_key` `invalid_api_key` |
| 403 | `forbidden` `builtin_role` |
| 404 | `task_not_found` `segment_not_found` `side_not_found` `file_not_found` `not_found` |
| 409 | `conflict` `already_bootstrapped` |
| 413 | `file_too_large`（附件上限 32MB） |

---

## Webhook

```http
POST <你的 URL>
Content-Type: application/json
X-KP-Event: report.created
X-KP-Event-Id: 42
X-KP-Signature: sha256=<hex hmac of raw body with your secret>

{"id":42,"type":"report.created","actor_name":"alice","task_code":"KP-12",
 "payload":{...},"created_at":"...","ui_url":"/t/KP-12"}
```

验签（Go）：

```go
ok := webhook.Verify(secret, rawBody, r.Header.Get("X-KP-Signature"))
```

订阅是按事件类型过滤的，支持前缀：订阅 `task` 等于 `task.*`。
新订阅从**订阅那一刻**开始投递，不补历史。失败重试 3 次（1s、2s 退避），
之后记录 `last_error` 并前进游标——一个坏订阅不会卡住整条流。

---

## 安全模型

- key 只存 SHA-256，明文只在创建/轮换时返回一次。
- 所有身份都能读全部任务、写任务和上报（这是给协作团队用的，不是多租户系统）。
- 只有 `admin` 角色能：建身份、改他人角色、停用身份、删任务、删角色、配 webhook。
- 轮换 key 后旧 key **立即**失效。
- 附件走 `GET /api/v1/files/{id}`，同样需要 key。SVG 强制下载不 inline（防 XSS）。
