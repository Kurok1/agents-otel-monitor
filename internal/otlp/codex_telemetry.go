/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

package otlp

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	lpb "go.opentelemetry.io/proto/otlp/logs/v1"
	mpb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// ValidUsage separates successful, measured completions from error events
// which share response.completed. Optional counts are subsets, not additions.
func (r CodexEventTokenUsageRow) ValidUsage() bool {
	if strings.TrimSpace(r.Error.String) != "" || !r.InputTokenCount.Valid || !r.OutputTokenCount.Valid {
		return false
	}
	in, out := r.InputTokenCount.Int64, r.OutputTokenCount.Int64
	read, write, reasoning := r.CachedTokenCount.Int64, r.CacheWriteTokenCount.Int64, r.ReasoningTokenCount.Int64
	return in >= 0 && out >= 0 && read >= 0 && write >= 0 && reasoning >= 0 &&
		read <= in && write <= in-read && reasoning <= out && out <= math.MaxInt64-in
}

func parseCodexSkillInvocation(rec *lpb.LogRecord, resource []*commonpb.KeyValue) (CodexEventSkillInvocationRow, error) {
	m, common := preparseCodexEvent(rec, resource)
	r := CodexEventSkillInvocationRow{CodexCommonAttrs: common,
		TurnID: m.takeString("turn.id"), SkillName: m.takeString("skill.name"),
		Scope: m.takeString("skill.scope"), PluginID: m.takeString("skill.plugin_id"),
		InvocationType: m.takeString("skill.invocation_type")}
	if strings.TrimSpace(r.ConversationID.String) == "" || strings.TrimSpace(r.TurnID.String) == "" || strings.TrimSpace(r.SkillName.String) == "" {
		return r, fmt.Errorf("codex skill invocation: missing conversation, turn or skill identity")
	}
	if r.InvocationType.String != "explicit" && r.InvocationType.String != "implicit" {
		return r, fmt.Errorf("codex skill invocation: unsupported invocation type")
	}
	r.Attrs = m.leftover()
	return r, nil
}

// DECIMAL(38,18) preserves native money before the existing float API boundary. Reject
// invalid/overlarge values rather than silently turning missing cost into zero.
var codexUSDPattern = regexp.MustCompile(`^[0-9]{1,20}(\.[0-9]{1,18})?$`)

func parseCodexTurnCost(rec *lpb.LogRecord, resource []*commonpb.KeyValue) (CodexEventTurnCostRow, error) {
	m, common := preparseCodexEvent(rec, resource)
	r := CodexEventTurnCostRow{CodexCommonAttrs: common,
		TurnID: m.takeString("turn.id"), EstimatedUSD: m.takeString("usage.estimated_usd"),
		Interrupted: m.takeBool("turn.interrupted"), Speed: m.takeString("speed"),
		ReasoningEffort: m.takeString("reasoning_effort")}
	if strings.TrimSpace(r.ConversationID.String) == "" || strings.TrimSpace(r.TurnID.String) == "" {
		return r, fmt.Errorf("codex turn cost: missing conversation or turn identity")
	}
	if !codexUSDPattern.MatchString(r.EstimatedUSD.String) {
		return r, fmt.Errorf("codex turn cost: invalid decimal USD")
	}
	r.Attrs = m.leftover()
	return r, nil
}

func parseCodexTurnTokenUsage(dp *mpb.HistogramDataPoint, resource []*commonpb.KeyValue) (CodexMetricTurnTokenUsageRow, error) {
	if dp.Sum == nil || math.IsNaN(dp.GetSum()) || math.IsInf(dp.GetSum(), 0) || dp.GetSum() < 0 || dp.Count > math.MaxInt64 {
		return CodexMetricTurnTokenUsageRow{}, fmt.Errorf("codex turn token usage: invalid histogram count or sum")
	}
	m := newAttrMap(resource, dp.Attributes)
	ts, start := histogramDataPointTimestamps(dp)
	r := CodexMetricTurnTokenUsageRow{CodexCommonAttrs: extractCodexCommonAttrs(m, ts),
		StartTimestamp: start, SampleCount: int64(dp.Count), SumTokens: dp.GetSum(),
		TokenType: m.takeString("token_type"), SessionSource: m.takeString("session_source")}
	if r.TokenType.String == "" {
		return r, fmt.Errorf("codex turn token usage: missing token_type")
	}
	r.Attrs = m.leftover()
	return r, nil
}

func (d *Dispatcher) dispatchCodexHistogram(name string, dp *mpb.HistogramDataPoint, resource []*commonpb.KeyValue) error {
	if name == codexTurnTokenUsageMetricName {
		r, err := parseCodexTurnTokenUsage(dp, resource)
		if err != nil {
			return err
		}
		return d.sink.AppendMetric(r)
	}
	r, err := parseCodexResponseTBT(dp, resource)
	if err != nil {
		return err
	}
	return d.sink.AppendMetric(r)
}
