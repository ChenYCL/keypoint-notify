---
name: kp-done
description: "Keypoint 完结：上报结果并把工作面置为完成，下游自动解封、最后一个面完成时任务自动收尾。用法 /kp-done [KP-12] [面] [补充]"
argument-hint: "[KP-12] [面] [补充]"
disable-model-invocation: true
disableModelInvocation: true
allowed-tools: Bash(kp:*)
---

# /kp-done — 完结

参数：$ARGUMENTS（Kimi 里如果是字面量，用命令后面的文字）

1. 定位：参数里有任务号/面就用；没有就 `kp board`，找你名下没完成的那个。
2. 从会话里写结果，三段都要有：**改了什么 / 怎么验证的 / 遗留风险**（没有就写「无」）。
3. 一条命令完成上报 + 置完成：

```bash
kp done KP-12 ui -m "改了什么：… / 验证：… / 风险：…"
```

4. 把输出里的「下游 xx 已解封」「任务已自动收尾」告诉用户。
   只完结你自己的面；别人的面要动，先 `kp report --type handoff` 跟对方说。
