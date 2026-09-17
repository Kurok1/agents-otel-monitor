package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// BuildTrends runs the trends query and pivots rows into the response shape.
// Bucket count matches the design: day → 14, week → 12, month → 12.
//
// Groups are derived by the Classifier; the legend order is by total tokens
// across the window (descending), tie-broken alphabetically. All groups are
// included — no "other" filter — so third-party models render alongside
// Claude family buckets.
func BuildTrends(ctx context.Context, db *sql.DB, c *Classifier, w TimeWindow, rng string, client Client) (TrendsResponse, error) {
	grain, start, count, err := trendsParams(w, rng)
	if err != nil {
		return TrendsResponse{}, err
	}

	rows, err := withDashboardSnapshot(ctx, db, func(q sqlQueryer) ([]trendRow, error) {
		return QueryTrends(ctx, q, client, w, grain, start)
	})
	if err != nil {
		return TrendsResponse{}, err
	}

	type key struct {
		bucket time.Time
		group  string
	}
	cell := map[key]int64{}
	groupTotal := map[string]int64{}
	for _, r := range rows {
		g := c.Classify(r.Model)
		if g == "" {
			continue
		}
		k := key{bucket: r.Bucket.UTC(), group: g}
		cell[k] += r.Tokens
		groupTotal[g] += r.Tokens
	}

	groups := make([]string, 0, len(groupTotal))
	for g := range groupTotal {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groupTotal[groups[i]] != groupTotal[groups[j]] {
			return groupTotal[groups[i]] > groupTotal[groups[j]]
		}
		return groups[i] < groups[j]
	})

	points := make([]TrendsPoint, 0, count)
	d := start.In(w.Loc)
	for i := 0; i < count; i++ {
		bucketUTC := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC) // DuckDB DATE
		date, label := formatBucket(d, grain)
		values := make(map[string]int64, len(groups))
		for _, g := range groups {
			if v, ok := cell[key{bucketUTC, g}]; ok {
				values[g] = v
			}
		}
		points = append(points, TrendsPoint{
			Date:   date,
			Label:  label,
			Values: values,
		})
		d = advance(d, grain)
	}

	return TrendsResponse{Range: rng, Groups: groups, Points: points}, nil
}

func trendsParams(w TimeWindow, rng string) (grain string, start time.Time, count int, err error) {
	switch rng {
	case "day":
		return "day", w.DayTrendStartUTC, 14, nil
	case "week":
		return "week", w.WeekTrendStartUTC, 12, nil
	case "month":
		return "month", w.MonthTrendStartUTC, 12, nil
	default:
		return "", time.Time{}, 0, fmt.Errorf("invalid range %q: want day|week|month", rng)
	}
}

func advance(d time.Time, grain string) time.Time {
	switch grain {
	case "day":
		return d.AddDate(0, 0, 1)
	case "week":
		return d.AddDate(0, 0, 7)
	case "month":
		return d.AddDate(0, 1, 0)
	}
	return d
}

// formatBucket returns (date, label) for one bucket.
// date: machine-readable key — YYYY-MM-DD (day/week start) or YYYY-MM (month)
// label: human-friendly — "M/D" (day/week) or "M月" (month)
func formatBucket(d time.Time, grain string) (date, label string) {
	switch grain {
	case "month":
		return fmt.Sprintf("%04d-%02d", d.Year(), d.Month()),
			fmt.Sprintf("%d月", int(d.Month()))
	default:
		return fmt.Sprintf("%04d-%02d-%02d", d.Year(), d.Month(), d.Day()),
			fmt.Sprintf("%d/%d", int(d.Month()), d.Day())
	}
}
