/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

package store

import (
	"database/sql"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/otlp"
	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"
)

func TestCodex162RealFixtureAppenderRoundTrip(t *testing.T) {
	db, err := Open(config.StorageConfig{DuckDBPath: filepath.Join(t.TempDir(), "fixture.duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(db.SQL, migrations); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w, err := NewBufferedWriter(db, config.IngestConfig{BatchSize: 100, BufferHardLimit: 1000, FlushInterval: config.Duration(time.Second)}, log)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := otlp.NewDispatcher(log, w, nil)
	logs := &logspb.ExportLogsServiceRequest{}
	metrics := &metricspb.ExportMetricsServiceRequest{}
	for _, fixture := range []struct {
		kind    string
		message proto.Message
	}{{"logs", logs}, {"metrics", metrics}} {
		encoded, err := os.ReadFile("../otlp/testdata/codex_0_162_1_" + fixture.kind + ".pb.b64")
		if err != nil {
			t.Fatal(err)
		}
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
		if err != nil {
			t.Fatal(err)
		}
		if err := proto.Unmarshal(b, fixture.message); err != nil {
			t.Fatal(err)
		}
	}
	if s := dispatch.DispatchLogs(logs); s.Errors != 0 {
		t.Fatalf("log parse: %+v", s)
	}
	if s := dispatch.DispatchMetrics(metrics); s.Errors != 0 {
		t.Fatalf("metric parse: %+v", s)
	}
	if err := w.Stop(); err != nil {
		t.Fatal(err)
	}
	var count, write, ttft int64
	if err := db.SQL.QueryRow(`SELECT COUNT(*),SUM(cache_write_token_count),COUNT(ttft_ms) FROM codex_valid_token_usage`).Scan(&count, &write, &ttft); err != nil || count != 4 || write != 120 || ttft != 4 {
		t.Fatalf("usage roundtrip: %d %d %d %v", count, write, ttft, err)
	}
	var amount string
	if err := db.SQL.QueryRow(`SELECT estimated_usd FROM codex_event_turn_cost`).Scan(&amount); err != nil || amount != "1.250001" {
		t.Fatalf("native decimal: %q %v", amount, err)
	}
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM codex_event_tool_result WHERE output_truncated AND output_length>0 AND json_extract(attrs,'$.output') IS NULL AND json_extract(attrs,'$.arguments') IS NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("privacy roundtrip: %d %v", count, err)
	}
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM codex_event_skill_invocation WHERE skill_name='fixture' AND invocation_type='explicit'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("skill roundtrip: %d %v", count, err)
	}
	if err := db.SQL.QueryRow(`SELECT CAST(sum_tokens AS BIGINT) FROM codex_metric_turn_token_usage WHERE token_type='total'`).Scan(&count); err != nil || count != 240 {
		t.Fatalf("turn metric roundtrip: %d %v", count, err)
	}
}

func TestMigration008BackfillAndInterruptedUpgrade(t *testing.T) {
	// A database named archive also exercises catalog/schema qualification.
	db, err := Open(config.StorageConfig{DuckDBPath: filepath.Join(t.TempDir(), "archive.duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(db.SQL, migrations[:7]); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO codex_event_token_usage (ts,conversation_id,input_token_count,output_token_count,cost_usd,attrs) VALUES ('2026-10-01','valid',100,20,0.5,'{"ttft_ms":123,"cache_write_token_count":"30"}'), ('2026-10-01','missing',NULL,NULL,0.7,'{"error.message":"failed"}'), ('2026-10-01','malformed',100,20,0.8,'{"ttft_ms":"bad","cache_write_token_count":"bad"}')`,
		`INSERT INTO codex_event_tool_result (ts,attrs) VALUES ('2026-10-01','{"output_truncated":true}')`,
		`INSERT INTO "archive".archive.usage_hourly (bucket_start,client,model,cost_usd) VALUES ('2026-09-01','codex','gpt-test',0.2)`,
		`INSERT INTO "archive".archive.sessions (client,session_id,cost_usd) VALUES ('codex','old',0.2)`,
	}
	for _, q := range statements {
		if _, err := db.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// Replaying the first DDL statements models a failure before the migration
	// marker. Re-running the complete upgrade must preserve the old cost.
	for _, q := range splitStatements(migrations[7].SQL)[:5] {
		if _, err := db.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := RunMigrations(db.SQL, migrations); err != nil {
		t.Fatal(err)
	}
	var ttft, write int64
	var cost float64
	var valid bool
	if err := db.SQL.QueryRow(`SELECT ttft_ms,cache_write_token_count,cost_usd,usage_valid FROM codex_event_token_usage WHERE conversation_id='valid'`).Scan(&ttft, &write, &cost, &valid); err != nil {
		t.Fatal(err)
	}
	if ttft != 123 || write != 30 || cost != 0.5 || !valid {
		t.Fatalf("backfill: %d %d %v %v", ttft, write, cost, valid)
	}
	var n int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM codex_valid_token_usage`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("valid completions = %d, %v", n, err)
	}
	var nullable sql.NullInt64
	if err := db.SQL.QueryRow(`SELECT ttft_ms FROM codex_event_token_usage WHERE conversation_id='malformed'`).Scan(&nullable); err != nil || nullable.Valid {
		t.Fatalf("malformed TTFT = %+v, %v", nullable, err)
	}
	if err := db.SQL.QueryRow(`SELECT output_truncated FROM codex_event_tool_result`).Scan(&valid); err != nil || !valid {
		t.Fatalf("truncation backfill: %v %v", valid, err)
	}
	if _, err := db.SQL.Exec(`DELETE FROM schema_migrations WHERE version=8`); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(db.SQL, migrations); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow(`SELECT SUM(cost_usd) FROM "archive".archive.codex_legacy_cost_hourly`).Scan(&cost); err != nil || cost != 0.2 {
		t.Fatalf("legacy cost duplicated: %v %v", cost, err)
	}
	if err := db.SQL.QueryRow(`SELECT cost_usd FROM codex_event_token_usage WHERE conversation_id='missing'`).Scan(&cost); err != nil || cost != 0.7 {
		t.Fatalf("migration repriced history: %v %v", cost, err)
	}
}
