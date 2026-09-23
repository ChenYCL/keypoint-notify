---
name: "keypoint-notify"
description: "把当前会话里的工作变成结构化任务并上报到 Keypoint 中枢：依托你的角色抽取骨架分段（背景/目标/交付物/约束/验收/接口/文件）、拆分 side 工作面并指派给角色、生成可直接粘进另一个 Claude Code 会话的开工上下文包。当用户说 上报/建任务/记个任务/查任务/有哪些活/谁来接/交给 X 做/接任务/开工/把上下文给出去 时使用。"
version: "1.0.0"
category: "playbook"
emoji: "📍"

requires:
  bins: [kp]
  env: []

metadata:
  acepro:
    triggers:
      - 上报
      - 报一下
      - 建任务
      - 记个任务
      - 记一下这个
      - 查任务
      - 有哪些活
      - 我手上有什么
      - 交给谁
      - 谁来接
      - 指派
      - 开工
      - 接任务
      - 拿上下文
      - keypoint
      - kp
    source: "workspace"
    always: false
---

# Keypoint Notify

把"我在 Claude Code 里干的活"变成结构化、可查询、可分发给别人（或别的 agent）的任务。

**核心一句话**：你不是在写笔记，你是在给一个**还没看到这次对话的人或 agent** 准备开工材料。
你写的东西会被别人直接用 `kp task pack` 拉出来，粘进他们的会话当上下文。
所以：**具体、可执行、带验收标准**，别写"优化一下"。

---

## 如果你是被拉进来的（第一次接触这个系统）

别人给你一段话，里面有一条 `kp init --server ... --key ...` 和一个角色。
照着做就行：

```bash
kp init --server <URL> --key <kp_...> --yes    # 接入
kp whoami                                      # 确认你是谁、什么角色
kp next --wait 30 --claim                      # 等活
```

**如果你本机已经有别的 keypoint 配置**（`~/.keypoint/config.json` 指向另一台
服务器），别直接覆盖 —— 一个目录一套身份：

```bash
KEYPOINT_HOME=~/.keypoint-<你的名字> kp init --server <URL> --key <kp_...> --yes
export KEYPOINT_HOME=~/.keypoint-<你的名字>     # 之后所有 kp 命令都带上它
```

不知道这套系统怎么用？读它自己给的说明书：

```bash
kp docs                    # 打印完整 API 与用法（也可以直接把输出喂给模型）
```

**管理员那边**：`kp identity create <名字> --kind agent --roles <角色>` 会直接
打印一段可以整段转发出去的接入说明（含 URL、key、角色、上手命令）。

---

## 第 0 步：永远先确认身份

```bash
kp whoami
```

这一步给出三件事，后面全靠它：

1. **你是谁** —— 上报会归属到这个身份
2. **你的激活角色** —— 决定上报归属到哪个角色（`@backend` 还是 `@review`）
3. **未读数** —— 有未读时先看一眼，可能有人在等你

**如果 `kp whoami` 报错**（没配置 / 连不上 / key 失效）：

```bash
kp init                       # 交互式：连服务器、建身份、写 ~/.keypoint/config.json
kp init --server <url> --key <kp_...> --yes    # 非交互
```

配置写在 `~/.keypoint/config.json`。用 `KEYPOINT_HOME` 环境变量可以同时持有多套身份。

> **不要跳过这一步。** 角色决定任务的归属和 `pack` 里"交付契约"的口吻；
> 没有身份的话所有写操作都会 401。

---

## 场景一：建任务（把一个 bug / 需求 / 待办记下来）

### 先判断要不要拆 side

| 情况 | 做法 |
|---|---|
| 一个人一口气能干完（改个文案、修个 typo） | 不拆 side，只填分段 |
| 需要两种以上角色协作（后端接口 + 前端接、实现 + 审查） | 拆 side，各自指派 |
| 需要并行但同角色（A 模块 / B 模块） | 拆 side，同一个角色 |

**不要为了好看而拆。** 2-4 个 side 是常态；超过 5 个通常说明这是三个任务。

### 骨架分段：能填就填，不知道就别编

固定的 7 个 key（任务创建时自动存在，空的也能用 key 取）：

| key | 该写什么 | 不写会怎样 |
|---|---|---|
| `context` | 为什么有这件事：报障、上游变化、谁提的 | 承接方不知道边界在哪 |
| `goal` | **做完之后世界变成什么样**，一句话 | 承接方只能猜 |
| `deliverable` | 具体产出物：文件、接口、截图 | 交付物模糊 |
| `constraint` | 不能动什么：API 签名、依赖、兼容性 | 承接方顺手改坏别的东西 |
| `acceptance` | **怎么算完成**，可验证的条目 | 承接方永远不知道何时算完 |
| `interface` | 接口契约、数据结构、协议 | 前后端对不上 |
| `files` | 相关文件路径、仓库、分支 | 承接方满仓库找 |

**规则**：
- `goal` 和 `acceptance` 是底线，必须填。没有验收标准的任务等于没有任务。
- 不确定的**不要编**，写 `（待确认：<具体要问什么>）`，让对方知道这里有个洞。
- 其余自由分段随便加（中文 key 保留，空格转成 `-`）：`踩坑记录`、`复现步骤`、`数据样本`。

### 执行：走 JSON stdin（推荐）

```bash
kp task new --from-json - <<'EOF'
{
  "title": "登录页验证码倒计时在切后台后错位",
  "kind": "bug",
  "priority": "P1",
  "summary": "短信验证码 60s 倒计时切到后台再回来，剩余秒数跳变",
  "owner_role": "backend",
  "labels": ["auth", "mobile"],
  "links": [{"kind": "repo", "url": "https://github.com/acme/web"}],
  "segments": {
    "context": "iOS Safari 上 visibilitychange 触发时机与 Android 不同。线上报障 3 起，集中在 iOS 17。",
    "goal": "切后台 ≥10s 后回到前台，倒计时显示与真实剩余时间一致。",
    "deliverable": "- 修复后的 countdown hook\n- 回归测试说明",
    "constraint": "不能改 public API 签名；不能引入新依赖。",
    "acceptance": "1. 切后台 60s 回来秒数正确\n2. iOS/Android 行为一致\n3. 单测覆盖 visibilitychange 分支",
    "interface": "GET /api/v1/sms/cooldown → {\"remaining_ms\": number}",
    "files": "src/hooks/useCountdown.ts\nsrc/pages/Login/SmsCode.tsx",
    "踩坑记录": "iOS 上 pagehide 比 visibilitychange 更可靠"
  },
  "sides": [
    {"key": "api", "title": "后端冷却接口", "assignee_role": "backend",
     "segments": {"接口契约": "返回服务端权威 remaining_ms，前端不再自己算"}},
    {"key": "ui", "title": "前端倒计时", "assignee_role": "frontend", "deps": ["api"],
     "segments": {"组件位置": "src/hooks/useCountdown.ts"}},
    {"key": "review", "title": "回归审查", "assignee_role": "review", "deps": ["api", "ui"]}
  ],
  "notify": ["@review"]
}
EOF
```

`--from-json` 是**严格模式**：字段名拼错会直接报错（不会静默丢掉）。这是好事，改掉重来。

### 或者用 flag（短任务更快）

```bash
kp task new --title "..." --kind bug --priority P1 \
  --goal "..." --acceptance "..." --role backend
```

### 指派之前先查谁能接

```bash
kp role ls --holders        # 角色 → 谁持有
kp identity ls --names      # 可指派的身份名
```

**别凭空指派一个没人持有的角色** —— 通知会发给"持有该角色的人"，没人持有就等于没人收到。
指派前如果不确定，先查 `kp role ls --holders`。

---

## 场景二：上报（告诉别人进展 / 卡住了 / 做完了）

```bash
kp report KP-12 --side ui --type blocker -m "api 的 remaining_ms 还没上线，前端只能先本地算" --mention @backend
```

或者结构化（推荐，尤其要带分段或附件时）：

```bash
kp report KP-12 --from-json - <<'EOF'
{
  "type": "blocker",
  "side_key": "ui",
  "body": "被 api 工作面挡住：冷却接口还没上线。",
  "segments": [
    {"key": "阻塞点", "title": "阻塞点", "body": "需要 GET /api/v1/sms/cooldown 先可用"},
    {"key": "临时方案", "title": "临时方案", "body": "本地算，等接口上线后切回"}
  ],
  "mentions": ["@backend"],
  "status": "blocked"
}
EOF
```

### type 怎么选

| type | 什么时候用 | 副作用 |
|---|---|---|
| `progress` | 阶段进展 | side 从 todo → doing |
| `blocker` | **卡住了，需要别人做点什么** | side 自动变 blocked |
| `question` | 需要确认一个点才能继续 | 无 |
| `decision` | 我们做了个选择，记一下理由 | 无 |
| `handoff` | 我把这块交出去了 | 无 |
| `result` | 干完了 | side 从 todo → doing |

### 上报的三条纪律

1. **别静默结束**。干完活不吭声，别人以为你没动。收工就 `--type result`。
2. **blocker 要说清"需要什么"**，不是"我很难"。`--mention` 上能解决的人或角色。
3. **带证据**。截图、日志、复现步骤。`--attach` 或先 `kp attach`：
   ```bash
   kp report KP-12 --side ui --type result -m "修好了" --attach /tmp/before.png --attach /tmp/after.png
   ```

### 附件

```bash
kp attach shot.png                      # 返回 id / url / markdown 片段
kp attach shot.png --task KP-12         # 直接挂任务上
```

返回的 markdown 片段可以贴进分段正文：`![shot.png](/api/v1/files/fil_xxx)`。
**图片是 agent 能看懂的**：`pack` 里会渲染成 markdown 图片链接，承接方拉过去就能看图。

---

## 场景三：接活 —— 协作循环（最重要的一条）

**当你是"被派活的一方"，不要自己拼「拉事件 → 判断是不是我的 → 取任务 → 取 pack」。
一条命令就够：**

```bash
kp next --wait 30 --claim
```

它回答三件事：**有没有属于我的活 / 为什么是我 / 开工需要的全部上下文**。

| 参数 | 作用 |
|---|---|
| `--wait 30` | 没有活时在服务端挂起最多 30 秒（长轮询）。**用它，不要 `sleep 5` 循环轮询** |
| `--claim` | 拿到属于我的工作面就原子认领；同角色的多个会话只有一个能抢到 |
| `--task KP-12 --side ui` | 收窄到某个任务/工作面 |
| `--exclude KP-14` | 跳过某个任务。判定"这不归我接"时用它，否则会被永远推同一件 |
| `--json` | 结构化：`{work:{reason,explanation,task,side,dependents}, cursor, claimed, pack}` |
| `--cursor-only` | 只输出游标（脚本里取用） |

返回的第一段就是「为什么是你」，`reason` 决定你怎么做：

| reason | 含义 | 你该做什么 |
|---|---|---|
| `mention` | 有人在某个上报里 @ 了我 | **回应**。这不是让你接手他的工作面 |
| `unblocked` | 我的工作面依赖刚完成，解封了 | 接手，开工 |
| `assigned` | 指派给我角色的工作面，还没人认领 | 接手，开工 |
| `owned` | 我是任务负责人，任务有新动静 | 看一眼，决定要不要派人 |

后面直接跟完整开工包（含交付契约），不用再单独取。

**游标不用自己管。** 省略 `--since` 时服务端用你上次调用存下的游标，
所以循环不会反复收到同一条提及；要回放才显式传 `--since`。

### 三个会话接力的完整样子

```bash
# ── 会话 A（角色 backend）──
kp next --wait 30 --claim          # 醒来：KP-3 的 api 工作面派给我了
# ……改代码……
kp report KP-3 --side api --type result -m "冷却接口上线，返回服务端权威 remaining_ms"
kp task side assign KP-3 api --status done
# ↑ 这一句就是交棒。不用通知谁 —— 系统会自己唤醒等它的人

# ── 会话 B（角色 frontend，此刻正阻塞在 kp next --wait）──
kp next --wait 30 --claim          # 被 api 的完成叫醒：reason=unblocked
# ……改代码……
kp report KP-3 --side ui --type result -m "改用 pagehide 重置倒计时"
kp task side assign KP-3 ui --status done

# ── 会话 C（角色 review）──
kp next --wait 30 --claim          # 接力，不会被跳过
kp report KP-3 --side review --type result -m "边界条件与回归覆盖通过"
kp task side assign KP-3 review --status done
kp task status KP-3 done
```

**为什么这条链能自走**：把一个 side 置成 `done`，服务端会在同一个事务里
找出依赖它的下游 side，解封并发出 `side.unblocked` —— 下游会话的长轮询
立刻被唤醒。交棒是副作用，不是额外步骤。

### 两件系统会自动做的事 —— 别重复做

| 自动发生 | 你该怎么做 |
|---|---|
| side 完成后，依赖它的下游 side 自动解封 + 发 `side.unblocked` | 什么都不用做，交棒就是副作用 |
| **最后一个 side 完成时，任务自动变 `done`**（事件 `reason=all_sides_done`） | **不要**再手动 `kp task status <code> done`，会多一条无意义的状态变更 |

第二条也意味着一件事：**看到任务还是 `doing` 但所有 side 都 `done`**，
那是修复前的数据，正常手动关掉即可；新数据不会再出现这种状态。

### 关于"等活"的两种模式，别选错

| 模式 | 谁在等 | 用在哪 |
|---|---|---|
| **会话自己阻塞**（`kp next --wait 30`） | 这个 Claude 会话 | 你在被人派活的场景 —— **默认用这个** |
| **外部 loop**（`kp loop`） | 一个独立终端进程 | 人守着看、调试、或要把包喂给别的进程（`--run`） |

不要用 `sleep 5` + `kp task list` 自己轮询 —— 那是每秒一次请求，而
`kp next --wait` 是每次唤醒一次请求。实测在真实 Claude 会话里阻塞 25 秒
不会被工具超时打断，所以 `--wait 30` 是安全的。

想看着它跑（人肉观察 / 调试）：

```bash
kp loop                            # 一直等活，拿到就把包打出来
kp loop --max 3                    # 处理三条退出
kp loop --run 'claude -p "$KP_PACK"'   # 把包喂给另一个会话
```

### 不用循环时，直接拿上下文

```bash
kp board                          # 我手上有什么（工作面 + 我负责的任务 + 未读）
kp task pack KP-12 --side ui      # 指定任务/工作面的开工包
```

### 抢同一个工作面时

```bash
kp claim KP-12 ui                 # 原子认领；已被别人拿走会返回非零退出码
```

**别在没认领的情况下直接开干**。两个同角色的会话同时看到同一个 side 是常态，
`--claim` / `kp claim` 是让只有一个真正拿到它的办法。

`pack` 输出一份自包含的 markdown：任务头、分段索引、工作面表、所有分段正文、
最近上报、附件链接，**以及"交付契约"**——告诉承接方做完该怎么上报。

把人话变成开工动作时，**直接把 pack 的内容给用户 / 粘进新会话**，不要自己复述摘要。
摘要会丢信息，pack 不会。

单段复制（这是"可复制的最小单位"）：

```bash
kp task seg KP-12 acceptance            # 纯文本，可直接 pbcopy
kp task seg KP-12 acceptance --prompt   # 包一层"请基于它工作"的上下文
```

---

## 场景四：查询

```bash
kp task list --assigned me                 # 指派给我的
kp task list --role backend --status doing,blocked
kp task list --q 验证码 --since 7d
kp task list --group                       # 按状态分组（看板视图）
kp inbox --unread                          # 有谁在等我
kp task show KP-12 --side ui               # 任务全貌
```

**省 token 的开关**：`--json` 给程序，`--lite` 砍掉分段正文，`--limit N` 限量。

---

## 什么时候主动用这个 skill

**要主动发起，不要等用户说全**：

- 用户说"记一下"、"别忘了"、"这个要改" → 建任务
- 你在会话里发现了一个**不属于当前任务**的 bug / 待办 → 建任务（别顺手修）
- 你干完了一件有交付物的事 → 上报 `result`
- 你被某个外部因素卡住 → 上报 `blocker` + mention
- 用户说"这个交给 X" → 建 side 或 `kp task side assign`
- 用户给出**一个需要多角色协作的需求** → 拆 side 并指派
- 会话快结束了而工作没完 → 上报 `handoff`，把状态写清楚
- **用户说"盯着 / 等活 / 有活就干 / 配合别人"** → `kp next --wait` 循环，不要 sleep 轮询
- **你是被派活的一方** → 先 `kp next --claim`，别自己翻任务列表猜哪个该做

**不要用的场景**：纯问答、一次性查询、用户明确说"不用记"。

---

## 详细参考

- `reference/commands.md` —— 全部命令与参数
- `reference/api.md` —— HTTP API（给非 Claude-Code 的 agent / 脚本用）
- `reference/recipes.md` —— 常见组合：交接、多人协作、CI 集成、批量上报
