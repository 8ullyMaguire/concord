package store

import (
	"context"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/discovery"
)

// STAT-3.5: the newcomer-responsiveness leaderboard.
//
// The rule this file exists for is "absent, never last". A project under the
// sample floor must not appear AT ALL: a leaderboard row of 0.0 would rank it
// first, which is the best possible score, and it is the exact inversion of the
// truth -- nothing was measured.

func seedResponsiveness(t *testing.T, d *DB, uid int64, slug string, hours *float64, n int, visibility string) {
	t.Helper()
	ctx := context.Background()
	p, err := d.CreateProject(ctx, uid, slug, slug, "d", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject(%s): %v", slug, err)
	}
	if err := d.UpdateProjectNewcomer(ctx, slug, hours, n); err != nil {
		t.Fatalf("UpdateProjectNewcomer(%s): %v", slug, err)
	}
	if visibility != "" && visibility != "public" {
		if _, err := d.ExecContext(ctx, `UPDATE projects SET visibility=? WHERE id=?`, visibility, p.ID); err != nil {
			t.Fatalf("set visibility on %s: %v", slug, err)
		}
	}
}

func ptr(v float64) *float64 { return &v }

func TestResponsivenessLeaderboardRanksFastestFirstAndIsStable(t *testing.T) {
	d, uid := setupWithUser(t)
	seedResponsiveness(t, d, uid, "slow-project", ptr(40), 6, "")
	seedResponsiveness(t, d, uid, "fast-project", ptr(2), 9, "")
	// Two projects at the same median: the slug tiebreak makes the order a
	// property of the data rather than of the query planner.
	seedResponsiveness(t, d, uid, "tied-b", ptr(7), 5, "")
	seedResponsiveness(t, d, uid, "tied-a", ptr(7), 12, "")

	rows, err := d.ResponsivenessLeaderboard(context.Background())
	if err != nil {
		t.Fatalf("ResponsivenessLeaderboard: %v", err)
	}
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.Slug)
	}
	want := []string{"fast-project", "tied-a", "tied-b", "slow-project"}
	if len(got) != len(want) {
		t.Fatalf("leaderboard = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("leaderboard = %v, want %v (fastest first, slug tiebreak)", got, want)
		}
	}
	if rows[0].N != 9 {
		t.Fatalf("the row lost its sample: n=%d, want 9. A median without n is not publishable", rows[0].N)
	}
}

func TestResponsivenessLeaderboardOmitsProjectsUnderTheFloor(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	seedResponsiveness(t, d, uid, "measured-project", ptr(5), discovery.NewcomerMinSample, "")
	// Four samples, with a median written DIRECTLY -- not through
	// UpdateProjectNewcomer, which refuses exactly this row. The floor is in the
	// query as well as in the writer because a row can arrive here from a path
	// that never called the writer: an import, a backfill, a repair script. That
	// is the state this assertion exists for, and it is why dropping the
	// `newcomer_sample >= ?` clause is a real defect rather than a tidy-up.
	thin, err := d.CreateProject(ctx, uid, "thin-project", "thin-project", "d", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject(thin-project): %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE project_metrics SET newcomer_response_hours=1, newcomer_sample=? WHERE project_id=?`,
		discovery.NewcomerMinSample-1, thin.ID); err != nil {
		t.Fatalf("seed an under-floor row: %v", err)
	}

	rows, err := d.ResponsivenessLeaderboard(ctx)
	if err != nil {
		t.Fatalf("ResponsivenessLeaderboard: %v", err)
	}
	if len(rows) != 1 || rows[0].Slug != "measured-project" {
		t.Fatalf("leaderboard = %+v, want only measured-project: a project under the floor must be ABSENT, not last", rows)
	}
}

func TestResponsivenessLeaderboardOmitsPrivateProjects(t *testing.T) {
	d, uid := setupWithUser(t)
	seedResponsiveness(t, d, uid, "public-project", ptr(6), 8, "")
	// Fastest on the instance, and unlisted: ranking it would disclose a
	// position its owner never published.
	seedResponsiveness(t, d, uid, "private-project", ptr(1), 8, "private")

	rows, err := d.ResponsivenessLeaderboard(context.Background())
	if err != nil {
		t.Fatalf("ResponsivenessLeaderboard: %v", err)
	}
	if len(rows) != 1 || rows[0].Slug != "public-project" {
		t.Fatalf("leaderboard = %+v, want only public-project", rows)
	}
}

func TestResponsivenessLeaderboardWithNothingMeasuredIsEmptyNotNil(t *testing.T) {
	d, uid := setupWithUser(t)
	if _, err := d.CreateProject(context.Background(), uid, "unmeasured", "unmeasured", "d", "collective", "MIT"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	rows, err := d.ResponsivenessLeaderboard(context.Background())
	if err != nil {
		t.Fatalf("ResponsivenessLeaderboard: %v", err)
	}
	if rows == nil {
		t.Fatal("empty leaderboard is nil; the JSON contract for a list route is []")
	}
	if len(rows) != 0 {
		t.Fatalf("leaderboard = %+v, want empty", rows)
	}
}
