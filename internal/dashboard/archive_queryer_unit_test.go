/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.1.0
 */

package dashboard

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/store"
)

func TestArchiveScopedQueryerHandlesArchiveCatalogName(t *testing.T) {
	db, err := store.Open(config.StorageConfig{DuckDBPath: filepath.Join(t.TempDir(), "archive.duckdb")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrations, err := store.LoadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if err := store.RunMigrations(db.SQL, migrations); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO "archive".archive.usage_hourly (bucket_start, client, model) VALUES ('2026-01-01 00:00:00', 'claude', 'model')`); err != nil {
		t.Fatalf("seed archive table: %v", err)
	}
	got, err := withDashboardSnapshot(context.Background(), db.SQL, func(q sqlQueryer) (int64, error) {
		var count int64
		err := q.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM archive.usage_hourly`).Scan(&count)
		return count, err
	})
	if err != nil {
		t.Fatalf("scoped archive query: %v", err)
	}
	if got != 1 {
		t.Fatalf("archive row count = %d, want 1", got)
	}
}

func TestQuerySessionListMergesCostOnlyCounterparts(t *testing.T) {
	db, w, _ := testDB(t)
	old := w.TodayStartUTC.Add(-48 * time.Hour)
	recent := w.TodayStartUTC.Add(time.Hour)
	_, err := db.Exec(`INSERT INTO archive.sessions (client, session_id, first_active, last_active, cost_usd) VALUES ('claude','archive-active',?,?,2), ('claude','raw-active',NULL,NULL,4)`, old, old)
	if err != nil {
		t.Fatalf("seed archive sessions: %v", err)
	}
	_, err = db.Exec(`INSERT INTO metric_cost_usage (ts,start_ts,value,user_id,session_id) VALUES (?,? ,3,'test','archive-active'), (?,? ,1,'test','raw-active')`, recent, recent, recent, recent)
	if err != nil {
		t.Fatalf("seed raw cost: %v", err)
	}
	_, err = db.Exec(`INSERT INTO event_api_request (ts,user_id,session_id) VALUES (?,'test','raw-active')`, recent)
	if err != nil {
		t.Fatalf("seed raw activity: %v", err)
	}
	rows, err := QuerySessionList(context.Background(), db, ClientClaude, 10)
	if err != nil {
		t.Fatalf("QuerySessionList: %v", err)
	}
	costs := make(map[string]float64, len(rows))
	for _, row := range rows {
		costs[row.SessionID] = row.Cost
	}
	if costs["archive-active"] != 5 {
		t.Fatalf("archive-active cost = %v, want 5", costs["archive-active"])
	}
	if costs["raw-active"] != 5 {
		t.Fatalf("raw-active cost = %v, want 5", costs["raw-active"])
	}
}

func TestArchiveTokenSourcePresenceFiltersModelQueries(t *testing.T) {
	db, w, _ := testDB(t)
	bucket := w.TodayStartUTC.Add(-48 * time.Hour)
	_, err := db.Exec(`INSERT INTO archive.usage_hourly (bucket_start,client,model,token_rows,speed_units,speed_duration_ms,cost_usd) VALUES (?,'codex','tbt-only',0,2,10,1), (?,'codex','zero-token-response',1,0,0,0)`, bucket, bucket)
	if err != nil {
		t.Fatalf("seed archive usage: %v", err)
	}
	trends, err := QueryTrends(context.Background(), db, ClientCodex, w, "day", bucket)
	if err != nil {
		t.Fatalf("QueryTrends: %v", err)
	}
	if len(trends) != 1 || trends[0].Model != "zero-token-response" {
		t.Fatalf("trends = %+v, want only zero-token-response", trends)
	}
	models, err := QueryModelTokens(context.Background(), db, ClientCodex)
	if err != nil {
		t.Fatalf("QueryModelTokens: %v", err)
	}
	if len(models) != 1 || models[0].Model != "zero-token-response" {
		t.Fatalf("model tokens = %+v, want only zero-token-response", models)
	}
}

func TestBuildRankingsFailsWithoutPartialResponseAndReusesConnection(t *testing.T) {
	db, _, _ := testDB(t)
	if _, err := db.Exec(`DROP TABLE archive.tool_hourly`); err != nil {
		t.Fatalf("drop archive table: %v", err)
	}
	_, err := BuildRankings(context.Background(), db, RankingsOpts{Client: ClientAll, ToolsTopN: 10, SkillsTopN: 10})
	if err == nil {
		t.Fatal("BuildRankings succeeded with missing cold table")
	}
	var value int
	if err := db.QueryRow(`SELECT 1`).Scan(&value); err != nil {
		t.Fatalf("connection unusable after failed snapshot: %v", err)
	}
	if value != 1 {
		t.Fatalf("query after failure = %d, want 1", value)
	}
}

func TestWithDashboardSnapshotHonorsCanceledContext(t *testing.T) {
	db, _, _ := testDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := withDashboardSnapshot(ctx, db, func(q sqlQueryer) (int64, error) { return 0, nil })
	if err == nil {
		t.Fatal("withDashboardSnapshot succeeded with canceled context")
	}
}
