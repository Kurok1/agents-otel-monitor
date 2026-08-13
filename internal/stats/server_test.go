/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0
 */

package stats

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kuroky/claude-code-monitor/internal/buildinfo"
	"github.com/kuroky/claude-code-monitor/internal/config"
)

func TestVersionEndpointReturnsServiceIdentity(t *testing.T) {
	server := NewServer(
		config.StatsConfig{},
		nil,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/version", nil)

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q, want application/json; charset=utf-8", got)
	}
	var response struct {
		Service string `json:"service"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Service != "claude-code-monitor" {
		t.Errorf("service = %q, want claude-code-monitor", response.Service)
	}
	if response.Version != buildinfo.Version() {
		t.Errorf("version = %q, want %q", response.Version, buildinfo.Version())
	}
}

func TestVersionEndpointRejectsUnsupportedMethod(t *testing.T) {
	server := NewServer(
		config.StatsConfig{},
		nil,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/version", nil)

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}
