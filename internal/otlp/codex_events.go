/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.2.0
 */

package otlp

import (
	"database/sql"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	lpb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// preparseCodexEvent mirrors preparseEvent for the codex.* event family.
// There is no error path: Codex has no required identity attribute.
func preparseCodexEvent(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (attrMap, CodexCommonAttrs) {
	m := newAttrMap(resourceAttrs, rec.Attributes)
	delete(m, "event.name")
	ts := codexEventTime(rec, m)
	return m, extractCodexCommonAttrs(m, ts)
}

// codexEventTime resolves the event time. Codex (0.142.x) leaves the
// LogRecord's time_unix_nano at 0, so fall back to observed_time_unix_nano,
// then to the RFC3339 event.timestamp attribute. The attribute is consumed
// unconditionally so it never pollutes the attrs leftover.
func codexEventTime(rec *lpb.LogRecord, m attrMap) time.Time {
	evTs := m.takeString("event.timestamp")
	if rec.TimeUnixNano != 0 {
		return time.Unix(0, int64(rec.TimeUnixNano)).UTC()
	}
	if rec.ObservedTimeUnixNano != 0 {
		return time.Unix(0, int64(rec.ObservedTimeUnixNano)).UTC()
	}
	if evTs.Valid {
		if t, err := time.Parse(time.RFC3339Nano, evTs.String); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC() // last resort: receive time ≈ event time for a live stream
}

func extractCodexCommonAttrs(m attrMap, ts time.Time) CodexCommonAttrs {
	return CodexCommonAttrs{
		Timestamp:      ts,
		ConversationID: m.takeString("conversation.id"),
		AppVersion:     m.takeString("app.version"),
		AuthMode:       m.takeString("auth_mode"),
		Originator:     m.takeString("originator"),
		TerminalType:   m.takeString("terminal.type"),
		Model:          m.takeString("model"),
		Slug:           m.takeString("slug"),
		UserAccountID:  m.takeString("user.account_id"),
		UserEmail:      m.takeString("user.email"),
	}
}

// takeLength consumes a possibly-sensitive string attribute and returns only
// its byte length. The raw value never reaches a column or the attrs leftover.
func takeLength(m attrMap, key string) sql.NullInt64 {
	v, ok := m.take(key)
	if !ok || v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(len(asString(v))), Valid: true}
}

func parseCodexConversationStarts(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (CodexEventConversationStartsRow, error) {
	m, common := preparseCodexEvent(rec, resourceAttrs)
	row := CodexEventConversationStartsRow{
		CodexCommonAttrs:      common,
		ProviderName:          m.takeString("provider_name"),
		ReasoningEffort:       m.takeString("reasoning_effort"),
		ReasoningSummary:      m.takeString("reasoning_summary"),
		ContextWindow:         m.takeInt64("context_window"),
		AutoCompactTokenLimit: m.takeInt64("auto_compact_token_limit"),
		ApprovalPolicy:        m.takeString("approval_policy"),
		SandboxPolicy:         m.takeString("sandbox_policy"),
		MCPServers:            m.takeString("mcp_servers"),
	}
	row.Attrs = m.leftover() // auth.env_* flags land here
	return row, nil
}

func parseCodexApiRequest(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (CodexEventApiRequestRow, error) {
	m, common := preparseCodexEvent(rec, resourceAttrs)
	row := CodexEventApiRequestRow{
		CodexCommonAttrs: common,
		DurationMs:       m.takeInt64("duration_ms"),
		StatusCode:       m.takeInt32("http.response.status_code"),
		Error:            m.takeString("error.message"),
		Attempt:          m.takeInt64("attempt"),
		Endpoint:         m.takeString("endpoint"),
	}
	row.Attrs = m.leftover() // auth.* diagnostics land here
	return row, nil
}

func parseCodexTokenUsage(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (CodexEventTokenUsageRow, error) {
	m, common := preparseCodexEvent(rec, resourceAttrs)
	m.take("event.kind") // always "response.completed" here (dispatcher filtered)
	row := CodexEventTokenUsageRow{
		CodexCommonAttrs:     common,
		InputTokenCount:      m.takeInt64("input_token_count"),
		OutputTokenCount:     m.takeInt64("output_token_count"),
		CachedTokenCount:     m.takeInt64("cached_token_count"),
		ReasoningTokenCount:  m.takeInt64("reasoning_token_count"),
		ToolTokenCount:       m.takeInt64("tool_token_count"),
		ServiceTier:          m.takeString("service_tier"),
		ModelReasoningEffort: m.takeString("model_reasoning_effort"),
		DurationMs:           m.takeInt64("duration_ms"),
		TTFTMs:               m.takeInt64("ttft_ms"),
		CacheWriteTokenCount: m.takeInt64("cache_write_token_count"),
		Error:                m.takeString("error.message"),
	}
	row.UsageValid = row.ValidUsage()
	if row.TTFTMs.Valid && row.TTFTMs.Int64 < 0 {
		row.TTFTMs = sql.NullInt64{}
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseCodexUserPrompt(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (CodexEventUserPromptRow, error) {
	m, common := preparseCodexEvent(rec, resourceAttrs)
	row := CodexEventUserPromptRow{
		CodexCommonAttrs: common,
		PromptLength:     m.takeInt32("prompt_length"),
		Prompt:           m.takeString("prompt"), // "[REDACTED]" unless log_user_prompt=true
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseCodexToolDecision(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (CodexEventToolDecisionRow, error) {
	m, common := preparseCodexEvent(rec, resourceAttrs)
	row := CodexEventToolDecisionRow{
		CodexCommonAttrs: common,
		ToolName:         m.takeString("tool_name"),
		CallID:           m.takeString("call_id"),
		Decision:         m.takeString("decision"), // raw codex enum: approved / denied / ...
		Source:           m.takeString("source"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseCodexToolResult(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (CodexEventToolResultRow, error) {
	m, common := preparseCodexEvent(rec, resourceAttrs)
	row := CodexEventToolResultRow{
		CodexCommonAttrs: common,
		ToolName:         m.takeString("tool_name"),
		CallID:           m.takeString("call_id"),
		DurationMs:       m.takeInt64("duration_ms"),
		Success:          m.takeBool("success"),
		MCPServer:        m.takeString("mcp_server"),
		MCPServerOrigin:  m.takeString("mcp_server_origin"),
		ArgumentsLength:  takeLength(m, "arguments"), // privacy: length only, raw dropped
		OutputLength:     takeLength(m, "output"),
		OutputTruncated:  m.takeBool("output_truncated"),
	}
	row.Attrs = m.leftover()
	return row, nil
}
