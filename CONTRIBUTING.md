# 贡献指南

先谢谢你有兴趣。这个项目不大，规则也不多，但有几条是硬的。

---

## 最快的上手方式

```bash
git clone <repo> && cd keypoint-notify
make build      # → ./kp
make check      # vet + test，提交前必须过
make run        # 起一个本地服务端，数据在 ./data
```

一个能跑的最小闭环（另一个终端）：

```bash
./kp init --server http://127.0.0.1:8787
./kp task new --title "试试" --goal "跑通" --acceptance "能看到任务"
./kp next --wait 5 --claim
```

---

## 提交前必须过的

```bash
make check      # go vet + go test ./...
gofmt -l .      # 必须为空
```

CI 会跑同样的东西（外加 `-race` 和一次真实二进制的冒烟测试）。本地过不了的，
CI 也过不了。

**改了 skill 内容的话**，还要跑：

```bash
make sync-skill   # skills/keypoint-notify/ → internal/skill/assets/
```

`internal/skill` 里有一份内嵌副本（`go:embed` 到不了包外），有个测试会锁住
两份不让它们漂移。忘了同步，测试会告诉你。

---

## 改动会被怎么审

评审主要看四件事，按重要性排：

**1. 契约有没有被破坏。** 见 [`docs/stability.md`](stability.md)。改 JSON 字段名、
改枚举含义、改错误码 —— 这些是破坏性的，通常会被要求换一种做法。

**2. 错误是不是可恢复的。** 这个项目对 LLM 友好是硬要求，而 LLM 靠错误里的
线索自我纠正。所以：

```go
// 不要
NewError(400, "bad_request", "invalid status")

// 要
NewError(400, "bad_status", "未知状态 %q", sv).
    WithOptions("status", allStatuses).
    WithSuggest(nearest(sv, allStatuses))
```

新加的错误，如果调用方**能做什么**来修复，就必须写进 `hint` / `did_you_mean` /
`options`。

**3. 有没有测试证明它能用。** 不是覆盖率数字 —— 是「这个改动要防止的
那个具体失败，有没有一个测试会在它复发时红」。

**4. 注释在解释为什么。** 代码说做什么，注释说为什么这么做（以及试过什么
不行）。看仓库里的现有注释，照着写。

---

## 代码风格

- **标准库优先。** 目前只有两个直接依赖（`modernc.org/sqlite` 及其传递依赖），
  保持这样。想加依赖的话，在 PR 里说明为什么标准库做不到。
- **`internal/store` 是唯一写 SQL 的包。** 上层只说 model 类型。
- **错误往上抛，不要就地打印。** CLI 层负责呈现。
- **中文注释和中文用户可见文案**是这个项目的现状（目标用户包含中文团队）。
  英文注释没问题，但同一个文件里别混着来。
- 文件名体现职责：`side.go` 管工作面，`next.go` 管「轮到谁了」。

---

## 加一个新功能时

问自己三个问题，答案写进 PR 描述：

1. **一个从没见过这个项目的 agent，能不能靠 `/api/v1/llms.txt` 和
   `/skill/SKILL.md` 自己学会用它？** 如果新功能需要看源码才知道怎么调，
   那是因为文档没写全。
2. **多会话并发时它会怎样？** 这个系统里同时跑着多个 agent 是常态。
   任何「先查再改」的地方都要想想能不能变成原子的。
3. **系统的自动化会不会和它打架？** 已知会自动发生的事：依赖解封、
   最后一面完成时任务收尾。别做重复的事。

---

## 报告问题

- **安全漏洞**：不要开公开 issue，看 [`SECURITY.md`](../SECURITY.md)。
- **行为不符合预期**：带上 `kp version`、你跑的命令、实际输出、期望输出。
  如果服务端是自己的，附上 `/api/v1/health` 的输出（不含 key）。
- **想要的功能**：先说清楚你想做什么事、现在的做法哪里别扭。功能请求里
  描述场景比描述方案有用得多。

---

## 协议

贡献的内容按 [Apache-2.0](../LICENSE) 授权。提交 PR 即表示你同意这一点，
并且你有权这么做。
