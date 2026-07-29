/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.4.0
 */

package pricing

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

func writeTempJSON(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "litellm.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp json: %v", err)
	}
	return p
}

func TestEngineDisabledIsNoop(t *testing.T) {
	e, err := NewEngine(config.PricingConfig{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if v := e.CostFor("gpt-4o", TokenCounts{Input: 1000, Output: 1000}); v.Valid {
		t.Fatal("disabled engine must return invalid NullFloat64")
	}
}

func TestEngineFileLoadAndCost(t *testing.T) {
	path := writeTempJSON(t, `{"gpt-4o":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`)
	e, err := NewEngine(config.PricingConfig{Enabled: true, SourceFile: path}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	v := e.CostFor("gpt-4o", TokenCounts{Input: 1000, Output: 1000})
	want := 1000*0.000001 + 1000*0.000002
	if !v.Valid {
		t.Fatal("expected valid cost")
	}
	if diff := v.Float64 - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost = %v, want %v", v.Float64, want)
	}
	// unmatched → invalid + counted.
	if v := e.CostFor("mystery-model", TokenCounts{Input: 1}); v.Valid {
		t.Fatal("unmatched must be invalid")
	}
	if e.Stats().Unmatched["mystery-model"] == 0 {
		t.Fatal("unmatched counter not incremented")
	}
}

func TestEngineFailFastOnBadFile(t *testing.T) {
	if _, err := NewEngine(config.PricingConfig{Enabled: true, SourceFile: "/no/such/file.json"}, nil); err == nil {
		t.Fatal("expected fail-fast when source_file unreadable")
	}
}

func TestEngineOverridesWinOverFile(t *testing.T) {
	path := writeTempJSON(t, `{"gpt-4o":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`)
	over := 0.000009
	cfg := config.PricingConfig{
		Enabled:    true,
		SourceFile: path,
		Overrides:  map[string]config.PriceOverride{"gpt-4o": {InputCostPerToken: &over, OutputCostPerToken: &over}},
	}
	e, err := NewEngine(cfg, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	v := e.CostFor("gpt-4o", TokenCounts{Input: 1000})
	if diff := v.Float64 - 1000*over; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("override should win: %v", v.Float64)
	}
}

func TestPriceForExactAndNormalized(t *testing.T) {
	path := writeTempJSON(t, `{
		"gpt-5.1":{"input_cost_per_token":0.00000125,"output_cost_per_token":0.00001,"cache_read_input_token_cost":0.000000125},
		"claude-opus-4-8":{"input_cost_per_token":0.000015,"output_cost_per_token":0.000075}
	}`)
	e, err := NewEngine(config.PricingConfig{Enabled: true, SourceFile: path}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// 精确匹配
	p, ok := e.PriceFor("claude-opus-4-8")
	if !ok {
		t.Fatal("exact match: want ok=true")
	}
	if p.InputCostPerToken == nil || *p.InputCostPerToken != 0.000015 {
		t.Fatalf("exact match input rate = %v, want 0.000015", p.InputCostPerToken)
	}

	// 归一化匹配：去 provider/ 前缀 + 去 -YYYY-MM-DD 日期后缀
	if _, ok := e.PriceFor("openai/gpt-5.1-2025-11-13"); !ok {
		t.Fatal("normalized match: want ok=true")
	}

	// 未匹配：ok=false 且【不得】计入 unmatched 统计（只读探测不污染 ingest 观测）
	if _, ok := e.PriceFor("mystery-model-x"); ok {
		t.Fatal("unmatched: want ok=false")
	}
	if n := e.Stats().Unmatched["mystery-model-x"]; n != 0 {
		t.Fatalf("PriceFor must not record unmatched, got count %d", n)
	}
}

func TestPriceForDisabled(t *testing.T) {
	e, err := NewEngine(config.PricingConfig{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, ok := e.PriceFor("gpt-5.1"); ok {
		t.Fatal("disabled engine: want ok=false")
	}
}

func TestSearchPricesUsesCaseInsensitivePrefixAndPagination(t *testing.T) {
	path := writeTempJSON(t, `{
		"gpt-5.6-terra":{"input_cost_per_token":0.0000025,"output_cost_per_token":0.000015},
		"claude-opus-4-8":{"input_cost_per_token":0.000005,"output_cost_per_token":0.000025},
		"gpt-5.6-sol":{"input_cost_per_token":0.000005,"output_cost_per_token":0.00003}
	}`)
	e, err := NewEngine(config.PricingConfig{Enabled: true, SourceFile: path}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	got, total := e.SearchPrices(" GPT-5.6 ", 1, 1)
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if len(got) != 1 || got[0].Model != "gpt-5.6-terra" {
		t.Fatalf("page = %+v, want second alphabetic GPT-5.6 model", got)
	}
	if got[0].Price.InputCostPerToken == nil || *got[0].Price.InputCostPerToken != 0.0000025 {
		t.Fatalf("price = %+v, want terra input rate", got[0].Price)
	}
}
