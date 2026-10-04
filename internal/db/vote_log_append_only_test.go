package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// pairwise_votes is append-only, enforced by migration 0024 (spec §9.3's
// always-visible rule, applied to the table every rating is derived from).
//
// THE POINT OF THIS FILE IS THAT IT WRITES THE BAD ROW. `pg_constraint proves a
// constraint EXISTS` has no sqlite3 analogue and the Go way has the same hole:
// TestVoteLogIsAppendOnlyExistingTheTriggers is satisfied by a migration that
// creates the triggers and drops them a line later, and by a trigger whose WHEN
// clause always evaluates false. The only assertion that means anything is the
// one that attempts the mutation and requires it to fail.
//
// The escape hatch is tested in the same way, in both directions: dedupe.py
// --apply must still be able to delete a vote while armed, and the arming must
// stop working the moment it is disarmed. A gate that only proves the happy path
// is half a gate -- the same tautology in a second costume, as in the DTO
// whitelist that listed required keys as well as forbidden ones.

// openMigrated opens a fresh migrated database and returns it plus a closer.
func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := Migrate(context.Background(), sqlDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return sqlDB
}

// seedVoteRow inserts one vote, bypassing the application layer entirely.
func seedVoteRow(t *testing.T, d *sql.DB) { seedVoteRowN(t, d, 0) }

// seedVoteRowN is seedVoteRow with a distinguishable username, for a test that
// needs a second vote in the same database. Re-seeding with the same username
// fails on users.username UNIQUE, and that failure reads as "the escape hatch
// did not work" when it is only the fixture colliding with itself.
func seedVoteRowN(t *testing.T, d *sql.DB, n int) {
	t.Helper()
	ctx := context.Background()
	username := "voter"
	if n > 0 {
		username = fmt.Sprintf("voter-%d", n)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO users (username, display_name, created_at) VALUES (?,'Voter',1)`,
		username); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var uid int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM users WHERE username=?`, username).Scan(&uid); err != nil {
		t.Fatalf("read user id: %v", err)
	}
	// The project, its features and its arena are created once and REUSED, so a
	// second seed in the same test adds a vote rather than colliding with a
	// duplicate slug. INSERT OR IGNORE plus a re-read is the honest form here:
	// the fixture must not fail for a reason unrelated to what is under test.
	if _, err := d.ExecContext(ctx,
		`INSERT OR IGNORE INTO projects (slug, name, governance_model, created_at, updated_at)
		 VALUES ('vp','Voting Project','collective',1,1)`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	var pid int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM projects WHERE slug='vp'`).Scan(&pid); err != nil {
		t.Fatalf("read project id: %v", err)
	}
	// Each feature is created only if absent, and looked up BY TITLE.
	//
	// INSERT OR IGNORE is not enough: features has no unique constraint on
	// (project_id, title), so it ignores nothing and a second seed adds a
	// duplicate pair -- which then fails the "want 2 features" assertion below
	// and reads as a broken vote rather than as a duplicated fixture.
	ids := []int64{}
	for i, title := range []string{"f-a", "f-b"} {
		var id int64
		err := d.QueryRowContext(ctx,
			`SELECT id FROM features WHERE project_id=? AND title=?`, pid, title).Scan(&id)
		switch {
		case err == sql.ErrNoRows:
			// author_id is NOT NULL and status is CHECKed, so both come from the
			// schema rather than guessed: a fixture omitting a NOT NULL column
			// fails in the INSERT and reads as a broken test rather than a bad row.
			// "draft" is in features.status's CHECK; "proposed" is not, and the
			// error message is a cheaper place to learn that than the migration.
			res, err := d.ExecContext(ctx,
				`INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)
				 VALUES (?,?,?,?,?,1,1)`, pid, uid, title, title, "draft")
			if err != nil {
				t.Fatalf("insert feature %d: %v", i, err)
			}
			if n, err := res.LastInsertId(); err == nil {
				id = n
			} else {
				if err := d.QueryRowContext(ctx,
					`SELECT id FROM features WHERE project_id=? AND title=?`, pid, title).Scan(&id); err != nil {
					t.Fatalf("re-read feature %d: %v", i, err)
				}
			}
		case err != nil:
			t.Fatalf("look up feature %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d feature ids, want 2: the fixture needs a distinct pair to vote between", len(ids))
	}
	fa, fb := ids[0], ids[1]
	if fa == fb {
		t.Fatal("both feature ids are the same; a vote between one feature and itself is not a vote")
	}
	// arena_id is NOT NULL since 0017, so a vote without an arena is not a row
	// production can hold. Seeding the real arena rather than passing NULL keeps
	// the fixture on the shape the store actually writes, and means the trigger
	// under test is guarding the table as it exists rather than a variant.
	if _, err := d.ExecContext(ctx,
		`INSERT OR IGNORE INTO arenas (type, project_id, created_at) VALUES ('feature-priority', ?, 1)`,
		pid); err != nil {
		t.Fatalf("insert arena: %v", err)
	}
	var aid int64
	if err := d.QueryRowContext(ctx,
		`SELECT id FROM arenas WHERE project_id=? AND type='feature-priority'`, pid).Scan(&aid); err != nil {
		t.Fatalf("read arena id: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO pairwise_votes (project_id, arena_id, voter_id, feature_a, feature_b,
		 outcome, weight, created_at)
		 VALUES (?,?,?,?,?,?,1.0,1)`, pid, aid, uid, fa, fb, "a"); err != nil {
		t.Fatalf("insert vote: %v", err)
	}
}

func TestAVoteCannotBeEdited(t *testing.T) {
	d := openMigrated(t)
	seedVoteRow(t, d)

	_, err := d.Exec(`UPDATE pairwise_votes SET outcome = 'b'`)
	if err == nil {
		t.Fatal("updating a vote's outcome SUCCEEDED: the append-only trigger is not firing")
	}
	// The message, not just the failure: "some error" is satisfied by a syntax
	// error in the UPDATE itself, which proves nothing about the trigger. This
	// is the assertion that separates "the trigger refused it" from "my UPDATE
	// was malformed".
	if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("update failed for the wrong reason: %v\nwant an error naming the append-only rule", err)
	}

	// And the row is genuinely untouched, not merely reported as an error.
	var outcome string
	if err := d.QueryRow(`SELECT outcome FROM pairwise_votes`).Scan(&outcome); err != nil {
		t.Fatalf("read outcome: %v", err)
	}
	if outcome != "a" {
		t.Errorf("outcome is %q after a refused UPDATE, want the original %q", outcome, "a")
	}
}

func TestAVoteCannotBeDeleted(t *testing.T) {
	d := openMigrated(t)
	seedVoteRow(t, d)

	_, err := d.Exec(`DELETE FROM pairwise_votes`)
	if err == nil {
		t.Fatal("deleting a vote SUCCEEDED: the append-only trigger is not firing")
	}
	if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("delete failed for the wrong reason: %v\nwant an error naming the append-only rule", err)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM pairwise_votes`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("%d votes remain after a refused DELETE, want 1", n)
	}
}

// TestTheDedupeEscapeHatchWorksOnlyWhileArmed is the other half, and it is the
// half that would make the trigger a trap.
//
// seed/dedupe.py --apply deletes from pairwise_votes when it repairs duplicate
// seed data. If a blanket trigger stopped it, the maintenance script would crash
// instead of warning. So the hatch must open, and it must close again.
func TestTheDedupeEscapeHatchWorksOnlyWhileArmed(t *testing.T) {
	d := openMigrated(t)
	seedVoteRow(t, d)

	// Disarmed (the default): refused, which is the everyday case.
	if _, err := d.Exec(`DELETE FROM pairwise_votes`); err == nil {
		t.Fatal("a delete succeeded with the hatch disarmed")
	}

	// Armed, as dedupe.py sets it.
	if _, err := d.Exec(
		`INSERT INTO maintenance_signals (name, enabled, set_at, set_by)
		 VALUES ('seed_dedupe', 1, 1, 'test')`); err != nil {
		t.Fatalf("arm the hatch: %v", err)
	}
	if _, err := d.Exec(`DELETE FROM pairwise_votes`); err != nil {
		t.Fatalf("delete with the hatch armed: %v\ndedupe.py --apply could not repair a duplicate", err)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM pairwise_votes`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("%d votes remain after an armed delete, want 0", n)
	}

	// Disarmed again, and re-seeded: refused once more. A hatch that stays open
	// is not a hatch, it is the absence of a rule.
	if _, err := d.Exec(`UPDATE maintenance_signals SET enabled = 0 WHERE name='seed_dedupe'`); err != nil {
		t.Fatalf("disarm: %v", err)
	}
	// A DIFFERENT username, because the first one is still in users -- reusing it
	// fails on the UNIQUE constraint, which reads as "the hatch is broken" rather
	// than as the fixture colliding with itself.
	seedVoteRowN(t, d, 1)
	if _, err := d.Exec(`DELETE FROM pairwise_votes`); err == nil {
		t.Fatal("a delete succeeded after the hatch was disarmed")
	}
}

// TestTheHatchCannotBeArmedIntoPermittingMoreThanOneRun: the trigger requires
// EXACTLY ONE enabled signal. Two concurrent maintenance runs must not each be
// able to delete -- the second one's deletes would ride on the first one's
// arming, and neither would know the other existed.
func TestTheHatchCannotBeArmedIntoPermittingMoreThanOneRun(t *testing.T) {
	d := openMigrated(t)
	seedVoteRow(t, d)

	for _, name := range []string{"seed_dedupe", "something_else"} {
		if _, err := d.Exec(
			`INSERT INTO maintenance_signals (name, enabled, set_at, set_by) VALUES (?,1,1,'test')`,
			name); err != nil {
			t.Fatalf("arm %s: %v", name, err)
		}
	}
	if _, err := d.Exec(`DELETE FROM pairwise_votes`); err == nil {
		t.Fatal("a delete succeeded with TWO maintenance signals enabled: the " +
			"exactly-one check in the WHEN clause is not firing")
	}
}

// TestTheHatchSignalIsCheckedNotAnyValue: the flag column has a CHECK, so it
// cannot be armed with a truthy string. A trigger that tested `enabled` for
// non-null instead of = 1 would be defeatable by this insert.
func TestTheHatchSignalIsCheckedNotAnyValue(t *testing.T) {
	d := openMigrated(t)

	_, err := d.Exec(
		`INSERT INTO maintenance_signals (name, enabled, set_at, set_by) VALUES ('bogus', 2, 1, 'test')`)
	if err == nil {
		t.Error("maintenance_signals accepted enabled=2: the CHECK is missing, so a " +
			"trigger testing for a truthy value would be open to any non-zero int")
	}
}
