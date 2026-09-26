package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies any pending migrations in lexicographic filename order,
// each inside its own transaction, and records the version in
// schema_migrations. Files are named NNNN_description.sql.
func Migrate(ctx context.Context, d *sql.DB) error {
	return migrateFS(ctx, d, migrationsFS, "migrations")
}

// migrateFS is Migrate with the source of migration files supplied, so a test
// can exercise version handling against a temporary directory instead of
// whatever happens to be embedded in the binary. The shipped migrations are
// compiled in, which is deliberate — a deployable binary must not depend on a
// directory it might not have — but it also means the interesting cases
// (duplicate versions, ordering) cannot be tested through Migrate alone.
func migrateFS(ctx context.Context, d *sql.DB, fsys fs.FS, dir string) error {
	if _, err := d.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at REAL NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	// Reject duplicate versions before running anything.
	//
	// schema_migrations is keyed on version alone, so two files sharing a
	// version are not an error: whichever sorts first is applied and the other
	// is silently skipped forever. This is not hypothetical — 0002_auth.sql and
	// 0002_request_board.sql briefly coexisted, the auth columns were never
	// created, and the only symptom was unrelated tests failing on a missing
	// table. A migration runner that cannot tell you a migration did not run is
	// not a migration runner.
	seen := make(map[int64]string, len(names))
	for _, name := range names {
		version, err := versionOf(name)
		if err != nil {
			return fmt.Errorf("bad migration filename %q: %w", name, err)
		}
		if prev, dup := seen[version]; dup {
			return fmt.Errorf("duplicate migration version %d: %q and %q", version, prev, name)
		}
		seen[version] = name
	}

	for _, name := range names {
		version, err := versionOf(name)
		if err != nil {
			return fmt.Errorf("bad migration filename %q: %w", name, err)
		}
		var applied int64
		err = d.QueryRowContext(ctx,
			`SELECT version FROM schema_migrations WHERE version=?`, version).Scan(&applied)
		if err == nil {
			continue // already applied
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check migration %d: %w", version, err)
		}

		body, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, float64(time.Now().Unix())); err != nil {
			tx.Rollback()
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func versionOf(filename string) (int64, error) {
	prefix := strings.SplitN(filename, "_", 2)[0]
	return strconv.ParseInt(prefix, 10, 64)
}
