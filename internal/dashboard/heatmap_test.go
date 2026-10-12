package dashboard

/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v1.6.0
 */

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

func findPoint(points []HeatmapPoint, date string) (HeatmapPoint, bool) {
	for _, p := range points {
		if p.Date == date {
			return p, true
		}
	}
	return HeatmapPoint{}, false
}

func approx(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }

func TestBuildHeatmap_ShapeAndGapFill(t *testing.T) {
	db, w, _ := testDB(t)                                    // now = 2026-05-13 10:00 SH; today = 2026-05-13
	day1 := w.TodayStartUTC.Add(time.Hour)                   // SH 2026-05-13
	day2 := w.TodayStartUTC.Add(-3*24*time.Hour + time.Hour) // SH 2026-05-10

	insertTokenUsage(t, db, day1, "claude-opus-4-1", "input", 100)
	insertCostUsage(t, db, day1, "claude-opus-4-1", 8.0)
	for i := 0; i < 4; i++ {
		insertApiRequest(t, db, day1, "claude-opus-4-1")
	}
	insertTokenUsage(t, db, day2, "claude-opus-4-1", "input", 50)
	insertCostUsage(t, db, day2, "claude-opus-4-1", 4.0)
	for i := 0; i < 2; i++ {
		insertApiRequest(t, db, day2, "claude-opus-4-1")
	}

	resp, err := BuildHeatmap(context.Background(), db, w,
		HeatmapWeights{Tokens: 0.4, Cost: 0.4, Requests: 0.2}, ClientAll, false)
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	if len(resp.Points) != 360 {
		t.Fatalf("len(points) = %d, want 360", len(resp.Points))
	}
	if resp.Points[0].Date != "2025-05-19" {
		t.Errorf("first date = %q, want 2025-05-19", resp.Points[0].Date)
	}
	if resp.Points[359].Date != "2026-05-13" {
		t.Errorf("last date = %q, want 2026-05-13", resp.Points[359].Date)
	}

	p1, ok := findPoint(resp.Points, "2026-05-13")
	if !ok || p1.Tokens != 100 || p1.Cost != 8.0 || p1.Requests != 4 {
		t.Fatalf("2026-05-13 = %+v, want tokens=100 cost=8 reqs=4", p1)
	}
	if !approx(p1.Score, 1.0) { // all three metrics at window max
		t.Errorf("2026-05-13 score = %v, want 1.0", p1.Score)
	}

	p2, _ := findPoint(resp.Points, "2026-05-10")
	if p2.Tokens != 50 || p2.Cost != 4.0 || p2.Requests != 2 {
		t.Fatalf("2026-05-10 = %+v, want tokens=50 cost=4 reqs=2", p2)
	}
	if !approx(p2.Score, 0.5) { // half of each max → 0.5 with any weights
		t.Errorf("2026-05-10 score = %v, want 0.5", p2.Score)
	}

	gap, ok := findPoint(resp.Points, "2026-05-12") // inside window, no data
	if !ok || gap.Tokens != 0 || gap.Cost != 0 || gap.Requests != 0 || gap.Score != 0 {
		t.Errorf("gap day 2026-05-12 = %+v, want all-zero", gap)
	}
}

func TestBuildHeatmap_WeightScaleInvariant(t *testing.T) {
	db, w, _ := testDB(t)
	peak := w.TodayStartUTC.Add(time.Hour)                  // 2026-05-13 = window max
	day := w.TodayStartUTC.Add(-2*24*time.Hour + time.Hour) // 2026-05-11 = half of max
	insertTokenUsage(t, db, peak, "m", "input", 100)
	insertCostUsage(t, db, peak, "m", 10)
	for i := 0; i < 10; i++ {
		insertApiRequest(t, db, peak, "m")
	}
	insertTokenUsage(t, db, day, "m", "input", 50)
	insertCostUsage(t, db, day, "m", 5)
	for i := 0; i < 5; i++ {
		insertApiRequest(t, db, day, "m")
	}

	a, _ := BuildHeatmap(context.Background(), db, w, HeatmapWeights{Tokens: 0.4, Cost: 0.4, Requests: 0.2}, ClientAll, false)
	b, _ := BuildHeatmap(context.Background(), db, w, HeatmapWeights{Tokens: 2, Cost: 2, Requests: 1}, ClientAll, false)
	pa, _ := findPoint(a.Points, "2026-05-11")
	pb, _ := findPoint(b.Points, "2026-05-11")
	if !approx(pa.Score, pb.Score) {
		t.Errorf("weight-scale variance: %v vs %v", pa.Score, pb.Score)
	}
	if !approx(pa.Score, 0.5) {
		t.Errorf("score = %v, want 0.5", pa.Score)
	}
}

func TestBuildHeatmap_AllCostAvailability(t *testing.T) {
	for _, tc := range []struct {
		name         string
		claude       bool
		codex        bool
		observedZero bool
		wantScore    float64
	}{
		{name: "claude unknown", claude: true, wantScore: 1},
		{name: "codex unknown", codex: true, wantScore: 1},
		{name: "both unknown", claude: true, codex: true, wantScore: 1},
		{name: "claude observed zero", claude: true, observedZero: true, wantScore: 0.6},
		{name: "codex observed zero", codex: true, observedZero: true, wantScore: 0.6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, w, _ := testDB(t)
			at := w.TodayStartUTC.Add(time.Hour)
			if tc.claude {
				insertTokenUsage(t, db, at, "claude-test", "input", 100)
				insertApiRequest(t, db, at, "claude-test")
				if tc.observedZero {
					insertCostUsage(t, db, at, "claude-test", 0)
				}
			}
			if tc.codex {
				insertCodexTokenUsage(t, db, at, "heatmap", "gpt-test", 100, 20, 0, 0)
				if tc.observedZero {
					seedNativeCost(t, db, at, "heatmap", "turn", "0")
				}
			}
			weights := HeatmapWeights{Tokens: 0.4, Cost: 0.4, Requests: 0.2}
			for _, pricingEnabled := range []bool{false, true} {
				resp, err := BuildHeatmap(context.Background(), db, w, weights, ClientAll, pricingEnabled)
				if err != nil {
					t.Fatalf("BuildHeatmap: %v", err)
				}
				point, found := findPoint(resp.Points, at.In(w.Loc).Format(time.DateOnly))
				if !found || point.Tokens == 0 || point.Requests == 0 || point.Cost != 0 || !approx(point.Score, tc.wantScore) {
					t.Errorf("pricing_enabled=%v: point=%+v, want score=%v", pricingEnabled, point, tc.wantScore)
				}
			}
		})
	}
}

func TestHandler_Heatmap_Route(t *testing.T) {
	db, _, _ := testDB(t)
	insertTokenUsage(t, db, time.Now().UTC(), "claude-opus-4-1", "input", 10)

	h, err := NewHandler(db, config.DashboardConfig{
		Timezone: "Asia/Shanghai",
		Heatmap:  config.HeatmapConfig{WTokens: 0.4, WCost: 0.4, WRequests: 0.2},
	}, false, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/usage/heatmap", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp HeatmapResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Points) != 360 || resp.Days != 360 {
		t.Errorf("days=%d points=%d, want 360/360", resp.Days, len(resp.Points))
	}
	if resp.Weights.Tokens != 0.4 {
		t.Errorf("weights echoed = %+v", resp.Weights)
	}
}
