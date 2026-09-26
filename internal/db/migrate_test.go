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
