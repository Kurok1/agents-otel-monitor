package dashboard

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

// Handler exposes /api/usage/{snapshot,trends,rankings,heatmap,rates}.
// All endpoints serve JSON and short-cache via `Cache-Control: private, max-age=30`.
type Handler struct {
	db             *sql.DB
	cfg            config.DashboardConfig
	classifier     *Classifier
	pricingEnabled bool
	prices         PriceLookup
	priceCatalog   PriceCatalogSource
	log            *slog.Logger
}

// NewHandler compiles the model-group classifier from cfg. The patterns
// were already validated at config.Load, so an error here implies an
// internal mismatch between validation and compilation paths. pricingEnabled
// (from config.Pricing.Enabled) gates whether codex estimated cost is surfaced.
func NewHandler(db *sql.DB, cfg config.DashboardConfig, pricingEnabled bool, log *slog.Logger) (*Handler, error) {
	if log == nil {
		log = slog.Default()
	}
	c, err := NewClassifier(cfg.ModelGroups)
	if err != nil {
		return nil, err
	}
	return &Handler{db: db, cfg: cfg, classifier: c, pricingEnabled: pricingEnabled, log: log}, nil
}

// ServeHTTP routes by path. Only GET is allowed.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch r.URL.Path {
	case "/api/usage/snapshot":
		h.handleSnapshot(w, r)
	case "/api/usage/trends":
		h.handleTrends(w, r)
	case "/api/usage/rankings":
		h.handleRankings(w, r)
	case "/api/usage/heatmap":
		h.handleHeatmap(w, r)
	case "/api/usage/rates":
		h.handleRates(w, r)
	case "/api/usage/rates/realtime":
		h.handleRealtimeSpeed(w, r)
	case "/api/pricing/models":
		h.handlePricingModels(w, r)
	case "/api/pricing/catalog":
		h.handlePricingCatalog(w, r)
	case "/api/sessions":
		h.handleSessionList(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/api/sessions/") {
			h.handleSessionDetail(w, r)
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "day"
	}
	client, err := ParseClient(r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tw, err := NowWindow(time.Now(), h.cfg.Timezone)
	if err != nil {
		h.log.Error("snapshot: build time window", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp, err := BuildSnapshot(
		r.Context(),
		h.db,
		h.classifier,
		tw,
		rng,
		client,
		h.pricingEnabled,
		h.prices,
	)
	if err != nil {
		if isUserError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.log.Error("snapshot: build", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleTrends(w http.ResponseWriter, r *http.Request) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "day"
	}
	client, err := ParseClient(r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tw, err := NowWindow(time.Now(), h.cfg.Timezone)
	if err != nil {
		h.log.Error("trends: build time window", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp, err := BuildTrends(r.Context(), h.db, h.classifier, tw, rng, client)
	if err != nil {
		// trendsParams returns a 400-class error for unknown range values;
		// query errors are 500.
		if isUserError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
		} else {
			h.log.Error("trends: build", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleRankings(w http.ResponseWriter, r *http.Request) {
	client, err := ParseClient(r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tw, err := NowWindow(time.Now(), h.cfg.Timezone)
	if err != nil {
		h.log.Error("rankings: build time window", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	sinceStart, sinceTag, err := SinceStart(tw, r.URL.Query().Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := BuildRankings(r.Context(), h.db, RankingsOpts{
		SinceStart: sinceStart,
		SinceTag:   sinceTag,
		Client:     client,
		ToolsTopN:  h.cfg.TopN.Tools,
		SkillsTopN: h.cfg.TopN.Skills,
	})
	if err != nil {
		h.log.Error("rankings: build", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleHeatmap serves the fixed 360-day usage heatmap. No request params —
// the window is always the trailing 360 local days; weights come from config.
func (h *Handler) handleHeatmap(w http.ResponseWriter, r *http.Request) {
	client, err := ParseClient(r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tw, err := NowWindow(time.Now(), h.cfg.Timezone)
	if err != nil {
		h.log.Error("heatmap: build time window", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp, err := BuildHeatmap(r.Context(), h.db, tw, HeatmapWeights{
		Tokens:   h.cfg.Heatmap.WTokens,
		Cost:     h.cfg.Heatmap.WCost,
		Requests: h.cfg.Heatmap.WRequests,
	}, client, h.pricingEnabled)
	if err != nil {
		h.log.Error("heatmap: build", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSessionList serves GET /api/sessions?limit= — the most recently
// active sessions, newest first. limit defaults to 30, clamped to [1, 200].
func (h *Handler) handleSessionList(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r.URL.Query().Get("limit"), 30, 200)
	client, err := ParseClient(r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := BuildSessionList(r.Context(), h.db, client, limit, h.pricingEnabled)
	if err != nil {
		h.log.Error("sessions: list", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSessionDetail serves GET /api/sessions/{id}. Unknown ids → 404.
func (h *Handler) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	client, err := ParseClient(r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, found, err := BuildSessionDetail(r.Context(), h.db, id, client, h.cfg.TopN.Tools, h.cfg.TopN.Skills, h.pricingEnabled)
	if err != nil {
		h.log.Error("sessions: detail", "err", err, "session_id", id)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetPriceLookup wires the pricing engine for /api/pricing/models. Optional:
// when unset the endpoint reports enabled=false. Follows the stats.Server
// SetRootHandler/SetAPIHandler idiom — must be called before serving starts.
func (h *Handler) SetPriceLookup(p PriceLookup) {
	h.prices = p
	h.priceCatalog, _ = p.(PriceCatalogSource)
}

func (h *Handler) handleRates(w http.ResponseWriter, r *http.Request) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "day"
	}
	client, err := ParseClient(r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tw, err := NowWindow(time.Now(), h.cfg.Timezone)
	if err != nil {
		h.log.Error("rates: build time window", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp, err := BuildRates(r.Context(), h.db, h.classifier, tw, rng, client)
	if err != nil {
		if isUserError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.log.Error("rates: build", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleRealtimeSpeed(w http.ResponseWriter, r *http.Request) {
	client, err := ParseClient(r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := BuildRealtimeSpeed(r.Context(), h.db, time.Now(), client)
	if err != nil {
		h.log.Error("realtime speed: build", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handlePricingModels(w http.ResponseWriter, r *http.Request) {
	client, err := ParseClient(r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	enabled := h.pricingEnabled && h.prices != nil
	resp, err := BuildPricingModels(r.Context(), h.db, client, h.prices, enabled)
	if err != nil {
		h.log.Error("pricing models: build", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp.Models = filterPricingModelsByPrefix(resp.Models, r.URL.Query().Get("prefix"))
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handlePricingCatalog(w http.ResponseWriter, r *http.Request) {
	const (
		defaultLimit = 100
		maxLimit     = 500
	)
	limit := parseLimit(r.URL.Query().Get("limit"), defaultLimit, maxLimit)
	offset := parseOffset(r.URL.Query().Get("offset"))
	enabled := h.pricingEnabled && h.priceCatalog != nil
	resp := BuildPricingCatalog(
		h.priceCatalog,
		enabled,
		r.URL.Query().Get("prefix"),
		offset,
		limit,
	)
	writeJSON(w, http.StatusOK, resp)
}

// parseLimit parses a positive int from raw, falling back to def and capping
// at max. Empty / invalid / non-positive input → def.
func parseLimit(raw string, def, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func parseOffset(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// isUserError returns true when err comes from input validation
// (range / since parsing). Keeps the handler from dragging a custom
// error type just to discriminate.
func isUserError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return contains(s, "invalid range") || contains(s, "invalid since") || contains(s, "invalid client")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// writeJSON writes a JSON body with cache headers. Marshal failures fall back
// to a plain 500 — they indicate a Go-side bug in response construction.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=30")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Header is already flushed — just log via stderr equivalent.
		fmt.Fprintf(w, "\n{\"error\":\"encode failure\"}\n")
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
