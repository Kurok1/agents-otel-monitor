package dashboard

/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.0.0
 */

import (
	"context"
	"database/sql"
	"time"
)

// otherBucketLabel is the synthetic name for the folded Top-N tail in the
// per-session pie charts.
const otherBucketLabel = "其他"

// BuildSessionDetail assembles GET /api/sessions/{id}. client is the hint
// from the query string: ClientClaude / ClientCodex query one family only;
// ClientAll probes claude first, then codex. found=false (with a nil error)
// means the session id has no activity in any queried family — the handler
// maps that to 404.
func BuildSessionDetail(ctx context.Context, db *sql.DB, sessionID string, client Client, toolsTopN, skillsTopN int, pricingEnabled bool) (SessionDetailResponse, bool, error) {
	type result struct {
		claude *claudeSessionDetail
		codex  *codexSessionDetail
	}
	resultValue, err := withDashboardSnapshot(ctx, db, func(q sqlQueryer) (result, error) {
		if client.includesClaude() {
			data, found, err := readClaudeSessionDetail(ctx, q, sessionID)
			if err != nil || found {
				return result{claude: data}, err
			}
		}
		if client.includesCodex() {
			data, _, err := readCodexSessionDetail(ctx, q, sessionID, pricingEnabled)
			return result{codex: data}, err
		}
		return result{}, nil
	})
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	if resultValue.claude != nil {
		return formatClaudeSessionDetail(resultValue.claude, toolsTopN, skillsTopN), true, nil
	}
	if resultValue.codex != nil {
		return formatCodexSessionDetail(resultValue.codex, toolsTopN), true, nil
	}
	return SessionDetailResponse{}, false, nil
}

type claudeSessionDetail struct {
	sessionID        string
	first, last      time.Time
	tokens, requests int64
	tools            []ToolRank
	skills           []SkillRank
	cost             float64
}
type codexSessionDetail struct {
	sessionID        string
	first, last      time.Time
	tokens, requests int64
	tools            []ToolRank
	detail           SessionTokenDetail
	cost             float64
	pricingEnabled   bool
}

func readClaudeSessionDetail(ctx context.Context, db sqlQueryer, sessionID string) (*claudeSessionDetail, bool, error) {
	first, last, err := QuerySessionTimespan(ctx, db, sessionID)
	if err != nil {
		return nil, false, err
	}
	if !last.Valid {
		return nil, false, nil
	}
	tokens, err := QuerySessionTokens(ctx, db, sessionID)
	if err != nil {
		return nil, false, err
	}
	requests, err := QuerySessionRequests(ctx, db, sessionID)
	if err != nil {
		return nil, false, err
	}
	tools, err := QuerySessionToolBreakdown(ctx, db, sessionID)
	if err != nil {
		return nil, false, err
	}
	skills, err := QuerySessionSkillBreakdown(ctx, db, sessionID)
	if err != nil {
		return nil, false, err
	}
	cost, err := QueryClaudeSessionCost(ctx, db, sessionID)
	if err != nil {
		return nil, false, err
	}
	return &claudeSessionDetail{sessionID, first.Time.UTC(), last.Time.UTC(), tokens, requests, tools, skills, cost}, true, nil
}
func readCodexSessionDetail(ctx context.Context, db sqlQueryer, sessionID string, pricingEnabled bool) (*codexSessionDetail, bool, error) {
	first, last, err := QueryCodexSessionTimespan(ctx, db, sessionID)
	if err != nil {
		return nil, false, err
	}
	if !last.Valid {
		return nil, false, nil
	}
	tokens, detail, err := QueryCodexSessionTokens(ctx, db, sessionID)
	if err != nil {
		return nil, false, err
	}
	requests, err := QueryCodexSessionRequests(ctx, db, sessionID)
	if err != nil {
		return nil, false, err
	}
	tools, err := QueryCodexSessionToolBreakdown(ctx, db, sessionID)
	if err != nil {
		return nil, false, err
	}
	data := &codexSessionDetail{sessionID, first.Time.UTC(), last.Time.UTC(), tokens, requests, tools, detail, 0, pricingEnabled}
	if pricingEnabled {
		cost, err := QueryCodexSessionCost(ctx, db, sessionID)
		if err != nil {
			return nil, false, err
		}
		data.cost = cost
	}
	return data, true, nil
}
func formatClaudeSessionDetail(data *claudeSessionDetail, toolsTopN, skillsTopN int) SessionDetailResponse {
	var toolCalls, skillTotal int64
	for _, tool := range data.tools {
		toolCalls += tool.Count
	}
	for _, skill := range data.skills {
		skillTotal += skill.Activations
	}
	cost := data.cost
	resp := SessionDetailResponse{SessionID: data.sessionID, Client: "claude", FirstActive: data.first.Format(time.RFC3339), LastActive: data.last.Format(time.RFC3339), Tokens: data.tokens, Requests: data.requests, ToolCalls: toolCalls, SkillActivations: skillTotal, Tools: bucketToolsTopN(data.tools, toolsTopN), Skills: bucketSkillsTopN(data.skills, skillsTopN), Cost: &cost}
	if resp.Tools == nil {
		resp.Tools = []ToolRank{}
	}
	if resp.Skills == nil {
		resp.Skills = []SkillRank{}
	}
	return resp
}
func formatCodexSessionDetail(data *codexSessionDetail, toolsTopN int) SessionDetailResponse {
	var toolCalls int64
	for _, tool := range data.tools {
		toolCalls += tool.Count
	}
	resp := SessionDetailResponse{SessionID: data.sessionID, Client: "codex", FirstActive: data.first.Format(time.RFC3339), LastActive: data.last.Format(time.RFC3339), Tokens: data.tokens, Requests: data.requests, ToolCalls: toolCalls, Tools: bucketToolsTopN(data.tools, toolsTopN), Skills: []SkillRank{}, TokenDetail: &data.detail}
	if resp.Tools == nil {
		resp.Tools = []ToolRank{}
	}
	if data.pricingEnabled {
		cost := data.cost
		resp.Cost = &cost
		resp.CostEstimated = true
	}
	return resp
}

// buildClaudeSessionDetail is the original claude-family detail path.
// toolsTopN / skillsTopN come from dashboard.top_n; <= 0 disables bucketing.
// Queries are sequential — DuckDB MaxOpenConns=1 makes parallelism pointless.
func buildClaudeSessionDetail(ctx context.Context, db sqlQueryer, sessionID string, toolsTopN, skillsTopN int) (SessionDetailResponse, bool, error) {
	first, last, err := QuerySessionTimespan(ctx, db, sessionID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	if !last.Valid {
		return SessionDetailResponse{}, false, nil // unknown session → 404
	}

	tokens, err := QuerySessionTokens(ctx, db, sessionID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	requests, err := QuerySessionRequests(ctx, db, sessionID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	tools, err := QuerySessionToolBreakdown(ctx, db, sessionID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	skills, err := QuerySessionSkillBreakdown(ctx, db, sessionID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}

	var toolCalls int64
	for _, t := range tools {
		toolCalls += t.Count
	}
	var skillTotal int64
	for _, s := range skills {
		skillTotal += s.Activations
	}

	resp := SessionDetailResponse{
		SessionID:        sessionID,
		Client:           "claude",
		FirstActive:      first.Time.UTC().Format(time.RFC3339),
		LastActive:       last.Time.UTC().Format(time.RFC3339),
		Tokens:           tokens,
		Requests:         requests,
		ToolCalls:        toolCalls,
		SkillActivations: skillTotal,
		Tools:            bucketToolsTopN(tools, toolsTopN),
		Skills:           bucketSkillsTopN(skills, skillsTopN),
	}
	if resp.Tools == nil {
		resp.Tools = []ToolRank{}
	}
	if resp.Skills == nil {
		resp.Skills = []SkillRank{}
	}
	cost, err := QueryClaudeSessionCost(ctx, db, sessionID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	resp.Cost = &cost // authoritative; CostEstimated stays false
	return resp, true, nil
}

// buildCodexSessionDetail assembles the codex-family detail: tokens follow
// the merged projection (total = input + output), requests count completed
// responses, and there is no skill concept.
func buildCodexSessionDetail(ctx context.Context, db sqlQueryer, conversationID string, toolsTopN int, pricingEnabled bool) (SessionDetailResponse, bool, error) {
	first, last, err := QueryCodexSessionTimespan(ctx, db, conversationID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	if !last.Valid {
		return SessionDetailResponse{}, false, nil
	}
	tokens, detail, err := QueryCodexSessionTokens(ctx, db, conversationID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	requests, err := QueryCodexSessionRequests(ctx, db, conversationID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	tools, err := QueryCodexSessionToolBreakdown(ctx, db, conversationID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	var toolCalls int64
	for _, t := range tools {
		toolCalls += t.Count
	}
	resp := SessionDetailResponse{
		SessionID:   conversationID,
		Client:      "codex",
		FirstActive: first.Time.UTC().Format(time.RFC3339),
		LastActive:  last.Time.UTC().Format(time.RFC3339),
		Tokens:      tokens,
		Requests:    requests,
		ToolCalls:   toolCalls,
		Tools:       bucketToolsTopN(tools, toolsTopN),
		Skills:      []SkillRank{}, // codex has no skill concept
		TokenDetail: &detail,
	}
	if resp.Tools == nil {
		resp.Tools = []ToolRank{}
	}
	if pricingEnabled {
		cost, err := QueryCodexSessionCost(ctx, db, conversationID)
		if err != nil {
			return SessionDetailResponse{}, false, err
		}
		resp.Cost = &cost
		resp.CostEstimated = true
	}
	return resp, true, nil
}

// BuildSessionList assembles GET /api/sessions. The caller is responsible for
// clamping limit to a sane range (see parseLimit in handler.go).
func BuildSessionList(ctx context.Context, db *sql.DB, client Client, limit int, pricingEnabled bool) (SessionListResponse, error) {
	rows, err := withDashboardSnapshot(ctx, db, func(q sqlQueryer) ([]sessionListRow, error) { return QuerySessionList(ctx, q, client, limit) })
	if err != nil {
		return SessionListResponse{}, err
	}
	out := make([]SessionSummary, 0, len(rows))
	for _, r := range rows {
		s := SessionSummary{
			SessionID:        r.SessionID,
			Client:           r.Client,
			FirstActive:      r.FirstTs.UTC().Format(time.RFC3339),
			LastActive:       r.LastTs.UTC().Format(time.RFC3339),
			Tokens:           r.Tokens,
			Requests:         r.Requests,
			ToolCalls:        r.ToolCalls,
			SkillActivations: r.Skills,
		}
		if r.Client == "codex" {
			if pricingEnabled {
				c := r.Cost
				s.Cost = &c
				s.CostEstimated = true
			}
		} else {
			c := r.Cost
			s.Cost = &c // claude authoritative
		}
		out = append(out, s)
	}
	return SessionListResponse{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Sessions:  out,
	}, nil
}

// bucketToolsTopN keeps the top n tools and folds the remaining rows into a
// single "其他" entry, preserving the full call total. Input must be sorted by
// count desc. n <= 0 or len <= n returns the input unchanged.
func bucketToolsTopN(rows []ToolRank, n int) []ToolRank {
	if n <= 0 || len(rows) <= n {
		return rows
	}
	out := make([]ToolRank, 0, n+1)
	out = append(out, rows[:n]...)
	var rest int64
	for _, r := range rows[n:] {
		rest += r.Count
	}
	if rest > 0 {
		out = append(out, ToolRank{Name: otherBucketLabel, Count: rest})
	}
	return out
}

// bucketSkillsTopN mirrors bucketToolsTopN for the skill-activation pie.
func bucketSkillsTopN(rows []SkillRank, n int) []SkillRank {
	if n <= 0 || len(rows) <= n {
		return rows
	}
	out := make([]SkillRank, 0, n+1)
	out = append(out, rows[:n]...)
	var rest int64
	for _, r := range rows[n:] {
		rest += r.Activations
	}
	if rest > 0 {
		out = append(out, SkillRank{Name: otherBucketLabel, Activations: rest})
	}
	return out
}
