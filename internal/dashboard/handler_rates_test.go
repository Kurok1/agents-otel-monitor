/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"encoding/json"
	"net/http/httptest"
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

// 编译期断言:*pricing.Engine 满足 PriceLookup(main.go 直接注入引擎的契约)。
var _ PriceLookup = (*pricing.Engine)(nil)
