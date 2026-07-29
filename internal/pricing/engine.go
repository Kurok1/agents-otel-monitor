/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.4.0
 */

package pricing

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

// Engine holds the live price table and computes per-record cost. It is safe
// for concurrent CostFor calls: the table is swapped atomically by the refresh
// goroutine. When disabled it is a no-op (CostFor always returns invalid).
type Engine struct {
	enabled   bool
	cfg       config.PricingConfig
	log       *slog.Logger
	fileBase  priceTable // immutable baseline from source_file
	overrides priceTable // immutable, always layered on top
	table     atomic.Pointer[priceTable]

	mu                sync.Mutex
	entries           int
	unmatched         map[string]int64
	lastRefreshAt     time.Time
	lastRefreshSource string
	lastRefreshOK     bool

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// Stats is an observability snapshot of the engine (see internal/stats).
type Stats struct {
	Enabled           bool
	Entries           int
	LastRefreshAt     time.Time
	LastRefreshSource string
	LastRefreshOK     bool
	Unmatched         map[string]int64
}

// PriceEntry is one named row from the current in-memory price table.
type PriceEntry struct {
	Model string
	Price ModelPrice
}

// NewEngine constructs the engine. When enabled it synchronously loads
// source_file (fail-fast on error). URL refresh is started separately via Start.
func NewEngine(cfg config.PricingConfig, log *slog.Logger) (*Engine, error) {
	if log == nil {
		log = slog.Default()
	}
	e := &Engine{
		enabled:   cfg.Enabled,
		cfg:       cfg,
		log:       log,
		unmatched: make(map[string]int64),
		stopCh:    make(chan struct{}),
	}
	if !cfg.Enabled {
		return e, nil
	}
	e.overrides = overridesToTable(cfg.Overrides)
	data, err := os.ReadFile(cfg.SourceFile)
	if err != nil {
		return nil, fmt.Errorf("read pricing source_file %s: %w", cfg.SourceFile, err)
	}
	base, err := parseLiteLLM(data)
	if err != nil {
		return nil, fmt.Errorf("parse pricing source_file %s: %w", cfg.SourceFile, err)
	}
	e.fileBase = base
	e.publish(merge(base, e.overrides), "file")
	e.log.Info("pricing engine loaded", "entries", len(base), "source", "file", "path", cfg.SourceFile)
	return e, nil
}

func overridesToTable(m map[string]config.PriceOverride) priceTable {
	out := make(priceTable, len(m))
	for name, o := range m {
		out[name] = ModelPrice{
			InputCostPerToken:           o.InputCostPerToken,
			OutputCostPerToken:          o.OutputCostPerToken,
			CacheReadInputTokenCost:     o.CacheReadInputTokenCost,
			OutputCostPerReasoningToken: o.OutputCostPerReasoningToken,
		}
	}
	return out
}

func (e *Engine) publish(t priceTable, source string) {
	e.table.Store(&t)
	e.mu.Lock()
	e.entries = len(t)
	e.lastRefreshAt = time.Now().UTC()
	e.lastRefreshSource = source
	e.lastRefreshOK = true
	e.mu.Unlock()
}

// CostFor returns the estimated cost, or an invalid NullFloat64 when the engine
// is disabled, the model is unmatched, or the model has no input/output rate.
func (e *Engine) CostFor(model string, c TokenCounts) sql.NullFloat64 {
	if !e.enabled {
		return sql.NullFloat64{}
	}
	tbl := e.table.Load()
	if tbl == nil {
		return sql.NullFloat64{}
	}
	p, ok := (*tbl).lookup(model)
	if !ok {
		e.recordUnmatched(model)
		return sql.NullFloat64{}
	}
	cost, ok := p.cost(c)
	if !ok {
		e.recordUnmatched(model)
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: cost, Valid: true}
}

// PriceFor returns the price entry for model via the same exact→normalized
// lookup CostFor uses. ok=false when the engine is disabled or the model is
// unmatched. Read-only display probe: it must NOT touch the unmatched counter
// (that stat tracks ingest-time misses only).
func (e *Engine) PriceFor(model string) (ModelPrice, bool) {
	if !e.enabled {
		return ModelPrice{}, false
	}
	tbl := e.table.Load()
	if tbl == nil {
		return ModelPrice{}, false
	}
	return (*tbl).lookup(model)
}

// SearchPrices returns one stable, alphabetically sorted page from the live
// price table. Prefix matching is case-insensitive and surrounding whitespace
// is ignored. The total is calculated before pagination.
func (e *Engine) SearchPrices(prefix string, offset, limit int) ([]PriceEntry, int) {
	if !e.enabled {
		return []PriceEntry{}, 0
	}
	tbl := e.table.Load()
	if tbl == nil {
		return []PriceEntry{}, 0
	}

	matches := make([]PriceEntry, 0)
	for model, price := range *tbl {
		if !MatchesModelPrefix(model, prefix) {
			continue
		}
		matches = append(matches, PriceEntry{Model: model, Price: price})
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Model < matches[j].Model
	})

	total := len(matches)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []PriceEntry{}, total
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return matches[offset:end], total
}

func (e *Engine) recordUnmatched(model string) {
	e.mu.Lock()
	e.unmatched[model]++
	e.mu.Unlock()
}

// Start launches the background URL refresh loop when a source_url is set.
// No-op when disabled or url empty.
func (e *Engine) Start() {
	if !e.enabled || e.cfg.SourceURL == "" {
		return
	}
	e.wg.Add(1)
	go e.refreshLoop()
}

// Stop terminates the refresh loop. No-op when Start did not launch one.
func (e *Engine) Stop() {
	if !e.enabled || e.cfg.SourceURL == "" {
		return
	}
	close(e.stopCh)
	e.wg.Wait()
}

func (e *Engine) refreshLoop() {
	defer e.wg.Done()
	e.refreshOnce()
	ticker := time.NewTicker(e.cfg.RefreshInterval.AsDuration())
	defer ticker.Stop()
	for {
		select {
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.refreshOnce()
		}
	}
}

func (e *Engine) refreshOnce() {
	data, err := fetchURL(e.cfg.SourceURL)
	if err != nil {
		e.markRefreshFailed(err)
		return
	}
	urlTable, err := parseLiteLLM(data)
	if err != nil {
		e.markRefreshFailed(err)
		return
	}
	// file < url < overrides; file-only keys survive the URL layer.
	e.publish(merge(merge(e.fileBase, urlTable), e.overrides), "url")
	e.log.Info("pricing table refreshed", "source", "url", "url", e.cfg.SourceURL)
}

func (e *Engine) markRefreshFailed(err error) {
	e.mu.Lock()
	e.lastRefreshOK = false
	e.mu.Unlock()
	e.log.Warn("pricing refresh failed; keeping previous table", "err", err, "url", e.cfg.SourceURL)
}

func fetchURL(url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch pricing url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch pricing url: status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Stats returns a snapshot for the /internal/stats endpoint.
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	un := make(map[string]int64, len(e.unmatched))
	for k, v := range e.unmatched {
		un[k] = v
	}
	return Stats{
		Enabled:           e.enabled,
		Entries:           e.entries,
		LastRefreshAt:     e.lastRefreshAt,
		LastRefreshSource: e.lastRefreshSource,
		LastRefreshOK:     e.lastRefreshOK,
		Unmatched:         un,
	}
}
