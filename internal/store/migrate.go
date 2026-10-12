package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Migration struct {
	Version int
	Name    string
	SQL     string
}

var migrationFilenamePattern = regexp.MustCompile(`^(\d+)_(\w+)\.sql$`)

const schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       VARCHAR NOT NULL,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`

func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations dir: %w", err)
	}

	out := make([]Migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFilenamePattern.FindStringSubmatch(e.Name())
		if m == nil {
			slog.Warn("skipping non-migration file in migrations dir", "name", e.Name())
			continue
		}
		version, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("parse version from %s: %w", e.Name(), err)
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded %s: %w", e.Name(), err)
		}
		out = append(out, Migration{
			Version: version,
			Name:    m[2],
			SQL:     string(body),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })

	for i := 1; i < len(out); i++ {
		if out[i].Version == out[i-1].Version {
			return nil, fmt.Errorf("duplicate migration version %d (%s and %s)",
				out[i].Version, out[i-1].Name, out[i].Name)
		}
	}

	return out, nil
}

func RunMigrations(db *sql.DB, migrations []Migration) error {
	if _, err := db.Exec(schemaMigrationsDDL); err != nil {
		return fmt.Errorf("bootstrap schema_migrations: %w", err)
	}

	applied, err := loadAppliedVersions(db)
	if err != nil {
		return err
	}

	pending := 0
	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return fmt.Errorf("apply migration %d_%s: %w", m.Version, m.Name, err)
		}
		slog.Info("migration applied", "version", m.Version, "name", m.Name)
		pending++
	}
	if pending == 0 {
		slog.Info("no pending migrations")
	}
	return nil
}

func loadAppliedVersions(db *sql.DB) (map[int]bool, error) {
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan applied version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return applied, nil
}

func applyMigration(db *sql.DB, m Migration) error {
	if strings.Contains(m.SQL, "archive.") {
		var catalog string
		if err := db.QueryRow(`SELECT current_database()`).Scan(&catalog); err != nil {
			return fmt.Errorf("read migration catalog: %w", err)
		}
		m.SQL = strings.ReplaceAll(m.SQL, "archive.", quoteArchiveIdentifier(catalog)+".archive.")
	}
	for _, stmt := range splitStatements(m.SQL) {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", truncate(stmt, 80), err)
		}
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version, name) VALUES (?, ?)`, m.Version, m.Name); err != nil {
		return fmt.Errorf("record migration in schema_migrations: %w", err)
	}
	return nil
}

func splitStatements(script string) []string {
	raw := strings.Split(script, ";")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
