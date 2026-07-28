package otlp

import (
	"fmt"
	"math"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	mpb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// numberDataPointAsInt extracts an int64 value. Falls back to double->int cast
// if the wire format is unexpectedly AsDouble.
func numberDataPointAsInt(dp *mpb.NumberDataPoint) (int64, bool) {
	switch v := dp.Value.(type) {
	case *mpb.NumberDataPoint_AsInt:
		return v.AsInt, true
	case *mpb.NumberDataPoint_AsDouble:
		return int64(v.AsDouble), true
	}
	return 0, false
}

func numberDataPointAsDouble(dp *mpb.NumberDataPoint) (float64, bool) {
	switch v := dp.Value.(type) {
	case *mpb.NumberDataPoint_AsDouble:
		return v.AsDouble, true
	case *mpb.NumberDataPoint_AsInt:
		return float64(v.AsInt), true
	}
	return 0, false
}

// dataPointTimestamps returns (ts, start_ts) in UTC.
func dataPointTimestamps(dp *mpb.NumberDataPoint) (time.Time, time.Time) {
	return time.Unix(0, int64(dp.TimeUnixNano)).UTC(),
		time.Unix(0, int64(dp.StartTimeUnixNano)).UTC()
}

func histogramDataPointTimestamps(dp *mpb.HistogramDataPoint) (time.Time, time.Time) {
	return time.Unix(0, int64(dp.TimeUnixNano)).UTC(),
		time.Unix(0, int64(dp.StartTimeUnixNano)).UTC()
}

func parseCodexResponseTBT(dp *mpb.HistogramDataPoint, resourceAttrs []*commonpb.KeyValue) (CodexMetricResponseTBTRow, error) {
	if dp.Sum == nil {
		return CodexMetricResponseTBTRow{}, fmt.Errorf("codex response tbt: missing sum")
	}
	if math.IsNaN(*dp.Sum) || math.IsInf(*dp.Sum, 0) {
		return CodexMetricResponseTBTRow{}, fmt.Errorf("codex response tbt: sum is not finite")
	}
	if dp.Count > uint64(1<<63-1) {
		return CodexMetricResponseTBTRow{}, fmt.Errorf("codex response tbt: count overflows int64")
	}
	m := newAttrMap(resourceAttrs, dp.Attributes)
	ts, startTs := histogramDataPointTimestamps(dp)
	row := CodexMetricResponseTBTRow{
		CodexCommonAttrs: extractCodexCommonAttrs(m, ts),
		StartTimestamp:   startTs,
		SampleCount:      int64(dp.Count),
		SumMs:            *dp.Sum,
		SessionSource:    m.takeString("session_source"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseCodexSkillInjected(dp *mpb.NumberDataPoint, resourceAttrs []*commonpb.KeyValue) (CodexMetricSkillInjectedRow, error) {
	m := newAttrMap(resourceAttrs, dp.Attributes)
	ts, startTs := dataPointTimestamps(dp)
	value, ok := numberDataPointAsInt(dp)
	if !ok {
		return CodexMetricSkillInjectedRow{}, fmt.Errorf("codex skill injected: value not int-like")
	}
	row := CodexMetricSkillInjectedRow{
		CodexCommonAttrs: extractCodexCommonAttrs(m, ts),
		StartTimestamp:   startTs,
		Value:            value,
		Skill:            m.takeString("skill"),
		Status:           m.takeString("status"),
		InvokeType:       m.takeString("invoke_type"),
		SessionSource:    m.takeString("session_source"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseSessionCount(dp *mpb.NumberDataPoint, resourceAttrs []*commonpb.KeyValue) (MetricSessionCountRow, error) {
	m := newAttrMap(resourceAttrs, dp.Attributes)
	ts, startTs := dataPointTimestamps(dp)
	common, err := extractCommonAttrs(m, ts)
	if err != nil {
		return MetricSessionCountRow{}, fmt.Errorf("session_count: %w", err)
	}
	value, ok := numberDataPointAsInt(dp)
	if !ok {
		return MetricSessionCountRow{}, fmt.Errorf("session_count: value not int-like")
	}
	row := MetricSessionCountRow{
		CommonAttrs:    common,
		StartTimestamp: startTs,
		Value:          value,
		StartType:      m.takeString("start_type"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseLinesOfCode(dp *mpb.NumberDataPoint, resourceAttrs []*commonpb.KeyValue) (MetricLinesOfCodeCountRow, error) {
	m := newAttrMap(resourceAttrs, dp.Attributes)
	ts, startTs := dataPointTimestamps(dp)
	common, err := extractCommonAttrs(m, ts)
	if err != nil {
		return MetricLinesOfCodeCountRow{}, fmt.Errorf("lines_of_code: %w", err)
	}
	value, ok := numberDataPointAsInt(dp)
	if !ok {
		return MetricLinesOfCodeCountRow{}, fmt.Errorf("lines_of_code: value not int-like")
	}
	row := MetricLinesOfCodeCountRow{
		CommonAttrs:    common,
		StartTimestamp: startTs,
		Value:          value,
		Type:           m.takeString("type"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parsePullRequest(dp *mpb.NumberDataPoint, resourceAttrs []*commonpb.KeyValue) (MetricPullRequestCountRow, error) {
	m := newAttrMap(resourceAttrs, dp.Attributes)
	ts, startTs := dataPointTimestamps(dp)
	common, err := extractCommonAttrs(m, ts)
	if err != nil {
		return MetricPullRequestCountRow{}, fmt.Errorf("pull_request: %w", err)
	}
	value, ok := numberDataPointAsInt(dp)
	if !ok {
		return MetricPullRequestCountRow{}, fmt.Errorf("pull_request: value not int-like")
	}
	row := MetricPullRequestCountRow{
		CommonAttrs:    common,
		StartTimestamp: startTs,
		Value:          value,
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseCommit(dp *mpb.NumberDataPoint, resourceAttrs []*commonpb.KeyValue) (MetricCommitCountRow, error) {
	m := newAttrMap(resourceAttrs, dp.Attributes)
	ts, startTs := dataPointTimestamps(dp)
	common, err := extractCommonAttrs(m, ts)
	if err != nil {
		return MetricCommitCountRow{}, fmt.Errorf("commit: %w", err)
	}
	value, ok := numberDataPointAsInt(dp)
	if !ok {
		return MetricCommitCountRow{}, fmt.Errorf("commit: value not int-like")
	}
	row := MetricCommitCountRow{
		CommonAttrs:    common,
		StartTimestamp: startTs,
		Value:          value,
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseCostUsage(dp *mpb.NumberDataPoint, resourceAttrs []*commonpb.KeyValue) (MetricCostUsageRow, error) {
	m := newAttrMap(resourceAttrs, dp.Attributes)
	ts, startTs := dataPointTimestamps(dp)
	common, err := extractCommonAttrs(m, ts)
	if err != nil {
		return MetricCostUsageRow{}, fmt.Errorf("cost_usage: %w", err)
	}
	value, ok := numberDataPointAsDouble(dp)
	if !ok {
		return MetricCostUsageRow{}, fmt.Errorf("cost_usage: value not double-like")
	}
	row := MetricCostUsageRow{
		CommonAttrs:     common,
		StartTimestamp:  startTs,
		Value:           value,
		Model:           m.takeString("model"),
		QuerySource:     m.takeString("query_source"),
		Speed:           m.takeString("speed"),
		Effort:          m.takeString("effort"),
		AgentName:       m.takeString("agent.name"),
		SkillName:       m.takeString("skill.name"),
		PluginName:      m.takeString("plugin.name"),
		MarketplaceName: m.takeString("marketplace.name"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseTokenUsage(dp *mpb.NumberDataPoint, resourceAttrs []*commonpb.KeyValue) (MetricTokenUsageRow, error) {
	m := newAttrMap(resourceAttrs, dp.Attributes)
	ts, startTs := dataPointTimestamps(dp)
	common, err := extractCommonAttrs(m, ts)
	if err != nil {
		return MetricTokenUsageRow{}, fmt.Errorf("token_usage: %w", err)
	}
	value, ok := numberDataPointAsInt(dp)
	if !ok {
		return MetricTokenUsageRow{}, fmt.Errorf("token_usage: value not int-like")
	}
	row := MetricTokenUsageRow{
		CommonAttrs:     common,
		StartTimestamp:  startTs,
		Value:           value,
		Type:            m.takeString("type"),
		Model:           m.takeString("model"),
		QuerySource:     m.takeString("query_source"),
		Speed:           m.takeString("speed"),
		Effort:          m.takeString("effort"),
		AgentName:       m.takeString("agent.name"),
		SkillName:       m.takeString("skill.name"),
		PluginName:      m.takeString("plugin.name"),
		MarketplaceName: m.takeString("marketplace.name"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseCodeEditDecision(dp *mpb.NumberDataPoint, resourceAttrs []*commonpb.KeyValue) (MetricCodeEditToolDecisionRow, error) {
	m := newAttrMap(resourceAttrs, dp.Attributes)
	ts, startTs := dataPointTimestamps(dp)
	common, err := extractCommonAttrs(m, ts)
	if err != nil {
		return MetricCodeEditToolDecisionRow{}, fmt.Errorf("code_edit_decision: %w", err)
	}
	value, ok := numberDataPointAsInt(dp)
	if !ok {
		return MetricCodeEditToolDecisionRow{}, fmt.Errorf("code_edit_decision: value not int-like")
	}
	row := MetricCodeEditToolDecisionRow{
		CommonAttrs:    common,
		StartTimestamp: startTs,
		Value:          value,
		ToolName:       m.takeString("tool_name"),
		Decision:       m.takeString("decision"),
		Source:         m.takeString("source"),
		Language:       m.takeString("language"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseActiveTime(dp *mpb.NumberDataPoint, resourceAttrs []*commonpb.KeyValue) (MetricActiveTimeTotalRow, error) {
	m := newAttrMap(resourceAttrs, dp.Attributes)
	ts, startTs := dataPointTimestamps(dp)
	common, err := extractCommonAttrs(m, ts)
	if err != nil {
		return MetricActiveTimeTotalRow{}, fmt.Errorf("active_time: %w", err)
	}
	value, ok := numberDataPointAsDouble(dp)
	if !ok {
		return MetricActiveTimeTotalRow{}, fmt.Errorf("active_time: value not double-like")
	}
	row := MetricActiveTimeTotalRow{
		CommonAttrs:    common,
		StartTimestamp: startTs,
		Value:          value,
		Type:           m.takeString("type"),
	}
	row.Attrs = m.leftover()
	return row, nil
}
