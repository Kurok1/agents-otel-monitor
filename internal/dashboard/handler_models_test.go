/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0
 */

package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

func TestHandleModelsReturnsCurrentPeriodUsage(t *testing.T) {
	db, _, _ := testDB(t)
	now := time.Now().UTC()
	current := now.Add(-time.Second)
	outsideDay := now.Add(-48 * time.Hour)

	insertTokenUsage(t, db, current, "claude-opus-4-1", "input", 100)
	insertTokenUsage(t, db, current, "claude-opus-4-1", "output", 50)
	insertCostUsage(t, db, current, "claude-opus-4-1", 1.25)
	insertApiRequest(t, db, current, "claude-opus-4-1")

	insertTokenUsage(t, db, outsideDay, "claude-haiku-4-5", "input", 999)
	insertCostUsage(t, db, outsideDay, "claude-haiku-4-5", 99)
	insertApiRequest(t, db, outsideDay, "claude-haiku-4-5")

	handler, err := NewHandler(db, config.DashboardConfig{Timezone: "UTC"}, false, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/usage/models?range=day&client=claude",
		nil,
	)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response struct {
		Range  string `json:"range"`
		Client Client `json:"client"`
		Models []struct {
			Model        string  `json:"model"`
			Requests     int64   `json:"requests"`
			InputTokens  int64   `json:"input_tokens"`
			OutputTokens int64   `json:"output_tokens"`
			TotalTokens  int64   `json:"total_tokens"`
			CostUSD      float64 `json:"cost_usd"`
			Share        float64 `json:"share"`
		} `json:"models"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Range != "day" || response.Client != ClientClaude {
		t.Fatalf("metadata = range:%q client:%q, want day/claude", response.Range, response.Client)
	}
	if len(response.Models) != 1 {
		t.Fatalf("models = %+v, want one current-period model", response.Models)
	}
	model := response.Models[0]
	if model.Model != "opus-4.1" || model.Requests != 1 {
		t.Errorf("model identity = %+v, want opus-4.1 with one request", model)
	}
	if model.InputTokens != 100 || model.OutputTokens != 50 || model.TotalTokens != 150 {
		t.Errorf("model tokens = %+v, want input=100 output=50 total=150", model)
	}
	if model.CostUSD != 1.25 || model.Share != 1 {
		t.Errorf("model totals = %+v, want cost=1.25 share=1", model)
	}
}

func TestHandleModelsCombinesClaudeAndCodex(t *testing.T) {
	db, _, _ := testDB(t)
	current := time.Now().UTC().Add(-time.Second)
	insertTokenUsage(t, db, current, "claude-opus-4-1", "input", 100)
	insertTokenUsage(t, db, current, "claude-opus-4-1", "output", 50)
	insertCostUsage(t, db, current, "claude-opus-4-1", 1.25)
	insertApiRequest(t, db, current, "claude-opus-4-1")
	_, err := db.Exec(`
		INSERT INTO codex_event_token_usage
		  (ts, conversation_id, model, input_token_count, output_token_count, cached_token_count, cost_usd)
		VALUES (?, 'conv-current', 'gpt-5.6-sol', 200, 100, 50, 0.75)
	`, current)
	if err != nil {
		t.Fatalf("insert codex token usage: %v", err)
	}

	handler, err := NewHandler(db, config.DashboardConfig{Timezone: "UTC"}, true, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodGet,
		"/api/usage/models?range=day&client=all",
		nil,
	))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response PeriodModelsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.CostEstimated {
		t.Error("cost_estimated = false, want true when codex estimates are included")
	}
	if len(response.Models) != 2 {
		t.Fatalf("models = %+v, want codex and claude", response.Models)
	}
	codex := response.Models[0]
	if codex.Model != "gpt-5.6-sol" || codex.InputTokens != 200 || codex.OutputTokens != 100 {
		t.Fatalf("codex model = %+v, want raw input=200 output=100", codex)
	}
	if codex.TotalTokens != 300 || codex.Requests != 1 || codex.CostUSD != 0.75 {
		t.Errorf("codex totals = %+v, want total=300 requests=1 cost=.75", codex)
	}
	if codex.Share < 0.6666 || codex.Share > 0.6667 {
		t.Errorf("codex share = %v, want two thirds", codex.Share)
	}
	claude := response.Models[1]
	if claude.Model != "opus-4.1" || claude.TotalTokens != 150 {
		t.Errorf("claude model = %+v, want opus-4.1 total=150", claude)
	}
}
