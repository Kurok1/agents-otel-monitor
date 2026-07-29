/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// throughputTypes is the fixed wire order of the stacked throughput series.
var throughputTypes = []string{"input", "output", "cache_read", "cache_creation"}

const realtimeSpeedWindow = 2 * time.Minute

// BuildRealtimeSpeed assembles the lightweight speed KPI polled by the
// dashboard. It scans only the trailing four minutes and returns adjacent
// two-minute weighted windows, avoiding the historical bucket queries in
// BuildRates.
func BuildRealtimeSpeed(
	ctx context.Context,
	db *sql.DB,
	now time.Time,
	client Client,
) (RealtimeSpeedResponse, error) {
	asOf := now.UTC()
	resp := RealtimeSpeedResponse{
		Client:        client,
		WindowSeconds: int(realtimeSpeedWindow / time.Second),
		AsOf:          asOf.Format(time.RFC3339),
	}
	// Claude request duration and Codex native TBT are incompatible speed
	// methods. The all-client view therefore has no scalar KPI and can return
	// without touching either telemetry table.
	if client == ClientAll {
		return resp, nil
	}
	split := asOf.Add(-realtimeSpeedWindow)
	windows, err := QuerySpeedWindowPair(
		ctx,
		db,
		client,
		speedWindowBounds{
			Start: split.Add(-realtimeSpeedWindow),
			Split: split,
			End:   asOf,
		},
	)
	if err != nil {
		return resp, fmt.Errorf("build realtime speed: %w", err)
	}
	resp.Current = windowTokPerSec(windows.Current, client)
	resp.Previous = windowTokPerSec(windows.Previous, client)
	return resp, nil
}

// BuildRates assembles /api/usage/rates: per-bucket weighted speed by model
// group, whole-window speed KPIs, and per-bucket throughput by token type.
func BuildRates(ctx context.Context, db *sql.DB, c *Classifier, w TimeWindow, rng string, client Client) (RatesResponse, error) {
	spec, err := w.ResolveRates(rng)
	if err != nil {
		return RatesResponse{}, err
	}
	resp := RatesResponse{Range: spec.Range, BucketInterval: spec.IntervalLabel}

	// ── 生成速度:按 (桶, 组) 合并分子分母后再除(加权平均可无损合并) ──
	speedRows, err := QuerySpeedBuckets(ctx, db, client, spec.Start, spec.End)
	if err != nil {
		return resp, err
	}
	type cellKey struct {
		idx   int
		group string
	}
	type cellAgg struct {
		units, dur float64
	}
	type classifiedSpeedRow struct {
		speedBucketRow
		group string
	}
	classified := make([]classifiedSpeedRow, 0, len(speedRows))
	groupSources := make(map[string]map[Client]struct{})
	for _, r := range speedRows {
		g := c.Classify(r.Model)
		if g == "" {
			continue
		}
		classified = append(classified, classifiedSpeedRow{speedBucketRow: r, group: g})
		sources := groupSources[g]
		if sources == nil {
			sources = make(map[Client]struct{}, 1)
			groupSources[g] = sources
		}
		sources[r.Client] = struct{}{}
	}

	cells := make(map[cellKey]*cellAgg)
	groupWeight := make(map[string]float64)
	for _, r := range classified {
		g := r.group
		if len(groupSources[g]) > 1 {
			if r.Client == ClientCodex {
				g += " · Codex"
			} else {
				g += " · Claude"
			}
		}
		idx := spec.BucketIndex(r.Hour)
		if idx < 0 {
			continue
		}
		k := cellKey{idx: idx, group: g}
		a := cells[k]
		if a == nil {
			a = &cellAgg{}
			cells[k] = a
		}
		a.units += r.Units
		a.dur += r.DurMs
		groupWeight[g] += r.Units
	}

	groups := make([]string, 0, len(groupWeight))
	for g := range groupWeight {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groupWeight[groups[i]] != groupWeight[groups[j]] {
			return groupWeight[groups[i]] > groupWeight[groups[j]]
		}
		return groups[i] < groups[j]
	})

	speedPoints := make([]RatesPoint, 0, spec.Count)
	for i := 0; i < spec.Count; i++ {
		bucketStart := spec.Start.Add(time.Duration(i) * spec.Interval)
		values := make(map[string]float64, len(groups))
		for _, g := range groups {
			if a, ok := cells[cellKey{idx: i, group: g}]; ok && a.dur > 0 {
				values[g] = a.units * 1000 / a.dur
			}
		}
		speedPoints = append(speedPoints, ratesPointAt(bucketStart, spec.Interval, w.Loc, values))
	}

	var current, previous *float64
	if client == ClientClaude || client == ClientCodex {
		cur, err := QuerySpeedWindow(ctx, db, client, spec.Start, spec.End)
		if err != nil {
			return resp, err
		}
		prevStart := spec.Start.Add(-time.Duration(spec.Count) * spec.Interval)
		prev, err := QuerySpeedWindow(ctx, db, client, prevStart, spec.Start)
		if err != nil {
			return resp, err
		}
		current = windowTokPerSec(cur, client)
		previous = windowTokPerSec(prev, client)
	}
	resp.Speed = SpeedBlock{
		Groups:   groups,
		Points:   speedPoints,
		Current:  current,
		Previous: previous,
	}

	// ── 吞吐率:小时行落桶累加,末桶按实际流逝分钟归一 ──
	thrRows, err := QueryThroughputBuckets(ctx, db, client, spec.Start, spec.End)
	if err != nil {
		return resp, err
	}
	thrCells := make([]throughputBucketRow, spec.Count)
	for _, r := range thrRows {
		idx := spec.BucketIndex(r.Hour)
		if idx < 0 {
			continue
		}
		thrCells[idx].In += r.In
		thrCells[idx].Out += r.Out
		thrCells[idx].CacheRead += r.CacheRead
		thrCells[idx].CacheCreation += r.CacheCreation
	}
	thrPoints := make([]RatesPoint, 0, spec.Count)
	for i := 0; i < spec.Count; i++ {
		bucketStart := spec.Start.Add(time.Duration(i) * spec.Interval)
		mins := spec.Interval.Minutes()
		if elapsed := spec.End.Sub(bucketStart); elapsed < spec.Interval {
			mins = elapsed.Minutes()
			if mins < 1 {
				mins = 1 // 桶刚开始时避免分母趋零导致数值爆炸
			}
		}
		values := map[string]float64{
			"input":          float64(thrCells[i].In) / mins,
			"output":         float64(thrCells[i].Out) / mins,
			"cache_read":     float64(thrCells[i].CacheRead) / mins,
			"cache_creation": float64(thrCells[i].CacheCreation) / mins,
		}
		thrPoints = append(thrPoints, ratesPointAt(bucketStart, spec.Interval, w.Loc, values))
	}
	resp.Throughput = ThroughputBlock{Types: throughputTypes, Points: thrPoints}
	return resp, nil
}

// windowTokPerSec converts one source's numerator/denominator into tok/s.
// Claude request speed and Codex native TBT are deliberately not compared or
// merged, so all-client views never expose the whole-window KPI.
func windowTokPerSec(sw speedWindow, client Client) *float64 {
	if client != ClientClaude && client != ClientCodex {
		return nil
	}
	if sw.DurMs <= 0 || sw.Sources != 1 {
		return nil
	}
	v := sw.Units * 1000 / sw.DurMs
	return &v
}

// ratesPointAt renders one bucket: RFC3339 UTC ts + local display label.
// Sub-day buckets label as "HH:00", switching to "M/D" at local midnight so
// 48h charts keep day context; day buckets always label "M/D".
func ratesPointAt(bucketStart time.Time, interval time.Duration, loc *time.Location, values map[string]float64) RatesPoint {
	local := bucketStart.In(loc)
	var label string
	if interval >= 24*time.Hour || local.Hour() == 0 {
		label = fmt.Sprintf("%d/%d", int(local.Month()), local.Day())
	} else {
		label = fmt.Sprintf("%02d:00", local.Hour())
	}
	return RatesPoint{
		Ts:     bucketStart.UTC().Format(time.RFC3339),
		Label:  label,
		Values: values,
	}
}
