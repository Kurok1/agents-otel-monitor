package config

/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v1.6.0
 */

import (
	"testing"
	"time"
)

// baseValidConfig returns a config with all defaults applied (passes validate).
func baseValidConfig() Config {
	var cfg Config
	applyDefaults(&cfg)
	return cfg
}

func TestHeatmapWeightDefaults(t *testing.T) {
	cfg := baseValidConfig()
	h := cfg.Dashboard.Heatmap
	if h.WTokens != 0.4 || h.WCost != 0.4 || h.WRequests != 0.2 {
		t.Errorf("default weights = %+v, want 0.4/0.4/0.2", h)
	}
	if err := validate(&cfg); err != nil {
		t.Errorf("validate default config: %v", err)
	}
}

func TestHeatmapWeightDefaults_PartialKept(t *testing.T) {
	var cfg Config
	cfg.Dashboard.Heatmap.WTokens = 0.7 // only one set → others stay 0, no defaulting
	applyDefaults(&cfg)
	h := cfg.Dashboard.Heatmap
	if h.WTokens != 0.7 || h.WCost != 0 || h.WRequests != 0 {
		t.Errorf("partial weights = %+v, want 0.7/0/0", h)
	}
}

func TestHeatmapNegativeWeightRejected(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Dashboard.Heatmap.WCost = -0.1
	if err := validate(&cfg); err == nil {
		t.Error("expected error for negative heatmap weight")
	}
}

func TestPricingConfigDefaultsAndValidate(t *testing.T) {
	// refresh_interval default is applied by applyDefaults (via baseValidConfig).
	base := baseValidConfig()
	if base.Pricing.RefreshInterval.AsDuration() != 24*time.Hour {
		t.Fatalf("refresh_interval default = %v, want 24h", base.Pricing.RefreshInterval.AsDuration())
	}

	// enabled but source_file empty → error.
	cfg := baseValidConfig()
	cfg.Pricing.Enabled = true
	if err := validate(&cfg); err == nil {
		t.Fatal("expected error when pricing.enabled but source_file empty")
	}

	// negative override rate → error.
	neg := -1.0
	cfg2 := baseValidConfig()
	cfg2.Pricing.Enabled = true
	cfg2.Pricing.SourceFile = "x.json"
	cfg2.Pricing.Overrides = map[string]PriceOverride{"m": {InputCostPerToken: &neg}}
	if err := validate(&cfg2); err == nil {
		t.Fatal("expected error on negative override rate")
	}

	// disabled → valid without source_file.
	cfg3 := baseValidConfig()
	if err := validate(&cfg3); err != nil {
		t.Fatalf("disabled pricing should validate, got %v", err)
	}
}

func TestArchiveDefaultsDisabledAndCanBeEnabled(t *testing.T) {
	cfg := baseValidConfig()
	if cfg.Archive.Enabled {
		t.Fatal("archive should default to disabled")
	}
	cfg.Archive.Enabled = true
	if err := validate(&cfg); err != nil {
		t.Fatalf("enabled archive should validate: %v", err)
	}
}

func TestArchiveTimezoneConstraintAppliesOnlyWhenEnabled(t *testing.T) {
	for _, timezone := range []string{"Asia/Kolkata", "Asia/Kathmandu", "Australia/Lord_Howe"} {
		t.Run(timezone, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Dashboard.Timezone = timezone
			if err := validate(&cfg); err != nil {
				t.Fatalf("disabled archive rejected %s: %v", timezone, err)
			}
			cfg.Archive.Enabled = true
			if err := validate(&cfg); err == nil {
				t.Fatalf("enabled archive accepted fractional timezone %s", timezone)
			}
		})
	}
	for _, timezone := range []string{"Asia/Shanghai", "UTC", "America/New_York"} {
		t.Run(timezone, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Dashboard.Timezone = timezone
			cfg.Archive.Enabled = true
			if err := validate(&cfg); err != nil {
				t.Fatalf("enabled archive rejected whole-hour timezone %s: %v", timezone, err)
			}
		})
	}
}
