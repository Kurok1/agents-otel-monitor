package dashboard

// Response types for /api/usage/*. JSON tags use snake_case to match
// the frontend wire contract.

// SnapshotResponse → GET /api/usage/snapshot?range=
//
// All three KPI blocks (tokens / cost / cache) are scoped to the same
// `range` window (day / week / month). The model breakdown table stays
// all-time — it's a global mix indicator, range-independent by design.
type SnapshotResponse struct {
	UpdatedAt string        `json:"updated_at"`
	Range     string        `json:"range"`
	Tokens    TokensBlock   `json:"tokens"`
	Cost      CostBlock     `json:"cost"`
	Cache     CacheBlock    `json:"cache"`
	Requests  RequestsBlock `json:"requests"`
	Models    []ModelBlock  `json:"models"`
}

type TokensBlock struct {
	In        int64   `json:"in"`
	Out       int64   `json:"out"`
	Total     int64   `json:"total"`      // input + output + cacheRead + cacheCreation
	PrevTotal int64   `json:"prev_total"` // previous same-length window
	Sparkline []int64 `json:"sparkline"`  // length matches range bucket count (14/12/12)
}

type CostBlock struct {
	Total     float64   `json:"total"`
	PrevTotal float64   `json:"prev_total"`
	Sparkline []float64 `json:"sparkline"`
	Estimated bool      `json:"cost_estimated"` // true when the shown cost includes codex estimates
}

// RequestsBlock counts API requests (rows in event_api_request) in the
// current window, the previous same-length window, and per-bucket for the
// sparkline. Mirrors TokensBlock's shape.
type RequestsBlock struct {
	Total     int64   `json:"total"`
	PrevTotal int64   `json:"prev_total"`
	Sparkline []int64 `json:"sparkline"`
}

// CacheBlock describes prompt-cache efficacy in the current window.
//
//   - HitRate = cacheRead / (cacheRead + cacheCreation). Null when both
//     are zero (no caching used in this window) — frontend renders "N/A".
//   - ReadTokens     = tokens served from cache (hits).
//   - CreationTokens = tokens written into cache (cache writes / refreshes).
type CacheBlock struct {
	HitRate        *float64 `json:"hit_rate"`
	ReadTokens     int64    `json:"read_tokens"`
	CreationTokens int64    `json:"creation_tokens"`
}

// ModelBlock is one row of the breakdown table, keyed by the classifier
// group (e.g. "opus-4.7", "sonnet-4.6", "deepseek-v3"). Order in the
// response is by total tokens descending so the busiest group sorts first.
type ModelBlock struct {
	Group       string  `json:"group"`
	Requests    int64   `json:"requests"`
	TokensIn    int64   `json:"tokens_in"`
	TokensOut   int64   `json:"tokens_out"`
	CacheTokens int64   `json:"cache_tokens"`
	Cost        float64 `json:"cost"`
	Share       float64 `json:"share"`
}

// TrendsResponse → GET /api/usage/trends?range=
//
// `Groups` enumerates the legend order (descending by total tokens across
// the window). `Points[i].Values[g]` is the bucket value for group `g`;
// groups absent from a bucket are simply missing from the map (frontend
// treats them as zero).
type TrendsResponse struct {
	Range  string        `json:"range"`
	Groups []string      `json:"groups"`
	Points []TrendsPoint `json:"points"`
}

type TrendsPoint struct {
	Date   string           `json:"date"`
	Label  string           `json:"label"`
	Values map[string]int64 `json:"values"`
}

// RankingsResponse → GET /api/usage/rankings?since=
type RankingsResponse struct {
	Since  string      `json:"since"`
	Tools  []ToolRank  `json:"tools"`
	Skills []SkillRank `json:"skills"`
}

type ToolRank struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type SkillRank struct {
	Name        string `json:"name"`
	Activations int64  `json:"activations"`
}

// HeatmapResponse → GET /api/usage/heatmap
//
// A fixed 360-day daily calendar heatmap, range-independent by design
// (always the trailing 360 local days). Each point carries the raw per-day
// components plus a composite Score in [0,1]: each component is normalized
// against its own 360-day-window max (min pinned at 0 — a zero day means
// "no activity"), then combined with the config weights:
//
//	score = (wT·nT + wC·nC + wR·nR) / (wT + wC + wR)
type HeatmapResponse struct {
	UpdatedAt string         `json:"updated_at"`
	Days      int            `json:"days"`     // always 360
	Timezone  string         `json:"timezone"` // IANA name used for day bucketing
	Weights   HeatmapWeights `json:"weights"`
	Points    []HeatmapPoint `json:"points"`
}

// HeatmapWeights echoes the configured composite weights so the UI can
// caption the chart. They are relative (the score is divided by their sum).
type HeatmapWeights struct {
	Tokens   float64 `json:"tokens"`
	Cost     float64 `json:"cost"`
	Requests float64 `json:"requests"`
}

// HeatmapPoint is one calendar day. Raw components feed the tooltip; Score
// (composite intensity, [0,1]) drives the cell color.
type HeatmapPoint struct {
	Date     string  `json:"date"` // YYYY-MM-DD, local calendar day
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
	Requests int64   `json:"requests"`
	Score    float64 `json:"score"`
}

// ─────────────────────────────────────────────────────────────────────
// Sessions — GET /api/sessions and GET /api/sessions/{id}
// ─────────────────────────────────────────────────────────────────────

// SessionSummary is one row of the session list. Times are RFC3339 UTC
// (frontend formats to local). Counts are all-time for that session.
type SessionSummary struct {
	SessionID        string `json:"session_id"`
	Client           string `json:"client"` // "claude" | "codex"
	FirstActive      string `json:"first_active"`
	LastActive       string `json:"last_active"`
	Tokens           int64  `json:"tokens"`
	Requests         int64  `json:"requests"`
	ToolCalls        int64  `json:"tool_calls"`
	SkillActivations int64  `json:"skill_activations"`
	// Cost is claude authoritative or codex estimated; nil when a codex row has
	// no estimate (pricing disabled). CostEstimated marks codex-estimated rows.
	Cost          *float64 `json:"cost,omitempty"`
	CostEstimated bool     `json:"cost_estimated"`
}

// SessionListResponse → GET /api/sessions?limit=
// Sessions ordered by last activity, most recent first.
type SessionListResponse struct {
	UpdatedAt string           `json:"updated_at"`
	Sessions  []SessionSummary `json:"sessions"`
}

// SessionDetailResponse → GET /api/sessions/{id}
// Tools/Skills are the per-session pie data, already folded to Top-N + an
// aggregated "其他" tail (see bucketToolsTopN / bucketSkillsTopN). The
// breakdown sums equal ToolCalls / SkillActivations respectively.
type SessionDetailResponse struct {
	SessionID        string      `json:"session_id"`
	Client           string      `json:"client"` // "claude" | "codex"
	FirstActive      string      `json:"first_active"`
	LastActive       string      `json:"last_active"`
	Tokens           int64       `json:"tokens"`
	Requests         int64       `json:"requests"`
	ToolCalls        int64       `json:"tool_calls"`
	SkillActivations int64       `json:"skill_activations"`
	Tools            []ToolRank  `json:"tools"`
	Skills           []SkillRank `json:"skills"`
	// Cost is claude authoritative or codex estimated; nil when a codex session
	// has no estimate (pricing disabled). CostEstimated marks codex estimates.
	Cost          *float64 `json:"cost,omitempty"`
	CostEstimated bool     `json:"cost_estimated"`
	// TokenDetail is codex-only: the four raw token dimensions
	// (subset semantics: cached ⊂ input, reasoning ⊂ output). Nil for
	// claude sessions.
	TokenDetail *SessionTokenDetail `json:"token_detail,omitempty"`
}

// SessionTokenDetail carries codex raw token dimensions for the detail page.
type SessionTokenDetail struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Cached    int64 `json:"cached"`
	Reasoning int64 `json:"reasoning"`
}

// RatesResponse → GET /api/usage/rates?range=&client=
//
// Sliding-window rate metrics (spec 2026-07-09). Claude speed is output
// tokens per request-second; Codex speed is 1000 / mean service TBT(ms).
// Throughput is tokens per wall-clock minute split by token type. Buckets:
// day → 48×1h, week → 28×6h, month → 30×1d.
type RatesResponse struct {
	Range          string          `json:"range"`
	BucketInterval string          `json:"bucket_interval"` // "1h" / "6h" / "1d"
	Speed          SpeedBlock      `json:"speed"`
	Throughput     ThroughputBlock `json:"throughput"`
}

// SpeedBlock: Groups is the legend order (window source-weight descending).
// A group absent from a bucket's Values means "no data" (frontend breaks the
// line). Current/Previous are whole-window source-native averages; nil when
// the window has no data or the request is an all-client view.
type SpeedBlock struct {
	Groups   []string     `json:"groups"`
	Points   []RatesPoint `json:"points"`
	Current  *float64     `json:"current"`
	Previous *float64     `json:"previous"`
}

// ThroughputBlock: Values carry tokens/min per type; empty buckets are 0.
type ThroughputBlock struct {
	Types  []string     `json:"types"` // input / output / cache_read / cache_creation
	Points []RatesPoint `json:"points"`
}

// RatesPoint is one bucket. Ts is the RFC3339 UTC bucket start; Label is the
// local-time display label ("HH:00", or "M/D" at local midnight / day grain).
type RatesPoint struct {
	Ts     string             `json:"ts"`
	Label  string             `json:"label"`
	Values map[string]float64 `json:"values"`
}

// PricingModelsResponse → GET /api/pricing/models?client=
//
// Lists models actually seen in the data with their LiteLLM unit prices.
// enabled=false (pricing off) is a normal state, not an error: models is
// empty and the table metadata is omitted.
type PricingModelsResponse struct {
	Enabled      bool          `json:"enabled"`
	TableEntries int           `json:"table_entries,omitempty"`
	LastRefresh  string        `json:"last_refresh,omitempty"`
	Models       []PricedModel `json:"models"`
}

// PricedModel is one seen model. Prices are USD per 1M tokens (LiteLLM
// per-token rates × 1e6); nil means the field is absent in the price table.
// Matched=false → the model was seen in data but has no price entry (all
// four rates nil).
type PricedModel struct {
	Model                string   `json:"model"`
	Clients              []string `json:"clients"` // "claude" / "codex", sorted
	Matched              bool     `json:"matched"`
	InputPer1M           *float64 `json:"input_per_1m"`
	OutputPer1M          *float64 `json:"output_per_1m"`
	CacheReadPer1M       *float64 `json:"cache_read_per_1m"`
	ReasoningOutputPer1M *float64 `json:"reasoning_output_per_1m"`
	Requests             int64    `json:"requests"`
	LastSeen             string   `json:"last_seen"` // RFC3339 UTC
}
