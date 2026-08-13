/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

type periodModelRow struct {
	Model        string
	Requests     int64
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// PeriodModelsOptions groups the selected time window and filters for the
// compact menu-bar model breakdown.
type PeriodModelsOptions struct {
	Window         TimeWindow
	Range          string
	Client         Client
	PricingEnabled bool
}

type periodModelQuery struct {
	Client Client
	Start  time.Time
	End    time.Time
}

// BuildPeriodModels returns the model mix for the selected current period.
// It deliberately lives beside, rather than inside, BuildSnapshot so the
// dashboard's existing all-time model contract remains unchanged.
func BuildPeriodModels(
	ctx context.Context,
	db *sql.DB,
	classifier *Classifier,
	opts PeriodModelsOptions,
) (PeriodModelsResponse, error) {
	response := PeriodModelsResponse{
		UpdatedAt: opts.Window.NowUTC.Format(time.RFC3339),
		Client:    opts.Client,
		Models:    make([]PeriodModelBlock, 0),
	}
	spec, err := opts.Window.Resolve(opts.Range)
	if err != nil {
		return response, err
	}
	response.Range = spec.Range
	response.CostEstimated = opts.PricingEnabled && opts.Client.includesCodex()

	rows, err := queryPeriodModelRows(ctx, db, periodModelQuery{
		Client: opts.Client,
		Start:  spec.CurrentStart,
		End:    spec.CurrentEnd,
	})
	if err != nil {
		return response, err
	}
	type totals struct {
		requests     int64
		inputTokens  int64
		outputTokens int64
		costUSD      float64
	}
	byModel := make(map[string]*totals)
	for _, row := range rows {
		model := classifier.Classify(row.Model)
		if model == "" {
			continue
		}
		modelTotals, ok := byModel[model]
		if !ok {
			modelTotals = &totals{}
			byModel[model] = modelTotals
		}
		modelTotals.requests += row.Requests
		modelTotals.inputTokens += row.InputTokens
		modelTotals.outputTokens += row.OutputTokens
		modelTotals.costUSD += row.CostUSD
	}

	var allTokens int64
	for _, modelTotals := range byModel {
		allTokens += modelTotals.inputTokens + modelTotals.outputTokens
	}
	for model, modelTotals := range byModel {
		totalTokens := modelTotals.inputTokens + modelTotals.outputTokens
		share := 0.0
		if allTokens > 0 {
			share = float64(totalTokens) / float64(allTokens)
		}
		response.Models = append(response.Models, PeriodModelBlock{
			Model:        model,
			Requests:     modelTotals.requests,
			InputTokens:  modelTotals.inputTokens,
			OutputTokens: modelTotals.outputTokens,
			TotalTokens:  totalTokens,
			CostUSD:      modelTotals.costUSD,
			Share:        share,
		})
	}
	sort.Slice(response.Models, func(i, j int) bool {
		if response.Models[i].TotalTokens != response.Models[j].TotalTokens {
			return response.Models[i].TotalTokens > response.Models[j].TotalTokens
		}
		return response.Models[i].Model < response.Models[j].Model
	})
	return response, nil
}

func queryPeriodModelRows(
	ctx context.Context,
	db *sql.DB,
	params periodModelQuery,
) ([]periodModelRow, error) {
	rows := make([]periodModelRow, 0)
	if params.Client.includesClaude() {
		const query = `
			SELECT model,
			       CAST(SUM(requests) AS BIGINT) AS requests,
			       CAST(SUM(input_tokens) AS BIGINT) AS input_tokens,
			       CAST(SUM(output_tokens) AS BIGINT) AS output_tokens,
			       SUM(cost_usd) AS cost_usd
			FROM (
			  SELECT model,
			         0 AS requests,
			         CASE WHEN type = 'input' THEN value ELSE 0 END AS input_tokens,
			         CASE WHEN type = 'output' THEN value ELSE 0 END AS output_tokens,
			         0.0 AS cost_usd
			  FROM metric_token_usage
			  WHERE ts >= ? AND ts < ? AND model IS NOT NULL
			  UNION ALL
			  SELECT model, 0, 0, 0, value
			  FROM metric_cost_usage
			  WHERE ts >= ? AND ts < ? AND model IS NOT NULL
			  UNION ALL
			  SELECT model, 1, 0, 0, 0.0
			  FROM event_api_request
			  WHERE ts >= ? AND ts < ? AND model IS NOT NULL
			) usage
			GROUP BY model
		`
		queryRows, err := db.QueryContext(
			ctx,
			query,
			params.Start,
			params.End,
			params.Start,
			params.End,
			params.Start,
			params.End,
		)
		if err != nil {
			return nil, fmt.Errorf("query period models (claude): %w", err)
		}
		claudeRows, err := scanPeriodModelRows(queryRows, "claude")
		if err != nil {
			return nil, err
		}
		rows = append(rows, claudeRows...)
	}
	if params.Client.includesCodex() {
		const query = `
			SELECT model,
			       COUNT(*) AS requests,
			       CAST(SUM(COALESCE(input_token_count, 0)) AS BIGINT) AS input_tokens,
			       CAST(SUM(COALESCE(output_token_count, 0)) AS BIGINT) AS output_tokens,
			       SUM(COALESCE(cost_usd, 0)) AS cost_usd
			FROM codex_event_token_usage
			WHERE ts >= ? AND ts < ? AND model IS NOT NULL
			GROUP BY model
		`
		queryRows, err := db.QueryContext(ctx, query, params.Start, params.End)
		if err != nil {
			return nil, fmt.Errorf("query period models (codex): %w", err)
		}
		codexRows, err := scanPeriodModelRows(queryRows, "codex")
		if err != nil {
			return nil, err
		}
		rows = append(rows, codexRows...)
	}
	return rows, nil
}

func scanPeriodModelRows(queryRows *sql.Rows, source string) (rows []periodModelRow, err error) {
	defer func() {
		if closeErr := queryRows.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close period models (%s): %w", source, closeErr)
		}
	}()
	rows = make([]periodModelRow, 0)
	for queryRows.Next() {
		var row periodModelRow
		if err := queryRows.Scan(
			&row.Model,
			&row.Requests,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CostUSD,
		); err != nil {
			return nil, fmt.Errorf("scan period models (%s): %w", source, err)
		}
		rows = append(rows, row)
	}
	if err := queryRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate period models (%s): %w", source, err)
	}
	return rows, nil
}
