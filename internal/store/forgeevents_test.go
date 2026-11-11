package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/discovery"
)

// forge_events: what happened in a repository, recorded so a median can be
// recomputed after a rule change (0031_repo_identity.sql, R12 / CF-4.1).
//
// The load-bearing property is IDEMPOTENCY. Forgejo retries a delivery, and a
// table that double-inserts on retry weights whichever deliveries happened to
// be retried. The UNIQUE constraint plus ON CONFLICT DO NOTHING is what makes a
// replay free, and TestReplayInsertsNothing is the test that would catch its
// removal.

func TestForgeEventReplayInsertsNothing(t *testing.T) {
	d := setup(t)
	ctx := context.Background()

	ev := discovery.ForgeEvent{
		RepoFull: "owner/repo", Kind: discovery.ForgeEventPROpened,
		Number: 7, Actor: "alice", At: 1_700_000_000,
	}

	first, err := d.RecordForgeEvent(ctx, ev, 1_700_000_100)
	if err != nil {
		t.Fatalf("first record: %v", err)
	}
	if !first {
		t.Fatal("first record reported inserted=false; the row was new")
	}

	// The same payload again, as a redelivery would arrive it.
	second, err := d.RecordForgeEvent(ctx, ev, 1_700_000_200)
	if err != nil {
		t.Fatalf("replayed record returned an error: %v", err)
	}
	if second {
		t.Fatal("replayed record reported inserted=true; ON CONFLICT DO NOTHING is not in effect")
	}

	got, err := d.ListForgeEvents(ctx, "owner/repo")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after a replay the table holds %d rows, want 1", len(got))
	}
}

func TestForgeEventRefusesUnknownKind(t *testing.T) {
	d := setup(t)

	_, err := d.RecordForgeEvent(context.Background(), discovery.ForgeEvent{
		RepoFull: "owner/repo", Kind: "pr_teleported", Number: 1, Actor: "a", At: 1,
	}, 1)
	if err == nil {
		t.Fatal("an unknown kind was accepted; the table's CHECK would reject it with a column-level error instead of naming the value")
	}
}

func TestForgeEventRefusesEmptyRepo(t *testing.T) {
	d := setup(t)
	// repo_full is NOT NULL, so the insert would fail anyway -- but with a
	// constraint message rather than the sentence a caller can act on.
	_, err := d.RecordForgeEvent(context.Background(), discovery.ForgeEvent{
		Kind: discovery.ForgeEventPush, Actor: "a", At: 1,
	}, 1)
	if err == nil {
		t.Fatal("an event with no repository was accepted")
	}
}

func TestListForgeEventsIsOldestFirstAndScopedToTheRepo(t *testing.T) {
	d := setup(t)
	ctx := context.Background()

	// Inserted newest-first on purpose: the ORDER BY is what the median depends
	// on, and a caller that got them unordered would still be able to sort --
	// but the store promises an order rather than leaving it to chance.
	for _, ev := range []discovery.ForgeEvent{
		{RepoFull: "owner/repo", Kind: discovery.ForgeEventPRReview, Number: 1, Actor: "m", At: 300},
		{RepoFull: "owner/repo", Kind: discovery.ForgeEventPROpened, Number: 1, Actor: "alice", At: 100},
		{RepoFull: "other/repo", Kind: discovery.ForgeEventPROpened, Number: 2, Actor: "bob", At: 50},
		{RepoFull: "owner/repo", Kind: discovery.ForgeEventPRComment, Number: 1, Actor: "m", At: 200},
	} {
		if _, err := d.RecordForgeEvent(ctx, ev, ev.At); err != nil {
			t.Fatalf("record %+v: %v", ev, err)
		}
	}

	got, err := d.ListForgeEvents(ctx, "owner/repo")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events for owner/repo, want 3 (other/repo must not leak in)", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].At < got[i-1].At {
			t.Fatalf("events are not oldest-first: %v at index %d", got[i].At, i)
		}
	}
}

func TestListForgeEventsOnAnUnwatchedRepoIsEmptyNotAnError(t *testing.T) {
	d := setup(t)
	got, err := d.ListForgeEvents(context.Background(), "nobody/nothing")
	if err != nil {
		t.Fatalf("a repo with no events returned an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d events for an unwatched repo, want 0", len(got))
	}
}

func TestForgeEventWithNoPRNumberStoresNull(t *testing.T) {
	d := setup(t)
	ctx := context.Background()
	// A push has no PR number. Storing 0 would collide every push with every
	// other push from the same actor at the same instant; NULL lets the UNIQUE
	// constraint compare them properly.
	if _, err := d.RecordForgeEvent(ctx, discovery.ForgeEvent{
		RepoFull: "owner/repo", Kind: discovery.ForgeEventPush, Actor: "a", At: 100,
	}, 100); err != nil {
		t.Fatalf("record push: %v", err)
	}
	got, err := d.ListForgeEvents(ctx, "owner/repo")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Number != 0 {
		t.Fatalf("push event round-tripped as %+v, want Number 0", got)
	}
}

// R12's write half: the two newcomer columns are computed from forge_events by
// cmd/healthsync and landed here. The rules that matter are (a) an unmeasured
// project stores NULL rather than zero, (b) the sample is stored either way so
// the absence is explained, and (c) this write does not disturb the forge facts
// that a DIFFERENT route owns.

func readNewcomer(t *testing.T, d *DB, projectID int64) (any, int) {
	t.Helper()
	var hours sql.NullFloat64
	var sample int
	if err := d.QueryRowContext(context.Background(),
		`SELECT newcomer_response_hours, newcomer_sample FROM project_metrics WHERE project_id=?`,
		projectID).Scan(&hours, &sample); err != nil {
		t.Fatalf("read newcomer columns: %v", err)
	}
	if !hours.Valid {
		return nil, sample
	}
	return hours.Float64, sample
}

func TestUpdateProjectNewcomerStoresMedianAndSample(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, uid, "resp-project", "Responsive", "d", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	hours := 3.0
	if err := d.UpdateProjectNewcomer(ctx, p.Slug, &hours, 7); err != nil {
		t.Fatalf("UpdateProjectNewcomer: %v", err)
	}
	got, sample := readNewcomer(t, d, p.ID)
	if got != 3.0 {
		t.Fatalf("newcomer_response_hours = %v, want 3", got)
	}
	if sample != 7 {
		t.Fatalf("newcomer_sample = %d, want 7", sample)
	}
}

func TestUpdateProjectNewcomerWithNoMedianStoresNullAndKeepsTheSample(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, uid, "quiet-project", "Quiet", "d", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// Three responses where five are required: the median is unknown, but the
	// count is a fact and it is what explains the project's absence from the
	// leaderboard.
	if err := d.UpdateProjectNewcomer(ctx, p.Slug, nil, 3); err != nil {
		t.Fatalf("UpdateProjectNewcomer: %v", err)
	}
	got, sample := readNewcomer(t, d, p.ID)
	if got != nil {
		t.Fatalf("newcomer_response_hours = %v, want NULL: a zero would claim instant response", got)
	}
	if sample != 3 {
		t.Fatalf("newcomer_sample = %d, want 3", sample)
	}
}

func TestUpdateProjectNewcomerRefusesAMedianBelowTheFloor(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, uid, "floor-project", "Floor", "d", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	hours := 1.5
	err = d.UpdateProjectNewcomer(ctx, p.Slug, &hours, discovery.NewcomerMinSample-1)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("publishing a median over %d samples returned %v, want ErrInvalid",
			discovery.NewcomerMinSample-1, err)
	}
	if got, sample := readNewcomer(t, d, p.ID); got != nil || sample != 0 {
		t.Fatalf("a refused write landed: hours=%v sample=%d", got, sample)
	}
}

func TestUpdateProjectNewcomerLeavesTheForgeFactsAloneAndViceVersa(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, uid, "two-writers", "Two writers", "d", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	m := discovery.Metrics{
		LastCommitAgeDays: 2,
		MedianReviewHours: discovery.UnknownReviewHours,
		Contributors:      4,
		Releases90d:       1,
	}
	if _, err := d.UpdateProjectMetrics(ctx, p.Slug, m, 10, 2, 1, 100, "MIT"); err != nil {
		t.Fatalf("UpdateProjectMetrics: %v", err)
	}
	hours := 12.0
	if err := d.UpdateProjectNewcomer(ctx, p.Slug, &hours, 6); err != nil {
		t.Fatalf("UpdateProjectNewcomer: %v", err)
	}
	// Now the other direction: PATCH /projects/{slug}/metrics has no newcomer
	// fields, so an operator repairing a star count must not blank the median.
	if _, err := d.UpdateProjectMetrics(ctx, p.Slug, m, 11, 2, 1, 101, "MIT"); err != nil {
		t.Fatalf("second UpdateProjectMetrics: %v", err)
	}
	var stars int
	var gotHours sql.NullFloat64
	var sample int
	if err := d.QueryRowContext(ctx,
		`SELECT stars, newcomer_response_hours, newcomer_sample FROM project_metrics WHERE project_id=?`,
		p.ID).Scan(&stars, &gotHours, &sample); err != nil {
		t.Fatalf("read: %v", err)
	}
	if stars != 11 {
		t.Fatalf("stars = %d, want 11", stars)
	}
	if !gotHours.Valid || gotHours.Float64 != 12 {
		t.Fatalf("the metrics write blanked the newcomer median: got %+v", gotHours)
	}
	if sample != 6 {
		t.Fatalf("newcomer_sample = %d, want 6", sample)
	}
}
