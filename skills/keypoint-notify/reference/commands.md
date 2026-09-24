# kp 命令参考

全局参数可以放在命令前或命令后：

```
--json            机器可读输出（agent / 脚本用）
--server URL      覆盖服务端（或 KEYPOINT_SERVER）
--key kp_...      覆盖 API key（或 KEYPOINT_API_KEY）
--role <角色>     本次调用以哪个角色归属（或 KEYPOINT_ROLE）
```

参数顺序自由：`kp task pack KP-12 --side ui` 和 `kp task pack --side ui KP-12` 等价。

---

## 工作流短命令（日常就用这几条）

```
kp next --claim                          接一件活（打印开工包并认领）
kp report KP-12 --side ui -m "…"         中途上报，--type blocker|question|decision
kp done KP-12 [ui] -m "…"                完结：上报 result + 面置 done；只有一个面在你名下时可省略面
kp release KP-12 ui [-m "原因"]          放手：只清认领人，角色保留，同角色的人能接
kp cancel KP-12 -m "原因"                取消：记 decision 并归档（恢复：kp task status KP-12 inbox）
kp wait [--timeout 600] [--task KP-12]   有新活/新通知就退出；第一行 KP-WAIT: work|notification|timeout
kp watch|unwatch KP-12                   订阅/退订一个任务的动态（进收件箱）
kp loop --agent claude|kimi [--max N]    无人值守：每件活起一个新会话，做完自动 kp done
kp install [--target claude,kimi,…]      装行为手册 + /kp-xxx 斜杠命令
```

## 调研 / 讨论

```
kp research                                   待定的问题（所有任务）+ 全部调研
kp research new "问题" --option "A：…" --option "B：…" [--context "…"] [--role R]
kp research note   KP-12 -m "论点 / 发现（带证据）"      → finding 上报
kp research ask    KP-12 -m "拿不准的点" --mention @x    → question，进「待定」
kp research decide KP-12 -m "定论：…" [--close]          → decision，之前的问题都算定了
```

## init / config

```
kp init                                   连本机 127.0.0.1:8787，自动认领第一个身份
kp init --server https://kp.example.com    连远程
kp init --server URL --key kp_xxx --yes   纯非交互（CI / agent）
kp init --name ci-runner --kind agent --roles backend,qa

kp config show                            打印配置（key 打码）
kp config get server
kp config set server https://new-host
kp config path                            配置文件路径
kp config env                             打印可 export 的环境变量
```

配置位置 `~/.keypoint/config.json`（0600）。`KEYPOINT_HOME=<dir>` 可换目录，
一台机器同时持有多套身份。

---

## 服务端

```
kp serve                                  127.0.0.1:8787，数据 ./data
kp serve --addr 0.0.0.0:8787              对外监听
kp serve --data ~/.keypoint/data
```

首次启动建库、灌内置角色。之后 `kp init` 建身份。

---

## 视图

```
kp whoami                                 身份、角色、未读、能力、入口提示
kp board                                  我手上的工作面 + 我负责的任务 + 下一步建议
kp docs                                   打印 /api/v1/llms.txt（完整 API 说明）
```

## 协作循环

```
kp next [--wait 30] [--claim] [--since <游标>] [--task KP-12] [--side ui]
        [--max-chars N] [--reports N] [--json] [--cursor-only]

kp loop [--wait 20] [--max N] [--run '<命令>'] [--claim] [--task KP-12] [--side ui]
        [--interval N]

kp claim <code> <side>                    原子认领一个工作面；已被拿走则退出码非 0
```

`kp next` 的 `reason`：`mention`（有人 @ 我，等着回应）· `unblocked`（依赖刚完成，
解封）· `assigned`（派给我角色的，还没人认领）· `owned`（我是任务负责人）。

游标由服务端按身份记忆：省略 `--since` 就用上次存下的，循环不会重复收到同一条。
要回放才显式传。

`--wait` 只为新东西醒来：已在你名下、之后没变化的工作面（以及你负责、之后没动静的
任务）不会让它立刻返回。不带 `--wait` 的 `kp next` 仍会列出它们（新会话接着干用这个）。
被 blocker 置为 `blocked` 的工作面不会被当成活派出去，拿到回答后自己改回 `doing`。

`kp loop --run '<cmd>'` 会把开工包同时放进 `$KP_PACK` 和 stdin。

**系统自动做的两件事**：side 完成 → 下游自动解封并唤醒；**最后一个 side
完成 → 任务自动变 done**（`task.status_changed` 的 `payload.reason`
为 `all_sides_done`）。别手动重复。


---

## task

```
kp task list [--status inbox,doing] [--kind bug] [--priority P1]
             [--role backend] [--assigned me] [--q 关键词]
             [--since 7d] [--limit 50] [--archived]
             [--group]        按状态分组
             [--lite]         砍掉分段正文（省 token）

kp task show <code> [--side <key>] [--reports N]
kp task pack <code> [--side <key>] [--max-chars N] [--reports N]
                    [--format md|json] [--empty] [--out FILE]
kp task seg <code> <key> [--prompt] [--format json]
kp task seg set <code> <key> [-m 正文 | --file <path> | --file -]
                             [--title T] [--side K] [--append] [--format md|text|code]

kp task side ls <code> [--assignable]
kp task side add <code> <key> [--title T] [--role R] [--identity I]
                              [--deps a,b] [--repo R] [--branch B]
kp task side assign <code> <key> [--role R] [--identity I] [--status S]
                                 [--deps a,b] [--unassign]
kp task side rm <code> <key>

kp task new --title "..." [--summary S] [--kind bug|feature|chore|research|review|incident]
            [--priority P0..P3] [--status S] [--role R] [--identity I]
            [--label a,b]
            [--context T] [--goal T] [--deliverable T] [--constraint T]
            [--acceptance T] [--interface T] [--files T]
            [--seg key=正文] [--repo URL]
            [--from-json -|<path>] [--dry-run]

kp task status <code> <新状态>
kp task edit <code> [--title T] [--summary S] [--kind K] [--priority P] [--role R] [--label a,b]
kp task rm <code>                          删任务（需 admin）
```

### 固定骨架分段 key

| key | 标题 |
|---|---|
| `context` | 背景 |
| `goal` | 目标 |
| `deliverable` | 交付物 |
| `constraint` | 约束 |
| `acceptance` | 验收标准 |
| `interface` | 接口/契约 |
| `files` | 相关文件 |

这 7 个在任务创建时自动存在，永远可以用 key 取（空的就是空的）。
自由分段自起 key；中文会保留，空格和标点转成 `-`。

### 状态

```
任务: inbox | ready | doing | blocked | review | done | archived
工作面: todo | doing | blocked | done
```

任务变 `done` / `archived` 会把所有未完成工作面一起置为 `done`。

参数顺序自由：`kp task pack KP-12 --side ui` 和 `kp task pack --side ui KP-12` 等价。

---

## report

```
kp report <code> -m "正文" [--type T] [--side K] [--status S]
                 [--priority P] [--mention @a,b] [--attach f1,f2] [--role R]
kp report <code> --from-json -           从 stdin 读结构化 JSON
kp report <code> --dry-run               只打印将发送的 JSON
echo "正文" | kp report <code>           管道喂 stdin（无 -m 时自动读）
```

`type`：`progress` | `blocker` | `decision` | `handoff` | `result` | `question`

**副作用**（可预期，别重复做）：
- `blocker` + `--side` → 该 side 自动变 `blocked`
- `progress` / `result` + 该 side 还是 `todo` → 自动变 `doing`
- `--status` → 同时改任务本身的状态
- `--mention` → 被提到的人/角色收到站内未读

---

## inbox / attach / events

```
kp inbox [--unread] [--limit N]
kp inbox --read-all
kp inbox --read ntf_x,ntf_y

kp attach <file>... [--task KP-12] [--side ui]

kp events [--since <cursor>] [--type report] [--task KP-12]
          [--limit N] [--backlog] [--follow]
```

`kp events` 返回的 `cursor` 下次原样传给 `--since`。

---

## role / identity / hook

```
kp role ls [--holders] [--keys]
kp role add <key> [--name 显示名] [--desc 说明]      （需 admin）
kp role rm <key>                          内置角色不可删

kp identity ls [--names]
kp identity create <name> [--kind human|agent] [--roles a,b] [--active R]
                                          打印一段可整段转发的接入说明（含一次性 key）
kp identity invite <name>                 重印接入说明（不含 key）
kp identity set-roles <name> a,b,c        改绑角色，key 不变（需 admin）
kp identity use <name> [role]             切换本机身份/激活角色
kp identity rotate <name>                 轮换 key，旧的立即失效
kp identity disable <name> / enable <name>

kp hook ls
kp hook add <url> [--secret S] [--events a,b] [--disable]   （需 admin）
kp hook rm <id>
```

---

## 退出码

| 码 | 含义 |
|---|---|
| 0 | 成功 |
| 1 | 一般错误（服务端返回的业务错误） |
| 2 | 用法错误 |
| 3 | 认证/授权失败（401/403） |
| 4 | 本地没有配置（需要 `kp init`） |
| 5 | 连不上服务端 |
