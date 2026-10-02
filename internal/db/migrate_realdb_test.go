package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigrateRebuildPreservesData migrates a database file that already holds
// rows, because a table rebuild that only ever runs on an empty table proves
// nothing about the data it is supposed to preserve.
//
// This is not hypothetical. Migration 0014 rebuilds consensus_calls, and while
// writing it I found that CloseConsensusCall has always written a closed_at
// column that was never in the schema -- so closing any consensus call failed
// with "no such column". An empty-database migration test cannot catch that,
// because the failing query is never run.
//
// The test migrates first (to get the real schema), seeds rows using only the
// NOT NULL columns the application writes, then asserts the shapes 0014 exists
// to provide: a nullable feature_id for a process decision, a readable question,
// and a closed_at column that is actually there.
func TestMigrateRebuildPreservesData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seeded.db")

	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqlDB.Close()
	if err := Migrate(context.Background(), sqlDB); err != nil {
		t.Fatalf("Migrate on empty db: %v", err)
	}

	// Seed only columns with no default, mirroring what the store layer writes.
	// Any NOT NULL column missed here surfaces as a clear error.
	seeds := []struct{ name, stmt string }{
		{"users", `INSERT INTO users (id, username, created_at) VALUES (1, 'seeduser', 1700000000)`},
		{"projects", `INSERT INTO projects (id, slug, name, created_at, updated_at)
			VALUES (1, 'seedproj', 'Seed Project', 1700000000, 1700000000)`},
		{"complaints", `INSERT INTO complaints (id, project_id, author_id, title, severity, created_at, updated_at)
			VALUES (1, 1, 1, 'seed complaint', 1, 1700000000, 1700000000)`},
		{"features", `INSERT INTO features (id, project_id, author_id, title, status, created_at, updated_at)
			VALUES (1, 1, 1, 'seed feature', 'draft', 1700000000, 1700000000)`},
		{"call with feature", `INSERT INTO consensus_calls
			(id, project_id, feature_id, opened_by, opens_at, closes_at, status)
			VALUES (1, 1, 1, 1, 1700000000, 1700000000, 'open')`},
	}
	for _, s := range seeds {
		if _, err := sqlDB.Exec(s.stmt); err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
	}

	// A process decision: no feature, and a stated question. This shape is why
	// 0014 makes feature_id nullable -- emergency-hold confirmations and charter
	// amendments are decisions without a feature.
	if _, err := sqlDB.Exec(`INSERT INTO consensus_calls
		(id, project_id, feature_id, opened_by, opens_at, closes_at, status, question, description)
		VALUES (2, 1, NULL, 1, 1700000000, 1700000000, 'open', 'Hold expired?', 'the stated reason')`); err != nil {
		t.Fatalf("insert process call with NULL feature_id: %v", err)
	}

	t.Run("closed_at exists", func(t *testing.T) {
		// CloseConsensusCall writes this column; it was missing from the schema
		// for the life of the table, so closing a call always errored.
		if _, err := sqlDB.Exec(
			`UPDATE consensus_calls SET closed_at=1700000000 WHERE id=1`); err != nil {
			t.Fatalf("UPDATE closed_at: %v", err)
		}
		var got sql.NullFloat64
		if err := sqlDB.QueryRow(
			`SELECT closed_at FROM consensus_calls WHERE id=1`).Scan(&got); err != nil {
			t.Fatalf("read closed_at: %v", err)
		}
		if !got.Valid || got.Float64 != 1700000000 {
			t.Errorf("closed_at = %v, want 1700000000", got)
		}
	})

	t.Run("feature_id is nullable", func(t *testing.T) {
		var fid sql.NullInt64
		if err := sqlDB.QueryRow(
			`SELECT feature_id FROM consensus_calls WHERE id=2`).Scan(&fid); err != nil {
			t.Fatalf("read NULL feature_id: %v", err)
		}
		if fid.Valid {
			t.Errorf("feature_id = %d, want NULL", fid.Int64)
		}
	})

	t.Run("question round-trips", func(t *testing.T) {
		// The question is the decision itself. It used to be passed in and
		// discarded, so no call recorded what was being decided.
		var q, desc string
		if err := sqlDB.QueryRow(
			`SELECT question, description FROM consensus_calls WHERE id=2`).Scan(&q, &desc); err != nil {
			t.Fatalf("read question: %v", err)
		}
		if q != "Hold expired?" {
			t.Errorf("question = %q, want 'Hold expired?'", q)
		}
		if desc != "the stated reason" {
			t.Errorf("description = %q, want the stated reason", desc)
		}
	})

	t.Run("migrate is idempotent on populated data", func(t *testing.T) {
		if err := Migrate(context.Background(), sqlDB); err != nil {
			t.Fatalf("second Migrate: %v", err)
		}
		var n int
		if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM consensus_calls`).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 2 {
			t.Errorf("consensus_calls = %d after re-migrate, want 2", n)
		}
	})

	t.Run("emergency hold tables and ledger triggers exist", func(t *testing.T) {
		// 0013 creates the hold and the append-only admin ledger. The triggers
		// are the enforcement for §9.3, so their absence would be silent.
		var n int
		if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master
			WHERE type='table' AND name IN ('emergency_holds','admin_ledger')`).Scan(&n); err != nil {
			t.Fatalf("count tables: %v", err)
		}
		if n != 2 {
			t.Errorf("hold/ledger tables found = %d, want 2", n)
		}
		var trg int
		if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master
			WHERE type='trigger' AND tbl_name='admin_ledger'`).Scan(&trg); err != nil {
			t.Fatalf("count triggers: %v", err)
		}
		if trg != 2 {
			t.Errorf("admin_ledger triggers = %d, want 2 (update and delete must both be refused)", trg)
		}
	})
}
