package db

import (
	"bytes"
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

		// A migration that rebuilds a table (SQLite cannot ALTER a foreign key,
		// so ON DELETE CASCADE can only be added by create-copy-drop-rename)
		// cannot run with foreign-key enforcement on: the DROP is refused
		// against the table's own children, and it fails with
		// "constraint failed: FOREIGN KEY constraint failed (787)".
		//
		// PRAGMA foreign_keys is a documented NO-OP inside a transaction, so a
		// migration file cannot turn it off for itself -- the pragma has to be
		// set on the connection *before* the tx opens. These files therefore
		// opt in with a marker comment, which is checked here rather than
		// pattern-matched on the body.
		needsFKOff := bytes.Contains(body, []byte("-- concord:requires-foreign-keys-off"))

		if needsFKOff {
			if _, err := d.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
				return fmt.Errorf("apply %s: disable foreign keys: %w", name, err)
			}
			// With a single-connection pool this is the same connection the tx
			// will use. With more than one, the pragma applies per-connection
			// and cannot be relied on; db.go pins MaxOpenConns(1) for this
			// reason, so that is an invariant here and not an assumption.
		}

		applyErr := func() error {
			tx, err := d.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				tx.Rollback()
				return fmt.Errorf("apply %s: %w", name, err)
			}
			// Checked BEFORE the version is recorded and before the commit, so a
			// migration that loses rows rolls back exactly like one that fails a
			// statement. Recording the version first would make the damage
			// permanent: no later run would try again.
			if err := assertNoSilentDataLoss(ctx, tx); err != nil {
				tx.Rollback()
				return fmt.Errorf("apply %s: %w", name, err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
				version, float64(time.Now().Unix())); err != nil {
				tx.Rollback()
				return fmt.Errorf("record %s: %w", name, err)
			}
			return tx.Commit()
		}()

		// Restore enforcement on every path out of this iteration, including
		// the error one -- a deferred call would instead pile up one closure
		// per migration and only run when the whole function returned.
		if needsFKOff {
			_, _ = d.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys=ON`)
		}
		if applyErr != nil {
			return applyErr
		}
	}
	return nil
}

// assertNoSilentDataLoss checks the invariants a table rebuild can break without
// raising an error.
//
// # Why this exists
//
// The worst failure a migration can have is one that reports success and deletes
// data. That is not hypothetical: migration 0017 rebuilds pairwise_votes, and its
// backfill is an INSERT ... SELECT whose JOIN target (arenas) is populated LATER
// in the same file. The JOIN matched nothing, the INSERT wrote zero rows without
// raising, the migration was recorded as applied, and the legacy table was
// dropped -- destroying two historical votes. integrity_check reported "ok",
// every later run saw version 17 as done, and nothing anywhere said "error".
//
// The runner cannot catch this by inspecting SQL. It does not know what a
// backfill was supposed to move, and SQLite reports a zero-row INSERT ... SELECT
// as success because it *is* success by any definition SQL has.
//
// What it can do is check the structural invariants that every rebuild must
// leave behind, and those are worth checking everywhere rather than only where a
// mistake has already happened. Each assertion is a property of the schema, not
// of a particular migration, so a future rebuild inherits the guard for free.
//
// Deliberately not included: a general row-count comparison. A migration is
// allowed to delete rows (that is sometimes the point), and guessing which
// deletions were intended is how a guard becomes something to disable.
// querier is the read surface assertNoSilentDataLoss needs. *sql.DB satisfies it,
// and so does *sql.Tx, so the check can run inside the migration's transaction --
// which it must, because the point is to refuse the commit, not to report after it.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func assertNoSilentDataLoss(ctx context.Context, d querier) error {
	checks := []struct {
		what  string
		query string
	}{
		// Every vote belongs to an arena. A vote with no arena is invisible to
		// the ranking queries while still counting in the legacy ones: a vote
		// that moves one ranking and not the other, with no error anywhere.
		{"pairwise votes with no arena", `
			SELECT COUNT(*) FROM pairwise_votes v
			LEFT JOIN arenas a ON a.id = v.arena_id
			WHERE a.id IS NULL`},
		// A vote naming both shapes has to name the same entity twice, or the
		// legacy and generic readers disagree about what was compared.
		{"pairwise votes whose two shapes disagree", `
			SELECT COUNT(*) FROM pairwise_votes
			WHERE feature_a IS NOT NULL AND a IS NOT NULL
			  AND (feature_a <> a OR feature_b <> b)`},
		// An arena entry must point at a real arena. The entity is deliberately
		// not a foreign key (four entity tables, so SQLite cannot express it),
		// which makes this the only thing standing between a delete and a rating
		// that silently stops being scored.
		{"arena entries with no arena", `
			SELECT COUNT(*) FROM arena_entries e
			LEFT JOIN arenas a ON a.id = e.arena_id
			WHERE a.id IS NULL`},
	}
	for _, c := range checks {
		var n int
		if err := d.QueryRowContext(ctx, c.query).Scan(&n); err != nil {
			// The table may simply not exist yet in an early migration; that is
			// not a data-loss signal.
			continue
		}
		if n > 0 {
			return fmt.Errorf("migration left %d %s: "+
				"a rebuild or backfill moved rows it could not resolve. "+
				"Usually the backfill's JOIN dependency is populated later in the "+
				"same file, and SQLite reports a zero-row INSERT ... SELECT as success",
				n, c.what)
		}
	}
	return nil
}

func versionOf(filename string) (int64, error) {
	prefix := strings.SplitN(filename, "_", 2)[0]
	return strconv.ParseInt(prefix, 10, 64)
}
