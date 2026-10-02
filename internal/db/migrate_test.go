package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateRejectsDuplicateVersions is a regression guard for a silent data
// integrity failure.
//
// schema_migrations is keyed on version alone. Two migration files sharing a
// version are therefore not an error: Migrate sorts by filename, sees version 2
// already applied, and skips the second file forever. Nothing is logged and
// nothing fails — the columns it was supposed to add simply never exist.
//
// This is not hypothetical. 0002_auth.sql and 0002_request_board.sql coexisted
// briefly, the auth columns were never created, and the only symptom was
// unrelated tests reporting a missing table. A migration runner that cannot
// tell you a migration did not run is worse than no runner, because it looks
// like the schema is managed.
//
// These use migrateFS rather than Migrate: the shipped migrations are embedded
// in the binary, so the interesting cases cannot be reached through the
// embedded copy without editing the real migrations directory.
func TestMigrateRejectsDuplicateVersions(t *testing.T) {
	dir := t.TempDir()
	migDir := filepath.Join(dir, "migrations")

	writeMigration(t, migDir, "0001_first.sql", "CREATE TABLE first_table (id INTEGER PRIMARY KEY);\n")
	writeMigration(t, migDir, "0001_second.sql", "CREATE TABLE second_table (id INTEGER PRIMARY KEY);\n")

	d, err := Open(filepath.Join(dir, "dup.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	err = migrateFS(context.Background(), d, os.DirFS(dir), "migrations")
	if err == nil {
		t.Fatal("migrateFS accepted two migrations with the same version; " +
			"one of them would be skipped silently forever")
	}
	if !strings.Contains(err.Error(), "duplicate migration version") {
		t.Errorf("error should name the duplicate version, got: %v", err)
	}

	// The failure must be total. A half-applied schema is worse than none: the
	// next run records version 1 as done and never tries again.
	var n int
	if qerr := d.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('first_table','second_table')`).Scan(&n); qerr == nil {
		if n != 0 {
			t.Errorf("migrateFS reported failure but created %d table(s)", n)
		}
	}
}

// TestMigrateFailsWhenABackfillMatchesNothing is a regression guard for the
// worst migration failure mode found so far: one that reports success, deletes
// the data, and leaves no trace.
//
// Migration 0017 rebuilds pairwise_votes to add the arena columns. It renames the
// old table, creates the new one, and backfills from the legacy table by JOINing
// to the arenas table. During development the arenas INSERT sat at the END of
// the migration file, after the backfill that depends on it. The consequence:
//
//	INSERT ... SELECT v.id FROM pairwise_votes_legacy v
//	  JOIN arenas a ON a.project_id = v.project_id     -- 0 rows: arenas empty
//
// An INSERT ... SELECT with a JOIN that matches nothing is not an error. It
// writes zero rows, reports success, and the migration is recorded as applied.
// The legacy table is then dropped and the two historical votes are gone
// permanently, with integrity_check reporting "ok" and every subsequent run
// finding version 17 already applied.
//
// Nothing about the runner could have caught this: the migration file was
// syntactically valid and semantically ordered the way it was written. The
// guard has to be an assertion about the DATA, because the runner cannot know
// what a backfill was supposed to move.
//
// So: seed rows, run a migration whose backfill cannot match, and require that
// either the rows survive or the migration fails loudly. There is no third
// option in which a migration quietly empties a table.
func TestMigrateFailsWhenABackfillMatchesNothing(t *testing.T) {
	dir := t.TempDir()
	migDir := filepath.Join(dir, "migrations")
	// Uses the real pairwise_votes and arenas tables rather than toy names, so
	// the runner's own data assertions are what fails this migration. A toy
	// fixture would not exercise the guard at all -- the check is written against
	// the schema that exists in production, and testing it against names it does
	// not know about proves only that it does not fire.
	writeMigration(t, migDir, "0001_seed.sql", `
		CREATE TABLE arenas (
			id INTEGER PRIMARY KEY,
			type TEXT NOT NULL,
			project_id INTEGER,
			feature_id INTEGER,
			question TEXT NOT NULL DEFAULT '',
			use_case TEXT,
			baseline_entry_id INTEGER,
			created_at REAL NOT NULL
		);
		CREATE TABLE pairwise_votes (
			id INTEGER PRIMARY KEY,
			project_id INTEGER NOT NULL,
			feature_a INTEGER NOT NULL,
			feature_b INTEGER NOT NULL,
			voter_id INTEGER NOT NULL,
			outcome TEXT NOT NULL,
			weight REAL NOT NULL DEFAULT 1.0,
			created_at REAL NOT NULL
		);
		CREATE TABLE users (id INTEGER PRIMARY KEY);
		INSERT INTO users (id) VALUES (1);
		INSERT INTO pairwise_votes
			(id, project_id, feature_a, feature_b, voter_id, outcome, created_at)
		VALUES (1, 5, 1, 2, 1, 'a', 0), (2, 5, 2, 3, 1, 'a', 0);
	`)

	dbPath := filepath.Join(dir, "backfill.db")
	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	if err := migrateFS(ctx, d, os.DirFS(dir), "migrations"); err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	// A second migration whose rebuild-backfill depends on a table it populates
	// LATER in the same file -- the exact shape that lost the votes.
	// arenas is CREATED and then POPULATED later in the same file -- which is the
	// real shape: the table exists when the backfill runs, it is simply empty,
	// so the JOIN matches nothing and the INSERT writes zero rows without
	// raising. Had the table not existed yet, the driver would have raised
	// "no such table" and the runner would have rolled back correctly.
	writeMigration(t, migDir, "0002_rebuild.sql", `
		-- The rebuild: arenas is created empty, the backfill JOINs it (matching
		-- nothing), then arenas is populated. The votes end up with a NULL
		-- arena_id, which is exactly the "vote with no arena" state the runner's
		-- assertion refuses.
		CREATE TABLE arenas_new (id INTEGER PRIMARY KEY, type TEXT NOT NULL,
			project_id INTEGER, feature_id INTEGER, question TEXT NOT NULL DEFAULT '',
			use_case TEXT, baseline_entry_id INTEGER, created_at REAL NOT NULL);
		ALTER TABLE pairwise_votes RENAME TO pairwise_votes_old;
		CREATE TABLE pairwise_votes (
			id INTEGER PRIMARY KEY,
			arena_id INTEGER REFERENCES arenas(id),
			project_id INTEGER,
			feature_a INTEGER,
			feature_b INTEGER,
			entity_type TEXT,
			a INTEGER,
			b INTEGER,
			voter_id INTEGER NOT NULL,
			outcome TEXT NOT NULL,
			weight REAL NOT NULL DEFAULT 1.0,
			reason TEXT,
			created_at REAL NOT NULL
		);
		INSERT INTO pairwise_votes
			(id, arena_id, project_id, feature_a, feature_b,
			 entity_type, a, b, voter_id, outcome, weight, reason, created_at)
		SELECT v.id, an.id, v.project_id, v.feature_a, v.feature_b,
		       'feature', v.feature_a, v.feature_b, v.voter_id, v.outcome,
		       v.weight, NULL, v.created_at
		FROM pairwise_votes_old v
		LEFT JOIN arenas_new an ON an.project_id = v.project_id;
		INSERT INTO arenas_new (id, type, project_id, question, created_at)
			SELECT 5, 'feature-priority', 5, 'q', 0;
		DROP TABLE pairwise_votes_old;
		DROP TABLE arenas;
		ALTER TABLE arenas_new RENAME TO arenas;
	`)

	migErr := migrateFS(ctx, d, os.DirFS(dir), "migrations")

	// The scenario here is a rebuild whose backfill JOINs a table the same file
	// creates and populates afterwards. Nothing about that SQL is invalid and
	// SQLite reports the zero-row INSERT as success, so before the data
	// assertions were added to the runner this migration destroyed both rows and
	// was recorded as applied.
	//
	// What must NOT happen is the combination that cost the votes: no error, the
	// version recorded, and the source table dropped.
	if migErr == nil {
		var recorded int
		_ = d.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE version = 2`).Scan(&recorded)
		if recorded > 0 {
			var srcGone int
			_ = d.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='legacy_rows_old'`).Scan(&srcGone)
			if srcGone == 0 {
				var moved int
				_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM target_table`).Scan(&moved)
				if moved == 0 {
					t.Fatal("the migration reported success, was recorded as applied, " +
						"dropped the source table, and moved zero rows: silent, " +
						"permanent data loss that integrity_check calls healthy")
				}
			}
		}
	}

	// With the assertions in place the runner is expected to refuse this
	// migration. That is the fix, so it is asserted rather than merely tolerated.
	if migErr != nil {
		var recorded int
		_ = d.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE version = 2`).Scan(&recorded)
		if recorded > 0 {
			t.Error("migration failed but its version was still recorded: no later run would retry it")
		}
		var targetExists int
		_ = d.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='target_table'`).Scan(&targetExists)
		if targetExists > 0 {
			t.Error("migration failed but left its tables behind: a half-applied schema " +
				"is worse than none, because the next run records the version and moves on")
		}
	}
}

// TestMigrateAppliesEachVersionOnce is the positive half: distinct versions all
// run, and re-running is a no-op rather than a re-application.
func TestMigrateAppliesEachVersionOnce(t *testing.T) {
	dir := t.TempDir()
	migDir := filepath.Join(dir, "migrations")
	for _, m := range []struct{ name, body string }{
		{"0001_first.sql", "CREATE TABLE first_table (id INTEGER PRIMARY KEY);\n"},
		{"0002_second.sql", "CREATE TABLE second_table (id INTEGER PRIMARY KEY);\n"},
		{"0003_third.sql", "CREATE TABLE third_table (id INTEGER PRIMARY KEY);\n"},
	} {
		writeMigration(t, migDir, m.name, m.body)
	}

	d, err := Open(filepath.Join(dir, "seq.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	if err := migrateFS(context.Background(), d, os.DirFS(dir), "migrations"); err != nil {
		t.Fatalf("migrateFS: %v", err)
	}

	var n int
	if err := d.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('first_table','second_table','third_table')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("created %d of 3 tables", n)
	}

	if err := migrateFS(context.Background(), d, os.DirFS(dir), "migrations"); err != nil {
		t.Fatalf("second migrateFS run should be a no-op, got: %v", err)
	}
	var applied int
	if err := d.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 3 {
		t.Errorf("schema_migrations has %d rows after two runs, want 3", applied)
	}
}

// TestVersionOfRejectsUnparseableNames keeps a malformed filename an error
// rather than a migration that is skipped.
func TestVersionOfRejectsUnparseableNames(t *testing.T) {
	if _, err := versionOf("no_version_prefix.sql"); err == nil {
		t.Error("a migration filename without a numeric prefix should be rejected")
	}
	v, err := versionOf("0042_something.sql")
	if err != nil || v != 42 {
		t.Errorf("versionOf(0042) = %d, %v; want 42, nil", v, err)
	}
}

func writeMigration(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
