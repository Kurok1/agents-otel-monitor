package dashboard

import (
	"context"
	"database/sql"
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
	tools, err := QueryToolsRanking(ctx, db, opts)
	if err != nil {
		return RankingsResponse{}, err
	}
	skills, err := QuerySkillsRanking(ctx, db, opts)
	if err != nil {
		return RankingsResponse{}, err
	}
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
