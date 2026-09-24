---
name: kp-next
description: "Keypoint 接活：看有没有轮到我的活，有就认领、按开工包做完并交棒；没有就一句话结束。等活/接下一件/轮到我 时用，可配 /loop 定时跑。用法 /kp-next [KP-12]"
argument-hint: "[KP-12]"
allowed-tools: Bash(kp:*)
---

# /kp-next — 接一件活

范围：$ARGUMENTS（写了任务号就只看那一件；Kimi 里如果是字面量就忽略）

1. `kp next --claim`（有任务号就加 `--task KP-12`）。
   - 输出「（没有属于你的活）」→ 回一句「没有轮到我的活」，**结束**。别再做别的。
2. 看开工包开头的「为什么是你」：
   - `mention`：有人问你问题 → 回答（`kp report <任务号> --type question|decision -m "…"`），不接手那个面，结束。
   - `assigned` / `unblocked`：这件是你的，开工。
   - `owned`：你是负责人，看一眼进展，需要就派人，结束。
3. 真做：读代码、改、跑测试。缺信息写「（待确认：…）」，不要编。
4. 收尾（二选一）：
   - 做完：`kp done <任务号> <面> -m "改了什么 / 怎么验证 / 遗留风险"` —— 下游自动解封。
   - 卡住：`kp report <任务号> --side <面> --type blocker -m "卡在哪、需要什么" --mention @角色`
   - 做不了：`kp release <任务号> <面> -m "原因"`，还给同角色的人。
5. 一次只处理一件。
