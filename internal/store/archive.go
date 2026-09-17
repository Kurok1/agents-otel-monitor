/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.1.0
 */

package store

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/archivezone"
)

const archiveRetentionDays = 30

var archiveRawTables = []string{
	"metric_session_count",
	"metric_lines_of_code_count",
	"metric_pull_request_count",
	"metric_commit_count",
	"metric_cost_usage",
	"metric_token_usage",
	"metric_code_edit_tool_decision",
	"metric_active_time_total",
	"event_user_prompt",
	"event_api_request",
	"event_api_error",
	"event_tool_result",
	"event_tool_decision",
	"event_api_retries_exhausted",
	"event_compaction",
	"event_permission_mode_changed",
	"event_mcp_server_connection",
	"event_skill_activated",
	"event_at_mention",
	"codex_metric_skill_injected",
	"codex_metric_response_tbt",
	"codex_event_conversation_starts",
	"codex_event_api_request",
	"codex_event_token_usage",
	"codex_event_user_prompt",
	"codex_event_tool_decision",
	"codex_event_tool_result",
}

// ArchiveResult describes a completed raw-data archival sweep. Cutoff is the
// first retained instant, expressed in UTC.
type ArchiveResult struct {
	Cutoff      time.Time
	DeletedRows int64
	Days        int
}

// Archiver compacts expired raw telemetry into privacy-preserving summaries.
// It never uses maintenance_state as a source-of-truth watermark: late data is
// included naturally whenever its expired local date is processed.
type Archiver struct {
	db             *DB
	loc            *time.Location
	timezone       string
	log            *slog.Logger
	archivePrefix  string
	afterAggregate func() error

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewArchiver creates an archiver which uses the dashboard timezone for
// retention-day boundaries.
func NewArchiver(db *DB, timezone string, log *slog.Logger) (*Archiver, error) {
	if db == nil || db.SQL == nil {
		return nil, fmt.Errorf("archive requires an open database")
	}
	loc, err := archivezone.Validate(timezone)
	if err != nil {
		return nil, fmt.Errorf("validate archive timezone %q: %w", timezone, err)
	}
	if log == nil {
		log = slog.Default()
	}
	var catalog string
	if err := db.SQL.QueryRow(`SELECT current_database()`).Scan(&catalog); err != nil {
		return nil, fmt.Errorf("read archive database catalog: %w", err)
	}
	return &Archiver{
		db:            db,
		loc:           loc,
		timezone:      timezone,
		log:           log,
		archivePrefix: quoteArchiveIdentifier(catalog) + ".archive.",
	}, nil
}

// Start runs an initial catch-up after the service is ready, then schedules a
// daily sweep at 03:00 in the configured dashboard timezone.
func (a *Archiver) Start(ctx context.Context) {
	a.mu.Lock()
	if a.cancel != nil {
		a.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.wg.Add(1)
	a.mu.Unlock()

	go func() {
		defer a.wg.Done()
		a.runLoop(runCtx)
	}()
}

func (a *Archiver) runLoop(ctx context.Context) {
	if _, err := a.RunOnce(ctx, time.Now()); err != nil && ctx.Err() == nil {
		a.log.Error("archive startup catch-up failed", "err", err)
	}
	for {
		next := nextArchiveRun(time.Now(), a.loc)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if _, err := a.RunOnce(ctx, time.Now()); err != nil && ctx.Err() == nil {
				a.log.Error("scheduled archive failed", "err", err)
			}
		}
	}
}

// Stop cancels an in-flight sweep and waits for the background worker.
func (a *Archiver) Stop() {
	a.mu.Lock()
	cancel := a.cancel
	a.cancel = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.wg.Wait()
}

// RunOnce compacts every expired local date. Each date is a separate
// transaction, so failed or cancelled dates retain all their raw rows for a
// later retry.
func (a *Archiver) RunOnce(ctx context.Context, now time.Time) (ArchiveResult, error) {
	if _, err := archivezone.ValidateAt(a.timezone, now); err != nil {
		return ArchiveResult{}, fmt.Errorf("validate archive timezone before sweep: %w", err)
	}
	cutoff := archiveCutoff(now, a.loc)
	result := ArchiveResult{Cutoff: cutoff}

	days, err := a.expiredDays(ctx, cutoff)
	if err != nil {
		return result, err
	}
	if len(days) == 0 {
		a.recordState(ctx, result)
		return result, nil
	}
	for _, day := range days {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, _, err := a.archiveDayBounds(day, cutoff); err != nil {
			return result, err
		}
	}

	for _, day := range days {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		start, end, err := a.archiveDayBounds(day, cutoff)
		if err != nil {
			return result, err
		}
		deleted, hadRows, err := a.archiveDay(ctx, start, end)
		if err != nil {
			return result, err
		}
		if hadRows {
			result.Days++
			result.DeletedRows += deleted
		}
	}

	a.recordState(ctx, result)
	if result.Days > 0 && ctx.Err() == nil {
		a.checkpoint(ctx)
	}
	return result, nil
}

func (a *Archiver) archiveDayBounds(day, cutoff time.Time) (time.Time, time.Time, error) {
	start := day.UTC()
	end := day.In(a.loc).AddDate(0, 0, 1).UTC()
	if end.After(cutoff) {
		end = cutoff
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid archive day bounds for %s", start.Format(time.DateOnly))
	}
	if err := archivezone.ValidateInterval(a.loc, start, end); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("validate archive day %s: %w", start.Format(time.DateOnly), err)
	}
	return start, end, nil
}

func archiveCutoff(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	cutoff := today.AddDate(0, 0, -archiveRetentionDays).UTC()
	minimumAge := now.UTC().Add(-archiveRetentionDays * 24 * time.Hour)
	if cutoff.After(minimumAge) {
		return today.AddDate(0, 0, -archiveRetentionDays-1).UTC()
	}
	return cutoff
}

func localMidnight(at time.Time, loc *time.Location) time.Time {
	local := at.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}

func nextArchiveRun(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	next := time.Date(local.Year(), local.Month(), local.Day(), 3, 0, 0, 0, loc)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// expiredDays returns a small superset of expired local dates containing raw
// telemetry. DuckDB groups in UTC, so each UTC date is mapped to its local
// date and the following local date; that covers both sides of any UTC offset
// without walking potentially years of empty calendar days.
func (a *Archiver) expiredDays(ctx context.Context, cutoff time.Time) ([]time.Time, error) {
	parts := make([]string, 0, len(archiveRawTables))
	for _, table := range archiveRawTables {
		parts = append(parts, "SELECT date_trunc('day', ts) bucket_day FROM "+table+" WHERE ts < ?")
	}
	query := "SELECT DISTINCT bucket_day FROM (" + strings.Join(parts, " UNION ALL ") + ")"
	args := make([]any, len(archiveRawTables))
	for i := range args {
		args[i] = cutoff
	}
	rows, err := a.db.SQL.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("find expired telemetry dates: %w", err)
	}
	defer rows.Close()

	unique := make(map[time.Time]struct{})
	for rows.Next() {
		var utcDay time.Time
		if err := rows.Scan(&utcDay); err != nil {
			return nil, fmt.Errorf("scan expired telemetry date: %w", err)
		}
		localDay := localMidnight(utcDay, a.loc)
		for _, candidate := range []time.Time{localDay, localDay.AddDate(0, 0, 1)} {
			start := candidate.UTC()
			if start.Before(cutoff) {
				unique[start] = struct{}{}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired telemetry dates: %w", err)
	}
	days := make([]time.Time, 0, len(unique))
	for day := range unique {
		days = append(days, day)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	return days, nil
}

func (a *Archiver) archiveDay(ctx context.Context, start, end time.Time) (int64, bool, error) {
	if err := archivezone.ValidateInterval(a.loc, start, end); err != nil {
		return 0, false, fmt.Errorf("validate archive day %s: %w", start.Format(time.DateOnly), err)
	}
	a.db.writeMu.Lock()
	defer a.db.writeMu.Unlock()

	tx, err := a.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("begin archive day %s: %w", start.Format(time.DateOnly), err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, statement := range archiveAggregationStatements {
		statement = a.archiveSQL(statement)
		if err := ctx.Err(); err != nil {
			return 0, false, err
		}
		if _, err := tx.ExecContext(ctx, statement, archiveStatementArgs(statement, start, end)...); err != nil {
			return 0, false, fmt.Errorf("archive day %s: %w", start.Format(time.DateOnly), err)
		}
	}
	if a.afterAggregate != nil {
		if err := a.afterAggregate(); err != nil {
			return 0, false, fmt.Errorf("after archive aggregation: %w", err)
		}
	}

	var deleted int64
	for _, table := range archiveRawTables {
		if err := ctx.Err(); err != nil {
			return 0, false, err
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE ts >= ? AND ts < ?", start, end)
		if err != nil {
			return 0, false, fmt.Errorf("delete expired %s: %w", table, err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return 0, false, fmt.Errorf("count deleted %s: %w", table, err)
		}
		deleted += rows
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit archive day %s: %w", start.Format(time.DateOnly), err)
	}
	return deleted, deleted > 0, nil
}

func archiveStatementArgs(statement string, start, end time.Time) []any {
	pairs := strings.Count(statement, "?") / 2
	args := make([]any, 0, pairs*2)
	for range pairs {
		args = append(args, start, end)
	}
	return args
}

func (a *Archiver) recordState(ctx context.Context, result ArchiveResult) {
	if ctx.Err() != nil {
		return
	}
	a.db.writeMu.Lock()
	defer a.db.writeMu.Unlock()
	_, err := a.db.SQL.ExecContext(ctx, a.archiveSQL(`
		INSERT INTO archive.maintenance_state (id, last_cutoff_ts, last_success_at, last_deleted_rows)
		VALUES (1, ?, CURRENT_TIMESTAMP, ?)
		ON CONFLICT (id) DO UPDATE SET
			last_cutoff_ts = excluded.last_cutoff_ts,
			last_success_at = excluded.last_success_at,
			last_deleted_rows = excluded.last_deleted_rows
	`), result.Cutoff, result.DeletedRows)
	if err != nil {
		a.log.Warn("record archive maintenance state failed", "err", err)
	}
}

func (a *Archiver) archiveSQL(statement string) string {
	return strings.ReplaceAll(statement, "archive.", a.archivePrefix)
}

func quoteArchiveIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func (a *Archiver) checkpoint(ctx context.Context) {
	a.db.writeMu.Lock()
	defer a.db.writeMu.Unlock()
	if _, err := a.db.SQL.ExecContext(ctx, "CHECKPOINT"); err != nil {
		a.log.Warn("archive checkpoint failed after committed sweep", "err", err)
	}
}

var archiveAggregationStatements = []string{
	archiveUsageSQL,
	archiveToolHourlySQL,
	archiveSkillHourlySQL,
	archiveSessionsSQL,
	archiveSessionToolsSQL,
	archiveSessionSkillsSQL,
}

const archiveUsageSQL = `
INSERT INTO archive.usage_hourly (
    bucket_start, client, model, tokens_total, input_tokens, output_tokens,
    cache_read_tokens, cache_creation_tokens, reasoning_tokens,
    throughput_input_tokens, token_rows, request_count, cost_usd,
    speed_units, speed_duration_ms, model_last_seen
)
SELECT bucket_start, client, model,
       SUM(tokens_total), SUM(input_tokens), SUM(output_tokens),
       SUM(cache_read_tokens), SUM(cache_creation_tokens), SUM(reasoning_tokens),
       SUM(throughput_input_tokens), SUM(token_rows), SUM(request_count), SUM(cost_usd),
       SUM(speed_units), SUM(speed_duration_ms), MAX(model_last_seen)
FROM (
    SELECT date_trunc('hour', ts) bucket_start, 'claude' client, COALESCE(model, '') model,
           SUM(value) tokens_total,
           SUM(CASE WHEN type = 'input' THEN value ELSE 0 END) input_tokens,
           SUM(CASE WHEN type = 'output' THEN value ELSE 0 END) output_tokens,
           SUM(CASE WHEN type = 'cacheRead' THEN value ELSE 0 END) cache_read_tokens,
           SUM(CASE WHEN type = 'cacheCreation' THEN value ELSE 0 END) cache_creation_tokens,
           0 reasoning_tokens,
           SUM(CASE WHEN type = 'input' THEN value ELSE 0 END) throughput_input_tokens,
           COUNT(*) token_rows, 0 request_count, 0.0 cost_usd, 0.0 speed_units, 0.0 speed_duration_ms,
           MAX(CASE WHEN NULLIF(model, '') IS NOT NULL THEN ts END) model_last_seen
    FROM metric_token_usage WHERE ts >= ? AND ts < ? GROUP BY 1, 2, 3
    UNION ALL
    SELECT date_trunc('hour', ts), 'claude', COALESCE(model, ''),
           0, 0, 0, 0, 0, 0, 0, 0, 0, SUM(value), 0.0, 0.0, NULL
    FROM metric_cost_usage WHERE ts >= ? AND ts < ? GROUP BY 1, 2, 3
    UNION ALL
    SELECT date_trunc('hour', ts), 'claude', COALESCE(model, ''),
           0, 0, 0, 0, 0, 0, 0, 0, COUNT(*), 0.0,
           SUM(CASE WHEN NULLIF(model, '') IS NOT NULL AND duration_ms > 0 AND output_tokens > 0 THEN output_tokens ELSE 0 END),
           SUM(CASE WHEN NULLIF(model, '') IS NOT NULL AND duration_ms > 0 AND output_tokens > 0 THEN duration_ms ELSE 0 END),
           MAX(CASE WHEN NULLIF(model, '') IS NOT NULL THEN ts END)
    FROM event_api_request WHERE ts >= ? AND ts < ? GROUP BY 1, 2, 3
    UNION ALL
    SELECT date_trunc('hour', ts), 'codex', COALESCE(model, ''),
           SUM(COALESCE(input_token_count, 0) + COALESCE(output_token_count, 0)),
           SUM(COALESCE(input_token_count, 0)), SUM(COALESCE(output_token_count, 0)),
           SUM(COALESCE(cached_token_count, 0)), 0, SUM(COALESCE(reasoning_token_count, 0)),
           SUM(GREATEST(COALESCE(input_token_count, 0) - COALESCE(cached_token_count, 0), 0)),
           COUNT(*), COUNT(*), SUM(COALESCE(cost_usd, 0)), 0.0, 0.0,
           MAX(CASE WHEN NULLIF(model, '') IS NOT NULL THEN ts END)
    FROM codex_event_token_usage WHERE ts >= ? AND ts < ? GROUP BY 1, 2, 3
    UNION ALL
    SELECT date_trunc('hour', ts), 'codex', COALESCE(model, ''),
           0, 0, 0, 0, 0, 0, 0, 0, 0, 0.0,
           SUM(CASE WHEN NULLIF(model, '') IS NOT NULL AND sample_count > 0 AND sum_ms > 0 THEN sample_count ELSE 0 END),
           SUM(CASE WHEN NULLIF(model, '') IS NOT NULL AND sample_count > 0 AND sum_ms > 0 THEN sum_ms ELSE 0 END),
           NULL
    FROM codex_metric_response_tbt WHERE ts >= ? AND ts < ? GROUP BY 1, 2, 3
) grouped
GROUP BY 1, 2, 3
ON CONFLICT (bucket_start, client, model) DO UPDATE SET
    tokens_total = archive.usage_hourly.tokens_total + excluded.tokens_total,
    input_tokens = archive.usage_hourly.input_tokens + excluded.input_tokens,
    output_tokens = archive.usage_hourly.output_tokens + excluded.output_tokens,
    cache_read_tokens = archive.usage_hourly.cache_read_tokens + excluded.cache_read_tokens,
    cache_creation_tokens = archive.usage_hourly.cache_creation_tokens + excluded.cache_creation_tokens,
    reasoning_tokens = archive.usage_hourly.reasoning_tokens + excluded.reasoning_tokens,
    throughput_input_tokens = archive.usage_hourly.throughput_input_tokens + excluded.throughput_input_tokens,
    token_rows = archive.usage_hourly.token_rows + excluded.token_rows,
    request_count = archive.usage_hourly.request_count + excluded.request_count,
    cost_usd = archive.usage_hourly.cost_usd + excluded.cost_usd,
    speed_units = archive.usage_hourly.speed_units + excluded.speed_units,
    speed_duration_ms = archive.usage_hourly.speed_duration_ms + excluded.speed_duration_ms,
    model_last_seen = CASE
        WHEN archive.usage_hourly.model_last_seen IS NULL THEN excluded.model_last_seen
        WHEN excluded.model_last_seen IS NULL THEN archive.usage_hourly.model_last_seen
        ELSE GREATEST(archive.usage_hourly.model_last_seen, excluded.model_last_seen)
    END`

const archiveToolHourlySQL = `
INSERT INTO archive.tool_hourly (bucket_start, client, tool_name, call_count)
SELECT date_trunc('hour', ts), client, tool_name, COUNT(*)
FROM (
    SELECT ts, 'claude' client, tool_name FROM event_tool_result WHERE ts >= ? AND ts < ? AND tool_name IS NOT NULL
    UNION ALL
    SELECT ts, 'codex', tool_name FROM codex_event_tool_result WHERE ts >= ? AND ts < ? AND tool_name IS NOT NULL
) tools
GROUP BY 1, 2, 3
ON CONFLICT (bucket_start, client, tool_name) DO UPDATE SET call_count = archive.tool_hourly.call_count + excluded.call_count`

const archiveSkillHourlySQL = `
INSERT INTO archive.skill_hourly (bucket_start, client, skill_name, activation_count)
SELECT date_trunc('hour', ts), client, skill_name, SUM(activation_count)
FROM (
    SELECT ts, 'claude' client, skill_name, 1 activation_count
    FROM event_skill_activated WHERE ts >= ? AND ts < ? AND skill_name IS NOT NULL
    UNION ALL
    SELECT ts, 'codex', skill, value
    FROM codex_metric_skill_injected
    WHERE ts >= ? AND ts < ? AND skill IS NOT NULL AND lower(status) IN ('ok', 'success')
) skills
GROUP BY 1, 2, 3
ON CONFLICT (bucket_start, client, skill_name) DO UPDATE SET
    activation_count = archive.skill_hourly.activation_count + excluded.activation_count`

const archiveSessionsSQL = `
INSERT INTO archive.sessions (
    client, session_id, first_active, last_active, tokens_total, input_tokens,
    output_tokens, cache_read_tokens, reasoning_tokens, request_count,
    tool_calls, skill_activations, cost_usd
)
SELECT client, session_id, MIN(active_ts), MAX(active_ts),
       SUM(tokens_total), SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens),
       SUM(reasoning_tokens), SUM(request_count), SUM(tool_calls), SUM(skill_activations), SUM(cost_usd)
FROM (
    SELECT 'claude' client, session_id, ts active_ts, value tokens_total,
           CASE WHEN type = 'input' THEN value ELSE 0 END input_tokens,
           CASE WHEN type = 'output' THEN value ELSE 0 END output_tokens,
           CASE WHEN type = 'cacheRead' THEN value ELSE 0 END cache_read_tokens,
           0 reasoning_tokens, 0 request_count, 0 tool_calls, 0 skill_activations, 0.0 cost_usd
    FROM metric_token_usage WHERE ts >= ? AND ts < ? AND session_id IS NOT NULL
    UNION ALL
    SELECT 'codex', conversation_id, ts,
           COALESCE(input_token_count, 0) + COALESCE(output_token_count, 0),
           COALESCE(input_token_count, 0), COALESCE(output_token_count, 0), COALESCE(cached_token_count, 0),
           COALESCE(reasoning_token_count, 0), 1, 0, 0, COALESCE(cost_usd, 0)
    FROM codex_event_token_usage WHERE ts >= ? AND ts < ? AND conversation_id IS NOT NULL
    UNION ALL
    SELECT 'claude', session_id, NULL, 0, 0, 0, 0, 0, 0, 0, 0, value
    FROM metric_cost_usage WHERE ts >= ? AND ts < ? AND session_id IS NOT NULL
    UNION ALL
    SELECT 'claude', session_id, ts, 0, 0, 0, 0, 0, 1, 0, 0, 0.0
    FROM event_api_request WHERE ts >= ? AND ts < ? AND session_id IS NOT NULL
    UNION ALL
    SELECT 'claude', session_id, ts, 0, 0, 0, 0, 0, 0, 1, 0, 0.0
    FROM event_tool_result WHERE ts >= ? AND ts < ? AND session_id IS NOT NULL
    UNION ALL
    SELECT 'codex', conversation_id, ts, 0, 0, 0, 0, 0, 0, 1, 0, 0.0
    FROM codex_event_tool_result WHERE ts >= ? AND ts < ? AND conversation_id IS NOT NULL
    UNION ALL
    SELECT 'claude', session_id, ts, 0, 0, 0, 0, 0, 0, 0, 1, 0.0
    FROM event_skill_activated WHERE ts >= ? AND ts < ? AND session_id IS NOT NULL
    UNION ALL
    SELECT 'claude', session_id, ts, 0, 0, 0, 0, 0, 0, 0, 0, 0.0
    FROM event_user_prompt WHERE ts >= ? AND ts < ? AND session_id IS NOT NULL
    UNION ALL
    SELECT 'codex', conversation_id, ts, 0, 0, 0, 0, 0, 0, 0, 0, 0.0
    FROM codex_event_user_prompt WHERE ts >= ? AND ts < ? AND conversation_id IS NOT NULL
    UNION ALL
    SELECT 'codex', conversation_id, ts, 0, 0, 0, 0, 0, 0, 0, 0, 0.0
    FROM codex_event_conversation_starts WHERE ts >= ? AND ts < ? AND conversation_id IS NOT NULL
) sessions
GROUP BY 1, 2
ON CONFLICT (client, session_id) DO UPDATE SET
    first_active = CASE
        WHEN archive.sessions.first_active IS NULL THEN excluded.first_active
        WHEN excluded.first_active IS NULL THEN archive.sessions.first_active
        ELSE LEAST(archive.sessions.first_active, excluded.first_active)
    END,
    last_active = CASE
        WHEN archive.sessions.last_active IS NULL THEN excluded.last_active
        WHEN excluded.last_active IS NULL THEN archive.sessions.last_active
        ELSE GREATEST(archive.sessions.last_active, excluded.last_active)
    END,
    tokens_total = archive.sessions.tokens_total + excluded.tokens_total,
    input_tokens = archive.sessions.input_tokens + excluded.input_tokens,
    output_tokens = archive.sessions.output_tokens + excluded.output_tokens,
    cache_read_tokens = archive.sessions.cache_read_tokens + excluded.cache_read_tokens,
    reasoning_tokens = archive.sessions.reasoning_tokens + excluded.reasoning_tokens,
    request_count = archive.sessions.request_count + excluded.request_count,
    tool_calls = archive.sessions.tool_calls + excluded.tool_calls,
    skill_activations = archive.sessions.skill_activations + excluded.skill_activations,
    cost_usd = archive.sessions.cost_usd + excluded.cost_usd`

const archiveSessionToolsSQL = `
INSERT INTO archive.session_tools (client, session_id, tool_name, call_count)
SELECT client, session_id, tool_name, COUNT(*)
FROM (
    SELECT 'claude' client, session_id, tool_name FROM event_tool_result
    WHERE ts >= ? AND ts < ? AND session_id IS NOT NULL AND tool_name IS NOT NULL
    UNION ALL
    SELECT 'codex', conversation_id, tool_name FROM codex_event_tool_result
    WHERE ts >= ? AND ts < ? AND conversation_id IS NOT NULL AND tool_name IS NOT NULL
) tools
GROUP BY 1, 2, 3
ON CONFLICT (client, session_id, tool_name) DO UPDATE SET
    call_count = archive.session_tools.call_count + excluded.call_count`

const archiveSessionSkillsSQL = `
INSERT INTO archive.session_skills (client, session_id, skill_name, activation_count)
SELECT 'claude', session_id, skill_name, COUNT(*)
FROM event_skill_activated
WHERE ts >= ? AND ts < ? AND session_id IS NOT NULL AND skill_name IS NOT NULL
GROUP BY 1, 2, 3
ON CONFLICT (client, session_id, skill_name) DO UPDATE SET
    activation_count = archive.session_skills.activation_count + excluded.activation_count`
