# Claude Code OTLP 指标与事件参考

本文档描述 Claude Code 通过 OpenTelemetry 协议导出的指标 (Metrics) 与事件 (Events) 的数据结构，作为本项目接收端解析与建模的依据。

涵盖范围：
- **Metrics**：8 个核心指标全部纳入
- **Events**：第一梯队 5 个 + 第二梯队 6 个，共 11 个事件

> 不涵盖：`api_request_body` / `api_response_body` 原始 body 事件、`internal_error`、`auth`、`plugin_installed` / `plugin_loaded`、`hook_*` 系列。

---

## 1. OTLP 传输概览

本项目接收端选用 **gRPC** 传输：

- 客户端配置：`OTEL_EXPORTER_OTLP_PROTOCOL=grpc`，`OTEL_EXPORTER_OTLP_ENDPOINT=http://<host>:4317`
- 默认端口：`4317`
- 服务定义（来自 `go.opentelemetry.io/proto/otlp`）：
  - Metrics：`opentelemetry.proto.collector.metrics.v1.MetricsService/Export`
    - Request：`ExportMetricsServiceRequest`
    - Response：`ExportMetricsServiceResponse`
  - Logs / Events：`opentelemetry.proto.collector.logs.v1.LogsService/Export`
    - Request：`ExportLogsServiceRequest`
    - Response：`ExportLogsServiceResponse`
- Temporality 默认 `delta`（可改 `cumulative`，本项目按 `delta` 入库）

> gRPC 与 HTTP/protobuf 共享同一份 protobuf 消息定义，**只是传输层不同**。gRPC 走 HTTP/2 + 长度前缀帧 + trailer 携带状态码；HTTP/protobuf 走标准 HTTP path（`/v1/metrics`、`/v1/logs`）。本项目用 `google.golang.org/grpc` 实现 `MetricsServiceServer` / `LogsServiceServer`。

### 1.1 Metrics 数据点形态

所有 8 个指标都是 **Sum (monotonic, delta)** 类型，对应 OTLP `NumberDataPoint`：

```
NumberDataPoint {
  start_time_unix_nano: <interval 起点>
  time_unix_nano:       <interval 终点>
  value:                <int 或 double，看指标单位>
  attributes:           <map<string, AnyValue>>
}
```

### 1.2 Events 数据点形态

事件走 OTLP Logs，每个 `LogRecord` 对应一个事件：

```
LogRecord {
  time_unix_nano:    <事件发生时间>
  observed_time_unix_nano: <SDK 观察到的时间>
  severity_number:   INFO 居多
  body:              通常为空，关键字段全部在 attributes
  attributes:        <map<string, AnyValue>>
}
```

事件类型通过 `attributes["event.name"]` 区分。

---

## 2. 公共字段

### 2.1 Resource 级（每个 OTLP 请求共享）

| 字段 | 类型 | 说明 |
|---|---|---|
| `service.name` | string | 固定为 `claude-code` |
| `service.version` | string | Claude Code 版本号 |

### 2.2 公共 Attributes（所有 metric 和 event 都可能携带）

| 字段 | 类型 | 始终存在 | 说明 |
|---|---|---|---|
| `session.id` | string | ✅（默认开启） | 会话 UUID |
| `user.id` | string | ✅ | 安装级匿名 ID |
| `user.account_uuid` | string | 仅认证后 | 账号 UUID |
| `user.account_id` | string | 仅认证后 | tagged 格式账号 ID，如 `user_01BWBeN28...` |
| `user.email` | string | 仅 OAuth 认证后 | 用户邮箱 |
| `organization.id` | string | 仅认证后 | 组织 UUID |
| `app.version` | string | 默认关 | Claude Code 版本 |
| `terminal.type` | string | 检测到时 | `iTerm.app` / `vscode` / `cursor` / `tmux` 等 |

### 2.3 Event 额外公共字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `event.name` | string | 事件类型，对应下文每个事件标题 |
| `event.timestamp` | ISO 8601 | 事件发生时间（通常与 `time_unix_nano` 一致） |
| `event.sequence` | int64 | 会话内单调递增序号 |
| `prompt.id` | string (UUID) | 关联同一 prompt 触发的所有事件 |
| `workspace.host_paths` | string[] | 桌面端工作目录数组（如有） |

---

## 3. Metrics

8 个指标统一遵循下面的"归一化结构"，存储时只需 `ts / value` + 公共属性 + 各自的特有属性。

```jsonc
{
  "ts":         "<time_unix_nano>",        // interval 终点
  "start_ts":   "<start_time_unix_nano>",  // interval 起点
  "value":      <number>,                  // delta 数值
  "common":     { ... },                   // §2.2 中字段
  "specific":   { ... }                    // 各指标特有 attribute
}
```

下面只列出每个指标的 **特有属性**。

### 3.1 `claude_code.session.count`

- 单位：`count`
- 触发：每次会话启动时 +1

| 字段 | 类型 | 取值 |
|---|---|---|
| `start_type` | string | `fresh` / `resume` / `continue` |

### 3.2 `claude_code.lines_of_code.count`

- 单位：`count`
- 触发：代码增删时累加

| 字段 | 类型 | 取值 |
|---|---|---|
| `type` | string | `added` / `removed` |

### 3.3 `claude_code.pull_request.count`

- 单位：`count`
- 触发：通过 shell 命令或 MCP 工具创建 PR / MR 时 +1
- 无特有属性

### 3.4 `claude_code.commit.count`

- 单位：`count`
- 触发：经由 Claude Code 创建 git commit 时 +1
- 无特有属性

### 3.5 `claude_code.cost.usage`

- 单位：`USD`
- 触发：每次 API 请求后累加

| 字段 | 类型 | 取值 / 说明 |
|---|---|---|
| `model` | string | 如 `claude-sonnet-4-6` |
| `query_source` | string | `main` / `subagent` / `auxiliary` |
| `speed` | string | `fast`（fast mode 时存在），否则缺省 |
| `effort` | string | `low` / `medium` / `high` / `xhigh` / `max`，模型不支持时缺省 |
| `agent.name` | string | 内置或官方插件 agent 名；其他用户自定义统一为 `custom` |
| `skill.name` | string | 内置 / 用户 / 官方插件 skill 名；第三方插件 skill 统一为 `third-party` |
| `plugin.name` | string | 拥有该 skill / agent 的插件名；第三方为 `third-party` |
| `marketplace.name` | string | 仅 official marketplace 时出现 |

### 3.6 `claude_code.token.usage`

- 单位：`tokens`
- 触发：每次 API 请求后累加

| 字段 | 类型 | 取值 |
|---|---|---|
| `type` | string | `input` / `output` / `cacheRead` / `cacheCreation` |
| `model` | string | 同 §3.5 |
| `query_source` | string | 同 §3.5 |
| `speed` | string | 同 §3.5 |
| `effort` | string | 同 §3.5 |
| `agent.name` | string | 同 §3.5 |
| `skill.name` | string | 同 §3.5 |
| `plugin.name` | string | 同 §3.5 |
| `marketplace.name` | string | 同 §3.5 |

### 3.7 `claude_code.code_edit_tool.decision`

- 单位：`count`
- 触发：用户接受/拒绝 Edit / Write / NotebookEdit 工具调用

| 字段 | 类型 | 取值 |
|---|---|---|
| `tool_name` | string | `Edit` / `Write` / `NotebookEdit` |
| `decision` | string | `accept` / `reject` |
| `source` | string | `config` / `hook` / `user_permanent` / `user_temporary` / `user_abort` / `user_reject` |
| `language` | string | 文件语言，如 `TypeScript` / `Python` / `Markdown`，未识别为 `unknown` |

### 3.8 `claude_code.active_time.total`

- 单位：`s`
- 触发：用户交互或 CLI 处理时累加

| 字段 | 类型 | 取值 |
|---|---|---|
| `type` | string | `user`（键盘交互） / `cli`（工具执行与响应生成） |

---

## 4. Events — 第一梯队

事件归一化结构：

```jsonc
{
  "ts":             "<time_unix_nano>",
  "common":         { ... },         // §2.2
  "event_common":   { ... },         // §2.3
  "specific":       { ... }          // 事件特有 attribute
}
```

### 4.1 `claude_code.user_prompt`

用户提交 prompt 时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `prompt_length` | int | prompt 字符长度 |
| `prompt` | string | prompt 内容；默认脱敏，需 `OTEL_LOG_USER_PROMPTS=1` |
| `command_name` | string | 命令名；内置/官方命令原样，自定义/插件命令为 `custom`，MCP 命令为 `mcp`（除非 `OTEL_LOG_TOOL_DETAILS=1`） |
| `command_source` | string | `builtin` / `custom` / `mcp` |

### 4.2 `claude_code.api_request`

每次 API 请求成功时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `model` | string | 如 `claude-sonnet-4-6` |
| `cost_usd` | double | 估算成本 |
| `duration_ms` | int | 请求耗时 |
| `input_tokens` | int | |
| `output_tokens` | int | |
| `cache_read_tokens` | int | |
| `cache_creation_tokens` | int | |
| `request_id` | string | Anthropic API request ID，如 `req_011...` |
| `speed` | string | `fast` / `normal` |
| `query_source` | string | 子系统名，如 `repl_main_thread` / `compact` / 子 agent 名 |
| `effort` | string | 同 §3.5 |

### 4.3 `claude_code.api_error`

API 请求最终失败时记录（已耗尽内部重试）。

| 字段 | 类型 | 说明 |
|---|---|---|
| `model` | string | |
| `error` | string | 错误信息 |
| `status_code` | int | HTTP 状态码，非 HTTP 错误时缺省 |
| `duration_ms` | int | |
| `attempt` | int | 总尝试次数（含首次） |
| `request_id` | string | 仅 API 返回时存在 |
| `speed` | string | `fast` / `normal` |
| `query_source` | string | |
| `effort` | string | |

### 4.4 `claude_code.tool_result`

工具调用执行完成时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `tool_name` | string | 工具名 |
| `tool_use_id` | string | 工具调用 ID，与 hook 一致，可与 §4.5 关联 |
| `success` | bool (string) | `"true"` / `"false"` |
| `duration_ms` | int | |
| `error_type` | string | 错误分类，如 `Error:ENOENT` / `ShellError` |
| `error` | string | 完整错误信息（需 `OTEL_LOG_TOOL_DETAILS=1`） |
| `decision_type` | string | `accept` / `reject` |
| `decision_source` | string | 同 §3.7 `source` |
| `tool_input_size_bytes` | int | 输入 JSON 大小 |
| `tool_result_size_bytes` | int | 结果大小 |
| `mcp_server_scope` | string | MCP 工具时存在 |
| `tool_parameters` | string (JSON) | 需 `OTEL_LOG_TOOL_DETAILS=1`；Bash 工具含 `bash_command` / `full_command` / `git_commit_id` 等 |
| `tool_input` | string (JSON) | 需 `OTEL_LOG_TOOL_DETAILS=1`；单值超 512 字符截断，整体 ~4KB |

### 4.5 `claude_code.tool_decision`

工具权限决策时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `tool_name` | string | |
| `tool_use_id` | string | 与 §4.4 对齐 |
| `decision` | string | `accept` / `reject` |
| `source` | string | `config` / `hook` / `user_permanent` / `user_temporary` / `user_abort` / `user_reject` |

---

## 5. Events — 第二梯队

### 5.1 `claude_code.api_retries_exhausted`

API 请求多次重试仍失败时记录，与最终 `api_error` 同时出现。

| 字段 | 类型 | 说明 |
|---|---|---|
| `model` | string | |
| `error` | string | 最终错误信息 |
| `status_code` | int | |
| `total_attempts` | int | 总尝试次数 |
| `total_retry_duration_ms` | int | 所有尝试总耗时 |
| `speed` | string | `fast` / `normal` |

### 5.2 `claude_code.compaction`

会话压缩完成时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `trigger` | string | `auto` / `manual` |
| `success` | bool (string) | `"true"` / `"false"` |
| `duration_ms` | int | |
| `pre_tokens` | int | 压缩前 token 数 |

### 5.3 `claude_code.permission_mode_changed`

权限模式切换时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `from_mode` | string | `default` / `plan` / `acceptEdits` / `auto` / `bypassPermissions` |
| `to_mode` | string | 同上 |
| `trigger` | string | `shift_tab` / `exit_plan_mode` / `auto_gate_denied` / `auto_opt_in`；SDK / bridge 触发时缺省 |

### 5.4 `claude_code.mcp_server_connection`

MCP server 连接 / 断开 / 失败时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `status` | string | `connected` / `failed` / `disconnected` |
| `transport_type` | string | `stdio` / `sse` / `http` |
| `server_scope` | string | `user` / `project` / `local` |
| `duration_ms` | int | 连接耗时 |
| `error_code` | string | 失败时存在 |
| `server_name` | string | 需 `OTEL_LOG_TOOL_DETAILS=1` |
| `error` | string | 需 `OTEL_LOG_TOOL_DETAILS=1` |

### 5.5 `claude_code.skill_activated`

Skill 被调用时记录（通过 Skill 工具或 `/` 命令）。

| 字段 | 类型 | 说明 |
|---|---|---|
| `skill.name` | string | 用户自定义与第三方插件 skill 为 `custom_skill`（除非 `OTEL_LOG_TOOL_DETAILS=1`） |
| `invocation_trigger` | string | `user-slash` / `claude-proactive` / `nested-skill` |
| `skill.source` | string | `bundled` / `userSettings` / `projectSettings` / `plugin` |
| `plugin.name` | string | skill 来自插件时存在 |
| `marketplace.name` | string | 同上 |

### 5.6 `claude_code.at_mention`

prompt 中的 `@`-mention 被解析时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `mention_type` | string | `file` / `directory` / `agent` / `mcp_resource` |
| `success` | bool (string) | `"true"` / `"false"` |

---

## 6. 兜底策略

为应对 Claude Code 版本升级新增 attribute 的情况，每张表保留一个 `attrs JSON` 列，存储未被显式提取到列的所有 attribute。详见 [`models.md`](./models.md)。

---

## 7. Codex CLI 遥测

自 v2.2 起同步接收 OpenAI Codex CLI 的 OTEL Logs；当前同时接收 Dashboard 所需的 Skill 与 TBT Metrics（调研与决策见 `docs/superpowers/specs/2026-07-01-codex-otel-support-design.md`，本节 Metrics 实测基线 codex-cli 0.145.0）。

### 7.1 客户端配置

Codex **不读取标准 OTEL 环境变量**，只认 `~/.codex/config.toml` 的 `[otel]` 段；Logs 与 Metrics 都需要指向本服务：

```toml
[otel]
environment = "prod"
exporter = { otlp-grpc = { endpoint = "http://127.0.0.1:4317" } }
metrics_exporter = { otlp-grpc = { endpoint = "http://127.0.0.1:4317" } }
```

### 7.2 Resource 与公共属性

- Resource：`service.name` = originator（如 `codex_cli_rs` / `codex_exec` / `codex_vscode`）、`service.version`、`env`、`host.name`，均不提取（落 `attrs`）
- 每条事件的公共 attribute：`conversation.id`、`app.version`、`auth_mode`（`ApiKey` / `Chatgpt`）、`originator`、`terminal.type`、`model`、`slug`、`user.account_id`（可空）、`user.email`（可空）
- **没有 `user.id`**：codex 表族无身份硬约束，统一身份在查询层 `COALESCE(user_account_id, user_email, 'unknown')`
- 事件名在 `event.name` attribute，**完整带 `codex.` 前缀**（实测确认）
- **时间戳**：Codex 不设置 LogRecord 的 `time_unix_nano`（恒为 0，实测确认）。接收端按 `time_unix_nano` → `observed_time_unix_nano` → `event.timestamp` attribute（RFC3339）三级回退解析

### 7.3 入库事件（6 个）

| 事件 | 入库表 | 关键专有属性 |
|---|---|---|
| `codex.conversation_starts` | `codex_event_conversation_starts` | `provider_name`、`reasoning_effort`、`reasoning_summary`、`context_window`、`auto_compact_token_limit`、`approval_policy`、`sandbox_policy`、`mcp_servers`；`auth.env_*` 落 attrs |
| `codex.api_request` | `codex_event_api_request` | `duration_ms`、`http.response.status_code`、`error.message`、`attempt`、`endpoint`；`auth.*` 落 attrs。注意是 **HTTP attempt 粒度**（含重试） |
| `codex.sse_event` | `codex_event_token_usage` | **仅 `event.kind = response.completed` 落库**，其余 kind 计入 skipped 不持久化（量大且无用量数据）。字段：`input_token_count` / `output_token_count` / `cached_token_count`(可空) / `reasoning_token_count`(可空) / `tool_token_count`、`service_tier`、`model_reasoning_effort`、`duration_ms` |
| `codex.user_prompt` | `codex_event_user_prompt` | `prompt_length`、`prompt`（默认 `"[REDACTED]"`，需客户端 `log_user_prompt = true`） |
| `codex.tool_decision` | `codex_event_tool_decision` | `tool_name`、`call_id`、`decision`（原始枚举：`approved` / `approved_for_session` / `denied` / `abort` / `timed_out` 等）、`source`（`AutomatedReviewer` / `Config` / `User`） |
| `codex.tool_result` | `codex_event_tool_result` | `tool_name`、`call_id`、`duration_ms`、`success`、`mcp_server`、`mcp_server_origin`；**`arguments` / `output` 原文只算长度即丢弃**（隐私红线：Codex 默认不脱敏且无客户端开关），落列 `arguments_length` / `output_length` |

范围外（识别但不落库，计入 Unknown）：`codex.startup_phase`、`codex.websocket_connect` / `websocket_request`、`codex.auth_recovery`、`codex.turn_ttft`、`codex.sandbox_outcome`、`codex.network_proxy.policy_decision`、`codex.plugin_install_*`。

### 7.4 入库 Metrics

| Metric | OTLP 形态 | 入库表 | 用途 |
|---|---|---|---|
| `codex.skill.injected` | monotonic DELTA Sum | `codex_metric_skill_injected` | 保存 `value`、`skill`、`status`、`invoke_type`、`session_source`；排名只累加 `status=ok/success` |
| `codex.responses_api_engine_service_tbt.duration_ms` | DELTA Histogram | `codex_metric_response_tbt` | 每个 DataPoint 仅保存 `sample_count` 与 `sum_ms`，不展开 bucket；生成速度 = `sample_count × 1000 / sum_ms` |

TBT 是相邻生成 token 的平均时间（time between tokens），适合作为 Codex 的纯解码速度。`codex.websocket_request.duration_ms` 实测只有 WebSocket 发送开销，并非模型生成耗时，不能用于该指标。Metrics 当前不带稳定的 request / turn ID，因此接收端不做时间戳模糊关联。

### 7.5 token 口径（与 Claude Code 的关键差异）

OpenAI 计数是**子集式**：`cached ⊂ input`、`reasoning ⊂ output`；Anthropic 是**并列式**（cacheRead / cacheCreation 独立于 input）。统一总量公式：

- Claude 总量 = `input + output + cacheRead + cacheCreation`
- Codex 总量 = `input_token_count + output_token_count`（**不可再加 cached，否则重复计算**）

Codex **不上报成本（cost_usd）**，也没有 lines_of_code / commit / PR 类指标。自 v2.4.0 起，本项目可选地在 ingest 时按 LiteLLM 计价表**估算** Codex 的 `cost_usd` 并落入 `codex_event_token_usage.cost_usd`（由 `pricing.enabled` 门控，默认关闭；详见 `docs/models.md` 与 `config.example.yaml` 的 `pricing` 段）。此为估算值，与 Claude 客户端自报的权威成本语义不同。
