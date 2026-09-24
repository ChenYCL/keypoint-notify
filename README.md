# Keypoint Notify

[English](README.en.md) · **中文**

把"人/agent 在 Claude Code 里干的活"变成结构化、可查询、可分发的任务。
一个 Go 单二进制：服务端、看板、CLI 全在里面。

```bash
# 接入（管理员给你的 key），顺带把斜杠命令装进 Claude Code / Kimi Code
curl -fsSL "http://<服务端>/install.sh?key=kp_…" | sh

# 一天的活，就这几条
kp board                                   # 我手上有什么
kp next --claim                            # 接一件活，打印完整开工包
kp report KP-12 -m "接口写到一半"           # 中途上报（--type blocker|question|decision）
kp done KP-12 ui -m "改了什么 / 怎么验证 / 风险"   # 完结：下游自动解封
kp release KP-12 ui -m "今天做不完"         # 放手：还给角色，别人能接
kp cancel KP-12 -m "需求撤了"               # 取消：记原因并归档

# 让它自己跑
kp loop --agent claude                     # 无人值守：每件活一个新会话，做完自动 kp done
```

在 Claude Code 里同样的事是 `/kp-next`、`/kp-done`、`/kp-report`…（Kimi Code：`/skill:kp-next`）。

---

## 它解决什么

**协作的断点不是"活没分下去"，是"分下去之后对方没有上下文"。**
传统项管工具给出的是标题和几行描述，承接的人还是要回去问、去翻、去猜。
这个系统把"开工需要的一切"做成一次调用的产物：

- 任务拆成**固定骨架分段**（背景/目标/交付物/约束/验收/接口/文件）+ 自由扩展段
- 任务拆成并行的**工作面（side）**，各自有负责人角色、状态、依赖、自己的细节
- `kp task pack` 输出一份**自包含的 markdown**：任务头、分段索引、工作面表、
  全部正文、最近上报、附件链接，以及末尾的**交付契约**（做完该调什么上报）

承接方拿到的不是"一个标题"，是"可以直接开工的 prompt"。

---

## 三个核心概念

| 概念 | 是什么 | 为什么 |
|---|---|---|
| **分段 Segment** | 任务细节的切片，稳定 key，可单独取用 | "可复制"的最小单位。`kp task seg KP-1 acceptance` 直接进剪贴板 |
| **工作面 Side** | 一个任务的并行工作片（api / ui / review），各带负责人和依赖 | 让"同一任务的多边并行推进"能分开交接 |
| **上报 Report** | 只增时间线：progress / blocker / decision / handoff / result / question | 闭环。承接方做完不是静默结束，是发一条上报 |

---

## 角色与身份

API key 绑**身份**，身份持**角色**，角色可以随时改绑而不用换 key：

```bash
kp identity create alice --roles frontend          # 发 key（只显示一次）
kp identity set-roles alice backend,review         # key 没变，能力变了
kp identity rotate alice                           # 需要换 key 时
```

任务和工作面按**角色**指派（"谁能接前端"），而不是按人。
通知会发给所有持有该角色的人。所以一个人休假、换岗、加角色，都不需要重新派活。

---

## 斜杠命令（Claude Code / Kimi Code）

`kp install`（或 `install.sh`）会把一个行为手册和八个斜杠命令装到本机：Claude Code 在
`~/.claude/skills/`，Kimi Code 在 `~/.kimi-code/skills/`。在会话里输入 `/kp` 就能看到。

| 做什么 | Claude Code | Kimi Code | 终端里的等价命令 |
|---|---|---|---|
| 建任务 | `/kp-new 一句话` | `/skill:kp-new` | `kp task new --from-json task.json` |
| 接一件活 | `/kp-next` | `/skill:kp-next` | `kp next --claim` |
| 中途上报 | `/kp-report` | `/skill:kp-report` | `kp report KP-12 -m "…"` |
| 完结交棒 | `/kp-done` | `/skill:kp-done` | `kp done KP-12 ui -m "…"` |
| 放手 / 取消 | `/kp-cancel` | `/skill:kp-cancel` | `kp release …` / `kp cancel …` |
| 定时自动处理 | `/kp-loop 10m` | — | `kp loop --agent claude` |
| 订阅，来了就开始 | `/kp-watch` | `/skill:kp-watch` | `kp wait` / `kp watch KP-12` |
| 调研 / 定论 | `/kp-research` | `/skill:kp-research` | `kp research new` / `note` / `ask` / `decide` |

斜杠命令只是薄薄一层：它们让模型从当前会话里整理内容，再调上面那些 kp 命令。
不装也能用 —— 说「记个任务」「上报一下」「我手上有什么」，行为手册 `keypoint-notify` 会被自动调起。

不是 Claude Code / Kimi Code 的 agent，把服务端自带的说明书喂给它：
`curl $KP/api/v1/llms.txt`（API）、`curl -H "Authorization: Bearer $KEY" $KP/api/v1/agent-prompt`（运行说明）。

---

## 调研：方案、论点、待定、定论

agent 去调研一个方案、团队讨论一个取舍时，结论和理由不该散在聊天记录里。看板上的
「调研」页（`/research`）和 `kp research` 把它们收在一起：**最上面是所有任务里还没人拍板的问题**，
下面是每个调研的问题、方案、论点数和最近的定论。

```bash
kp research new "通知推送用 SSE 还是长轮询？" --option "A：SSE" --option "B：长轮询"
kp research note   KP-12 -m "支持 B：会话里阻塞 25s 不会超时（证据…）"   # 论点
kp research ask    KP-12 -m "要不要支持离线补发？" --mention @backend     # 拿不准 → 待定
kp research decide KP-12 -m "定论：选 B，因为…" --close                   # 定论
kp research                                                               # 还有什么没定
```

会话里是 `/kp-research`。规则只有一条：一个问题之后在同一任务里写了定论就算定了 ——
所以清空「待定」的办法就是把结论写下来。agent 只写论点和问题，**不替人拍板**。

---

## 日常自动化：选一种

| 场景 | 怎么开 | 说明 |
|---|---|---|
| 我盯着，一次一件 | `/kp-next` | 最常用。每一步都看得到 |
| 会话开着，定时自动接 | `/kp-loop 10m` | = `/loop 10m /kp-next`。会话空闲才触发，7 天过期，`/kp-cancel loop` 停 |
| 来了叫我 | `/kp-watch` | 后台挂 `kp wait`，有新活 / 新通知把会话叫醒；`/kp-watch KP-12` 盯别人的任务 |
| 关掉会话也一直跑 | `kp loop --agent claude` | 在仓库目录里启动；每件活一个新会话，做完自动 `kp done`。Kimi：`--agent kimi` |
| 推给飞书 / Slack / n8n | `kp hook add <url> --secret S` | 需 admin；HMAC 签名 |

多个 bot 并行：每个 bot 一个身份、一个配置目录，
`KEYPOINT_HOME=~/.keypoint-be-bot kp loop --agent claude`。同角色开几个都行，认领是原子的。

---

## 最佳实践

- **做完用 `kp done`，不要只报 result。** 只上报的话面停在「进行中」，下游永远等不到解封。
- **不做了用 `kp release`。** `kp task side assign --unassign` 会连角色一起清掉，那个面就没人接了。
- **goal 和 acceptance 是底线。** 不知道的写「（待确认：…）」，别编 —— 下游会当真。
- **派给角色，不派给人；派之前 `kp role ls --holders`。** 没人持有的角色等于没人收到。
- **一个面一个负责人。** 要动别人的面，先 `kp report --type handoff` 说一声。
- **卡住要说「需要什么」并 @ 能解决的人**：`--type blocker --mention @backend`。
- **管理员 key 只做管理。** 日常身份（如 frontend）和 admin 分开放：`KEYPOINT_HOME=~/.keypoint-admin`。
  bot 只给业务角色 —— 无人值守的会话能改文件、跑命令。
- **`kp loop` 在要干活的仓库目录里启动。** 会话就在那个目录里改代码。

---

## 协作循环怎么运转

一条调用回答三件事：**有没有属于我的活 / 为什么是我 / 开工需要的全部上下文**（`kp next`）。

| `reason` | 含义 | 怎么做 |
|---|---|---|
| `mention` | 有人在上报里 @ 了你 | **回应**。不是让你接手他的工作面 |
| `unblocked` | 你的面有依赖，刚全部完成 | 接手，开工 |
| `assigned` | 指派给你角色的面，还没人认领 | 接手，开工 |
| `owned` | 你是任务负责人，任务有新动静 | 看一眼，决定要不要派人 |

- 面完成 → 依赖它的下游**自动解封**，等着的会话立刻被唤醒。交棒是副作用，不是额外一步。
- 最后一个面完成 → 任务自己收尾。
- `kp next --wait 30` 只为**新**东西醒来；`kp wait` 同样等待但不认领、不动游标，适合放后台当「通知」。

---

## 看板

浏览器打开 `http://127.0.0.1:8787/`：

**首页是上手引导**（不是看板），从上到下：

- 三步让 agent 干活：`curl … install.sh` → 会话里敲 `/kp` → `/kp-loop 10m` 或 `kp loop --agent claude`
- 日常自动化五种场景并排，每张卡一条命令
- 斜杠命令对照表（会话里 / 终端里）
- 一个任务的标准流程（建 → 接 → 报 → 完结 → 放手/取消 → 盯例外），命令默认折叠
- 最佳实践、当前在跑什么、可复制的完整 Agent Prompt

看板在 `/board`。

- 6 列看板，拖拽改状态
- 任务页每个分段卡片右上角有**独立复制按钮**（"复制" / "复制为 prompt"）
- 顶部"复制开工包"，整份 pack 进剪贴板
- 收件箱、身份/角色/webhook 管理页

单 HTML + 原生 JS，`embed` 进二进制，没有构建步骤。

---

## 通知

| 方向 | 怎么用 |
|---|---|
| 站内 | 看板顶端的未读徽章 + `/inbox`；`kp inbox --unread` |
| Webhook | `kp hook add https://... --secret S --events report.created`，带 HMAC 签名 |
| 拉取 | `kp events --since <cursor>` / `GET /api/v1/stream`（SSE） |

---

## 安装

### 服务端：一台 Linux VPS，一条命令

```bash
# 有域名（推荐）：自动 HTTPS
curl -fsSL https://raw.githubusercontent.com/ChenYCL/keypoint-notify/main/deploy/install-server.sh | sudo sh -s -- --domain kp.example.com

# 没有域名：直接用 IP + 端口（明文 HTTP，脚本会提示风险）
curl -fsSL https://raw.githubusercontent.com/ChenYCL/keypoint-notify/main/deploy/install-server.sh | sudo sh
```

[`deploy/install-server.sh`](deploy/install-server.sh) 做完这些：从 GitHub Release 下载
`kp`（校验 SHA256）并把 4 个平台的客户端放在旁边（同事的 `install.sh` 按平台拿）、
建系统用户和 systemd 服务（开机自启、崩溃重启、非 root 运行）、`--domain` 时再起一个
Caddy 做自动证书、认领管理员并打印管理员 key。重跑就是升级，已有数据和管理员不动。

装完拉人进来（在 VPS 上）：

```bash
sudo kp-admin identity create alice --kind human --roles frontend
# → 打印一段接入说明，整段发给对方；对方一条 curl 装好 kp + skill
```

管理员 CLI 在 VPS 上连的是 `127.0.0.1`，接入说明里给别人的地址是 `public_url`（脚本已按
`--domain` 或公网 IP 设好；不对的话 `sudo kp-admin config set public_url <对外地址>`）。

其他参数：`--local`（只听 127.0.0.1，自己配隧道/反代）、`--port`、`--admin`、
`--version v0.2.0`、`--uninstall`。发版：推一个 `v*` tag，
[`release.yml`](.github/workflows/release.yml) 会把四个平台的二进制和 `SHA256SUMS` 发到 Release。

### 一台什么都没有的新机器

```bash
curl -fsSL "http://<服务端>/install.sh?key=kp_xxx" | sh
```

`install.sh` 由服务端自己生成，做三件事：下载**对应你自己平台**的 `kp`
（服务端跑在 Linux 不代表你用 Linux）、`chmod +x`、用这个 key 接入并把
skill 装到本机认识的 CLI 里。不需要仓库、不需要包管理器、不需要 Go。

服务端按请求方的平台从自己旁边的 `bin/kp-<os>-<arch>` 发二进制。一键安装脚本和 Docker
镜像都已经带好四个平台；**自己从源码部署**时要补上，否则只有和服务端同平台的人装得上：

```bash
make dist           # 交叉编译 darwin/linux × arm64/amd64 到 dist/（附 SHA256SUMS）
make release-bin    # 放到服务端二进制旁边的 bin/
```

没有对应平台时 `/kp?os=…&arch=…` 会 404 并告诉你 `go install` 的地址，
不会发一个错的二进制给你。

不带 `key` 就只装二进制：

```bash
curl -fsSL "http://<服务端>/install.sh" | sh
```

### 已经装了 kp 的机器

```bash
kp install                                 自动探测本机有哪些 CLI，各装一份
kp install --target claude                 只给 Claude Code
kp install --target codex,gemini           给多个
kp install --target agents                 装成当前目录的 AGENTS.md（跟仓库走）
kp install --from URL --key kp_xxx         首次接入
```

**各家 CLI 的约定不一样，装的位置也不同**：

| CLI | 装到哪 | 形式 |
|---|---|---|
| Claude Code | `~/.claude/skills/keypoint-notify/` | 技能目录（SKILL.md + reference/，带 frontmatter） |
| Codex CLI | `~/.codex/AGENTS.md` | 追加一节，`<!-- keypoint:start -->` 包裹 |
| Gemini CLI | `~/.gemini/GEMINI.md` | 同上 |
| opencode | `~/.config/opencode/AGENTS.md` | 同上 |
| Kimi Code | `~/.kimi-code/skills/keypoint-notify/` | 技能目录（同 Claude Code） |
| 任意 | `./AGENTS.md` | 同上，跟仓库走 |

`auto`（默认）只装到**确实存在配置目录**的 CLI 上；一个都没探测到会明确
报错并给出选项，而不是默默什么都不做。重装会替换标记内的那一节，不会
把文件叠两份，也不会动你自己写的内容。

skill 内容从**服务端**拉，所以对方拿到的永远是这个服务端当前版本的
行为手册，不是随二进制发的旧副本。

### 从源码

```bash
make build          # → ./kp（CGO_ENABLED=0，真单二进制）
make install        # → ~/.local/bin/kp
make skill          # → ~/.claude/skills/（开发用，软链）
```

需要 Go 1.25+（下限由 `modernc.org/sqlite` 决定，不是本项目的代码）。

公网访问推荐 **Cloudflare Tunnel** 而不是开端口；也支持 Docker（仓库根目录有
`Dockerfile`，两阶段构建、运行镜像里没有 Go 和 C 库，四个平台的客户端已放进镜像）。
两种方式都见 [`docs/deploy-tunnel.md`](docs/deploy-tunnel.md)。

### 升级

| 部署方式 | 怎么升 |
|---|---|
| 服务端，一键安装的 | 重跑安装命令（数据和管理员不动） |
| 服务端，Docker | 重新 build / 拉镜像，重建容器（数据在卷里） |
| 服务端，源码 | `git pull && make install`，重启 `kp serve` |
| 客户端 | `curl -fsSL "<服务端>/install.sh" \| sh` 换二进制，`kp install` 重装 skill |

每个版本改了什么、哪些行为变了，见 [`CHANGELOG.md`](CHANGELOG.md)。

---

## 文档

| 文件 | 内容 |
|---|---|
| [`docs/design.md`](docs/design.md) | **为什么这么设计**：取舍、被否掉的方案、已知边界 |
| [`docs/stability.md`](docs/stability.md) | **什么是契约、什么不是** —— 在它之上写工具之前先读这份 |
| [`SECURITY.md`](SECURITY.md) | 漏洞报告，以及**部署前必须评估的设计取舍** |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | 怎么编译、评审看什么 |
| [`CHANGELOG.md`](CHANGELOG.md) | 每个版本的变化，**行为变化**单独列出 |
| [`deploy/install-server.sh`](deploy/install-server.sh) | Linux VPS 服务端一键安装（`--help` 看参数） |
| [`docs/deploy-tunnel.md`](docs/deploy-tunnel.md) | 部署：launchd、Cloudflare 隧道、Docker、备份、安全清单 |
| [`skills/keypoint-notify/SKILL.md`](skills/keypoint-notify/SKILL.md) | Skill 本体：什么时候用、怎么从会话抽任务 |
| [`skills/keypoint-notify/reference/commands.md`](skills/keypoint-notify/reference/commands.md) | 全部命令与参数 |
| [`skills/keypoint-notify/reference/api.md`](skills/keypoint-notify/reference/api.md) | HTTP API |
| [`skills/keypoint-notify/reference/recipes.md`](skills/keypoint-notify/reference/recipes.md) | 常见组合：交接、CI 集成、轮询 |

运行时还有（**服务端把自己该给人给 agent 看的东西全部对外提供**）：

| 端点 | 给谁 | 是什么 |
|---|---|---|
| `GET /api/v1/llms.txt` | 模型 | 完整 API 说明，免鉴权 |
| `GET /api/v1/schema` | 程序 | 同一份内容的 JSON（枚举 / 错误码 / 端点表） |
| `GET /skill/SKILL.md` | 模型 | 行为手册：什么时候该主动做什么 |
| `GET /skill/reference/*.md` | 模型 | 命令速查 / HTTP API / 常见配方 |
| `GET /api/v1/agent-prompt` | 模型 | **运行说明**（需 key）：你是谁 + 规则 + 订阅模式 + 运行循环，按调用者真实身份生成 |

`kp docs` 在终端里打印第一份。

**让一个新 agent 上手的两种方式**：

```bash
# 方式一：管理员生成一段可整段转发的说明（含 install.sh 一行命令、一次性 key、角色、上手命令）
kp identity create <名字> --kind agent --roles <角色>

# 方式二：把一个已有身份的运行说明直接喂给模型
curl -H "Authorization: Bearer $KP_KEY" $KP/api/v1/agent-prompt
```

看板 `/admin` 页顶部有同样的入口：一键生成「接入说明」、一键复制 Agent Prompt。

---

## 开发

```bash
make check          # vet + test
make test           # 端到端测试（起真实服务，跑全流程）
make fmt
```

```
cmd/keypoint            main
internal/cli            命令分发
internal/client         HTTP 客户端
internal/httpapi        路由 / handlers / 错误模型
internal/pack           ★ 上下文包渲染
internal/store          ★ 唯一知道 SQL 的包
internal/webhook        出站投递
internal/webui          内嵌看板
internal/model          领域类型
```

---

## 边界

诚实说明没做的：没有 schema 迁移框架、没有游标分页、没有全文索引（`LIKE` 搜索）、
没有 MCP server、单写者（SQLite + WAL）。理由和触发条件都写在
[`docs/design.md`](docs/design.md) 的"已知的边界"一节。
