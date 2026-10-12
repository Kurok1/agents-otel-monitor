/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

package otlp

import (
	"encoding/base64"
	"math"
	"os"
	"strings"
	"testing"

	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	lpb "go.opentelemetry.io/proto/otlp/logs/v1"
	mpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

func loadCodex162Fixture(t *testing.T, kind string, target proto.Message) {
	t.Helper()
	encoded, err := os.ReadFile("testdata/codex_0_162_1_" + kind + ".pb.b64")
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func capturedCodexRecord(t *testing.T, name string, measured bool) *lpb.LogRecord {
	t.Helper()
	req := &logspb.ExportLogsServiceRequest{}
	loadCodex162Fixture(t, "logs", req)
	for _, resource := range req.ResourceLogs {
		for _, scope := range resource.ScopeLogs {
			for _, rec := range scope.LogRecords {
				if newAttrMap(nil, rec.Attributes).takeString("event.name").String != name {
					continue
				}
				if measured {
					r, err := parseCodexTokenUsage(rec, nil)
					if err != nil || !r.ValidUsage() {
						continue
					}
				}
				return rec
			}
		}
	}
	t.Fatalf("real fixture has no %s record", name)
	return nil
}

func setCapturedAttribute(rec *lpb.LogRecord, key string, value *commonpb.AnyValue) {
	for i, kv := range rec.Attributes {
		if kv.Key != key {
			continue
		}
		if value == nil {
			rec.Attributes = append(rec.Attributes[:i], rec.Attributes[i+1:]...)
		} else {
			kv.Value = value
		}
		return
	}
	if value != nil {
		rec.Attributes = append(rec.Attributes, &commonpb.KeyValue{Key: key, Value: value})
	}
}
func capturedString(value string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}
}

func TestCodex162RealLogsGolden(t *testing.T) {
	req := &logspb.ExportLogsServiceRequest{}
	loadCodex162Fixture(t, "logs", req)
	sink := &NoopSink{}
	summary := NewDispatcher(quietLogger(), sink, nil).DispatchLogs(req)
	if summary.Errors != 0 || summary.EventRows["codex.skill_invocation"] != 1 || summary.EventRows["codex.turn_cost"] != 1 {
		t.Fatalf("dispatch: %+v", summary)
	}
	var valid, unmeasured, tool int
	for _, event := range sink.Events {
		switch r := event.(type) {
		case CodexEventTokenUsageRow:
			if !r.ValidUsage() {
				unmeasured++
				continue
			}
			valid++
			if r.InputTokenCount.Int64 != 100 || r.OutputTokenCount.Int64 != 20 || r.CacheWriteTokenCount.Int64 != 30 || !r.TTFTMs.Valid || r.TTFTMs.Int64 < 0 {
				t.Fatalf("measured usage: %+v", r)
			}
		case CodexEventTurnCostRow:
			if r.EstimatedUSD.String != "1.250001" || r.TurnID.String == "" || !r.Interrupted.Valid || r.Interrupted.Bool {
				t.Fatalf("native cost: %+v", r)
			}
		case CodexEventSkillInvocationRow:
			if r.SkillName.String != "fixture" || r.InvocationType.String != "explicit" || r.Scope.String != "repo" {
				t.Fatalf("skill: %+v", r)
			}
		case CodexEventToolResultRow:
			tool++
			if !r.Success.Bool || !r.OutputTruncated.Valid || !r.OutputTruncated.Bool || r.OutputLength.Int64 <= 0 {
				t.Fatalf("tool: %+v", r)
			}
			for _, key := range []string{"arguments", "output", "output_truncated"} {
				if _, exists := r.Attrs[key]; exists {
					t.Fatalf("tool payload leaked in attrs: %s", key)
				}
			}
		}
	}
	if valid != 4 || unmeasured != 4 || tool != 2 {
		t.Fatalf("valid=%d unmeasured=%d tools=%d", valid, unmeasured, tool)
	}
}

func TestCodex162CompletionAndNativeCostBranches(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		remove, valid    bool
	}{
		{"missing-input", "input_token_count", "", true, false},
		{"malformed-input", "input_token_count", "bad", false, false},
		{"negative-output", "output_token_count", "-1", false, false},
		{"error-with-counts", "error.message", "failed", false, false},
		{"cache-subsets-overlap", "cache_write_token_count", "100", false, false},
		{"reasoning-too-large", "reasoning_token_count", "21", false, false},
		{"legacy-no-write", "cache_write_token_count", "", true, true},
		{"missing-ttft", "ttft_ms", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := capturedCodexRecord(t, "codex.sse_event", true)
			value := capturedString(tc.value)
			if tc.remove {
				value = nil
			}
			setCapturedAttribute(rec, tc.key, value)
			r, err := parseCodexTokenUsage(rec, nil)
			if err != nil || r.ValidUsage() != tc.valid {
				t.Fatalf("usage=%+v err=%v", r, err)
			}
		})
	}
	zero := capturedCodexRecord(t, "codex.sse_event", true)
	for _, key := range []string{"input_token_count", "output_token_count", "cached_token_count", "cache_write_token_count", "reasoning_token_count", "ttft_ms"} {
		setCapturedAttribute(zero, key, capturedString("0"))
	}
	r, err := parseCodexTokenUsage(zero, nil)
	if err != nil || !r.ValidUsage() || !r.TTFTMs.Valid || r.TTFTMs.Int64 != 0 {
		t.Fatalf("valid zero lost: %+v %v", r, err)
	}
	for _, amount := range []string{"0", "0.000000000000000001", "-1", "NaN", "1e3", "", "999999999999999999999"} {
		rec := capturedCodexRecord(t, "codex.turn_cost", false)
		setCapturedAttribute(rec, "usage.estimated_usd", capturedString(amount))
		_, err := parseCodexTurnCost(rec, nil)
		valid := amount == "0" || amount == "0.000000000000000001"
		if (err == nil) != valid {
			t.Fatalf("native decimal %q err=%v", amount, err)
		}
	}
	skill := capturedCodexRecord(t, "codex.skill_invocation", false)
	setCapturedAttribute(skill, "skill.invocation_type", capturedString("implicit"))
	s, err := parseCodexSkillInvocation(skill, nil)
	if err != nil || s.InvocationType.String != "implicit" {
		t.Fatalf("implicit: %+v %v", s, err)
	}
	setCapturedAttribute(skill, "turn.id", nil)
	if _, err := parseCodexSkillInvocation(skill, nil); err == nil {
		t.Fatal("missing turn accepted")
	}
	tool := capturedCodexRecord(t, "codex.tool_result", false)
	setCapturedAttribute(tool, "output_truncated", nil)
	setCapturedAttribute(tool, "success", capturedString("false"))
	tr, err := parseCodexToolResult(tool, nil)
	if err != nil || tr.OutputTruncated.Valid || tr.Success.Bool {
		t.Fatalf("legacy/error tool: %+v %v", tr, err)
	}
}

func TestCodex162RealTurnMetricGolden(t *testing.T) {
	req := &metricspb.ExportMetricsServiceRequest{}
	loadCodex162Fixture(t, "metrics", req)
	sink := &NoopSink{}
	summary := NewDispatcher(quietLogger(), sink, nil).DispatchMetrics(req)
	if summary.Errors != 0 || summary.MetricRows[codexTurnTokenUsageMetricName] != 6 {
		t.Fatalf("metrics: %+v", summary)
	}
	want := map[string]float64{"total": 240, "input": 200, "cached_input": 40, "cache_write_input": 60, "output": 40, "reasoning_output": 10}
	for _, metric := range sink.Metrics {
		if r, ok := metric.(CodexMetricTurnTokenUsageRow); ok {
			if r.SumTokens != want[r.TokenType.String] || r.SampleCount != 1 {
				t.Fatalf("turn metric: %+v", r)
			}
			delete(want, r.TokenType.String)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing types: %v", want)
	}
	for _, resource := range req.ResourceMetrics {
		for _, scope := range resource.ScopeMetrics {
			for _, m := range scope.Metrics {
				if m.Name != codexTurnTokenUsageMetricName {
					continue
				}
				hist := m.GetHistogram()
				for _, value := range []float64{-1, math.NaN(), math.Inf(1)} {
					dp := proto.Clone(hist.DataPoints[0]).(*mpb.HistogramDataPoint)
					dp.Sum = &value
					if _, err := parseCodexTurnTokenUsage(dp, nil); err == nil {
						t.Fatalf("accepted sum %v", value)
					}
				}
				hist.AggregationTemporality = mpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE
			}
		}
	}
	sink = &NoopSink{}
	summary = NewDispatcher(quietLogger(), sink, nil).DispatchMetrics(req)
	if summary.MetricRows[codexTurnTokenUsageMetricName] != 0 || summary.Errors == 0 {
		t.Fatalf("cumulative accepted: %+v", summary)
	}
}
