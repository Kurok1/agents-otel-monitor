/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.1.0
 */

package dashboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/pricing"
	"github.com/kuroky/claude-code-monitor/internal/store"
)

const archiveTestTimezone = "Asia/Shanghai"

var archiveTestNow = time.Date(2026, 9, 17, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))

// TestArchiveDashboardParitySynthetic exercises the public dashboard builders
// over data split by the retention boundary. It locks both summary contents and
// the visible API contract, so a before/after comparison cannot mask a loss
// caused by matching buggy queries on both sides.
func TestArchiveDashboardParitySynthetic(t *testing.T) {
	ctx := context.Background()
	db := openArchiveTestDB(t)
	cutoff := archiveCutoffForTest(t, archiveTestNow)
	seedArchiveFixture(t, db.SQL, cutoff, archiveTestNow.UTC())

	before := captureArchiveDashboard(t, ctx, db.SQL, archiveTestNow, []archiveSessionTarget{{Client: ClientClaude, ID: "claude-cross"}, {Client: ClientCodex, ID: "codex-cross"}})

	archiver, err := store.NewArchiver(db, archiveTestTimezone, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewArchiver: %v", err)
	}
	result, err := archiver.RunOnce(ctx, archiveTestNow)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !result.Cutoff.Equal(cutoff) {
		t.Fatalf("cutoff = %s, want %s", result.Cutoff, cutoff)
	}
	if result.DeletedRows == 0 || result.Days == 0 {
		t.Fatalf("archive result = %+v, want expired fixture rows deleted", result)
	}

	assertArchiveFixtureSummaries(t, db.SQL, cutoff)
	after := captureArchiveDashboard(t, ctx, db.SQL, archiveTestNow, []archiveSessionTarget{{Client: ClientClaude, ID: "claude-cross"}, {Client: ClientCodex, ID: "codex-cross"}})
	assertArchiveCaptureEqual(t, before, after)
	assertArchiveVisibleSemantics(t, ctx, db.SQL)
	assertArchiveTopNCrossBoundary(t, ctx, db.SQL)

	lateExpired := cutoff.Add(-30 * time.Minute)
	execArchiveFixture(t, db.SQL, `INSERT INTO metric_token_usage (ts,start_ts,value,user_id,session_id,model,type) VALUES (?, ?, 9, 'fixture', 'claude-cross', 'claude-opus-4-7', 'input')`, lateExpired, lateExpired)
	lateResult, err := archiver.RunOnce(ctx, archiveTestNow)
	if err != nil {
		t.Fatalf("late expired RunOnce: %v", err)
	}
	if lateResult.DeletedRows != 1 || lateResult.Days != 1 {
		t.Fatalf("late expired result = %+v, want one archived raw row", lateResult)
	}
	var lateTokens int64
	if err := db.SQL.QueryRow(`SELECT tokens_total FROM archive.sessions WHERE client='claude' AND session_id='claude-cross'`).Scan(&lateTokens); err != nil {
		t.Fatalf("query late archived session: %v", err)
	}
	if lateTokens != 209 {
		t.Fatalf("late archived Claude tokens = %d, want 209", lateTokens)
	}
	afterLate := captureArchiveDashboard(t, ctx, db.SQL, archiveTestNow, []archiveSessionTarget{{Client: ClientClaude, ID: "claude-cross"}, {Client: ClientCodex, ID: "codex-cross"}})

	again, err := archiver.RunOnce(ctx, archiveTestNow)
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if again.DeletedRows != 0 || again.Days != 0 {
		t.Fatalf("second archive result = %+v, want idempotent zero sweep", again)
	}
	afterAgain := captureArchiveDashboard(t, ctx, db.SQL, archiveTestNow, []archiveSessionTarget{{Client: ClientClaude, ID: "claude-cross"}, {Client: ClientCodex, ID: "codex-cross"}})
	assertArchiveCaptureEqual(t, afterLate, afterAgain)
}

// TestArchiveDashboardParityBackup is intentionally opt-in because it copies
// and compacts a production-shaped backup. The original is opened only for
// copying; all migrations and archival writes happen in t.TempDir().
func TestArchiveDashboardParityBackup(t *testing.T) {
	source := os.Getenv("MONITOR_ARCHIVE_TEST_DB")
	if source == "" {
		t.Skip("set MONITOR_ARCHIVE_TEST_DB to run the archived-backup regression")
	}
	sourceHash := archiveFileSHA256(t, source)

	ctx := context.Background()
	db := openArchiveCopy(t, source)
	beforeLists := archiveSessionTargets(t, ctx, db.SQL)
	before := captureArchiveDashboard(t, ctx, db.SQL, archiveTestNow, beforeLists)
	beforeRows := archiveRawRowCount(t, db.SQL)

	archiver, err := store.NewArchiver(db, archiveTestTimezone, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewArchiver: %v", err)
	}
	result, err := archiver.RunOnce(ctx, archiveTestNow)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	afterRows := archiveRawRowCount(t, db.SQL)
	if afterRows > beforeRows || result.DeletedRows != beforeRows-afterRows {
		t.Fatalf("archive rows: before=%d after=%d result_deleted=%d", beforeRows, afterRows, result.DeletedRows)
	}
	after := captureArchiveDashboard(t, ctx, db.SQL, archiveTestNow, beforeLists)
	assertArchiveCaptureEqual(t, before, after)

	second, err := archiver.RunOnce(ctx, archiveTestNow)
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if second.DeletedRows != 0 {
		t.Fatalf("second RunOnce deleted %d rows, want 0", second.DeletedRows)
	}
	if afterHash := archiveFileSHA256(t, source); afterHash != sourceHash {
		t.Fatalf("archive source backup changed during test")
	}
	summaryRows := archiveSummaryRowCount(t, db.SQL)
	var engineVersion string
	if err := db.SQL.QueryRow(`SELECT version()`).Scan(&engineVersion); err != nil {
		t.Fatalf("query DuckDB runtime version: %v", err)
	}
	t.Logf("archive backup validation: duckdb=%s raw_rows_before=%d raw_rows_after=%d archive_summary_rows=%d deleted=%d days=%d", engineVersion, beforeRows, afterRows, summaryRows, result.DeletedRows, result.Days)
}

// TestArchiveReadSnapshotDoesNotMixRawAndArchive locks the key consistency
// guarantee behind the endpoint transaction wrappers. The archival write runs
// after the cold-summary read but before the raw read; both reads in the open
// transaction must still observe one pre-archive database snapshot.
func TestArchiveReadSnapshotDoesNotMixRawAndArchive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db := openArchiveTestDB(t)
	db.SQL.SetMaxOpenConns(2)
	cutoff := archiveCutoffForTest(t, archiveTestNow)
	old := cutoff.Add(-time.Hour)
	execArchiveFixture(t, db.SQL, `INSERT INTO metric_token_usage (ts,start_ts,value,user_id,session_id,model,type) VALUES (?, ?, 100, 'fixture', 'snapshot-cross', 'claude-opus-4-7', 'input')`, old, old)

	archiver, err := store.NewArchiver(db, archiveTestTimezone, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewArchiver: %v", err)
	}
	_, err = withDashboardSnapshot(ctx, db.SQL, func(q sqlQueryer) (struct{}, error) {
		var coldBefore int64
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(tokens_total), 0) FROM archive.usage_hourly`).Scan(&coldBefore); err != nil {
			return struct{}{}, err
		}
		if coldBefore != 0 {
			return struct{}{}, fmt.Errorf("cold layer before archive = %d, want 0", coldBefore)
		}
		result, err := archiver.RunOnce(ctx, archiveTestNow)
		if err != nil {
			return struct{}{}, err
		}
		if result.DeletedRows != 1 {
			return struct{}{}, fmt.Errorf("RunOnce deleted %d rows, want 1", result.DeletedRows)
		}
		var rawDuring int64
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(value), 0) FROM metric_token_usage WHERE session_id='snapshot-cross'`).Scan(&rawDuring); err != nil {
			return struct{}{}, err
		}
		if rawDuring != 100 {
			return struct{}{}, fmt.Errorf("raw layer in wrapper snapshot = %d, want pre-archive 100", rawDuring)
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("withDashboardSnapshot archive-between-reads: %v", err)
	}
	var rawAfter, coldAfter int64
	if err := db.SQL.QueryRow(`SELECT COALESCE(SUM(value), 0) FROM metric_token_usage WHERE session_id='snapshot-cross'`).Scan(&rawAfter); err != nil {
		t.Fatalf("read raw layer after archive: %v", err)
	}
	if err := db.SQL.QueryRow(`SELECT COALESCE(SUM(tokens_total), 0) FROM archive.usage_hourly WHERE client='claude'`).Scan(&coldAfter); err != nil {
		t.Fatalf("read cold layer after archive: %v", err)
	}
	if rawAfter != 0 || coldAfter != 100 {
		t.Fatalf("fresh snapshot raw=%d cold=%d, want 0/100", rawAfter, coldAfter)
	}
}

func archiveFileSHA256(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open backup for checksum: %v", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatalf("checksum backup: %v", err)
	}
	var sum [sha256.Size]byte
	copy(sum[:], hash.Sum(nil))
	return sum
}

func openArchiveTestDB(t *testing.T) *store.DB {
	t.Helper()
	return openArchiveDB(t, filepath.Join(t.TempDir(), "archive-test.duckdb"))
}

func openArchiveCopy(t *testing.T, source string) *store.DB {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatalf("open archive source backup: %v", err)
	}
	defer in.Close()

	path := filepath.Join(t.TempDir(), "archive-backup-copy.duckdb")
	out, err := os.Create(path)
	if err != nil {
		t.Fatalf("create archive backup copy: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatalf("copy archive backup: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close archive backup copy: %v", err)
	}
	return openArchiveDB(t, path)
}

func openArchiveDB(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.Open(config.StorageConfig{DuckDBPath: path})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close archive test database: %v", err)
		}
	})
	migrations, err := store.LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	if err := store.RunMigrations(db.SQL, migrations); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	return db
}

func archiveCutoffForTest(t *testing.T, now time.Time) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(archiveTestTimezone)
	if err != nil {
		t.Fatalf("load archive test timezone: %v", err)
	}
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day()-30, 0, 0, 0, 0, loc).UTC()
}

func seedArchiveFixture(t *testing.T, db *sql.DB, cutoff, now time.Time) {
	t.Helper()
	old := cutoff.Add(-time.Hour)
	late := cutoff.Add(time.Hour)
	recent := now.Add(-time.Hour)

	execArchiveFixture(t, db, `INSERT INTO metric_token_usage (ts,start_ts,value,user_id,session_id,model,type) VALUES (?,? ,100,'fixture','claude-cross','claude-opus-4-7','input')`, old, old)
	execArchiveFixture(t, db, `INSERT INTO metric_token_usage (ts,start_ts,value,user_id,session_id,model,type) VALUES (?,? ,50,'fixture','claude-cross','claude-opus-4-7','output')`, old, old)
	execArchiveFixture(t, db, `INSERT INTO metric_token_usage (ts,start_ts,value,user_id,session_id,model,type) VALUES (?,? ,30,'fixture','claude-cross','claude-opus-4-7','cacheRead')`, old, old)
	execArchiveFixture(t, db, `INSERT INTO metric_token_usage (ts,start_ts,value,user_id,session_id,model,type) VALUES (?,? ,20,'fixture','claude-cross','claude-opus-4-7','cacheCreation')`, old, old)
	execArchiveFixture(t, db, `INSERT INTO metric_cost_usage (ts,start_ts,value,user_id,session_id,model) VALUES (?,? ,1.25,'fixture','claude-cross','claude-opus-4-7')`, old, old)
	execArchiveFixture(t, db, `INSERT INTO event_api_request (ts,user_id,session_id,model,duration_ms,output_tokens) VALUES (?,'fixture','claude-cross','claude-opus-4-7',500,50)`, old)
	execArchiveFixture(t, db, `INSERT INTO event_tool_result (ts,user_id,session_id,tool_name) VALUES (?,'fixture','claude-cross','Read')`, old)
	execArchiveFixture(t, db, `INSERT INTO event_skill_activated (ts,user_id,session_id,skill_name) VALUES (?,'fixture','claude-cross','golang-patterns')`, old)
	execArchiveFixture(t, db, `INSERT INTO event_user_prompt (ts,user_id,session_id,prompt_length) VALUES (?,'fixture','claude-cross',10)`, old)
	execArchiveFixture(t, db, `INSERT INTO metric_token_usage (ts,start_ts,value,user_id,session_id,model,type) VALUES (?,? ,7,'fixture','claude-cross','claude-opus-4-7','input')`, late, late)
	execArchiveFixture(t, db, `INSERT INTO event_api_request (ts,user_id,session_id,model,duration_ms,output_tokens) VALUES (?,'fixture','claude-cross','claude-opus-4-7',200,7)`, recent)

	execArchiveFixture(t, db, `INSERT INTO codex_event_token_usage (ts,conversation_id,model,input_token_count,output_token_count,cached_token_count,reasoning_token_count,cost_usd) VALUES (?,'codex-cross','gpt-5.6-sol',200,80,40,20,2.5)`, old)
	execArchiveFixture(t, db, `INSERT INTO codex_event_tool_result (ts,conversation_id,model,tool_name) VALUES (?,'codex-cross','gpt-5.6-sol','exec_command')`, old)
	execArchiveFixture(t, db, `INSERT INTO codex_event_user_prompt (ts,conversation_id,model,prompt_length) VALUES (?,'codex-cross','gpt-5.6-sol',8)`, old)
	execArchiveFixture(t, db, `INSERT INTO codex_metric_skill_injected (ts,start_ts,value,conversation_id,model,skill,status) VALUES (?,? ,3,'codex-cross','gpt-5.6-sol','golang-patterns','ok')`, old, old)
	execArchiveFixture(t, db, `INSERT INTO codex_metric_response_tbt (ts,start_ts,sample_count,sum_ms,conversation_id,model) VALUES (?,? ,2,40,'codex-cross','gpt-5.6-sol')`, old, old)
	execArchiveFixture(t, db, `INSERT INTO codex_event_token_usage (ts,conversation_id,model,input_token_count,output_token_count,cached_token_count,reasoning_token_count,cost_usd) VALUES (?,'codex-cross','gpt-5.6-sol',11,5,2,1,0.25)`, late)
	execArchiveFixture(t, db, `INSERT INTO codex_metric_response_tbt (ts,start_ts,sample_count,sum_ms,conversation_id,model) VALUES (?,? ,1,10,'codex-cross','gpt-5.6-sol')`, recent, recent)
	for i := 0; i < 10; i++ {
		execArchiveFixture(t, db, `INSERT INTO event_tool_result (ts,user_id,session_id,tool_name) VALUES (?,'fixture','topn-cold','cross-tool')`, old)
	}
	for i := 0; i < 9; i++ {
		execArchiveFixture(t, db, `INSERT INTO event_tool_result (ts,user_id,session_id,tool_name) VALUES (?,'fixture','topn-cold-other','cold-other')`, old)
	}
	execArchiveFixture(t, db, `INSERT INTO event_tool_result (ts,user_id,session_id,tool_name) VALUES (?,'fixture','topn-raw','raw-cross')`, old)
	execArchiveFixture(t, db, `INSERT INTO event_tool_result (ts,user_id,session_id,tool_name) VALUES (?,'fixture','topn-cold','cross-tool')`, late)
	for i := 0; i < 10; i++ {
		execArchiveFixture(t, db, `INSERT INTO event_tool_result (ts,user_id,session_id,tool_name) VALUES (?,'fixture','topn-raw','raw-cross')`, late)
	}
}

func execArchiveFixture(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("seed archive fixture: %v", err)
	}
}

func assertArchiveFixtureSummaries(t *testing.T, db *sql.DB, cutoff time.Time) {
	t.Helper()
	var oldRaw int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM metric_token_usage WHERE ts < ?`, cutoff).Scan(&oldRaw); err != nil {
		t.Fatalf("count expired raw Claude token rows: %v", err)
	}
	if oldRaw != 0 {
		t.Fatalf("expired Claude token rows retained: %d", oldRaw)
	}
	var lateRaw int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM metric_token_usage WHERE session_id = 'claude-cross' AND ts >= ?`, cutoff).Scan(&lateRaw); err != nil {
		t.Fatalf("count retained late rows: %v", err)
	}
	if lateRaw != 1 {
		t.Fatalf("retained late Claude rows = %d, want 1", lateRaw)
	}
	var tokens, requests, tools, skills int64
	var cost float64
	if err := db.QueryRow(`SELECT tokens_total, request_count, tool_calls, skill_activations, cost_usd FROM archive.sessions WHERE client='claude' AND session_id='claude-cross'`).Scan(&tokens, &requests, &tools, &skills, &cost); err != nil {
		t.Fatalf("query archived Claude session: %v", err)
	}
	if tokens != 200 || requests != 1 || tools != 1 || skills != 1 || math.Abs(cost-1.25) > 1e-12 {
		t.Fatalf("archived Claude session = tokens=%d requests=%d tools=%d skills=%d cost=%g", tokens, requests, tools, skills, cost)
	}
	if err := db.QueryRow(`SELECT tokens_total, request_count, tool_calls, skill_activations, cost_usd FROM archive.sessions WHERE client='codex' AND session_id='codex-cross'`).Scan(&tokens, &requests, &tools, &skills, &cost); err != nil {
		t.Fatalf("query archived Codex session: %v", err)
	}
	if tokens != 280 || requests != 1 || tools != 1 || skills != 0 || math.Abs(cost-2.5) > 1e-12 {
		t.Fatalf("archived Codex session = tokens=%d requests=%d tools=%d skills=%d cost=%g", tokens, requests, tools, skills, cost)
	}
}

func assertArchiveVisibleSemantics(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	claude, found, err := BuildSessionDetail(ctx, db, "claude-cross", ClientClaude, 10, 10, true)
	if err != nil || !found {
		t.Fatalf("archived Claude session detail: found=%v err=%v", found, err)
	}
	if claude.Tokens != 207 || claude.Requests != 2 || claude.ToolCalls != 1 || claude.SkillActivations != 1 || claude.Cost == nil || math.Abs(*claude.Cost-1.25) > 1e-12 {
		t.Fatalf("visible Claude session detail = %+v", claude)
	}
	codex, found, err := BuildSessionDetail(ctx, db, "codex-cross", ClientCodex, 10, 10, true)
	if err != nil || !found {
		t.Fatalf("archived Codex session detail: found=%v err=%v", found, err)
	}
	if codex.Tokens != 296 || codex.Requests != 2 || codex.ToolCalls != 1 || codex.Cost == nil || math.Abs(*codex.Cost-2.75) > 1e-12 {
		t.Fatalf("visible Codex session detail = %+v", codex)
	}
}

func assertArchiveTopNCrossBoundary(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rankings, err := BuildRankings(ctx, db, RankingsOpts{SinceTag: "all", Client: ClientClaude, ToolsTopN: 2, SkillsTopN: 2})
	if err != nil {
		t.Fatalf("cross-boundary Top-N rankings: %v", err)
	}
	if len(rankings.Tools) != 2 || rankings.Tools[0] != (ToolRank{Name: "cross-tool", Count: 11}) || rankings.Tools[1] != (ToolRank{Name: "raw-cross", Count: 11}) {
		t.Fatalf("cross-boundary Top-N tools = %+v, want cross-tool/raw-cross both 11", rankings.Tools)
	}
}

type archiveSessionTarget struct {
	Client Client
	ID     string
}

func captureArchiveDashboard(t *testing.T, ctx context.Context, db *sql.DB, now time.Time, sessionTargets []archiveSessionTarget) map[string]json.RawMessage {
	t.Helper()
	w, err := NowWindow(now, archiveTestTimezone)
	if err != nil {
		t.Fatalf("NowWindow: %v", err)
	}
	classifier, err := NewClassifier(nil)
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	weights := HeatmapWeights{Tokens: 1, Cost: 1, Requests: 1}
	prices := archiveNoPrices{}
	result := make(map[string]json.RawMessage)
	put := func(name string, call func() (any, error)) {
		t.Helper()
		value, err := call()
		if err != nil {
			t.Fatalf("capture %s: %v", name, err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		result[name] = encoded
	}

	for _, client := range []Client{ClientAll, ClientClaude, ClientCodex} {
		for _, rng := range []string{"day", "week", "month"} {
			put(fmt.Sprintf("snapshot/%s/%s", client, rng), func() (any, error) { return BuildSnapshot(ctx, db, classifier, w, rng, client, true, prices) })
			put(fmt.Sprintf("trends/%s/%s", client, rng), func() (any, error) { return BuildTrends(ctx, db, classifier, w, rng, client) })
			put(fmt.Sprintf("rates/%s/%s", client, rng), func() (any, error) { return BuildRates(ctx, db, classifier, w, rng, client) })
			put(fmt.Sprintf("period_models/%s/%s", client, rng), func() (any, error) {
				return BuildPeriodModels(ctx, db, classifier, PeriodModelsOptions{Window: w, Range: rng, Client: client, PricingEnabled: true})
			})
		}
		put(fmt.Sprintf("heatmap/%s", client), func() (any, error) { return BuildHeatmap(ctx, db, w, weights, client, true) })
		put(fmt.Sprintf("pricing_models/%s", client), func() (any, error) { return BuildPricingModels(ctx, db, client, prices, true) })
		for _, since := range []string{"7d", "30d", "all"} {
			start, tag, err := SinceStart(w, since)
			if err != nil {
				t.Fatalf("SinceStart(%s): %v", since, err)
			}
			put(fmt.Sprintf("rankings/%s/%s", client, since), func() (any, error) {
				return BuildRankings(ctx, db, RankingsOpts{SinceStart: start, SinceTag: tag, Client: client, ToolsTopN: 10, SkillsTopN: 10})
			})
		}
		put(fmt.Sprintf("sessions/%s", client), func() (any, error) { return BuildSessionList(ctx, db, client, 100, true) })
		for _, target := range sessionTargets {
			put(fmt.Sprintf("session/%s/%s/%s", client, target.Client, target.ID), func() (any, error) {
				detail, found, err := BuildSessionDetail(ctx, db, target.ID, client, 10, 10, true)
				return struct {
					Found  bool                  `json:"found"`
					Detail SessionDetailResponse `json:"detail"`
				}{Found: found, Detail: detail}, err
			})
		}
	}
	return result
}

func archiveSessionTargets(t *testing.T, ctx context.Context, db *sql.DB) []archiveSessionTarget {
	t.Helper()
	targets, err := collectArchiveSessionTargets(ctx, db, 1000)
	if err != nil {
		t.Fatalf("list backup sessions: %v", err)
	}
	return targets
}

func collectArchiveSessionTargets(ctx context.Context, db *sql.DB, limit int) ([]archiveSessionTarget, error) {
	targets := make([]archiveSessionTarget, 0, 10)
	for _, client := range []Client{ClientClaude, ClientCodex} {
		list, err := BuildSessionList(ctx, db, client, limit, true)
		if err != nil {
			return nil, fmt.Errorf("list sessions (%s): %w", client, err)
		}
		for _, session := range list.Sessions {
			targets = append(targets, archiveSessionTarget{Client: client, ID: session.SessionID})
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Client != targets[j].Client {
			return targets[i].Client < targets[j].Client
		}
		return targets[i].ID < targets[j].ID
	})
	return targets, nil
}

func archiveRawRowCount(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	rows, err := db.Query(`
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'main' AND table_name <> 'schema_migrations'
		ORDER BY table_name
	`)
	if err != nil {
		t.Fatalf("list archive source tables: %v", err)
	}
	tables := make([]string, 0, 27)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			t.Fatalf("scan archive source table: %v", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("iterate archive source tables: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close archive source table list: %v", err)
	}

	var count int64
	for _, table := range tables {
		// table names are selected from DuckDB's own catalog, never from input.
		var tableCount int64
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&tableCount); err != nil {
			t.Fatalf("count archive source table %s: %v", table, err)
		}
		count += tableCount
	}
	return count
}

func archiveSummaryRowCount(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	const query = `
		SELECT
		  (SELECT COUNT(*) FROM archive.usage_hourly) +
		  (SELECT COUNT(*) FROM archive.tool_hourly) +
		  (SELECT COUNT(*) FROM archive.skill_hourly) +
		  (SELECT COUNT(*) FROM archive.sessions) +
		  (SELECT COUNT(*) FROM archive.session_tools) +
		  (SELECT COUNT(*) FROM archive.session_skills)
	`
	var count int64
	if err := db.QueryRow(query).Scan(&count); err != nil {
		t.Fatalf("count archive summaries: %v", err)
	}
	return count
}

func assertArchiveCaptureEqual(t *testing.T, before, after map[string]json.RawMessage) {
	t.Helper()
	if !reflect.DeepEqual(sortedArchiveKeys(before), sortedArchiveKeys(after)) {
		t.Fatalf("capture keys differ: before=%v after=%v", sortedArchiveKeys(before), sortedArchiveKeys(after))
	}
	for _, name := range sortedArchiveKeys(before) {
		left, err := decodeArchiveJSON(before[name])
		if err != nil {
			t.Fatalf("decode before %s: %v", name, err)
		}
		right, err := decodeArchiveJSON(after[name])
		if err != nil {
			t.Fatalf("decode after %s: %v", name, err)
		}
		if err := compareArchiveJSON(left, right, name); err != nil {
			t.Error(err)
		}
	}
}

func decodeArchiveJSON(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func sortedArchiveKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func compareArchiveJSON(left, right any, path string) error {
	switch l := left.(type) {
	case map[string]any:
		r, ok := right.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: type mismatch %T != %T", path, left, right)
		}
		if !reflect.DeepEqual(sortedArchiveMapKeys(l), sortedArchiveMapKeys(r)) {
			return fmt.Errorf("%s: object keys differ", path)
		}
		for _, key := range sortedArchiveMapKeys(l) {
			if key == "updated_at" || key == "as_of" {
				continue
			}
			if err := compareArchiveJSON(l[key], r[key], path+"."+key); err != nil {
				return err
			}
		}
		return nil
	case []any:
		r, ok := right.([]any)
		if !ok || len(l) != len(r) {
			return fmt.Errorf("%s: array mismatch", path)
		}
		for i := range l {
			if err := compareArchiveJSON(l[i], r[i], fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	case json.Number:
		r, ok := right.(json.Number)
		if !ok {
			return fmt.Errorf("%s: number type mismatch %T != %T", path, left, right)
		}
		if !archiveFloatPath(path) {
			if l.String() != r.String() {
				return fmt.Errorf("%s: exact number mismatch %s != %s", path, l, r)
			}
			return nil
		}
		lv, leftErr := l.Float64()
		rv, rightErr := r.Float64()
		if leftErr != nil || rightErr != nil || math.Abs(lv-rv) > 1e-9*math.Max(1, math.Max(math.Abs(lv), math.Abs(rv))) {
			return fmt.Errorf("%s: float mismatch %s != %s", path, l, r)
		}
		return nil
	default:
		if !reflect.DeepEqual(left, right) {
			return fmt.Errorf("%s: value mismatch %v != %v", path, left, right)
		}
		return nil
	}
}

func archiveFloatPath(path string) bool {
	return strings.HasPrefix(path, "rates/") ||
		strings.Contains(path, ".cost.") ||
		strings.HasSuffix(path, ".cost") ||
		strings.HasSuffix(path, ".cost_usd") ||
		strings.HasSuffix(path, ".share") ||
		strings.HasSuffix(path, ".hit_rate") ||
		strings.HasSuffix(path, ".score") ||
		strings.Contains(path, ".weights.") ||
		strings.HasSuffix(path, ".input_cost") ||
		strings.HasSuffix(path, ".output_cost") ||
		strings.HasSuffix(path, ".cache_read_cost")
}

func sortedArchiveMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type archiveNoPrices struct{}

func (archiveNoPrices) PriceFor(model string) (pricing.ModelPrice, bool) {
	if model != "claude-opus-4-7" && model != "gpt-5.6-sol" {
		return pricing.ModelPrice{}, false
	}
	input, output, cached, reasoning := 1e-6, 2e-6, 5e-7, 3e-6
	return pricing.ModelPrice{
		InputCostPerToken:           &input,
		OutputCostPerToken:          &output,
		CacheReadInputTokenCost:     &cached,
		OutputCostPerReasoningToken: &reasoning,
	}, true
}

func (archiveNoPrices) Stats() pricing.Stats { return pricing.Stats{} }
