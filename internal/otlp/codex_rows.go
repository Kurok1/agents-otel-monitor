/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.2.0
 */

package otlp

import (
	"database/sql"
	"time"
)

// CodexCommonAttrs is shared by all Codex event rows. Unlike CommonAttrs there
// is no required user identity: Codex only emits nullable user.account_id /
// user.email. Fields map 1:1 to the common column prefix in
// internal/store/migrations/003_codex_event_tables.sql.
type CodexCommonAttrs struct {
	Timestamp      time.Time
	ConversationID sql.NullString
	AppVersion     sql.NullString
	AuthMode       sql.NullString
	Originator     sql.NullString
	TerminalType   sql.NullString
	Model          sql.NullString
	Slug           sql.NullString
	UserAccountID  sql.NullString
	UserEmail      sql.NullString
	Attrs          map[string]any // leftover, serialized into the attrs JSON column
}

type CodexMetricSkillInjectedRow struct {
	CodexCommonAttrs
	StartTimestamp time.Time
	Value          int64
	Skill          sql.NullString
	Status         sql.NullString
	InvokeType     sql.NullString
	SessionSource  sql.NullString
}

type CodexMetricResponseTBTRow struct {
	CodexCommonAttrs
	StartTimestamp time.Time
	SampleCount    int64
	SumMs          float64
	SessionSource  sql.NullString
}

type CodexEventConversationStartsRow struct {
	CodexCommonAttrs
	ProviderName          sql.NullString
	ReasoningEffort       sql.NullString
	ReasoningSummary      sql.NullString
	ContextWindow         sql.NullInt64
	AutoCompactTokenLimit sql.NullInt64
	ApprovalPolicy        sql.NullString
	SandboxPolicy         sql.NullString
	MCPServers            sql.NullString
}

type CodexEventApiRequestRow struct {
	CodexCommonAttrs
	DurationMs sql.NullInt64
	StatusCode sql.NullInt32
	Error      sql.NullString
	Attempt    sql.NullInt64
	Endpoint   sql.NullString
}

// CodexEventTokenUsageRow is parsed from codex.sse_event records whose
// event.kind is response.completed; other kinds are skipped by the dispatcher.
// OpenAI counts are subset-style: cached ⊂ input, reasoning ⊂ output.
type CodexEventTokenUsageRow struct {
	CodexCommonAttrs
	InputTokenCount      sql.NullInt64
	OutputTokenCount     sql.NullInt64
	CachedTokenCount     sql.NullInt64
	ReasoningTokenCount  sql.NullInt64
	ToolTokenCount       sql.NullInt64
	ServiceTier          sql.NullString
	ModelReasoningEffort sql.NullString
	DurationMs           sql.NullInt64
	CostUsd              sql.NullFloat64 // estimated at ingest by internal/pricing; NULL when unpriced/disabled
}

type CodexEventUserPromptRow struct {
	CodexCommonAttrs
	PromptLength sql.NullInt32
	Prompt       sql.NullString
}

type CodexEventToolDecisionRow struct {
	CodexCommonAttrs
	ToolName sql.NullString
	CallID   sql.NullString
	Decision sql.NullString
	Source   sql.NullString
}

// CodexEventToolResultRow deliberately has no fields for the raw arguments /
// output payloads: the parser consumes them and stores byte lengths only
// (privacy decision, see spec §2).
type CodexEventToolResultRow struct {
	CodexCommonAttrs
	ToolName        sql.NullString
	CallID          sql.NullString
	DurationMs      sql.NullInt64
	Success         sql.NullBool
	MCPServer       sql.NullString
	MCPServerOrigin sql.NullString
	ArgumentsLength sql.NullInt64
	OutputLength    sql.NullInt64
}
