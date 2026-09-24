---
name: kp-cancel
description: "Keypoint 取消：放弃认领（面还给角色）、取消任务（记原因并归档），或停掉自动接活。用法 /kp-cancel [KP-12] [面] [原因] 或 /kp-cancel loop"
argument-hint: "[KP-12] [面] [原因] | loop"
disable-model-invocation: true
disableModelInvocation: true
allowed-tools: Bash(kp:*)
---

# /kp-cancel — 取消

参数：$ARGUMENTS（Kimi 里如果是字面量，用命令后面的文字）

按参数判断是哪一种，拿不准就问一句，**不要猜着取消**：

| 想取消的 | 做法 |
|---|---|
| 我认领的某个面，不做了 | `kp release KP-12 ui -m "原因"` —— 只清认领人，角色保留，同角色的人能接 |
| 整个任务 | `kp cancel KP-12 -m "原因"` —— 记原因并归档；`kp task status KP-12 inbox` 可恢复 |
| 自动接活（`loop`） | Claude Code：用 CronList 找 prompt 是 /kp-next 的定时任务，CronDelete 删掉；后台的 `kp wait` 用停止后台任务的方式停掉。终端里的 `kp loop` 用 Ctrl-C |

删除任务需要 admin（`kp task rm`），一般用不到——归档就够了，时间线还在。
