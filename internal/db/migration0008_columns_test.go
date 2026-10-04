package db

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// 0008 rebuilds 21 tables and copies each with `INSERT INTO x_new SELECT * FROM x`.
//
// `SELECT *` copies POSITIONALLY. So once any later migration adds a column to a
// rebuilt table, the copy supplies N+1 values to an N-column target and the
// migration dies with:
//
//	table charters_new has 18 columns but 19 values were supplied
//
// The runner applies in version order, so this only bites on a replay -- a
// partially-migrated backup, a fresh database restored from one, or a manual
// re-run. KNOWN-ISSUES recorded it rather than fixing it, on the reasoning that
// "the deployed database is past both, and rewriting a historical migration
// changes what a fresh install does". That is half right: naming the columns
// changes nothing about a fresh install, because on a fresh install the two
// shapes match. It only changes what happens when they DON'T match, which is
// exactly the failure.

// migrateThrough applies only the migrations up to and including version max.
//
// An in-memory fs.FS rather than a temp directory: migrateFS builds its paths as
// `dir + "/" + name`, and neither os.DirFS(".") (rejects the leading "./" with
// "invalid argument") nor dir="" (fs.ReadDir rejects the empty path) works. A
// MapFS sidesteps path handling entirely and keeps the test off the filesystem.
func migrateThrough(t *testing.T, d *sql.DB, max int64) error {
	t.Helper()
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	sub := fstest.MapFS{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		v, err := versionOf(e.Name())
		if err != nil {
			return err
		}
		if v > max {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			return err
		}
		// MapFS is flat: the key must carry the directory migrateFS reads from.
		sub["migrations/"+e.Name()] = &fstest.MapFile{Data: body}
	}
	t.Logf("applying %d migrations (through version %d)", len(sub), max)
	return migrateFS(context.Background(), d, sub, "migrations")
}

// widenEveryRebuiltTable adds a column to EVERY table 0008 rebuilds, so the
// behavioural test above is not quietly narrow.
//
// It was, and the first version of this file caught that only by accident: it
// widened `charters` alone, so reverting `SELECT *` on any of the other 20 tables
// left the test green. A test that exercises one instance of a 21-instance
// pattern proves the class only if the class is one instance wide -- which is
// exactly the assumption that made the original defect survive review.
//
// So the widening is derived from the migration itself rather than hardcoded: the
// tables are exactly those that 0008 creates as `<name>_new`, so a future table
// added to 0008 is covered without editing this file.
func widenEveryRebuiltTable(t *testing.T, d *sql.DB) int {
	t.Helper()
	names := rebuiltTableNames(t)
	for _, n := range names {
		if _, err := d.Exec(
			`ALTER TABLE ` + n + ` ADD COLUMN zz_added_later REAL`); err != nil {
			t.Fatalf("widen %s: %v", n, err)
		}
	}
	return len(names)
}

// rebuiltTableNames reads the set of tables 0008 rebuilds, from the migration.
func rebuiltTableNames(t *testing.T) []string {
	t.Helper()
	body, err := fs.ReadFile(migrationsFS, "migrations/0008_project_delete_cascade.sql")
	if err != nil {
		t.Fatalf("read 0008: %v", err)
	}
	var out []string
	for _, m := range regexp.MustCompile(`CREATE TABLE (\w+)_new \(`).FindAllStringSubmatch(string(body), -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatal("no <name>_new tables found in 0008: the test is guarding nothing")
	}
	return out
}

func TestMigration8SurvivesALaterAddColumn(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqlDB.Close()

	// Apply 0001..0008 ONLY: the state 0008 is designed to receive.
	//
	// Deliberately not Migrate(), which would apply all 24 and include 0011 --
	// the very ADD COLUMN this test creates by hand. Applying the whole set
	// first and then adding the column again fails with "duplicate column name",
	// which is a correct error about a wrong test.
	if err := migrateThrough(t, sqlDB, 8); err != nil {
		t.Fatalf("apply 0001..0008: %v", err)
	}

	// A row, so the test proves the copy preserves DATA and not merely that the
	// statement parses. A copy that silently dropped every row would satisfy any
	// test that only checks for the absence of an error.
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO users (username, display_name, created_at) VALUES ('u','U',1)`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var uid int64
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT id FROM users WHERE username='u'`).Scan(&uid); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO projects (slug, name, governance_model, created_at, updated_at)
		 VALUES ('p','P','collective',1,1)`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	var pid int64
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT id FROM projects WHERE slug='p'`).Scan(&pid); err != nil {
		t.Fatalf("read project: %v", err)
	}
	// One charter per project (project_id is the PRIMARY KEY), so two projects
	// means two rows and the count assertion below means something.
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO charters (project_id) VALUES (?)`, pid); err != nil {
		t.Fatalf("insert charter: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO projects (slug, name, governance_model, created_at, updated_at)
		 VALUES ('q','Q','collective',1,1)`); err != nil {
		t.Fatalf("insert second project: %v", err)
	}
	var pid2 int64
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT id FROM projects WHERE slug='q'`).Scan(&pid2); err != nil {
		t.Fatalf("read second project: %v", err)
	}

	// What later migrations do: add a column to a table 0008 rebuilds. Applied to
	// EVERY such table, not just charters -- see widenEveryRebuiltTable for why a
	// single-table version of this test was quietly too narrow.
	nWidened := widenEveryRebuiltTable(t, sqlDB)
	t.Logf("widened %d rebuilt tables", nWidened)
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO charters (project_id, zz_added_later) VALUES (?, 0.9)`, pid2); err != nil {
		t.Fatalf("insert widened charter: %v", err)
	}

	// The replay, and ONLY 0008.
	//
	// Two things this has to get right, and the first version got the second one
	// wrong in a way that made the test permanently green:
	//
	//   * migrateFS SKIPS any version already in schema_migrations. So without
	//     clearing version 8 first, the "replay" re-ran nothing at all and passed
	//     no matter what 0008 contained. Clearing the record is also the honest
	//     model of the scenario: a restored backup has the rows but not the
	//     bookkeeping.
	//   * Not Migrate(), which would carry on to 0011 and fail with "duplicate
	//     column name" -- correct behaviour, wrong thing to be testing.
	//
	// With `SELECT *` this is where it dies:
	//   table charters_new has 18 columns but 19 values were supplied
	if _, err := sqlDB.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 8`); err != nil {
		t.Fatalf("clear version 8: %v", err)
	}
	if err := migrateThrough(t, sqlDB, 8); err != nil {
		t.Fatalf("replaying 0008 against a widened table failed: %v\n"+
			"0008 must name its columns: SELECT * copies positionally, so a later "+
			"ADD COLUMN supplies more values than the rebuilt table has columns", err)
	}

	var n int
	if err := sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM charters`).Scan(&n); err != nil {
		t.Fatalf("count charters: %v", err)
	}
	if n != 2 {
		t.Errorf("%d charter rows after the rebuild, want 2: the copy lost data", n)
	}
	// The widened column must still EXIST on the rebuilt table, with its value.
	//
	// This is a SECOND defect, and naming the columns did not fix it. 0008
	// rebuilds each table to a fixed shape, so on a replay against a table a later
	// migration widened, the rebuilt table does not carry the new column AT ALL --
	// and the data in it is gone. With `SELECT *` the replay crashed loudly, so
	// the data loss was masked by the crash; naming the columns made the copy
	// succeed and revealed the loss underneath.
	//
	// So the honest fix is not in 0008's column lists but in 0008's SHAPES: a
	// table rebuilt from a hardcoded definition cannot preserve a column it does
	// not know about. Either the rebuilt table must be built from the source
	// table's actual columns, or the replay must not be able to lose them.
	var has int
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('charters') WHERE name='zz_added_later'`).Scan(&has); err != nil {
		t.Fatalf("check widened column: %v", err)
	}
	// The residual gap, stated rather than hidden: widenRebuiltTables skips
	// NOT NULL columns, because SQLite refuses ADD COLUMN NOT NULL without a
	// default on a non-empty table. No later migration adds a NOT NULL column to
	// any of the 21 rebuilt tables, so nothing is lost today -- and this is the
	// boundary of the technique, written down so the next person to add one finds
	// the note before the data loss.
	//
	// The assertion below was FAILING when this test was written, which is how the
	// defect was found: 0008's rebuilt shapes are hardcoded, so the columns did not
	// survive. `t.Skip` was rejected throughout -- a skipped test is invisible in
	// the output, and an invisible defect is how it stays unknown.
	if has != 1 {
		t.Errorf("a column added by a later migration did not survive the rebuild "+
			"(%d found, want 1). 0008 rebuilds to a hardcoded shape, so preserving "+
			"the added columns depends on the concord:preserve-columns-split marker "+
			"being present in 0008 and on widenRebuiltTables running at that point. "+
			"19 later ADD COLUMNs across 5 rebuilt tables depend on this.", has)
	}
}

// TestNoMigrationCopiesRowsWithSelectStar asserts the CLASS rather than the one
// file, so a future migration cannot reintroduce the pattern.
//
// A positional copy is a latent time bomb: it works right up until someone adds a
// column. A test aimed at 0008 alone cannot catch that, because the file it would
// catch is whichever one is written next.
func TestNoMigrationCopiesRowsWithSelectStar(t *testing.T) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		checked++
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			upper := strings.ToUpper(trimmed)
			if !strings.HasPrefix(upper, "INSERT INTO") {
				continue
			}
			// A target column list would mean the first "(" comes before SELECT.
			after := trimmed[len("INSERT INTO"):]
			selectAt := strings.Index(strings.ToUpper(after), "SELECT")
			parenAt := strings.Index(after, "(")
			if selectAt < 0 {
				continue
			}
			if parenAt < 0 || parenAt > selectAt {
				t.Errorf("%s:%d copies rows positionally:\n    %s\n"+
					"    name the target columns, or the copy breaks the moment a later "+
					"migration adds one", e.Name(), i+1, trimmed)
			}
		}
	}
	t.Logf("checked %d migration files for positional row copies", checked)
}
