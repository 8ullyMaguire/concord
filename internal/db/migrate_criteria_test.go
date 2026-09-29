package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMigration6CreatesCriteriaTables applies the real shipped migration set to a
// scratch database and asserts the criteria tables exist, the direction CHECK is
// enforced, and a duplicate (project_id, slug) is refused.
//
// The migration runner embeds whatever is in migrations/, so a migration that
// parses but produces the wrong shape would otherwise only be discovered in
// production. This is the same discipline migrate_test.go applies to versions.
func TestMigration6CreatesCriteriaTables(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "m6.db")
	// Open (not sql.Open): it applies PRAGMA foreign_keys=ON, and without that
	// the ON DELETE CASCADE below silently does nothing — the test would then
	// assert a guarantee the deployed binary does not actually provide.
	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	if err := Migrate(context.Background(), d); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, table := range []string{
		"criteria", "criterion_ratings", "criterion_votes",
		"criteria_profiles", "criteria_profile_weights",
	} {
		var n int
		err := d.QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`,
			table).Scan(&n)
		if err != nil {
			t.Fatalf("query sqlite_master for %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("table %s missing after migrate", table)
		}
	}

	// Seed the parent rows the CHECK constraints and FKs depend on.
	// users has no password column: auth is by api_tokens, per migration 0001.
	mustExec(t, d, `INSERT INTO users (id, username, display_name, created_at)
		VALUES (1, 'tester', 'Tester', 0)`)
	mustExec(t, d, `INSERT INTO projects (id, slug, name, governance_model, created_at, updated_at)
		VALUES (1, 'p1', 'P1', 'collective', 0, 0)`)
	mustExec(t, d, `INSERT INTO features (id, project_id, author_id, title, created_at, updated_at)
		VALUES (1, 1, 1, 'f1', 0, 0)`)

	// The direction CHECK must reject an unknown value, or a typo would create a
	// criterion that normalises as higher-is-better and quietly mis-ranks.
	if _, err := d.Exec(`INSERT INTO criteria
		(project_id, slug, name, direction, created_at, updated_at)
		VALUES (1, 'bad', 'Bad', 'sideways', 0, 0)`); err == nil {
		t.Error("direction CHECK accepted 'sideways'")
	}

	for i, dir := range []string{"higher_is_better", "lower_is_better"} {
		if _, err := d.Exec(`INSERT INTO criteria
			(project_id, slug, name, direction, created_at, updated_at)
			VALUES (1, ?, 'O', ?, 0, 0)`,
			fmt.Sprintf("ok-%d", i), dir); err != nil {
			t.Errorf("direction %q rejected: %v", dir, err)
		}
	}

	// (project_id, slug) is UNIQUE: two criteria with the same name in one
	// project would double-count a weight.
	if _, err := d.Exec(`INSERT INTO criteria
		(project_id, slug, name, created_at, updated_at)
		VALUES (1, 'dup', 'Dup', 0, 0)`); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO criteria
		(project_id, slug, name, created_at, updated_at)
		VALUES (1, 'dup', 'Dup again', 0, 0)`); err == nil {
		t.Error("duplicate (project_id, slug) was accepted")
	}

	// A criterion vote is scoped to its own project, so a foreign-project
	// criterion must be refused rather than silently cross-linking pools.
	mustExec(t, d, `INSERT INTO criteria
		(id, project_id, slug, name, created_at, updated_at)
		VALUES (10, 1, 'speed', 'Speed', 0, 0)`)
	// project_id is duplicated from the criterion rather than derived, so a
	// mismatch is a caller bug the store must reject. The schema cannot enforce
	// it (a CHECK cannot see the criterion row), which is why the store test
	// covers it.
	if _, err := d.Exec(`INSERT INTO criterion_votes
		(criterion_id, project_id, feature_a, feature_b, voter_id, outcome, created_at)
		VALUES (10, 99, 1, 1, 1, 'a', 0)`); err == nil {
		t.Log("note: cross-project criterion_vote accepted at the schema level; " +
			"the store must reject it")
	}
	if _, err := d.Exec(`DELETE FROM criterion_votes WHERE project_id=99`); err != nil {
		t.Fatalf("clean up cross-project probe: %v", err)
	}

	// One vote per (criterion, voter, pair) — re-voting the same comparison is
	// not a new opinion, it is a double count.
	mustExec(t, d, `INSERT INTO criterion_votes
		(criterion_id, project_id, feature_a, feature_b, voter_id, outcome, created_at)
		VALUES (10, 1, 1, 1, 1, 'a', 0)`)
	if _, err := d.Exec(`INSERT INTO criterion_votes
		(criterion_id, project_id, feature_a, feature_b, voter_id, outcome, created_at)
		VALUES (10, 1, 1, 1, 1, 'b', 0)`); err == nil {
		t.Error("duplicate criterion vote was accepted")
	}

	// Deleting a profile must take its weights with it; a dangling weight would
	// resurrect a ranking under a profile that no longer exists.
	mustExec(t, d, `INSERT INTO criteria_profiles
		(id, project_id, slug, name, created_at, updated_at)
		VALUES (1, 1, 'edge', 'Edge', 0, 0)`)
	mustExec(t, d, `INSERT INTO criteria_profile_weights
		(profile_id, criterion_id, weight) VALUES (1, 10, 2.0)`)
	if _, err := d.Exec(`DELETE FROM criteria_profiles WHERE id=1`); err != nil {
		t.Fatalf("delete profile: %v", err)
	}
	var left int
	if err := d.QueryRow(
		`SELECT count(*) FROM criteria_profile_weights WHERE profile_id=1`).Scan(&left); err != nil {
		t.Fatalf("count weights: %v", err)
	}
	if left != 0 {
		t.Errorf("%d profile weights survived the profile delete", left)
	}

	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		t.Errorf("remove scratch db: %v", err)
	}
}

func mustExec(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
