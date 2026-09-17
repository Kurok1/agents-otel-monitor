/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.1.0
 */

package store

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

func TestArchiverArchivesExpiredDataAndRetainsBoundary(t *testing.T) {
	db, archiver := openArchiveTestStore(t)
	cutoff := archiveCutoff(time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC), time.FixedZone("CST", 8*60*60))
	old := cutoff.Add(-time.Hour)
	insertArchiveFixtures(t, db.SQL, old)
	insertClaudeToken(t, db.SQL, cutoff, "boundary", "input", 99, "m", "boundary-session")

	result, err := archiver.RunOnce(context.Background(), time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("run archive: %v", err)
	}
	if !result.Cutoff.Equal(cutoff) {
		t.Fatalf("cutoff = %s, want %s", result.Cutoff, cutoff)
	}
	if result.DeletedRows != 13 || result.Days != 1 {
		t.Fatalf("result = %+v, want 13 deleted rows in one day", result)
	}

	var claudeTokens, claudeInput, claudeOutput, cacheRead, cacheCreation, tokenRows, requests int64
	var claudeCost, speedUnits, speedDuration float64
	err = db.SQL.QueryRow(`SELECT tokens_total, input_tokens, output_tokens, cache_read_tokens,
		cache_creation_tokens, token_rows, request_count, cost_usd, speed_units, speed_duration_ms
		FROM archive.usage_hourly WHERE client = 'claude' AND model = 'm'`).
		Scan(&claudeTokens, &claudeInput, &claudeOutput, &cacheRead, &cacheCreation, &tokenRows, &requests, &claudeCost, &speedUnits, &speedDuration)
	if err != nil {
		t.Fatalf("query Claude usage archive: %v", err)
	}
	if claudeTokens != 180 || claudeInput != 100 || claudeOutput != 50 || cacheRead != 20 || cacheCreation != 10 || tokenRows != 4 || requests != 1 || claudeCost != 1.25 || speedUnits != 50 || speedDuration != 100 {
		t.Fatalf("unexpected Claude archive: tokens=%d input=%d output=%d cacheRead=%d cacheCreation=%d rows=%d requests=%d cost=%g speed=%g/%g", claudeTokens, claudeInput, claudeOutput, cacheRead, cacheCreation, tokenRows, requests, claudeCost, speedUnits, speedDuration)
	}

	var codexTokens, codexInput, codexOutput, codexCached, codexReasoning, throughput, codexRows, codexRequests int64
	var codexCost, codexSpeedUnits, codexSpeedDuration float64
	err = db.SQL.QueryRow(`SELECT tokens_total, input_tokens, output_tokens, cache_read_tokens, reasoning_tokens,
		throughput_input_tokens, token_rows, request_count, cost_usd, speed_units, speed_duration_ms
		FROM archive.usage_hourly WHERE client = 'codex' AND model = 'gpt-test'`).
		Scan(&codexTokens, &codexInput, &codexOutput, &codexCached, &codexReasoning, &throughput, &codexRows, &codexRequests, &codexCost, &codexSpeedUnits, &codexSpeedDuration)
	if err != nil {
		t.Fatalf("query Codex usage archive: %v", err)
	}
	if codexTokens != 100 || codexInput != 80 || codexOutput != 20 || codexCached != 30 || codexReasoning != 5 || throughput != 50 || codexRows != 1 || codexRequests != 1 || codexCost != 0.5 || codexSpeedUnits != 2 || codexSpeedDuration != 40 {
		t.Fatalf("unexpected Codex archive: tokens=%d input=%d output=%d cached=%d reasoning=%d throughput=%d rows=%d requests=%d cost=%g speed=%g/%g", codexTokens, codexInput, codexOutput, codexCached, codexReasoning, throughput, codexRows, codexRequests, codexCost, codexSpeedUnits, codexSpeedDuration)
	}

	assertArchiveCount(t, db.SQL, "archive.tool_hourly", 2)
	assertArchiveCount(t, db.SQL, "archive.skill_hourly", 2)
	assertArchiveCount(t, db.SQL, "archive.sessions", 2)
	assertArchiveCount(t, db.SQL, "archive.session_tools", 2)
	assertArchiveCount(t, db.SQL, "archive.session_skills", 1)
	assertArchiveCount(t, db.SQL, "metric_token_usage", 1)
}

func TestArchiverLateDataIsAdditiveAndEmptyRepeatIsIdempotent(t *testing.T) {
	db, archiver := openArchiveTestStore(t)
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	old := archiveCutoff(now, time.FixedZone("CST", 8*60*60)).Add(-time.Hour)
	insertClaudeToken(t, db.SQL, old, "first", "input", 10, "m", "s")
	if _, err := archiver.RunOnce(context.Background(), now); err != nil {
		t.Fatalf("first archive: %v", err)
	}
	if _, err := archiver.RunOnce(context.Background(), now); err != nil {
		t.Fatalf("empty repeat: %v", err)
	}
	insertClaudeToken(t, db.SQL, old.Add(10*time.Minute), "late", "output", 7, "m", "s")
	if _, err := archiver.RunOnce(context.Background(), now); err != nil {
		t.Fatalf("late archive: %v", err)
	}
	var total, input, output int64
	if err := db.SQL.QueryRow(`SELECT tokens_total, input_tokens, output_tokens FROM archive.usage_hourly WHERE client = 'claude' AND model = 'm'`).Scan(&total, &input, &output); err != nil {
		t.Fatalf("query archive: %v", err)
	}
	if total != 17 || input != 10 || output != 7 {
		t.Fatalf("late total = %d/%d/%d, want 17/10/7", total, input, output)
	}
}

func TestArchiverSkipsEmptyCalendarGaps(t *testing.T) {
	db, archiver := openArchiveTestStore(t)
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	old := archiveCutoff(now, time.FixedZone("CST", 8*60*60)).Add(-365 * 24 * time.Hour)
	insertClaudeToken(t, db.SQL, old, "historic", "input", 10, "m", "s")
	result, err := archiver.RunOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("archive historic row: %v", err)
	}
	if result.Days != 1 || result.DeletedRows != 1 {
		t.Fatalf("historic archive result = %+v, want one populated day", result)
	}
}

func TestArchiverKeepsUnnamedSkillOnlySessionVisible(t *testing.T) {
	db, archiver := openArchiveTestStore(t)
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	old := archiveCutoff(now, time.FixedZone("CST", 8*60*60)).Add(-time.Hour)
	mustArchiveExec(t, db.SQL, `INSERT INTO event_skill_activated (ts, session_id, user_id) VALUES (?, 'skill-only', 'u')`, old)
	if _, err := archiver.RunOnce(context.Background(), now); err != nil {
		t.Fatalf("archive unnamed skill: %v", err)
	}
	var first, last time.Time
	var activations int64
	if err := db.SQL.QueryRow(`SELECT first_active, last_active, skill_activations FROM archive.sessions WHERE client = 'claude' AND session_id = 'skill-only'`).Scan(&first, &last, &activations); err != nil {
		t.Fatalf("query unnamed skill session: %v", err)
	}
	if !first.Equal(old) || !last.Equal(old) || activations != 1 {
		t.Fatalf("unnamed skill session = first=%s last=%s activations=%d", first, last, activations)
	}
	assertArchiveCount(t, db.SQL, "archive.session_skills", 0)
}

func TestArchiverCancelledBeforeSweepPreservesRawRows(t *testing.T) {
	db, archiver := openArchiveTestStore(t)
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	old := archiveCutoff(now, time.FixedZone("CST", 8*60*60)).Add(-time.Hour)
	insertClaudeToken(t, db.SQL, old, "cancel", "input", 10, "m", "s")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := archiver.RunOnce(ctx, now); err == nil {
		t.Fatal("expected cancellation error")
	}
	assertArchiveCount(t, db.SQL, "metric_token_usage", 1)
}

func TestArchiverRollsBackWhenFailureFollowsAggregation(t *testing.T) {
	db, archiver := openArchiveTestStore(t)
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	old := archiveCutoff(now, time.FixedZone("CST", 8*60*60)).Add(-time.Hour)
	insertClaudeToken(t, db.SQL, old, "rollback", "input", 10, "m", "s")
	archiver.afterAggregate = func() error { return errors.New("forced failure") }
	if _, err := archiver.RunOnce(context.Background(), now); err == nil {
		t.Fatal("expected archive failure")
	}
	assertArchiveCount(t, db.SQL, "metric_token_usage", 1)
	assertArchiveCount(t, db.SQL, "archive.usage_hourly", 0)

	archiver.afterAggregate = nil
	result, err := archiver.RunOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("retry archive: %v", err)
	}
	if result.DeletedRows != 1 || result.Days != 1 {
		t.Fatalf("retry result = %+v, want one deleted row/day", result)
	}
	var tokens int64
	if err := db.SQL.QueryRow(`SELECT tokens_total FROM archive.usage_hourly WHERE client = 'claude' AND model = 'm'`).Scan(&tokens); err != nil {
		t.Fatalf("query retried summary: %v", err)
	}
	if tokens != 10 {
		t.Fatalf("retried summary tokens = %d, want 10", tokens)
	}
	second, err := archiver.RunOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("repeat archive: %v", err)
	}
	if second.DeletedRows != 0 || second.Days != 0 {
		t.Fatalf("repeat result = %+v, want zero", second)
	}
	assertArchiveCount(t, db.SQL, "metric_token_usage", 0)
}

func TestArchiverAndWriterShareWriteMutex(t *testing.T) {
	db, archiver := openArchiveTestStore(t)
	cfg := config.IngestConfig{
		BatchSize:       1,
		FlushInterval:   config.Duration(time.Hour),
		BufferHardLimit: 1,
	}
	writer, err := NewBufferedWriter(db, cfg, slog.Default())
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	t.Cleanup(func() { _ = writer.Stop() })
	if writer.writeMu != &db.writeMu {
		t.Fatal("writer does not use the database write mutex")
	}

	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	old := archiveCutoff(now, time.FixedZone("CST", 8*60*60)).Add(-time.Hour)
	insertClaudeToken(t, db.SQL, old, "locked", "input", 10, "m", "s")
	db.writeMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := archiver.RunOnce(context.Background(), now)
		done <- err
	}()
	select {
	case err := <-done:
		db.writeMu.Unlock()
		t.Fatalf("archive did not wait for shared lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	db.writeMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("archive after unlock: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("archive did not resume after shared lock release")
	}
}

func TestArchiveScheduleAndStartStop(t *testing.T) {
	loc := time.FixedZone("CST", 8*60*60)
	if got := nextArchiveRun(time.Date(2026, time.September, 17, 2, 59, 0, 0, loc), loc); got.Hour() != 3 || got.Day() != 17 {
		t.Fatalf("next run before 03:00 = %s", got)
	}
	if got := nextArchiveRun(time.Date(2026, time.September, 17, 3, 0, 0, 0, loc), loc); got.Hour() != 3 || got.Day() != 18 {
		t.Fatalf("next run at 03:00 = %s", got)
	}

	_, archiver := openArchiveTestStore(t)
	archiver.Start(context.Background())
	archiver.Stop()
	archiver.Stop()
}

func TestArchiveCutoffKeepsThirtyFullDaysAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load test location: %v", err)
	}
	now := time.Date(2026, time.March, 9, 0, 30, 0, 0, loc)
	cutoff := archiveCutoff(now, loc)
	if cutoff.After(now.UTC().Add(-archiveRetentionDays * 24 * time.Hour)) {
		t.Fatalf("cutoff %s archives rows younger than 30 full days from %s", cutoff, now)
	}
}

func TestArchiverUsesLocalDayBoundariesAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load test location: %v", err)
	}
	tests := []struct {
		name string
		now  time.Time
		old  time.Time
	}{
		{
			name: "spring forward",
			now:  time.Date(2026, time.April, 8, 12, 0, 0, 0, loc),
			old:  time.Date(2026, time.March, 8, 23, 0, 0, 0, loc),
		},
		{
			name: "fall back",
			now:  time.Date(2026, time.December, 2, 12, 0, 0, 0, loc),
			old:  time.Date(2026, time.November, 1, 23, 30, 0, 0, loc),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, archiver := openArchiveTestStoreIn(t, "America/New_York")
			cutoff := archiveCutoff(test.now, loc)
			insertClaudeToken(t, db.SQL, test.old, "old", "input", 10, "m", "s")
			insertClaudeToken(t, db.SQL, cutoff, "boundary", "input", 99, "m", "boundary")
			result, err := archiver.RunOnce(context.Background(), test.now)
			if err != nil {
				t.Fatalf("archive %s: %v", test.name, err)
			}
			if result.DeletedRows != 1 || result.Days != 1 {
				t.Fatalf("result = %+v, want one old row/day", result)
			}
			assertArchiveCount(t, db.SQL, "metric_token_usage", 1)
			var tokens int64
			if err := db.SQL.QueryRow(`SELECT tokens_total FROM archive.usage_hourly WHERE client = 'claude' AND model = 'm'`).Scan(&tokens); err != nil {
				t.Fatalf("query summary: %v", err)
			}
			if tokens != 10 {
				t.Fatalf("archived tokens = %d, want 10", tokens)
			}
		})
	}
}

func openArchiveTestStore(t *testing.T) (*DB, *Archiver) {
	return openArchiveTestStoreIn(t, "Asia/Shanghai")
}

func openArchiveTestStoreIn(t *testing.T, timezone string) (*DB, *Archiver) {
	t.Helper()
	db, err := Open(config.StorageConfig{DuckDBPath: filepath.Join(t.TempDir(), "store.duckdb")})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if err := RunMigrations(db.SQL, migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	archiver, err := NewArchiver(db, timezone, nil)
	if err != nil {
		t.Fatalf("new archiver: %v", err)
	}
	return db, archiver
}

func insertArchiveFixtures(t *testing.T, db *sql.DB, ts time.Time) {
	t.Helper()
	insertClaudeToken(t, db, ts, "token-input", "input", 100, "m", "claude-session")
	insertClaudeToken(t, db, ts, "token-output", "output", 50, "m", "claude-session")
	insertClaudeToken(t, db, ts, "token-cache-read", "cacheRead", 20, "m", "claude-session")
	insertClaudeToken(t, db, ts, "token-cache-create", "cacheCreation", 10, "m", "claude-session")
	mustArchiveExec(t, db, `INSERT INTO metric_cost_usage (ts, start_ts, value, session_id, user_id, model) VALUES (?, ?, 1.25, 'claude-session', 'u', 'm')`, ts, ts)
	mustArchiveExec(t, db, `INSERT INTO event_api_request (ts, session_id, user_id, model, duration_ms, output_tokens) VALUES (?, 'claude-session', 'u', 'm', 100, 50)`, ts)
	mustArchiveExec(t, db, `INSERT INTO event_tool_result (ts, session_id, user_id, tool_name) VALUES (?, 'claude-session', 'u', 'Bash')`, ts)
	mustArchiveExec(t, db, `INSERT INTO event_skill_activated (ts, session_id, user_id, skill_name) VALUES (?, 'claude-session', 'u', 'review')`, ts)
	mustArchiveExec(t, db, `INSERT INTO event_user_prompt (ts, session_id, user_id) VALUES (?, 'claude-session', 'u')`, ts)
	mustArchiveExec(t, db, `INSERT INTO codex_event_token_usage (ts, conversation_id, model, input_token_count, output_token_count, cached_token_count, reasoning_token_count, cost_usd) VALUES (?, 'codex-session', 'gpt-test', 80, 20, 30, 5, 0.5)`, ts)
	mustArchiveExec(t, db, `INSERT INTO codex_metric_response_tbt (ts, start_ts, sample_count, sum_ms, model) VALUES (?, ?, 2, 40, 'gpt-test')`, ts, ts)
	mustArchiveExec(t, db, `INSERT INTO codex_event_tool_result (ts, conversation_id, tool_name) VALUES (?, 'codex-session', 'shell')`, ts)
	mustArchiveExec(t, db, `INSERT INTO codex_metric_skill_injected (ts, start_ts, value, skill, status) VALUES (?, ?, 3, 'codex-skill', 'SUCCESS')`, ts, ts)
}

func insertClaudeToken(t *testing.T, db *sql.DB, ts time.Time, _ string, tokenType string, value int64, model, sessionID string) {
	t.Helper()
	mustArchiveExec(t, db, `INSERT INTO metric_token_usage (ts, start_ts, value, session_id, user_id, type, model) VALUES (?, ?, ?, ?, 'u', ?, ?)`, ts, ts, value, sessionID, tokenType, model)
}

func mustArchiveExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec fixture: %v", err)
	}
}

func assertArchiveCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Errorf("count %s = %d, want %d", table, got, want)
	}
}
