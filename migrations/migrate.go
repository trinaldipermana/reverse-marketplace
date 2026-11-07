// Package migrations contains the proposed single ordered migration sequence.
// Phase-2 infrastructure is tracked in issue #15; owner agreement precedes wiring.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"strings"
)

//go:embed *.sql
var files embed.FS

// Apply atomically applies immutable SQL files to SQLite configured for foreign
// keys and immediate write transactions. It does not open another connection.
func Apply(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(name TEXT PRIMARY KEY)`); err != nil {
		return err
	}
	entries, err := files.ReadDir(".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE name=?`, entry.Name()).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		data, err := files.ReadFile(entry.Name())
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(data)); err != nil {
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(name) VALUES(?)`, entry.Name()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
