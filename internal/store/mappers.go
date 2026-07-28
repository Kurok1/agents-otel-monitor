package store

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/otlp"
)

// --- sql.Null* → driver.Value helpers (nil for invalid) ---

func nullStr(v sql.NullString) driver.Value {
	if v.Valid {
		return v.String
	}
	return nil
}

func nullInt64(v sql.NullInt64) driver.Value {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func nullInt32(v sql.NullInt32) driver.Value {
	if v.Valid {
		return v.Int32
	}
	return nil
}

func nullFloat64(v sql.NullFloat64) driver.Value {
	if v.Valid {
		return v.Float64
	}
	return nil
}

func nullBool(v sql.NullBool) driver.Value {
	if v.Valid {
		return v.Bool
	}
	return nil
}

// commonMetricCols emits the 12-column prefix shared by every metric table.
// Column order matches the DDL in internal/store/migrations/001_metric_tables.sql.
func commonMetricCols(c otlp.CommonAttrs, startTs time.Time, value driver.Value) []driver.Value {
	return []driver.Value{
		c.Timestamp,
		startTs,
		time.Now().UTC(),
		value,
		nullStr(c.SessionID),
		c.UserID,
		nullStr(c.UserAccountUUID),
		nullStr(c.UserAccountID),
		nullStr(c.UserEmail),
		nullStr(c.OrganizationID),
		nullStr(c.AppVersion),
		nullStr(c.TerminalType),
	}
}

// commonEventCols emits the 13-column prefix shared by every event table.
// Column order matches the DDL in internal/store/migrations/002_event_tables.sql.
func commonEventCols(c otlp.EventCommonAttrs) []driver.Value {
	return []driver.Value{
		c.Timestamp,
		time.Now().UTC(),
		nullInt64(c.EventSequence),
		nullStr(c.PromptID),
		stringSliceValue(c.WorkspaceHostPaths),
		nullStr(c.SessionID),
		c.UserID,
		nullStr(c.UserAccountUUID),
		nullStr(c.UserAccountID),
		nullStr(c.UserEmail),
		nullStr(c.OrganizationID),
		nullStr(c.AppVersion),
		nullStr(c.TerminalType),
	}
}

// --- Metric mappers (8) ---

func mapSessionCount(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.MetricSessionCountRow)
	if !ok {
		return nil, fmt.Errorf("expected MetricSessionCountRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonMetricCols(r.CommonAttrs, r.StartTimestamp, r.Value)
	return append(args, nullStr(r.StartType), attrs), nil
}

func mapLinesOfCode(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.MetricLinesOfCodeCountRow)
	if !ok {
		return nil, fmt.Errorf("expected MetricLinesOfCodeCountRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonMetricCols(r.CommonAttrs, r.StartTimestamp, r.Value)
	return append(args, nullStr(r.Type), attrs), nil
}

func mapPullRequest(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.MetricPullRequestCountRow)
	if !ok {
		return nil, fmt.Errorf("expected MetricPullRequestCountRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonMetricCols(r.CommonAttrs, r.StartTimestamp, r.Value)
	return append(args, attrs), nil
}

func mapCommit(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.MetricCommitCountRow)
	if !ok {
		return nil, fmt.Errorf("expected MetricCommitCountRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonMetricCols(r.CommonAttrs, r.StartTimestamp, r.Value)
	return append(args, attrs), nil
}

func mapCostUsage(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.MetricCostUsageRow)
	if !ok {
		return nil, fmt.Errorf("expected MetricCostUsageRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonMetricCols(r.CommonAttrs, r.StartTimestamp, r.Value)
	return append(args,
		nullStr(r.Model),
		nullStr(r.QuerySource),
		nullStr(r.Speed),
		nullStr(r.Effort),
		nullStr(r.AgentName),
		nullStr(r.SkillName),
		nullStr(r.PluginName),
		nullStr(r.MarketplaceName),
		attrs,
	), nil
}

func mapTokenUsage(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.MetricTokenUsageRow)
	if !ok {
		return nil, fmt.Errorf("expected MetricTokenUsageRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonMetricCols(r.CommonAttrs, r.StartTimestamp, r.Value)
	return append(args,
		nullStr(r.Type),
		nullStr(r.Model),
		nullStr(r.QuerySource),
		nullStr(r.Speed),
		nullStr(r.Effort),
		nullStr(r.AgentName),
		nullStr(r.SkillName),
		nullStr(r.PluginName),
		nullStr(r.MarketplaceName),
		attrs,
	), nil
}

func mapCodeEditDecision(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.MetricCodeEditToolDecisionRow)
	if !ok {
		return nil, fmt.Errorf("expected MetricCodeEditToolDecisionRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonMetricCols(r.CommonAttrs, r.StartTimestamp, r.Value)
	return append(args,
		nullStr(r.ToolName),
		nullStr(r.Decision),
		nullStr(r.Source),
		nullStr(r.Language),
		attrs,
	), nil
}

func mapActiveTime(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.MetricActiveTimeTotalRow)
	if !ok {
		return nil, fmt.Errorf("expected MetricActiveTimeTotalRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonMetricCols(r.CommonAttrs, r.StartTimestamp, r.Value)
	return append(args, nullStr(r.Type), attrs), nil
}

// --- Event mappers tier 1 (5) ---

func mapUserPrompt(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.EventUserPromptRow)
	if !ok {
		return nil, fmt.Errorf("expected EventUserPromptRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonEventCols(r.EventCommonAttrs)
	return append(args,
		nullInt32(r.PromptLength),
		nullStr(r.Prompt),
		nullStr(r.CommandName),
		nullStr(r.CommandSource),
		attrs,
	), nil
}

func mapApiRequest(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.EventApiRequestRow)
	if !ok {
		return nil, fmt.Errorf("expected EventApiRequestRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonEventCols(r.EventCommonAttrs)
	return append(args,
		nullStr(r.Model),
		nullFloat64(r.CostUsd),
		nullInt64(r.DurationMs),
		nullInt64(r.InputTokens),
		nullInt64(r.OutputTokens),
		nullInt64(r.CacheReadTokens),
		nullInt64(r.CacheCreationTokens),
		nullStr(r.RequestID),
		nullStr(r.Speed),
		nullStr(r.QuerySource),
		nullStr(r.Effort),
		attrs,
	), nil
}

func mapApiError(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.EventApiErrorRow)
	if !ok {
		return nil, fmt.Errorf("expected EventApiErrorRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonEventCols(r.EventCommonAttrs)
	return append(args,
		nullStr(r.Model),
		nullStr(r.Error),
		nullInt32(r.StatusCode),
		nullInt64(r.DurationMs),
		nullInt32(r.Attempt),
		nullStr(r.RequestID),
		nullStr(r.Speed),
		nullStr(r.QuerySource),
		nullStr(r.Effort),
		attrs,
	), nil
}

func mapToolResult(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.EventToolResultRow)
	if !ok {
		return nil, fmt.Errorf("expected EventToolResultRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonEventCols(r.EventCommonAttrs)
	return append(args,
		nullStr(r.ToolName),
		nullStr(r.ToolUseID),
		nullBool(r.Success),
		nullInt64(r.DurationMs),
		nullStr(r.ErrorType),
		nullStr(r.Error),
		nullStr(r.DecisionType),
		nullStr(r.DecisionSource),
		nullInt64(r.ToolInputSizeBytes),
		nullInt64(r.ToolResultSizeBytes),
		nullStr(r.MCPServerScope),
		nullStr(r.ToolParameters),
		nullStr(r.ToolInput),
		attrs,
	), nil
}

func mapToolDecision(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.EventToolDecisionRow)
	if !ok {
		return nil, fmt.Errorf("expected EventToolDecisionRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonEventCols(r.EventCommonAttrs)
	return append(args,
		nullStr(r.ToolName),
		nullStr(r.ToolUseID),
		nullStr(r.Decision),
		nullStr(r.Source),
		attrs,
	), nil
}

// --- Event mappers tier 2 (6) ---

func mapApiRetriesExhausted(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.EventApiRetriesExhaustedRow)
	if !ok {
		return nil, fmt.Errorf("expected EventApiRetriesExhaustedRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonEventCols(r.EventCommonAttrs)
	return append(args,
		nullStr(r.Model),
		nullStr(r.Error),
		nullInt32(r.StatusCode),
		nullInt32(r.TotalAttempts),
		nullInt64(r.TotalRetryDurationMs),
		nullStr(r.Speed),
		attrs,
	), nil
}

func mapCompaction(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.EventCompactionRow)
	if !ok {
		return nil, fmt.Errorf("expected EventCompactionRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonEventCols(r.EventCommonAttrs)
	return append(args,
		nullStr(r.Trigger),
		nullBool(r.Success),
		nullInt64(r.DurationMs),
		nullInt64(r.PreTokens),
		attrs,
	), nil
}

func mapPermissionModeChanged(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.EventPermissionModeChangedRow)
	if !ok {
		return nil, fmt.Errorf("expected EventPermissionModeChangedRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonEventCols(r.EventCommonAttrs)
	return append(args,
		nullStr(r.FromMode),
		nullStr(r.ToMode),
		nullStr(r.Trigger),
		attrs,
	), nil
}

func mapMCPServerConnection(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.EventMCPServerConnectionRow)
	if !ok {
		return nil, fmt.Errorf("expected EventMCPServerConnectionRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonEventCols(r.EventCommonAttrs)
	return append(args,
		nullStr(r.Status),
		nullStr(r.TransportType),
		nullStr(r.ServerScope),
		nullInt64(r.DurationMs),
		nullStr(r.ErrorCode),
		nullStr(r.ServerName),
		nullStr(r.Error),
		attrs,
	), nil
}

func mapSkillActivated(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.EventSkillActivatedRow)
	if !ok {
		return nil, fmt.Errorf("expected EventSkillActivatedRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonEventCols(r.EventCommonAttrs)
	return append(args,
		nullStr(r.SkillName),
		nullStr(r.InvocationTrigger),
		nullStr(r.SkillSource),
		nullStr(r.PluginName),
		nullStr(r.MarketplaceName),
		attrs,
	), nil
}

func mapAtMention(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.EventAtMentionRow)
	if !ok {
		return nil, fmt.Errorf("expected EventAtMentionRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonEventCols(r.EventCommonAttrs)
	return append(args,
		nullStr(r.MentionType),
		nullBool(r.Success),
		attrs,
	), nil
}

// tableSpec pairs a table name with its row mapper. The order of allTables is
// not significant for correctness but kept stable for readable startup logs.
type tableSpec struct {
	name   string
	mapper rowMapper
}

var allTables = []tableSpec{
	{"metric_session_count", mapSessionCount},
	{"metric_lines_of_code_count", mapLinesOfCode},
	{"metric_pull_request_count", mapPullRequest},
	{"metric_commit_count", mapCommit},
	{"metric_cost_usage", mapCostUsage},
	{"metric_token_usage", mapTokenUsage},
	{"metric_code_edit_tool_decision", mapCodeEditDecision},
	{"metric_active_time_total", mapActiveTime},
	{"event_user_prompt", mapUserPrompt},
	{"event_api_request", mapApiRequest},
	{"event_api_error", mapApiError},
	{"event_tool_result", mapToolResult},
	{"event_tool_decision", mapToolDecision},
	{"event_api_retries_exhausted", mapApiRetriesExhausted},
	{"event_compaction", mapCompaction},
	{"event_permission_mode_changed", mapPermissionModeChanged},
	{"event_mcp_server_connection", mapMCPServerConnection},
	{"event_skill_activated", mapSkillActivated},
	{"event_at_mention", mapAtMention},
	{"codex_metric_skill_injected", mapCodexSkillInjected},
	{"codex_metric_response_tbt", mapCodexResponseTBT},
	{"codex_event_conversation_starts", mapCodexConversationStarts},
	{"codex_event_api_request", mapCodexApiRequest},
	{"codex_event_token_usage", mapCodexTokenUsage},
	{"codex_event_user_prompt", mapCodexUserPrompt},
	{"codex_event_tool_decision", mapCodexToolDecision},
	{"codex_event_tool_result", mapCodexToolResult},
}

// tableNameFor maps a row's Go type to its DuckDB table. Returns ok=false for
// unknown types (should never happen given dispatcher → Sink coupling).
func tableNameFor(row any) (string, bool) {
	switch row.(type) {
	case otlp.MetricSessionCountRow:
		return "metric_session_count", true
	case otlp.MetricLinesOfCodeCountRow:
		return "metric_lines_of_code_count", true
	case otlp.MetricPullRequestCountRow:
		return "metric_pull_request_count", true
	case otlp.MetricCommitCountRow:
		return "metric_commit_count", true
	case otlp.MetricCostUsageRow:
		return "metric_cost_usage", true
	case otlp.MetricTokenUsageRow:
		return "metric_token_usage", true
	case otlp.MetricCodeEditToolDecisionRow:
		return "metric_code_edit_tool_decision", true
	case otlp.MetricActiveTimeTotalRow:
		return "metric_active_time_total", true
	case otlp.EventUserPromptRow:
		return "event_user_prompt", true
	case otlp.EventApiRequestRow:
		return "event_api_request", true
	case otlp.EventApiErrorRow:
		return "event_api_error", true
	case otlp.EventToolResultRow:
		return "event_tool_result", true
	case otlp.EventToolDecisionRow:
		return "event_tool_decision", true
	case otlp.EventApiRetriesExhaustedRow:
		return "event_api_retries_exhausted", true
	case otlp.EventCompactionRow:
		return "event_compaction", true
	case otlp.EventPermissionModeChangedRow:
		return "event_permission_mode_changed", true
	case otlp.EventMCPServerConnectionRow:
		return "event_mcp_server_connection", true
	case otlp.EventSkillActivatedRow:
		return "event_skill_activated", true
	case otlp.EventAtMentionRow:
		return "event_at_mention", true
	case otlp.CodexMetricSkillInjectedRow:
		return "codex_metric_skill_injected", true
	case otlp.CodexMetricResponseTBTRow:
		return "codex_metric_response_tbt", true
	case otlp.CodexEventConversationStartsRow:
		return "codex_event_conversation_starts", true
	case otlp.CodexEventApiRequestRow:
		return "codex_event_api_request", true
	case otlp.CodexEventTokenUsageRow:
		return "codex_event_token_usage", true
	case otlp.CodexEventUserPromptRow:
		return "codex_event_user_prompt", true
	case otlp.CodexEventToolDecisionRow:
		return "codex_event_tool_decision", true
	case otlp.CodexEventToolResultRow:
		return "codex_event_tool_result", true
	}
	return "", false
}
