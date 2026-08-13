package stats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"sort"
	"strings"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/buildinfo"
	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/pricing"
	"github.com/kuroky/claude-code-monitor/internal/store"
)

// Server runs a small HTTP listener exposing /internal/healthz, /internal/stats
// and (optionally) /debug/pprof/*. The data source is BufferedWriter.Stats(),
// kept in-process so we do not introduce a Prometheus dep just for self-checks.
//
// Callers can mount an additional handler on "/" via SetRootHandler — used to
// serve the embedded SPA on the same port without spinning up a second
// listener.
type Server struct {
	cfg         config.StatsConfig
	writer      *store.BufferedWriter
	pricing     *pricing.Engine
	log         *slog.Logger
	startTime   time.Time
	srv         *http.Server
	rootHandler http.Handler
	apiHandler  http.Handler
}

func NewServer(cfg config.StatsConfig, writer *store.BufferedWriter, engine *pricing.Engine, log *slog.Logger) *Server {
	return &Server{
		cfg:       cfg,
		writer:    writer,
		pricing:   engine,
		log:       log,
		startTime: time.Now(),
	}
}

// SetRootHandler registers an http.Handler at "/", replacing the plain-text
// landing page. The stats endpoints (/internal/*, /debug/pprof/*) still take
// precedence — ServeMux routes the more specific prefix first. Must be called
// before Start.
func (s *Server) SetRootHandler(h http.Handler) {
	s.rootHandler = h
}

// SetAPIHandler mounts an http.Handler at the "/api/" prefix. Used for the
// dashboard query API; ServeMux routes the prefix more specifically than "/"
// so the SPA root handler still gets everything else. Must be called before Start.
func (s *Server) SetAPIHandler(h http.Handler) {
	s.apiHandler = h
}

// Handler returns the complete HTTP surface served by Start. Exposing the
// composed handler keeps endpoint behavior testable without binding a port.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/version", s.handleVersion)
	mux.HandleFunc("/internal/healthz", s.handleHealth)
	mux.HandleFunc("/internal/stats", s.handleStats)
	if s.apiHandler != nil {
		mux.Handle("/api/", s.apiHandler)
	}
	if s.rootHandler != nil {
		mux.Handle("/", s.rootHandler)
	} else {
		mux.HandleFunc("/", s.handleIndex)
	}

	if s.cfg.EnablePProf {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	return mux
}

// Start binds the HTTP listener in a goroutine. When cfg.Listen is empty the
// stats server is treated as disabled and Start is a no-op.
func (s *Server) Start() error {
	if s.cfg.Listen == "" {
		s.log.Info("stats server disabled (empty listen)")
		return nil
	}

	s.srv = &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		s.log.Info("stats server listening",
			"addr", s.cfg.Listen,
			"pprof", s.cfg.EnablePProf,
			"web_ui", s.rootHandler != nil,
			"api", s.apiHandler != nil,
		)
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Error("stats server", "err", err)
		}
	}()
	return nil
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(struct {
		Service string `json:"service"`
		Version string `json:"version"`
	}{
		Service: "claude-code-monitor",
		Version: buildinfo.Version(),
	}); err != nil {
		s.log.Error("encode version response", "err", err)
	}
}

// Shutdown stops the HTTP listener with the provided context timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "claude-code-monitor stats\n\n")
	io.WriteString(w, "  /internal/healthz   liveness check (returns ok)\n")
	io.WriteString(w, "  /internal/stats     per-table buffer metrics\n")
	if s.cfg.EnablePProf {
		io.WriteString(w, "  /debug/pprof/       runtime profiles\n")
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "ok")
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	var b strings.Builder

	fmt.Fprintf(&b, "# claude-code-monitor stats\n")
	fmt.Fprintf(&b, "uptime_seconds        %d\n", int(time.Since(s.startTime).Seconds()))
	fmt.Fprintf(&b, "\n# per-table buffers\n")
	fmt.Fprintf(&b, "%-34s %10s %10s %10s %12s %10s\n",
		"table", "appended", "flushed", "dropped", "flush_errors", "pending")

	stats := s.writer.Stats()
	pending := s.writer.PendingLens()
	names := make([]string, 0, len(stats))
	for n := range stats {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		m := stats[name]
		fmt.Fprintf(&b, "%-34s %10d %10d %10d %12d %10d\n",
			name, m.Appended, m.Flushed, m.Dropped, m.FlushErrors, pending[name])
	}

	if s.pricing != nil {
		ps := s.pricing.Stats()
		fmt.Fprintf(&b, "\n# pricing\n")
		fmt.Fprintf(&b, "pricing_enabled        %t\n", ps.Enabled)
		fmt.Fprintf(&b, "pricing_entries        %d\n", ps.Entries)
		fmt.Fprintf(&b, "pricing_last_refresh   %s ok=%t src=%s\n",
			ps.LastRefreshAt.Format(time.RFC3339), ps.LastRefreshOK, ps.LastRefreshSource)
		unmatched := make([]string, 0, len(ps.Unmatched))
		for model := range ps.Unmatched {
			unmatched = append(unmatched, model)
		}
		sort.Strings(unmatched)
		for _, model := range unmatched {
			fmt.Fprintf(&b, "pricing_unmatched      %-40s %d\n", model, ps.Unmatched[model])
		}
	}

	_, _ = io.WriteString(w, b.String())
}
