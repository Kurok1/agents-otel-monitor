/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

// insertRateApiReq seeds one Claude api_request row with the fields the
// speed metric consumes.
func insertRateApiReq(t *testing.T, db *sql.DB, ts time.Time, model string, outTokens, durMs int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO event_api_request (ts, user_id, model, output_tokens, duration_ms)
		VALUES (?, 'test-user', ?, ?, ?)
	`, ts, model, outTokens, durMs)
	if err != nil {
		t.Fatalf("insert rate api_request: %v", err)
	}
}

// insertRateCodexUsage seeds one codex token_usage row (one response.completed).
func insertRateCodexUsage(t *testing.T, db *sql.DB, ts time.Time, model string, in, out, cached, durMs int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_token_usage
			(ts, conversation_id, model, input_token_count, output_token_count, cached_token_count, duration_ms)
		VALUES (?, 'conv-rate-1', ?, ?, ?, ?, ?)
	`, ts, model, in, out, cached, durMs)
	if err != nil {
		t.Fatalf("insert rate codex usage: %v", err)
	}
}

type codexTBTFixture struct {
	Timestamp   time.Time
	Model       string
	SampleCount int64
	SumMs       float64
}

func insertRateCodexTBT(t *testing.T, db *sql.DB, fixture codexTBTFixture) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_metric_response_tbt
			(ts, start_ts, model, sample_count, sum_ms)
			VALUES (?, ?, ?, ?, ?)
	`, fixture.Timestamp, fixture.Timestamp.Add(-time.Second), fixture.Model, fixture.SampleCount, fixture.SumMs)
	if err != nil {
		t.Fatalf("insert codex response TBT: %v", err)
	}
}

func TestQuerySpeedBuckets(t *testing.T) {
	db, w, _ := testDB(t) // now = 2026-05-13 10:00 +08 = 02:00 UTC
	spec, _ := w.ResolveRates("day")

	at := time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC) // 桶 46(01:00-02:00)
	insertRateApiReq(t, db, at, "claude-opus-4-8", 500, 10000)
	insertRateApiReq(t, db, at, "claude-opus-4-8", 300, 5000) // 同桶同模型,SQL 层已聚合
	// 过滤分支:duration=0 / output=0 / 空 model 都不参与
	insertRateApiReq(t, db, at, "claude-opus-4-8", 100, 0)
	insertRateApiReq(t, db, at, "claude-opus-4-8", 0, 1000)
	insertRateApiReq(t, db, at, "", 100, 1000)
	// 窗口外
	insertRateApiReq(t, db, spec.Start.Add(-time.Hour), "claude-opus-4-8", 999, 1000)
	// codex 臂
	insertRateCodexTBT(t, db, codexTBTFixture{
		Timestamp: at, Model: "gpt-5.1-codex", SampleCount: 1, SumMs: 20,
	})

	rows, err := QuerySpeedBuckets(context.Background(), db, ClientAll, spec.Start, spec.End)
	if err != nil {
		t.Fatalf("QuerySpeedBuckets: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (claude 聚合行 + codex 行): %+v", len(rows), rows)
	}
	byModel := map[string]speedBucketRow{}
	for _, r := range rows {
		byModel[r.Model] = r
		if got := spec.BucketIndex(r.Hour); got != 46 {
			t.Errorf("model %s bucket idx = %d, want 46", r.Model, got)
		}
	}
	if r := byModel["claude-opus-4-8"]; r.Units != 800 || r.DurMs != 15000 {
		t.Errorf("claude row = %+v, want out=800 dur=15000", r)
	}
	if r := byModel["gpt-5.1-codex"]; r.Units != 1 || r.DurMs != 20 {
		t.Errorf("codex row = %+v, want samples=1 sum_ms=20", r)
	}

	// client 单臂过滤(claude / codex 两个方向都验)
	claudeOnly, err := QuerySpeedBuckets(context.Background(), db, ClientClaude, spec.Start, spec.End)
	if err != nil {
		t.Fatalf("QuerySpeedBuckets(claude): %v", err)
	}
	if len(claudeOnly) != 1 || claudeOnly[0].Model != "claude-opus-4-8" {
		t.Errorf("claude arm rows = %+v", claudeOnly)
	}
	codexOnly, err := QuerySpeedBuckets(context.Background(), db, ClientCodex, spec.Start, spec.End)
	if err != nil {
		t.Fatalf("QuerySpeedBuckets(codex): %v", err)
	}
	if len(codexOnly) != 1 || codexOnly[0].Model != "gpt-5.1-codex" {
		t.Errorf("codex arm rows = %+v", codexOnly)
	}
}

func TestQuerySpeedWindow(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.ResolveRates("day")

	at := time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC)
	insertRateApiReq(t, db, at, "claude-opus-4-8", 500, 10000)
	insertRateCodexTBT(t, db, codexTBTFixture{
		Timestamp: at, Model: "gpt-5.1-codex", SampleCount: 1, SumMs: 20,
	})

	got, err := QuerySpeedWindow(context.Background(), db, ClientAll, spec.Start, spec.End)
	if err != nil {
		t.Fatalf("QuerySpeedWindow: %v", err)
	}
	if got.Units != 501 || got.DurMs != 10020 {
		t.Errorf("window = %+v, want units=501 dur=10020", got)
	}

	// 空窗口(previous):全零,调用方转 null
	prev, err := QuerySpeedWindow(context.Background(), db, ClientAll,
		spec.Start.Add(-48*time.Hour), spec.Start)
	if err != nil {
		t.Fatalf("QuerySpeedWindow(prev): %v", err)
	}
	if prev.Units != 0 || prev.DurMs != 0 {
		t.Errorf("empty window = %+v, want zeros", prev)
	}
}

func TestQueryThroughputBuckets(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.ResolveRates("day")

	at := time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC) // 桶 46
	// Claude 臂:metric_token_usage 四类 delta
	insertTokenUsage(t, db, at, "claude-opus-4-8", "input", 600)
	insertTokenUsage(t, db, at, "claude-opus-4-8", "output", 120)
	insertTokenUsage(t, db, at, "claude-opus-4-8", "cacheRead", 6000)
	insertTokenUsage(t, db, at, "claude-opus-4-8", "cacheCreation", 300)
	// Codex 臂投影:input→input-cached(钳0)、cacheRead→cached、cacheCreation→0
	insertRateCodexUsage(t, db, at, "gpt-5.1-codex", 1000, 400, 1200, 8000) // cached>input,钳 0

	rows, err := QueryThroughputBuckets(context.Background(), db, ClientAll, spec.Start, spec.End)
	if err != nil {
		t.Fatalf("QueryThroughputBuckets: %v", err)
	}
	// 两臂同一小时 → 各出一行,builder 再合并;这里按小时聚起来断言
	agg := throughputBucketRow{}
	for _, r := range rows {
		if got := spec.BucketIndex(r.Hour); got != 46 {
			t.Fatalf("bucket idx = %d, want 46", got)
		}
		agg.In += r.In
		agg.Out += r.Out
		agg.CacheRead += r.CacheRead
		agg.CacheCreation += r.CacheCreation
	}
	// In = 600(claude) + max(1000-1200,0)(codex) = 600
	// Out = 120 + 400;CacheRead = 6000 + 1200;CacheCreation = 300 + 0
	if agg.In != 600 || agg.Out != 520 || agg.CacheRead != 7200 || agg.CacheCreation != 300 {
		t.Errorf("agg = %+v, want in=600 out=520 cacheRead=7200 cacheCreation=300", agg)
	}
}

func TestBuildRatesWeightedMergeAcrossGroups(t *testing.T) {
	db, w, _ := testDB(t)
	c, err := NewClassifier(nil)
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}

	// 同组两个原始 model(都折叠为 opus-4.8):
	//   modelA: 500 tok / 10s = 50 tok/s;modelB: 3000 tok / 30s = 100 tok/s
	// 加权 = 3500*1000/40000 = 87.5;算术平均 75 —— 断言能区分两者
	at := time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC) // 桶 46
	insertRateApiReq(t, db, at, "claude-opus-4-8", 500, 10000)
	insertRateApiReq(t, db, at, "claude-opus-4-8[1m]", 3000, 30000)

	resp, err := BuildRates(context.Background(), db, c, w, "day", ClientClaude)
	if err != nil {
		t.Fatalf("BuildRates: %v", err)
	}
	if resp.Range != "day" || resp.BucketInterval != "1h" {
		t.Errorf("meta = %s/%s", resp.Range, resp.BucketInterval)
	}
	if len(resp.Speed.Points) != 48 || len(resp.Throughput.Points) != 48 {
		t.Fatalf("points = %d/%d, want 48/48", len(resp.Speed.Points), len(resp.Throughput.Points))
	}
	if len(resp.Speed.Groups) != 1 || resp.Speed.Groups[0] != "opus-4.8" {
		t.Fatalf("groups = %v, want [opus-4.8]", resp.Speed.Groups)
	}
	v, ok := resp.Speed.Points[46].Values["opus-4.8"]
	if !ok {
		t.Fatal("bucket 46 missing group value")
	}
	if v < 87.49 || v > 87.51 {
		t.Errorf("weighted speed = %v, want 87.5 (NOT 75)", v)
	}
	// 空桶:speed 无该 key(null 语义)
	if _, ok := resp.Speed.Points[0].Values["opus-4.8"]; ok {
		t.Error("empty bucket must omit group key")
	}
	// KPI:窗口整体加权;previous 空窗口 → null
	if resp.Speed.Current == nil || *resp.Speed.Current < 87.49 || *resp.Speed.Current > 87.51 {
		t.Errorf("current = %v, want 87.5", resp.Speed.Current)
	}
	if resp.Speed.Previous != nil {
		t.Errorf("previous = %v, want nil", *resp.Speed.Previous)
	}
}

func TestBuildRatesAllDoesNotMixClaudeAndCodexSpeedKPIs(t *testing.T) {
	db, w, _ := testDB(t)
	c, err := NewClassifier(nil)
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	at := time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC)
	insertRateApiReq(t, db, at, "claude-opus-4-8", 1000, 10000)
	insertRateCodexTBT(t, db, codexTBTFixture{
		Timestamp: at, Model: "gpt-5.1-codex", SampleCount: 1, SumMs: 20,
	})

	resp, err := BuildRates(context.Background(), db, c, w, "day", ClientAll)
	if err != nil {
		t.Fatalf("BuildRates: %v", err)
	}
	if resp.Speed.Current != nil {
		t.Fatalf("mixed current = %v, want nil for incompatible speed methods", *resp.Speed.Current)
	}
	point := resp.Speed.Points[46].Values
	if got := point["opus-4.8"]; got < 99.99 || got > 100.01 {
		t.Fatalf("Claude point = %v, want 100 tok/s", got)
	}
	if got := point["gpt-5.1-codex"]; got < 49.99 || got > 50.01 {
		t.Fatalf("Codex point = %v, want 50 tok/s", got)
	}
}

func TestBuildRatesAllDoesNotCompareDifferentSourcesAcrossWindows(t *testing.T) {
	db, w, _ := testDB(t)
	c, err := NewClassifier(nil)
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	spec, err := w.ResolveRates("day")
	if err != nil {
		t.Fatalf("ResolveRates: %v", err)
	}
	insertRateCodexTBT(t, db, codexTBTFixture{
		Timestamp: spec.Start.Add(time.Hour), Model: "gpt-5.1-codex", SampleCount: 1, SumMs: 20,
	})
	insertRateApiReq(t, db, spec.Start.Add(-time.Hour), "claude-opus-4-8", 1000, 10000)

	resp, err := BuildRates(context.Background(), db, c, w, "day", ClientAll)
	if err != nil {
		t.Fatalf("BuildRates: %v", err)
	}
	if resp.Speed.Current != nil || resp.Speed.Previous != nil {
		t.Fatalf("all-client KPIs = current:%v previous:%v, want both nil", resp.Speed.Current, resp.Speed.Previous)
	}
}

func TestBuildRatesAllSeparatesCrossClientGroupCollisions(t *testing.T) {
	db, w, _ := testDB(t)
	c, err := NewClassifier([]config.ModelGroupRule{{Pattern: ".*", Group: "merged"}})
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	at := time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC)
	insertRateApiReq(t, db, at, "claude-opus-4-8", 1000, 10000)
	insertRateCodexTBT(t, db, codexTBTFixture{
		Timestamp: at, Model: "gpt-5.1-codex", SampleCount: 1, SumMs: 20,
	})

	resp, err := BuildRates(context.Background(), db, c, w, "day", ClientAll)
	if err != nil {
		t.Fatalf("BuildRates: %v", err)
	}
	point := resp.Speed.Points[46].Values
	if got := point["merged · Claude"]; got < 99.99 || got > 100.01 {
		t.Fatalf("Claude collision point = %v, want 100 tok/s", got)
	}
	if got := point["merged · Codex"]; got < 49.99 || got > 50.01 {
		t.Fatalf("Codex collision point = %v, want 50 tok/s", got)
	}
	if _, mixed := point["merged"]; mixed {
		t.Fatalf("cross-client group must not be merged: %+v", point)
	}
}

func TestBuildRatesThroughputNormalization(t *testing.T) {
	db, _, _ := testDB(t)
	c, _ := NewClassifier(nil)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	// 用 10:30 锚点让末桶(02:00 UTC 起)已流逝 30 分钟
	w2, err := NowWindow(time.Date(2026, 5, 13, 10, 30, 0, 0, loc), "Asia/Shanghai")
	if err != nil {
		t.Fatalf("NowWindow: %v", err)
	}

	// 满桶(01:00-02:00 UTC,桶 46):600 output → 10 tok/min
	insertTokenUsage(t, db, time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC), "claude-opus-4-8", "output", 600)
	// 末桶(02:00-,桶 47,流逝 30min):300 output → 10 tok/min(除以 30 而非 60)
	insertTokenUsage(t, db, time.Date(2026, 5, 13, 2, 10, 0, 0, time.UTC), "claude-opus-4-8", "output", 300)

	resp, err := BuildRates(context.Background(), db, c, w2, "day", ClientClaude)
	if err != nil {
		t.Fatalf("BuildRates: %v", err)
	}
	if len(resp.Throughput.Types) != 4 || resp.Throughput.Types[0] != "input" {
		t.Fatalf("types = %v", resp.Throughput.Types)
	}
	full := resp.Throughput.Points[46].Values["output"]
	if full < 9.99 || full > 10.01 {
		t.Errorf("full bucket = %v tok/min, want 10", full)
	}
	partial := resp.Throughput.Points[47].Values["output"]
	if partial < 9.99 || partial > 10.01 {
		t.Errorf("partial bucket = %v tok/min, want 10 (300 tok / 30 min)", partial)
	}
	// 空桶补 0
	if got := resp.Throughput.Points[0].Values["output"]; got != 0 {
		t.Errorf("empty bucket = %v, want 0", got)
	}
}
