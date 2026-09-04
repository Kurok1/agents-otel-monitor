# CLAUDE.md

This file provides repository guidance for coding agents working in this repository.

---

## 项目概览

`agents-otel-monitor` 是一个 Go 实现的本地 AI coding harness OTEL 监控服务：

- **领域主语**：harness；当前对等支持 Claude Code 与 OpenAI Codex CLI，术语边界见 `CONTEXT.md`
- **输入**：各 harness 通过 **OTLP gRPC** 向同一个 4317 端口推送 Metrics / Events
- **存储**：DuckDB 单文件，按 harness 协议维护独立表族，**一指标 / 一事件 = 一张表**，共 **27 张表**
- **当前版本**：后端 ingest、查询 API 与 Web Dashboard 均已落地

| Harness family | 信号 | 表族 |
|---|---|---|
| `claude` | 8 Metrics + 11 Events | 19 张历史无前缀表（`metric_*` / `event_*`） |
| `codex` | 2 Metrics + 6 Events | 8 张 `codex_metric_*` / `codex_event_*` 表 |

仓库、发布归档和二进制使用 `agents-otel-monitor`；更新器兼容读取旧命名归档，统一安装为新文件名。Go module 与 `/version.service` 保留 `claude-code-monitor`，后者用于桌面端识别服务。变更发布物命名时同步更新打包脚本与更新器；模块与协议标识的迁移需单独明确范围。

---

## 必读文档

下面三份是架构与领域基线，新增 / 修改逻辑前按变更范围读取：

| 文档 | 内容 |
|---|---|
| `CONTEXT.md` | harness、harness family、harness telemetry 等领域词汇 |
| `docs/protocol.md` | Claude / Codex 指标与事件的 OTLP 数据结构、字段约束、取值范围 |
| `docs/models.md` | 27 张 DuckDB 表的完整 DDL + 公共列约定 + 写入要点 |

`docs/plan-*`、`docs/plans/` 与 `docs/superpowers/{plans,specs}/` 记录当时的设计和实施背景，可能包含旧仓库名或 Claude-only 阶段的事实；保留其历史语境。与当前行为冲突时，以 `CONTEXT.md`、`docs/protocol.md`、`docs/models.md` 和代码为准。

---

## 架构脉络

数据流（**理解此图等于理解整个系统**）：

```
Claude Code                OpenAI Codex CLI
     │                           │
     └──────── OTLP/gRPC :4317 ──┘
        ▼
[MetricsServiceServer]  [LogsServiceServer]    ← internal/otlp/*_service.go
        │                       │
        │  ExportRequest        │  ExportRequest
        ▼                       ▼
              [Dispatcher]                     ← internal/otlp/dispatch.go
                    │
       按 harness 协议及 metric.name / event.name 路由
                    │
        ┌───────────┴───────────┐
        ▼                       ▼
 Claude parsers            Codex parsers       ← internal/otlp/{metrics,events,codex_events}.go
 → legacy table rows       → codex_* rows       （27 个强类型 row struct）
        │                       │
        └───────────┬───────────┘
                    ▼
              [Sink interface]
                    │
                    ▼
            [BufferedWriter]                    ← internal/store/writer.go
                    │
       tableNameFor(row) → 27 个 TableBuffer
                    │
       触发：batch_size 行 或 flush_interval
                    ▼
              [Appender]                        ← internal/store/appender.go
                    │
                    ▼
                 DuckDB                          单文件，单写者
```

**关键不变量**：
- DuckDB **不能跨进程并发写**，应用层全局 mutex 串行化所有 flush
- 解析器输出的 row struct 字段顺序**必须**与 DDL 列顺序对齐，因为 Appender 是位置式 API
- 公共属性（user.id / session.id / model 等）在 Resource 层和数据点层都可能出现，**数据点层覆盖 Resource 层**
- 未识别的 metric / event → `unknown` 日志，不报错；未识别的 attribute → 落入 `attrs JSON` 列
- Claude 表族 `user.id` 是硬性 NOT NULL 前提；**Codex 表族无身份硬约束**（user_account_id / user_email 可空）
- **Codex 隐私红线**：`codex.tool_result` 的 `arguments` / `output` 原文在解析层只算长度即丢弃，不落列也不落 attrs
- Codex 时间戳三级回退：`time_unix_nano`（恒为 0）→ `observed_time_unix_nano` → `event.timestamp` attribute

---

## 常用命令

```bash
# 构建与运行
go build -o bin/server ./cmd/server
./bin/server -config config.yaml

# 测试
go test ./...
go test -race ./...
go test ./internal/otlp/ -run TestParseTokenUsage -v   # 单个测试

# 静态检查
go vet ./...
gofmt -w .
goimports -w .

# 模块管理
go mod tidy
go mod verify

# DuckDB 数据验证（需安装 duckdb CLI）
duckdb ./data/monitor.duckdb "SELECT table_name FROM duckdb_tables() ORDER BY 1;"
duckdb ./data/monitor.duckdb "SELECT COUNT(*) FROM metric_token_usage;"
```

端到端采集配置见 `README.md` 的“配置 harness 遥测”：Claude Code 可直接 `source scripts/claude-env.sh`，Codex CLI 需配置 `~/.codex/config.toml` 的 `[otel]` 段。

---

## Go 开发规范（本项目专用）

### 类型纪律

- **避免 `any` / `interface{}`**：仅以下两处允许使用：
  - 27 张表的 `attrs` 兜底列（`map[string]any`），用于未识别的 OTLP attribute
  - `Sink.AppendMetric(row any)` / `AppendEvent(row any)`：内部立刻 type-switch 到具体 row struct
- **每张表对应一个 row struct**（共 27 个），字段名与列名一一对应；不用 `map[string]string` 代替
- **可空字段用 `sql.NullXxx`**，直接喂给 go-duckdb Appender，避免双层零值检查
- 时间统一 `time.Time`（UTC），OTLP 纳秒值除 1000 转微秒精度

### 函数签名

- **接口入参，具体类型出参**（Effective Go 原则）
- **`context.Context` 始终是第一个参数**，不要塞进 struct
- **同一函数参数 > 4 个时用 struct 包装**

```go
// 不推荐
func NewBuffer(name string, app Appender, size int, interval time.Duration, hardLimit int) *TableBuffer

// 推荐
func NewBuffer(name string, app Appender, cfg config.IngestConfig) *TableBuffer
```

### 错误处理

- **始终 wrap 错误并加上下文**：`fmt.Errorf("parse %s: %w", name, err)`
- **启动期错误 fail-fast**（配置 / 迁移 / 监听失败）：日志 + 非零退出码
- **运行期错误分级**：
  - 单条 OTLP 数据点解析失败 → warn + 跳过本行 + `summary.Errors++`，**不中断**整批
  - 单次 `appender.Flush()` 失败 → error 日志 + **保留 buffer 等下次重试**
  - 连续 N 次 flush 失败 → 升级告警，进入 degraded 模式
- **不要忽略错误**：除非真的不在乎（如 `defer w.Close()`），需注释说明
- **不要用 panic 做控制流**：只在不可恢复的初始化错误中用 `log.Fatal`

### 并发与生命周期

- **DuckDB 单写者**：`*sql.DB.SetMaxOpenConns(1)`，所有 flush 通过应用层 mutex 串行
- **goroutine 不能裸开**：必须有明确的退出路径（context cancel / channel close / WaitGroup）
- **优雅关闭顺序**：
  1. `grpcServer.GracefulStop()`（接受信号后）
  2. `writer.Stop()`（停 ticker → flush 全部 buffer → close Appender）
  3. `db.Close()`
  4. 整体超时 30s，超过强制 `Stop()`

### 包组织

```
cmd/server/                    # 入口，只做 wire-up，不放业务逻辑
internal/config/               # YAML 解析 + 默认值 + 校验
internal/store/                # DuckDB 连接、迁移、Appender、Buffer、Writer
internal/otlp/                 # 协议层：Server、Service、Parser、Dispatcher、Row 结构体
internal/ingest/               # （如需）粘合 dispatcher 与 writer
internal/logging/              # slog 配置
internal/stats/                # 自监控端点
```

- **interface 在消费方包定义**（如 `Sink` 在 `internal/otlp` 中），实现可在另一个包
- **没有 `pkg/`**：本项目不对外提供 API
- **避免包级可变全局变量**：DB / config / logger 通过参数注入

### 日志

- 统一 `log/slog`，默认 JSON handler，可切 text
- **键名 snake_case**：`"user_id"` / `"flush_errors"`，不用 camelCase
- **错误用键传递**：`slog.Error("flush failed", "table", name, "err", err)`，**不要**把错误拼进 message
- 频次约束：每个 OTLP Export 一条 `Info` 摘要；逐行 / 逐数据点用 `Debug`

### 测试

- **golden testdata 用真实 protobuf**：从隔离采集取样；含敏感 attribute 时先脱敏并重新 marshal，可用 `.pb.b64` 做二进制安全存放，不要手写 OTLP 消息
- **每个 parser 至少一个 golden case**；典型分支（accept/reject、success/error）各一例
- **`go test -race` 默认开启**：buffer 的并发路径必须无竞争
- 不写无意义的 mock 框架，简单 `countingSink` / `fakeAppender` 足够

### 性能与内存

- **切片预分配**：已知容量（如 `make([]any, 0, batchSize)`）必须用 `make` 而非裸 `append`
- **字符串拼接**：在循环里用 `strings.Builder`，单点拼接用 `+` 即可
- **不要过早优化**：先确认是热路径再上 `sync.Pool` 等手段

---

## 反模式（本项目禁止）

```go
// ❌ 用 map 代替 row struct
type Row map[string]any

// ❌ 全局可变 DB
var db *sql.DB
func init() { db, _ = sql.Open(...) }

// ❌ 忽略错误
result, _ := proto.Marshal(req)

// ❌ panic 替代错误返回
func parseToken(...) Row {
    if dp.Value == nil { panic("nil value") }
}

// ❌ Context 塞进 struct
type Request struct { ctx context.Context; ... }

// ❌ 解析时直接写库（破坏分层边界）
func (s *MetricsService) Export(...) {
    row := parseTokenUsage(dp)
    db.Exec("INSERT ...")    // 错：应通过 Sink
}

// ❌ 在循环里频繁开关 Appender
for _, row := range rows {
    app, _ := duckdb.NewAppender(...)
    app.AppendRow(row...)
    app.Close()
}
```

---

## 决策回溯锚点

以下决策已经拍板，**请勿在未经讨论的情况下改动**：

| 决策 | 理由 |
|---|---|
| 仅支持 gRPC，不支持 HTTP/protobuf | gRPC 已覆盖默认场景，stub 直接给 Export 签名，实现更简洁 |
| YAML 单一配置源，不引入 koanf / viper | 单一来源够用，`yaml.v3` 直接 Unmarshal 即可 |
| 一指标/事件一表（27 张窄表） | 避免大宽表 schema 漂移，详见 `docs/models.md` §1 |
| 未识别字段进 `attrs JSON` 兜底 | 任一受支持 harness 升级新增 attribute 时无需立即迁移 |
| Query API 推迟到 v2 | 没有前端需求时定义 API 容易过度设计 |
| `TIMESTAMP` 微秒精度而非 `TIMESTAMP_NS` | 兼容外部 BI 工具；详见 `docs/models.md` §5.1 |
| 全局 mutex 串行 flush | DuckDB 单写者约束；监控吞吐远低于其极限，简单优先 |
| 背压策略：丢最旧 + 日志，不反压 harness | OTLP SDK 自身就会丢，监控不能阻塞本地 harness |
| 各 harness 保留协议原生表族，查询层再统一 | 不把一个 harness 的身份、token 或事件语义强套到另一个；Claude 使用历史无前缀表，Codex 使用 `codex_*` 表（见 spec 2026-07-01） |
| Codex `tool_result` 原文只存长度 | Codex 默认不脱敏且无客户端开关，敏感内容不落盘 |
| Codex 接 6 个核心 Logs 事件及 Dashboard 所需的 Skill / service TBT Metrics | Skill 用 monotonic DELTA Sum；TBT 用 DELTA Histogram 且只保存 count/sum，不展开 bucket；其余高容量 metrics 与 sandbox / network_proxy 等事件无 Dashboard 需求 |
| Codex/第三方成本由 `internal/pricing` 在 ingest 时按 LiteLLM 计价表**估算** `cost_usd`（v2.4.0，反转早期「不估算」非目标）；Claude 仍用自报权威成本 | Codex 不上报 cost；用外部计价表估算填补，默认关闭零影响，单价写入时冻结不回填（见 spec/plan 2026-07-02-third-party-cost-estimation） |

## Agent skills

### Issue tracker

Issues and specs use local Markdown under `.scratch/`. See `docs/agents/issue-tracker.md`.

### Triage labels

The canonical Matt triage labels are used unchanged. See `docs/agents/triage-labels.md`.

### Domain docs

This repository uses the single-context domain-doc layout. See `docs/agents/domain.md`.
