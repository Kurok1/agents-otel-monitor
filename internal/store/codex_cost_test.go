/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.4.0
 */

package store

import (
	"path/filepath"
	"testing"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

// TestMigration004AddsCostColumnLast guards the positional-Appender invariant:
// migration 004 must add cost_usd as the LAST column of codex_event_token_usage
// so mapCodexTokenUsage's final appended value aligns with the DDL.
func TestMigration004AddsCostColumnLast(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(config.StorageConfig{DuckDBPath: filepath.Join(dir, "t.duckdb")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	migs, err := LoadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if err := RunMigrations(db.SQL, migs[:4]); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	var name string
	err = db.SQL.QueryRow(`
		SELECT column_name FROM information_schema.columns
		WHERE table_name = 'codex_event_token_usage'
		ORDER BY ordinal_position DESC
		LIMIT 1
	`).Scan(&name)
	if err != nil {
		t.Fatalf("query columns: %v", err)
	}
	if name != "cost_usd" {
		t.Fatalf("last column = %q, want cost_usd", name)
	}
}
