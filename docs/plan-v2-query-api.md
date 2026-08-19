# V2 — 看板查询 API 设计清单

> 状态：**已评审定稿**（v1），所有开放问题在 §9 已闭环。
> 关联文档：[`docs/models.md`](models.md)（表结构）、[`docs/protocol.md`](protocol.md)（字段含义）、`frontend/src/api/dashboard.ts`（前端期望的数据形状）。
>
> 历史范围说明：正文记录最初 Claude-only v1 的设计背景，不是当前多 harness 架构基线。
> Codex 扩展说明（2026-07-28）：当前 usage / rankings / sessions API 均支持
> `client=all|claude|codex`。排名会按客户端选择 Claude 与 Codex 的工具表，
> Skill 排名还会合并 `codex_metric_skill_injected` 的成功 DELTA 值；生成速度的
> Codex 口径为原生 service TBT。当前口径以 [`protocol.md`](protocol.md) §3、
> [`models.md`](models.md) 与实现代码为准。

---

## 1. 目标

把前端 `frontend/src/api/dashboard.ts` 里 mock 的 `Dashboard.fetch(range)` 换成真接口。当前看板由 7 块数据驱动：

| 区块 | 描述 | 数据范围 |
|---|---|---|
| KPI #1 | Token 用量 + 同比上期 + sparkline | **range：day / week / month** |
| KPI #2 | 消费金额 + 同比上期 + sparkline | **range：day / week / month** |
| KPI #3 | 缓存命中率 + hit/miss 字节数 | **range：day / week / month** |
| Trends | 各模型 Token 用量趋势（堆叠区域） | day/week/month 三档 |
| 工具排名 | `event_tool_result` 按 `tool_name` 计数 Top N | **可选时段：7d / 30d / 全时段** |
| Skill 排名 | `event_skill_activated` 按 `skill_name` 计数 Top N | **可选时段：7d / 30d / 全时段** |
| 模型明细 | 每个模型家族的请求数 / tokens / 费用 / 占比 | 全时段 |

range / since 两个用户控件分别影响 **三个 KPI + trends** 与 **rankings**；模型明细是 range-independent，固定全时段。

---

## 2. 接口拆分

按 "用户控件是否影响这块数据" 切，得到 3 个端点：

```
GET /api/usage/snapshot?range=day|week|month   # KPI + 缓存 + 模型明细
GET /api/usage/trends?range=day|week|month     # 堆叠面积趋势
GET /api/usage/rankings?since=7d|30d|all       # 工具 + Skill Top N
```

前端首次进入并发拉 3 个；切换 range 重拉 snapshot + trends；切换 since 重拉 rankings。
**前端只暴露一个 range 控件**（位于 page-hero 右侧），同时驱动 snapshot 与 trends；模型明细虽然在 snapshot 里返回，但它本身是全时段口径，不受 range 影响。

每个端点都加 `Cache-Control: private, max-age=30`，浏览器自己缓存 30s；后端不做进程内缓存。

---

## 3. 公共约定

### 3.1 模型家族映射

DuckDB 里 `model` 列存的是完整模型 id（如 `claude-opus-4-1-20250805`）。前端只关心三个家族 + `other` 兜底。统一用一个 SQL CASE 表达式做归类：

```sql
CASE
  WHEN model ILIKE '%opus%'   THEN 'opus'
  WHEN model ILIKE '%sonnet%' THEN 'sonnet'
  WHEN model ILIKE '%haiku%'  THEN 'haiku'
  ELSE 'other'
END AS family
```

后端把 `family` 作为字符串字段返回；`label / tier / color` 等展示元数据由前端 `MODELS` 映射表负责。

**`other` 家族的渲染规则**（已评审定稿）：
- **模型明细表**：显示为 "其他模型"
- **Trends 图**：不渲染（避免堆叠图陷入低对比色）

### 3.2 时区与时间窗

**业务时区统一 `Asia/Shanghai`**，所有 "今日 / 本月 / 7 天前" 都按上海当地墙钟切分。

DuckDB 里 `ts` 是 `TIMESTAMP`（naive，按 UTC 写入），所以时间窗边界**在 Go 层算成 UTC instant，再作为 bind 参数传给 SQL**，SQL 端不做时区运算：

```go
loc, _ := time.LoadLocation("Asia/Shanghai")
nowSH := time.Now().In(loc)

todayStartSH := time.Date(nowSH.Year(), nowSH.Month(), nowSH.Day(), 0, 0, 0, 0, loc)
todayStartUTC := todayStartSH.UTC()
todayEndUTC   := todayStartSH.Add(24 * time.Hour).UTC()
yesterdayStartUTC := todayStartSH.Add(-24 * time.Hour).UTC()

monthStartUTC := time.Date(nowSH.Year(), nowSH.Month(), 1, 0, 0, 0, 0, loc).UTC()
```

每个接口需要的边界：

| 名称 | Go 计算 | 用途 |
|---|---|---|
| `today_start_utc` | 今日 0 点（SH）→ UTC | 今日窗口起点 |
| `today_end_utc` | `today_start + 24h` | 今日窗口终点（半开） |
| `yesterday_start_utc` | `today_start - 24h` | 昨日同比起点 |
| `month_start_utc` | 本月 1 号 0 点（SH）→ UTC | MTD 起点 |
| `trend_start_utc(day)` | `today_start - 13*24h` | 14 天日窗口 |
| `trend_start_utc(week)` | 本周一 0 点（SH）- 11 周 → UTC | 12 周窗口 |
| `trend_start_utc(month)` | 本月 1 号（SH）- 11 月 → UTC | 12 月窗口 |
| `since_start_utc(7d)` | `today_start - 7*24h` | 近 7 天 |
| `since_start_utc(30d)` | `today_start - 30*24h` | 近 30 天 |
| `since_start_utc(all)` | `NULL`（谓词省略） | 全时段 |

**桶分组也要按上海日历**。`ts` 是 UTC，直接 `date_trunc('day', ts)` 会按 UTC 切割（凌晨 0-8 点会被划到前一天）。统一做法是先 `ts + INTERVAL 8 HOUR` 再 `date_trunc`：

```sql
-- 把 UTC ts 平移到上海墙钟，再按上海日分桶
CAST(date_trunc('day', ts + INTERVAL 8 HOUR) AS DATE) AS day_sh
```

不用 DuckDB `TIMEZONE()` 函数 — 它要求列是 `TIMESTAMPTZ`，而我们存的是 naive `TIMESTAMP`。

### 3.3 错误码

- `200` 正常
- `400` 参数值不在白名单内（如 `range=year`、`since=2y`）
- `500` DuckDB 查询失败

错误响应：`{"error": "...message..."}`，不暴露 SQL 细节。

### 3.4 响应格式

`Content-Type: application/json; charset=utf-8`。字段名用 snake_case，与前端 mock 一致，迁移时只换数据源不动 type 定义。

### 3.5 `since` 参数解析

```
since = "7d" | "30d" | "all"
```

合法值之外返回 400。Go 端：

```go
switch since {
case "7d":  start = &todayStartUTC; *start = start.Add(-7 * 24 * time.Hour)
case "30d": start = &todayStartUTC; *start = start.Add(-30 * 24 * time.Hour)
case "all": start = nil
default:    return 400
}
```

SQL 端用 `(:since_start IS NULL OR ts >= :since_start)` 让 `all` 走全表扫描，避免拼接 SQL。

---

## 4. 表数据来源选择

| 数据 | 选用表 | 原因 |
|---|---|---|
| 今日 / 历史 token 分类型分模型 | `metric_token_usage` | 已经按 `type / model` 切片 |
| 今日 / 历史费用分模型 | `metric_cost_usage` | `DOUBLE USD` 直接求和 |
| 请求数分模型 | `event_api_request` | metrics 没有请求数 metric |
| 工具调用次数 | `event_tool_result` | 每次工具执行一条 |
| Skill 激活次数 | `event_skill_activated` | 唯一来源 |

**冗余字段警告**：`event_api_request` 也带 `input_tokens / output_tokens / cache_read_tokens / cache_creation_tokens`。理论上 `SUM(input_tokens) FROM event_api_request` 应该等于 `SUM(value) WHERE type='input' FROM metric_token_usage`。OTLP delta 语义下两者会一致；漂移时排查接收端漏数据点。**统一以 `metric_token_usage` 为准**，事件表只用来补 metric 没有的字段（请求数、工具调用次数）。

---

## 5. 接口清单

### 5.1 `GET /api/usage/snapshot?range=day|week|month`

**返回**：KPI（tokens / cost / cache）+ 模型明细。三块 KPI 都按 `range` 切分；模型明细恒为全时段。

```ts
interface SnapshotResponse {
  updated_at: string;            // ISO8601, server now()
  range:      'day' | 'week' | 'month';
  tokens: {
    in:         number;
    out:        number;
    total:      number;          // total 含 cacheRead + cacheCreation
    prev_total: number;          // 上期同长度窗口的 total（昨日 / 上周 / 上月）
    sparkline:  number[];        // 长度 14/12/12，按 grain 补 0
  };
  cost: {
    total:      number;          // 当期总费用 (USD)
    prev_total: number;          // 上期同长度窗口费用
    sparkline:  number[];        // 长度 14/12/12，按 grain 补 0
  };
  cache: {
    hit_rate:    number;         // 0..1
    hit_tokens:  number;
    miss_tokens: number;
  };
  models: Array<{                // 全时段，与 range 无关
    family:      'opus' | 'sonnet' | 'haiku' | 'other';
    requests:    number;
    tokens_in:   number;
    tokens_out:  number;
    cache_tokens:number;          // cacheRead
    cost:        number;
    share:       number;          // tokens 占比，0..1
  }>;
}
```

#### 时间窗口表（Go 层算）

| `range` | `current_start` | `current_end` | `previous_start` | `previous_end` | `sparkline_grain` | `sparkline_start` | `sparkline_count` |
|---|---|---|---|---|---|---|---|
| `day` | 今日 0:00 (SH) → UTC | +24h | -24h | current_start | `day` | current_start - 13d | 14 |
| `week` | 本周一 0:00 (SH) → UTC | +7d | -7d | current_start | `week` | current_start - 11w | 12 |
| `month` | 本月 1 号 0:00 (SH) → UTC | 下月 1 号 0:00 → UTC | 上月 1 号 0:00 → UTC | current_start | `month` | current_start - 11mo | 12 |

#### A) 当期 tokens

```sql
SELECT
  COALESCE(SUM(CASE WHEN type='input'  THEN value END), 0) AS tokens_in,
  COALESCE(SUM(CASE WHEN type='output' THEN value END), 0) AS tokens_out,
  COALESCE(SUM(value), 0)                                   AS tokens_total
FROM metric_token_usage
WHERE ts >= :current_start_utc
  AND ts <  :current_end_utc;
```

#### B) 上期 tokens（同口径，单值，用于 delta%）

```sql
SELECT COALESCE(SUM(value), 0)
FROM metric_token_usage
WHERE ts >= :previous_start_utc
  AND ts <  :previous_end_utc;
```

> `total` 含 `input + output + cacheRead + cacheCreation` 全部类型。

#### C) Tokens sparkline

```sql
SELECT
  CAST(date_trunc(:grain, ts + INTERVAL 8 HOUR) AS DATE) AS bucket_sh,
  SUM(value)                                             AS total
FROM metric_token_usage
WHERE ts >= :sparkline_start_utc
  AND ts <  :current_end_utc
GROUP BY 1
ORDER BY 1;
```

Go 层按 `(sparkline_grain, sparkline_count)` 补 0 桶，输出长度恒等。

#### D) 当期 / 上期 cost（两次执行，参数不同）

```sql
SELECT COALESCE(SUM(value), 0)
FROM metric_cost_usage
WHERE ts >= ? AND ts < ?;
```

#### E) Cost sparkline

```sql
SELECT
  CAST(date_trunc(:grain, ts + INTERVAL 8 HOUR) AS DATE) AS bucket_sh,
  SUM(value)                                             AS cost
FROM metric_cost_usage
WHERE ts >= :sparkline_start_utc
  AND ts <  :current_end_utc
GROUP BY 1
ORDER BY 1;
```

#### F) 当期缓存命中率

```sql
SELECT
  COALESCE(SUM(CASE WHEN type='cacheRead' THEN value END), 0) AS hit_tokens,
  COALESCE(SUM(CASE WHEN type='input'     THEN value END), 0) AS miss_tokens
FROM metric_token_usage
WHERE ts >= :current_start_utc
  AND ts <  :current_end_utc
  AND type IN ('input', 'cacheRead');
```

> `hit_rate = hit_tokens / (hit_tokens + miss_tokens)`，Go 层算。
> **分母只算 `input + cacheRead`，不含 `cacheCreation`**。分母为 0 时返回 0。

#### E) 模型明细（全时段）

三条独立 SQL，Go 层按 `family` outer join + 计算 `share`：

```sql
-- E1. tokens 按 family + type
SELECT
  CASE
    WHEN model ILIKE '%opus%'   THEN 'opus'
    WHEN model ILIKE '%sonnet%' THEN 'sonnet'
    WHEN model ILIKE '%haiku%'  THEN 'haiku'
    ELSE 'other'
  END AS family,
  COALESCE(SUM(CASE WHEN type='input'     THEN value END), 0) AS tokens_in,
  COALESCE(SUM(CASE WHEN type='output'    THEN value END), 0) AS tokens_out,
  COALESCE(SUM(CASE WHEN type='cacheRead' THEN value END), 0) AS cache_tokens
FROM metric_token_usage
WHERE model IS NOT NULL
GROUP BY 1;

-- E2. cost 按 family
SELECT
  CASE
    WHEN model ILIKE '%opus%'   THEN 'opus'
    WHEN model ILIKE '%sonnet%' THEN 'sonnet'
    WHEN model ILIKE '%haiku%'  THEN 'haiku'
    ELSE 'other'
  END AS family,
  SUM(value) AS cost
FROM metric_cost_usage
WHERE model IS NOT NULL
GROUP BY 1;

-- E3. 请求数按 family
SELECT
  CASE
    WHEN model ILIKE '%opus%'   THEN 'opus'
    WHEN model ILIKE '%sonnet%' THEN 'sonnet'
    WHEN model ILIKE '%haiku%'  THEN 'haiku'
    ELSE 'other'
  END AS family,
  COUNT(*) AS requests
FROM event_api_request
WHERE model IS NOT NULL
GROUP BY 1;
```

> 拆三条 SQL 比一条大 CTE 更好测、好调；Go 层 join 到 `map[family]ModelBreakdown` 即可。
> `share = (tokens_in + tokens_out + cache_tokens) / Σ`，Go 层一遍循环算好。

---

### 5.2 `GET /api/usage/trends?range=day|week|month`

**返回**：堆叠区域图所需的时间序列。

```ts
interface TrendsResponse {
  range: 'day' | 'week' | 'month';
  points: Array<{
    date:   string;    // YYYY-MM-DD（day/week 起点）或 YYYY-MM（month）
    label:  string;    // "11/5" / "5月" 等
    opus:   number;
    sonnet: number;
    haiku:  number;
    // ⚠ 不包含 other 字段（设计决定：堆叠图低对比色，不渲染）
  }>;
}
```

**SQL（参数化，按 range 切 grain + 起点）**：

```sql
SELECT
  CAST(date_trunc(:grain, ts + INTERVAL 8 HOUR) AS DATE) AS bucket_sh,
  CASE
    WHEN model ILIKE '%opus%'   THEN 'opus'
    WHEN model ILIKE '%sonnet%' THEN 'sonnet'
    WHEN model ILIKE '%haiku%'  THEN 'haiku'
    ELSE 'other'
  END AS family,
  SUM(value) AS tokens
FROM metric_token_usage
WHERE ts >= :window_start_utc
  AND model IS NOT NULL
GROUP BY 1, 2
ORDER BY 1;
```

参数映射：

| `range` | `:grain` | `:window_start_utc` | 期望桶数 |
|---|---|---|---|
| `day` | `'day'` | 今日 0 点（SH）- 13 天 → UTC | 14 |
| `week` | `'week'` | 本周一 0 点（SH）- 11 周 → UTC | 12 |
| `month` | `'month'` | 本月 1 号 0 点（SH）- 11 月 → UTC | 12 |

Go 层职责：
1. 按 `[window_start, now()]` 生成完整桶序列（缺失桶补 0）
2. 把行式结果 pivot 成 `{date, label, opus, sonnet, haiku}`
3. `label` 格式化：day → `M/D`，week → `M/D`（DuckDB 默认周一为起点），month → `M月`
4. **丢弃 `family='other'` 的行**（前端不渲染该家族）

---

### 5.3 `GET /api/usage/rankings?since=7d|30d|all`

**返回**：工具 + Skill 排名（两块数据共享同一时段控件，一个请求一次返回）。

```ts
interface RankingsResponse {
  since: '7d' | '30d' | 'all';
  tools:  { name: string; count:       number }[];   // Top 10 DESC
  skills: { name: string; activations: number }[];   // Top 10 DESC
}
```

**SQL（两条独立查询）**：

```sql
-- tools
SELECT tool_name AS name, COUNT(*) AS count
FROM event_tool_result
WHERE tool_name IS NOT NULL
  AND (:since_start_utc IS NULL OR ts >= :since_start_utc)
GROUP BY tool_name
ORDER BY count DESC
LIMIT 10;

-- skills
SELECT skill_name AS name, COUNT(*) AS activations
FROM event_skill_activated
WHERE skill_name IS NOT NULL
  AND (:since_start_utc IS NULL OR ts >= :since_start_utc)
GROUP BY skill_name
ORDER BY activations DESC
LIMIT 10;
```

`:since_start_utc` 为 NULL 时 `(NULL IS NULL OR ...)` 短路成 true，整个表参与聚合 — DuckDB 会优化掉这个谓词。

---

### 5.4 `GET /internal/healthz`（已存在，不变）

仅在此完整列出端点全集。

---

## 6. 实现路径

### 6.1 包结构

新增 `internal/dashboard/`：

```
internal/dashboard/
├── types.go         # SnapshotResponse / TrendsResponse / RankingsResponse / 等
├── timewin.go       # 上海时区时间窗计算（today_start_utc / month_start_utc / ...）
├── queries.go       # 独立查询函数：QueryTodayTokens / QuerySparkline / ...
├── snapshot.go      # 调度多个查询 → 组装 SnapshotResponse
├── trends.go        # 查询 + pivot → TrendsResponse
├── rankings.go      # 查询 → RankingsResponse
├── handler.go       # http.Handler，路由 3 个 /api/usage/* 端点
└── queries_test.go  # 端到端 SQL 测试（fixture DuckDB）
```

每个 `Query*` 函数签名形如：

```go
func QueryTodayTokens(ctx context.Context, db *sql.DB, w TimeWindow) (TodayTokens, error)
```

`TimeWindow` 是 `timewin.go` 里定义的边界 struct（含 `TodayStartUTC` / `TodayEndUTC` / 等），调用方传入避免每个查询重复算。

### 6.2 挂载点

复用 `internal/stats/server.go` 的同端口 mux 模式（参考已有 `SetRootHandler`）：

```go
// stats/server.go 新增
func (s *Server) SetAPIHandler(h http.Handler) { s.apiHandler = h }

// Start() 里
if s.apiHandler != nil {
    mux.Handle("/api/", s.apiHandler)  // 注意尾斜杠：匹配 /api/* 前缀
}
```

`cmd/server/main.go` wire-up：

```go
statsSrv := stats.NewServer(cfg.Stats, writer, slog.Default())
if webHandler, err := web.Handler(); err == nil {
    statsSrv.SetRootHandler(webHandler)
}
statsSrv.SetAPIHandler(dashboard.NewHandler(db.SQL, cfg.Dashboard, slog.Default()))
```

`mux.Handle("/api/", ...)` 必须用尾斜杠才能匹配前缀。

### 6.3 数据库连接

只读，与 BufferedWriter **共用** `*store.DB.SQL`。当前 `SetMaxOpenConns(1)` 为写者串行化，读会被串行化但不影响正确性。若后续读放大成瓶颈再放宽。

### 6.4 鉴权

**不做**。服务只 bind `127.0.0.1`，单用户本地工具场景下足够。

### 6.5 缓存头

snapshot / trends / rankings 都加：

```
Cache-Control: private, max-age=30
```

后端不实现进程内缓存。

---

## 7. 配置新增

`config.yaml` 新增 `dashboard` 段：

```yaml
dashboard:
  top_n:
    tools:  10
    skills: 10
  timezone: "Asia/Shanghai" # 业务时区，目前固定，留参便于将来开多用户
```

`config.DashboardConfig` 对应 struct，默认值在 `applyDefaults` 里赋。

`stats` 已有的 `enable_pprof / listen` 不变；`/api/*` 跟 web UI 一起挂在 `stats.listen`（9100）。

---

## 8. 前端改造

`frontend/src/api/dashboard.ts` 改造点：

1. 删除 `build()` mock 与所有内部生成器
2. `Dashboard.fetch` 拆成 3 个并发请求，前端层组装：
   ```ts
   async fetch(range: Range, since: Since = '7d'): Promise<DashboardData> {
     const [snap, trends, rankings] = await Promise.all([
       fetch('/api/usage/snapshot').then(r => r.json()),
       fetch(`/api/usage/trends?range=${range}`).then(r => r.json()),
       fetch(`/api/usage/rankings?since=${since}`).then(r => r.json()),
     ]);
     return adapt(snap, trends, rankings);
   }
   ```
3. `adapt()` 负责把后端 `family` 拼回 `{id, label, tier, color}`（用 `MODELS` 表 lookup，`other` 兜底）
4. `App.tsx` 增加 since 控件（7d / 30d / 全时段三选一），驱动 rankings 重拉；range 控件继续只驱动 trends 重拉
5. 模型明细表里 `family='other'` 渲染为 "其他模型"（已与设计确认）

---

## 9. 评审决策（闭环）

| # | 问题 | 决策 |
|---|---|---|
| Q1 | 今日 token total 是否包含 cache？ | **包含 cacheRead + cacheCreation**，即 `SUM(value)` 全类型 |
| Q2 | hit_rate 分母组成？ | **只算 input + cacheRead**，不含 cacheCreation |
| Q3 | 模型家族识别正则是否够稳？ | **用 `ILIKE '%opus%'` / `'%sonnet%'` / `'%haiku%'`**，未来出现冲突再改 |
| Q4 | `other` 家族如何展示？ | **表格显示 "其他模型"，trends 图不渲染** |
| Q5 | 排名是否支持时段？ | **支持 7d / 30d / all**，新增 `/api/usage/rankings?since=` 端点 |
| Q6 | 排名是否分页？ | **不分页**，Top 10 固定 |
| Q7 | 时区策略？ | **固定 `Asia/Shanghai`**，单用户场景 |
| Q8 | 鉴权？ | **不做**，bind `127.0.0.1` 即可 |

---

## 10. 落地任务拆分

按以下顺序执行，每步可独立验证 / 提交：

1. **配置扩展**：`config.yaml` + `config.go` 加 `DashboardConfig`，验证默认值
2. **包骨架**：`internal/dashboard/` 建空文件，定义 `types.go` 与 `timewin.go`（上海时区时间窗）
3. **查询层**：`queries.go` 所有查询函数 + 单测（用 fixture DuckDB 或 testdata）
4. **组装层**：`snapshot.go` / `trends.go` / `rankings.go`，把查询结果拼成响应
5. **HTTP handler**：`handler.go`，挂 3 个 `/api/usage/*` 端点
6. **挂载**：`stats.Server.SetAPIHandler` + main.go wire-up
7. **前端**：`Dashboard.fetch` 改 real fetch + `adapt()`，删除 mock builder；`App.tsx` 加 since 控件
8. **冒烟**：浏览器打开 `:9100`，看板用真实数据渲染
9. **README**：补一段说明 `/api/usage/*` 端点

---

## 11. 不在本阶段范围

- 历史趋势超过 12 个月（需要归档表逻辑，见 `docs/models.md` §5.6）
- 任意维度的自由查询（如 "按 session 看 token 分布"）
- 实时推送 / WebSocket
- 多用户视图（`?user_id=`）
- 鉴权 / RBAC
- 进程内缓存或 Redis 缓存层
