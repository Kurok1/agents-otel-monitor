package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// All queries here are read-only against the writer-shared *sql.DB
// (MaxOpenConns=1). They serialize behind in-flight flushes, but the
// dashboard load is light enough that this is acceptable for v1.

// localGrainExpr returns the SQL fragment that buckets a UTC ts column by
// the local calendar grain (day/week/month). Whole-hour offsets only —
// Asia/Shanghai (+08:00) and the rest of CJK fit; India (+05:30) would
// need minute granularity.
//
// We inline the offset as an integer literal rather than parameterizing
// because DuckDB's INTERVAL ... HOUR syntax doesn't accept bind params;
// the offset comes from a validated config tz so it's not user-controlled.
func localGrainExpr(w TimeWindow, tsCol, grain string) string {
	offsetHours := shanghaiOffsetSeconds(w, w.TodayStartUTC) / 3600
	return fmt.Sprintf("date_trunc('%s', %s + INTERVAL %d HOUR)", grain, tsCol, offsetHours)
}

func querySessionListOptimized(ctx context.Context, db sqlQueryer, client Client, limit int, arms string) ([]sessionListRow, error) {
	candidates := make(map[string]sessionListRow, limit*2)
	candidateSQL := `WITH activity(session_id, ts, client) AS (` + arms + `) SELECT session_id, client, MIN(ts), MAX(ts) FROM activity GROUP BY session_id, client ORDER BY MAX(ts) DESC, client, session_id LIMIT ?`
	rows, err := db.QueryContext(ctx, candidateSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("query raw session candidates: %w", err)
	}
	for rows.Next() {
		var row sessionListRow
		if err := rows.Scan(&row.SessionID, &row.Client, &row.FirstTs, &row.LastTs); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan raw session candidate: %w", err)
		}
		row.FirstTs = row.FirstTs.UTC()
		row.LastTs = row.LastTs.UTC()
		candidates[row.Client+"\x00"+row.SessionID] = row
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate raw session candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close raw session candidates: %w", err)
	}
	for _, source := range archiveClients(client) {
		rows, err := db.QueryContext(ctx, `SELECT session_id, first_active, last_active FROM archive.sessions WHERE client=? AND last_active IS NOT NULL ORDER BY last_active DESC, client, session_id LIMIT ?`, source, limit)
		if err != nil {
			return nil, fmt.Errorf("query archive session candidates: %w", err)
		}
		for rows.Next() {
			var row sessionListRow
			var first, last sql.NullTime
			if err := rows.Scan(&row.SessionID, &first, &last); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan archive session candidate: %w", err)
			}
			if !last.Valid {
				continue
			}
			row.Client = string(source)
			row.FirstTs = first.Time.UTC()
			row.LastTs = last.Time.UTC()
			key := row.Client + "\x00" + row.SessionID
			if old, ok := candidates[key]; ok {
				if row.FirstTs.Before(old.FirstTs) {
					old.FirstTs = row.FirstTs
				}
				if row.LastTs.After(old.LastTs) {
					old.LastTs = row.LastTs
				}
				candidates[key] = old
			} else {
				candidates[key] = row
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate archive session candidates: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close archive session candidates: %w", err)
		}
	}
	selected := make([]sessionListRow, 0, len(candidates))
	for _, row := range candidates {
		selected = append(selected, row)
	}
	sort.Slice(selected, func(i, j int) bool {
		if !selected[i].LastTs.Equal(selected[j].LastTs) {
			return selected[i].LastTs.After(selected[j].LastTs)
		}
		if selected[i].Client != selected[j].Client {
			return selected[i].Client < selected[j].Client
		}
		return selected[i].SessionID < selected[j].SessionID
	})
	if len(selected) > limit {
		selected = selected[:limit]
	}
	if len(selected) == 0 {
		return []sessionListRow{}, nil
	}
	values := make([]string, 0, len(selected))
	args := make([]any, 0, len(selected)*2)
	for _, row := range selected {
		values = append(values, "(?, ?)")
		args = append(args, row.Client, row.SessionID)
	}
	wanted := strings.Join(values, ",")
	rawSQL := `WITH wanted(client, session_id) AS (VALUES ` + wanted + `), activity(session_id,ts,client) AS (` + arms + `), sess AS (SELECT w.session_id,w.client,MIN(a.ts) first_ts,MAX(a.ts) last_ts FROM wanted w LEFT JOIN activity a ON a.client=w.client AND a.session_id=w.session_id GROUP BY w.session_id,w.client) SELECT s.session_id,s.client,s.first_ts,s.last_ts,CASE WHEN s.client='claude' THEN COALESCE((SELECT SUM(value) FROM metric_token_usage t WHERE t.session_id=s.session_id),0) ELSE COALESCE((SELECT SUM(COALESCE(input_token_count,0)+COALESCE(output_token_count,0)) FROM codex_event_token_usage t WHERE t.conversation_id=s.session_id),0) END,CASE WHEN s.client='claude' THEN (SELECT COUNT(*) FROM event_api_request t WHERE t.session_id=s.session_id) ELSE (SELECT COUNT(*) FROM codex_event_token_usage t WHERE t.conversation_id=s.session_id) END,CASE WHEN s.client='claude' THEN (SELECT COUNT(*) FROM event_tool_result t WHERE t.session_id=s.session_id) ELSE (SELECT COUNT(*) FROM codex_event_tool_result t WHERE t.conversation_id=s.session_id) END,CASE WHEN s.client='claude' THEN (SELECT COUNT(*) FROM event_skill_activated t WHERE t.session_id=s.session_id) ELSE 0 END,CASE WHEN s.client='claude' THEN COALESCE((SELECT SUM(value) FROM metric_cost_usage t WHERE t.session_id=s.session_id),0) ELSE COALESCE((SELECT SUM(cost_usd) FROM codex_event_token_usage t WHERE t.conversation_id=s.session_id),0) END FROM sess s`
	byKey := make(map[string]sessionListRow, len(selected))
	rows, err = db.QueryContext(ctx, rawSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("query selected raw sessions: %w", err)
	}
	for rows.Next() {
		var row sessionListRow
		var first, last sql.NullTime
		if err := rows.Scan(&row.SessionID, &row.Client, &first, &last, &row.Tokens, &row.Requests, &row.ToolCalls, &row.Skills, &row.Cost); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan selected raw session: %w", err)
		}
		if first.Valid {
			row.FirstTs = first.Time.UTC()
		}
		if last.Valid {
			row.LastTs = last.Time.UTC()
		}
		byKey[row.Client+"\x00"+row.SessionID] = row
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate selected raw sessions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close selected raw sessions: %w", err)
	}
	archiveSQL := `WITH wanted(client, session_id) AS (VALUES ` + wanted + `) SELECT a.client,a.session_id,a.first_active,a.last_active,a.tokens_total,a.request_count,a.tool_calls,a.skill_activations,a.cost_usd FROM archive.sessions a JOIN wanted w ON a.client=w.client AND a.session_id=w.session_id`
	rows, err = db.QueryContext(ctx, archiveSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("query selected archive sessions: %w", err)
	}
	for rows.Next() {
		var row sessionListRow
		var first, last sql.NullTime
		if err := rows.Scan(&row.Client, &row.SessionID, &first, &last, &row.Tokens, &row.Requests, &row.ToolCalls, &row.Skills, &row.Cost); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan selected archive session: %w", err)
		}
		if first.Valid {
			row.FirstTs = first.Time.UTC()
		}
		if last.Valid {
			row.LastTs = last.Time.UTC()
		}
		key := row.Client + "\x00" + row.SessionID
		if raw, ok := byKey[key]; ok {
			if !row.FirstTs.IsZero() && (raw.FirstTs.IsZero() || row.FirstTs.Before(raw.FirstTs)) {
				raw.FirstTs = row.FirstTs
			}
			if !row.LastTs.IsZero() && (raw.LastTs.IsZero() || row.LastTs.After(raw.LastTs)) {
				raw.LastTs = row.LastTs
			}
			raw.Tokens += row.Tokens
			raw.Requests += row.Requests
			raw.ToolCalls += row.ToolCalls
			raw.Skills += row.Skills
			raw.Cost += row.Cost
			byKey[key] = raw
		} else {
			byKey[key] = row
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate selected archive sessions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close selected archive sessions: %w", err)
	}
	out := make([]sessionListRow, 0, len(selected))
	for _, candidate := range selected {
		if row, ok := byKey[candidate.Client+"\x00"+candidate.SessionID]; ok {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastTs.Equal(out[j].LastTs) {
			return out[i].LastTs.After(out[j].LastTs)
		}
		if out[i].Client != out[j].Client {
			return out[i].Client < out[j].Client
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────
// Period-based KPI queries — work for any [start, end) window.
// ─────────────────────────────────────────────────────────────────────

type periodTokens struct {
	In    int64
	Out   int64
	Total int64
}

// archiveBounds selects only complete UTC-hour archive buckets. Raw reads keep
// the caller's exact window, so a partial current hour is never rounded into
// a historical aggregate.
func archiveBounds(start, end time.Time) (time.Time, time.Time, bool) {
	start = start.UTC()
	end = end.UTC()
	first := start.Truncate(time.Hour)
	if first.Before(start) {
		first = first.Add(time.Hour)
	}
	last := end.Truncate(time.Hour)
	return first, last, first.Before(last)
}

func archiveClients(client Client) []Client {
	clients := make([]Client, 0, 2)
	if client.includesClaude() {
		clients = append(clients, ClientClaude)
	}
	if client.includesCodex() {
		clients = append(clients, ClientCodex)
	}
	return clients
}

func addArchivePeriodTokens(ctx context.Context, db sqlQueryer, client Client, start, end time.Time, dst *periodTokens) error {
	first, last, ok := archiveBounds(start, end)
	if !ok {
		return nil
	}
	for _, source := range archiveClients(client) {
		var in, out, total int64
		const q = `SELECT COALESCE(SUM(CASE WHEN client='codex' THEN input_tokens-cache_read_tokens ELSE input_tokens END), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(tokens_total), 0) FROM archive.usage_hourly WHERE client=? AND bucket_start>=? AND bucket_start<?`
		if err := db.QueryRowContext(ctx, q, source, first, last).Scan(&in, &out, &total); err != nil {
			return fmt.Errorf("query archive period tokens (%s): %w", source, err)
		}
		dst.In += in
		dst.Out += out
		dst.Total += total
	}
	return nil
}

// QueryPeriodTokens — totals for [start, end), summed across the requested
// client arms. Codex projection: in = input - cached (non-cache input, so the
// split is comparable with Claude's), total = input + output (cached and
// reasoning are subsets and must not be re-added).
func QueryPeriodTokens(ctx context.Context, db sqlQueryer, client Client, start, end time.Time) (periodTokens, error) {
	var r periodTokens
	if err := addArchivePeriodTokens(ctx, db, client, start, end, &r); err != nil {
		return r, err
	}
	if client.includesClaude() {
		const q = `
			SELECT
			  COALESCE(SUM(CASE WHEN type='input'  THEN value END), 0) AS tokens_in,
			  COALESCE(SUM(CASE WHEN type='output' THEN value END), 0) AS tokens_out,
			  COALESCE(SUM(value), 0)                                   AS tokens_total
			FROM metric_token_usage
			WHERE ts >= ? AND ts < ?
		`
		var c periodTokens
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&c.In, &c.Out, &c.Total); err != nil {
			return r, fmt.Errorf("query period tokens (claude): %w", err)
		}
		r.In += c.In
		r.Out += c.Out
		r.Total += c.Total
	}
	if client.includesCodex() {
		const q = `
			SELECT
			  COALESCE(SUM(COALESCE(input_token_count, 0) - COALESCE(cached_token_count, 0)), 0),
			  COALESCE(SUM(COALESCE(output_token_count, 0)), 0),
			  COALESCE(SUM(COALESCE(input_token_count, 0) + COALESCE(output_token_count, 0)), 0)
			FROM codex_event_token_usage
			WHERE ts >= ? AND ts < ?
		`
		var c periodTokens
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&c.In, &c.Out, &c.Total); err != nil {
			return r, fmt.Errorf("query period tokens (codex): %w", err)
		}
		r.In += c.In
		r.Out += c.Out
		r.Total += c.Total
	}
	return r, nil
}

// QueryPeriodTokensTotal — just the merged total (prev-period convenience).
func QueryPeriodTokensTotal(ctx context.Context, db sqlQueryer, client Client, start, end time.Time) (int64, error) {
	r, err := QueryPeriodTokens(ctx, db, client, start, end)
	if err != nil {
		return 0, err
	}
	return r.Total, nil
}

// QueryPeriodCost — total cost in [start, end). Claude cost is authoritative
// (metric_cost_usage); codex cost is the ingest-time estimate (cost_usd), only
// present when pricing is enabled. Both arms accumulate.
func QueryPeriodCost(ctx context.Context, db sqlQueryer, client Client, start, end time.Time) (float64, error) {
	var total float64
	if first, last, ok := archiveBounds(start, end); ok {
		for _, source := range archiveClients(client) {
			var v float64
			if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_usd), 0) FROM archive.usage_hourly WHERE client=? AND bucket_start>=? AND bucket_start<?`, source, first, last).Scan(&v); err != nil {
				return 0, fmt.Errorf("query archive period cost (%s): %w", source, err)
			}
			total += v
		}
	}
	if client.includesClaude() {
		const q = `SELECT COALESCE(SUM(value), 0) FROM metric_cost_usage WHERE ts >= ? AND ts < ?`
		var v float64
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&v); err != nil {
			return 0, fmt.Errorf("query period cost (claude): %w", err)
		}
		total += v
	}
	if client.includesCodex() {
		const q = `SELECT COALESCE(SUM(cost_usd), 0) FROM codex_event_token_usage WHERE ts >= ? AND ts < ?`
		var v float64
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&v); err != nil {
			return 0, fmt.Errorf("query period cost (codex): %w", err)
		}
		total += v
	}
	return total, nil
}

// periodCache carries cache KPIs with an explicit hit-rate denominator,
// because the two families define the rate differently:
//   - Claude: read / (read + creation) — fraction of cache-touched tokens
//   - Codex:  cached / input           — fraction of input served from cache
//
// The merged rate is Read / HitDenom with both sides accumulated.
type periodCache struct {
	Read     int64
	Creation int64
	HitDenom int64
}

// QueryPeriodCache — cache stats in [start, end) across the requested arms.
func QueryPeriodCache(ctx context.Context, db sqlQueryer, client Client, start, end time.Time) (periodCache, error) {
	var pc periodCache
	if first, last, ok := archiveBounds(start, end); ok {
		for _, source := range archiveClients(client) {
			var read, creation, input int64
			if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_creation_tokens), 0), COALESCE(SUM(input_tokens), 0) FROM archive.usage_hourly WHERE client=? AND bucket_start>=? AND bucket_start<?`, source, first, last).Scan(&read, &creation, &input); err != nil {
				return pc, fmt.Errorf("query archive period cache (%s): %w", source, err)
			}
			pc.Read += read
			pc.Creation += creation
			if source == ClientCodex {
				pc.HitDenom += input
			} else {
				pc.HitDenom += read + creation
			}
		}
	}
	if client.includesClaude() {
		const q = `
			SELECT
			  COALESCE(SUM(CASE WHEN type='cacheRead'     THEN value END), 0) AS read_tokens,
			  COALESCE(SUM(CASE WHEN type='cacheCreation' THEN value END), 0) AS creation_tokens
			FROM metric_token_usage
			WHERE ts >= ? AND ts < ?
			  AND type IN ('cacheRead', 'cacheCreation')
		`
		var read, creation int64
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&read, &creation); err != nil {
			return pc, fmt.Errorf("query period cache (claude): %w", err)
		}
		pc.Read += read
		pc.Creation += creation
		pc.HitDenom += read + creation
	}
	if client.includesCodex() {
		const q = `
			SELECT
			  COALESCE(SUM(COALESCE(cached_token_count, 0)), 0),
			  COALESCE(SUM(COALESCE(input_token_count, 0)), 0)
			FROM codex_event_token_usage
			WHERE ts >= ? AND ts < ?
		`
		var cached, input int64
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&cached, &input); err != nil {
			return pc, fmt.Errorf("query period cache (codex): %w", err)
		}
		pc.Read += cached
		pc.HitDenom += input
	}
	return pc, nil
}

// QueryPeriodRequests — API request count. Codex counts response.completed
// rows (codex_event_token_usage), NOT codex_event_api_request (attempt grain,
// includes retries).
func QueryPeriodRequests(ctx context.Context, db sqlQueryer, client Client, start, end time.Time) (int64, error) {
	var total int64
	if first, last, ok := archiveBounds(start, end); ok {
		for _, source := range archiveClients(client) {
			var v int64
			if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(request_count), 0) FROM archive.usage_hourly WHERE client=? AND bucket_start>=? AND bucket_start<?`, source, first, last).Scan(&v); err != nil {
				return 0, fmt.Errorf("query archive period requests (%s): %w", source, err)
			}
			total += v
		}
	}
	if client.includesClaude() {
		const q = `SELECT COUNT(*) FROM event_api_request WHERE ts >= ? AND ts < ?`
		var v int64
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&v); err != nil {
			return 0, fmt.Errorf("query period requests (claude): %w", err)
		}
		total += v
	}
	if client.includesCodex() {
		const q = `SELECT COUNT(*) FROM codex_event_token_usage WHERE ts >= ? AND ts < ?`
		var v int64
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&v); err != nil {
			return 0, fmt.Errorf("query period requests (codex): %w", err)
		}
		total += v
	}
	return total, nil
}

// ─────────────────────────────────────────────────────────────────────
// Sparkline queries — bucketed by `grain` over [start, end).
// ─────────────────────────────────────────────────────────────────────

type periodBucket struct {
	Bucket time.Time
	Total  int64
}

// runBucketQuery executes one arm's bucketed query and folds rows into acc.
func runBucketQuery(ctx context.Context, db sqlQueryer, q string, acc map[time.Time]int64, label string, args ...any) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("query %s: %w", label, err)
	}
	defer rows.Close()
	for rows.Next() {
		var b periodBucket
		if err := rows.Scan(&b.Bucket, &b.Total); err != nil {
			return fmt.Errorf("scan %s: %w", label, err)
		}
		acc[b.Bucket.UTC()] = acc[b.Bucket.UTC()] + b.Total
	}
	return rows.Err()
}

func bucketsFromMap(acc map[time.Time]int64) []periodBucket {
	out := make([]periodBucket, 0, len(acc))
	for k, v := range acc {
		out = append(out, periodBucket{Bucket: k, Total: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket.Before(out[j].Bucket) })
	return out
}

// QueryTokensSparkline — bucketed total tokens across the requested arms.
// Caller pads missing buckets.
func QueryTokensSparkline(ctx context.Context, db sqlQueryer, client Client, w TimeWindow, grain string, start, end time.Time) ([]periodBucket, error) {
	acc := map[time.Time]int64{}
	if first, last, ok := archiveBounds(start, end); ok {
		for _, source := range archiveClients(client) {
			q := fmt.Sprintf(`SELECT CAST(%s AS DATE), COALESCE(SUM(tokens_total), 0) FROM archive.usage_hourly WHERE client=? AND bucket_start>=? AND bucket_start<? GROUP BY 1`, localGrainExpr(w, "bucket_start", grain))
			if err := runBucketQuery(ctx, db, q, acc, "archive tokens sparkline", source, first, last); err != nil {
				return nil, err
			}
		}
	}
	if client.includesClaude() {
		q := fmt.Sprintf(`
			SELECT CAST(%s AS DATE) AS bucket, SUM(value) AS total
			FROM metric_token_usage
			WHERE ts >= ? AND ts < ?
			GROUP BY 1 ORDER BY 1
		`, localGrainExpr(w, "ts", grain))
		if err := runBucketQuery(ctx, db, q, acc, "tokens sparkline (claude)", start, end); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		q := fmt.Sprintf(`
			SELECT CAST(%s AS DATE) AS bucket,
			       SUM(COALESCE(input_token_count, 0) + COALESCE(output_token_count, 0)) AS total
			FROM codex_event_token_usage
			WHERE ts >= ? AND ts < ?
			GROUP BY 1 ORDER BY 1
		`, localGrainExpr(w, "ts", grain))
		if err := runBucketQuery(ctx, db, q, acc, "tokens sparkline (codex)", start, end); err != nil {
			return nil, err
		}
	}
	return bucketsFromMap(acc), nil
}

type periodCostBucket struct {
	Bucket time.Time
	Cost   float64
}

// QueryCostSparkline — bucketed cost. Claude authoritative + codex estimated,
// merged per bucket.
func QueryCostSparkline(ctx context.Context, db sqlQueryer, client Client, w TimeWindow, grain string, start, end time.Time) ([]periodCostBucket, error) {
	byBucket := map[time.Time]float64{}
	if first, last, ok := archiveBounds(start, end); ok {
		for _, source := range archiveClients(client) {
			q := fmt.Sprintf(`SELECT CAST(%s AS DATE), COALESCE(SUM(cost_usd), 0) FROM archive.usage_hourly WHERE client=? AND bucket_start>=? AND bucket_start<? GROUP BY 1`, localGrainExpr(w, "bucket_start", grain))
			rows, err := db.QueryContext(ctx, q, source, first, last)
			if err != nil {
				return nil, fmt.Errorf("query archive cost sparkline (%s): %w", source, err)
			}
			for rows.Next() {
				var b periodCostBucket
				if err := rows.Scan(&b.Bucket, &b.Cost); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("scan archive cost sparkline: %w", err)
				}
				byBucket[b.Bucket.UTC()] += b.Cost
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("iterate archive cost sparkline: %w", err)
			}
			if err := rows.Close(); err != nil {
				return nil, fmt.Errorf("close archive cost sparkline: %w", err)
			}
		}
	}
	add := func(table, valueExpr string) error {
		q := fmt.Sprintf(`
			SELECT CAST(%s AS DATE) AS bucket, SUM(%s) AS cost
			FROM %s
			WHERE ts >= ? AND ts < ?
			GROUP BY 1
		`, localGrainExpr(w, "ts", grain), valueExpr, table)
		rows, err := db.QueryContext(ctx, q, start, end)
		if err != nil {
			return fmt.Errorf("query cost sparkline (%s): %w", table, err)
		}
		defer rows.Close()
		for rows.Next() {
			var b periodCostBucket
			if err := rows.Scan(&b.Bucket, &b.Cost); err != nil {
				return fmt.Errorf("scan cost sparkline (%s): %w", table, err)
			}
			byBucket[b.Bucket] += b.Cost
		}
		return rows.Err()
	}
	if client.includesClaude() {
		if err := add("metric_cost_usage", "value"); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		if err := add("codex_event_token_usage", "COALESCE(cost_usd, 0)"); err != nil {
			return nil, err
		}
	}
	out := make([]periodCostBucket, 0, len(byBucket))
	for b, c := range byBucket {
		out = append(out, periodCostBucket{Bucket: b, Cost: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket.Before(out[j].Bucket) })
	return out, nil
}

// QueryRequestsSparkline — bucketed request counts (codex = completed rows).
// Reuses periodBucket so the caller can pad with fillTokensSparkline.
func QueryRequestsSparkline(ctx context.Context, db sqlQueryer, client Client, w TimeWindow, grain string, start, end time.Time) ([]periodBucket, error) {
	acc := map[time.Time]int64{}
	if first, last, ok := archiveBounds(start, end); ok {
		for _, source := range archiveClients(client) {
			q := fmt.Sprintf(`SELECT CAST(%s AS DATE), COALESCE(SUM(request_count), 0) FROM archive.usage_hourly WHERE client=? AND bucket_start>=? AND bucket_start<? GROUP BY 1`, localGrainExpr(w, "bucket_start", grain))
			if err := runBucketQuery(ctx, db, q, acc, "archive requests sparkline", source, first, last); err != nil {
				return nil, err
			}
		}
	}
	if client.includesClaude() {
		q := fmt.Sprintf(`
			SELECT CAST(%s AS DATE) AS bucket, COUNT(*) AS total
			FROM event_api_request
			WHERE ts >= ? AND ts < ?
			GROUP BY 1 ORDER BY 1
		`, localGrainExpr(w, "ts", grain))
		if err := runBucketQuery(ctx, db, q, acc, "requests sparkline (claude)", start, end); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		q := fmt.Sprintf(`
			SELECT CAST(%s AS DATE) AS bucket, COUNT(*) AS total
			FROM codex_event_token_usage
			WHERE ts >= ? AND ts < ?
			GROUP BY 1 ORDER BY 1
		`, localGrainExpr(w, "ts", grain))
		if err := runBucketQuery(ctx, db, q, acc, "requests sparkline (codex)", start, end); err != nil {
			return nil, err
		}
	}
	return bucketsFromMap(acc), nil
}

// ─────────────────────────────────────────────────────────────────────
// Model breakdown (3 sub-queries joined in Go — all-time)
//
// Queries return rows keyed by the raw `model` column. The dashboard layer
// (Classifier) then folds raw names into user-facing groups. This keeps the
// classification logic in Go where it can be configured at runtime.
// ─────────────────────────────────────────────────────────────────────

type modelTokens struct {
	Model                  string
	Client                 Client
	TokenRows              int64
	FromArchive            bool
	TokensIn               int64
	TokensOut              int64
	CacheTokens            int64
	ReasoningTokens        int64
	InputCost              float64
	OutputCost             float64
	CacheReadCost          float64
	InputCostAvailable     bool
	OutputCostAvailable    bool
	CacheReadCostAvailable bool
}

func QueryModelTokens(ctx context.Context, db sqlQueryer, client Client) ([]modelTokens, error) {
	scanInto := func(q, label string, source Client, out []modelTokens) ([]modelTokens, error) {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", label, err)
		}
		defer rows.Close()
		for rows.Next() {
			var r modelTokens
			if err := rows.Scan(
				&r.Model,
				&r.TokensIn,
				&r.TokensOut,
				&r.CacheTokens,
				&r.ReasoningTokens,
			); err != nil {
				return nil, fmt.Errorf("scan %s: %w", label, err)
			}
			out = append(out, r)
			out[len(out)-1].TokenRows = 1
			out[len(out)-1].Client = source
		}
		return out, rows.Err()
	}

	var out []modelTokens
	var err error
	for _, source := range archiveClients(client) {
		const q = `SELECT model, COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(reasoning_tokens), 0), COALESCE(SUM(token_rows), 0) FROM archive.usage_hourly WHERE client=? AND model<>'' AND token_rows>0 GROUP BY model`
		rows, queryErr := db.QueryContext(ctx, q, source)
		if queryErr != nil {
			return nil, fmt.Errorf("query archive model tokens (%s): %w", source, queryErr)
		}
		for rows.Next() {
			var r modelTokens
			if err := rows.Scan(&r.Model, &r.TokensIn, &r.TokensOut, &r.CacheTokens, &r.ReasoningTokens, &r.TokenRows); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan archive model tokens (%s): %w", source, err)
			}
			if source == ClientCodex {
				r.TokensIn -= r.CacheTokens
			}
			r.FromArchive = true
			r.Client = source
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate archive model tokens (%s): %w", source, err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close archive model tokens (%s): %w", source, err)
		}
	}
	if client.includesClaude() {
		const q = `
			SELECT
			  model,
			  COALESCE(SUM(CASE WHEN type='input'     THEN value END), 0) AS tokens_in,
			  COALESCE(SUM(CASE WHEN type='output'    THEN value END), 0) AS tokens_out,
			  COALESCE(SUM(CASE WHEN type='cacheRead' THEN value END), 0) AS cache_tokens,
			  0 AS reasoning_tokens
			FROM metric_token_usage
			WHERE model IS NOT NULL
			GROUP BY model
		`
		if out, err = scanInto(q, "model tokens (claude)", ClientClaude, out); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		// Same projection rule as QueryPeriodTokens: in excludes the cached
		// subset so in+out+cache equals the codex total exactly.
		const q = `
			SELECT model,
			  COALESCE(SUM(COALESCE(input_token_count, 0) - COALESCE(cached_token_count, 0)), 0) AS tokens_in,
			  COALESCE(SUM(COALESCE(output_token_count, 0)), 0)                                   AS tokens_out,
			  COALESCE(SUM(COALESCE(cached_token_count, 0)), 0)                                   AS cache_tokens,
			  COALESCE(SUM(COALESCE(reasoning_token_count, 0)), 0)                                AS reasoning_tokens
			FROM codex_event_token_usage
			WHERE model IS NOT NULL
			GROUP BY model
		`
		if out, err = scanInto(q, "model tokens (codex)", ClientCodex, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type modelCost struct {
	Model string
	Cost  float64
}

// QueryModelCost — per-model all-time cost. Claude authoritative + codex
// estimated, summed per model name.
func QueryModelCost(ctx context.Context, db sqlQueryer, client Client) ([]modelCost, error) {
	byModel := map[string]float64{}
	for _, source := range archiveClients(client) {
		rows, err := db.QueryContext(ctx, `SELECT model, COALESCE(SUM(cost_usd), 0) FROM archive.usage_hourly WHERE client=? AND model<>'' GROUP BY model`, source)
		if err != nil {
			return nil, fmt.Errorf("query archive model cost (%s): %w", source, err)
		}
		for rows.Next() {
			var m modelCost
			if err := rows.Scan(&m.Model, &m.Cost); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan archive model cost: %w", err)
			}
			byModel[m.Model] += m.Cost
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate archive model cost: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close archive model cost: %w", err)
		}
	}
	add := func(table, valueExpr string) error {
		q := fmt.Sprintf(`SELECT model, SUM(%s) AS cost FROM %s WHERE model IS NOT NULL GROUP BY model`, valueExpr, table)
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return fmt.Errorf("query model cost (%s): %w", table, err)
		}
		defer rows.Close()
		for rows.Next() {
			var m modelCost
			if err := rows.Scan(&m.Model, &m.Cost); err != nil {
				return fmt.Errorf("scan model cost (%s): %w", table, err)
			}
			byModel[m.Model] += m.Cost
		}
		return rows.Err()
	}
	if client.includesClaude() {
		if err := add("metric_cost_usage", "value"); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		if err := add("codex_event_token_usage", "COALESCE(cost_usd, 0)"); err != nil {
			return nil, err
		}
	}
	out := make([]modelCost, 0, len(byModel))
	for m, c := range byModel {
		out = append(out, modelCost{Model: m, Cost: c})
	}
	return out, nil
}

type modelRequests struct {
	Model    string
	Requests int64
}

func QueryModelRequests(ctx context.Context, db sqlQueryer, client Client) ([]modelRequests, error) {
	scanInto := func(q, label string, out []modelRequests) ([]modelRequests, error) {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", label, err)
		}
		defer rows.Close()
		for rows.Next() {
			var r modelRequests
			if err := rows.Scan(&r.Model, &r.Requests); err != nil {
				return nil, fmt.Errorf("scan %s: %w", label, err)
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}

	var out []modelRequests
	var err error
	for _, source := range archiveClients(client) {
		rows, queryErr := db.QueryContext(ctx, `SELECT model, COALESCE(SUM(request_count), 0) FROM archive.usage_hourly WHERE client=? AND model<>'' GROUP BY model`, source)
		if queryErr != nil {
			return nil, fmt.Errorf("query archive model requests (%s): %w", source, queryErr)
		}
		for rows.Next() {
			var r modelRequests
			if err := rows.Scan(&r.Model, &r.Requests); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan archive model requests: %w", err)
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate archive model requests: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close archive model requests: %w", err)
		}
	}
	if client.includesClaude() {
		const q = `
			SELECT model, COUNT(*) AS requests
			FROM event_api_request
			WHERE model IS NOT NULL
			GROUP BY model
		`
		if out, err = scanInto(q, "model requests (claude)", out); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		const q = `
			SELECT model, COUNT(*) AS requests
			FROM codex_event_token_usage
			WHERE model IS NOT NULL
			GROUP BY model
		`
		if out, err = scanInto(q, "model requests (codex)", out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────
// Trends
// ─────────────────────────────────────────────────────────────────────

type trendRow struct {
	Bucket time.Time
	Model  string
	Tokens int64
}

// QueryTrends — stacked-area data for /api/usage/trends.
// Returns one row per (bucket, raw model) across the requested arms; the
// dashboard layer folds rows into groups via the Classifier (BuildTrends
// accumulates, so duplicate (bucket, model) pairs across arms are safe).
func QueryTrends(ctx context.Context, db sqlQueryer, client Client, w TimeWindow, grain string, windowStart time.Time) ([]trendRow, error) {
	scanInto := func(q, label string, out []trendRow) ([]trendRow, error) {
		rows, err := db.QueryContext(ctx, q, windowStart)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", label, err)
		}
		defer rows.Close()
		for rows.Next() {
			var r trendRow
			if err := rows.Scan(&r.Bucket, &r.Model, &r.Tokens); err != nil {
				return nil, fmt.Errorf("scan %s: %w", label, err)
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}

	var out []trendRow
	var err error
	for _, source := range archiveClients(client) {
		q := fmt.Sprintf(`SELECT CAST(%s AS DATE), model, COALESCE(SUM(tokens_total), 0) FROM archive.usage_hourly WHERE client=? AND bucket_start>=? AND model<>'' AND token_rows>0 GROUP BY 1,2 ORDER BY 1`, localGrainExpr(w, "bucket_start", grain))
		rows, queryErr := db.QueryContext(ctx, q, source, windowStart)
		if queryErr != nil {
			return nil, fmt.Errorf("query archive trends (%s): %w", source, queryErr)
		}
		for rows.Next() {
			var r trendRow
			if err := rows.Scan(&r.Bucket, &r.Model, &r.Tokens); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan archive trends: %w", err)
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate archive trends: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close archive trends: %w", err)
		}
	}
	if client.includesClaude() {
		q := fmt.Sprintf(`
			SELECT CAST(%s AS DATE) AS bucket_sh, model, SUM(value) AS tokens
			FROM metric_token_usage
			WHERE ts >= ? AND model IS NOT NULL
			GROUP BY 1, 2 ORDER BY 1
		`, localGrainExpr(w, "ts", grain))
		if out, err = scanInto(q, "trends (claude)", out); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		q := fmt.Sprintf(`
			SELECT CAST(%s AS DATE) AS bucket_sh, model,
			       SUM(COALESCE(input_token_count, 0) + COALESCE(output_token_count, 0)) AS tokens
			FROM codex_event_token_usage
			WHERE ts >= ? AND model IS NOT NULL
			GROUP BY 1, 2 ORDER BY 1
		`, localGrainExpr(w, "ts", grain))
		if out, err = scanInto(q, "trends (codex)", out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────
// Rankings
// ─────────────────────────────────────────────────────────────────────

// QueryToolsRanking — Top N tools by call count.
// opts.SinceStart zero ⇒ all-time (predicate elided via `IS NULL OR ts >= ?`).
func QueryToolsRanking(ctx context.Context, db sqlQueryer, opts RankingsOpts) ([]ToolRank, error) {
	if opts.Client == "" {
		opts.Client = ClientAll
	}
	if opts.Client != "" && opts.Client != ClientAll && opts.Client != ClientClaude && opts.Client != ClientCodex {
		return nil, fmt.Errorf("query tools ranking: invalid client %q", opts.Client)
	}
	counts := make(map[string]int64)
	add := func(q string, args ...any) error {
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name string
			var count int64
			if err := rows.Scan(&name, &count); err != nil {
				_ = rows.Close()
				return err
			}
			counts[name] += count
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		return rows.Close()
	}
	for _, source := range archiveClients(opts.Client) {
		if err := add(`SELECT tool_name, COALESCE(SUM(call_count), 0) FROM archive.tool_hourly WHERE client=? AND (? IS NULL OR bucket_start>=?) GROUP BY tool_name`, source, nullableTime(opts.SinceStart), nullableTime(opts.SinceStart)); err != nil {
			return nil, fmt.Errorf("query archive tools ranking: %w", err)
		}
	}
	if opts.Client.includesClaude() {
		if err := add(`SELECT tool_name, COUNT(*) FROM event_tool_result WHERE tool_name IS NOT NULL AND (? IS NULL OR ts>=?) GROUP BY tool_name`, nullableTime(opts.SinceStart), nullableTime(opts.SinceStart)); err != nil {
			return nil, fmt.Errorf("query tools ranking (claude): %w", err)
		}
	}
	if opts.Client.includesCodex() {
		if err := add(`SELECT tool_name, COUNT(*) FROM codex_event_tool_result WHERE tool_name IS NOT NULL AND (? IS NULL OR ts>=?) GROUP BY tool_name`, nullableTime(opts.SinceStart), nullableTime(opts.SinceStart)); err != nil {
			return nil, fmt.Errorf("query tools ranking (codex): %w", err)
		}
	}
	out := make([]ToolRank, 0, len(counts))
	for name, count := range counts {
		out = append(out, ToolRank{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if opts.ToolsTopN > 0 && len(out) > opts.ToolsTopN {
		out = out[:opts.ToolsTopN]
	}
	return out, nil
}

// QuerySkillsRanking — Top N skills by activation count.
func QuerySkillsRanking(ctx context.Context, db sqlQueryer, opts RankingsOpts) ([]SkillRank, error) {
	if opts.Client == "" {
		opts.Client = ClientAll
	}
	if opts.Client != "" && opts.Client != ClientAll && opts.Client != ClientClaude && opts.Client != ClientCodex {
		return nil, fmt.Errorf("query skills ranking: invalid client %q", opts.Client)
	}
	counts := make(map[string]int64)
	add := func(q string, args ...any) error {
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name string
			var count int64
			if err := rows.Scan(&name, &count); err != nil {
				_ = rows.Close()
				return err
			}
			counts[name] += count
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		return rows.Close()
	}
	for _, source := range archiveClients(opts.Client) {
		if err := add(`SELECT skill_name, COALESCE(SUM(activation_count), 0) FROM archive.skill_hourly WHERE client=? AND (? IS NULL OR bucket_start>=?) GROUP BY skill_name`, source, nullableTime(opts.SinceStart), nullableTime(opts.SinceStart)); err != nil {
			return nil, fmt.Errorf("query archive skills ranking: %w", err)
		}
	}
	if opts.Client.includesClaude() {
		if err := add(`SELECT skill_name, COUNT(*) FROM event_skill_activated WHERE skill_name IS NOT NULL AND (? IS NULL OR ts>=?) GROUP BY skill_name`, nullableTime(opts.SinceStart), nullableTime(opts.SinceStart)); err != nil {
			return nil, fmt.Errorf("query skills ranking (claude): %w", err)
		}
	}
	if opts.Client.includesCodex() {
		if err := add(`SELECT skill, COALESCE(SUM(value), 0) FROM codex_metric_skill_injected WHERE skill IS NOT NULL AND LOWER(status) IN ('ok','success') AND (? IS NULL OR ts>=?) GROUP BY skill`, nullableTime(opts.SinceStart), nullableTime(opts.SinceStart)); err != nil {
			return nil, fmt.Errorf("query skills ranking (codex): %w", err)
		}
	}
	out := make([]SkillRank, 0, len(counts))
	for name, count := range counts {
		out = append(out, SkillRank{Name: name, Activations: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Activations != out[j].Activations {
			return out[i].Activations > out[j].Activations
		}
		return out[i].Name < out[j].Name
	})
	if opts.SkillsTopN > 0 && len(out) > opts.SkillsTopN {
		out = out[:opts.SkillsTopN]
	}
	return out, nil
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// ─────────────────────────────────────────────────────────────────────
// Sessions
//
// The "activity union" below is the set of tables whose `ts` defines
// session activity for first/last-seen and the recent-sessions ordering. We
// union the high-signal tables (every API turn, every tool result, every
// skill activation, every prompt, plus the token metric). metric_session_count
// is intentionally excluded — a session may have several start rows, and we
// want activity recency, not session-start recency.
// ─────────────────────────────────────────────────────────────────────

// QuerySessionTimespan returns the first/last activity instants for one
// session. Both are invalid (NULL) when the session id is unknown — callers
// treat that as 404.
func QuerySessionTimespan(ctx context.Context, db sqlQueryer, sessionID string) (first, last sql.NullTime, err error) {
	if err = db.QueryRowContext(ctx, `SELECT first_active, last_active FROM archive.sessions WHERE client='claude' AND session_id=?`, sessionID).Scan(&first, &last); err != nil && err != sql.ErrNoRows {
		return first, last, fmt.Errorf("query archive session timespan: %w", err)
	}
	var rawFirst, rawLast sql.NullTime
	const q = `
		WITH activity AS (
		  SELECT ts FROM event_api_request      WHERE session_id = ?
		  UNION ALL SELECT ts FROM event_tool_result     WHERE session_id = ?
		  UNION ALL SELECT ts FROM event_skill_activated WHERE session_id = ?
		  UNION ALL SELECT ts FROM event_user_prompt     WHERE session_id = ?
		  UNION ALL SELECT ts FROM metric_token_usage    WHERE session_id = ?
		)
		SELECT MIN(ts), MAX(ts) FROM activity
	`
	row := db.QueryRowContext(ctx, q, sessionID, sessionID, sessionID, sessionID, sessionID)
	if err = row.Scan(&rawFirst, &rawLast); err != nil {
		return first, last, fmt.Errorf("query session timespan: %w", err)
	}
	if rawFirst.Valid && (!first.Valid || rawFirst.Time.Before(first.Time)) {
		first = rawFirst
	}
	if rawLast.Valid && (!last.Valid || rawLast.Time.After(last.Time)) {
		last = rawLast
	}
	return first, last, nil
}

// QuerySessionTokens returns total tokens (all types) for one session.
func QuerySessionTokens(ctx context.Context, db sqlQueryer, sessionID string) (int64, error) {
	const q = `SELECT COALESCE(SUM(value), 0) FROM metric_token_usage WHERE session_id = ?`
	var v int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(tokens_total, 0) FROM archive.sessions WHERE client='claude' AND session_id=?`, sessionID).Scan(&v); err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("query archive session tokens: %w", err)
	}
	var raw int64
	if err := db.QueryRowContext(ctx, q, sessionID).Scan(&raw); err != nil {
		return 0, fmt.Errorf("query session tokens: %w", err)
	}
	return v + raw, nil
}

// QuerySessionRequests returns the API-request count for one session.
func QuerySessionRequests(ctx context.Context, db sqlQueryer, sessionID string) (int64, error) {
	const q = `SELECT COUNT(*) FROM event_api_request WHERE session_id = ?`
	var v int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(request_count, 0) FROM archive.sessions WHERE client='claude' AND session_id=?`, sessionID).Scan(&v); err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("query archive session requests: %w", err)
	}
	var raw int64
	if err := db.QueryRowContext(ctx, q, sessionID).Scan(&raw); err != nil {
		return 0, fmt.Errorf("query session requests: %w", err)
	}
	return v + raw, nil
}

// QuerySessionToolBreakdown returns every tool's call count for one session,
// ordered by count desc. The builder folds the tail past Top-N into "其他".
func QuerySessionToolBreakdown(ctx context.Context, db sqlQueryer, sessionID string) ([]ToolRank, error) {
	counts := map[string]int64{}
	archiveRows, err := db.QueryContext(ctx, `SELECT tool_name, call_count FROM archive.session_tools WHERE client='claude' AND session_id=?`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query archive session tools: %w", err)
	}
	for archiveRows.Next() {
		var name string
		var count int64
		if err := archiveRows.Scan(&name, &count); err != nil {
			_ = archiveRows.Close()
			return nil, fmt.Errorf("scan archive session tool: %w", err)
		}
		counts[name] += count
	}
	if err := archiveRows.Err(); err != nil {
		_ = archiveRows.Close()
		return nil, fmt.Errorf("iterate archive session tools: %w", err)
	}
	if err := archiveRows.Close(); err != nil {
		return nil, fmt.Errorf("close archive session tools: %w", err)
	}
	const q = `
		SELECT tool_name AS name, COUNT(*) AS count
		FROM event_tool_result
		WHERE session_id = ? AND tool_name IS NOT NULL
		GROUP BY tool_name
		ORDER BY count DESC, name
	`
	rows, err := db.QueryContext(ctx, q, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session tool breakdown: %w", err)
	}
	defer rows.Close()

	var out []ToolRank
	for rows.Next() {
		var r ToolRank
		if err := rows.Scan(&r.Name, &r.Count); err != nil {
			return nil, fmt.Errorf("scan session tool: %w", err)
		}
		counts[r.Name] += r.Count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for name, count := range counts {
		out = append(out, ToolRank{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// QuerySessionSkillBreakdown returns every skill's activation count for one
// session, ordered by count desc.
func QuerySessionSkillBreakdown(ctx context.Context, db sqlQueryer, sessionID string) ([]SkillRank, error) {
	counts := map[string]int64{}
	archiveRows, err := db.QueryContext(ctx, `SELECT skill_name, activation_count FROM archive.session_skills WHERE client='claude' AND session_id=?`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query archive session skills: %w", err)
	}
	for archiveRows.Next() {
		var name string
		var count int64
		if err := archiveRows.Scan(&name, &count); err != nil {
			_ = archiveRows.Close()
			return nil, fmt.Errorf("scan archive session skill: %w", err)
		}
		counts[name] += count
	}
	if err := archiveRows.Err(); err != nil {
		_ = archiveRows.Close()
		return nil, fmt.Errorf("iterate archive session skills: %w", err)
	}
	if err := archiveRows.Close(); err != nil {
		return nil, fmt.Errorf("close archive session skills: %w", err)
	}
	const q = `
		SELECT skill_name AS name, COUNT(*) AS activations
		FROM event_skill_activated
		WHERE session_id = ? AND skill_name IS NOT NULL
		GROUP BY skill_name
		ORDER BY activations DESC, name
	`
	rows, err := db.QueryContext(ctx, q, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session skill breakdown: %w", err)
	}
	defer rows.Close()

	var out []SkillRank
	for rows.Next() {
		var r SkillRank
		if err := rows.Scan(&r.Name, &r.Activations); err != nil {
			return nil, fmt.Errorf("scan session skill: %w", err)
		}
		counts[r.Name] += r.Activations
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for name, count := range counts {
		out = append(out, SkillRank{Name: name, Activations: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Activations != out[j].Activations {
			return out[i].Activations > out[j].Activations
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// sessionListRow is the raw per-session aggregate scanned from QuerySessionList;
// BuildSessionList formats the timestamps into the wire SessionSummary.
type sessionListRow struct {
	SessionID string
	Client    string
	FirstTs   time.Time
	LastTs    time.Time
	Tokens    int64
	Requests  int64
	ToolCalls int64
	Skills    int64
	Cost      float64 // claude authoritative (metric_cost_usage) or codex estimated (cost_usd)
}

const claudeActivityArms = `
	  SELECT session_id, ts, 'claude' AS client FROM event_api_request      WHERE session_id IS NOT NULL
	  UNION ALL SELECT session_id, ts, 'claude' FROM event_tool_result     WHERE session_id IS NOT NULL
	  UNION ALL SELECT session_id, ts, 'claude' FROM event_skill_activated WHERE session_id IS NOT NULL
	  UNION ALL SELECT session_id, ts, 'claude' FROM event_user_prompt     WHERE session_id IS NOT NULL
	  UNION ALL SELECT session_id, ts, 'claude' FROM metric_token_usage    WHERE session_id IS NOT NULL`

const codexActivityArms = `
	  SELECT conversation_id, ts, 'codex' AS client FROM codex_event_token_usage         WHERE conversation_id IS NOT NULL
	  UNION ALL SELECT conversation_id, ts, 'codex' FROM codex_event_user_prompt         WHERE conversation_id IS NOT NULL
	  UNION ALL SELECT conversation_id, ts, 'codex' FROM codex_event_tool_result         WHERE conversation_id IS NOT NULL
	  UNION ALL SELECT conversation_id, ts, 'codex' FROM codex_event_conversation_starts WHERE conversation_id IS NOT NULL`

// QuerySessionList returns the `limit` most-recently-active sessions across
// the requested arms, ordered by last activity desc, each with all-time
// aggregate counts. Column 1 of every activity arm binds to session_id by
// position, so codex conversations surface their conversation_id there. The
// correlated subqueries run only for the `limit` surviving rows.
func QuerySessionList(ctx context.Context, db sqlQueryer, client Client, limit int) ([]sessionListRow, error) {
	var arms string
	switch client {
	case ClientClaude:
		arms = claudeActivityArms
	case ClientCodex:
		arms = codexActivityArms
	default:
		arms = claudeActivityArms + "\n	  UNION ALL " + codexActivityArms
	}
	if limit <= 0 {
		return []sessionListRow{}, nil
	}
	return querySessionListOptimized(ctx, db, client, limit, arms)
}

// ─────────────────────────────────────────────────────────────────────
// Codex sessions (conversation_id keyed)
// ─────────────────────────────────────────────────────────────────────

// QueryCodexSessionTimespan mirrors QuerySessionTimespan for one codex
// conversation. Both NULL ⇒ unknown conversation.
func QueryCodexSessionTimespan(ctx context.Context, db sqlQueryer, conversationID string) (first, last sql.NullTime, err error) {
	if err = db.QueryRowContext(ctx, `SELECT first_active, last_active FROM archive.sessions WHERE client='codex' AND session_id=?`, conversationID).Scan(&first, &last); err != nil && err != sql.ErrNoRows {
		return first, last, fmt.Errorf("query archive codex session timespan: %w", err)
	}
	var rawFirst, rawLast sql.NullTime
	const q = `
		WITH activity AS (
		  SELECT ts FROM codex_event_token_usage         WHERE conversation_id = ?
		  UNION ALL SELECT ts FROM codex_event_user_prompt         WHERE conversation_id = ?
		  UNION ALL SELECT ts FROM codex_event_tool_result         WHERE conversation_id = ?
		  UNION ALL SELECT ts FROM codex_event_conversation_starts WHERE conversation_id = ?
		)
		SELECT MIN(ts), MAX(ts) FROM activity
	`
	row := db.QueryRowContext(ctx, q, conversationID, conversationID, conversationID, conversationID)
	if err = row.Scan(&rawFirst, &rawLast); err != nil {
		return first, last, fmt.Errorf("query codex session timespan: %w", err)
	}
	if rawFirst.Valid && (!first.Valid || rawFirst.Time.Before(first.Time)) {
		first = rawFirst
	}
	if rawLast.Valid && (!last.Valid || rawLast.Time.After(last.Time)) {
		last = rawLast
	}
	return first, last, nil
}

// QueryCodexSessionTokens returns the merged total plus the four raw
// dimensions for the detail card.
func QueryCodexSessionTokens(ctx context.Context, db sqlQueryer, conversationID string) (total int64, detail SessionTokenDetail, err error) {
	var archiveTotal int64
	if err = db.QueryRowContext(ctx, `SELECT tokens_total, input_tokens, output_tokens, cache_read_tokens, reasoning_tokens FROM archive.sessions WHERE client='codex' AND session_id=?`, conversationID).Scan(&archiveTotal, &detail.Input, &detail.Output, &detail.Cached, &detail.Reasoning); err != nil && err != sql.ErrNoRows {
		return 0, detail, fmt.Errorf("query archive codex session tokens: %w", err)
	}
	var rawDetail SessionTokenDetail
	var rawTotal int64
	const q = `
		SELECT
		  COALESCE(SUM(COALESCE(input_token_count, 0) + COALESCE(output_token_count, 0)), 0),
		  COALESCE(SUM(COALESCE(input_token_count, 0)), 0),
		  COALESCE(SUM(COALESCE(output_token_count, 0)), 0),
		  COALESCE(SUM(COALESCE(cached_token_count, 0)), 0),
		  COALESCE(SUM(COALESCE(reasoning_token_count, 0)), 0)
		FROM codex_event_token_usage
		WHERE conversation_id = ?
	`
	err = db.QueryRowContext(ctx, q, conversationID).Scan(&rawTotal, &rawDetail.Input, &rawDetail.Output, &rawDetail.Cached, &rawDetail.Reasoning)
	if err != nil {
		return 0, detail, fmt.Errorf("query codex session tokens: %w", err)
	}
	detail.Input += rawDetail.Input
	detail.Output += rawDetail.Output
	detail.Cached += rawDetail.Cached
	detail.Reasoning += rawDetail.Reasoning
	return archiveTotal + rawTotal, detail, nil
}

// QueryCodexSessionRequests — completed-response count for one conversation.
func QueryCodexSessionRequests(ctx context.Context, db sqlQueryer, conversationID string) (int64, error) {
	const q = `SELECT COUNT(*) FROM codex_event_token_usage WHERE conversation_id = ?`
	var v int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(request_count, 0) FROM archive.sessions WHERE client='codex' AND session_id=?`, conversationID).Scan(&v); err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("query archive codex session requests: %w", err)
	}
	var raw int64
	if err := db.QueryRowContext(ctx, q, conversationID).Scan(&raw); err != nil {
		return 0, fmt.Errorf("query codex session requests: %w", err)
	}
	return v + raw, nil
}

// QueryCodexSessionToolBreakdown mirrors QuerySessionToolBreakdown.
func QueryCodexSessionToolBreakdown(ctx context.Context, db sqlQueryer, conversationID string) ([]ToolRank, error) {
	counts := map[string]int64{}
	archiveRows, err := db.QueryContext(ctx, `SELECT tool_name, call_count FROM archive.session_tools WHERE client='codex' AND session_id=?`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("query archive codex session tools: %w", err)
	}
	for archiveRows.Next() {
		var name string
		var count int64
		if err := archiveRows.Scan(&name, &count); err != nil {
			_ = archiveRows.Close()
			return nil, fmt.Errorf("scan archive codex session tool: %w", err)
		}
		counts[name] += count
	}
	if err := archiveRows.Err(); err != nil {
		_ = archiveRows.Close()
		return nil, fmt.Errorf("iterate archive codex session tools: %w", err)
	}
	if err := archiveRows.Close(); err != nil {
		return nil, fmt.Errorf("close archive codex session tools: %w", err)
	}
	const q = `
		SELECT tool_name AS name, COUNT(*) AS count
		FROM codex_event_tool_result
		WHERE conversation_id = ? AND tool_name IS NOT NULL
		GROUP BY tool_name
		ORDER BY count DESC, name
	`
	rows, err := db.QueryContext(ctx, q, conversationID)
	if err != nil {
		return nil, fmt.Errorf("query codex session tool breakdown: %w", err)
	}
	defer rows.Close()

	var out []ToolRank
	for rows.Next() {
		var r ToolRank
		if err := rows.Scan(&r.Name, &r.Count); err != nil {
			return nil, fmt.Errorf("scan codex session tool: %w", err)
		}
		counts[r.Name] += r.Count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for name, count := range counts {
		out = append(out, ToolRank{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// QueryClaudeSessionCost returns a claude session's authoritative all-time cost.
func QueryClaudeSessionCost(ctx context.Context, db sqlQueryer, sessionID string) (float64, error) {
	const q = `SELECT COALESCE(SUM(value), 0) FROM metric_cost_usage WHERE session_id = ?`
	var v float64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(cost_usd, 0) FROM archive.sessions WHERE client='claude' AND session_id=?`, sessionID).Scan(&v); err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("query archive claude session cost: %w", err)
	}
	var raw float64
	if err := db.QueryRowContext(ctx, q, sessionID).Scan(&raw); err != nil {
		return 0, fmt.Errorf("query claude session cost: %w", err)
	}
	return v + raw, nil
}

// QueryCodexSessionCost returns a codex conversation's estimated all-time cost.
func QueryCodexSessionCost(ctx context.Context, db sqlQueryer, conversationID string) (float64, error) {
	const q = `SELECT COALESCE(SUM(cost_usd), 0) FROM codex_event_token_usage WHERE conversation_id = ?`
	var v float64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(cost_usd, 0) FROM archive.sessions WHERE client='codex' AND session_id=?`, conversationID).Scan(&v); err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("query archive codex session cost: %w", err)
	}
	var raw float64
	if err := db.QueryRowContext(ctx, q, conversationID).Scan(&raw); err != nil {
		return 0, fmt.Errorf("query codex session cost: %w", err)
	}
	return v + raw, nil
}

// ─────────────────────────────────────────────────────────────────────
// Rates — /api/usage/rates (Claude speed = output tokens/request-second,
// Codex speed = inverse native service TBT, throughput = tokens/wall minute)
//
// SQL groups by date_trunc('hour', ts) (UTC hours == local hours for the
// whole-hour-offset zones we support); the builder merges hour rows into
// 1h/6h/1d buckets via RatesSpec.BucketIndex. Each client arm keeps its own
// ratio components; cross-client KPI values are never combined.
// ─────────────────────────────────────────────────────────────────────

type speedBucketRow struct {
	Hour   time.Time
	Model  string
	Client Client
	Units  float64
	DurMs  float64
}

// QuerySpeedBuckets returns ratio components for [start, end). Claude units
// are output tokens over request duration; Codex units are native service-TBT
// histogram samples over their summed milliseconds (1000 / mean TBT).
func QuerySpeedBuckets(ctx context.Context, db sqlQueryer, client Client, start, end time.Time) ([]speedBucketRow, error) {
	scanInto := func(q, label string, source Client, out []speedBucketRow, args ...any) ([]speedBucketRow, error) {
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", label, err)
		}
		defer rows.Close()
		for rows.Next() {
			var r speedBucketRow
			if err := rows.Scan(&r.Hour, &r.Model, &r.Units, &r.DurMs); err != nil {
				return nil, fmt.Errorf("scan %s: %w", label, err)
			}
			r.Hour = r.Hour.UTC()
			r.Client = source
			out = append(out, r)
		}
		return out, rows.Err()
	}

	var out []speedBucketRow
	var err error
	if first, last, ok := archiveBounds(start, end); ok {
		for _, source := range archiveClients(client) {
			const q = `SELECT bucket_start, model, speed_units, speed_duration_ms FROM archive.usage_hourly WHERE client=? AND bucket_start>=? AND bucket_start<? AND model<>'' AND speed_units>0 AND speed_duration_ms>0`
			if out, err = scanInto(q, "archive speed buckets", source, out, source, first, last); err != nil {
				return nil, err
			}
		}
	}
	if client.includesClaude() {
		const q = `
			SELECT date_trunc('hour', ts) AS h, model,
			       CAST(SUM(output_tokens) AS DOUBLE) AS units,
			       CAST(SUM(duration_ms) AS DOUBLE) AS dur_ms
			FROM event_api_request
			WHERE ts >= ? AND ts < ?
			  AND model IS NOT NULL AND model <> ''
			  AND duration_ms > 0 AND output_tokens > 0
			GROUP BY 1, 2
		`
		if out, err = scanInto(q, "speed buckets (claude)", ClientClaude, out, start, end); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		const q = `
			SELECT date_trunc('hour', ts) AS h, model,
			       CAST(SUM(sample_count) AS DOUBLE) AS units,
			       SUM(sum_ms) AS dur_ms
			FROM codex_metric_response_tbt
			WHERE ts >= ? AND ts < ?
			  AND model IS NOT NULL AND model <> ''
			  AND sample_count > 0 AND sum_ms > 0
			GROUP BY 1, 2
		`
		if out, err = scanInto(q, "speed buckets (codex)", ClientCodex, out, start, end); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type speedWindow struct {
	Units   float64
	DurMs   float64
	Sources int
}

type speedWindowPair struct {
	Current  speedWindow
	Previous speedWindow
}

type speedWindowBounds struct {
	Start time.Time
	Split time.Time
	End   time.Time
}

// QuerySpeedWindowPair returns adjacent speed windows with one bounded scan
// per included telemetry source. start <= ts < split feeds Previous while
// split <= ts < end feeds Current.
func QuerySpeedWindowPair(
	ctx context.Context,
	db sqlQueryer,
	client Client,
	bounds speedWindowBounds,
) (speedWindowPair, error) {
	var out speedWindowPair
	scanSource := func(q, label string) error {
		var currentUnits, currentDur, previousUnits, previousDur float64
		if err := db.QueryRowContext(ctx, q,
			bounds.Split, bounds.Split, bounds.Split, bounds.Split, bounds.Start, bounds.End,
		).Scan(&currentUnits, &currentDur, &previousUnits, &previousDur); err != nil {
			return fmt.Errorf("query realtime speed windows (%s): %w", label, err)
		}
		if currentDur > 0 {
			out.Current.Sources++
		}
		if previousDur > 0 {
			out.Previous.Sources++
		}
		out.Current.Units += currentUnits
		out.Current.DurMs += currentDur
		out.Previous.Units += previousUnits
		out.Previous.DurMs += previousDur
		return nil
	}

	if client.includesClaude() {
		const q = `
			SELECT
			  COALESCE(CAST(SUM(CASE WHEN ts >= ? THEN output_tokens ELSE 0 END) AS DOUBLE), 0),
			  COALESCE(CAST(SUM(CASE WHEN ts >= ? THEN duration_ms ELSE 0 END) AS DOUBLE), 0),
			  COALESCE(CAST(SUM(CASE WHEN ts <  ? THEN output_tokens ELSE 0 END) AS DOUBLE), 0),
			  COALESCE(CAST(SUM(CASE WHEN ts <  ? THEN duration_ms ELSE 0 END) AS DOUBLE), 0)
			FROM event_api_request
			WHERE ts >= ? AND ts < ?
			  AND model IS NOT NULL AND model <> ''
			  AND duration_ms > 0 AND output_tokens > 0
		`
		if err := scanSource(q, "claude"); err != nil {
			return out, err
		}
	}
	if client.includesCodex() {
		const q = `
			SELECT
			  COALESCE(CAST(SUM(CASE WHEN ts >= ? THEN sample_count ELSE 0 END) AS DOUBLE), 0),
			  COALESCE(SUM(CASE WHEN ts >= ? THEN sum_ms ELSE 0 END), 0),
			  COALESCE(CAST(SUM(CASE WHEN ts <  ? THEN sample_count ELSE 0 END) AS DOUBLE), 0),
			  COALESCE(SUM(CASE WHEN ts <  ? THEN sum_ms ELSE 0 END), 0)
			FROM codex_metric_response_tbt
			WHERE ts >= ? AND ts < ?
			  AND model IS NOT NULL AND model <> ''
			  AND sample_count > 0 AND sum_ms > 0
		`
		if err := scanSource(q, "codex"); err != nil {
			return out, err
		}
	}
	return out, nil
}

// QuerySpeedWindow returns whole-window numerator/denominator for the speed
// KPI. Zero DurMs means "no usable requests" — the builder renders null.
func QuerySpeedWindow(ctx context.Context, db sqlQueryer, client Client, start, end time.Time) (speedWindow, error) {
	var r speedWindow
	if client.includesClaude() {
		var archived speedWindow
		if first, last, ok := archiveBounds(start, end); ok {
			if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(speed_units), 0), COALESCE(SUM(speed_duration_ms), 0) FROM archive.usage_hourly WHERE client='claude' AND bucket_start>=? AND bucket_start<? AND speed_units>0 AND speed_duration_ms>0`, first, last).Scan(&archived.Units, &archived.DurMs); err != nil {
				return r, fmt.Errorf("query archive speed window (claude): %w", err)
			}
		}
		const q = `
			SELECT COALESCE(CAST(SUM(output_tokens) AS DOUBLE), 0),
			       COALESCE(CAST(SUM(duration_ms) AS DOUBLE), 0)
			FROM event_api_request
			WHERE ts >= ? AND ts < ?
			  AND model IS NOT NULL AND model <> ''
			  AND duration_ms > 0 AND output_tokens > 0
		`
		var c speedWindow
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&c.Units, &c.DurMs); err != nil {
			return r, fmt.Errorf("query speed window (claude): %w", err)
		}
		c.Units += archived.Units
		c.DurMs += archived.DurMs
		if c.DurMs > 0 {
			r.Sources++
		}
		r.Units += c.Units
		r.DurMs += c.DurMs
	}
	if client.includesCodex() {
		var archived speedWindow
		if first, last, ok := archiveBounds(start, end); ok {
			if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(speed_units), 0), COALESCE(SUM(speed_duration_ms), 0) FROM archive.usage_hourly WHERE client='codex' AND bucket_start>=? AND bucket_start<? AND speed_units>0 AND speed_duration_ms>0`, first, last).Scan(&archived.Units, &archived.DurMs); err != nil {
				return r, fmt.Errorf("query archive speed window (codex): %w", err)
			}
		}
		const q = `
			SELECT COALESCE(CAST(SUM(sample_count) AS DOUBLE), 0),
			       COALESCE(SUM(sum_ms), 0)
			FROM codex_metric_response_tbt
			WHERE ts >= ? AND ts < ?
			  AND model IS NOT NULL AND model <> ''
			  AND sample_count > 0 AND sum_ms > 0
		`
		var c speedWindow
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&c.Units, &c.DurMs); err != nil {
			return r, fmt.Errorf("query speed window (codex): %w", err)
		}
		c.Units += archived.Units
		c.DurMs += archived.DurMs
		if c.DurMs > 0 {
			r.Sources++
		}
		r.Units += c.Units
		r.DurMs += c.DurMs
	}
	return r, nil
}

type throughputBucketRow struct {
	Hour          time.Time
	In            int64
	Out           int64
	CacheRead     int64
	CacheCreation int64
}

// QueryThroughputBuckets returns per-hour token sums split by type.
// Codex projection follows the QueryPeriodTokens precedent so client=all
// stays additive: in = max(input - cached, 0), cacheRead = cached,
// cacheCreation = 0 (subset semantics folded into parallel semantics).
func QueryThroughputBuckets(ctx context.Context, db sqlQueryer, client Client, start, end time.Time) ([]throughputBucketRow, error) {
	scanInto := func(q, label string, out []throughputBucketRow, args ...any) ([]throughputBucketRow, error) {
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", label, err)
		}
		defer rows.Close()
		for rows.Next() {
			var r throughputBucketRow
			if err := rows.Scan(&r.Hour, &r.In, &r.Out, &r.CacheRead, &r.CacheCreation); err != nil {
				return nil, fmt.Errorf("scan %s: %w", label, err)
			}
			r.Hour = r.Hour.UTC()
			out = append(out, r)
		}
		return out, rows.Err()
	}

	var out []throughputBucketRow
	var err error
	if first, last, ok := archiveBounds(start, end); ok {
		for _, source := range archiveClients(client) {
			const q = `SELECT bucket_start, CASE WHEN client='codex' THEN throughput_input_tokens ELSE input_tokens END, output_tokens, cache_read_tokens, cache_creation_tokens FROM archive.usage_hourly WHERE client=? AND bucket_start>=? AND bucket_start<?`
			if out, err = scanInto(q, "archive throughput buckets", out, source, first, last); err != nil {
				return nil, err
			}
		}
	}
	if client.includesClaude() {
		const q = `
			SELECT date_trunc('hour', ts) AS h,
			  COALESCE(SUM(CASE WHEN type='input'         THEN value END), 0),
			  COALESCE(SUM(CASE WHEN type='output'        THEN value END), 0),
			  COALESCE(SUM(CASE WHEN type='cacheRead'     THEN value END), 0),
			  COALESCE(SUM(CASE WHEN type='cacheCreation' THEN value END), 0)
			FROM metric_token_usage
			WHERE ts >= ? AND ts < ?
			GROUP BY 1
		`
		if out, err = scanInto(q, "throughput buckets (claude)", out, start, end); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		const q = `
			SELECT date_trunc('hour', ts) AS h,
			  COALESCE(SUM(GREATEST(COALESCE(input_token_count, 0) - COALESCE(cached_token_count, 0), 0)), 0),
			  COALESCE(SUM(COALESCE(output_token_count, 0)), 0),
			  COALESCE(SUM(COALESCE(cached_token_count, 0)), 0),
			  0
			FROM codex_event_token_usage
			WHERE ts >= ? AND ts < ?
			GROUP BY 1
		`
		if out, err = scanInto(q, "throughput buckets (codex)", out, start, end); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type seenModelRow struct {
	Model    string
	LastSeen time.Time
	Requests int64
	Client   string // "claude" | "codex"
}

// QuerySeenModels lists distinct raw models seen in the data, per arm.
// Claude coverage unions event_api_request (real request counts) with
// metric_token_usage (coverage only — Requests=0 so counts are not doubled).
// Placeholder pseudo-models like "<synthetic>" are filtered out. The builder
// merges rows per model across arms.
func QuerySeenModels(ctx context.Context, db sqlQueryer, client Client) ([]seenModelRow, error) {
	scanInto := func(q, label, arm string, out []seenModelRow) ([]seenModelRow, error) {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", label, err)
		}
		defer rows.Close()
		for rows.Next() {
			var r seenModelRow
			if err := rows.Scan(&r.Model, &r.LastSeen, &r.Requests); err != nil {
				return nil, fmt.Errorf("scan %s: %w", label, err)
			}
			r.LastSeen = r.LastSeen.UTC()
			r.Client = arm
			out = append(out, r)
		}
		return out, rows.Err()
	}

	var out []seenModelRow
	var err error
	for _, source := range archiveClients(client) {
		const q = `SELECT model, MAX(model_last_seen), COALESCE(SUM(request_count), 0) FROM archive.usage_hourly WHERE client=? AND model<>'' AND model NOT LIKE '<%' AND model_last_seen IS NOT NULL GROUP BY model`
		rows, queryErr := db.QueryContext(ctx, q, source)
		if queryErr != nil {
			return nil, fmt.Errorf("query archive seen models: %w", queryErr)
		}
		for rows.Next() {
			var r seenModelRow
			if err := rows.Scan(&r.Model, &r.LastSeen, &r.Requests); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan archive seen models: %w", err)
			}
			r.LastSeen = r.LastSeen.UTC()
			r.Client = string(source)
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate archive seen models: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close archive seen models: %w", err)
		}
	}
	if client.includesClaude() {
		const qReq = `
			SELECT model, MAX(ts), COUNT(*)
			FROM event_api_request
			WHERE model IS NOT NULL AND model <> '' AND model NOT LIKE '<%'
			GROUP BY model
		`
		if out, err = scanInto(qReq, "seen models (claude api_request)", "claude", out); err != nil {
			return nil, err
		}
		const qMetric = `
			SELECT model, MAX(ts), 0
			FROM metric_token_usage
			WHERE model IS NOT NULL AND model <> '' AND model NOT LIKE '<%'
			GROUP BY model
		`
		if out, err = scanInto(qMetric, "seen models (claude metric)", "claude", out); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		const q = `
			SELECT model, MAX(ts), COUNT(*)
			FROM codex_event_token_usage
			WHERE model IS NOT NULL AND model <> '' AND model NOT LIKE '<%'
			GROUP BY model
		`
		if out, err = scanInto(q, "seen models (codex)", "codex", out); err != nil {
			return nil, err
		}
	}
	return out, nil
}
