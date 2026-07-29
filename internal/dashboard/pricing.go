/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/pricing"
)

// PriceLookup is the consumer-side view of *pricing.Engine (interface
// defined here per the "interface at the consumer" rule). Implemented by
// *pricing.Engine; faked in tests.
type PriceLookup interface {
	PriceFor(model string) (pricing.ModelPrice, bool)
	Stats() pricing.Stats
}

// PriceCatalogSource is the optional full-table capability used only by
// /api/pricing/catalog. Keeping it separate lets snapshot and seen-model
// callers depend on the smaller PriceLookup interface.
type PriceCatalogSource interface {
	SearchPrices(prefix string, offset, limit int) ([]pricing.PriceEntry, int)
	Stats() pricing.Stats
}

// BuildPricingModels assembles /api/pricing/models: distinct seen models ×
// price table lookup. Disabled pricing short-circuits without touching the DB.
func BuildPricingModels(ctx context.Context, db *sql.DB, client Client, prices PriceLookup, enabled bool) (PricingModelsResponse, error) {
	resp := PricingModelsResponse{Enabled: enabled, Models: []PricedModel{}}
	if !enabled || prices == nil {
		resp.Enabled = false
		return resp, nil
	}

	st := prices.Stats()
	resp.TableEntries = st.Entries
	if !st.LastRefreshAt.IsZero() {
		resp.LastRefresh = st.LastRefreshAt.UTC().Format(time.RFC3339)
	}

	rows, err := QuerySeenModels(ctx, db, client)
	if err != nil {
		return resp, err
	}

	type acc struct {
		lastSeen time.Time
		requests int64
		clients  map[string]bool
	}
	byModel := make(map[string]*acc)
	for _, r := range rows {
		a := byModel[r.Model]
		if a == nil {
			a = &acc{clients: make(map[string]bool)}
			byModel[r.Model] = a
		}
		a.requests += r.Requests
		if r.LastSeen.After(a.lastSeen) {
			a.lastSeen = r.LastSeen
		}
		a.clients[r.Client] = true
	}

	type entry struct {
		pm       PricedModel
		lastSeen time.Time
	}
	entries := make([]entry, 0, len(byModel))
	for model, a := range byModel {
		clients := make([]string, 0, len(a.clients))
		for cl := range a.clients {
			clients = append(clients, cl)
		}
		sort.Strings(clients)
		pm := PricedModel{
			Model:    model,
			Clients:  clients,
			Requests: a.requests,
			LastSeen: a.lastSeen.UTC().Format(time.RFC3339),
		}
		if p, ok := prices.PriceFor(model); ok {
			pm.Matched = true
			pm.InputPer1M = per1M(p.InputCostPerToken)
			pm.OutputPer1M = per1M(p.OutputCostPerToken)
			pm.CacheReadPer1M = per1M(p.CacheReadInputTokenCost)
			pm.ReasoningOutputPer1M = per1M(p.OutputCostPerReasoningToken)
		}
		entries = append(entries, entry{pm: pm, lastSeen: a.lastSeen})
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].lastSeen.Equal(entries[j].lastSeen) {
			return entries[i].lastSeen.After(entries[j].lastSeen)
		}
		return entries[i].pm.Model < entries[j].pm.Model
	})
	for _, e := range entries {
		resp.Models = append(resp.Models, e.pm)
	}
	return resp, nil
}

func filterPricingModelsByPrefix(models []PricedModel, prefix string) []PricedModel {
	filtered := make([]PricedModel, 0, len(models))
	for _, model := range models {
		if pricing.MatchesModelPrefix(model.Model, prefix) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

// BuildPricingCatalog assembles one page from the complete in-memory price
// table. Disabled pricing returns a stable empty response.
func BuildPricingCatalog(catalog PriceCatalogSource, enabled bool, prefix string, offset, limit int) PricingCatalogResponse {
	resp := PricingCatalogResponse{
		Enabled: enabled,
		Offset:  offset,
		Limit:   limit,
		Models:  []CatalogPriceModel{},
	}
	if !enabled || catalog == nil {
		resp.Enabled = false
		return resp
	}

	st := catalog.Stats()
	resp.TableEntries = st.Entries
	if !st.LastRefreshAt.IsZero() {
		resp.LastRefresh = st.LastRefreshAt.UTC().Format(time.RFC3339)
	}

	entries, total := catalog.SearchPrices(prefix, offset, limit)
	resp.TotalMatches = total
	for _, entry := range entries {
		resp.Models = append(resp.Models, CatalogPriceModel{
			Model:                entry.Model,
			InputPer1M:           per1M(entry.Price.InputCostPerToken),
			OutputPer1M:          per1M(entry.Price.OutputCostPerToken),
			CacheReadPer1M:       per1M(entry.Price.CacheReadInputTokenCost),
			ReasoningOutputPer1M: per1M(entry.Price.OutputCostPerReasoningToken),
		})
	}
	return resp
}

// per1M converts a per-token USD rate into USD per 1M tokens; nil passes through.
func per1M(rate *float64) *float64 {
	if rate == nil {
		return nil
	}
	v := *rate * 1e6
	return &v
}
