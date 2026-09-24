---
name: kp-new
description: "Keypoint 建任务：把当前会话里的这件事建成任务——抽目标/验收等分段、按角色拆工作面并指派。用法 /kp-new [一句话]"
argument-hint: "[标题或一句话]"
disable-model-invocation: true
disableModelInvocation: true
allowed-tools: Bash(kp:*)
---

# /kp-new — 建任务

要建的事：$ARGUMENTS
（为空就按当前会话在做的事；Kimi 里如果上面是字面量，用命令后面的文字。）

1. `kp whoami`、`kp role ls --holders` —— 确认你是谁、有哪些角色真的有人接。
2. 从会话里抽内容，**goal 和 acceptance 必须有**；不知道的写「（待确认：具体要问什么）」，不要编。
3. 需要几个角色并行就拆几个工作面（2–4 个常见），写清依赖。
4. 写成 JSON 文件再建（引号多，别塞进命令行）：

```bash
cat > /tmp/kp-new.json <<'JSON'
{"title":"…","kind":"feature","priority":"P2",
 "segments":{"context":"…","goal":"…","acceptance":"…","files":"…"},
 "sides":[{"key":"api","assignee_role":"backend","segments":{"要做的事":"…"}},
          {"key":"ui","assignee_role":"frontend","deps":["api"],"segments":{"要做的事":"…"}}]}
JSON
kp task new --from-json /tmp/kp-new.json
```

5. 回给用户：任务号、各工作面派给了谁、开工包命令 `kp task pack <任务号> --side <面>`。
   服务端提示「没有可依据的内容」时，说明缺什么。

分段怎么抽、要不要拆面的细则见 keypoint-notify skill。
