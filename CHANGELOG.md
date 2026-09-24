# 变更记录

契约与非契约的边界见 [`docs/stability.md`](docs/stability.md)。「行为变化」一节列的是
升级后你的脚本 / agent 循环可能感觉得到的地方。

## v0.1.3 — 2026-09-24

### 新增

- **调研**：看板新增「调研」页（`/research`），CLI 新增 `kp research`（`new` / `note` / `ask` /
  `decide`），会话里 `/kp-research`。调研就是 kind=research 的任务：问题写在 goal，方案各一段，
  论点是新的上报类型 `finding`，拿不准的是 `question`，定下来是 `decision`。
  「待定」列出**所有**未完成任务里还没有定论的 question —— 规则是同一任务里之后出现
  decision 就算定了。服务端新增 `GET /api/v1/research`。
- 上报类型新增 `finding`（论点 / 发现）。按契约，枚举只增不改义；读到不认识的类型原样显示即可。

## v0.1.2 — 2026-09-24

### 新增

- **斜杠命令**：除了行为手册 `keypoint-notify`，新增七个命令 skill —— `/kp-new` 建任务、
  `/kp-next` 接活、`/kp-report` 上报、`/kp-done` 完结、`/kp-cancel` 放手/取消/停自动、
  `/kp-loop` 定时自动处理、`/kp-watch` 订阅（来了就开始）。Claude Code 里是 `/kp-xxx`，
  Kimi Code 里是 `/skill:kp-xxx`；`kp install` 一起装。只有 `/kp-next`、`/kp-watch` 允许模型
  自动调用（定时触发要用），其余只在你敲的时候执行。
- **工作流短命令**：`kp done`（上报 result + 面置完成，一步完结）、`kp release`（只清认领人，
  角色保留）、`kp cancel`（记原因并归档）、`kp wait`（有新活/新通知才退出，第一行
  `KP-WAIT: work|notification|timeout`）、`kp watch` / `kp unwatch`。
- **`kp loop --agent claude|kimi`**：无人值守的预设。每件活起一个新会话，带上一段短提示
  （mention 只回答、做完用 kp done、只做一件），不用再手写一长串 `--run`。
- 服务端：`POST/DELETE /api/v1/tasks/{code}/watch`；`/me/next?peek=1` 不推进游标；
  `/skill/index.json` 与 `/skill/<名字>/…` 发放全部 skill（旧路径 `/skill/SKILL.md` 不变）。
- 看板首页改版：三步上手、五种日常自动化场景、斜杠命令对照表、更短的标准流程和最佳实践。
- skill 描述加上「等活 / 轮到我 / 循环接活」，循环模式下能被自动调起。

### 修复

- **`kp install --target kimi` 装到了错的地方**：原来写 `~/.kimi/AGENTS.md`（已归档的旧
  kimi-cli 的目录）。现在的 Kimi Code 用 `~/.kimi-code/`，原生认 SKILL.md，改为装技能目录。
- **删掉的任务不再在收件箱里留死链**：删除任务时一并清掉指向它的通知，只留「任务已删除」
  那一条且不可点；看板打开不存在的任务显示「已被删除」而不是一直「加载中…」。

## v0.1.1 — 2026-09-24

安全修复。建议所有部署升级。

### 行为变化

- **增删角色、配/删 webhook、删任务现在需要 admin**，非 admin 调用返回 403
  `forbidden`（带 hint；删任务的 hint 指向「归档」）。SECURITY.md 一直写着这些是 admin
  动作，但服务端没查 —— 发给 agent 的非 admin key 也能删别人的任务、把 webhook 指到
  外部地址。脚本里用非 admin key 做这几件事的，换成管理员的 key。

## v0.1.0 — 2026-09-24

第一个带预编译二进制的版本。问题大多是在一个多角色沙盒里跑出来的：管理员 + 五个各自
`install.sh` 装好的角色 + 真 Claude Code 会话按工作面依赖接力改一个示例仓库。

### 新增

- **服务端一键安装（Linux VPS）**：`deploy/install-server.sh`。下载 Release 并校验
  SHA256，四个平台的客户端放在旁边，systemd 服务（非 root、开机自启），`--domain` 时
  Caddy 自动 HTTPS，认领管理员（`kp-admin`）。重跑 = 升级，`--uninstall` 卸载（数据保留）。
- **发版**：推 `v*` tag → `.github/workflows/release.yml` 发四个平台的二进制 + `SHA256SUMS`。
- **config `public_url`**：接入说明里给别人的地址。管理员在服务端本机连 127.0.0.1，
  发出去的说明用它（`kp config set public_url https://kp.example.com`）。
- **接入说明第一行是 `curl …/install.sh?key=… | sh`**：收到的人多半还没装 kp。

### 行为变化（升级前看一眼）

- **`kp next --wait N` 只为新东西醒来。** 已在你名下、之后没变化的工作面，和你负责、
  之后没动静的任务，不再让它立刻返回 —— 以前手上有未完成的面时它从不等待。
  想看手上已认领的：不带 `--wait` 的 `kp next`，或 `kp board`。
- **`reason=unblocked` 现在名副其实**：这个面有依赖，且都已完成。以前依赖完成时给的是
  `assigned`，`unblocked` 反而只出现在被 blocker 挡住的面上。按 reason 分支的脚本注意。
- **被 blocker 上报置为 `blocked` 的面不再被派出去。** 拿到回答后自己改回：
  `kp task side assign KP-12 ui --status doing`。
- **严格模式的 unknown field 会说该放哪**（`did_you_mean: segments.acceptance`、
  `options` 列出可用字段）。错误码仍是 `invalid_json`。

### 修复

- 全局参数放在命令后面不生效：`whoami` / `board` / `claim` / `task status|rm|side rm` /
  `role rm` / `identity use|rotate|invite|disable|enable` / `hook ls|rm`
- `kp --json identity rotate <自己>` 没保存新 key，本机被锁在外面
- `kp loop` 每轮请求两次，带 claim 时认领两个面、只处理一个
- `kp task new --help` 被总帮助拦截，拿不到 `--from-json` 的形状
- `kp docs`（llms.txt）没写建任务的请求体
- install.sh：Apple Silicon 从 Rosetta 下的 shell 运行时拿到 amd64
- 服务端旁边没有 `bin/`（Docker 镜像、`make install`）时 install.sh 对所有人 404；
  Docker 镜像现在带上四个平台的客户端
- Makefile：`dist` / `release-bin` 没声明 `.PHONY`，`dist/` 存在后改了代码也不重编
- CI 冒烟测试从来没通过过（grep 对不上缩进的 JSON）
- SKILL.md：在 Claude Code 里用文件传 JSON，heredoc 会被它的 Bash 安全检查拦下

### 升级

| 部署方式 | 怎么升 |
|---|---|
| 服务端，一键安装的 | 重跑安装命令 |
| 服务端，Docker | 重新 `docker build`（或拉新镜像）后重建容器，数据在卷里 |
| 服务端，源码 + launchd | `git pull && make install`，然后 `launchctl kickstart -k gui/$(id -u)/com.local.keypoint` |
| 客户端 | `curl -fsSL "<服务端>/install.sh" \| sh`（不带 key 只换二进制），skill 用 `kp install` 重装 |
| 从源码的开发机 | `git pull && make install`；`make skill` 链接的 skill 随仓库更新 |

新客户端连旧服务端可以用；`--wait` / `unblocked` / blocked 的新语义在**服务端**升级后才生效。
