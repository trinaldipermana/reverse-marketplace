package mockpos

import (
	"context"
	"database/sql"
	_ "github.com/mattn/go-sqlite3"
	"net/url"
	"path/filepath"
)

// OpenSQLite creates an opt-in shared SQLite pool. The app opens it once and
// injects it into components; New never opens a competing DB. Requires CGO.
func OpenSQLite(ctx context.Context, path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: absolute}
	q := u.Query()
	q.Set("_foreign_keys", "on")
	q.Set("_busy_timeout", "5000")
	q.Set("_txlock", "immediate")
	q.Set("_journal_mode", "WAL")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
