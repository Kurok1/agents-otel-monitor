package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/marcboeker/go-duckdb/v2"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

// DB owns the DuckDB connector + a sql.DB wrapping it for queries and
// migrations. The Connector is exposed so the appender layer can open its
// own driver.Conn for each per-table appender, bypassing the sql.DB pool.
type DB struct {
	SQL       *sql.DB
	Connector *duckdb.Connector
	Path      string

	writeMu sync.Mutex
}

func Open(cfg config.StorageConfig) (*DB, error) {
	if dir := filepath.Dir(cfg.DuckDBPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create duckdb dir %s: %w", dir, err)
		}
	}

	connector, err := duckdb.NewConnector(cfg.DuckDBPath, nil)
	if err != nil {
		return nil, fmt.Errorf("create duckdb connector %s: %w", cfg.DuckDBPath, err)
	}

	sqlDB := sql.OpenDB(connector)
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		_ = connector.Close()
		return nil, fmt.Errorf("ping duckdb %s: %w", cfg.DuckDBPath, err)
	}

	return &DB{SQL: sqlDB, Connector: connector, Path: cfg.DuckDBPath}, nil
}

func (d *DB) Close() error {
	if d == nil {
		return nil
	}
	var errs []error
	if d.SQL != nil {
		if err := d.SQL.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close sql: %w", err))
		}
	}
	if d.Connector != nil {
		if err := d.Connector.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close connector: %w", err))
		}
	}
	return errors.Join(errs...)
}
