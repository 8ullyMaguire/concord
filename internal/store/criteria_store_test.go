package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

// secondProject makes a distinct project in the same DB. setupWithProject
// hardcodes the slug "test-project", so calling it twice collides on the UNIQUE
// constraint rather than giving a second project.
func secondProject(t *testing.T, d *DB, uid int64) int64 {
	t.Helper()
	n := time.Now().UnixNano()
	proj, err := d.CreateProject(context.Background(), uid,
		fmt.Sprintf("second-%d", n), "Second", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return proj.ID
}

// secondFixture is criteriaFixture for a second project in the same DB.
func secondFixture(t *testing.T, d *DB, uid int64) (*DB, int64, int64, []int64) {
	t.Helper()
	ctx := context.Background()
	pid := secondProject(t, d, uid)
	comp, err := d.CreateComplaint(ctx, pid, uid, "why2", "body", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := d.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	ids := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		f, err := d.CreateFeature(ctx, pid, uid,
			fmt.Sprintf("other feat %d", i), "body", []int64{comp.ID})
		if err != nil {
			t.Fatalf("CreateFeature: %v", err)
		}
		ids = append(ids, f.ID)
	}
	return d, uid, pid, ids
}

// criteriaFixture builds a project with three features and one user.
//
// CreateFeature refuses a feature with no linked validated complaint — the same
// rule the API enforces — so the fixture creates and validates one complaint and
// hangs every feature off it. Guessing this signature cost a build cycle.
func criteriaFixture(t *testing.T) (*DB, int64, int64, []int64) {
	t.Helper()
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()

	comp, err := d.CreateComplaint(ctx, pid, uid, "why", "body", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := d.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}

	ids := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		f, err := d.CreateFeature(ctx, pid, uid,
			[]string{"feat a", "feat b", "feat c"}[i], "body", []int64{comp.ID})
		if err != nil {
			t.Fatalf("CreateFeature %d: %v", i, err)
		}
		ids = append(ids, f.ID)
	}
	return d, uid, pid, ids
}

func mkCriterion(t *testing.T, d *DB, uid, pid int64, slug, dir string) ranking.Criterion {
	t.Helper()
	// created_by is a real FK to users(id), so it cannot be 0.
	c, err := d.CreateCriterion(context.Background(), pid, uid, slug, slug, "desc", dir, 1.0)
	if err != nil {
		t.Fatalf("CreateCriterion %s: %v", slug, err)
	}
	return c
}

// A criterion belongs to one project, and its slug is unique within it — two
// criteria of the same name would double-count a weight.
func TestCreateCriterionUniqueSlugPerProject(t *testing.T) {
	d, uid, pid, _ := criteriaFixture(t)
	ctx := context.Background()
	mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)

	_, err := d.CreateCriterion(ctx, pid, uid, "design", "Design again", "", ranking.DirHigher, 1)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("duplicate slug err = %v, want ErrInvalid", err)
	}

	// The same slug in a different project is fine: pools are per-project.
	pid2 := secondProject(t, d, uid)
	if _, err := d.CreateCriterion(ctx, pid2, uid, "design", "Design", "",
		ranking.DirHigher, 1); err != nil {
		t.Errorf("same slug in another project rejected: %v", err)
	}
}

// A typo'd direction must be rejected, not defaulted. A criterion created as
// "sideways" would normalise as higher-is-better and quietly mis-rank every
// query that used it.
func TestCreateCriterionRejectsBadDirection(t *testing.T) {
	d, uid, pid, _ := criteriaFixture(t)
	_, err := d.CreateCriterion(context.Background(), pid, uid, "x", "X", "", "sideways", 1)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("bad direction err = %v, want ErrInvalid", err)
	}
}

func TestCreateCriterionRejectsEmpty(t *testing.T) {
	d, uid, pid, _ := criteriaFixture(t)
	ctx := context.Background()
	if _, err := d.CreateCriterion(ctx, pid, uid, "  ", "X", "", ranking.DirHigher, 1); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty slug accepted")
	}
	if _, err := d.CreateCriterion(ctx, pid, uid, "ok", "", "", ranking.DirHigher, 1); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty name accepted")
	}
}

// The scope check is the whole point: a caller must not pool votes from another
// project's features into this criterion.
func TestCriterionVoteRejectsForeignFeatures(t *testing.T) {
	d, uid, pid, ids := criteriaFixture(t)
	_, _, pid2, otherIDs := secondFixture(t, d, uid)

	c := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)
	ctx := context.Background()

	// A feature from another project.
	err := d.CastCriterionVote(ctx, pid, c.ID, uid, ids[0], otherIDs[0], "a", 1, 0.5)
	if !errors.Is(err, ErrCriterionScope) {
		t.Errorf("foreign feature err = %v, want ErrCriterionScope", err)
	}

	// A criterion from another project, addressed through this project.
	c2 := mkCriterion(t, d, uid, pid2, "speed", ranking.DirHigher)
	err = d.CastCriterionVote(ctx, pid, c2.ID, uid, ids[0], ids[1], "a", 1, 0.5)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign criterion err = %v, want ErrNotFound", err)
	}
}

// A feature cannot be its own opponent: the game would have an undefined
// expected score.
func TestCriterionVoteRejectsSelfComparison(t *testing.T) {
	d, uid, pid, ids := criteriaFixture(t)
	c := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)
	err := d.CastCriterionVote(context.Background(), pid, c.ID, uid, ids[0], ids[0], "a", 1, 0.5)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("self-comparison err = %v, want ErrInvalid", err)
	}
}

// A re-vote is a double count, so it must be refused rather than silently
// applied twice.
func TestCriterionVoteIsOncePerPair(t *testing.T) {
	d, uid, pid, ids := criteriaFixture(t)
	c := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)
	ctx := context.Background()

	if err := d.CastCriterionVote(ctx, pid, c.ID, uid, ids[0], ids[1], "a", 1, 0.5); err != nil {
		t.Fatalf("first vote: %v", err)
	}
	if err := d.CastCriterionVote(ctx, pid, c.ID, uid, ids[0], ids[1], "b", 1, 0.5); !errors.Is(err, ErrInvalid) {
		t.Errorf("second vote err = %v, want ErrInvalid", err)
	}

	// A different pair is fine, and a different voter on the same pair is fine.
	if err := d.CastCriterionVote(ctx, pid, c.ID, uid, ids[0], ids[2], "a", 1, 0.5); err != nil {
		t.Errorf("different pair rejected: %v", err)
	}
	// A second user in the SAME database. setupWithUser calls setup(t) again,
	// which opens a fresh in-memory DB, so it cannot produce a second voter for
	// this one — that mistake looked like a constraint bug.
	u2, err := d.CreateUser(ctx, "second-voter", "Second")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := d.CastCriterionVote(ctx, pid, c.ID, u2.ID, ids[0], ids[1], "b", 1, 0.5); err != nil {
		t.Errorf("second voter on same pair rejected: %v", err)
	}
}

// 'both' is a draw and scores; 'neither' and 'skip' must move no rating but
// must still be recorded — abstention is data.
func TestNeutralOutcomesDoNotMoveRatings(t *testing.T) {
	d, uid, pid, ids := criteriaFixture(t)
	c := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)
	ctx := context.Background()

	// One verdict per pair per voter, so each neutral outcome needs its own pair.
	// The unique index is (criterion, voter, a, b) by design: a second verdict on
	// the same comparison is a double count, not a refinement.
	neutralPairs := [][2]int64{{ids[0], ids[1]}, {ids[1], ids[2]}, {ids[0], ids[2]}}
	for i, outcome := range []string{"neither", "skip", "both"} {
		a, b := neutralPairs[i][0], neutralPairs[i][1]
		if err := d.CastCriterionVote(ctx, pid, c.ID, uid, a, b, outcome, 1, 0.5); err != nil {
			t.Fatalf("%s vote: %v", outcome, err)
		}
		// 'neither' and 'skip' must leave no rating at all.
		if outcome != "both" {
			if _, err := d.GetCriterionRating(ctx, pid, c.ID, a); !errors.Is(err, ErrNotFound) {
				t.Errorf("%s created a rating (err=%v); it must move nothing", outcome, err)
			}
		}
	}

	// The votes themselves are on record.
	var n int
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM criterion_votes WHERE criterion_id=?`, c.ID).Scan(&n); err != nil {
		t.Fatalf("count votes: %v", err)
	}
	if n != 3 {
		t.Errorf("recorded %d votes, want 3", n)
	}

	// 'both' is a draw and does create ratings, at equal values. It was cast on
	// the third pair, so read that pair's features.
	drawA, drawB := neutralPairs[2][0], neutralPairs[2][1]
	ra, err := d.GetCriterionRating(ctx, pid, c.ID, drawA)
	if err != nil {
		t.Fatalf("draw rating: %v", err)
	}
	rb, err := d.GetCriterionRating(ctx, pid, c.ID, drawB)
	if err != nil {
		t.Fatalf("draw rating: %v", err)
	}
	if diff := ra.R - rb.R; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("draw produced unequal ratings: %v vs %v", ra.R, rb.R)
	}
}

// A vote must move only the criterion it was cast on. Cross-criterion bleed is
// the failure that would make "best designed" and "best lightweight" the same
// question wearing two names.
func TestVoteAffectsOnlyItsCriterion(t *testing.T) {
	d, uid, pid, ids := criteriaFixture(t)
	design := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)
	speed := mkCriterion(t, d, uid, pid, "speed", ranking.DirHigher)
	ctx := context.Background()

	if err := d.CastCriterionVote(ctx, pid, design.ID, uid, ids[0], ids[1], "a", 1, 0.5); err != nil {
		t.Fatalf("vote: %v", err)
	}

	if _, err := d.GetCriterionRating(ctx, pid, design.ID, ids[0]); err != nil {
		t.Errorf("design rating missing: %v", err)
	}
	if _, err := d.GetCriterionRating(ctx, pid, speed.ID, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Errorf("speed rating created by a design vote (err=%v)", err)
	}
}

// An unrated feature is absent, not average. Imputing the mean would rank it as
// though a panel had judged it mediocre.
func TestUnratedFeatureIsAbsentNotAverage(t *testing.T) {
	d, uid, pid, ids := criteriaFixture(t)
	c := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)
	ctx := context.Background()

	if err := d.CastCriterionVote(ctx, pid, c.ID, uid, ids[0], ids[1], "a", 1, 0.5); err != nil {
		t.Fatalf("vote: %v", err)
	}
	if _, err := d.GetCriterionRating(ctx, pid, c.ID, ids[2]); !errors.Is(err, ErrNotFound) {
		t.Errorf("unvoted feature has a rating (err=%v); absent must mean unrated", err)
	}
}

// The headline behaviour: one feature can lead on design and trail on speed.
func TestRankingDiffersByCriterion(t *testing.T) {
	d, uid, pid, ids := criteriaFixture(t)
	ctx := context.Background()
	design := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)
	speed := mkCriterion(t, d, uid, pid, "speed", ranking.DirHigher)

	// Feature 0 beats 1 on design; feature 2 beats 0 on speed.
	if err := d.CastCriterionVote(ctx, pid, design.ID, uid, ids[0], ids[1], "a", 1, 0.5); err != nil {
		t.Fatal(err)
	}
	if err := d.CastCriterionVote(ctx, pid, speed.ID, uid, ids[2], ids[0], "a", 1, 0.5); err != nil {
		t.Fatal(err)
	}

	byDesign, err := d.ListCriterionRatings(ctx, pid, design.ID)
	if err != nil {
		t.Fatalf("design ratings: %v", err)
	}
	bySpeed, err := d.ListCriterionRatings(ctx, pid, speed.ID)
	if err != nil {
		t.Fatalf("speed ratings: %v", err)
	}
	if len(byDesign) != 2 || len(bySpeed) != 2 {
		t.Fatalf("want 2 ratings each, got design=%d speed=%d", len(byDesign), len(bySpeed))
	}

	designTop := ranking.RankByCriterion(design, byDesign)
	speedTop := ranking.RankByCriterion(speed, bySpeed)
	if designTop[0].FeatureID != ids[0] {
		t.Errorf("design leader = %d, want %d", designTop[0].FeatureID, ids[0])
	}
	if speedTop[0].FeatureID != ids[2] {
		t.Errorf("speed leader = %d, want %d", speedTop[0].FeatureID, ids[2])
	}
	// The two orders must genuinely differ, or the criteria are not independent.
	if designTop[0].FeatureID == speedTop[0].FeatureID {
		t.Skip("one game is not enough to separate the orders")
	}
}

// Composite must combine criteria and expose the breakdown that produced it.
func TestCompositeRankExplainsItself(t *testing.T) {
	d, uid, pid, ids := criteriaFixture(t)
	ctx := context.Background()
	design := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)
	speed := mkCriterion(t, d, uid, pid, "speed", ranking.DirHigher)

	for i := 0; i < 3; i++ {
		if err := d.CastCriterionVote(ctx, pid, design.ID, uid, ids[i], ids[(i+1)%3], "a", 1, 0.5); err != nil {
			t.Fatalf("design vote %d: %v", i, err)
		}
		if err := d.CastCriterionVote(ctx, pid, speed.ID, uid, ids[i], ids[(i+1)%3], "b", 1, 0.5); err != nil {
			t.Fatalf("speed vote %d: %v", i, err)
		}
	}

	res, err := d.CompositeRank(ctx, pid, nil, ranking.CompositeOptions{})
	if err != nil {
		t.Fatalf("CompositeRank: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("want 3 ranked, got %d", len(res))
	}
	for i, r := range res {
		if r.Rank != i+1 {
			t.Errorf("position %d has Rank=%d", i, r.Rank)
		}
		if len(r.Contributions) == 0 {
			t.Errorf("feature %d has no contributions; a result must explain itself", r.FeatureID)
		}
		// Contributions must sum to the score, or the explanation is decorative.
		sum := 0.0
		for _, c := range r.Contributions {
			sum += c.Score
		}
		if diff := sum - r.Score; diff > 1e-6 || diff < -1e-6 {
			t.Errorf("feature %d: contributions %.6f != score %.6f", r.FeatureID, sum, r.Score)
		}
		if got := ranking.TotalConfidence(r.Contributions); got <= 0 {
			t.Errorf("feature %d confidence = %v", r.FeatureID, got)
		}
	}
}

// Weights must actually change the order, or the weighting interface is
// decorative.
func TestCompositeWeightsChangeOrder(t *testing.T) {
	d, uid, pid, ids := criteriaFixture(t)
	ctx := context.Background()
	design := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)
	speed := mkCriterion(t, d, uid, pid, "speed", ranking.DirHigher)

	// 0 beats 1 on design; 1 beats 0 on speed. A weighting must be able to pick.
	if err := d.CastCriterionVote(ctx, pid, design.ID, uid, ids[0], ids[1], "a", 1, 0.5); err != nil {
		t.Fatal(err)
	}
	if err := d.CastCriterionVote(ctx, pid, speed.ID, uid, ids[1], ids[0], "a", 1, 0.5); err != nil {
		t.Fatal(err)
	}

	designHeavy, err := d.CompositeRank(ctx, pid,
		[]ranking.Weight{{CriterionID: design.ID, Weight: 9}, {CriterionID: speed.ID, Weight: 1}},
		ranking.CompositeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	speedHeavy, err := d.CompositeRank(ctx, pid,
		[]ranking.Weight{{CriterionID: design.ID, Weight: 1}, {CriterionID: speed.ID, Weight: 9}},
		ranking.CompositeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(designHeavy) < 2 || len(speedHeavy) < 2 {
		t.Fatalf("want 2 ranked each, got %d/%d", len(designHeavy), len(speedHeavy))
	}
	if designHeavy[0].FeatureID == speedHeavy[0].FeatureID {
		t.Errorf("weights did not change the leader (both %d)", designHeavy[0].FeatureID)
	}
}

// Deactivating must remove a criterion from ranking without deleting votes.
func TestSetCriterionActiveRemovesFromRanking(t *testing.T) {
	d, uid, pid, ids := criteriaFixture(t)
	ctx := context.Background()
	design := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)
	speed := mkCriterion(t, d, uid, pid, "speed", ranking.DirHigher)

	if err := d.CastCriterionVote(ctx, pid, design.ID, uid, ids[0], ids[1], "a", 1, 0.5); err != nil {
		t.Fatal(err)
	}
	if err := d.CastCriterionVote(ctx, pid, speed.ID, uid, ids[2], ids[0], "a", 1, 0.5); err != nil {
		t.Fatal(err)
	}

	before, err := d.CompositeRank(ctx, pid, nil, ranking.CompositeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetCriterionActive(ctx, pid, speed.ID, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	after, err := d.CompositeRank(ctx, pid, nil, ranking.CompositeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range after {
		for _, c := range r.Contributions {
			if c.CriterionID == speed.ID {
				t.Errorf("deactivated criterion %d still contributed", speed.ID)
			}
		}
	}
	if len(before) == len(after) {
		t.Log("note: deactivating did not change the result count; contributions were still checked")
	}

	// Votes survive: retiring a dimension is not erasing the history.
	var n int
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM criterion_votes WHERE criterion_id=?`, speed.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("deactivating lost %d votes", 1-n)
	}

	// And it can be restored.
	if err := d.SetCriterionActive(ctx, pid, speed.ID, true); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	c, err := d.GetCriterion(ctx, pid, speed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Active {
		t.Error("criterion did not come back")
	}
}

func TestSetCriterionActiveUnknownIsNotFound(t *testing.T) {
	d, _, pid, _ := criteriaFixture(t)
	if err := d.SetCriterionActive(context.Background(), pid, 9999, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A project with no criteria has no composite ranking — not an error, and
// certainly not an empty list presented as a ranking.
func TestCompositeRankWithNoCriteria(t *testing.T) {
	d, _, pid, _ := criteriaFixture(t)
	res, err := d.CompositeRank(context.Background(), pid, nil, ranking.CompositeOptions{})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(res) != 0 {
		t.Errorf("got %d results with no criteria", len(res))
	}
}

// Profiles save a weighting by name and return exactly what was saved.
func TestCriteriaProfileRoundTrip(t *testing.T) {
	d, uid, pid, _ := criteriaFixture(t)
	ctx := context.Background()
	design := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)
	speed := mkCriterion(t, d, uid, pid, "speed", ranking.DirHigher)

	if _, err := d.CreateCriteriaProfile(ctx, pid, uid, "edge", "Edge deployment",
		[]ranking.Weight{{CriterionID: design.ID, Weight: 1}, {CriterionID: speed.ID, Weight: 3}}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	got, err := d.GetCriteriaProfileWeights(ctx, pid, "edge")
	if err != nil {
		t.Fatalf("get weights: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d weights, want 2", len(got))
	}
	byID := map[int64]float64{}
	for _, w := range got {
		byID[w.CriterionID] = w.Weight
	}
	if byID[design.ID] != 1 || byID[speed.ID] != 3 {
		t.Errorf("weights = %v, want design=1 speed=3", byID)
	}

	// Re-saving replaces rather than merges, so a weight the caller dropped does
	// not linger and dilute the profile.
	if _, err := d.CreateCriteriaProfile(ctx, pid, uid, "edge", "Edge deployment",
		[]ranking.Weight{{CriterionID: design.ID, Weight: 2}}); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	got, err = d.GetCriteriaProfileWeights(ctx, pid, "edge")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("re-save left %d weights, want 1 (weights must be replaced)", len(got))
	}
}

func TestCriteriaProfileRejectsForeignCriterion(t *testing.T) {
	d, uid, pid, _ := criteriaFixture(t)
	pid2 := secondProject(t, d, uid)
	foreign := mkCriterion(t, d, uid, pid2, "elsewhere", ranking.DirHigher)

	_, err := d.CreateCriteriaProfile(context.Background(), pid, uid, "bad", "Bad",
		[]ranking.Weight{{CriterionID: foreign.ID, Weight: 1}})
	if !errors.Is(err, ErrCriterionScope) {
		t.Errorf("err = %v, want ErrCriterionScope", err)
	}
}

func TestCriteriaProfileRejectsNegativeWeight(t *testing.T) {
	d, uid, pid, _ := criteriaFixture(t)
	c := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)
	_, err := d.CreateCriteriaProfile(context.Background(), pid, uid, "neg", "Neg",
		[]ranking.Weight{{CriterionID: c.ID, Weight: -1}})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

func TestGetCriteriaProfileWeightsUnknown(t *testing.T) {
	d, _, pid, _ := criteriaFixture(t)
	_, err := d.GetCriteriaProfileWeights(context.Background(), pid, "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// RankByCriterion must not inflate a rating when called twice.
func TestRankByCriterionIsIdempotent(t *testing.T) {
	d, uid, pid, ids := criteriaFixture(t)
	ctx := context.Background()
	c := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)

	for i := 0; i < 3; i++ {
		if err := d.CastCriterionVote(ctx, pid, c.ID, uid, ids[i], ids[(i+1)%3], "a", 1, 0.5); err != nil {
			t.Fatal(err)
		}
	}
	first, err := d.RankByCriterion(ctx, pid, c.ID, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := map[int64]float64{}
	for _, r := range first {
		snapshot[r.FeatureID] = r.R
	}

	second, err := d.RankByCriterion(ctx, pid, c.ID, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range second {
		if diff := r.R - snapshot[r.FeatureID]; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("feature %d moved %.9f on a second run", r.FeatureID, diff)
		}
	}
}

func TestRankByCriterionUnknownIsNotFound(t *testing.T) {
	d, _, pid, _ := criteriaFixture(t)
	if _, err := d.RankByCriterion(context.Background(), pid, 4242, 0.5); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A bad outcome must be rejected before any row is written, so a refused vote
// leaves no partial state behind.
func TestCriterionVoteRejectsBadOutcomeWithoutWriting(t *testing.T) {
	d, uid, pid, ids := criteriaFixture(t)
	c := mkCriterion(t, d, uid, pid, "design", ranking.DirHigher)
	ctx := context.Background()

	if err := d.CastCriterionVote(ctx, pid, c.ID, uid, ids[0], ids[1], "maybe", 1, 0.5); !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
	var n int
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM criterion_votes WHERE criterion_id=?`, c.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("rejected vote left %d rows behind", n)
	}
}
