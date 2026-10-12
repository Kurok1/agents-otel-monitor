/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CostMetadata reports observed availability, independent of today's local
// pricing switch. Native turn costs are estimates with unknown coverage.
type CostMetadata struct {
	Available       bool     `json:"cost_available"`
	Source          string   `json:"cost_source"`
	Coverage        string   `json:"cost_coverage"`
	NativeTurnCount int64    `json:"native_turn_count"`
	Legacy          bool     `json:"legacy_cost_included"`
	LocalEstimate   *float64 `json:"local_estimate_usd"`
}

type codexCostFilter struct {
	Start, End     time.Time
	ConversationID string
}

type codexCostResult struct {
	Total float64
	CostMetadata
}

func queryCostMetadata(ctx context.Context, db sqlQueryer, client Client, filter codexCostFilter) (CostMetadata, error) {
	m := CostMetadata{Source: "unknown", Coverage: "unknown"}
	if client.includesCodex() {
		result, err := queryCodexCost(ctx, db, filter)
		if err != nil {
			return m, err
		}
		m = result.CostMetadata
	}
	if client.includesClaude() {
		// In the combined view, an absent Claude stream must not turn an
		// unknown Codex amount into an apparently observed zero or mixed source.
		if client == ClientAll {
			var observed bool
			conditions := []string{"TRUE"}
			args := make([]any, 0, 2)
			if !filter.Start.IsZero() {
				conditions = append(conditions, "ts >= ?")
				args = append(args, filter.Start)
			}
			if !filter.End.IsZero() {
				conditions = append(conditions, "ts < ?")
				args = append(args, filter.End)
			}
			q := `SELECT EXISTS(SELECT 1 FROM (
 SELECT ts FROM metric_cost_usage
 UNION ALL SELECT bucket_start FROM archive.usage_hourly WHERE client='claude' AND cost_usd<>0
) costs WHERE ` + strings.Join(conditions, " AND ") + `)`
			if err := db.QueryRowContext(ctx, q, args...).Scan(&observed); err != nil {
				return m, fmt.Errorf("query Claude cost availability: %w", err)
			}
			if !observed {
				return m, nil
			}
		}
		if m.Available {
			m.Source = "mixed"
		} else {
			m.Source = "authoritative"
		}
		m.Available = true
	}
	return m, nil
}

func costMetadata(native, local, legacy int64, legacyRows bool, reference sql.NullFloat64) CostMetadata {
	sources := make([]string, 0, 3)
	if native > 0 {
		sources = append(sources, "native")
	}
	if local > 0 {
		sources = append(sources, "local")
	}
	if legacy > 0 {
		sources = append(sources, "legacy")
	}
	m := CostMetadata{Available: len(sources) > 0, Source: "unknown", Coverage: "unknown", NativeTurnCount: native, Legacy: legacyRows}
	if len(sources) == 1 {
		m.Source = sources[0]
	} else if len(sources) > 1 {
		m.Source = "mixed"
	}
	if reference.Valid {
		m.LocalEstimate = float64Ptr(reference.Float64)
	}
	return m
}

func queryCodexCost(ctx context.Context, db sqlQueryer, filter codexCostFilter) (codexCostResult, error) {
	var result codexCostResult
	conditions := []string{"TRUE"}
	args := make([]any, 0, 3)
	if !filter.Start.IsZero() {
		conditions = append(conditions, "ts >= ?")
		args = append(args, filter.Start)
	}
	if !filter.End.IsZero() {
		conditions = append(conditions, "ts < ?")
		args = append(args, filter.End)
	}
	if filter.ConversationID != "" {
		conditions = append(conditions, "conversation_id = ?")
		args = append(args, filter.ConversationID)
	}
	where := strings.Join(conditions, " AND ")
	var native, local, legacy int64
	var legacyRows bool
	const totals = `SELECT COALESCE(SUM(cost_usd),0),
 COALESCE(SUM(CASE WHEN cost_source='native' THEN sample_count ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN cost_source='local' THEN sample_count ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN cost_source='legacy' THEN sample_count ELSE 0 END),0),
 COALESCE(BOOL_OR(cost_source='legacy'), FALSE) FROM codex_selected_costs WHERE `
	if err := db.QueryRowContext(ctx, totals+where, args...).Scan(&result.Total, &native, &local, &legacy, &legacyRows); err != nil {
		return result, fmt.Errorf("query selected codex cost: %w", err)
	}
	if filter.ConversationID != "" {
		var old sql.NullFloat64
		err := db.QueryRowContext(ctx, `SELECT cost_usd FROM archive.codex_legacy_session_cost WHERE conversation_id=?`, filter.ConversationID).Scan(&old)
		if err != nil && err != sql.ErrNoRows {
			return result, fmt.Errorf("query legacy session cost: %w", err)
		}
		if old.Valid {
			legacyRows = true
			result.Total += old.Float64
			if old.Float64 > 0 {
				legacy++
			}
		}
	}
	var reference sql.NullFloat64
	if err := db.QueryRowContext(ctx, `SELECT SUM(cost_usd) FROM codex_local_costs WHERE `+where, args...).Scan(&reference); err != nil {
		return result, fmt.Errorf("query codex local reference: %w", err)
	}
	result.CostMetadata = costMetadata(native, local, legacy, legacyRows, reference)
	return result, nil
}

// One batched query enriches only the selected session candidates. The
// invocation view deduplicates across hot records and archived identities.
func attachCodexSessionRows(ctx context.Context, db sqlQueryer, sessions []sessionListRow) error {
	values := make([]string, 0, len(sessions))
	args := make([]any, 0, len(sessions))
	byID := make(map[string]int, len(sessions))
	for i, row := range sessions {
		if row.Client != "codex" {
			continue
		}
		values = append(values, "(?)")
		args = append(args, row.SessionID)
		byID[row.SessionID] = i
	}
	if len(values) == 0 {
		return nil
	}
	q := `WITH wanted(conversation_id) AS (VALUES ` + strings.Join(values, ",") + `), costs AS (
 SELECT c.conversation_id, SUM(cost_usd) cost_usd,
 SUM(CASE WHEN cost_source='native' THEN sample_count ELSE 0 END) native_count,
 SUM(CASE WHEN cost_source='local' THEN sample_count ELSE 0 END) local_count
 FROM codex_selected_costs c JOIN wanted w USING(conversation_id) GROUP BY 1
), refs AS (
 SELECT c.conversation_id, SUM(cost_usd) cost_usd FROM codex_local_costs c JOIN wanted w USING(conversation_id) GROUP BY 1
), skills AS (
 SELECT c.conversation_id, COUNT(*) invocations FROM codex_skill_invocations c JOIN wanted w USING(conversation_id) GROUP BY 1
)
SELECT w.conversation_id, COALESCE(c.cost_usd,0)+COALESCE(l.cost_usd,0),
 COALESCE(c.native_count,0), COALESCE(c.local_count,0), l.cost_usd, r.cost_usd, COALESCE(s.invocations,0)
FROM wanted w LEFT JOIN costs c USING(conversation_id)
LEFT JOIN archive.codex_legacy_session_cost l USING(conversation_id)
LEFT JOIN refs r USING(conversation_id) LEFT JOIN skills s USING(conversation_id)`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("query codex session sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var total float64
		var native, local, skills int64
		var legacy, reference sql.NullFloat64
		if err := rows.Scan(&id, &total, &native, &local, &legacy, &reference, &skills); err != nil {
			return fmt.Errorf("scan codex session sources: %w", err)
		}
		var legacyCount int64
		if legacy.Valid && legacy.Float64 > 0 {
			legacyCount = 1
		}
		i := byID[id]
		sessions[i].Cost = total
		sessions[i].Skills = skills
		sessions[i].CostMetadata = costMetadata(native, local, legacyCount, legacy.Valid, reference)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate codex session sources: %w", err)
	}
	return nil
}
