# DuckDB 数据模型

每个指标 / 事件单独建一张窄表，共 **27 张表**：

- Claude Code：8 张 metric 表（前缀 `metric_`）+ 11 张 event 表（前缀 `event_`）
- Codex CLI：2 张 metric 表（前缀 `codex_metric_`）+ 6 张 event 表（前缀 `codex_event_`），见 §7

设计原则：
1. **一指标 / 一事件 = 一张表**，避免大宽表 schema 漂移
2. 高频查询维度提到顶层列；其余 attribute 落入 `attrs VARCHAR`，保证 Claude Code 升级新增字段时无需迁移
3. 列名使用 `snake_case`，OTLP 中带 `.` 的字段（如 `agent.name`）映射为下划线（`agent_name`）
4. 时间戳统一使用 `TIMESTAMP`（微秒精度），从 OTLP 的 `time_unix_nano` 转换得到
5. 字符串枚举一律 `VARCHAR`，不使用 DuckDB `ENUM` 类型（演进成本高）

---

## 1. 公共列约定

### 1.1 所有表共有

| 列 | 类型 | NULL | 说明 |
|---|---|---|---|
| `ts` | TIMESTAMP | NOT NULL | OTLP `time_unix_nano` 转换得到 |
| `received_at` | TIMESTAMP | NOT NULL DEFAULT `now()` | 接收端入库时刻 |
| `session_id` | VARCHAR | NULL | `session.id` |
| `user_id` | VARCHAR | NOT NULL | `user.id` 必存在 |
| `user_account_uuid` | VARCHAR | NULL | `user.account_uuid` |
| `user_account_id` | VARCHAR | NULL | `user.account_id`（tagged 格式） |
| `user_email` | VARCHAR | NULL | `user.email` |
| `organization_id` | VARCHAR | NULL | `organization.id` |
| `app_version` | VARCHAR | NULL | `app.version` |
| `terminal_type` | VARCHAR | NULL | `terminal.type` |
| `attrs` | VARCHAR | NULL | 未识别 attribute 兜底，存 JSON 文本（`json_extract_string()` 等函数仍可用） |

### 1.2 Metric 表额外共有

| 列 | 类型 | NULL | 说明 |
|---|---|---|---|
| `start_ts` | TIMESTAMP | NOT NULL | OTLP `start_time_unix_nano`，delta interval 起点 |
| `value` | BIGINT 或 DOUBLE | NOT NULL | delta 数值，类型见各表 |

### 1.3 Event 表额外共有

| 列 | 类型 | NULL | 说明 |
|---|---|---|---|
| `event_sequence` | BIGINT | NULL | 会话内单调递增序号 |
| `prompt_id` | VARCHAR | NULL | `prompt.id` |
| `workspace_host_paths` | VARCHAR[] | NULL | `workspace.host_paths` |

---

## 2. Metric 表（8 张）

### 2.1 `metric_session_count`

```sql
CREATE TABLE metric_session_count (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT now(),
    value              BIGINT    NOT NULL,
    -- 公共属性
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    -- 特有属性
    start_type         VARCHAR,        -- fresh / resume / continue
    attrs              VARCHAR
);
```

### 2.2 `metric_lines_of_code_count`

```sql
CREATE TABLE metric_lines_of_code_count (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT now(),
    value              BIGINT    NOT NULL,
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    type               VARCHAR,        -- added / removed
    attrs              VARCHAR
);
```

### 2.3 `metric_pull_request_count`

```sql
CREATE TABLE metric_pull_request_count (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT now(),
    value              BIGINT    NOT NULL,
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    attrs              VARCHAR
);
```

### 2.4 `metric_commit_count`

```sql
CREATE TABLE metric_commit_count (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT now(),
    value              BIGINT    NOT NULL,
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    attrs              VARCHAR
);
```

### 2.5 `metric_cost_usage`

```sql
CREATE TABLE metric_cost_usage (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT now(),
    value              DOUBLE    NOT NULL,   -- USD
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    model              VARCHAR,
    query_source       VARCHAR,        -- main / subagent / auxiliary
    speed              VARCHAR,        -- fast / NULL
    effort             VARCHAR,        -- low / medium / high / xhigh / max / NULL
    agent_name         VARCHAR,
    skill_name         VARCHAR,
    plugin_name        VARCHAR,
    marketplace_name   VARCHAR,
    attrs              VARCHAR
);
```

### 2.6 `metric_token_usage`

```sql
CREATE TABLE metric_token_usage (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT now(),
    value              BIGINT    NOT NULL,
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    type               VARCHAR,        -- input / output / cacheRead / cacheCreation
    model              VARCHAR,
    query_source       VARCHAR,
    speed              VARCHAR,
    effort             VARCHAR,
    agent_name         VARCHAR,
    skill_name         VARCHAR,
    plugin_name        VARCHAR,
    marketplace_name   VARCHAR,
    attrs              VARCHAR
);
```

### 2.7 `metric_code_edit_tool_decision`

```sql
CREATE TABLE metric_code_edit_tool_decision (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT now(),
    value              BIGINT    NOT NULL,
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    tool_name          VARCHAR,        -- Edit / Write / NotebookEdit
    decision           VARCHAR,        -- accept / reject
    source             VARCHAR,        -- config / hook / user_permanent / user_temporary / user_abort / user_reject
    language           VARCHAR,
    attrs              VARCHAR
);
```

### 2.8 `metric_active_time_total`

```sql
CREATE TABLE metric_active_time_total (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT now(),
    value              DOUBLE    NOT NULL,   -- 秒，可能小数
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    type               VARCHAR,        -- user / cli
    attrs              VARCHAR
);
```

---

## 3. Event 表 — 第一梯队（5 张）

### 3.1 `event_user_prompt`

```sql
CREATE TABLE event_user_prompt (
    ts                    TIMESTAMP NOT NULL,
    received_at           TIMESTAMP NOT NULL DEFAULT now(),
    event_sequence        BIGINT,
    prompt_id             VARCHAR,
    workspace_host_paths  VARCHAR[],
    session_id            VARCHAR,
    user_id               VARCHAR   NOT NULL,
    user_account_uuid     VARCHAR,
    user_account_id       VARCHAR,
    user_email            VARCHAR,
    organization_id       VARCHAR,
    app_version           VARCHAR,
    terminal_type         VARCHAR,
    prompt_length         INTEGER,
    prompt                VARCHAR,        -- 默认 NULL，需 OTEL_LOG_USER_PROMPTS=1
    command_name          VARCHAR,
    command_source        VARCHAR,        -- builtin / custom / mcp
    attrs                 VARCHAR
);
```

### 3.2 `event_api_request`

```sql
CREATE TABLE event_api_request (
    ts                     TIMESTAMP NOT NULL,
    received_at            TIMESTAMP NOT NULL DEFAULT now(),
    event_sequence         BIGINT,
    prompt_id              VARCHAR,
    workspace_host_paths   VARCHAR[],
    session_id             VARCHAR,
    user_id                VARCHAR   NOT NULL,
    user_account_uuid      VARCHAR,
    user_account_id        VARCHAR,
    user_email             VARCHAR,
    organization_id        VARCHAR,
    app_version            VARCHAR,
    terminal_type          VARCHAR,
    model                  VARCHAR,
    cost_usd               DOUBLE,
    duration_ms            BIGINT,
    input_tokens           BIGINT,
    output_tokens          BIGINT,
    cache_read_tokens      BIGINT,
    cache_creation_tokens  BIGINT,
    request_id             VARCHAR,
    speed                  VARCHAR,        -- fast / normal
    query_source           VARCHAR,
    effort                 VARCHAR,
    attrs                  VARCHAR
);
```

### 3.3 `event_api_error`

```sql
CREATE TABLE event_api_error (
    ts                    TIMESTAMP NOT NULL,
    received_at           TIMESTAMP NOT NULL DEFAULT now(),
    event_sequence        BIGINT,
    prompt_id             VARCHAR,
    workspace_host_paths  VARCHAR[],
    session_id            VARCHAR,
    user_id               VARCHAR   NOT NULL,
    user_account_uuid     VARCHAR,
    user_account_id       VARCHAR,
    user_email            VARCHAR,
    organization_id       VARCHAR,
    app_version           VARCHAR,
    terminal_type         VARCHAR,
    model                 VARCHAR,
    error                 VARCHAR,
    status_code           INTEGER,
    duration_ms           BIGINT,
    attempt               INTEGER,
    request_id            VARCHAR,
    speed                 VARCHAR,
    query_source          VARCHAR,
    effort                VARCHAR,
    attrs                 VARCHAR
);
```

### 3.4 `event_tool_result`

```sql
CREATE TABLE event_tool_result (
    ts                       TIMESTAMP NOT NULL,
    received_at              TIMESTAMP NOT NULL DEFAULT now(),
    event_sequence           BIGINT,
    prompt_id                VARCHAR,
    workspace_host_paths     VARCHAR[],
    session_id               VARCHAR,
    user_id                  VARCHAR   NOT NULL,
    user_account_uuid        VARCHAR,
    user_account_id          VARCHAR,
    user_email               VARCHAR,
    organization_id          VARCHAR,
    app_version              VARCHAR,
    terminal_type            VARCHAR,
    tool_name                VARCHAR,
    tool_use_id              VARCHAR,
    success                  BOOLEAN,
    duration_ms              BIGINT,
    error_type               VARCHAR,
    error                    VARCHAR,        -- 需 OTEL_LOG_TOOL_DETAILS=1
    decision_type            VARCHAR,        -- accept / reject
    decision_source          VARCHAR,
    tool_input_size_bytes    BIGINT,
    tool_result_size_bytes   BIGINT,
    mcp_server_scope         VARCHAR,
    tool_parameters          VARCHAR,           -- 需 OTEL_LOG_TOOL_DETAILS=1
    tool_input               VARCHAR,           -- 需 OTEL_LOG_TOOL_DETAILS=1
    attrs                    VARCHAR
);
```

### 3.5 `event_tool_decision`

```sql
CREATE TABLE event_tool_decision (
    ts                    TIMESTAMP NOT NULL,
    received_at           TIMESTAMP NOT NULL DEFAULT now(),
    event_sequence        BIGINT,
    prompt_id             VARCHAR,
    workspace_host_paths  VARCHAR[],
    session_id            VARCHAR,
    user_id               VARCHAR   NOT NULL,
    user_account_uuid     VARCHAR,
    user_account_id       VARCHAR,
    user_email            VARCHAR,
    organization_id       VARCHAR,
    app_version           VARCHAR,
    terminal_type         VARCHAR,
    tool_name             VARCHAR,
    tool_use_id           VARCHAR,        -- 与 event_tool_result 关联
    decision              VARCHAR,        -- accept / reject
    source                VARCHAR,        -- config / hook / user_permanent / user_temporary / user_abort / user_reject
    attrs                 VARCHAR
);
```

---

## 4. Event 表 — 第二梯队（6 张）

### 4.1 `event_api_retries_exhausted`

```sql
CREATE TABLE event_api_retries_exhausted (
    ts                       TIMESTAMP NOT NULL,
    received_at              TIMESTAMP NOT NULL DEFAULT now(),
    event_sequence           BIGINT,
    prompt_id                VARCHAR,
    workspace_host_paths     VARCHAR[],
    session_id               VARCHAR,
    user_id                  VARCHAR   NOT NULL,
    user_account_uuid        VARCHAR,
    user_account_id          VARCHAR,
    user_email               VARCHAR,
    organization_id          VARCHAR,
    app_version              VARCHAR,
    terminal_type            VARCHAR,
    model                    VARCHAR,
    error                    VARCHAR,
    status_code              INTEGER,
    total_attempts           INTEGER,
    total_retry_duration_ms  BIGINT,
    speed                    VARCHAR,
    attrs                    VARCHAR
);
```

### 4.2 `event_compaction`

```sql
CREATE TABLE event_compaction (
    ts                    TIMESTAMP NOT NULL,
    received_at           TIMESTAMP NOT NULL DEFAULT now(),
    event_sequence        BIGINT,
    prompt_id             VARCHAR,
    workspace_host_paths  VARCHAR[],
    session_id            VARCHAR,
    user_id               VARCHAR   NOT NULL,
    user_account_uuid     VARCHAR,
    user_account_id       VARCHAR,
    user_email            VARCHAR,
    organization_id       VARCHAR,
    app_version           VARCHAR,
    terminal_type         VARCHAR,
    trigger               VARCHAR,        -- auto / manual
    success               BOOLEAN,
    duration_ms           BIGINT,
    pre_tokens            BIGINT,
    attrs                 VARCHAR
);
```

### 4.3 `event_permission_mode_changed`

```sql
CREATE TABLE event_permission_mode_changed (
    ts                    TIMESTAMP NOT NULL,
    received_at           TIMESTAMP NOT NULL DEFAULT now(),
    event_sequence        BIGINT,
    prompt_id             VARCHAR,
    workspace_host_paths  VARCHAR[],
    session_id            VARCHAR,
    user_id               VARCHAR   NOT NULL,
    user_account_uuid     VARCHAR,
    user_account_id       VARCHAR,
    user_email            VARCHAR,
    organization_id       VARCHAR,
    app_version           VARCHAR,
    terminal_type         VARCHAR,
    from_mode             VARCHAR,        -- default / plan / acceptEdits / auto / bypassPermissions
    to_mode               VARCHAR,
    trigger               VARCHAR,        -- shift_tab / exit_plan_mode / auto_gate_denied / auto_opt_in
    attrs                 VARCHAR
);
```

### 4.4 `event_mcp_server_connection`

```sql
CREATE TABLE event_mcp_server_connection (
    ts                    TIMESTAMP NOT NULL,
    received_at           TIMESTAMP NOT NULL DEFAULT now(),
    event_sequence        BIGINT,
    prompt_id             VARCHAR,
    workspace_host_paths  VARCHAR[],
    session_id            VARCHAR,
    user_id               VARCHAR   NOT NULL,
    user_account_uuid     VARCHAR,
    user_account_id       VARCHAR,
    user_email            VARCHAR,
    organization_id       VARCHAR,
    app_version           VARCHAR,
    terminal_type         VARCHAR,
    status                VARCHAR,        -- connected / failed / disconnected
    transport_type        VARCHAR,        -- stdio / sse / http
    server_scope          VARCHAR,        -- user / project / local
    duration_ms           BIGINT,
    error_code            VARCHAR,
    server_name           VARCHAR,        -- 需 OTEL_LOG_TOOL_DETAILS=1
    error                 VARCHAR,        -- 需 OTEL_LOG_TOOL_DETAILS=1
    attrs                 VARCHAR
);
```

### 4.5 `event_skill_activated`

```sql
CREATE TABLE event_skill_activated (
    ts                    TIMESTAMP NOT NULL,
    received_at           TIMESTAMP NOT NULL DEFAULT now(),
    event_sequence        BIGINT,
    prompt_id             VARCHAR,
    workspace_host_paths  VARCHAR[],
    session_id            VARCHAR,
    user_id               VARCHAR   NOT NULL,
    user_account_uuid     VARCHAR,
    user_account_id       VARCHAR,
    user_email            VARCHAR,
    organization_id       VARCHAR,
    app_version           VARCHAR,
    terminal_type         VARCHAR,
    skill_name            VARCHAR,
    invocation_trigger    VARCHAR,        -- user-slash / claude-proactive / nested-skill
    skill_source          VARCHAR,        -- bundled / userSettings / projectSettings / plugin
    plugin_name           VARCHAR,
    marketplace_name      VARCHAR,
    attrs                 VARCHAR
);
```

### 4.6 `event_at_mention`

```sql
CREATE TABLE event_at_mention (
    ts                    TIMESTAMP NOT NULL,
    received_at           TIMESTAMP NOT NULL DEFAULT now(),
    event_sequence        BIGINT,
    prompt_id             VARCHAR,
    workspace_host_paths  VARCHAR[],
    session_id            VARCHAR,
    user_id               VARCHAR   NOT NULL,
    user_account_uuid     VARCHAR,
    user_account_id       VARCHAR,
    user_email            VARCHAR,
    organization_id       VARCHAR,
    app_version           VARCHAR,
    terminal_type         VARCHAR,
    mention_type          VARCHAR,        -- file / directory / agent / mcp_resource
    success               BOOLEAN,
    attrs                 VARCHAR
);
```

---

## 5. 写入与查询要点

### 5.1 时间戳

OTLP 协议使用 `int64` 纳秒精度 (`time_unix_nano`)。DuckDB 默认 `TIMESTAMP` 为微秒精度，转换时除以 1000 即可。如需保留纳秒精度可改用 `TIMESTAMP_NS`，但会牺牲与外部 BI 工具的兼容性。本项目采用 `TIMESTAMP` (微秒)。

### 5.2 批量写入

DuckDB 单条 INSERT 性能较差，必须批量化：
- 接收端按 `(表名)` 分桶缓冲
- 每批 ≥ 500 行或 ≥ 5 秒触发 flush
- 使用 `Appender` API（go-duckdb 提供）或 `INSERT INTO ... VALUES (...), (...), ...`

### 5.3 写入顺序

按 `ts` 单调递增写入可获得最优 zone map 效果。OTLP delta 数据通常已按时间到达，乱序窗口应该不大。

### 5.4 索引

DuckDB 索引（ART index）对写入性能影响显著，**默认不创建索引**，依赖列存 + zone map。仅在以下场景考虑：
- 单点查询 `request_id` / `tool_use_id` / `prompt_id` → 可加 ART 索引
- `user_id` / `session_id` 等等值过滤 → 通常 zone map + filter pushdown 足够

```sql
-- 仅在确实有点查需求时执行
CREATE INDEX idx_event_api_request_request_id ON event_api_request(request_id);
CREATE INDEX idx_event_tool_result_tool_use_id ON event_tool_result(tool_use_id);
```

### 5.5 关联查询

跨表关联主要靠：
- `prompt_id`：串联同一 prompt 的 user_prompt / api_request / tool_result
- `tool_use_id`：串联 tool_decision ↔ tool_result
- `session_id` + `ts` 范围：单会话时间线
- `request_id`：同一 API 请求的 cost / token 指标与 api_request / api_error 事件对齐（注意：metrics 没有 request_id，无法直接 join，只能按 session_id + 时间窗近似关联）

### 5.6 数据保留

DuckDB 单文件长期增长后查询性能下降。建议：
- 热数据保留在主 `.duckdb` 文件
- 老数据（如 > 90 天）`COPY ... TO 'archive/<table>_<yyyymm>.parquet'`，然后 `DELETE FROM ...`
- 查询时按需 `read_parquet('archive/*.parquet')` 联合查询

---

### 5.7 用量热点图（heatmap）

`GET /api/usage/heatmap` 复用现有三张表按本地日（`date_trunc('day', ts + INTERVAL N HOUR)`）聚合，**无需新增表 / 迁移**：

- Token：`SUM(value)` from `metric_token_usage`
- 费用：`SUM(value)` from `metric_cost_usage`
- 请求：`COUNT(*)` from `event_api_request`

固定取最近 360 个本地日（含今日），逐日补零。每天的综合强度 `score ∈ [0,1]` 在 Go 侧计算：各指标按 360 天窗口内最大值归一化（min 固定为 0），再用 `config.yaml` 的 `dashboard.heatmap` 权重加权后除以权重和。前端按分位数把 `score` 分成 5 档着色。

> 注意：`docs §5.6` 的归档策略（>90 天导出 parquet 后 `DELETE`）一旦实装，360 天热点图需要 `UNION read_parquet(...)` 才能覆盖完整窗口；当前归档未实装，故暂不受影响。

## 6. 演进策略

新增 Claude Code 版本带来的字段处理流程：

1. **未识别 attribute** 自动落入 `attrs VARCHAR`，前端 / API 可临时查询
2. 验证为高频维度后，执行 `ALTER TABLE ... ADD COLUMN <name> <type>;`
3. 接收端代码补充提取逻辑，新数据写入新列
4. 老数据保留在 `attrs` 内不回填（DuckDB 也可批量 UPDATE 回填，按需）

新增整张表（新指标 / 新事件）：

1. `internal/store/migrations/` 增加 `NNN_add_<table>.sql`
2. 启动时按版本号顺序执行迁移
3. 接收端 dispatcher 增加对应路由

---

## 7. Codex 表（8 张）

来自 OpenAI Codex CLI 的 OTEL Logs 与选定 Metrics（协议见 [`protocol.md`](./protocol.md) §7），DDL 在 `internal/store/migrations/003_codex_event_tables.sql`、`005_codex_skill_metrics.sql`、`006_codex_response_tbt_metric.sql`。

### 7.1 公共列（codex 表族）

| 列 | 类型 | NULL | 来源 |
|---|---|---|---|
| `ts` | TIMESTAMP | NOT NULL | `time_unix_nano` → `observed_time_unix_nano` → `event.timestamp` 三级回退（Codex 不设置 time_unix_nano） |
| `received_at` | TIMESTAMP | NOT NULL DEFAULT now() | 入库时刻 |
| `conversation_id` | VARCHAR | NULL | `conversation.id` |
| `app_version` | VARCHAR | NULL | `app.version` |
| `auth_mode` | VARCHAR | NULL | `ApiKey` / `Chatgpt` |
| `originator` | VARCHAR | NULL | `codex_cli_rs` / `codex_exec` / `codex_vscode` 等 |
| `terminal_type` | VARCHAR | NULL | `terminal.type` |
| `model` | VARCHAR | NULL | `model` |
| `slug` | VARCHAR | NULL | `slug` |
| `user_account_id` | VARCHAR | NULL | `user.account_id` |
| `user_email` | VARCHAR | NULL | `user.email` |
| `attrs` | VARCHAR | NULL | 未识别 attribute 兜底（JSON 文本） |

**与 Claude 表族的关键差异**：无 `user_id NOT NULL` 约束（Codex 身份字段天然可空），统一身份在查询层做 `COALESCE(user_account_id, user_email, 'unknown')`。

Metric 表额外包含 `start_ts`；Codex Metrics 当前不保证携带 `conversation.id`，因此会话级 Skill / 速度不做推断关联。

### 7.2 Metric 表

| 表 | 专有列 |
|---|---|
| `codex_metric_skill_injected` | `start_ts`、`value` BIGINT、`skill`、`status`、`invoke_type`、`session_source` |
| `codex_metric_response_tbt` | `start_ts`、`sample_count` BIGINT、`sum_ms` DOUBLE、`session_source` |

TBT 表不保存 Histogram 的 explicit bounds / bucket counts。一个 OTLP HistogramDataPoint 恒落一行，查询只按时间与模型聚合 `SUM(sample_count)` / `SUM(sum_ms)`，避免按 token 或 bucket 放大存储量。

### 7.3 Event 表特有列

| 表 | 特有列 |
|---|---|
| `codex_event_conversation_starts` | `provider_name`、`reasoning_effort`、`reasoning_summary`、`context_window` BIGINT、`auto_compact_token_limit` BIGINT、`approval_policy`、`sandbox_policy`、`mcp_servers` |
| `codex_event_api_request` | `duration_ms` BIGINT、`status_code` INTEGER、`error`、`attempt` BIGINT、`endpoint` |
| `codex_event_token_usage` | `input_token_count` / `output_token_count` / `cached_token_count` / `reasoning_token_count` / `tool_token_count` BIGINT、`service_tier`、`model_reasoning_effort`、`duration_ms` BIGINT、`cost_usd` DOUBLE（v2.4.0；ingest 时由 `internal/pricing` 按 LiteLLM 计价表估算，未匹配/计价关闭时为 NULL） |
| `codex_event_user_prompt` | `prompt_length` INTEGER、`prompt`（默认 `[REDACTED]`） |
| `codex_event_tool_decision` | `tool_name`、`call_id`、`decision`（Codex 原始枚举）、`source` |
| `codex_event_tool_result` | `tool_name`、`call_id`、`duration_ms` BIGINT、`success` BOOLEAN、`mcp_server`、`mcp_server_origin`、`arguments_length` / `output_length` BIGINT |

### 7.4 写入要点

- `codex.skill.injected` 只接受 monotonic DELTA Sum；Dashboard 仅统计 `status=ok/success`
- `codex.responses_api_engine_service_tbt.duration_ms` 只接受 DELTA Histogram；显式 bucket 在解析后丢弃
- `codex.sse_event` 仅 `event.kind = response.completed` 写入 `codex_event_token_usage`，其余 kind 由 dispatcher 计入 skipped，不持久化
- `codex_event_tool_result` 的 `arguments` / `output` 原文在解析层即丢弃（只算字节长度），**不落任何列也不落 `attrs`**——Codex 默认不脱敏且无客户端开关，这是接收端的隐私红线
- token 统计口径为子集式（`cached ⊂ input`、`reasoning ⊂ output`），与 Claude 并列式不同：Codex 总量 = `input_token_count + output_token_count`，不可再加 cached
- Codex 不上报成本；`cost_usd` 由 `internal/pricing` 在 ingest 时按 LiteLLM 计价表**估算**后落列（默认关闭，见 §配置 `pricing`）。Claude 仍用自报的权威 `cost_usd`（`metric_cost_usage`），不重算；两族表互不交叉
