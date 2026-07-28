/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.3.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	mpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/otlp"
	"github.com/kuroky/claude-code-monitor/internal/store"
)

// ── Codex 种子 helpers ──────────────────────────────────────────────

func insertCodexTokenUsage(t *testing.T, db *sql.DB, ts time.Time, conv, model string, input, output, cached, reasoning int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_token_usage
		  (ts, conversation_id, model, input_token_count, output_token_count, cached_token_count, reasoning_token_count)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, ts, conv, model, input, output, cached, reasoning)
	if err != nil {
		t.Fatalf("insert codex token_usage: %v", err)
	}
}

func insertCodexApiRequest(t *testing.T, db *sql.DB, ts time.Time, conv string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_api_request (ts, conversation_id, attempt) VALUES (?, ?, 0)
	`, ts, conv)
	if err != nil {
		t.Fatalf("insert codex api_request: %v", err)
	}
}

func insertCodexToolResult(t *testing.T, db *sql.DB, ts time.Time, conv, tool string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_tool_result (ts, conversation_id, tool_name, success) VALUES (?, ?, ?, true)
	`, ts, conv, tool)
	if err != nil {
		t.Fatalf("insert codex tool_result: %v", err)
	}
}

func insertCodexUserPrompt(t *testing.T, db *sql.DB, ts time.Time, conv string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_user_prompt (ts, conversation_id, prompt_length) VALUES (?, ?, 10)
	`, ts, conv)
	if err != nil {
		t.Fatalf("insert codex user_prompt: %v", err)
	}
}

func insertCodexConversationStarts(t *testing.T, db *sql.DB, ts time.Time, conv string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_conversation_starts (ts, conversation_id, provider_name) VALUES (?, ?, 'OpenAI')
	`, ts, conv)
	if err != nil {
		t.Fatalf("insert codex conversation_starts: %v", err)
	}
}

func newCodexMetricTestService(t *testing.T) (*store.DB, *otlp.MetricsService, *slog.Logger) {
	t.Helper()
	db, err := store.Open(config.StorageConfig{
		DuckDBPath: filepath.Join(t.TempDir(), "codex-metrics.duckdb"),
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close: %v", err)
		}
	})
	migrations, err := store.LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	if err := store.RunMigrations(db.SQL, migrations); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer, err := store.NewBufferedWriter(db, config.IngestConfig{
		BatchSize:       1,
		FlushInterval:   config.Duration(time.Hour),
		BufferHardLimit: 100,
	}, log)
	if err != nil {
		t.Fatalf("NewBufferedWriter: %v", err)
	}
	t.Cleanup(func() {
		if err := writer.Stop(); err != nil {
			t.Errorf("writer.Stop: %v", err)
		}
	})
	service := otlp.NewMetricsService(log, nil, otlp.NewDispatcher(log, writer, nil))
	return db, service, log
}

func TestHandleRankings_CodexTools(t *testing.T) {
	db, w, _ := testDB(t)
	ts := w.TodayStartUTC.Add(time.Hour)
	insertToolResult(t, db, ts, "Read")
	insertCodexToolResult(t, db, ts, "conv-rankings", "exec_command")
	insertCodexToolResult(t, db, ts.Add(time.Second), "conv-rankings", "exec_command")

	h, err := NewHandler(db, config.DashboardConfig{
		Timezone: "Asia/Shanghai",
		TopN:     config.TopNConfig{Tools: 10, Skills: 10},
	}, false, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rankings?since=all&client=codex", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp RankingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Tools) != 1 || resp.Tools[0].Name != "exec_command" || resp.Tools[0].Count != 2 {
		t.Fatalf("tools = %+v, want exec_command=2", resp.Tools)
	}
}

func TestHandleRankings_AllTools(t *testing.T) {
	db, w, _ := testDB(t)
	ts := w.TodayStartUTC.Add(time.Hour)
	insertToolResult(t, db, ts, "Read")
	insertCodexToolResult(t, db, ts.Add(time.Second), "conv-rankings-all", "exec_command")

	h, err := NewHandler(db, config.DashboardConfig{
		Timezone: "Asia/Shanghai",
		TopN:     config.TopNConfig{Tools: 10, Skills: 10},
	}, false, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rankings?since=all&client=all", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp RankingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Tools) != 2 {
		t.Fatalf("tools = %+v, want Read and exec_command", resp.Tools)
	}
	if resp.Tools[0].Name != "Read" || resp.Tools[1].Name != "exec_command" {
		t.Fatalf("tools = %+v, want deterministic Read then exec_command", resp.Tools)
	}
}

func TestHandleRankings_CodexSkillMetric(t *testing.T) {
	db, service, log := newCodexMetricTestService(t)
	now := time.Now().UTC()
	req := &metricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*mpb.ResourceMetrics{{
			Resource: &resourcepb.Resource{},
			ScopeMetrics: []*mpb.ScopeMetrics{{
				Metrics: []*mpb.Metric{{
					Name: "codex.skill.injected",
					Data: &mpb.Metric_Sum{Sum: &mpb.Sum{
						AggregationTemporality: mpb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
						IsMonotonic:            true,
						DataPoints: []*mpb.NumberDataPoint{{
							StartTimeUnixNano: uint64(now.Add(-time.Second).UnixNano()),
							TimeUnixNano:      uint64(now.UnixNano()),
							Value:             &mpb.NumberDataPoint_AsInt{AsInt: 1},
							Attributes: []*commonpb.KeyValue{
								{Key: "app.version", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "0.145.0"}}},
								{Key: "model", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "gpt-5.6-sol"}}},
								{Key: "skill", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "golang-patterns"}}},
								{Key: "status", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "ok"}}},
								{Key: "invoke_type", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "implicit"}}},
							},
						}},
					}},
				}},
			}},
		}},
	}
	if _, err := service.Export(context.Background(), req); err != nil {
		t.Fatalf("Export: %v", err)
	}

	h, err := NewHandler(db.SQL, config.DashboardConfig{
		Timezone: "Asia/Shanghai",
		TopN:     config.TopNConfig{Tools: 10, Skills: 10},
	}, false, log)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rankings?since=all&client=codex", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp RankingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Skills) != 1 || resp.Skills[0].Name != "golang-patterns" || resp.Skills[0].Activations != 1 {
		t.Fatalf("skills = %+v, want golang-patterns=1", resp.Skills)
	}
}

func TestHandleRates_CodexServiceTBTMetric(t *testing.T) {
	db, service, log := newCodexMetricTestService(t)
	now := time.Now().UTC().Add(-time.Minute)
	sumMs := 40.0
	req := &metricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*mpb.ResourceMetrics{{
			Resource: &resourcepb.Resource{},
			ScopeMetrics: []*mpb.ScopeMetrics{{
				Metrics: []*mpb.Metric{{
					Name: "codex.responses_api_engine_service_tbt.duration_ms",
					Data: &mpb.Metric_Histogram{Histogram: &mpb.Histogram{
						AggregationTemporality: mpb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
						DataPoints: []*mpb.HistogramDataPoint{{
							StartTimeUnixNano: uint64(now.Add(-time.Second).UnixNano()),
							TimeUnixNano:      uint64(now.UnixNano()),
							Count:             2,
							Sum:               &sumMs,
							ExplicitBounds:    []float64{5, 10, 25},
							BucketCounts:      []uint64{0, 0, 2, 0},
							Attributes: []*commonpb.KeyValue{
								{Key: "app.version", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "0.145.0"}}},
								{Key: "model", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "gpt-5.6-sol"}}},
								{Key: "session_source", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "exec"}}},
							},
						}},
					}},
				}},
			}},
		}},
	}
	if _, err := service.Export(context.Background(), req); err != nil {
		t.Fatalf("Export: %v", err)
	}
	var storedRows, storedSamples int64
	var storedSumMs float64
	if err := db.SQL.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(sample_count), 0), COALESCE(SUM(sum_ms), 0)
		FROM codex_metric_response_tbt
	`).Scan(&storedRows, &storedSamples, &storedSumMs); err != nil {
		t.Fatalf("query stored TBT: %v", err)
	}
	if storedRows != 1 || storedSamples != 2 || storedSumMs != 40 {
		t.Fatalf("stored TBT = rows=%d samples=%d sum_ms=%v, want 1/2/40", storedRows, storedSamples, storedSumMs)
	}

	h, err := NewHandler(db.SQL, config.DashboardConfig{
		Timezone: "Asia/Shanghai",
		TopN:     config.TopNConfig{Tools: 10, Skills: 10},
	}, false, log)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rates?range=day&client=codex", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp RatesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Speed.Current == nil || *resp.Speed.Current < 49.99 || *resp.Speed.Current > 50.01 {
		t.Fatalf("current = %v, want 50 tok/s", resp.Speed.Current)
	}
	if len(resp.Speed.Groups) != 1 || resp.Speed.Groups[0] != "gpt-5.6-sol" {
		t.Fatalf("groups = %v, want [gpt-5.6-sol]", resp.Speed.Groups)
	}
	var found bool
	for _, point := range resp.Speed.Points {
		if got, ok := point.Values["gpt-5.6-sol"]; ok {
			found = got > 49.99 && got < 50.01
		}
	}
	if !found {
		t.Fatalf("speed points do not contain gpt-5.6-sol=50: %+v", resp.Speed.Points)
	}
}

// seedMixedPeriod 在当前 day 窗口内种一组固定数据:
//
//	Claude: input 100 / output 50 / cacheRead 30 / cacheCreation 20 → total 200
//	Codex : input 1000(含 cached 400) / output 200(含 reasoning 50) → total 1200
func seedMixedPeriod(t *testing.T, db *sql.DB, w TimeWindow) (spec WindowSpec) {
	t.Helper()
	spec, _ = w.Resolve("day")
	ts := spec.CurrentStart.Add(2 * time.Hour)
	insertTokenUsage(t, db, ts, "claude-opus-4-1", "input", 100)
	insertTokenUsage(t, db, ts, "claude-opus-4-1", "output", 50)
	insertTokenUsage(t, db, ts, "claude-opus-4-1", "cacheRead", 30)
	insertTokenUsage(t, db, ts, "claude-opus-4-1", "cacheCreation", 20)
	insertApiRequest(t, db, ts, "claude-opus-4-1")
	insertCodexTokenUsage(t, db, ts, "conv-1", "gpt-5.5", 1000, 200, 400, 50)
	// attempt 粒度行:请求数口径断言用——不应被计入
	insertCodexApiRequest(t, db, ts, "conv-1")
	insertCodexApiRequest(t, db, ts, "conv-1")
	return spec
}

// ── 口径与三态断言 ─────────────────────────────────────────────────

func TestQueryPeriodTokens_ClientModes(t *testing.T) {
	db, w, _ := testDB(t)
	spec := seedMixedPeriod(t, db, w)
	ctx := context.Background()

	claude, err := QueryPeriodTokens(ctx, db, ClientClaude, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("claude: %v", err)
	}
	if claude.In != 100 || claude.Out != 50 || claude.Total != 200 {
		t.Errorf("claude = %+v, want in=100 out=50 total=200", claude)
	}

	codex, err := QueryPeriodTokens(ctx, db, ClientCodex, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("codex: %v", err)
	}
	// in = input - cached = 600;total = input + output = 1200(cached/reasoning 是子集,不得重复计)
	if codex.In != 600 || codex.Out != 200 || codex.Total != 1200 {
		t.Errorf("codex = %+v, want in=600 out=200 total=1200", codex)
	}

	all, err := QueryPeriodTokens(ctx, db, ClientAll, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if all.In != 700 || all.Out != 250 || all.Total != 1400 {
		t.Errorf("all = %+v, want in=700 out=250 total=1400", all)
	}
}

func TestQueryPeriodCache_ClientModes(t *testing.T) {
	db, w, _ := testDB(t)
	spec := seedMixedPeriod(t, db, w)
	ctx := context.Background()

	claude, err := QueryPeriodCache(ctx, db, ClientClaude, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("claude: %v", err)
	}
	if claude.Read != 30 || claude.Creation != 20 || claude.HitDenom != 50 {
		t.Errorf("claude = %+v, want read=30 creation=20 denom=50", claude)
	}

	codex, err := QueryPeriodCache(ctx, db, ClientCodex, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("codex: %v", err)
	}
	// codex 命中率 = cached / input → denom 是 input,不是 read+creation
	if codex.Read != 400 || codex.Creation != 0 || codex.HitDenom != 1000 {
		t.Errorf("codex = %+v, want read=400 creation=0 denom=1000", codex)
	}

	all, err := QueryPeriodCache(ctx, db, ClientAll, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if all.Read != 430 || all.Creation != 20 || all.HitDenom != 1050 {
		t.Errorf("all = %+v, want read=430 creation=20 denom=1050", all)
	}
}

func TestQueryPeriodRequests_CodexUsesCompleted(t *testing.T) {
	db, w, _ := testDB(t)
	spec := seedMixedPeriod(t, db, w)
	ctx := context.Background()

	codex, err := QueryPeriodRequests(ctx, db, ClientCodex, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("codex: %v", err)
	}
	// 1 条 token_usage(response.completed);2 条 attempt 粒度的 api_request 不计
	if codex != 1 {
		t.Errorf("codex requests = %d, want 1 (attempt rows must not count)", codex)
	}

	all, err := QueryPeriodRequests(ctx, db, ClientAll, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if all != 2 { // claude 1 + codex 1
		t.Errorf("all requests = %d, want 2", all)
	}
}

func TestQueryTokensSparkline_AllMergesBuckets(t *testing.T) {
	db, w, _ := testDB(t)
	spec := seedMixedPeriod(t, db, w)
	ctx := context.Background()

	rows, err := QueryTokensSparkline(ctx, db, ClientAll, w, spec.SparklineGrain, spec.SparklineStart, spec.PeriodEnd)
	if err != nil {
		t.Fatalf("sparkline: %v", err)
	}
	var sum int64
	for _, r := range rows {
		sum += r.Total
	}
	if sum != 1400 { // 同一天两家合并进一个 bucket
		t.Errorf("sparkline sum = %d, want 1400", sum)
	}
}

func TestQueryModelTokens_CodexProjection(t *testing.T) {
	db, w, _ := testDB(t)
	seedMixedPeriod(t, db, w)
	ctx := context.Background()

	rows, err := QueryModelTokens(ctx, db, ClientAll)
	if err != nil {
		t.Fatalf("QueryModelTokens: %v", err)
	}
	byModel := map[string]modelTokens{}
	for _, r := range rows {
		byModel[r.Model] = r
	}
	g, ok := byModel["gpt-5.5"]
	if !ok {
		t.Fatal("gpt-5.5 missing from model tokens")
	}
	// in=600(input-cached) out=200 cache=400 → 三者之和恰等于 codex 总量 1200
	if g.TokensIn != 600 || g.TokensOut != 200 || g.CacheTokens != 400 {
		t.Errorf("gpt-5.5 = %+v, want in=600 out=200 cache=400", g)
	}
	if _, ok := byModel["claude-opus-4-1"]; !ok {
		t.Error("claude model missing under ClientAll")
	}

	codexOnly, err := QueryModelTokens(ctx, db, ClientCodex)
	if err != nil {
		t.Fatalf("codex only: %v", err)
	}
	if len(codexOnly) != 1 || codexOnly[0].Model != "gpt-5.5" {
		t.Errorf("ClientCodex rows = %+v, want only gpt-5.5", codexOnly)
	}
}

func TestQueryTrends_IncludesCodex(t *testing.T) {
	db, w, _ := testDB(t)
	seedMixedPeriod(t, db, w)
	ctx := context.Background()

	rows, err := QueryTrends(ctx, db, ClientAll, w, "day", w.DayTrendStartUTC)
	if err != nil {
		t.Fatalf("QueryTrends: %v", err)
	}
	var codexTokens int64
	for _, r := range rows {
		if r.Model == "gpt-5.5" {
			codexTokens += r.Tokens
		}
	}
	if codexTokens != 1200 {
		t.Errorf("codex trend tokens = %d, want 1200 (input+output, no cached)", codexTokens)
	}
}

func TestBuildHeatmap_CodexWeightDenominator(t *testing.T) {
	db, w, _ := testDB(t)
	// 只种一条 codex 数据 → 该日 token 与请求都是窗口 max(norm=1),cost 恒 0
	ts := w.TodayStartUTC.Add(2 * time.Hour)
	insertCodexTokenUsage(t, db, ts, "conv-h", "gpt-5.5", 100, 50, 0, 0)

	weights := HeatmapWeights{Tokens: 0.4, Cost: 0.4, Requests: 0.2}
	resp, err := BuildHeatmap(context.Background(), db, w, weights, ClientCodex, false)
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	var maxScore float64
	for _, p := range resp.Points {
		if p.Score > maxScore {
			maxScore = p.Score
		}
	}
	// codex 视图分母须剔除 cost 权重:score = (0.4·1 + 0.2·1)/(0.4+0.2) = 1.0
	// 若沿用三权重分母会得到 0.6,即被恒零的 cost 系统性压低。
	if maxScore < 0.999 {
		t.Errorf("max score = %v, want 1.0 (cost weight must leave the denominator for codex)", maxScore)
	}
}

func TestSessionList_MixedClients(t *testing.T) {
	db, w, _ := testDB(t)
	base := w.TodayStartUTC.Add(time.Hour)
	// Claude 会话
	_, err := db.Exec(`INSERT INTO event_api_request (ts, user_id, session_id, model) VALUES (?, 'u', 'sess-c', 'claude-opus-4-1')`, base)
	if err != nil {
		t.Fatal(err)
	}
	insertTokenUsageSession(t, db, base, "sess-c", "input", 100)
	// Codex 会话(更晚活动 → 排前)
	insertCodexTokenUsage(t, db, base.Add(time.Hour), "conv-x", "gpt-5.5", 1000, 200, 400, 0)
	insertCodexToolResult(t, db, base.Add(time.Hour), "conv-x", "exec_command")
	insertCodexUserPrompt(t, db, base.Add(2*time.Hour), "conv-x")

	rows, err := QuerySessionList(context.Background(), db, ClientAll, 10)
	if err != nil {
		t.Fatalf("QuerySessionList: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].SessionID != "conv-x" || rows[0].Client != "codex" {
		t.Errorf("row0 = %+v, want codex conv-x first", rows[0])
	}
	if rows[0].Tokens != 1200 || rows[0].Requests != 1 || rows[0].ToolCalls != 1 || rows[0].Skills != 0 {
		t.Errorf("codex row = %+v, want tokens=1200 requests=1 tools=1 skills=0", rows[0])
	}
	if rows[1].SessionID != "sess-c" || rows[1].Client != "claude" {
		t.Errorf("row1 = %+v, want claude sess-c", rows[1])
	}

	codexOnly, err := QuerySessionList(context.Background(), db, ClientCodex, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(codexOnly) != 1 || codexOnly[0].SessionID != "conv-x" {
		t.Errorf("ClientCodex rows = %+v, want only conv-x", codexOnly)
	}
}

func TestSessionDetail_Codex(t *testing.T) {
	db, w, _ := testDB(t)
	ts := w.TodayStartUTC.Add(time.Hour)
	insertCodexConversationStarts(t, db, ts, "conv-d")
	insertCodexTokenUsage(t, db, ts.Add(time.Minute), "conv-d", "gpt-5.5", 1000, 200, 400, 50)
	insertCodexToolResult(t, db, ts.Add(2*time.Minute), "conv-d", "exec_command")
	insertCodexToolResult(t, db, ts.Add(3*time.Minute), "conv-d", "web_search")

	resp, found, err := BuildSessionDetail(context.Background(), db, "conv-d", ClientCodex, 10, 10, false)
	if err != nil || !found {
		t.Fatalf("BuildSessionDetail: found=%v err=%v", found, err)
	}
	if resp.Client != "codex" {
		t.Errorf("client = %q, want codex", resp.Client)
	}
	if resp.Tokens != 1200 || resp.Requests != 1 || resp.ToolCalls != 2 || resp.SkillActivations != 0 {
		t.Errorf("resp = %+v, want tokens=1200 requests=1 tools=2 skills=0", resp)
	}
	if resp.TokenDetail == nil ||
		resp.TokenDetail.Input != 1000 || resp.TokenDetail.Output != 200 ||
		resp.TokenDetail.Cached != 400 || resp.TokenDetail.Reasoning != 50 {
		t.Errorf("token_detail = %+v, want 1000/200/400/50", resp.TokenDetail)
	}

	// 无 hint(ClientAll)时按 claude → codex 顺序探测,也应命中
	_, found, err = BuildSessionDetail(context.Background(), db, "conv-d", ClientAll, 10, 10, false)
	if err != nil || !found {
		t.Errorf("probe with ClientAll: found=%v err=%v", found, err)
	}
}

// insertTokenUsageSession 补一个带 session_id 的 Claude token 种子。
func insertTokenUsageSession(t *testing.T, db *sql.DB, ts time.Time, sessionID, typ string, value int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO metric_token_usage (ts, start_ts, value, user_id, session_id, model, type)
		VALUES (?, ?, ?, 'u', ?, 'claude-opus-4-1', ?)
	`, ts, ts, value, sessionID, typ)
	if err != nil {
		t.Fatalf("insert token_usage(session): %v", err)
	}
}
