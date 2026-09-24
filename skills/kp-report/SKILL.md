---
name: kp-report
description: "Keypoint 上报：把刚做的进展/阻塞/决定/问题上报到任务工作面，内容从当前会话里整理。用法 /kp-report [KP-12] [面] [说明]"
argument-hint: "[KP-12] [面] [说明]"
disable-model-invocation: true
disableModelInvocation: true
allowed-tools: Bash(kp:*)
---

# /kp-report — 上报

参数：$ARGUMENTS（Kimi 里如果是字面量，用命令后面的文字）

1. 定位任务和面：参数里有就用；没有就 `kp board` 找你名下正在做的那个，只有一个就用它，多个就问。
2. 定类型（从说法判断）：进展 `progress` · 卡住 `blocker` · 要确认 `question` · 定了个方案 `decision`。
   **做完了不要用这个**，用 /kp-done（它会把面置完成、解封下游）。
3. 正文从会话里整理，给没看过这次对话的人看：做了什么、现在到哪、下一步 / 需要谁做什么。

```bash
kp report KP-12 --side ui -m "…"
kp report KP-12 --side ui --type blocker -m "卡在哪、需要什么" --mention @backend
kp report KP-12 --side ui --attach shot.png -m "…"       # 截图、日志做证据
```

4. 回给用户一行：报到了哪、什么类型。上报是追加的，重试前先 `kp task show KP-12 --reports 3` 看是不是已经写上了。
