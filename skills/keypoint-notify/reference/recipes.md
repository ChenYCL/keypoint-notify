# 常见配方

## 1. 交接：把一个任务交给另一个 Claude Code 会话

**发起方**（当前会话）：

```bash
kp task new --from-json - <<'EOF'
{ "title": "...", "goal": "...", "acceptance": "...",
  "sides": [{"key":"ui","assignee_role":"frontend","deps":["api"]}] }
EOF
# → ✓ 已创建 KP-7
```

**承接方**（另一个会话，可能是别的机器 / 别的人）：

```bash
kp init --server https://kp.example.com --key kp_xxx      # 一次性
kp task pack KP-7 --side ui | pbcopy                       # 粘进 Claude Code
```

或者把 `pack` 的输出**直接贴进对话**——它自包含，不需要承接方再查任何东西。

**关键**：pack 末尾有"交付契约"，承接方做完自然会 `kp report`。

---

## 2. 从一次会话里抽出多个任务

用户在一次对话里提了三件事，别混成一个任务：

```bash
# 逐个建，各自独立分 side
kp task new --title "A" --goal "..." --acceptance "..."
kp task new --title "B" --goal "..." --acceptance "..."
kp task new --title "C" --goal "..." --acceptance "..."
```

判断标准：**如果两件事可以分别验收，就是两个任务。**

---

## 3. 拆 side 并指派

```bash
# 先看谁能接
kp role ls --holders

# 加两个并行工作面，ui 依赖 api
kp task side add KP-7 api  --title "后端接口" --role backend
kp task side add KP-7 ui   --title "前端接入" --role frontend --deps api
kp task side add KP-7 test --title "端到端验证" --role qa --deps api,ui

# 后来想改派
kp task side assign KP-7 ui --role frontend --identity alice --status doing

# 接口先好了
kp task side assign KP-7 api --status done
```

**注意**：`--identity` 的名字必须真实存在（先 `kp identity ls --names` 查）。
只给 `--role` 时，通知会发给所有持有该角色的人。

---

## 4. 保住"干活的人是谁"

一个身份可以持有多个角色，用 `--role` 或 `role` 字段指定这一次写入归属给谁：

```bash
kp --role review report KP-7 --type decision -m "这个改法有风险，理由：..."
```

角色可以随时改绑而不动 key：

```bash
kp identity set-roles alice backend,review     # key 不变
kp identity set-roles alice review             # 只留 review
```

本机要切换"我以什么身份工作"：

```bash
kp identity use alice review
```

---

## 5. 带截图的阻塞上报

```bash
# 上传并拿到 id
kp attach /tmp/before.png /tmp/after.png --task KP-7

# 结构化上报，把附件挂上去
kp report KP-7 --from-json - <<'EOF'
{
  "type": "blocker",
  "side_key": "ui",
  "body": "iOS 17 上可见性事件不触发，需要改成 pagehide。附前后对比。",
  "segments": [
    {"key": "复现", "title": "复现步骤", "body": "1. 登录页获得验证码\n2. 切到后台 30s\n3. 切回，秒数跳变"},
    {"key": "怀疑", "title": "怀疑点", "body": "visibilitychange 在 iOS 17 的 PWA 场景下不可靠"}
  ],
  "mentions": ["@frontend"],
  "attachments": ["fil_xxx", "fil_yyy"],
  "status": "blocked"
}
EOF
```

`pack` 里会渲染成 markdown 图片链接——承接方拉上下文时能直接看图。

---

## 6. Agent 轮询等回应

```bash
# 首次同步到位点
CURSOR=$(kp events --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["cursor"])')

# 之后循环
while true; do
  RESP=$(kp events --since "$CURSOR" --type report,task.status_changed --json)
  echo "$RESP" | python3 -m json.tool
  CURSOR=$(echo "$RESP" | python3 -c 'import sys,json;print(json.load(sys.stdin)["cursor"])')
  sleep 15
done
```

SSE 版本：

```bash
curl -N -H "Authorization: Bearer $KP_KEY" \
  "$KP/api/v1/stream?since=$CURSOR&type=report"
```

---

## 7. CI 集成：构建失败自动建任务

```bash
if ! make test; then
  kp task new --title "CI 失败：$(git log -1 --pretty=%s)" \
    --kind bug --priority P1 --role backend --label ci \
    --context "分支 $(git rev-parse --abbrev-ref HEAD) 的构建失败" \
    --goal "让 main 恢复绿色" \
    --acceptance "CI 通过；同一类失败有回归覆盖" \
    --files "$(git log -1 --name-only --pretty=format: | head -20)"
  kp report "$(kp task list --label ci --limit 1 --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["tasks"][0]["code"])')" \
    --type blocker -m "构建日志见附件" --attach build.log --mention @ops
fi
```

---

## 8. 接活并开工（承接方视角）

```bash
kp whoami                       # 我是谁、什么角色
kp board                        # 我手上有什么
kp inbox --unread               # 有谁在等我

kp task pack KP-7 --side ui     # 拿上下文
# …… 干活 ……
kp report KP-7 --side ui --type result -m "改完了：useCountdown 改用 pagehide。
验证：iOS 17 真机切后台 60s 回来秒数正确；新增单测 3 条全绿。
遗留：PWA 场景没覆盖，另开了任务。" --attach /tmp/after.png
```

**收工必须上报。** 静默结束等于让别人干等。

---

## 9. 把上报推到 Slack / 飞书 / n8n

```bash
kp hook add https://hooks.example.com/keypoint \
  --secret "$(openssl rand -hex 32)" \
  --events report.created,side.assigned,task.status_changed
```

收到的东西：

```json
{
  "id": 42, "type": "report.created",
  "actor_name": "alice", "task_code": "KP-7", "side_id": "sid_...",
  "payload": {"type":"blocker","side":"ui","mentions":["backend"]},
  "ui_url": "/t/KP-7"
}
```

验签（Node）：

```js
const crypto = require("crypto");
const expected = "sha256=" + crypto.createHmac("sha256", secret).update(rawBody).digest("hex");
if (!crypto.timingSafeEqual(Buffer.from(expected), Buffer.from(req.headers["x-kp-signature"]))) {
  return res.status(401).end();
}
```

---

## 10. 给非 Claude-Code 的 agent 用（纯 HTTP）

```bash
KP=https://kp.example.com
KEY=kp_xxxxx

# 1. 我是谁
curl -s -H "Authorization: Bearer $KEY" $KP/api/v1/whoami | jq .

# 2. 一次调用说明怎么用这个 API
curl -s -H "Authorization: Bearer $KEY" $KP/api/v1/llms.txt

# 3. 拿上下文
curl -s -H "Authorization: Bearer $KEY" "$KP/api/v1/tasks/KP-7/pack?side=ui&format=md"

# 4. 上报
curl -s -X POST -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"type":"result","side_key":"ui","body":"完成"}' \
  $KP/api/v1/tasks/KP-7/reports | jq .
```

**把 `/api/v1/llms.txt` 喂给模型**，它就懂了——不用你写 prompt。

---

## 11. 两台机器 / 两个人协作

服务器放一台常开的机器（内网 VPS 或本机 + 隧道）：

```bash
# 服务端
kp serve --data ~/.keypoint/data

# 公网暴露（推荐隧道，别开端口）
cloudflared tunnel --url http://127.0.0.1:8787
```

```bash
# 每个人各自
kp init --server https://kp.your-domain.com

# 管理员发 key
kp identity create alice-frontend --kind human --roles frontend
# 把返回的 key 给 alice，她跑：
kp init --server https://kp.your-domain.com --key kp_xxx --yes
```

详见 `docs/deploy-tunnel.md`。
