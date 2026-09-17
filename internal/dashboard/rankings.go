package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// BuildRankings runs the two rankings queries; sinceTag is the validated
// canonical form (7d|30d|all) — caller is responsible for converting the
// raw query string to (sinceStart, sinceTag) via SinceStart().
type RankingsOpts struct {
	SinceStart time.Time // zero ⇒ all-time
	SinceTag   string
	Client     Client
	ToolsTopN  int
	SkillsTopN int
}

func BuildRankings(ctx context.Context, db *sql.DB, opts RankingsOpts) (RankingsResponse, error) {
	rows, err := withDashboardSnapshot(ctx, db, func(q sqlQueryer) (rankingLayers, error) {
		return readRankingLayers(ctx, q, opts)
	})
	if err != nil {
		return RankingsResponse{}, err
	}
	tools, skills := mergeToolRanks(rows.tools, opts.ToolsTopN), mergeSkillRanks(rows.skills, opts.SkillsTopN)
	if tools == nil {
		tools = []ToolRank{}
	}
	if skills == nil {
		skills = []SkillRank{}
	}
	return RankingsResponse{
		Since:  opts.SinceTag,
		Tools:  tools,
		Skills: skills,
	}, nil
}

type rankingLayers struct {
	tools  []ToolRank
	skills []SkillRank
}

func readRankingLayers(ctx context.Context, db sqlQueryer, opts RankingsOpts) (rankingLayers, error) {
	if opts.Client == "" {
		opts.Client = ClientAll
	}
	if opts.Client != ClientAll && opts.Client != ClientClaude && opts.Client != ClientCodex {
		return rankingLayers{}, fmt.Errorf("query rankings: invalid client %q", opts.Client)
	}
	var out rankingLayers
	readTools := func(statement string, args ...any) error {
		rows, err := db.QueryContext(ctx, statement, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var row ToolRank
			if err := rows.Scan(&row.Name, &row.Count); err != nil {
				_ = rows.Close()
				return err
			}
			out.tools = append(out.tools, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		return rows.Close()
	}
	readSkills := func(statement string, args ...any) error {
		rows, err := db.QueryContext(ctx, statement, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var row SkillRank
			if err := rows.Scan(&row.Name, &row.Activations); err != nil {
				_ = rows.Close()
				return err
			}
			out.skills = append(out.skills, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		return rows.Close()
	}
	since := nullableTime(opts.SinceStart)
	for _, source := range archiveClients(opts.Client) {
		if err := readTools(`SELECT tool_name, COALESCE(SUM(call_count),0) FROM archive.tool_hourly WHERE client=? AND (? IS NULL OR bucket_start>=?) GROUP BY tool_name`, source, since, since); err != nil {
			return out, fmt.Errorf("read archive ranking tools: %w", err)
		}
		if err := readSkills(`SELECT skill_name, COALESCE(SUM(activation_count),0) FROM archive.skill_hourly WHERE client=? AND (? IS NULL OR bucket_start>=?) GROUP BY skill_name`, source, since, since); err != nil {
			return out, fmt.Errorf("read archive ranking skills: %w", err)
		}
	}
	if opts.Client.includesClaude() {
		if err := readTools(`SELECT tool_name, COUNT(*) FROM event_tool_result WHERE tool_name IS NOT NULL AND (? IS NULL OR ts>=?) GROUP BY tool_name`, since, since); err != nil {
			return out, fmt.Errorf("read raw claude ranking tools: %w", err)
		}
		if err := readSkills(`SELECT skill_name, COUNT(*) FROM event_skill_activated WHERE skill_name IS NOT NULL AND (? IS NULL OR ts>=?) GROUP BY skill_name`, since, since); err != nil {
			return out, fmt.Errorf("read raw claude ranking skills: %w", err)
		}
	}
	if opts.Client.includesCodex() {
		if err := readTools(`SELECT tool_name, COUNT(*) FROM codex_event_tool_result WHERE tool_name IS NOT NULL AND (? IS NULL OR ts>=?) GROUP BY tool_name`, since, since); err != nil {
			return out, fmt.Errorf("read raw codex ranking tools: %w", err)
		}
		if err := readSkills(`SELECT skill, COALESCE(SUM(value),0) FROM codex_metric_skill_injected WHERE skill IS NOT NULL AND LOWER(status) IN ('ok','success') AND (? IS NULL OR ts>=?) GROUP BY skill`, since, since); err != nil {
			return out, fmt.Errorf("read raw codex ranking skills: %w", err)
		}
	}
	return out, nil
}

func mergeToolRanks(rows []ToolRank, topN int) []ToolRank {
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		counts[row.Name] += row.Count
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
	if topN > 0 && len(out) > topN {
		out = out[:topN]
	}
	return out
}
func mergeSkillRanks(rows []SkillRank, topN int) []SkillRank {
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		counts[row.Name] += row.Activations
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
	if topN > 0 && len(out) > topN {
		out = out[:topN]
	}
	return out
}
