/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/store"
)

func TestCodexTelemetryEndpointEmptyAndInvalidRange(t *testing.T) {
	h, _ := newTestHandler(t)
	for _, rng := range []string{"day", "week", "month", ""} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage/codex/telemetry?range="+rng, nil))
		var value CodexTelemetryResponse
		if rec.Code != http.StatusOK {
			t.Fatalf("empty %q: %s", rng, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if value.Models == nil || len(value.Models) != 0 || value.TTFT.Samples != 0 || value.TTFT.MeanMs != nil || value.TTFT.P50Ms != nil {
			t.Fatalf("empty values: %+v", value)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage/codex/telemetry?range=year", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid range: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCodexNativeZeroAndAllViewAvailability(t *testing.T) {
	db, w, now := testDB(t)
	c, err := NewClassifier(nil)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := BuildSnapshot(context.Background(), db, c, w, "day", ClientAll, false, nil)
	if err != nil || empty.Cost.Available || empty.Cost.Source != "unknown" {
		t.Fatalf("empty combined source: %+v %v", empty.Cost, err)
	}
	seedNativeCost(t, db, now.Add(-time.Minute), "zero-only", "turn-zero", "0")
	value, err := BuildSnapshot(context.Background(), db, c, w, "day", ClientAll, false, nil)
	if err != nil || !value.Cost.Available || value.Cost.Source != "native" || value.Cost.Total != 0 || !value.Cost.Estimated {
		t.Fatalf("native zero source: %+v %v", value.Cost, err)
	}
	if len(value.Models) != 1 || !value.Models[0].CostAvailable || value.Models[0].Cost != 0 {
		t.Fatalf("zero-only model omitted: %+v", value.Models)
	}
}

func seedNativeCost(t *testing.T, db *sql.DB, at time.Time, conversation, turn, amount string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO codex_event_turn_cost (ts, conversation_id, turn_id, model, estimated_usd) VALUES (?, ?, ?, 'gpt-test', ?)`, at, conversation, turn, amount); err != nil {
		t.Fatal(err)
	}
}

func TestCodexCostsSelectConversationAcrossWindowsAndArchive(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	now := archiveTestNow
	old := archiveCutoffForTest(t, now).Add(-time.Hour)
	recent := now.Add(-time.Hour)
	for _, item := range []struct {
		at   time.Time
		id   string
		cost float64
	}{
		{old, "native", 0.4}, {recent, "native", 0.6}, {old, "fallback", 0.3}, {recent, "zero", 0.5},
	} {
		if _, err := db.SQL.Exec(`INSERT INTO codex_event_token_usage (ts,conversation_id,model,input_token_count,output_token_count,cost_usd) VALUES (?,?,'gpt-test',100,20,?)`, item.at, item.id, item.cost); err != nil {
			t.Fatal(err)
		}
	}
	seedNativeCost(t, db.SQL, recent, "native", "turn-a", "1.250001")
	seedNativeCost(t, db.SQL, recent, "native", "turn-a", "1.250001") // retransmit
	seedNativeCost(t, db.SQL, old, "zero", "turn-zero", "0")
	check := func(want float64) {
		t.Helper()
		result, err := queryCodexCost(ctx, db.SQL, codexCostFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(result.Total-want) > 1e-10 || result.NativeTurnCount != 2 || !result.Available || result.Source != "mixed" {
			t.Fatalf("cost = %+v, want %v", result, want)
		}
		if result.LocalEstimate == nil || math.Abs(*result.LocalEstimate-1.8) > 1e-10 {
			t.Fatalf("frozen local reference = %+v", result.LocalEstimate)
		}
		zero, found, err := BuildSessionDetail(ctx, db.SQL, "zero", ClientCodex, 10, 10, false)
		if err != nil || !found || zero.Cost == nil || *zero.Cost != 0 || zero.Source != "native" || !zero.CostEstimated {
			t.Fatalf("native zero with local pricing disabled: %+v, %v", zero, err)
		}
		past, err := QueryPeriodCost(ctx, db.SQL, ClientCodex, old.Add(-time.Hour), old.Add(time.Hour))
		if err != nil || math.Abs(past-0.3) > 1e-10 {
			t.Fatalf("conversation selection must exclude historical local cost: %v %v", past, err)
		}
	}
	check(1.550001)
	archiver, err := store.NewArchiver(db, archiveTestTimezone, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archiver.RunOnce(ctx, now); err != nil {
		t.Fatal(err)
	}
	check(1.550001)
	seedNativeCost(t, db.SQL, old, "zero", "turn-zero", "0") // duplicate of archived turn
	check(1.550001)
	seedNativeCost(t, db.SQL, recent.Add(time.Minute), "native", "turn-a", "1.5") // corrected value
	check(1.8)
	if _, err := archiver.RunOnce(ctx, now); err != nil {
		t.Fatal(err)
	}
	check(1.8)
	// An out-of-window native event still suppresses local money for the
	// complete conversation, including already archived local rows.
	seedNativeCost(t, db.SQL, recent, "fallback", "late-native", "0.2")
	past, err := QueryPeriodCost(ctx, db.SQL, ClientCodex, old.Add(-time.Hour), old.Add(time.Hour))
	if err != nil || past != 0 {
		t.Fatalf("late source selection across archive: %v %v", past, err)
	}
	list, err := BuildSessionList(ctx, db.SQL, ClientCodex, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	var total float64
	for _, s := range list.Sessions {
		if s.Cost != nil {
			total += *s.Cost
		}
	}
	if math.Abs(total-1.7) > 1e-10 {
		t.Fatalf("session total = %v", total)
	}
}

func TestCodexTelemetryAndSkillArchiveParity(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	old := archiveCutoffForTest(t, archiveTestNow).Add(-2 * time.Hour)
	for _, ttft := range []int{0, 100, 100, 900} {
		execArchiveFixture(t, db.SQL, `INSERT INTO codex_event_token_usage (ts,conversation_id,model,input_token_count,output_token_count,cached_token_count,cache_write_token_count,ttft_ms) VALUES (?,'c','gpt-test',100,20,30,10,?)`, old, ttft)
	}
	execArchiveFixture(t, db.SQL, `INSERT INTO codex_event_token_usage (ts,conversation_id,model,input_token_count,output_token_count,error,ttft_ms) VALUES (?,'c','gpt-test',100,20,'failed',8000)`, old)
	for _, typ := range []string{"explicit", "explicit", "implicit"} {
		execArchiveFixture(t, db.SQL, `INSERT INTO codex_event_skill_invocation (ts,conversation_id,turn_id,skill_name,invocation_type) VALUES (?,'c','turn-1','sample-skill',?)`, old, typ)
	}
	execArchiveFixture(t, db.SQL, `INSERT INTO codex_metric_turn_token_usage (ts,start_ts,sample_count,sum_tokens,model,token_type) VALUES (?,?,1,500,'gpt-test','total')`, old, old)
	spec := WindowSpec{Range: "month", CurrentStart: old.Truncate(time.Hour), CurrentEnd: archiveTestNow.UTC()}
	before, err := queryCodexTelemetry(ctx, db.SQL, spec)
	if err != nil {
		t.Fatal(err)
	}
	if before.TTFT.Samples != 4 || before.TTFT.MeanMs == nil || *before.TTFT.MeanMs != 275 || *before.TTFT.P50Ms != 100 || *before.TTFT.P95Ms != 900 {
		t.Fatalf("TTFT = %+v", before.TTFT)
	}
	if len(before.Models) != 1 {
		t.Fatalf("models = %+v", before.Models)
	}
	for _, u := range before.Models[0].Usage {
		if u.TokenType == "total" && (u.Difference == nil || *u.Difference != 20 || u.TurnSamples != 1 || u.ResponseSamples != 4) {
			t.Fatalf("total check = %+v", u)
		}
		if u.TokenType == "input" && u.Difference != nil {
			t.Fatalf("missing metric should be unknown: %+v", u)
		}
	}
	detail, found, err := BuildSessionDetail(ctx, db.SQL, "c", ClientCodex, 10, 10, false)
	if err != nil || !found || detail.SkillActivations != 2 || detail.TokenDetail.CacheWrite != 40 || detail.Requests != 4 || detail.Cost != nil {
		t.Fatalf("detail = %+v %v", detail, err)
	}
	archiver, err := store.NewArchiver(db, archiveTestTimezone, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archiver.RunOnce(ctx, archiveTestNow); err != nil {
		t.Fatal(err)
	}
	after, err := queryCodexTelemetry(ctx, db.SQL, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("telemetry changed after archive:\nbefore=%+v\nafter=%+v", before, after)
	}
	archived, found, err := BuildSessionDetail(ctx, db.SQL, "c", ClientCodex, 10, 10, false)
	if err != nil || !found || !reflect.DeepEqual(detail, archived) {
		t.Fatalf("session changed after archive:\n%+v\n%+v %v", detail, archived, err)
	}
	execArchiveFixture(t, db.SQL, `INSERT INTO codex_event_skill_invocation (ts,conversation_id,turn_id,skill_name,invocation_type) VALUES (?,'c','turn-1','sample-skill','explicit')`, old)
	if _, err := archiver.RunOnce(ctx, archiveTestNow); err != nil {
		t.Fatal(err)
	}
	skills, err := queryCodexSessionSkills(ctx, db.SQL, "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Activations != 2 || skills[0].Explicit != 1 || skills[0].Implicit != 1 {
		t.Fatalf("skill replay was counted twice: %+v", skills)
	}
}

func TestCodexOptionalUsageMissingStaysUnknownAfterArchive(t *testing.T) {
	db := openArchiveTestDB(t)
	ctx := context.Background()
	old := archiveCutoffForTest(t, archiveTestNow).Add(-2 * time.Hour)
	execArchiveFixture(t, db.SQL, `INSERT INTO codex_event_token_usage (ts,model,input_token_count,output_token_count) VALUES (?,'old-model',100,20)`, old)
	execArchiveFixture(t, db.SQL, `INSERT INTO codex_event_token_usage (ts,model,input_token_count,output_token_count,cache_write_token_count) VALUES (?,'zero-model',100,20,0)`, old)
	spec := WindowSpec{Range: "month", CurrentStart: old.Truncate(time.Hour), CurrentEnd: archiveTestNow.UTC()}
	check := func() {
		t.Helper()
		value, err := queryCodexTelemetry(ctx, db.SQL, spec)
		if err != nil {
			t.Fatal(err)
		}
		for _, model := range value.Models {
			for _, u := range model.Usage {
				if u.TokenType != "cache_write_input" {
					continue
				}
				if model.Model == "old-model" && (u.ResponseTokens != nil || u.ResponseSamples != 0) {
					t.Fatalf("missing cache write became zero: %+v", u)
				}
				if model.Model == "zero-model" && (u.ResponseTokens == nil || *u.ResponseTokens != 0 || u.ResponseSamples != 1) {
					t.Fatalf("measured zero lost: %+v", u)
				}
			}
		}
	}
	check()
	archiver, err := store.NewArchiver(db, archiveTestTimezone, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archiver.RunOnce(ctx, archiveTestNow); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestCodexNativeCostTieBreakSurvivesArchiveReplay(t *testing.T) {
	for _, tc := range []struct {
		name, lower, higher string
	}{
		{"different digit counts", "9", "10"},
		{"leading zeros", "9", "010"},
		{"decimal precision", "9.000000000000000001", "9.000000000000000002"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openArchiveTestDB(t)
			ctx := context.Background()
			old := archiveCutoffForTest(t, archiveTestNow).Add(-time.Hour)
			seed := func(amount string) {
				t.Helper()
				execArchiveFixture(t, db.SQL, `INSERT INTO codex_event_turn_cost (ts,received_at,conversation_id,turn_id,model,estimated_usd) VALUES (?,?,'tie','turn','gpt-test',?)`, old, old, amount)
			}
			archiver, err := store.NewArchiver(db, archiveTestTimezone, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			archive := func() {
				t.Helper()
				if _, err := archiver.RunOnce(ctx, archiveTestNow); err != nil {
					t.Fatal(err)
				}
			}
			check := func(phase string) {
				t.Helper()
				// Compare in DECIMAL so a DOUBLE tie-break cannot hide a loss
				// of precision below the API's floating-point representation.
				var amount string
				var equal bool
				err := db.SQL.QueryRow(`SELECT CAST(cost_usd AS VARCHAR), cost_usd = CAST(? AS DECIMAL(38,18)) FROM codex_native_turn_costs`, tc.higher).Scan(&amount, &equal)
				if err != nil || !equal {
					t.Errorf("%s: cost=%q, want %s: %v", phase, amount, tc.higher, err)
				}
				cost, err := queryCodexCost(ctx, db.SQL, codexCostFilter{})
				if err != nil || cost.NativeTurnCount != 1 || cost.Source != "native" {
					t.Errorf("%s: deduplicated cost=%+v: %v", phase, cost, err)
				}
			}
			seed(tc.lower)
			archive()
			seed(tc.higher)
			seed(tc.lower)
			check("mixed raw and archive")
			archive()
			check("archived increase")
			seed(tc.lower)
			check("smaller replay")
			archive()
			check("archived replay")
		})
	}
}

func TestCodexMeasuredCompletionValidityAndCacheWrites(t *testing.T) {
	db, w, now := testDB(t)
	ctx := context.Background()
	for _, values := range []string{
		"0,0,0,0,NULL", "100,20,30,10,NULL", "NULL,NULL,NULL,NULL,'error'",
		"100,20,30,10,'error'", "-1,20,0,0,NULL", "100,20,80,30,NULL",
	} {
		execArchiveFixture(t, db, `INSERT INTO codex_event_token_usage(ts,input_token_count,output_token_count,cached_token_count,cache_write_token_count,error) VALUES (?,`+values+`)`, now)
	}
	count, err := QueryPeriodRequests(ctx, db, ClientCodex, w.TodayStartUTC, w.TodayEndUTC)
	if err != nil || count != 2 {
		t.Fatalf("successful measured requests = %d %v", count, err)
	}
	tokens, err := QueryPeriodTokens(ctx, db, ClientCodex, w.TodayStartUTC, w.TodayEndUTC)
	if err != nil || tokens.In != 60 || tokens.Out != 20 || tokens.Total != 120 {
		t.Fatalf("token stack = %+v %v", tokens, err)
	}
	cache, err := QueryPeriodCache(ctx, db, ClientCodex, w.TodayStartUTC, w.TodayEndUTC)
	if err != nil || cache.Creation != 10 || cache.Read != 30 || cache.HitDenom != 100 {
		t.Fatalf("cache = %+v %v", cache, err)
	}
}
