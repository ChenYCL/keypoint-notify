---
name: kp-loop
description: "Keypoint 自动定时处理：按间隔自动接活并做完交棒（Claude Code 里建定时任务；也给出无人值守的 kp loop 命令）。用法 /kp-loop [间隔，如 10m] 或 /kp-loop stop"
argument-hint: "[10m | stop]"
disable-model-invocation: true
disableModelInvocation: true
allowed-tools: Bash(kp:*)
---

# /kp-loop — 自动定时处理

参数：$ARGUMENTS（间隔，默认 10m；`stop` 表示停掉。Kimi 里如果是字面量，用命令后面的文字）

先 `kp whoami` 确认身份，**身份是 admin 就停下提醒用户**：自动处理应该用业务角色的身份跑。

**在 Claude Code 里（有 CronCreate 工具时）**

- 开：用 CronCreate 建一个循环任务，prompt 就是 `/kp-next`，按间隔写 cron（10m → `*/10 * * * *`）。
  告诉用户：会话开着且空闲时才触发、7 天后过期、`/kp-cancel loop` 可停。
  等价的手动写法是 `/loop 10m /kp-next`。
- 停：CronList 找 prompt 为 `/kp-next` 的任务，CronDelete。

**要关掉会话也一直跑（或在 Kimi Code 里）**：没有会话内定时器，给用户这条命令，
在要干活的仓库目录下的终端里跑（每件活起一个新会话，做完自动 `kp done`）：

```bash
kp loop --agent claude     # 或 --agent kimi
```

多个 bot 身份并行时各用各的配置目录：`KEYPOINT_HOME=~/.keypoint-be-bot kp loop --agent claude`。
