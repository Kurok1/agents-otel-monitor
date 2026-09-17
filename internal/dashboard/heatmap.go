package dashboard

/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v1.6.0
 */

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// BuildHeatmap assembles the fixed 360-day usage heatmap. It reuses the
// day-grain sparkline queries (tokens SUM / cost SUM / requests COUNT) over
// the trailing 360-local-day window, gap-fills every day to a contiguous
// series, then computes each day's composite Score normalized against the
// window max for each metric (see HeatmapResponse doc). Queries are
// sequential — DuckDB MaxOpenConns=1 makes parallelism pointless.
func BuildHeatmap(ctx context.Context, db *sql.DB, w TimeWindow, weights HeatmapWeights, client Client, pricingEnabled bool) (HeatmapResponse, error) {
	resp := HeatmapResponse{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Days:      heatmapDays,
		Timezone:  w.Loc.String(),
		Weights:   weights,
	}

	start, end := w.HeatmapStartUTC, w.TodayEndUTC

	type buckets struct {
		tokens   []periodBucket
		costs    []periodCostBucket
		requests []periodBucket
	}
	data, err := withDashboardSnapshot(ctx, db, func(q sqlQueryer) (buckets, error) {
		tokens, err := QueryTokensSparkline(ctx, q, client, w, "day", start, end)
		if err != nil {
			return buckets{}, err
		}
		costs, err := QueryCostSparkline(ctx, q, client, w, "day", start, end)
		if err != nil {
			return buckets{}, err
		}
		requests, err := QueryRequestsSparkline(ctx, q, client, w, "day", start, end)
		if err != nil {
			return buckets{}, err
		}
		return buckets{tokens, costs, requests}, nil
	})
	if err != nil {
		return resp, err
	}
	tokBuckets, costBuckets, reqBuckets := data.tokens, data.costs, data.requests

	byTok := make(map[time.Time]int64, len(tokBuckets))
	for _, b := range tokBuckets {
		byTok[b.Bucket.UTC()] = b.Total
	}
	byCost := make(map[time.Time]float64, len(costBuckets))
	for _, b := range costBuckets {
		byCost[b.Bucket.UTC()] = b.Cost
	}
	byReq := make(map[time.Time]int64, len(reqBuckets))
	for _, b := range reqBuckets {
		byReq[b.Bucket.UTC()] = b.Total
	}

	points := make([]HeatmapPoint, 0, heatmapDays)
	var maxTok, maxReq int64
	var maxCost float64
	d := w.HeatmapStartUTC.In(w.Loc)
	for i := 0; i < heatmapDays; i++ {
		// DuckDB CAST(... AS DATE) scans as UTC-midnight of that calendar
		// day; key the gap-fill the same way (matches fillTokensSparkline).
		key := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
		tok, cost, req := byTok[key], byCost[key], byReq[key]
		if tok > maxTok {
			maxTok = tok
		}
		if cost > maxCost {
			maxCost = cost
		}
		if req > maxReq {
			maxReq = req
		}
		points = append(points, HeatmapPoint{
			Date:     fmt.Sprintf("%04d-%02d-%02d", d.Year(), d.Month(), d.Day()),
			Tokens:   tok,
			Cost:     cost,
			Requests: req,
		})
		d = d.AddDate(0, 0, 1)
	}

	// Codex cost is estimated and only present when pricing is enabled. When
	// pricing is off, codex cost is always NULL/0, so drop the cost weight to
	// avoid systematically depressing codex-only scores (stage-two behavior).
	// When pricing is on, keep all three weights so codex matches all/claude.
	wsum := weights.Tokens + weights.Cost + weights.Requests
	if client == ClientCodex && !pricingEnabled {
		wsum = weights.Tokens + weights.Requests
	}
	for i := range points {
		if wsum <= 0 {
			break // validated > 0 at config load; defensive guard
		}
		nt := normFrac(float64(points[i].Tokens), float64(maxTok))
		nc := normFrac(points[i].Cost, maxCost)
		nr := normFrac(float64(points[i].Requests), float64(maxReq))
		points[i].Score = (weights.Tokens*nt + weights.Cost*nc + weights.Requests*nr) / wsum
	}

	resp.Points = points
	return resp, nil
}

// normFrac maps v into [0,1] against max, with min pinned at 0. max <= 0 → 0.
func normFrac(v, max float64) float64 {
	if max <= 0 {
		return 0
	}
	return v / max
}
