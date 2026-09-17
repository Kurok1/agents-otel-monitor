/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.1.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// sqlQueryer is the small read-only surface shared by *sql.DB and *sql.Tx.
// Keeping it local lets endpoint builders run every related cold/raw read in
// one DuckDB snapshot without changing the public dashboard API.
type sqlQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func withDashboardSnapshot[T any](ctx context.Context, db *sql.DB, fn func(sqlQueryer) (T, error)) (T, error) {
	var zero T
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return zero, fmt.Errorf("begin dashboard snapshot: %w", err)
	}
	queryer, err := archiveScopedQueryer(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return zero, err
	}
	result, err := fn(queryer)
	if err != nil {
		_ = tx.Rollback() // best effort after a failed read-only snapshot
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, fmt.Errorf("commit dashboard snapshot: %w", err)
	}
	return result, nil
}

type archiveQueryer struct {
	sqlQueryer
	archivePrefix string
}

func archiveScopedQueryer(ctx context.Context, queryer sqlQueryer) (sqlQueryer, error) {
	var catalog string
	if err := queryer.QueryRowContext(ctx, `SELECT current_database()`).Scan(&catalog); err != nil {
		return nil, fmt.Errorf("read dashboard database catalog: %w", err)
	}
	escaped := `"` + strings.ReplaceAll(catalog, `"`, `""`) + `".archive.`
	return archiveQueryer{sqlQueryer: queryer, archivePrefix: escaped}, nil
}

func (q archiveQueryer) QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	return q.sqlQueryer.QueryContext(ctx, strings.ReplaceAll(statement, "archive.", q.archivePrefix), args...)
}
func (q archiveQueryer) QueryRowContext(ctx context.Context, statement string, args ...any) *sql.Row {
	return q.sqlQueryer.QueryRowContext(ctx, strings.ReplaceAll(statement, "archive.", q.archivePrefix), args...)
}
