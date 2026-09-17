package store

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

// BufferedWriter is the production implementation of otlp.Sink. It owns 19
// per-table buffers + appenders. All flushes are serialized via flushMu so
// DuckDB sees at most one writer at a time (per the project's single-writer
// constraint; see CLAUDE.md).
type BufferedWriter struct {
	cfg       config.IngestConfig
	log       *slog.Logger
	buffers   map[string]*TableBuffer
	appenders map[string]*tableAppender

	writeMu *sync.Mutex // serializes appender flushes and archival writes
	wg      sync.WaitGroup
	stop    chan struct{}
}

// NewBufferedWriter opens an Appender for every table in allTables. Failure to
// open any appender closes the others and returns the error.
func NewBufferedWriter(db *DB, cfg config.IngestConfig, log *slog.Logger) (*BufferedWriter, error) {
	w := &BufferedWriter{
		cfg:       cfg,
		log:       log,
		buffers:   make(map[string]*TableBuffer, len(allTables)),
		appenders: make(map[string]*tableAppender, len(allTables)),
		stop:      make(chan struct{}),
		writeMu:   &db.writeMu,
	}

	for _, t := range allTables {
		app, err := newTableAppender(db.Connector, t.name, t.mapper)
		if err != nil {
			w.closeAppenders()
			return nil, fmt.Errorf("open appender %s: %w", t.name, err)
		}
		w.appenders[t.name] = app
		w.buffers[t.name] = NewTableBuffer(t.name, cfg)
	}

	log.Info("buffered writer ready",
		"tables", len(w.buffers),
		"batch_size", cfg.BatchSize,
		"flush_interval", cfg.FlushInterval.AsDuration().String(),
		"buffer_hard_limit", cfg.BufferHardLimit,
	)
	return w, nil
}

// Start launches the periodic flush ticker. Call once after construction.
func (w *BufferedWriter) Start() {
	w.wg.Add(1)
	go w.tickLoop()
}

func (w *BufferedWriter) tickLoop() {
	defer w.wg.Done()
	ticker := time.NewTicker(w.cfg.FlushInterval.AsDuration())
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.flushAll()
		case <-w.stop:
			return
		}
	}
}

// Stop halts the ticker, flushes everything once more, and closes all
// appenders. Subsequent Append calls return an error.
func (w *BufferedWriter) Stop() error {
	close(w.stop)
	w.wg.Wait()
	w.flushAll()
	return w.closeAppenders()
}

func (w *BufferedWriter) closeAppenders() error {
	var errs []error
	for name, app := range w.appenders {
		if err := app.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", name, err))
		}
	}
	w.appenders = nil
	return errors.Join(errs...)
}

// AppendMetric implements otlp.Sink.
func (w *BufferedWriter) AppendMetric(row any) error {
	return w.append(row)
}

// AppendEvent implements otlp.Sink.
func (w *BufferedWriter) AppendEvent(row any) error {
	return w.append(row)
}

func (w *BufferedWriter) append(row any) error {
	name, ok := tableNameFor(row)
	if !ok {
		return fmt.Errorf("no table mapping for row type %T", row)
	}
	buf, ok := w.buffers[name]
	if !ok {
		return fmt.Errorf("no buffer for table %s", name)
	}
	size := buf.Append(row)
	if size >= w.cfg.BatchSize {
		w.flushOne(name)
	}
	return nil
}

func (w *BufferedWriter) flushAll() {
	for _, t := range allTables {
		w.flushOne(t.name)
	}
}

// flushOne drains buffer[name] and pushes the batch through its appender.
// Failures preserve the batch via Pushback so the next tick retries.
func (w *BufferedWriter) flushOne(name string) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()

	buf := w.buffers[name]
	app := w.appenders[name]
	if buf == nil || app == nil {
		return
	}

	rows, droppedSince := buf.PopAll()
	if droppedSince > 0 {
		w.log.Warn("buffer overflow drops detected", "table", name, "dropped", droppedSince)
	}
	if len(rows) == 0 {
		return
	}

	start := time.Now()
	appended, err := app.AppendBatch(rows)
	dur := time.Since(start)
	if err != nil {
		w.log.Error("flush failed; rows preserved for next retry",
			"table", name, "rows", len(rows), "appended", appended, "err", err)
		buf.addFlushError()
		buf.Pushback(rows)
		return
	}
	buf.addFlushed(appended)
	w.log.Debug("flush ok", "table", name, "rows", appended, "duration", dur.String())
}

// Stats returns a snapshot of per-table buffer metrics for the stats endpoint.
func (w *BufferedWriter) Stats() map[string]BufferMetrics {
	out := make(map[string]BufferMetrics, len(w.buffers))
	for name, buf := range w.buffers {
		out[name] = buf.Snapshot()
	}
	return out
}

// PendingLens reports the in-memory queue length per table at this instant.
// Useful for stats endpoints to spot back-pressure that has not yet flushed.
func (w *BufferedWriter) PendingLens() map[string]int {
	out := make(map[string]int, len(w.buffers))
	for name, buf := range w.buffers {
		out[name] = buf.PendingLen()
	}
	return out
}
