# 本地 Harness 可观测性

本领域描述本地 AI coding harness 产生的遥测，以及基于这些遥测形成的统一视图。

## Language

**Harness**:
在本地协调模型、工具与用户会话的 AI coding runtime。当前支持 Claude Code 与 OpenAI Codex CLI。
_Avoid_: 用“AI 编码客户端”统称该领域对象

**Harness family**:
共享同一套 harness 协议语义的稳定分类，当前为 `claude` 或 `codex`，不受厂商特有 Resource 与 Event 名称影响。
_Avoid_: Provider、model family

**Harness telemetry**:
Harness 针对本地 coding session 与 agent activity 发出的 Metrics 和 Events；它不同于模型厂商的账单或平台遥测。
_Avoid_: 用“Claude telemetry”指代全部 harness telemetry

**Supported harness**:
属于本项目明确兼容范围的 harness。
_Avoid_: 任意 OTLP client
