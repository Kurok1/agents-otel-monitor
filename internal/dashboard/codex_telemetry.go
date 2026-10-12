/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"
)

type TTFTSummary struct {
	Samples int64    `json:"samples"`
	MeanMs  *float64 `json:"mean_ms"`
	P50Ms   *int64   `json:"p50_ms"`
	P95Ms   *int64   `json:"p95_ms"`
}

type CodexUsageCheck struct {
	TokenType       string   `json:"token_type"`
	ResponseTokens  *float64 `json:"response_tokens"`
	TurnTokens      *float64 `json:"turn_tokens"`
	ResponseSamples int64    `json:"response_samples"`
	TurnSamples     int64    `json:"turn_samples"`
	Difference      *float64 `json:"difference"` // turn minus response, never a correction
}

type CodexModelTelemetry struct {
	Model string            `json:"model"`
	TTFT  TTFTSummary       `json:"ttft"`
	Usage []CodexUsageCheck `json:"usage"`
}

type CodexTelemetryResponse struct {
	Range  string                `json:"range"`
	TTFT   TTFTSummary           `json:"ttft"`
	Models []CodexModelTelemetry `json:"models"`
}

type ttftFrequency struct{ Milliseconds, Count int64 }

// Quantiles are calculated after merging frequencies, so archival preserves
// exactly the same nearest-rank result as reading individual observations.
func summarizeTTFT(frequencies []ttftFrequency) TTFTSummary {
	result := TTFTSummary{}
	var sum float64
	for _, f := range frequencies {
		result.Samples += f.Count
		sum += float64(f.Milliseconds) * float64(f.Count)
	}
	if result.Samples == 0 {
		return result
	}
	result.MeanMs = float64Ptr(sum / float64(result.Samples))
	sort.Slice(frequencies, func(i, j int) bool { return frequencies[i].Milliseconds < frequencies[j].Milliseconds })
	rank50 := int64(math.Ceil(float64(result.Samples) * 0.50))
	rank95 := int64(math.Ceil(float64(result.Samples) * 0.95))
	var count int64
	for _, f := range frequencies {
		count += f.Count
		if result.P50Ms == nil && count >= rank50 {
			value := f.Milliseconds
			result.P50Ms = &value
		}
		if result.P95Ms == nil && count >= rank95 {
			value := f.Milliseconds
			result.P95Ms = &value
		}
	}
	return result
}

func BuildCodexTelemetry(ctx context.Context, db *sql.DB, window TimeWindow, rng string) (CodexTelemetryResponse, error) {
	spec, err := window.Resolve(rng)
	if err != nil {
		return CodexTelemetryResponse{}, err
	}
	return withDashboardSnapshot(ctx, db, func(q sqlQueryer) (CodexTelemetryResponse, error) {
		return queryCodexTelemetry(ctx, q, spec)
	})
}

func queryCodexTelemetry(ctx context.Context, db sqlQueryer, spec WindowSpec) (CodexTelemetryResponse, error) {
	result := CodexTelemetryResponse{Range: spec.Range, Models: make([]CodexModelTelemetry, 0)}
	first, last, _ := archiveBounds(spec.CurrentStart, spec.CurrentEnd)
	args := []any{spec.CurrentStart, spec.CurrentEnd, first, last}
	models := make(map[string]*CodexModelTelemetry)
	frequencies := make(map[string][]ttftFrequency)
	getModel := func(model string) *CodexModelTelemetry {
		if value, ok := models[model]; ok {
			return value
		}
		value := &CodexModelTelemetry{Model: model, Usage: make([]CodexUsageCheck, 0, 6)}
		models[model] = value
		return value
	}
	rows, err := db.QueryContext(ctx, `SELECT COALESCE(model,''), ttft_ms, COUNT(*) FROM codex_valid_token_usage
 WHERE ts>=? AND ts<? AND ttft_ms>=0 GROUP BY 1,2
 UNION ALL SELECT model, ttft_ms, sample_count FROM archive.codex_ttft_histogram WHERE bucket_start>=? AND bucket_start<?`, args...)
	if err != nil {
		return result, fmt.Errorf("query codex TTFT: %w", err)
	}
	for rows.Next() {
		var model string
		var f ttftFrequency
		if err := rows.Scan(&model, &f.Milliseconds, &f.Count); err != nil {
			_ = rows.Close()
			return result, fmt.Errorf("scan codex TTFT: %w", err)
		}
		getModel(model)
		frequencies[model] = append(frequencies[model], f)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return result, fmt.Errorf("iterate codex TTFT: %w", err)
	}
	if err := rows.Close(); err != nil {
		return result, fmt.Errorf("close codex TTFT: %w", err)
	}

	types := []string{"input", "output", "cached_input", "cache_write_input", "reasoning_output", "total"}
	checks := make(map[string]map[string]*CodexUsageCheck)
	getCheck := func(model, typ string) *CodexUsageCheck {
		getModel(model)
		if checks[model] == nil {
			checks[model] = make(map[string]*CodexUsageCheck)
		}
		if checks[model][typ] == nil {
			checks[model][typ] = &CodexUsageCheck{TokenType: typ}
		}
		return checks[model][typ]
	}
	rows, err = db.QueryContext(ctx, `SELECT COALESCE(model,''), SUM(input_token_count), SUM(output_token_count),
 SUM(COALESCE(cached_token_count,0)), SUM(COALESCE(cache_write_token_count,0)), SUM(COALESCE(reasoning_token_count,0)),
 SUM(input_token_count+output_token_count), COUNT(*), COUNT(cached_token_count), COUNT(cache_write_token_count), COUNT(reasoning_token_count)
 FROM codex_valid_token_usage WHERE ts>=? AND ts<? GROUP BY 1
 UNION ALL SELECT model, SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens), SUM(cache_creation_tokens),
 SUM(reasoning_tokens), SUM(tokens_total), SUM(request_count), SUM(cached_input_samples), SUM(cache_write_input_samples), SUM(reasoning_output_samples)
 FROM archive.usage_hourly
 WHERE client='codex' AND token_rows>0 AND bucket_start>=? AND bucket_start<? GROUP BY 1`, args...)
	if err != nil {
		return result, fmt.Errorf("query response token checks: %w", err)
	}
	for rows.Next() {
		var model string
		var values [6]float64
		var count int64
		var cachedCount, writeCount, reasoningCount int64
		if err := rows.Scan(&model, &values[0], &values[1], &values[2], &values[3], &values[4], &values[5], &count, &cachedCount, &writeCount, &reasoningCount); err != nil {
			_ = rows.Close()
			return result, fmt.Errorf("scan response token checks: %w", err)
		}
		counts := [6]int64{count, count, cachedCount, writeCount, reasoningCount, count}
		for i, typ := range types {
			check := getCheck(model, typ)
			if counts[i] == 0 {
				continue
			}
			if check.ResponseTokens == nil {
				check.ResponseTokens = float64Ptr(0)
			}
			*check.ResponseTokens += values[i]
			check.ResponseSamples += counts[i]
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return result, fmt.Errorf("iterate response token checks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return result, fmt.Errorf("close response token checks: %w", err)
	}
	rows, err = db.QueryContext(ctx, `SELECT COALESCE(model,''), token_type, SUM(sum_tokens), SUM(sample_count)
 FROM codex_metric_turn_token_usage WHERE ts>=? AND ts<? GROUP BY 1,2
 UNION ALL SELECT model, token_type, SUM(sum_tokens), SUM(sample_count)
 FROM archive.codex_turn_usage_hourly WHERE bucket_start>=? AND bucket_start<? GROUP BY 1,2`, args...)
	if err != nil {
		return result, fmt.Errorf("query turn token checks: %w", err)
	}
	for rows.Next() {
		var model, typ string
		var tokens float64
		var count int64
		if err := rows.Scan(&model, &typ, &tokens, &count); err != nil {
			_ = rows.Close()
			return result, fmt.Errorf("scan turn token checks: %w", err)
		}
		check := getCheck(model, typ)
		if check.TurnTokens == nil {
			check.TurnTokens = float64Ptr(0)
		}
		*check.TurnTokens += tokens
		check.TurnSamples += count
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return result, fmt.Errorf("iterate turn token checks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return result, fmt.Errorf("close turn token checks: %w", err)
	}
	all := make([]ttftFrequency, 0)
	for name, model := range models {
		all = append(all, frequencies[name]...)
		model.TTFT = summarizeTTFT(frequencies[name])
		for _, check := range checks[name] {
			if check.TurnTokens != nil && check.ResponseTokens != nil {
				check.Difference = float64Ptr(*check.TurnTokens - *check.ResponseTokens)
			}
			model.Usage = append(model.Usage, *check)
		}
		sort.Slice(model.Usage, func(i, j int) bool { return model.Usage[i].TokenType < model.Usage[j].TokenType })
		result.Models = append(result.Models, *model)
	}
	result.TTFT = summarizeTTFT(all)
	sort.Slice(result.Models, func(i, j int) bool { return result.Models[i].Model < result.Models[j].Model })
	return result, nil
}

func (h *Handler) handleCodexTelemetry(w http.ResponseWriter, r *http.Request) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "day"
	}
	window, err := NowWindow(time.Now(), h.cfg.Timezone)
	if err == nil {
		var result CodexTelemetryResponse
		result, err = BuildCodexTelemetry(r.Context(), h.db, window, rng)
		if err == nil {
			writeJSON(w, http.StatusOK, result)
			return
		}
	}
	if isUserError(err) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.log.Error("codex telemetry query failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
