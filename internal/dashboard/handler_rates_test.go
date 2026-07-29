/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/pricing"
)

func newRatesTestHandler(t *testing.T, pricingEnabled bool) *Handler {
	t.Helper()
	db, _, _ := testDB(t)
	h, err := NewHandler(db, config.DashboardConfig{
		Timezone: "Asia/Shanghai",
		TopN:     config.TopNConfig{Tools: 10, Skills: 10},
	}, pricingEnabled, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func TestHandleRatesRouting(t *testing.T) {
	h := newRatesTestHandler(t, false)

	// 非法 range → 400
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rates?range=year", nil))
	if rec.Code != 400 {
		t.Errorf("invalid range status = %d, want 400", rec.Code)
	}

	// 非法 client → 400
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rates?client=gemini", nil))
	if rec.Code != 400 {
		t.Errorf("invalid client status = %d, want 400", rec.Code)
	}

	// 缺省参数 → 200,48 桶
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rates", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp RatesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Range != "day" || resp.BucketInterval != "1h" || len(resp.Speed.Points) != 48 {
		t.Errorf("resp = range=%s interval=%s points=%d", resp.Range, resp.BucketInterval, len(resp.Speed.Points))
	}
}

func TestHandleRealtimeSpeedUsesTwoMinuteWindows(t *testing.T) {
	db, _, _ := testDB(t)
	h, err := NewHandler(db, config.DashboardConfig{
		Timezone: "Asia/Shanghai",
		TopN:     config.TopNConfig{Tools: 10, Skills: 10},
	}, false, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	now := time.Now().UTC()
	insertRateCodexTBT(t, db, codexTBTFixture{
		Timestamp: now.Add(-30 * time.Second), Model: "gpt-5.6-sol", SampleCount: 2, SumMs: 40,
	})
	insertRateCodexTBT(t, db, codexTBTFixture{
		Timestamp: now.Add(-3 * time.Minute), Model: "gpt-5.6-sol", SampleCount: 1, SumMs: 25,
	})
	insertRateCodexTBT(t, db, codexTBTFixture{
		Timestamp: now.Add(-5 * time.Minute), Model: "gpt-5.6-sol", SampleCount: 100, SumMs: 100,
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rates/realtime?client=codex", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Client        Client   `json:"client"`
		WindowSeconds int      `json:"window_seconds"`
		AsOf          string   `json:"as_of"`
		Current       *float64 `json:"current"`
		Previous      *float64 `json:"previous"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Client != ClientCodex || resp.WindowSeconds != 120 {
		t.Errorf("metadata = client=%q window=%d, want codex/120", resp.Client, resp.WindowSeconds)
	}
	if resp.Current == nil || *resp.Current != 50 {
		t.Errorf("current = %v, want 50 tok/s", resp.Current)
	}
	if resp.Previous == nil || *resp.Previous != 40 {
		t.Errorf("previous = %v, want 40 tok/s", resp.Previous)
	}
	asOf, err := time.Parse(time.RFC3339, resp.AsOf)
	if err != nil {
		t.Fatalf("as_of = %q, want RFC3339: %v", resp.AsOf, err)
	}
	if delta := time.Since(asOf); delta < 0 || delta > 5*time.Second {
		t.Errorf("as_of age = %v, want 0..5s", delta)
	}
}

func TestHandleRealtimeSpeedReturnsNullWithoutSamples(t *testing.T) {
	h := newRatesTestHandler(t, false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rates/realtime?client=codex", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Current  *float64 `json:"current"`
		Previous *float64 `json:"previous"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Current != nil || resp.Previous != nil {
		t.Errorf("speed = current:%v previous:%v, want null/null", resp.Current, resp.Previous)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rates/realtime?client=gemini", nil))
	if rec.Code != 400 {
		t.Errorf("invalid client status = %d, want 400", rec.Code)
	}
}

func TestHandlePricingModelsDisabledAndEnabled(t *testing.T) {
	// 未接 PriceLookup(或 pricing.enabled=false)→ 200 + enabled:false
	h := newRatesTestHandler(t, false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/pricing/models", nil))
	if rec.Code != 200 {
		t.Fatalf("disabled status = %d, want 200", rec.Code)
	}
	var resp PricingModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Enabled || resp.Models == nil || len(resp.Models) != 0 {
		t.Errorf("disabled resp = %+v, want enabled=false models=[]", resp)
	}

	// 接上 PriceLookup 且 enabled → 200 + enabled:true
	h2 := newRatesTestHandler(t, true)
	h2.SetPriceLookup(fakePriceLookup{table: map[string]pricing.ModelPrice{}})
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, httptest.NewRequest("GET", "/api/pricing/models?client=claude", nil))
	if rec.Code != 200 {
		t.Fatalf("enabled status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Enabled {
		t.Error("want enabled=true")
	}

	// 非法 client → 400
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, httptest.NewRequest("GET", "/api/pricing/models?client=x", nil))
	if rec.Code != 400 {
		t.Errorf("invalid client status = %d, want 400", rec.Code)
	}
}

func TestHandlePricingCatalogSupportsPrefixAndPagination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	err := os.WriteFile(path, []byte(`{
		"gpt-5.6-terra":{"input_cost_per_token":0.0000025,"output_cost_per_token":0.000015},
		"claude-opus-4-8":{"input_cost_per_token":0.000005,"output_cost_per_token":0.000025},
		"gpt-5.6-sol":{"input_cost_per_token":0.000005,"output_cost_per_token":0.00003}
	}`), 0o600)
	if err != nil {
		t.Fatalf("write price table: %v", err)
	}
	engine, err := pricing.NewEngine(config.PricingConfig{Enabled: true, SourceFile: path}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	h := newRatesTestHandler(t, true)
	h.SetPriceLookup(engine)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(
		"GET",
		"/api/pricing/catalog?prefix=GPT-5.6&offset=1&limit=1",
		nil,
	))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Enabled      bool `json:"enabled"`
		TableEntries int  `json:"table_entries"`
		TotalMatches int  `json:"total_matches"`
		Offset       int  `json:"offset"`
		Limit        int  `json:"limit"`
		Models       []struct {
			Model      string   `json:"model"`
			InputPer1M *float64 `json:"input_per_1m"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Enabled || resp.TableEntries != 3 || resp.TotalMatches != 2 {
		t.Fatalf("metadata = %+v, want enabled entries=3 matches=2", resp)
	}
	if resp.Offset != 1 || resp.Limit != 1 {
		t.Fatalf("page metadata = offset:%d limit:%d, want 1/1", resp.Offset, resp.Limit)
	}
	if len(resp.Models) != 1 || resp.Models[0].Model != "gpt-5.6-terra" {
		t.Fatalf("models = %+v, want second alphabetic GPT-5.6 model", resp.Models)
	}
	if resp.Models[0].InputPer1M == nil || *resp.Models[0].InputPer1M != 2.5 {
		t.Fatalf("input_per_1m = %v, want 2.5", resp.Models[0].InputPer1M)
	}
}

func TestHandlePricingModelsFiltersSeenModelsByPrefix(t *testing.T) {
	db, w, _ := testDB(t)
	at := w.TodayStartUTC.Add(time.Hour)
	insertApiRequest(t, db, at, "claude-opus-4-8")
	insertRateCodexUsage(t, db, at.Add(time.Minute), "gpt-5.6-sol", 100, 50, 0, 1000)

	h, err := NewHandler(db, config.DashboardConfig{
		Timezone: "Asia/Shanghai",
		TopN:     config.TopNConfig{Tools: 10, Skills: 10},
	}, true, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	h.SetPriceLookup(fakePriceLookup{table: map[string]pricing.ModelPrice{
		"claude-opus-4-8": {},
		"gpt-5.6-sol":     {},
	}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(
		"GET",
		"/api/pricing/models?prefix=GPT-5.6",
		nil,
	))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp PricingModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Models) != 1 || resp.Models[0].Model != "gpt-5.6-sol" {
		t.Fatalf("models = %+v, want only gpt-5.6-sol", resp.Models)
	}
}

func TestHandleSnapshotIncludesEstimatedModelCostBreakdown(t *testing.T) {
	db, _, _ := testDB(t)
	_, err := db.Exec(`
		INSERT INTO codex_event_token_usage
			(ts, conversation_id, model, input_token_count, output_token_count,
			 cached_token_count, reasoning_token_count, cost_usd)
		VALUES (CURRENT_TIMESTAMP, 'conv-costs', 'gpt-5.6-sol', 1000, 500, 200, 100, 0.00195)
	`)
	if err != nil {
		t.Fatalf("insert codex usage: %v", err)
	}

	h, err := NewHandler(db, config.DashboardConfig{
		Timezone: "Asia/Shanghai",
		TopN:     config.TopNConfig{Tools: 10, Skills: 10},
	}, true, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	h.SetPriceLookup(fakePriceLookup{table: map[string]pricing.ModelPrice{
		"gpt-5.6-sol": {
			InputCostPerToken:           f64(1e-6),
			OutputCostPerToken:          f64(2e-6),
			CacheReadInputTokenCost:     f64(0.25e-6),
			OutputCostPerReasoningToken: f64(3e-6),
		},
	}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(
		"GET",
		"/api/usage/snapshot?range=day&client=codex",
		nil,
	))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Models []struct {
			Group         string   `json:"group"`
			InputCost     *float64 `json:"input_cost"`
			OutputCost    *float64 `json:"output_cost"`
			CacheReadCost *float64 `json:"cache_read_cost"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Models) != 1 || resp.Models[0].Group != "gpt-5.6-sol" {
		t.Fatalf("models = %+v, want gpt-5.6-sol", resp.Models)
	}
	model := resp.Models[0]
	if model.InputCost == nil {
		t.Error("input_cost = nil, want 0.0008")
	} else if diff := *model.InputCost - 0.0008; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("input_cost = %.12f, want 0.0008", *model.InputCost)
	}
	if model.OutputCost == nil {
		t.Error("output_cost = nil, want 0.0011")
	} else if diff := *model.OutputCost - 0.0011; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("output_cost = %.12f, want 0.0011", *model.OutputCost)
	}
	if model.CacheReadCost == nil {
		t.Error("cache_read_cost = nil, want 0.00005")
	} else if diff := *model.CacheReadCost - 0.00005; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("cache_read_cost = %.12f, want 0.00005", *model.CacheReadCost)
	}
}

// 编译期断言:*pricing.Engine 满足 PriceLookup(main.go 直接注入引擎的契约)。
var _ PriceLookup = (*pricing.Engine)(nil)
