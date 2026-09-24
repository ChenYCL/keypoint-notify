# 变更记录

契约与非契约的边界见 [`docs/stability.md`](docs/stability.md)。「行为变化」一节列的是
升级后你的脚本 / agent 循环可能感觉得到的地方。

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
