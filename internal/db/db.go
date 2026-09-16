// Package db opens the SQLite database and applies file-based migrations.
//
// Driver: modernc.org/sqlite (pure Go, CGO_ENABLED=0). Pragmas set WAL,
// foreign keys, and a busy timeout. MaxOpenConns is pinned to 1 — SQLite
// has a single writer, and this keeps the skeleton free of SQLITE_BUSY
// surprises; revisit under load (see docs/PLAN.md hardening milestone).
package db

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	d, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, pragma := range []string{
		"PRAGMA foreign_keys=ON",
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := d.Exec(pragma); err != nil {
			d.Close()
			return nil, err
		}
	}
	d.SetMaxOpenConns(1)
	return d, nil
}
