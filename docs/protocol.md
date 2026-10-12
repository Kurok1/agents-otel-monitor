# Harness OTLP 指标与事件参考

本文档描述 `agents-otel-monitor` 当前支持的本地 AI coding harness 如何通过 OpenTelemetry 导出 Metrics 与 Events，以及接收端如何识别这些 vendor-specific 信号。传输层共用 OTLP gRPC，字段、时间戳、身份和 token 口径按 harness 分别建模。

| Harness | 纳入的 Metrics | 纳入的 Events | 协议章节 |
|---|---:|---:|---|
| Claude Code | 8 | 11 | §2 |
| OpenAI Codex CLI | 3 | 8 | §3 |

本文档只描述已持久化的信号。Claude Code 的 `api_request_body` / `api_response_body`、`internal_error`、`auth`、`plugin_installed` / `plugin_loaded`、`hook_*`，以及 Codex 的高容量或 Dashboard 暂不需要的事件，均不在当前持久化范围内。

---

## 1. OTLP 传输概览

所有受支持 harness 共用 **gRPC** 接收端：

- Endpoint：`http://<host>:4317`；具体客户端配置分别见 §2.1 与 §3.1
- 默认端口：`4317`
- 服务定义（来自 `go.opentelemetry.io/proto/otlp`）：
  - Metrics：`opentelemetry.proto.collector.metrics.v1.MetricsService/Export`
    - Request：`ExportMetricsServiceRequest`
    - Response：`ExportMetricsServiceResponse`
  - Logs / Events：`opentelemetry.proto.collector.logs.v1.LogsService/Export`
    - Request：`ExportLogsServiceRequest`
    - Response：`ExportLogsServiceResponse`
- 接收端按各 harness 的协议约束校验 signal type 与 temporality，不在传输层强行归一化

> gRPC 与 HTTP/protobuf 共享同一份 protobuf 消息定义，**只是传输层不同**。gRPC 走 HTTP/2 + 长度前缀帧 + trailer 携带状态码；HTTP/protobuf 走标准 HTTP path（`/v1/metrics`、`/v1/logs`）。本项目用 `google.golang.org/grpc` 实现 `MetricsServiceServer` / `LogsServiceServer`。

### 1.1 Metrics 数据点形态

Claude Code 的 8 个指标与 Codex 的 Skill 指标都是 **Sum (monotonic, delta)**，对应 OTLP `NumberDataPoint`：

```
NumberDataPoint {
  start_time_unix_nano: <interval 起点>
  time_unix_nano:       <interval 终点>
  value:                <int 或 double，看指标单位>
  attributes:           <map<string, AnyValue>>
}
```

Codex 的 TBT 与 turn token 指标是 **DELTA Histogram**，接收端只保存 `count` 与 `sum`，不展开 buckets：

```
HistogramDataPoint {
  start_time_unix_nano: <interval 起点>
  time_unix_nano:       <interval 终点>
  count:                <样本数>
  sum:                  <总毫秒数>
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

## 2. Claude Code 遥测

### 2.1 客户端配置

Claude Code 使用标准 OTEL 环境变量：

```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_METRICS_EXPORTER=otlp
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317
```

### 2.2 公共字段

#### 2.2.1 Resource 级（每个 OTLP 请求共享）

| 字段 | 类型 | 说明 |
|---|---|---|
| `service.name` | string | 固定为 `claude-code` |
| `service.version` | string | Claude Code 版本号 |

#### 2.2.2 公共 Attributes（所有 metric 和 event 都可能携带）

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

#### 2.2.3 Event 额外公共字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `event.name` | string | 事件类型，对应下文每个事件标题 |
| `event.timestamp` | ISO 8601 | 事件发生时间（通常与 `time_unix_nano` 一致） |
| `event.sequence` | int64 | 会话内单调递增序号 |
| `prompt.id` | string (UUID) | 关联同一 prompt 触发的所有事件 |
| `workspace.host_paths` | string[] | 桌面端工作目录数组（如有） |

---

### 2.3 Metrics（8 个）

8 个指标统一遵循下面的"归一化结构"，存储时只需 `ts / value` + 公共属性 + 各自的特有属性。

```jsonc
{
  "ts":         "<time_unix_nano>",        // interval 终点
  "start_ts":   "<start_time_unix_nano>",  // interval 起点
  "value":      <number>,                  // delta 数值
  "common":     { ... },                   // §2.2.2 中字段
  "specific":   { ... }                    // 各指标特有 attribute
}
```

下面只列出每个指标的 **特有属性**。

#### 2.3.1 `claude_code.session.count`

- 单位：`count`
- 触发：每次会话启动时 +1

| 字段 | 类型 | 取值 |
|---|---|---|
| `start_type` | string | `fresh` / `resume` / `continue` |

#### 2.3.2 `claude_code.lines_of_code.count`

- 单位：`count`
- 触发：代码增删时累加

| 字段 | 类型 | 取值 |
|---|---|---|
| `type` | string | `added` / `removed` |

#### 2.3.3 `claude_code.pull_request.count`

- 单位：`count`
- 触发：通过 shell 命令或 MCP 工具创建 PR / MR 时 +1
- 无特有属性

#### 2.3.4 `claude_code.commit.count`

- 单位：`count`
- 触发：经由 Claude Code 创建 git commit 时 +1
- 无特有属性

#### 2.3.5 `claude_code.cost.usage`

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

#### 2.3.6 `claude_code.token.usage`

- 单位：`tokens`
- 触发：每次 API 请求后累加

| 字段 | 类型 | 取值 |
|---|---|---|
| `type` | string | `input` / `output` / `cacheRead` / `cacheCreation` |
| `model` | string | 同 §2.3.5 |
| `query_source` | string | 同 §2.3.5 |
| `speed` | string | 同 §2.3.5 |
| `effort` | string | 同 §2.3.5 |
| `agent.name` | string | 同 §2.3.5 |
| `skill.name` | string | 同 §2.3.5 |
| `plugin.name` | string | 同 §2.3.5 |
| `marketplace.name` | string | 同 §2.3.5 |

#### 2.3.7 `claude_code.code_edit_tool.decision`

- 单位：`count`
- 触发：用户接受/拒绝 Edit / Write / NotebookEdit 工具调用

| 字段 | 类型 | 取值 |
|---|---|---|
| `tool_name` | string | `Edit` / `Write` / `NotebookEdit` |
| `decision` | string | `accept` / `reject` |
| `source` | string | `config` / `hook` / `user_permanent` / `user_temporary` / `user_abort` / `user_reject` |
| `language` | string | 文件语言，如 `TypeScript` / `Python` / `Markdown`，未识别为 `unknown` |

#### 2.3.8 `claude_code.active_time.total`

- 单位：`s`
- 触发：用户交互或 CLI 处理时累加

| 字段 | 类型 | 取值 |
|---|---|---|
| `type` | string | `user`（键盘交互） / `cli`（工具执行与响应生成） |

---

### 2.4 Events — 核心（5 个）

事件归一化结构：

```jsonc
{
  "ts":             "<time_unix_nano>",
  "common":         { ... },         // §2.2.2
  "event_common":   { ... },         // §2.2.3
  "specific":       { ... }          // 事件特有 attribute
}
```

#### 2.4.1 `claude_code.user_prompt`

用户提交 prompt 时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `prompt_length` | int | prompt 字符长度 |
| `prompt` | string | prompt 内容；默认脱敏，需 `OTEL_LOG_USER_PROMPTS=1` |
| `command_name` | string | 命令名；内置/官方命令原样，自定义/插件命令为 `custom`，MCP 命令为 `mcp`（除非 `OTEL_LOG_TOOL_DETAILS=1`） |
| `command_source` | string | `builtin` / `custom` / `mcp` |

#### 2.4.2 `claude_code.api_request`

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
| `effort` | string | 同 §2.3.5 |

#### 2.4.3 `claude_code.api_error`

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

#### 2.4.4 `claude_code.tool_result`

工具调用执行完成时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `tool_name` | string | 工具名 |
| `tool_use_id` | string | 工具调用 ID，与 hook 一致，可与 §2.4.5 关联 |
| `success` | bool (string) | `"true"` / `"false"` |
| `duration_ms` | int | |
| `error_type` | string | 错误分类，如 `Error:ENOENT` / `ShellError` |
| `error` | string | 完整错误信息（需 `OTEL_LOG_TOOL_DETAILS=1`） |
| `decision_type` | string | `accept` / `reject` |
| `decision_source` | string | 同 §2.3.7 `source` |
| `tool_input_size_bytes` | int | 输入 JSON 大小 |
| `tool_result_size_bytes` | int | 结果大小 |
| `mcp_server_scope` | string | MCP 工具时存在 |
| `tool_parameters` | string (JSON) | 需 `OTEL_LOG_TOOL_DETAILS=1`；Bash 工具含 `bash_command` / `full_command` / `git_commit_id` 等 |
| `tool_input` | string (JSON) | 需 `OTEL_LOG_TOOL_DETAILS=1`；单值超 512 字符截断，整体 ~4KB |

#### 2.4.5 `claude_code.tool_decision`

工具权限决策时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `tool_name` | string | |
| `tool_use_id` | string | 与 §2.4.4 对齐 |
| `decision` | string | `accept` / `reject` |
| `source` | string | `config` / `hook` / `user_permanent` / `user_temporary` / `user_abort` / `user_reject` |

---

### 2.5 Events — 扩展（6 个）

#### 2.5.1 `claude_code.api_retries_exhausted`

API 请求多次重试仍失败时记录，与最终 `api_error` 同时出现。

| 字段 | 类型 | 说明 |
|---|---|---|
| `model` | string | |
| `error` | string | 最终错误信息 |
| `status_code` | int | |
| `total_attempts` | int | 总尝试次数 |
| `total_retry_duration_ms` | int | 所有尝试总耗时 |
| `speed` | string | `fast` / `normal` |

#### 2.5.2 `claude_code.compaction`

会话压缩完成时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `trigger` | string | `auto` / `manual` |
| `success` | bool (string) | `"true"` / `"false"` |
| `duration_ms` | int | |
| `pre_tokens` | int | 压缩前 token 数 |

#### 2.5.3 `claude_code.permission_mode_changed`

权限模式切换时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `from_mode` | string | `default` / `plan` / `acceptEdits` / `auto` / `bypassPermissions` |
| `to_mode` | string | 同上 |
| `trigger` | string | `shift_tab` / `exit_plan_mode` / `auto_gate_denied` / `auto_opt_in`；SDK / bridge 触发时缺省 |

#### 2.5.4 `claude_code.mcp_server_connection`

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

#### 2.5.5 `claude_code.skill_activated`

Skill 被调用时记录（通过 Skill 工具或 `/` 命令）。

| 字段 | 类型 | 说明 |
|---|---|---|
| `skill.name` | string | 用户自定义与第三方插件 skill 为 `custom_skill`（除非 `OTEL_LOG_TOOL_DETAILS=1`） |
| `invocation_trigger` | string | `user-slash` / `claude-proactive` / `nested-skill` |
| `skill.source` | string | `bundled` / `userSettings` / `projectSettings` / `plugin` |
| `plugin.name` | string | skill 来自插件时存在 |
| `marketplace.name` | string | 同上 |

#### 2.5.6 `claude_code.at_mention`

prompt 中的 `@`-mention 被解析时记录。

| 字段 | 类型 | 说明 |
|---|---|---|
| `mention_type` | string | `file` / `directory` / `agent` / `mcp_resource` |
| `success` | bool (string) | `"true"` / `"false"` |

---

### 2.6 未识别字段兜底

为应对 Claude Code 版本升级新增 attribute 的情况，每张表保留一个 `attrs JSON` 列，存储未被显式提取到列的所有 attribute。详见 [`models.md`](./models.md)。

---

## 3. OpenAI Codex CLI 遥测

当前接入 8 个 Logs 事件、3 个 Metrics。新增字段以官方 codex-cli 0.162.1（commit `092d3acd6bec3e3a14bdc7e7a2810ab628ab759d`）为协议基线；兼容旧版缺字段的记录，原有 Metrics fixture 基线为 0.145.0。

### 3.1 客户端配置

导出地址通过 `~/.codex/config.toml` 配置，Logs 与 Metrics 分别启用：

```toml
[otel]
environment = "prod"
log_user_prompt = false
exporter = { otlp-grpc = { endpoint = "http://127.0.0.1:4317" } }
metrics_exporter = { otlp-grpc = { endpoint = "http://127.0.0.1:4317" } }

[analytics]
enabled = true  # Metrics 导出需要启用 analytics
```

在启动 Codex 的环境中设置 `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=DELTA`。本服务拒绝累计型数据，以免重复计数；不能用标准 endpoint 环境变量代替上述 exporter 配置。历史未导出的新事件不能补采。

### 3.2 Resource 与公共属性

- Resource 的 `service.name`、`service.version`、`env`、`host.name` 落入 `attrs`；已识别属性按数据点覆盖 Resource 的顺序提取。
- 公共属性：`conversation.id`、`app.version`、`auth_mode`、`originator`、`terminal.type`、`model`、`slug`、`user.account_id`、`user.email`。身份字段可空，没有 Claude 的 `user.id` 硬约束。
- 事件名来自完整带 `codex.` 前缀的 `event.name`。
- 时间戳回退顺序为 `time_unix_nano` → `observed_time_unix_nano` → RFC3339 `event.timestamp`。当前 Codex Logs 通常不设置第一项。

### 3.3 入库事件（8 个）

| 事件 | 入库表 | 专有属性与语义 |
|---|---|---|
| `codex.conversation_starts` | `codex_event_conversation_starts` | `provider_name`、`reasoning_effort`、`reasoning_summary`、`context_window`、`auto_compact_token_limit`、`approval_policy`、`sandbox_policy`、`mcp_servers` |
| `codex.api_request` | `codex_event_api_request` | `duration_ms`、`http.response.status_code`、`error.message`、`attempt`、`endpoint`；HTTP attempt 粒度，含重试 |
| `codex.sse_event` | `codex_event_token_usage` | 只保留 `event.kind=response.completed`；input/output/cached/cache_write/reasoning/tool 的 `*_token_count`，`ttft_ms`、`error.message`、`service_tier`、`model_reasoning_effort`、旧版 `duration_ms` |
| `codex.user_prompt` | `codex_event_user_prompt` | `prompt_length`、`prompt`（默认 `[REDACTED]`） |
| `codex.tool_decision` | `codex_event_tool_decision` | `tool_name`、`call_id`、原始 `decision`、`source` |
| `codex.tool_result` | `codex_event_tool_result` | `tool_name`、`call_id`、`duration_ms`、`success`、`mcp_server`、`mcp_server_origin`、可空 `output_truncated`；`arguments` / `output` 只算字节长度即丢弃，不能落列或落 attrs |
| `codex.skill_invocation` | `codex_event_skill_invocation` | `turn.id`、`skill.name`、`skill.scope`、`skill.plugin_id`、`skill.invocation_type=explicit/implicit`；要求 conversation、turn、skill 名称 |
| `codex.turn_cost` | `codex_event_turn_cost` | `turn.id`、十进制字符串 `usage.estimated_usd`、`turn.interrupted`、`speed`、`reasoning_effort`；要求 conversation、turn 和有效非负金额 |

`response.completed` 也会承载错误。只有没有非空错误且 input/output 都存在、非负、子集计数合法、总量不溢出的记录，才进入 token、请求数与本地计价统计；有效零用量仍计作一次请求。错误记录保留用于诊断与会话活动。缺少 TTFT、缓存写入或截断标志保持 NULL，不伪造观测值。

`ttft_ms` 是开始读取响应流到首次输出（含推理输出）的延迟，不含此前的建连、响应头等待。API 给出样本数、均值、nearest-rank P50/P95；无样本为 NULL。`output_length` 是收到的 **输出预览** 的字节数，不能作为完整工具输出大小；NULL 截断标志不等于 false。

Skill 会话统计按 `(conversation_id, turn_id, skill_name, plugin_id, invocation_type)` 去重，分别计显式与隐式调用。全局排名仍使用成功的 Skill 注入 Metric，两者不相加。

范围外事件（计入 Unknown）包括启动阶段、WebSocket、sandbox、network proxy 及插件安装事件。

### 3.4 入库 Metrics（3 个）

| Metric | OTLP 形态 | 入库表 | 用途 |
|---|---|---|---|
| `codex.skill.injected` | monotonic DELTA Sum | `codex_metric_skill_injected` | 保存 `value`、`skill`、`status`、`invoke_type`、`session_source`；排名仅累计 `status=ok/success` |
| `codex.responses_api_engine_service_tbt.duration_ms` | DELTA Histogram | `codex_metric_response_tbt` | 保存 count/sum，速度估计 = `count × 1000 / sum_ms` |
| `codex.turn.token_usage` | DELTA Histogram | `codex_metric_turn_token_usage` | 保存 count/sum、原始 model、token_type、session_source，用于回合与响应用量核对 |

Histogram 不展开 bucket。TBT 是服务端相邻 token 时间的速度估计，与墙钟吞吐或 TTFT 分开展示。Metrics 没有稳定 request/turn ID，不与响应做时间模糊 join。

Turn 对账按原始模型与 `total/input/cached_input/cache_write_input/output/reasoning_output` 六种类型分别比较。差值为 Turn 减 response，不回写主统计；其中子集不能相加为总量。跨窗口、导出缺失或重传会造成差异；任一来源缺失时差值为 NULL。可选 token 字段分别统计有效样本数，未上报不能冒充零用量。

### 3.5 token 与成本口径

- Claude 总量 = `input + output + cacheRead + cacheCreation`。
- Codex 总量 = `input_token_count + output_token_count`；缓存读、缓存写是 input 的不重叠子集，reasoning 是 output 子集。
- Codex 普通输入 = input − cached − cache_write；缓存命中率 = cached / input。
- 本地计价：普通输入、缓存读、缓存写分别计价；缓存写使用 LiteLLM `cache_creation_input_token_cost`，缺失时退回普通输入价。显式零价保留。reasoning 只在 output 内拆分。

成本同时保留两个估算来源：原生 `codex.turn_cost` 与写入时冻结的本地 LiteLLM `cost_usd`。原生金额按字符串保存，并通过 DECIMAL 校验；相同 conversation+turn 选择最新事件（再按 received_at 排序），有效零金额不能当作缺失。

**先跨全历史按会话选择来源，再过滤查询时间窗口**：会话一旦有原生成本，就只用已观测到的原生成本；否则用本地估算。不用本地值补齐原生回合，覆盖率始终未知。原生金额归属其事件时间；迟到原生事件可能改变以前窗口的选源。未接入 `codex.turn.cost` Metric，以免与日志重复计费。

`pricing.enabled` 只控制价目表加载与新本地估算。关闭后，已保存的本地成本和原生成本仍可见。API 的 `cost_available` 区分未知与零，`cost_source` 区分 native/local/legacy/mixed/unknown，另提供 `native_turn_count`、`cost_coverage=unknown`、`legacy_cost_included`、`local_estimate_usd`（独立参考值）。Claude 自报成本仍是 authoritative。

原生成本由 Codex 后台查询计价端点后尽力上报，可能延迟、缺失或被中断，不是结算账单。TTFT 与 Turn 对账入口：`GET /api/usage/codex/telemetry?range=day|week|month`。

官方来源：[session telemetry](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/otel/src/events/session_telemetry.rs)、[Skill invocation](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/otel/src/skill_invocation.rs)、[Turn tokens](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/core/src/state/turn_token_usage.rs)。
