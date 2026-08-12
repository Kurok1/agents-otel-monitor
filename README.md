# claude-code-monitor

Go 实现的 Claude Code / OpenAI Codex 监控服务。通过 **OTLP gRPC** 接收两种客户端的 Metrics 与 Events，落到本地 DuckDB（一指标 / 一事件 = 一张表，共 **27 张表**），便于后续做聚合分析与可视化。

---

## 架构

```
Claude Code / Codex (gRPC)
        │  :4317
        ▼
[OTLP MetricsService / LogsService]
        │
        ▼
[Dispatcher]  → 按 metric.name / event.name 路由到 27 个强类型 row 结构体
        │
        ▼
[BufferedWriter]  → 每张表独立 buffer + DuckDB Appender
        │            按 batch_size 或 flush_interval 触发
        ▼
   DuckDB 单文件
```

更深的设计依据见：
- [`docs/protocol.md`](docs/protocol.md) — OTLP 指标 / 事件字段规范
- [`docs/models.md`](docs/models.md) — DuckDB 表结构 + 写入要点
- [`CLAUDE.md`](CLAUDE.md) — 项目开发规范与不变量

---

## 快速开始

### 1. 构建

仅后端：
```bash
go build -o bin/server ./cmd/server
```

依赖 CGO（go-duckdb），首次构建会拉取 DuckDB 静态库。

> Release 预编译二进制覆盖 linux-amd64 / linux-arm64 / darwin-arm64；**Intel Mac（darwin-amd64）请自行 `go build` 编译**（GitHub Actions 的 macos-13 runner 队列不稳定，已从 release matrix 移除）；**Windows 请使用 Docker 镜像 `ghcr.io/kurok1/claude-code-monitor`**（runner 的 mingw gcc ≥ 13 与 duckdb 预编译静态库存在 emutls 符号不兼容，windows-amd64 已于 v2.2.0 移出 release matrix）。

含前端（一键打前端 + Go 嵌入产出单二进制）：
```bash
./scripts/build-all.sh
```

脚本内部依次：`npm install && npm run build`（产物写入 `internal/web/dist/`）→ `go build -trimpath -o bin/server ./cmd/server`（用 `//go:embed` 嵌入 dist）。

### 2. 启动 server

复制配置示例并按需修改：
```bash
cp config.example.yaml config.yaml
./bin/server -config config.yaml
```

启动后会看到：
```
buffered writer ready  tables=27
stats server listening addr=127.0.0.1:9100 web_ui=true
grpc server listening  addr=127.0.0.1:4317
```

服务暴露两个端口：

| 端口 | 协议 | 用途 |
|---|---|---|
| `4317` | gRPC (HTTP/2) | Claude Code / Codex OTLP 接收，**不要用浏览器访问** |
| `9100` | HTTP/1.1 | Web UI（`/`）+ 查询 API（`/api/usage/*`）+ stats（`/internal/*`）+ pprof（`/debug/pprof/*`） |

浏览器访问 **`http://localhost:9100/`** 即可看到前端看板。**前提**：先在 `frontend/` 跑过 `npm run build`，二进制重新 `go build` 一次（前端产物通过 `//go:embed` 嵌入）。前端没构建时 server 启动日志里会有 `web UI not mounted`，`/` 会回落到原先的纯文本说明页。

### 2.1 版本与启动更新

查看当前二进制版本（不会加载配置、检查更新或启动服务）：

```bash
./claude-code-monitor version
# 也支持：./claude-code-monitor --version
```

官方 GitHub Release 二进制会在服务启动前检查最新稳定版。交互终端发现新版本时显示 `[y/N]`；输入 `y` 后下载当前平台的 `tar.gz`，按 Release 的 `checksums.txt` 校验 SHA-256，在二进制同目录原子替换，然后以原参数、环境和工作目录继续启动新版本。

- 支持 `linux-amd64`、`linux-arm64`、`darwin-arm64`；源码构建默认版本为 `dev`，不会访问 GitHub。
- 非交互启动（Hook、`nohup`、systemd 等）只打印升级提示，不下载或替换。
- 检查超时、断网、校验失败或目录无写权限时仅打印 warning，继续启动当前版本；程序不会主动调用 `sudo`。
- `-skip-if-running` 检测到已有实例时直接退出，不执行更新检查。
- 可用 `--no-update-check` 或 `CLAUDE_CODE_MONITOR_NO_UPDATE_CHECK=1` 完全关闭启动检查。
- Docker 镜像默认关闭二进制自更新；请拉取新镜像并重建容器。

**端口已被占用时的默认行为是 restart**：server 启动前会探测 `grpc_listen`，若有其它进程在监听，用 `lsof` 查出 PID 后发 `SIGTERM`，等端口释放（最多 5s），仍未释放则升级为 `SIGKILL`（再等 2s）。开发时反复 `./bin/server` 不需要手动 `pkill`。

如果你希望保持"端口被占用就什么都不做"的幂等语义（典型场景：Claude Code SessionStart hook），加 `-skip-if-running`：

```bash
./bin/server -config config.yaml -skip-if-running   # 已有实例就 exit 0
```

> 平台说明：restart 实现依赖 `lsof`（macOS / Linux 自带）。Windows 暂未实现自动 restart，重复启动会因 PID 解析失败返回错误；请手动 `taskkill` 或加 `-skip-if-running`。

### 3. 把 Claude Code 指向本服务

在另一个终端：
```bash
source scripts/claude-env.sh
claude
```

`claude-env.sh` 内容：
```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_METRICS_EXPORTER=otlp
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317
export OTEL_METRIC_EXPORT_INTERVAL=10000   # 调试可短，生产改回 60000
export OTEL_LOGS_EXPORT_INTERVAL=5000
```

### 3.1（可选）把 OpenAI Codex CLI 指向本服务

Codex **不读取标准 OTEL 环境变量**，只认 `~/.codex/config.toml` 的 `[otel]` 段；其 Logs 导出默认关闭，需要显式配置：

```toml
[otel]
environment = "prod"
exporter = { otlp-grpc = { endpoint = "http://127.0.0.1:4317" } }
metrics_exporter = { otlp-grpc = { endpoint = "http://127.0.0.1:4317" } }
# log_user_prompt = true    # 可选：上报 prompt 原文（默认 "[REDACTED]"）
```

Logs 落入 6 张 `codex_event_*` 表（会话 / API 请求 / token 用量 / prompt / 工具决策与结果）；Metrics 中的 Skill 注入和原生 TBT 分别落入 `codex_metric_skill_injected`、`codex_metric_response_tbt`。**必须同时配置 `exporter` 与 `metrics_exporter`**；后者只影响启用后的新会话，历史 Skill / TBT 无法回填。详见 `docs/protocol.md` §7 与 `docs/models.md` §7。

注意：Codex 不上报成本（cost_usd），token 计数是子集式口径（cached ⊂ input、reasoning ⊂ output）。自 v2.4.0 起可选启用 `pricing`（默认关闭）按 LiteLLM 计价表在 ingest 时**估算** Codex 成本，落入 `codex_event_token_usage.cost_usd`——配置见 `config.example.yaml` 的 `pricing` 段。

### 4. 查询

```bash
duckdb data/monitor.duckdb "SELECT table_name FROM duckdb_tables() ORDER BY 1;"

# Token 用量（按模型 + 类型）
duckdb data/monitor.duckdb "
  SELECT model, type, SUM(value) AS tokens
  FROM metric_token_usage
  WHERE ts >= now() - INTERVAL 1 DAY
  GROUP BY 1, 2 ORDER BY 1, 2;"

# 当日成本（USD）
duckdb data/monitor.duckdb "
  SELECT model, ROUND(SUM(value), 4) AS usd
  FROM metric_cost_usage
  WHERE ts >= now() - INTERVAL 1 DAY
  GROUP BY 1 ORDER BY 2 DESC;"

# 工具调用接受 / 拒绝比
duckdb data/monitor.duckdb "
  SELECT tool_name, decision_type, COUNT(*) FROM event_tool_result
  GROUP BY 1, 2 ORDER BY 1, 2;"

# 用 prompt.id 串起一个 prompt 全周期
duckdb data/monitor.duckdb "
  SELECT 'prompt' AS evt, ts FROM event_user_prompt WHERE prompt_id = '<UUID>'
  UNION ALL
  SELECT 'api'    , ts FROM event_api_request WHERE prompt_id = '<UUID>'
  UNION ALL
  SELECT 'tool'   , ts FROM event_tool_result WHERE prompt_id = '<UUID>'
  ORDER BY ts;"

# Codex token 用量（注意子集式口径：总量 = input + output，不加 cached）
duckdb data/monitor.duckdb "
  SELECT model, SUM(input_token_count + output_token_count) AS tokens
  FROM codex_event_token_usage
  WHERE ts >= now() - INTERVAL 1 DAY
  GROUP BY 1 ORDER BY 2 DESC;"
```

未识别的 attribute 都落在每张表的 `attrs` 列（VARCHAR，内容是 JSON 文本），需要时用 `json_extract_string(attrs, '$."key.name"')` 提取。

---

## 用 Claude Code Hook 自动启动（可选）

不想每次手动开 server，可以挂到 Claude Code 的 SessionStart / SessionResume 钩子。仓库里提供了一个幂等脚本 `scripts/hook-session-start.sh`：缺二进制会自动 `go build`，缺 `config.yaml` 会从 `config.example.yaml` 复制，最后用 `nohup` 后台拉起 server。**脚本会传 `-skip-if-running`，使 server preflight 在 gRPC 端口已被占用时 ~14ms 内 exit 0**，所以重复触发完全安全（不会反复 cycle 现有实例）。

在 `~/.claude/settings.json` 里加入：

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "/绝对路径/to/claude-code-monitor/scripts/hook-session-start.sh"
          }
        ]
      }
    ]
  }
}
```

可用环境变量覆盖默认路径（在 hook 命令前 `MONITOR_CONFIG=... MONITOR_LOG=... /path/to/hook-session-start.sh`）：

| 变量 | 默认 | 说明 |
|---|---|---|
| `MONITOR_CONFIG` | `<repo>/config.yaml` | 配置文件路径 |
| `MONITOR_LOG`    | `/tmp/claude-code-monitor.log` | server stdout/stderr 重定向到此 |

排查：
```bash
tail -f /tmp/claude-code-monitor.log               # server 日志
pgrep -lf 'bin/server'                              # 当前实例
curl -s http://127.0.0.1:9100/internal/stats       # 累计指标
pkill -TERM -f 'bin/server -config'                # 手动停
```

注意：preflight 只 probe `grpc_listen` 端口，不区分占用者身份。若 4317 被别的进程占了（其它 OTLP collector 等）：
- 加了 `-skip-if-running`（hook 默认）：server 静默退出 0，日志里有 `another instance is listening; -skip-if-running set, exiting`。
- 默认 restart 模式：用 `lsof` 找到占用 PID 后发 `SIGTERM`，即使对端不是本服务也会被杀。注意别把端口配错。

---

## 配置项（`config.yaml`）

| 段 | 字段 | 默认 | 说明 |
|---|---|---|---|
| `server` | `grpc_listen` | `0.0.0.0:4317` | OTLP gRPC 监听地址 |
| `storage` | `duckdb_path` | `./data/monitor.duckdb` | DuckDB 文件路径，父目录会自动创建 |
| `ingest` | `batch_size` | `500` | 单 buffer 满 N 行立即 flush |
| `ingest` | `flush_interval` | `5s` | 至少每 N 秒 flush |
| `ingest` | `buffer_hard_limit` | `50000` | 超过则丢最旧 + 计数 |
| `capture` | `enabled` | `false` | 开启后原始 OTLP protobuf 字节落盘到 `dir`，用于 P3 testdata 或调试 |
| `capture` | `dir` | `./captured` | 采样目录 |
| `stats` | `listen` | `127.0.0.1:9100` | HTTP 端口（同时承载 Web UI、查询 API、stats、pprof），留空则全禁用 |
| `stats` | `enable_pprof` | `false` | 注册 `/debug/pprof/*`，建议本地调试时开 |
| `dashboard` | `top_n.tools` | `10` | 工具排名 Top N |
| `dashboard` | `top_n.skills` | `10` | Skill 排名 Top N |
| `dashboard` | `timezone` | `Asia/Shanghai` | 业务时区，所有时间窗按此切分 |
| `logging` | `level` | `info` | `debug` / `info` / `warn` / `error` |
| `logging` | `format` | `json` | `json` / `text` |

---

## 运维工具

### Web UI / Stats 端点

`stats.listen`（默认 `127.0.0.1:9100`）上同时提供：

```
GET /                                       Web UI（SPA，前端构建后才有）
GET /api/usage/snapshot?range=day|week|month  KPI（tokens/cost/cache 按 range 切）+ 模型明细
GET /api/usage/trends?range=day|week|month  各模型 Token 用量趋势
GET /api/usage/rankings?since=7d|30d|all    工具 + Skill Top10 排名
GET /api/usage/heatmap                      360 天用量热点图
GET /api/usage/rates?range=day|week|month   生产速率：生成速度（tok/s）+ 吞吐率（tok/min），滑动窗口细粒度分桶
GET /api/usage/rates/realtime?client=codex  即时生成速度：最近 2 分钟与前一段 2 分钟的加权平均
GET /api/pricing/models                     价目表：实际出现过的模型 × LiteLLM 单价（$/1M，需启用 pricing）
GET /api/sessions?limit=                    会话列表（Claude session + Codex conversation 混排）
GET /api/sessions/{id}                      会话详情
GET /internal/healthz                       liveness
GET /internal/stats                         per-table buffer 计数
GET /debug/pprof/*                          运行时 profile（enable_pprof: true 时）
```

usage / rankings / sessions 端点均支持 `client=all|claude|codex`（缺省 `all`）按客户端过滤。工具排名在 `all` 下直接合并两家的原始工具名；Skill 排名将 Claude 的 `skill_activated` 与 Codex 成功的 `skill.injected` delta 相加。Codex 的 token 统计口径为子集式，合并总量 = input + output（不重复计 cached/reasoning）。生成速度：Claude 使用 output tokens / 请求耗时，Codex 使用 `1000 / 平均 service TBT(ms)`；两种口径不会合并成一个 all-client KPI。成本：Claude 为客户端自报的权威值；Codex 为可选估算值（启用 `pricing` 后），响应用 `cost_estimated` 标记，前端在 codex/all 视图标注「含估算」。

查询 API 设计与每个端点的 SQL 见 [`docs/plan-v2-query-api.md`](docs/plan-v2-query-api.md)。响应统一带 `Cache-Control: private, max-age=30`，所有时间窗按 `dashboard.timezone`（默认 `Asia/Shanghai`）切分。

```bash
curl http://127.0.0.1:9100/internal/healthz   # liveness
curl http://127.0.0.1:9100/internal/stats     # per-table buffer 计数
open  http://127.0.0.1:9100/                  # 浏览器打开看板
```

`/internal/stats` 输出（节选）：
```
# claude-code-monitor stats
uptime_seconds        342

# per-table buffers
table                                appended    flushed    dropped flush_errors    pending
metric_token_usage                       12834      12834          0            0          0
event_user_prompt                          823        820          0            0          3
...
```

启用 pprof 时（`enable_pprof: true`）：
```bash
go tool pprof http://127.0.0.1:9100/debug/pprof/heap
```

---

## 排查

| 现象 | 排查方向 |
|---|---|
| 服务收到数据，但所有表为空 | 看 `dispatched` 日志中的 `unknown` / `errors` 字段；Claude Code 升级可能引入新 metric / event |
| 数据延迟入库 | 调小 `ingest.flush_interval`；或检查 `/internal/stats` 中 `pending` 是否堆积 |
| `flush_errors` 非零 | 看 server 日志 ERROR；通常是磁盘满或文件损坏 |
| 重启后 `attrs` 抽不出字段 | 老版本 `attrs` 列曾用 `JSON` 类型导致双重转义；当前是 `VARCHAR`，旧数据需清表重新写入 |
| `.duckdb` 文件不收敛 | `duckdb data/monitor.duckdb "PRAGMA force_checkpoint;"` 手工强制 checkpoint |
| 启动报 `stop existing instance: locate listener` | `lsof` 没装或权限不足。临时方案：加 `-skip-if-running` 让 server 直接退出，或 `pkill -f bin/server` 后再启 |

---

## 开发

```bash
go test ./...
go test -race ./...
go vet ./...
gofmt -w .
```

更多规范见 [`CLAUDE.md`](CLAUDE.md)。
