package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A migration is only safe if it applies to a database that ALREADY has the
// tables, not just to an empty one. 0024 creates a table and two triggers; on a
// fresh database that is trivially fine, and on a populated one it could collide
// with an existing name, trip the runner's bookkeeping, or -- the real risk --
// leave the triggers attached to a table the runner is about to rebuild.
//
// The live instance is 78 tables with 1,806 audit rows and 55 arenas, so the
// honest test copies it (WAL included, per the trap in KNOWN-ISSUES.md) and runs
// the app's own Migrate against the copy.
//
// It skips rather than fails when the live database is absent, because a test
// suite that hard-requires a developer's seeded instance is a test suite that
// fails on every other machine. The skip is loud so it cannot be mistaken for a
// pass.

func liveDBCopy(t *testing.T) (string, bool) {
	t.Helper()
	const live = "/home/alvaro/.local/share/concord/concord.db"
	if _, err := os.Stat(live); err != nil {
		t.Skipf("no live database at %s; skipping the populated-migration check", live)
		return "", false
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "live.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := live + suffix
		if _, err := os.Stat(src); err != nil {
			continue
		}
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		if err := os.WriteFile(dst+suffix, b, 0o600); err != nil {
			t.Fatalf("write %s: %v", dst+suffix, err)
		}
	}
	return dst, true
}

func TestMigration24AppliesToAPopulatedDatabase(t *testing.T) {
	path, ok := liveDBCopy(t)
	if !ok {
		return
	}
	sqlDB, err := Open(path)
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	defer sqlDB.Close()
	ctx := context.Background()

	// Record the pre-state so the test can show the migration is additive rather
	// than silently rebuilding something.
	var tablesBefore, auditsBefore int
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&tablesBefore); err != nil {
		t.Fatalf("count tables before: %v", err)
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM audit_log`).Scan(&auditsBefore); err != nil {
		t.Fatalf("count audits before: %v", err)
	}
	t.Logf("live copy: %d tables, %d audit rows", tablesBefore, auditsBefore)

	if err := Migrate(ctx, sqlDB); err != nil {
		t.Fatalf("migrate a populated database: %v", err)
	}

	var auditsAfter, tablesAfter int
	if err := sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM audit_log`).Scan(&auditsAfter); err != nil {
		t.Fatalf("count audits after: %v", err)
	}
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&tablesAfter); err != nil {
		t.Fatalf("count tables after: %v", err)
	}
	if auditsAfter != auditsBefore {
		t.Errorf("audit rows went from %d to %d: the migration lost data", auditsBefore, auditsAfter)
	}
	// +1 for maintenance_signals. Anything else means it rebuilt something.
	if want := tablesBefore + 1; tablesAfter != want {
		t.Errorf("table count went from %d to %d, want %d: the migration was not purely additive",
			tablesBefore, tablesAfter, want)
	}

	// The triggers must be present AND working on the populated database -- a
	// trigger that exists on an empty database and is absent here would be a
	// silent difference between test and production.
	var n int
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='trigger'
		 AND name IN ('trg_pairwise_votes_no_update','trg_pairwise_votes_no_delete')`).Scan(&n); err != nil {
		t.Fatalf("count triggers: %v", err)
	}
	if n != 2 {
		t.Errorf("%d append-only triggers present on the populated database, want 2", n)
	}

	// And prove they fire. The live copy holds no votes, so seed one -- reading
	// the live rows here would be a vacuous assertion for the same reason the
	// first version of the python check was.
	seedVoteRow(t, sqlDB)
	if _, err := sqlDB.ExecContext(ctx, `UPDATE pairwise_votes SET outcome='b'`); err == nil {
		t.Error("UPDATE on a vote SUCCEEDED on the populated database: the trigger is not attached")
	}
	if _, err := sqlDB.ExecContext(ctx, `DELETE FROM pairwise_votes`); err == nil {
		t.Error("DELETE on a vote SUCCEEDED on the populated database: the trigger is not attached")
	}
}
