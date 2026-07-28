package dashboard

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/store"
)

// testDB builds a fresh DuckDB in a temp dir, applies migrations, and
// returns a *sql.DB plus the "now" the queries are anchored at. The "now"
// is fixed so windows are deterministic: 2026-05-13 10:00:00 Asia/Shanghai
// = 2026-05-13 02:00:00 UTC. May 13 is a Wednesday.
func testDB(t *testing.T) (*sql.DB, TimeWindow, time.Time) {
	t.Helper()

	dir := t.TempDir()
	cfg := config.StorageConfig{DuckDBPath: filepath.Join(dir, "test.duckdb")}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	migrations, err := store.LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	if err := store.RunMigrations(db.SQL, migrations); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, loc)
	w, err := NowWindow(now, "Asia/Shanghai")
	if err != nil {
		t.Fatalf("NowWindow: %v", err)
	}
	return db.SQL, w, now
}

func insertTokenUsage(t *testing.T, db *sql.DB, ts time.Time, model, typ string, value int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO metric_token_usage (ts, start_ts, value, user_id, model, type)
		VALUES (?, ?, ?, 'test-user', ?, ?)
	`, ts, ts, value, model, typ)
	if err != nil {
		t.Fatalf("insert token_usage: %v", err)
	}
}

func insertCostUsage(t *testing.T, db *sql.DB, ts time.Time, model string, value float64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO metric_cost_usage (ts, start_ts, value, user_id, model)
		VALUES (?, ?, ?, 'test-user', ?)
	`, ts, ts, value, model)
	if err != nil {
		t.Fatalf("insert cost_usage: %v", err)
	}
}

func insertApiRequest(t *testing.T, db *sql.DB, ts time.Time, model string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO event_api_request (ts, user_id, model)
		VALUES (?, 'test-user', ?)
	`, ts, model)
	if err != nil {
		t.Fatalf("insert api_request: %v", err)
	}
}

func insertToolResult(t *testing.T, db *sql.DB, ts time.Time, name string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO event_tool_result (ts, user_id, tool_name)
		VALUES (?, 'test-user', ?)
	`, ts, name)
	if err != nil {
		t.Fatalf("insert tool_result: %v", err)
	}
}

func insertSkillActivated(t *testing.T, db *sql.DB, ts time.Time, name string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO event_skill_activated (ts, user_id, skill_name)
		VALUES (?, 'test-user', ?)
	`, ts, name)
	if err != nil {
		t.Fatalf("insert skill_activated: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────

func TestQueryPeriodTokens_Day(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("day")

	inToday := spec.CurrentStart.Add(2 * time.Hour)
	insertTokenUsage(t, db, inToday, "claude-opus-4-1", "input", 100)
	insertTokenUsage(t, db, inToday, "claude-opus-4-1", "output", 50)
	insertTokenUsage(t, db, inToday, "claude-sonnet-4-5", "cacheRead", 30)
	// Yesterday — counted in prev window only.
	insertTokenUsage(t, db, spec.PreviousStart.Add(time.Hour), "claude-opus-4-1", "input", 999)

	got, err := QueryPeriodTokens(context.Background(), db, ClientAll, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("QueryPeriodTokens: %v", err)
	}
	if got.In != 100 || got.Out != 50 || got.Total != 180 {
		t.Errorf("got %+v, want in=100 out=50 total=180", got)
	}
	prev, err := QueryPeriodTokensTotal(context.Background(), db, ClientAll, spec.PreviousStart, spec.PreviousEnd)
	if err != nil {
		t.Fatalf("QueryPeriodTokensTotal: %v", err)
	}
	if prev != 999 {
		t.Errorf("prev = %d, want 999", prev)
	}
}

func TestQueryPeriodTokens_Week(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("week")
	// This week's Monday is 2026-05-11 00:00 SH.

	// Put a row at SH 2026-05-12 (Tue) — inside this week.
	insertTokenUsage(t, db, spec.CurrentStart.Add(24*time.Hour+time.Hour),
		"claude-opus-4-1", "input", 200)
	// Put a row at SH 2026-05-09 (Sat last week) — previous week.
	insertTokenUsage(t, db, spec.PreviousStart.Add(5*24*time.Hour+time.Hour),
		"claude-opus-4-1", "input", 500)
	// Way out of range
	insertTokenUsage(t, db, spec.PreviousStart.Add(-24*time.Hour), "claude-opus-4-1", "input", 999)

	got, _ := QueryPeriodTokens(context.Background(), db, ClientAll, spec.CurrentStart, spec.CurrentEnd)
	if got.In != 200 {
		t.Errorf("week.In = %d, want 200", got.In)
	}
	prev, _ := QueryPeriodTokensTotal(context.Background(), db, ClientAll, spec.PreviousStart, spec.PreviousEnd)
	if prev != 500 {
		t.Errorf("week.Prev = %d, want 500", prev)
	}
}

func TestQueryPeriodTokens_Month(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("month")
	// May 2026 → prev = Apr 2026.

	insertTokenUsage(t, db, spec.CurrentStart.Add(3*24*time.Hour), "claude-opus-4-1", "input", 700)
	insertTokenUsage(t, db, spec.PreviousStart.Add(5*24*time.Hour), "claude-opus-4-1", "input", 1000)
	insertTokenUsage(t, db, spec.PreviousStart.Add(-24*time.Hour), "claude-opus-4-1", "input", 999)

	got, _ := QueryPeriodTokens(context.Background(), db, ClientAll, spec.CurrentStart, spec.CurrentEnd)
	if got.In != 700 {
		t.Errorf("month.In = %d, want 700", got.In)
	}
	prev, _ := QueryPeriodTokensTotal(context.Background(), db, ClientAll, spec.PreviousStart, spec.PreviousEnd)
	if prev != 1000 {
		t.Errorf("month.Prev = %d, want 1000", prev)
	}
}

func TestQueryTokensSparkline_LocalDayBucketing(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("day")

	// SH 2026-05-13 00:30 = UTC 2026-05-12 16:30 — must bucket as 05-13 not 05-12.
	insertTokenUsage(t, db, spec.CurrentStart.Add(30*time.Minute), "claude-opus-4-1", "input", 11)
	insertTokenUsage(t, db, spec.CurrentStart.Add(-5*24*time.Hour).Add(time.Hour),
		"claude-opus-4-1", "output", 22)

	got, err := QueryTokensSparkline(context.Background(), db, ClientAll, w, "day",
		spec.SparklineStart, spec.PeriodEnd)
	if err != nil {
		t.Fatalf("QueryTokensSparkline: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	want13 := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	want08 := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	if !got[0].Bucket.Equal(want08) || got[0].Total != 22 {
		t.Errorf("bucket[0] = %+v, want 5-8/22", got[0])
	}
	if !got[1].Bucket.Equal(want13) || got[1].Total != 11 {
		t.Errorf("bucket[1] = %+v, want 5-13/11", got[1])
	}
}

func TestQueryTokensSparkline_WeekGrain(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("week")

	// Drop one row in each of three different weeks within the 12-week window.
	insertTokenUsage(t, db, spec.CurrentStart.Add(time.Hour), "claude-opus-4-1", "input", 7)
	insertTokenUsage(t, db, spec.CurrentStart.Add(-7*24*time.Hour).Add(time.Hour),
		"claude-opus-4-1", "input", 14)
	insertTokenUsage(t, db, spec.CurrentStart.Add(-2*7*24*time.Hour).Add(time.Hour),
		"claude-opus-4-1", "input", 21)

	got, _ := QueryTokensSparkline(context.Background(), db, ClientAll, w, "week",
		spec.SparklineStart, spec.PeriodEnd)
	if len(got) != 3 {
		t.Fatalf("len(week buckets) = %d, want 3: %+v", len(got), got)
	}
}

func TestQueryPeriodCost(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("month")

	insertCostUsage(t, db, spec.CurrentStart.Add(24*time.Hour), "claude-opus-4-1", 1.25)
	insertCostUsage(t, db, spec.CurrentStart.Add(2*24*time.Hour), "claude-sonnet-4-5", 0.75)
	insertCostUsage(t, db, spec.PreviousStart.Add(24*time.Hour), "claude-opus-4-1", 99.0)

	got, err := QueryPeriodCost(context.Background(), db, ClientAll, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("QueryPeriodCost: %v", err)
	}
	if got != 2.0 {
		t.Errorf("cost = %v, want 2.0", got)
	}
}

func TestQueryPeriodCache(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("day")

	insertTokenUsage(t, db, spec.CurrentStart.Add(time.Hour), "claude-opus-4-1", "cacheRead", 80)
	insertTokenUsage(t, db, spec.CurrentStart.Add(time.Hour), "claude-opus-4-1", "cacheCreation", 20)
	// Should be excluded — input / output are unrelated to cache efficacy:
	insertTokenUsage(t, db, spec.CurrentStart.Add(time.Hour), "claude-opus-4-1", "input", 999)
	insertTokenUsage(t, db, spec.CurrentStart.Add(time.Hour), "claude-opus-4-1", "output", 999)

	pc, err := QueryPeriodCache(context.Background(), db, ClientAll, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("QueryPeriodCache: %v", err)
	}
	if pc.Read != 80 || pc.Creation != 20 || pc.HitDenom != 100 {
		t.Errorf("got %+v, want read=80 creation=20 denom=100", pc)
	}
}

func TestCacheHitRate(t *testing.T) {
	cases := []struct {
		name           string
		read, creation int64
		wantNil        bool
		wantRateApprox float64
	}{
		{name: "hit_and_creation", read: 80, creation: 20, wantRateApprox: 0.80},
		{name: "all_reads", read: 100, creation: 0, wantRateApprox: 1.0},
		{name: "all_creations", read: 0, creation: 100, wantRateApprox: 0.0},
		{name: "no_cache_activity", read: 0, creation: 0, wantNil: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := hitRateFrom(periodCache{Read: c.read, Creation: c.creation, HitDenom: c.read + c.creation})
			if c.wantNil {
				if got != nil {
					t.Errorf("got %v, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want %v", c.wantRateApprox)
			}
			if diff := *got - c.wantRateApprox; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("got %v, want %v", *got, c.wantRateApprox)
			}
		})
	}
}

func TestQueryModelTokens_RawModels(t *testing.T) {
	db, w, _ := testDB(t)
	ts := w.TodayStartUTC.Add(time.Hour)

	insertTokenUsage(t, db, ts, "claude-opus-4-1-20250805", "input", 10)
	insertTokenUsage(t, db, ts, "claude-sonnet-4-5", "input", 20)
	insertTokenUsage(t, db, ts, "claude-haiku-4-5", "output", 30)
	insertTokenUsage(t, db, ts, "deepseek-v3", "cacheRead", 5)

	got, err := QueryModelTokens(context.Background(), db, ClientAll)
	if err != nil {
		t.Fatalf("QueryModelTokens: %v", err)
	}
	byModel := map[string]modelTokens{}
	for _, r := range got {
		byModel[r.Model] = r
	}
	if byModel["claude-opus-4-1-20250805"].TokensIn != 10 {
		t.Errorf("opus row = %+v", byModel["claude-opus-4-1-20250805"])
	}
	if byModel["claude-sonnet-4-5"].TokensIn != 20 {
		t.Errorf("sonnet row = %+v", byModel["claude-sonnet-4-5"])
	}
	if byModel["claude-haiku-4-5"].TokensOut != 30 {
		t.Errorf("haiku row = %+v", byModel["claude-haiku-4-5"])
	}
	if byModel["deepseek-v3"].CacheTokens != 5 {
		t.Errorf("deepseek row = %+v", byModel["deepseek-v3"])
	}
}

func TestQueryTrends_DayGrain(t *testing.T) {
	db, w, _ := testDB(t)

	insertTokenUsage(t, db, w.TodayStartUTC.Add(2*time.Hour), "claude-opus-4-1", "input", 100)
	insertTokenUsage(t, db, w.TodayStartUTC.Add(-24*time.Hour).Add(time.Hour),
		"claude-opus-4-1", "input", 50)
	insertTokenUsage(t, db, w.TodayStartUTC.Add(-2*24*time.Hour).Add(time.Hour),
		"claude-sonnet-4-5", "output", 25)

	got, err := QueryTrends(context.Background(), db, ClientAll, w, "day", w.DayTrendStartUTC)
	if err != nil {
		t.Fatalf("QueryTrends: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(rows) = %d, want 3: %+v", len(got), got)
	}
}

func TestQueryToolsRanking(t *testing.T) {
	db, w, _ := testDB(t)
	ts := w.TodayStartUTC.Add(time.Hour)

	for i := 0; i < 5; i++ {
		insertToolResult(t, db, ts, "Read")
	}
	for i := 0; i < 3; i++ {
		insertToolResult(t, db, ts, "Bash")
	}
	insertToolResult(t, db, ts, "Grep")
	insertToolResult(t, db, w.TodayStartUTC.Add(-40*24*time.Hour), "Read")

	got, err := QueryToolsRanking(context.Background(), db, RankingsOpts{Client: ClientAll, ToolsTopN: 10})
	if err != nil {
		t.Fatalf("QueryToolsRanking: %v", err)
	}
	if len(got) != 3 || got[0].Name != "Read" || got[0].Count != 6 {
		t.Errorf("all-time: %+v", got)
	}

	got, err = QueryToolsRanking(context.Background(), db, RankingsOpts{
		Client:     ClientAll,
		SinceStart: w.TodayStartUTC.Add(-7 * 24 * time.Hour),
		ToolsTopN:  10,
	})
	if err != nil {
		t.Fatalf("QueryToolsRanking 7d: %v", err)
	}
	if len(got) != 3 || got[0].Name != "Read" || got[0].Count != 5 {
		t.Errorf("7d: %+v", got)
	}
}

func TestQuerySkillsRanking(t *testing.T) {
	db, w, _ := testDB(t)
	ts := w.TodayStartUTC.Add(time.Hour)
	for i := 0; i < 7; i++ {
		insertSkillActivated(t, db, ts, "frontend-design")
	}
	for i := 0; i < 4; i++ {
		insertSkillActivated(t, db, ts, "pdf")
	}

	got, err := QuerySkillsRanking(context.Background(), db, RankingsOpts{Client: ClientAll, SkillsTopN: 10})
	if err != nil {
		t.Fatalf("QuerySkillsRanking: %v", err)
	}
	if len(got) != 2 || got[0].Name != "frontend-design" || got[0].Activations != 7 {
		t.Errorf("ranking = %+v", got)
	}
}

func TestQueryModelCostAndRequests(t *testing.T) {
	db, w, _ := testDB(t)
	ts := w.TodayStartUTC.Add(time.Hour)

	insertCostUsage(t, db, ts, "claude-opus-4-1", 1.5)
	insertCostUsage(t, db, ts, "claude-opus-4-1", 2.5)
	insertCostUsage(t, db, ts, "claude-sonnet-4-5", 1.0)

	insertApiRequest(t, db, ts, "claude-opus-4-1")
	insertApiRequest(t, db, ts, "claude-opus-4-1")
	insertApiRequest(t, db, ts, "claude-haiku-4-5")

	costs, _ := QueryModelCost(context.Background(), db, ClientAll)
	byModel := map[string]float64{}
	for _, c := range costs {
		byModel[c.Model] = c.Cost
	}
	if byModel["claude-opus-4-1"] != 4.0 || byModel["claude-sonnet-4-5"] != 1.0 {
		t.Errorf("costs = %v", byModel)
	}

	reqs, _ := QueryModelRequests(context.Background(), db, ClientAll)
	byR := map[string]int64{}
	for _, r := range reqs {
		byR[r.Model] = r.Requests
	}
	if byR["claude-opus-4-1"] != 2 || byR["claude-haiku-4-5"] != 1 {
		t.Errorf("requests = %v", byR)
	}
}

// mergeModelGroups should fold raw models into classifier groups, sum
// across the snapshots/`[1m]` variants, and emit rows ordered by total
// tokens descending.
func TestMergeModelGroups_FoldsClaudeVariants(t *testing.T) {
	c, err := NewClassifier(nil)
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	tok := []modelTokens{
		{Model: "claude-opus-4-7", TokensIn: 100, TokensOut: 50},
		{Model: "claude-opus-4-7[1m]", TokensIn: 30, TokensOut: 20},
		{Model: "claude-haiku-4-5-20251001", CacheTokens: 5},
		{Model: "deepseek-v3", TokensIn: 7},
	}
	costs := []modelCost{
		{Model: "claude-opus-4-7", Cost: 1.5},
		{Model: "claude-opus-4-7[1m]", Cost: 0.5},
	}
	reqs := []modelRequests{
		{Model: "claude-opus-4-7", Requests: 3},
		{Model: "claude-opus-4-7[1m]", Requests: 4},
		{Model: "deepseek-v3", Requests: 1},
	}

	got := mergeModelGroups(c, tok, costs, reqs)
	if len(got) != 3 {
		t.Fatalf("len(groups) = %d, want 3: %+v", len(got), got)
	}
	// Order: opus-4.7 (200 tok) > deepseek-v3 (7 tok) > haiku-4.5 (5 tok)
	if got[0].Group != "opus-4.7" || got[0].Requests != 7 || got[0].Cost != 2.0 {
		t.Errorf("group[0] = %+v, want opus-4.7 r=7 cost=2.0", got[0])
	}
	if got[0].TokensIn != 130 || got[0].TokensOut != 70 {
		t.Errorf("group[0] tokens = %+v, want in=130 out=70", got[0])
	}
	if got[1].Group != "deepseek-v3" {
		t.Errorf("group[1] = %+v, want deepseek-v3", got[1])
	}
	if got[2].Group != "haiku-4.5" || got[2].CacheTokens != 5 {
		t.Errorf("group[2] = %+v", got[2])
	}
}

func insertCodexCostRow(t *testing.T, db *sql.DB, ts time.Time, model string, cost sql.NullFloat64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_token_usage
		  (ts, conversation_id, model, input_token_count, output_token_count, cost_usd)
		VALUES (?, 'conv-1', ?, 100, 100, ?)
	`, ts, model, cost)
	if err != nil {
		t.Fatalf("insert codex cost row: %v", err)
	}
}

func TestQueryPeriodCostIncludesCodex(t *testing.T) {
	db, w, _ := testDB(t)
	ctx := context.Background()
	ts := w.TodayStartUTC.Add(time.Hour)
	insertCostUsage(t, db, ts, "claude-opus-4-1", 1.50)                                   // claude authoritative
	insertCodexCostRow(t, db, ts, "gpt-5.5", sql.NullFloat64{Float64: 0.25, Valid: true}) // codex estimated
	insertCodexCostRow(t, db, ts, "gpt-5.5", sql.NullFloat64{})                           // NULL → ignored

	start, end := w.TodayStartUTC, w.TodayEndUTC
	claudeOnly, _ := QueryPeriodCost(ctx, db, ClientClaude, start, end)
	codexOnly, _ := QueryPeriodCost(ctx, db, ClientCodex, start, end)
	all, _ := QueryPeriodCost(ctx, db, ClientAll, start, end)
	if claudeOnly != 1.50 {
		t.Fatalf("claude cost = %v, want 1.50", claudeOnly)
	}
	if codexOnly != 0.25 {
		t.Fatalf("codex cost = %v, want 0.25 (NULL row ignored)", codexOnly)
	}
	if all != 1.75 {
		t.Fatalf("all cost = %v, want 1.75", all)
	}
}

func TestSnapshotCostEstimatedFlag(t *testing.T) {
	db, w, _ := testDB(t)
	ctx := context.Background()
	c, _ := NewClassifier(nil)

	claude, err := BuildSnapshot(ctx, db, c, w, "day", ClientClaude, true)
	if err != nil {
		t.Fatalf("claude snapshot: %v", err)
	}
	if claude.Cost.Estimated {
		t.Fatal("claude view must not be flagged estimated")
	}
	codexOn, _ := BuildSnapshot(ctx, db, c, w, "day", ClientCodex, true)
	if !codexOn.Cost.Estimated {
		t.Fatal("codex view with pricing enabled must be estimated")
	}
	codexOff, _ := BuildSnapshot(ctx, db, c, w, "day", ClientCodex, false)
	if codexOff.Cost.Estimated {
		t.Fatal("codex view with pricing disabled must not be estimated")
	}
}

func TestHeatmapCodexWeightGating(t *testing.T) {
	db, w, _ := testDB(t)
	ctx := context.Background()
	weights := HeatmapWeights{Tokens: 0.4, Cost: 0.4, Requests: 0.2}
	// Both signatures must run; disabled=2-weight is asserted numerically in
	// TestBuildHeatmap_CodexWeightDenominator.
	if _, err := BuildHeatmap(ctx, db, w, weights, ClientCodex, false); err != nil {
		t.Fatalf("disabled: %v", err)
	}
	if _, err := BuildHeatmap(ctx, db, w, weights, ClientCodex, true); err != nil {
		t.Fatalf("enabled: %v", err)
	}
}

func TestSessionDetailCost(t *testing.T) {
	db, _, now := testDB(t)
	ctx := context.Background()

	// claude session: authoritative cost + some activity so it's found.
	if _, err := db.Exec(`INSERT INTO metric_cost_usage (ts, start_ts, value, user_id, session_id, model) VALUES (?, ?, 2.0, 'u', 'sess-claude', 'claude-opus-4-1')`, now, now); err != nil {
		t.Fatalf("seed claude cost: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO event_api_request (ts, user_id, session_id, model) VALUES (?, 'u', 'sess-claude', 'claude-opus-4-1')`, now); err != nil {
		t.Fatalf("seed claude activity: %v", err)
	}
	resp, found, err := BuildSessionDetail(ctx, db, "sess-claude", ClientClaude, 5, 5, true)
	if err != nil || !found {
		t.Fatalf("claude detail: found=%v err=%v", found, err)
	}
	if resp.Cost == nil || *resp.Cost != 2.0 || resp.CostEstimated {
		t.Fatalf("claude cost should be authoritative 2.0, got %+v estimated=%v", resp.Cost, resp.CostEstimated)
	}

	// codex session: estimated cost, gated on pricingEnabled.
	insertCodexCostRow(t, db, now, "gpt-5.5", sql.NullFloat64{Float64: 0.5, Valid: true}) // conversation_id = conv-1
	on, found, _ := BuildSessionDetail(ctx, db, "conv-1", ClientCodex, 5, 5, true)
	if !found || on.Cost == nil || *on.Cost != 0.5 || !on.CostEstimated {
		t.Fatalf("codex enabled cost wrong: cost=%v estimated=%v found=%v", on.Cost, on.CostEstimated, found)
	}
	off, _, _ := BuildSessionDetail(ctx, db, "conv-1", ClientCodex, 5, 5, false)
	if off.Cost != nil {
		t.Fatalf("codex disabled cost must be nil, got %v", *off.Cost)
	}
}
