package otlp

import (
	"errors"
	"log/slog"
	"strings"

	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	lpb "go.opentelemetry.io/proto/otlp/logs/v1"
	mpb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/kuroky/claude-code-monitor/internal/pricing"
)

// DispatchSummary is what the dispatcher returns from each Dispatch call.
// It is suitable for logging at Info level once per Export request.
type DispatchSummary struct {
	MetricRows map[string]int // metric name → rows successfully parsed
	EventRows  map[string]int // event.name → rows successfully parsed
	Unknown    map[string]int // names we have no parser for
	Skipped    map[string]int // recognized but intentionally not persisted (e.g. non-completed codex.sse_event)
	Errors     int            // parse failures (data shape unexpected etc.)
}

type Dispatcher struct {
	log     *slog.Logger
	sink    Sink
	pricing *pricing.Engine
}

func NewDispatcher(log *slog.Logger, sink Sink, engine *pricing.Engine) *Dispatcher {
	return &Dispatcher{log: log, sink: sink, pricing: engine}
}

// enrichCodexCost fills r.CostUsd from the pricing engine. No-op when engine is
// nil (pricing not wired) — CostFor itself no-ops when pricing is disabled.
func enrichCodexCost(r *CodexEventTokenUsageRow, engine *pricing.Engine) {
	if engine == nil {
		return
	}
	r.CostUsd = engine.CostFor(r.Model.String, pricing.TokenCounts{
		Input:     r.InputTokenCount.Int64,
		Output:    r.OutputTokenCount.Int64,
		Cached:    r.CachedTokenCount.Int64,
		Reasoning: r.ReasoningTokenCount.Int64,
		Tool:      r.ToolTokenCount.Int64,
	})
}

var (
	errUnknownMetric = errors.New("unknown metric")
	errUnknownEvent  = errors.New("unknown event")
	errSkippedEvent  = errors.New("skipped event")
)

const (
	codexSkillInjectedMetricName = "codex.skill.injected"
	codexServiceTBTMetricName    = "codex.responses_api_engine_service_tbt.duration_ms"
)

func (d *Dispatcher) DispatchMetrics(req *metricspb.ExportMetricsServiceRequest) DispatchSummary {
	summary := DispatchSummary{
		MetricRows: make(map[string]int),
		Unknown:    make(map[string]int),
	}
	for _, rm := range req.ResourceMetrics {
		resourceAttrs := rm.Resource.GetAttributes()
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == codexSkillInjectedMetricName {
					sum, ok := m.Data.(*mpb.Metric_Sum)
					if !ok || sum.Sum == nil ||
						sum.Sum.AggregationTemporality != mpb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA ||
						!sum.Sum.IsMonotonic {
						d.log.Warn("metric parse failed", "metric", m.Name, "err", "expected monotonic delta sum")
						summary.Errors++
						continue
					}
				}
				if m.Name == codexServiceTBTMetricName {
					histogram, ok := m.Data.(*mpb.Metric_Histogram)
					if !ok || histogram.Histogram == nil ||
						histogram.Histogram.AggregationTemporality != mpb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA {
						d.log.Warn("metric parse failed", "metric", m.Name, "err", "expected delta histogram")
						summary.Errors++
						continue
					}
					for _, dp := range histogram.Histogram.DataPoints {
						r, err := parseCodexResponseTBT(dp, resourceAttrs)
						if err == nil {
							err = d.sink.AppendMetric(r)
						}
						if err != nil {
							d.log.Warn("metric parse failed", "metric", m.Name, "err", err)
							summary.Errors++
							continue
						}
						summary.MetricRows[m.Name]++
					}
					continue
				}
				points, ok := metricNumberPoints(m)
				if !ok {
					summary.Unknown[m.Name]++
					continue
				}
				for _, dp := range points {
					err := d.dispatchMetric(m.Name, dp, resourceAttrs)
					switch {
					case errors.Is(err, errUnknownMetric):
						summary.Unknown[m.Name]++
					case err != nil:
						d.log.Warn("metric parse failed", "metric", m.Name, "err", err)
						summary.Errors++
					default:
						summary.MetricRows[m.Name]++
					}
				}
			}
		}
	}
	return summary
}

func (d *Dispatcher) DispatchLogs(req *logspb.ExportLogsServiceRequest) DispatchSummary {
	summary := DispatchSummary{
		EventRows: make(map[string]int),
		Unknown:   make(map[string]int),
		Skipped:   make(map[string]int),
	}
	for _, rl := range req.ResourceLogs {
		resourceAttrs := rl.Resource.GetAttributes()
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				name := lookupAttr(lr.Attributes, "event.name")
				if name == "" {
					name = lr.EventName // OTLP >= 1.4 top-level field (defensive; codex sets the attribute)
				}
				if name == "" {
					summary.Unknown["<no_event_name>"]++
					continue
				}
				err := d.dispatchEvent(name, lr, resourceAttrs)
				switch {
				case errors.Is(err, errUnknownEvent):
					summary.Unknown[name]++
				case errors.Is(err, errSkippedEvent):
					summary.Skipped[name]++
				case err != nil:
					d.log.Warn("event parse failed", "event", name, "err", err)
					summary.Errors++
				default:
					summary.EventRows[name]++
				}
			}
		}
	}
	return summary
}

// metricNumberPoints returns the data points slice if the metric is a Sum or
// Gauge. Histograms/Summaries return false so the dispatcher records them as
// Unknown (we do not parse those types).
func metricNumberPoints(m *mpb.Metric) ([]*mpb.NumberDataPoint, bool) {
	switch d := m.Data.(type) {
	case *mpb.Metric_Sum:
		return d.Sum.DataPoints, true
	case *mpb.Metric_Gauge:
		return d.Gauge.DataPoints, true
	}
	return nil, false
}

func (d *Dispatcher) dispatchMetric(name string, dp *mpb.NumberDataPoint, resourceAttrs []*commonpb.KeyValue) error {
	switch name {
	case codexSkillInjectedMetricName:
		r, err := parseCodexSkillInjected(dp, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendMetric(r)
	case "claude_code.session.count":
		r, err := parseSessionCount(dp, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendMetric(r)
	case "claude_code.lines_of_code.count":
		r, err := parseLinesOfCode(dp, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendMetric(r)
	case "claude_code.pull_request.count":
		r, err := parsePullRequest(dp, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendMetric(r)
	case "claude_code.commit.count":
		r, err := parseCommit(dp, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendMetric(r)
	case "claude_code.cost.usage":
		r, err := parseCostUsage(dp, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendMetric(r)
	case "claude_code.token.usage":
		r, err := parseTokenUsage(dp, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendMetric(r)
	case "claude_code.code_edit_tool.decision":
		r, err := parseCodeEditDecision(dp, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendMetric(r)
	case "claude_code.active_time.total":
		r, err := parseActiveTime(dp, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendMetric(r)
	}
	return errUnknownMetric
}

func (d *Dispatcher) dispatchEvent(name string, rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) error {
	if strings.HasPrefix(name, "codex.") {
		return d.dispatchCodexEvent(name, rec, resourceAttrs)
	}
	switch name {
	case "user_prompt":
		r, err := parseUserPrompt(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "api_request":
		r, err := parseApiRequest(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "api_error":
		r, err := parseApiError(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "tool_result":
		r, err := parseToolResult(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "tool_decision":
		r, err := parseToolDecision(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "api_retries_exhausted":
		r, err := parseApiRetriesExhausted(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "compaction":
		r, err := parseCompaction(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "permission_mode_changed":
		r, err := parsePermissionModeChanged(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "mcp_server_connection":
		r, err := parseMCPServerConnection(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "skill_activated":
		r, err := parseSkillActivated(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "at_mention":
		r, err := parseAtMention(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	}
	return errUnknownEvent
}

// dispatchCodexEvent routes the codex.* event family (spec:
// docs/superpowers/specs/2026-07-01-codex-otel-support-design.md). Only the
// 6 core-usage events are persisted; sse_event is filtered to
// response.completed, everything else is counted as skipped or unknown.
func (d *Dispatcher) dispatchCodexEvent(name string, rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) error {
	switch name {
	case "codex.conversation_starts":
		r, err := parseCodexConversationStarts(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "codex.api_request":
		r, err := parseCodexApiRequest(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "codex.sse_event":
		if lookupAttr(rec.Attributes, "event.kind") != "response.completed" {
			return errSkippedEvent // high-volume stream events carry no usage data
		}
		r, err := parseCodexTokenUsage(rec, resourceAttrs)
		if err != nil {
			return err
		}
		enrichCodexCost(&r, d.pricing)
		return d.sink.AppendEvent(r)
	case "codex.user_prompt":
		r, err := parseCodexUserPrompt(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "codex.tool_decision":
		r, err := parseCodexToolDecision(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "codex.tool_result":
		r, err := parseCodexToolResult(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	}
	return errUnknownEvent // other codex.* events are out of scope for v1
}

func lookupAttr(attrs []*commonpb.KeyValue, key string) string {
	for _, a := range attrs {
		if a.Key == key {
			return asString(a.Value)
		}
	}
	return ""
}
