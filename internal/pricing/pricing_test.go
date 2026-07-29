/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.4.0
 */

package pricing

import "testing"

func ptr(f float64) *float64 { return &f }

func TestParseLiteLLMSkipsSampleSpecAndKeepsFields(t *testing.T) {
	data := []byte(`{
		"sample_spec": {"input_cost_per_token": "desc", "litellm_provider": "x"},
		"gpt-4o": {"input_cost_per_token": 0.0000025, "output_cost_per_token": 0.00001, "cache_read_input_token_cost": 0.00000125, "max_tokens": 128000}
	}`)
	tbl, err := parseLiteLLM(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := tbl["sample_spec"]; ok {
		t.Fatal("sample_spec must be skipped")
	}
	p, ok := tbl["gpt-4o"]
	if !ok || p.InputCostPerToken == nil || *p.InputCostPerToken != 0.0000025 {
		t.Fatalf("gpt-4o input rate wrong: %+v", p)
	}
	if p.CacheReadInputTokenCost == nil || *p.CacheReadInputTokenCost != 0.00000125 {
		t.Fatalf("gpt-4o cache rate wrong: %+v", p)
	}
	if p.OutputCostPerReasoningToken != nil {
		t.Fatal("absent reasoning rate must stay nil")
	}
}

func TestLookupExactThenNormalized(t *testing.T) {
	tbl := priceTable{"gpt-4o": {InputCostPerToken: ptr(1)}}
	if _, ok := tbl.lookup("gpt-4o"); !ok {
		t.Fatal("exact lookup failed")
	}
	if _, ok := tbl.lookup("openai/gpt-4o-2024-08-06"); !ok {
		t.Fatal("normalized lookup (strip provider + date) failed")
	}
	if _, ok := tbl.lookup("unknown-model"); ok {
		t.Fatal("unknown model must miss")
	}
	if _, ok := tbl.lookup(""); ok {
		t.Fatal("empty model must miss")
	}
}

func TestCostSubsetSemantics(t *testing.T) {
	// input=1000 (cached 200), output=500 (reasoning 100). rates: in=1e-6, out=2e-6, cacheRead=0.25e-6.
	p := ModelPrice{InputCostPerToken: ptr(1e-6), OutputCostPerToken: ptr(2e-6), CacheReadInputTokenCost: ptr(0.25e-6)}
	got, ok := p.cost(TokenCounts{Input: 1000, Output: 500, Cached: 200, Reasoning: 100, Tool: 999})
	if !ok {
		t.Fatal("expected priceable")
	}
	// (1000-200)*1e-6 + 200*0.25e-6 + 500*2e-6 = 0.0008 + 0.00005 + 0.001 = 0.00185
	want := 0.00185
	if diff := got - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

func TestCostBreakdownSeparatesInputOutputAndCacheRead(t *testing.T) {
	p := ModelPrice{
		InputCostPerToken:           ptr(1e-6),
		OutputCostPerToken:          ptr(2e-6),
		CacheReadInputTokenCost:     ptr(0.25e-6),
		OutputCostPerReasoningToken: ptr(3e-6),
	}

	got, ok := p.CostBreakdown(TokenCounts{
		Input:     1000,
		Output:    500,
		Cached:    200,
		Reasoning: 100,
	})
	if !ok {
		t.Fatal("expected priceable breakdown")
	}
	if diff := got.Input - 0.0008; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("input cost = %v, want 0.0008", got.Input)
	}
	if diff := got.CacheRead - 0.00005; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("cache-read cost = %v, want 0.00005", got.CacheRead)
	}
	if diff := got.Output - 0.0011; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("output cost = %v, want 0.0011 including reasoning", got.Output)
	}
}

func TestCostBreakdownKeepsAvailableComponentsWhenCoreRateIsMissing(t *testing.T) {
	p := ModelPrice{
		InputCostPerToken:       ptr(1e-6),
		CacheReadInputTokenCost: ptr(0.25e-6),
	}

	got, complete := p.CostBreakdown(TokenCounts{
		Input:  1000,
		Output: 500,
		Cached: 200,
	})
	if complete {
		t.Fatal("breakdown with no output rate must not be complete")
	}
	if diff := got.Input - 0.0008; !got.InputAvailable || diff > 1e-12 || diff < -1e-12 {
		t.Errorf("input component = (%v, %v), want available 0.0008", got.InputAvailable, got.Input)
	}
	if diff := got.CacheRead - 0.00005; !got.CacheReadAvailable || diff > 1e-12 || diff < -1e-12 {
		t.Errorf("cache-read component = (%v, %v), want available 0.00005", got.CacheReadAvailable, got.CacheRead)
	}
	if got.OutputAvailable {
		t.Errorf("output component = (%v, %v), want unavailable", got.OutputAvailable, got.Output)
	}
}

func TestCacheRateNilFallsBackButZeroDoesNot(t *testing.T) {
	// nil cache rate → cached billed at input rate.
	pNil := ModelPrice{InputCostPerToken: ptr(1e-6), OutputCostPerToken: ptr(1e-6)}
	gotNil, _ := pNil.cost(TokenCounts{Input: 100, Cached: 100})
	if diff := gotNil - 100*1e-6; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("nil cache rate should fall back to input rate: %v", gotNil)
	}
	// explicit 0 cache rate → cached billed at 0.
	pZero := ModelPrice{InputCostPerToken: ptr(1e-6), OutputCostPerToken: ptr(1e-6), CacheReadInputTokenCost: ptr(0)}
	gotZero, _ := pZero.cost(TokenCounts{Input: 100, Cached: 100})
	if gotZero != 0 {
		t.Fatalf("explicit 0 cache rate should charge 0: %v", gotZero)
	}
}

func TestCostUnpriceableWhenMissingCoreRate(t *testing.T) {
	p := ModelPrice{OutputCostPerToken: ptr(1e-6)} // no input rate
	if _, ok := p.cost(TokenCounts{Input: 10, Output: 10}); ok {
		t.Fatal("missing input rate must be unpriceable")
	}
}

func TestReasoningSplitAndClamp(t *testing.T) {
	p := ModelPrice{InputCostPerToken: ptr(1e-6), OutputCostPerToken: ptr(2e-6), OutputCostPerReasoningToken: ptr(3e-6)}
	// output=100 reasoning=40 → (100-40)*2e-6 + 40*3e-6 = 0.00012 + 0.00012 = 0.00024
	got, _ := p.cost(TokenCounts{Output: 100, Reasoning: 40})
	if diff := got - 0.00024; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("reasoning split = %v, want 0.00024", got)
	}
	// negative (cached>input) clamps to 0, no panic.
	got2, _ := p.cost(TokenCounts{Input: 10, Cached: 999})
	if got2 < 0 {
		t.Fatalf("clamp failed: %v", got2)
	}
}
