package config

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Storage   StorageConfig   `yaml:"storage"`
	Ingest    IngestConfig    `yaml:"ingest"`
	Capture   CaptureConfig   `yaml:"capture"`
	Stats     StatsConfig     `yaml:"stats"`
	Dashboard DashboardConfig `yaml:"dashboard"`
	Archive   ArchiveConfig   `yaml:"archive"`
	Pricing   PricingConfig   `yaml:"pricing"`
	Logging   LoggingConfig   `yaml:"logging"`
}

type ServerConfig struct {
	GRPCListen string `yaml:"grpc_listen"`
}

type StorageConfig struct {
	DuckDBPath string `yaml:"duckdb_path"`
}

type IngestConfig struct {
	BatchSize       int      `yaml:"batch_size"`
	FlushInterval   Duration `yaml:"flush_interval"`
	BufferHardLimit int      `yaml:"buffer_hard_limit"`
}

type CaptureConfig struct {
	Enabled bool   `yaml:"enabled"`
	Dir     string `yaml:"dir"`
}

type StatsConfig struct {
	Listen      string `yaml:"listen"`       // "" disables the stats HTTP server
	EnablePProf bool   `yaml:"enable_pprof"` // when true, /debug/pprof/* is registered
}

type DashboardConfig struct {
	TopN     TopNConfig `yaml:"top_n"`
	Timezone string     `yaml:"timezone"` // IANA name, e.g. "Asia/Shanghai"

	// Heatmap configures the per-day composite intensity for /api/usage/heatmap.
	// Each metric (tokens / cost / requests) is normalized against its own
	// 360-day-window max, then combined: score = (wT·nT + wC·nC + wR·nR) / Σw.
	// All three zero ⇒ defaults to 0.4 / 0.4 / 0.2.
	Heatmap HeatmapConfig `yaml:"heatmap"`

	// ModelGroups configures how raw model names are bucketed for the dashboard
	// breakdown / trends. Rules are tried in order; first match wins. If nothing
	// matches and the name fits the built-in Claude pattern, the classifier
	// derives `<family>-<major>.<minor>` (e.g. claude-opus-4-7[1m] → opus-4.7).
	// Otherwise the raw model name is kept verbatim.
	ModelGroups []ModelGroupRule `yaml:"model_groups"`
}

// ArchiveConfig controls compaction of expired raw telemetry into archive
// summaries. Disabled preserves the existing unbounded raw-data behavior.
type ArchiveConfig struct {
	Enabled bool `yaml:"enabled"`
}

type TopNConfig struct {
	Tools  int `yaml:"tools"`
	Skills int `yaml:"skills"`
}

// HeatmapConfig holds the composite weights for the usage heatmap. Weights
// are relative (only their ratio matters); the score is divided by their sum.
type HeatmapConfig struct {
	WTokens   float64 `yaml:"w_tokens"`
	WCost     float64 `yaml:"w_cost"`
	WRequests float64 `yaml:"w_requests"`
}

// ModelGroupRule is one user-defined classification rule. `Pattern` is a
// Go regexp; `Group` is the resulting label and supports `$1`/`$N` back
// references via regexp.Regexp.ExpandString.
type ModelGroupRule struct {
	Pattern string `yaml:"pattern"`
	Group   string `yaml:"group"`
}

// PricingConfig configures the third-party cost-estimation engine (internal/pricing).
// Disabled by default: when Enabled is false the engine is a no-op and codex
// cost_usd stays NULL. SourceFile is the required local baseline (LiteLLM-format
// JSON — NOT vendored in this repo); SourceURL optionally refreshes it in the
// background; Overrides are per-model hand rates layered on top (file < url < overrides).
type PricingConfig struct {
	Enabled         bool                     `yaml:"enabled"`
	SourceFile      string                   `yaml:"source_file"`
	SourceURL       string                   `yaml:"source_url"`
	RefreshInterval Duration                 `yaml:"refresh_interval"`
	Overrides       map[string]PriceOverride `yaml:"overrides"`
}

// PriceOverride mirrors the LiteLLM per-model price fields we consume. Pointers
// distinguish "absent" from "explicitly 0" (a genuinely free cache read).
type PriceOverride struct {
	InputCostPerToken           *float64 `yaml:"input_cost_per_token"`
	OutputCostPerToken          *float64 `yaml:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64 `yaml:"cache_read_input_token_cost"`
	OutputCostPerReasoningToken *float64 `yaml:"output_cost_per_reasoning_token"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) AsDuration() time.Duration { return time.Duration(d) }

func Load(path string) (Config, error) {
	var cfg Config

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config file %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}

	applyDefaults(&cfg)

	if err := validate(&cfg); err != nil {
		return cfg, fmt.Errorf("validate config %s: %w", path, err)
	}

	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.GRPCListen == "" {
		cfg.Server.GRPCListen = "0.0.0.0:4317"
	}
	if cfg.Storage.DuckDBPath == "" {
		cfg.Storage.DuckDBPath = "./data/monitor.duckdb"
	}
	if cfg.Ingest.BatchSize == 0 {
		cfg.Ingest.BatchSize = 500
	}
	if cfg.Ingest.FlushInterval == 0 {
		cfg.Ingest.FlushInterval = Duration(5 * time.Second)
	}
	if cfg.Ingest.BufferHardLimit == 0 {
		cfg.Ingest.BufferHardLimit = 50000
	}
	if cfg.Capture.Dir == "" {
		cfg.Capture.Dir = "./captured"
	}
	if cfg.Stats.Listen == "" {
		cfg.Stats.Listen = "127.0.0.1:9100"
	}
	if cfg.Dashboard.TopN.Tools == 0 {
		cfg.Dashboard.TopN.Tools = 10
	}
	if cfg.Dashboard.TopN.Skills == 0 {
		cfg.Dashboard.TopN.Skills = 10
	}
	if cfg.Dashboard.Timezone == "" {
		cfg.Dashboard.Timezone = "Asia/Shanghai"
	}
	h := &cfg.Dashboard.Heatmap
	if h.WTokens == 0 && h.WCost == 0 && h.WRequests == 0 {
		h.WTokens, h.WCost, h.WRequests = 0.4, 0.4, 0.2
	}
	if cfg.Pricing.RefreshInterval == 0 {
		cfg.Pricing.RefreshInterval = Duration(24 * time.Hour)
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "json"
	}
}

func validate(cfg *Config) error {
	if cfg.Server.GRPCListen == "" {
		return fmt.Errorf("server.grpc_listen is required")
	}
	if cfg.Storage.DuckDBPath == "" {
		return fmt.Errorf("storage.duckdb_path is required")
	}
	if cfg.Ingest.BatchSize <= 0 {
		return fmt.Errorf("ingest.batch_size must be > 0")
	}
	if cfg.Ingest.FlushInterval <= 0 {
		return fmt.Errorf("ingest.flush_interval must be > 0")
	}
	if cfg.Ingest.BufferHardLimit < cfg.Ingest.BatchSize {
		return fmt.Errorf("ingest.buffer_hard_limit (%d) must be >= batch_size (%d)",
			cfg.Ingest.BufferHardLimit, cfg.Ingest.BatchSize)
	}
	if cfg.Dashboard.TopN.Tools <= 0 {
		return fmt.Errorf("dashboard.top_n.tools must be > 0")
	}
	if cfg.Dashboard.TopN.Skills <= 0 {
		return fmt.Errorf("dashboard.top_n.skills must be > 0")
	}
	if _, err := time.LoadLocation(cfg.Dashboard.Timezone); err != nil {
		return fmt.Errorf("dashboard.timezone %q: %w", cfg.Dashboard.Timezone, err)
	}
	hm := cfg.Dashboard.Heatmap
	if hm.WTokens < 0 || hm.WCost < 0 || hm.WRequests < 0 {
		return fmt.Errorf("dashboard.heatmap weights must be >= 0")
	}
	if hm.WTokens+hm.WCost+hm.WRequests <= 0 {
		return fmt.Errorf("dashboard.heatmap weights sum must be > 0")
	}
	switch cfg.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logging.level must be one of debug/info/warn/error, got %q", cfg.Logging.Level)
	}
	switch cfg.Logging.Format {
	case "json", "text":
	default:
		return fmt.Errorf("logging.format must be json or text, got %q", cfg.Logging.Format)
	}
	for i, rule := range cfg.Dashboard.ModelGroups {
		if rule.Pattern == "" {
			return fmt.Errorf("dashboard.model_groups[%d].pattern is required", i)
		}
		if rule.Group == "" {
			return fmt.Errorf("dashboard.model_groups[%d].group is required", i)
		}
		if _, err := regexp.Compile(rule.Pattern); err != nil {
			return fmt.Errorf("dashboard.model_groups[%d].pattern %q: %w", i, rule.Pattern, err)
		}
	}
	if cfg.Pricing.Enabled {
		if cfg.Pricing.SourceFile == "" {
			return fmt.Errorf("pricing.source_file is required when pricing.enabled")
		}
		if cfg.Pricing.RefreshInterval <= 0 {
			return fmt.Errorf("pricing.refresh_interval must be > 0")
		}
		for name, o := range cfg.Pricing.Overrides {
			for field, p := range map[string]*float64{
				"input_cost_per_token":            o.InputCostPerToken,
				"output_cost_per_token":           o.OutputCostPerToken,
				"cache_read_input_token_cost":     o.CacheReadInputTokenCost,
				"output_cost_per_reasoning_token": o.OutputCostPerReasoningToken,
			} {
				if p != nil && *p < 0 {
					return fmt.Errorf("pricing.overrides[%q].%s must be >= 0", name, field)
				}
			}
		}
	}
	return nil
}
