---
name: kp-watch
description: "Keypoint 订阅：盯住轮到我的活和收件箱，一有新活/新通知就提示并开始处理；也能订阅某个任务的动态。用法 /kp-watch [KP-12] 或 /kp-watch stop"
argument-hint: "[KP-12 | stop]"
allowed-tools: Bash(kp:*)
---

# /kp-watch — 订阅，来了就开始

参数：$ARGUMENTS（Kimi 里如果是字面量，用命令后面的文字）

- 参数是任务号：先 `kp watch KP-12`（这个任务的动态从此进你的收件箱），再往下开始盯。
- 参数是 `stop`：停掉后台的 `kp wait`；给了任务号就 `kp unwatch KP-12`。结束。

**开始盯**

1. 跑 `kp wait --timeout 3600`。它会阻塞，直到有新活或新通知才退出。
   - Claude Code：用 Bash 的**后台模式**（run_in_background）跑。它退出时你会被叫醒，
     这期间用户可以照常和你说话。
   - Kimi Code 或不能后台时：前台跑 `kp wait --timeout 50`，没动静就再跑，直到用户叫停。
2. 被叫醒后看输出第一行：
   - `KP-WAIT: work` → 告诉用户「有活了：<任务号> <标题>」，按 /kp-next 的流程认领并处理
     （`kp next --claim --task <输出里的任务号>` 认领这一件）。
   - `KP-WAIT: notification` → `kp inbox --unread`，一两句话告诉用户是什么；需要你动手的就动手。
   - `KP-WAIT: timeout` → 什么都没来，不用说话。
3. 处理完**重新挂上** `kp wait`，直到用户说停。

`kp wait` 不认领也不动游标，不会吞掉提及。
