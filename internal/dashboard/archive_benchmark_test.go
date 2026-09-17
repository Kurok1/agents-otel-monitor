/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.1.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/store"
)

// BenchmarkArchiveDashboardBackup measures representative public APIs before
// and after compaction on a temporary copy of a production-shaped backup.
// Use -benchtime=1x: each scenario gathers 20 individual API samples itself.
func BenchmarkArchiveDashboardBackup(b *testing.B) {
	source := os.Getenv("MONITOR_ARCHIVE_TEST_DB")
	if source == "" {
		b.Skip("set MONITOR_ARCHIVE_TEST_DB to run archive backup benchmarks")
	}
	ctx := context.Background()
	db := openArchiveCopyBenchmark(b, source)
	ids, err := collectArchiveSessionTargets(ctx, db.SQL, 5)
	if err != nil {
		b.Fatalf("list archive benchmark sessions: %v", err)
	}

	crossTarget, err := archiveCrossBoundaryTarget(ctx, db.SQL)
	if err != nil {
		b.Fatalf("find cross-boundary benchmark session: %v", err)
	}
	benchmarkArchiveAPIs(b, "raw", ctx, db.SQL, ids, crossTarget)

	archiver, err := store.NewArchiver(db, archiveTestTimezone, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatalf("NewArchiver: %v", err)
	}
	if _, err := archiver.RunOnce(ctx, archiveTestNow); err != nil {
		b.Fatalf("RunOnce: %v", err)
	}
	benchmarkArchiveAPIs(b, "archived", ctx, db.SQL, ids, crossTarget)
	benchmarkArchiveComponents(b, ctx, db.SQL)

	expandArchiveBenchmarkHistory(b, db.SQL)
	benchmarkArchiveAPIs(b, "archived_expanded_history", ctx, db.SQL, ids, crossTarget)
}

func benchmarkArchiveAPIs(b *testing.B, phase string, ctx context.Context, db *sql.DB, targets []archiveSessionTarget, crossTarget *archiveSessionTarget) {
	b.Helper()
	w, err := NowWindow(archiveTestNow, archiveTestTimezone)
	if err != nil {
		b.Fatal(err)
	}
	classifier, err := NewClassifier(nil)
	if err != nil {
		b.Fatal(err)
	}
	prices := archiveNoPrices{}
	weights := HeatmapWeights{Tokens: 1, Cost: 1, Requests: 1}
	cases := []struct {
		name string
		call func() error
	}{
		{"snapshot_all_month", func() error {
			response, err := BuildSnapshot(ctx, db, classifier, w, "month", ClientAll, true, prices)
			if err != nil {
				return err
			}
			return json.NewEncoder(io.Discard).Encode(response)
		}},
		{"heatmap_all", func() error {
			response, err := BuildHeatmap(ctx, db, w, weights, ClientAll, true)
			if err != nil {
				return err
			}
			return json.NewEncoder(io.Discard).Encode(response)
		}},
		{"rankings_all_all", func() error {
			response, err := BuildRankings(ctx, db, RankingsOpts{SinceTag: "all", Client: ClientAll, ToolsTopN: 10, SkillsTopN: 10})
			if err != nil {
				return err
			}
			return json.NewEncoder(io.Discard).Encode(response)
		}},
		{"rates_codex_month", func() error {
			response, err := BuildRates(ctx, db, classifier, w, "month", ClientCodex)
			if err != nil {
				return err
			}
			return json.NewEncoder(io.Discard).Encode(response)
		}},
		{"sessions_all_limit100", func() error {
			response, err := BuildSessionList(ctx, db, ClientAll, 100, true)
			if err != nil {
				return err
			}
			return json.NewEncoder(io.Discard).Encode(response)
		}},
	}
	if crossTarget != nil {
		target := *crossTarget
		cases = append(cases, struct {
			name string
			call func() error
		}{"session_detail_cross_boundary", func() error {
			response, found, err := BuildSessionDetail(ctx, db, target.ID, target.Client, 10, 10, true)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("cross-boundary session not found")
			}
			return json.NewEncoder(io.Discard).Encode(response)
		}})
	}
	for _, tc := range cases {
		tc := tc
		b.Run(phase+"/"+tc.name, func(b *testing.B) { benchmarkArchiveAPI(b, tc.call) })
	}
}

func benchmarkArchiveAPI(b *testing.B, call func() error) {
	b.Helper()
	const samplesPerRun = 20
	allocs := testing.AllocsPerRun(5, func() {
		if err := call(); err != nil {
			b.Fatal(err)
		}
	})
	bytes := archiveBytesPerRun(5, call, b)
	samples := make([]time.Duration, 0, samplesPerRun*b.N)
	b.ResetTimer()
	for i := 0; i < b.N*samplesPerRun; i++ {
		started := time.Now()
		if err := call(); err != nil {
			b.Fatal(err)
		}
		samples = append(samples, time.Since(started))
	}
	b.StopTimer()
	b.ReportMetric(allocs, "allocs/api")
	b.ReportMetric(bytes, "bytes/api")
	b.ReportMetric(float64(archiveDurationPercentile(samples, 0.50))/float64(time.Millisecond), "p50_ms")
	b.ReportMetric(float64(archiveDurationPercentile(samples, 0.95))/float64(time.Millisecond), "p95_ms")
}

func archiveBytesPerRun(runs int, call func() error, b *testing.B) float64 {
	b.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		if err := call(); err != nil {
			b.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / float64(runs)
}

// benchmarkArchiveComponents reports execution-plus-scan timing for the two
// independently queried data sources, then isolates the representative Go
// bucket merge and a read transaction. These probes use the same table shape
// and aggregation grain as dashboard history paths without adding production
// timing hooks solely for tests.
func benchmarkArchiveComponents(b *testing.B, ctx context.Context, db *sql.DB) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := archiveTestNow.UTC()
	b.Run("components/historical_sql_exec_scan", func(b *testing.B) {
		benchmarkArchiveComponent(b, func() error {
			rows, err := db.QueryContext(ctx, `
				SELECT bucket_start, COALESCE(SUM(tokens_total), 0)
				FROM archive.usage_hourly
				WHERE client='claude' AND bucket_start >= ? AND bucket_start < ?
				GROUP BY bucket_start
			`, start, end)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var bucket time.Time
				var total int64
				if err := rows.Scan(&bucket, &total); err != nil {
					return err
				}
			}
			return rows.Err()
		})
	})
	b.Run("components/raw_sql_exec_scan", func(b *testing.B) {
		benchmarkArchiveComponent(b, func() error {
			rows, err := db.QueryContext(ctx, `
				SELECT date_trunc('hour', ts), COALESCE(SUM(value), 0)
				FROM metric_token_usage
				WHERE ts >= ? AND ts < ?
				GROUP BY 1
			`, start, end)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var bucket time.Time
				var total int64
				if err := rows.Scan(&bucket, &total); err != nil {
					return err
				}
			}
			return rows.Err()
		})
	})

	mergeRows := loadArchiveMergeRows(b, ctx, db, start, end)
	b.Run("components/go_bucket_merge", func(b *testing.B) {
		benchmarkArchiveComponent(b, func() error {
			merged := make(map[time.Time]int64, len(mergeRows))
			for _, row := range mergeRows {
				merged[row.Bucket] += row.Total
			}
			if len(merged) == 0 && len(mergeRows) > 0 {
				return fmt.Errorf("merge produced no buckets")
			}
			return nil
		})
	})
	b.Run("components/read_transaction", func(b *testing.B) {
		benchmarkArchiveComponent(b, func() error {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err := archiveScanBuckets(ctx, tx, `SELECT bucket_start, COALESCE(SUM(tokens_total), 0) FROM archive.usage_hourly WHERE client='claude' AND bucket_start >= ? AND bucket_start < ? GROUP BY bucket_start`, start, end); err != nil {
				return err
			}
			if err := archiveScanBuckets(ctx, tx, `SELECT date_trunc('hour', ts), COALESCE(SUM(value), 0) FROM metric_token_usage WHERE ts >= ? AND ts < ? GROUP BY 1`, start, end); err != nil {
				return err
			}
			return tx.Commit()
		})
	})
}

func archiveScanBuckets(ctx context.Context, db sqlQueryer, query string, start, end time.Time) error {
	rows, err := db.QueryContext(ctx, query, start, end)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket time.Time
		var total int64
		if err := rows.Scan(&bucket, &total); err != nil {
			return err
		}
	}
	return rows.Err()
}

func loadArchiveMergeRows(b *testing.B, ctx context.Context, db *sql.DB, start, end time.Time) []periodBucket {
	b.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT bucket_start, COALESCE(SUM(tokens_total), 0) FROM archive.usage_hourly
		WHERE client='claude' AND bucket_start >= ? AND bucket_start < ? GROUP BY bucket_start
	`, start, end)
	if err != nil {
		b.Fatalf("load archive merge rows: %v", err)
	}
	defer rows.Close()
	result := make([]periodBucket, 0)
	for rows.Next() {
		var row periodBucket
		if err := rows.Scan(&row.Bucket, &row.Total); err != nil {
			b.Fatalf("scan archive merge row: %v", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		b.Fatalf("iterate archive merge rows: %v", err)
	}
	rawRows, err := db.QueryContext(ctx, `SELECT date_trunc('hour', ts), COALESCE(SUM(value), 0) FROM metric_token_usage WHERE ts >= ? AND ts < ? GROUP BY 1`, start, end)
	if err != nil {
		b.Fatalf("load raw merge rows: %v", err)
	}
	defer rawRows.Close()
	for rawRows.Next() {
		var row periodBucket
		if err := rawRows.Scan(&row.Bucket, &row.Total); err != nil {
			b.Fatalf("scan raw merge row: %v", err)
		}
		result = append(result, row)
	}
	if err := rawRows.Err(); err != nil {
		b.Fatalf("iterate raw merge rows: %v", err)
	}
	return result
}

func benchmarkArchiveComponent(b *testing.B, call func() error) {
	b.Helper()
	const samplesPerRun = 20
	samples := make([]time.Duration, 0, samplesPerRun*b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N*samplesPerRun; i++ {
		started := time.Now()
		if err := call(); err != nil {
			b.Fatal(err)
		}
		samples = append(samples, time.Since(started))
	}
	b.StopTimer()
	b.ReportMetric(float64(archiveDurationPercentile(samples, 0.50))/float64(time.Microsecond), "p50_us")
	b.ReportMetric(float64(archiveDurationPercentile(samples, 0.95))/float64(time.Microsecond), "p95_us")
}

func openArchiveCopyBenchmark(b *testing.B, source string) *store.DB {
	b.Helper()
	// Reuse the tested copy/migration path with a tiny adapter through testing.T
	// semantics would be unsafe; benchmark setup repeats its read/copy behavior.
	in, err := os.Open(source)
	if err != nil {
		b.Fatalf("open archive source backup: %v", err)
	}
	defer in.Close()
	path := b.TempDir() + "/archive-benchmark-copy.duckdb"
	out, err := os.Create(path)
	if err != nil {
		b.Fatalf("create archive benchmark copy: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		b.Fatalf("copy archive benchmark backup: %v", err)
	}
	if err := out.Close(); err != nil {
		b.Fatalf("close archive benchmark copy: %v", err)
	}
	db, err := store.Open(config.StorageConfig{DuckDBPath: path})
	if err != nil {
		b.Fatalf("store.Open: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })
	migrations, err := store.LoadMigrations()
	if err != nil {
		b.Fatalf("LoadMigrations: %v", err)
	}
	if err := store.RunMigrations(db.SQL, migrations); err != nil {
		b.Fatalf("RunMigrations: %v", err)
	}
	return db
}

func benchmarkArchiveDashboardSuite(b *testing.B, ctx context.Context, db *sql.DB, sessionTargets []archiveSessionTarget) {
	b.Helper()
	w, err := NowWindow(archiveTestNow, archiveTestTimezone)
	if err != nil {
		b.Fatalf("NowWindow: %v", err)
	}
	classifier, err := NewClassifier(nil)
	if err != nil {
		b.Fatalf("NewClassifier: %v", err)
	}
	prices := archiveNoPrices{}
	weights := HeatmapWeights{Tokens: 1, Cost: 1, Requests: 1}
	samples := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		started := time.Now()
		if err := runArchiveDashboardSuite(ctx, db, classifier, w, weights, prices, sessionTargets); err != nil {
			b.Fatal(err)
		}
		samples = append(samples, time.Since(started))
	}
	b.StopTimer()
	b.ReportMetric(float64(archiveDurationPercentile(samples, 0.50))/float64(time.Millisecond), "p50_ms")
	b.ReportMetric(float64(archiveDurationPercentile(samples, 0.95))/float64(time.Millisecond), "p95_ms")
}

func runArchiveDashboardSuite(ctx context.Context, db *sql.DB, classifier *Classifier, w TimeWindow, weights HeatmapWeights, prices PriceLookup, sessionTargets []archiveSessionTarget) error {
	for _, client := range []Client{ClientAll, ClientClaude, ClientCodex} {
		for _, rng := range []string{"day", "week", "month"} {
			if _, err := BuildSnapshot(ctx, db, classifier, w, rng, client, true, prices); err != nil {
				return fmt.Errorf("snapshot %s/%s: %w", client, rng, err)
			}
			if _, err := BuildTrends(ctx, db, classifier, w, rng, client); err != nil {
				return fmt.Errorf("trends %s/%s: %w", client, rng, err)
			}
			if _, err := BuildRates(ctx, db, classifier, w, rng, client); err != nil {
				return fmt.Errorf("rates %s/%s: %w", client, rng, err)
			}
			if _, err := BuildPeriodModels(ctx, db, classifier, PeriodModelsOptions{Window: w, Range: rng, Client: client, PricingEnabled: true}); err != nil {
				return fmt.Errorf("period models %s/%s: %w", client, rng, err)
			}
		}
		if _, err := BuildHeatmap(ctx, db, w, weights, client, true); err != nil {
			return fmt.Errorf("heatmap %s: %w", client, err)
		}
		if _, err := BuildPricingModels(ctx, db, client, prices, true); err != nil {
			return fmt.Errorf("pricing models %s: %w", client, err)
		}
		for _, since := range []string{"7d", "30d", "all"} {
			start, tag, err := SinceStart(w, since)
			if err != nil {
				return err
			}
			if _, err := BuildRankings(ctx, db, RankingsOpts{SinceStart: start, SinceTag: tag, Client: client, ToolsTopN: 10, SkillsTopN: 10}); err != nil {
				return fmt.Errorf("rankings %s/%s: %w", client, since, err)
			}
		}
		if _, err := BuildSessionList(ctx, db, client, 100, true); err != nil {
			return fmt.Errorf("session list %s: %w", client, err)
		}
		for _, target := range sessionTargets {
			if _, _, err := BuildSessionDetail(ctx, db, target.ID, client, 10, 10, true); err != nil {
				return fmt.Errorf("session detail %s/%s/%s: %w", client, target.Client, target.ID, err)
			}
		}
	}
	return nil
}

func expandArchiveBenchmarkHistory(b *testing.B, db *sql.DB) {
	b.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		b.Fatalf("begin expanded history: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	usage, err := tx.Prepare(`
		INSERT INTO archive.usage_hourly (bucket_start, client, model, tokens_total, input_tokens, output_tokens, token_rows, request_count, cost_usd, model_last_seen)
		VALUES (?, ?, ?, 1000, 600, 400, 1, 1, 0.01, ?)
	`)
	if err != nil {
		b.Fatalf("prepare expanded usage: %v", err)
	}
	defer usage.Close()
	sessions, err := tx.Prepare(`
		INSERT INTO archive.sessions (client, session_id, first_active, last_active, tokens_total, input_tokens, output_tokens, request_count, cost_usd)
		VALUES (?, ?, ?, ?, 1000, 600, 400, 1, 0.01)
	`)
	if err != nil {
		b.Fatalf("prepare expanded sessions: %v", err)
	}
	defer sessions.Close()
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	models := []string{"archive-bench-a", "archive-bench-b", "archive-bench-c", "archive-bench-d"}
	for day := 0; day < 365; day++ {
		for hour := 0; hour < 24; hour++ {
			bucket := base.AddDate(0, 0, day).Add(time.Duration(hour) * time.Hour)
			for _, client := range []string{"claude", "codex"} {
				for _, model := range models {
					if _, err := usage.Exec(bucket, client, model, bucket); err != nil {
						b.Fatalf("insert expanded usage: %v", err)
					}
				}
			}
		}
	}
	for i := 0; i < 1000; i++ {
		client := "claude"
		if i%2 == 1 {
			client = "codex"
		}
		at := base.Add(time.Duration(i) * time.Hour)
		if _, err := sessions.Exec(client, fmt.Sprintf("archive-bench-session-%04d", i), at, at); err != nil {
			b.Fatalf("insert expanded session: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit expanded history: %v", err)
	}
}

func archiveDurationPercentile(values []time.Duration, percentile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := int(math.Ceil(float64(len(ordered))*percentile)) - 1
	if index < 0 {
		index = 0
	}
	return ordered[index]
}

func archiveCrossBoundaryTarget(ctx context.Context, db *sql.DB) (*archiveSessionTarget, error) {
	cutoff := archiveBenchmarkCutoff()
	const query = `WITH raw_activity AS (
		SELECT 'claude' client, session_id, ts FROM event_api_request WHERE session_id IS NOT NULL
		UNION ALL SELECT 'claude', session_id, ts FROM event_tool_result WHERE session_id IS NOT NULL
		UNION ALL SELECT 'claude', session_id, ts FROM metric_token_usage WHERE session_id IS NOT NULL
		UNION ALL SELECT 'codex', conversation_id, ts FROM codex_event_token_usage WHERE conversation_id IS NOT NULL
		UNION ALL SELECT 'codex', conversation_id, ts FROM codex_event_tool_result WHERE conversation_id IS NOT NULL
	) SELECT client, session_id FROM raw_activity
	GROUP BY 1, 2 HAVING MIN(ts) < ? AND MAX(ts) >= ? ORDER BY 1, 2 LIMIT 1`
	var client Client
	var id string
	err := db.QueryRowContext(ctx, query, cutoff, cutoff).Scan(&client, &id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &archiveSessionTarget{Client: client, ID: id}, nil
}

func archiveBenchmarkCutoff() time.Time {
	loc, _ := time.LoadLocation(archiveTestTimezone)
	local := archiveTestNow.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -30).UTC()
}
