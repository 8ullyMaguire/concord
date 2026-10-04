package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

// Arenas: the single ranking engine (spec revision 4 §5).
//
// The property that matters most is not "does a vote move a rating" -- it does,
// and that was already true of features before this. It is that the SAME engine
// now scores entities that are not features, because that is what §5 is for and
// what makes the solution, alternatives and use-case arenas possible. An engine
// that only works for one entity type would be §5 renamed rather than
// implemented, and TestTheSameEngineScoresANonFeatureEntity is the test that
// would catch it.

// arenaFixture gives a store, an owner, a project with one feature, and that
// feature's id.
func arenaFixture(t *testing.T) (*DB, int64, int64, int64) {
	t.Helper()
	d, uid, pid := setupWithProject(t)
	return d, uid, pid, newFeature(t, d, pid, uid, "First feature")
}

// strangerCounter makes createStranger's usernames unique.
//
// The existing secondVoter helper hardcodes one username, so calling it twice in
// a test fails on the unique index and aborts the whole test at the second call.
// Two arena tests needed two voters, and the failure surfaced as a nonsense
// "pair repeated" message rather than "could not create a user".
var strangerCounter int

// createStranger returns a user who authored nothing in this test, so a test can
// vote on entries without tripping §5.2's self-vote rule.
func createStranger(t *testing.T, d *DB) int64 {
	t.Helper()
	strangerCounter++
	return newUserNamed(t, d, fmt.Sprintf("stranger%d", strangerCounter))
}

// newFeature makes an extra feature, for arena populations.
//
// §6.2 requires a feature to trace back to a validated complaint, so the fixture
// creates and validates one and links it. That is not ceremony: a feature that
// cannot be created without pain behind it is the spec's central claim, and a
// helper that skipped it would make the arena tests depend on a state the
// product forbids.
func newFeature(t *testing.T, d *DB, pid, uid int64, title string) int64 {
	t.Helper()
	f, err := d.CreateFeature(context.Background(), pid, uid, title, "body", "M",
		nil, nil, []int64{newValidatedComplaint(t, d, pid, uid, title+" complaint")})
	if err != nil {
		t.Fatalf("CreateFeature(%q): %v", title, err)
	}
	return f.ID
}

// newValidatedComplaint files a complaint and validates it.
func newValidatedComplaint(t *testing.T, d *DB, pid, uid int64, title string) int64 {
	t.Helper()
	ctx := context.Background()
	c, err := d.CreateComplaint(ctx, pid, uid, title, "body", 3, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint(%q): %v", title, err)
	}
	if err := d.ValidateComplaint(ctx, c.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	return c.ID
}

// addBaselineEntry adds the permanent do-nothing competitor (§6.3).
//
// The solutions table does not exist yet at this milestone, so the test builds a
// baseline entry directly against the arena under test. That is a fair stand-in:
// what is tested is how the engine treats a flagged baseline entry, not how the
// baseline comes into being.
func addBaselineEntry(t *testing.T, d *DB, arenaID int64) ArenaEntry {
	t.Helper()
	ctx := context.Background()
	// Entity id 0 is the do-nothing baseline; §6.5 names it "the baseline wins"
	// on rejection, so it is not a feature and never will be one.
	const baselineID = 0
	if err := d.UpsertArenaEntry(ctx, arenaID, EntityFeature, baselineID); err != nil {
		t.Fatalf("UpsertArenaEntry(baseline): %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE arena_entries SET is_baseline = 1
		 WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		arenaID, EntityFeature, baselineID); err != nil {
		t.Fatalf("flag baseline: %v", err)
	}
	e, err := d.GetArenaEntry(ctx, arenaID, EntityFeature, baselineID)
	if err != nil {
		t.Fatalf("GetArenaEntry(baseline): %v", err)
	}
	if !e.IsBaseline {
		t.Fatal("the entry is not flagged as the baseline, so the test proves nothing")
	}
	return e
}

func TestEnsureArenaIsIdempotent(t *testing.T) {
	// Idempotence matters more than it looks: §6.5 opens a consensus call off
	// the ranking, so two requests reaching that path must not each create "the"
	// feature arena and leave the second orphaned.
	d, _, pid, _ := arenaFixture(t)
	ctx := context.Background()

	first, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("first EnsureArena: %v", err)
	}
	second, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("second EnsureArena: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("two calls produced arenas %d and %d: one owner got two arenas", first.ID, second.ID)
	}
	if want := DefaultArenaQuestion(ArenaFeaturePriority); first.Question != want {
		t.Errorf("question = %q, want the §5 default %q", first.Question, want)
	}
}

func TestEnsureArenaDistinguishesUseCaseArenas(t *testing.T) {
	// §5: "the same project can rank differently for different use-cases". Two use
	// cases are therefore two arenas, not one arena with a changing question.
	d, _, pid, _ := arenaFixture(t)
	ctx := context.Background()

	a, err := d.EnsureArena(ctx, ArenaUseCase, pid, 0, "self-hosted, small team", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	b, err := d.EnsureArena(ctx, ArenaUseCase, pid, 0, "enterprise, on-prem", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if a.ID == b.ID {
		t.Fatal("two different use cases shared one arena: their rankings would be mixed")
	}
	again, err := d.EnsureArena(ctx, ArenaUseCase, pid, 0, "self-hosted, small team", "")
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if again.ID != a.ID {
		t.Errorf("repeat use case created a third arena (%d)", again.ID)
	}
}

func TestAVoteMovesBothRatingsInOppositeDirections(t *testing.T) {
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	other := newFeature(t, d, pid, uid, "Another feature")
	for _, id := range []int64{fid, other} {
		if err := d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, id); err != nil {
			t.Fatalf("UpsertArenaEntry: %v", err)
		}
	}

	stranger := createStranger(t, d)
	// Both sides are read BEFORE the vote. Reading the loser afterwards compares
	// the post-vote value against itself, which is how this assertion passed
	// vacuously for a while.
	before, err := d.GetArenaEntry(ctx, arena.ID, EntityFeature, fid)
	if err != nil {
		t.Fatalf("GetArenaEntry: %v", err)
	}
	loserBefore, err := d.GetArenaEntry(ctx, arena.ID, EntityFeature, other)
	if err != nil {
		t.Fatalf("GetArenaEntry: %v", err)
	}

	if _, err := d.CastArenaVote(ctx, arena.ID, stranger, EntityFeature, fid,
		EntityFeature, other, "a", "clearly better", 1.0); err != nil {
		t.Fatalf("CastArenaVote: %v", err)
	}
	after, err := d.GetArenaEntry(ctx, arena.ID, EntityFeature, fid)
	if err != nil {
		t.Fatalf("GetArenaEntry: %v", err)
	}
	if after.R <= before.R {
		t.Errorf("the winner's rating went %.1f -> %.1f: a win must raise it", before.R, after.R)
	}

	// And the loser must move the other way. Asymmetric movement is the bug that
	// looks like reasonable behaviour.
	loserAfter, err := d.GetArenaEntry(ctx, arena.ID, EntityFeature, other)
	if err != nil {
		t.Fatalf("GetArenaEntry: %v", err)
	}
	if loserAfter.R >= loserBefore.R {
		t.Errorf("the loser's rating went %.1f -> %.1f: a loss must lower it",
			loserBefore.R, loserAfter.R)
	}
}

func TestWeightScalesTheUpdate(t *testing.T) {
	// §5.1's weighted votes, and M3's AC "a weight-2 voter moves a rating twice
	// as far as weight-1".
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()

	setup := func(useCase string) (int64, int64) {
		t.Helper()
		a, err := d.EnsureArena(ctx, ArenaUseCase, pid, 0, useCase, "")
		if err != nil {
			t.Fatalf("EnsureArena: %v", err)
		}
		other := newFeature(t, d, pid, uid, "opponent "+useCase)
		for _, id := range []int64{fid, other} {
			if err := d.UpsertArenaEntry(ctx, a.ID, EntityFeature, id); err != nil {
				t.Fatalf("UpsertArenaEntry: %v", err)
			}
		}
		return a.ID, other
	}
	// Two arenas, or the rating is read twice from one shared state. EnsureArena
	// is idempotent per (type, project), so these deliberately differ by use case.
	lightArena, lightOpp := setup("light")
	heavyArena, heavyOpp := setup("heavy")

	// Two separate voters, because §5.2 stops the author voting on their own
	// entry -- which is every entry here.
	if _, err := d.CastArenaVote(ctx, lightArena, createStranger(t, d), EntityFeature, fid,
		EntityFeature, lightOpp, "a", "", 1.0); err != nil {
		t.Fatalf("light vote: %v", err)
	}
	if _, err := d.CastArenaVote(ctx, heavyArena, createStranger(t, d), EntityFeature, fid,
		EntityFeature, heavyOpp, "a", "", 2.0); err != nil {
		t.Fatalf("heavy vote: %v", err)
	}

	light, _ := d.GetArenaEntry(ctx, lightArena, EntityFeature, fid)
	heavy, _ := d.GetArenaEntry(ctx, heavyArena, EntityFeature, fid)
	if heavy.R <= light.R {
		t.Errorf("weight 2 moved the rating to %.1f and weight 1 to %.1f: the "+
			"heavier vote must move it further", heavy.R, light.R)
	}
}

func TestTheSameEngineScoresANonFeatureEntity(t *testing.T) {
	// The load-bearing test for §5.
	d, uid, pid, _ := arenaFixture(t)
	ctx := context.Background()

	arena, err := d.EnsureArena(ctx, ArenaAlternatives, pid, 0,
		"self-hosted kanban", "Which is the better alternative to X for Y?")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	// Competitors here are PROJECTS -- the §5 alternatives arena.
	rival, err := d.CreateProject(ctx, uid, "rival-kanban", "Rival", "another kanban", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	for _, id := range []int64{pid, rival.ID} {
		if err := d.UpsertArenaEntry(ctx, arena.ID, EntityProject, id); err != nil {
			t.Fatalf("UpsertArenaEntry(project %d): %v", id, err)
		}
	}

	before, err := d.GetArenaEntry(ctx, arena.ID, EntityProject, pid)
	if err != nil {
		t.Fatalf("GetArenaEntry: %v", err)
	}
	stranger := createStranger(t, d)
	if _, err := d.CastArenaVote(ctx, arena.ID, stranger, EntityProject, pid,
		EntityProject, rival.ID, "a", "better fit", 1.0); err != nil {
		t.Fatalf("CastArenaVote on a project arena: %v", err)
	}
	after, err := d.GetArenaEntry(ctx, arena.ID, EntityProject, pid)
	if err != nil {
		t.Fatalf("GetArenaEntry: %v", err)
	}
	if after.R <= before.R {
		t.Errorf("project rating went %.1f -> %.1f: the engine did not score a "+
			"non-feature entity", before.R, after.R)
	}
}

func TestVotingOnYourOwnEntryIsRefused(t *testing.T) {
	// §5.2: "Authors and affiliated accounts are disclosed and cannot vote on
	// their own entries."
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	other := newFeature(t, d, pid, uid, "opponent")
	for _, id := range []int64{fid, other} {
		_ = d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, id)
	}

	// uid authored `other`, so voting for it must fail.
	if _, err := d.CastArenaVote(ctx, arena.ID, uid, EntityFeature, fid,
		EntityFeature, other, "a", "", 1.0); err != ErrSelfVote {
		t.Errorf("author voting on their own entry: err = %v, want ErrSelfVote", err)
	}
}

func TestSkipIsRecordedAndChangesNothing(t *testing.T) {
	// §5.1: "Skip (with optional reason: 'I don't understand this' flags an
	// unclear write-up)". Recorded, scored as nothing.
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	other := newFeature(t, d, pid, uid, "opponent")
	for _, id := range []int64{fid, other} {
		_ = d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, id)
	}
	before, _ := d.GetArenaEntry(ctx, arena.ID, EntityFeature, fid)

	vote, err := d.CastArenaVote(ctx, arena.ID, createStranger(t, d), EntityFeature, fid,
		EntityFeature, other, "skip", "I don't understand the write-up", 1.0)
	if err != nil {
		t.Fatalf("CastArenaVote(skip): %v", err)
	}
	if vote.ID == 0 {
		t.Error("the skip was not recorded: §5.1 needs it in the log to flag an unclear write-up")
	}
	after, _ := d.GetArenaEntry(ctx, arena.ID, EntityFeature, fid)
	if after.R != before.R || after.RD != before.RD {
		t.Errorf("skip changed the rating: %.1f/%.1f -> %.1f/%.1f",
			before.R, before.RD, after.R, after.RD)
	}
}

func TestNeitherInAnArenaWithABaselineLosesToTheBaseline(t *testing.T) {
	// §5.1: "In arenas with a baseline, Neither counts as both competitors losing
	// to the baseline."
	d, _, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaSolution, pid, fid, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	baseline := addBaselineEntry(t, d, arena.ID)
	_ = d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, fid)

	before, _ := d.GetArenaEntry(ctx, arena.ID, EntityFeature, baseline.EntityID)
	stranger := createStranger(t, d)
	if _, err := d.CastArenaVote(ctx, arena.ID, stranger, EntityFeature, baseline.EntityID,
		EntityFeature, fid, "neither", "", 1.0); err != nil {
		t.Fatalf("CastArenaVote(neither): %v", err)
	}
	after, _ := d.GetArenaEntry(ctx, arena.ID, EntityFeature, baseline.EntityID)
	if after.R <= before.R {
		t.Errorf("the baseline rose %.1f -> %.1f when a competitor lost to it: "+
			"'neither' must mean both lost to the baseline", before.R, after.R)
	}
}

func TestTheBaselineCannotBeRemoved(t *testing.T) {
	// §6.3: "Every solution arena has a permanent baseline." An arena whose
	// baseline can be deleted makes "beats doing nothing" meaningless.
	d, _, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaSolution, pid, fid, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	baseline := addBaselineEntry(t, d, arena.ID)

	if err := d.RemoveArenaEntry(ctx, arena.ID, baseline.EntityType, baseline.EntityID); err == nil {
		t.Error("RemoveArenaEntry removed the baseline: §6.3 makes it permanent")
	}
}

func TestUpsertArenaEntryKeepsAnExistingRating(t *testing.T) {
	// Refresh, not reset. Re-registering an entity must not wipe a rating earned
	// over many votes -- a data-loss bug that surfaces much later.
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	other := newFeature(t, d, pid, uid, "opponent")
	for _, id := range []int64{fid, other} {
		_ = d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, id)
	}
	stranger := createStranger(t, d)
	for i := 0; i < 3; i++ {
		if _, err := d.CastArenaVote(ctx, arena.ID, stranger, EntityFeature, fid,
			EntityFeature, other, "a", "", 1.0); err != nil {
			t.Fatalf("vote %d: %v", i, err)
		}
	}
	before, _ := d.GetArenaEntry(ctx, arena.ID, EntityFeature, fid)
	if before.R == ranking.DefaultRating {
		t.Fatalf("the entry never moved off the prior, so the test proves nothing")
	}

	if err := d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, fid); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	after, _ := d.GetArenaEntry(ctx, arena.ID, EntityFeature, fid)
	if after.R != before.R {
		t.Errorf("re-upserting reset the rating %.1f -> %.1f", before.R, after.R)
	}
}

func TestNextPairNeverRepeatsAJudgedPair(t *testing.T) {
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}

	all := []int64{fid}
	for i := 0; i < 3; i++ {
		all = append(all, newFeature(t, d, pid, uid, "opponent"))
	}
	for _, id := range all {
		if err := d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, id); err != nil {
			t.Fatalf("UpsertArenaEntry: %v", err)
		}
	}

	// 4 entries = 6 pairs. The voter must get all 6, then an error rather than a
	// seventh repeat.
	stranger := createStranger(t, d)
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		a, b, err := d.NextArenaPair(ctx, arena.ID, stranger)
		if err != nil {
			t.Fatalf("pair %d: %v", i, err)
		}
		key := pairKey(a, b)
		if seen[key] {
			var logged int
			_ = d.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM pairwise_votes WHERE arena_id=? AND voter_id=?`,
				arena.ID, stranger).Scan(&logged)
			t.Fatalf("pair %d repeated %v; the log holds %d votes for voter %d in arena %d",
				i, key, logged, stranger, arena.ID)
		}
		seen[key] = true
		if _, err := d.CastArenaVote(ctx, arena.ID, stranger, a.EntityType, a.EntityID,
			b.EntityType, b.EntityID, "a", "", 1.0); err != nil {
			t.Fatalf("voting pair %d: %v", i, err)
		}
	}
	if _, _, err := d.NextArenaPair(ctx, arena.ID, stranger); err == nil {
		t.Error("a seventh pair was offered after all 6 were judged: re-asking a " +
			"settled question is how a voting queue becomes a chore")
	}
}

func TestNextPairCanReaskASkippedPair(t *testing.T) {
	// A skip is not a judgement, so the pair stays open. Otherwise "I don't
	// understand this" permanently removes a comparison from the arena.
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	other := newFeature(t, d, pid, uid, "opponent")
	for _, id := range []int64{fid, other} {
		_ = d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, id)
	}
	stranger := createStranger(t, d)
	a, b, err := d.NextArenaPair(ctx, arena.ID, stranger)
	if err != nil {
		t.Fatalf("NextArenaPair: %v", err)
	}
	if _, err := d.CastArenaVote(ctx, arena.ID, stranger, a.EntityType, a.EntityID,
		b.EntityType, b.EntityID, "skip", "unclear", 1.0); err != nil {
		t.Fatalf("CastArenaVote(skip): %v", err)
	}
	if _, _, err := d.NextArenaPair(ctx, arena.ID, stranger); err != nil {
		t.Errorf("the only pair became unaskable after a skip: %v", err)
	}
}

func TestNextPairPrefersUnratedEntries(t *testing.T) {
	// §5.1's active learning: an unrated entry is exactly the one whose position
	// the arena does not know.
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}

	settled := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		id := newFeature(t, d, pid, uid, "settled")
		settled = append(settled, id)
		if err := d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, id); err != nil {
			t.Fatalf("UpsertArenaEntry: %v", err)
		}
	}
	if err := d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, fid); err != nil {
		t.Fatalf("UpsertArenaEntry(fid): %v", err)
	}
	// Settle them away from the prior.
	stranger := createStranger(t, d)
	for i := 0; i < len(settled); i++ {
		for j := i + 1; j < len(settled); j++ {
			if _, err := d.CastArenaVote(ctx, arena.ID, stranger, EntityFeature, settled[i],
				EntityFeature, settled[j], "a", "", 1.0); err != nil {
				t.Fatalf("settling: %v", err)
			}
		}
	}
	// Now add the unknown: it is the informative pair.
	unknown := newFeature(t, d, pid, uid, "brand new")
	if err := d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, unknown); err != nil {
		t.Fatalf("UpsertArenaEntry(unknown): %v", err)
	}

	a, b, err := d.NextArenaPair(ctx, arena.ID, stranger)
	if err != nil {
		t.Fatalf("NextArenaPair: %v", err)
	}
	// The pair must involve the unknown entry. Which side it lands on is not
	// meaningful -- the ranking is about the comparison, not the order.
	if a.EntityID != unknown && b.EntityID != unknown {
		t.Errorf("first pair was %d (RD %.0f) vs %d (RD %.0f), which does not include "+
			"the unrated entry %d: active learning should ask about the unknown one",
			a.EntityID, a.RD, b.EntityID, b.RD, unknown)
	}
	if a.RD < b.RD {
		t.Errorf("the pair is %d (RD %.0f) vs %d (RD %.0f): the more uncertain entry "+
			"should be first", a.EntityID, a.RD, b.EntityID, b.RD)
	}
}

func TestConservativeScoreKeepsAnUnratedEntryBelowAnAttestedOne(t *testing.T) {
	// §5.1 displays `r - 2*RD`. Ranking on r alone lets a brand-new entry look
	// like a winner until its first loss arrives.
	attested := ranking.Conservative(1600, 20)
	unrated := ranking.Conservative(1560, 350)
	if unrated >= attested {
		t.Errorf("ranking.Conservative put the unrated entry (%.1f) at or above the "+
			"attested one (%.1f): r alone would let an untested entry win",
			unrated, attested)
	}
}

func TestLeaderboardCarriesWinProbabilityAgainstTheLeader(t *testing.T) {
	// §5.1: "A is better with 87% confidence".
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	other := newFeature(t, d, pid, uid, "opponent")
	for _, id := range []int64{fid, other} {
		_ = d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, id)
	}
	board, err := d.ArenaLeaderboard(ctx, arena.ID, 10)
	if err != nil {
		t.Fatalf("ArenaLeaderboard: %v", err)
	}
	if len(board) != 2 {
		t.Fatalf("leaderboard has %d rows, want 2", len(board))
	}
	if board[0].WinProbabilityAgainstLeader != 0 {
		t.Error("the leader carries a non-zero probability against itself")
	}
	p := board[1].WinProbabilityAgainstLeader
	if p <= 0 || p > 1 {
		t.Errorf("second entry's probability = %.3f, want a fraction in (0,1]", p)
	}
	if board[0].Conservative < board[1].Conservative {
		t.Error("the leaderboard is not sorted by the conservative score §5.1 displays")
	}
}

func TestRecomputeFromTheVoteLogReproducesTheOrder(t *testing.T) {
	// §2.5 and §5.1: "ratings are recomputed deterministically from the
	// append-only vote log. Anyone can verify a ranking." If the incremental path
	// and the recompute disagree, the vote log is not the source of truth and the
	// transparency claim is false.
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}

	ids := []int64{fid, newFeature(t, d, pid, uid, "second"), newFeature(t, d, pid, uid, "third")}
	for _, id := range ids {
		if err := d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, id); err != nil {
			t.Fatalf("UpsertArenaEntry: %v", err)
		}
	}

	stranger := createStranger(t, d)
	// A decisive history: 0 beats 1 beats 2, repeatedly.
	for i := 0; i < 4; i++ {
		if _, err := d.CastArenaVote(ctx, arena.ID, stranger, EntityFeature, ids[0],
			EntityFeature, ids[1], "a", "", 1.0); err != nil {
			t.Fatalf("vote: %v", err)
		}
		if _, err := d.CastArenaVote(ctx, arena.ID, stranger, EntityFeature, ids[1],
			EntityFeature, ids[2], "a", "", 1.0); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	incremental, err := d.ArenaLeaderboard(ctx, arena.ID, 10)
	if err != nil {
		t.Fatalf("ArenaLeaderboard: %v", err)
	}
	recomputed, err := d.RecomputeArenaRatings(ctx, arena.ID)
	if err != nil {
		t.Fatalf("RecomputeArenaRatings: %v", err)
	}
	if len(incremental) != len(recomputed) {
		t.Fatalf("board sizes differ: %d incremental, %d recomputed", len(incremental), len(recomputed))
	}
	for i := range incremental {
		if incremental[i].EntityID != recomputed[i].EntityID {
			t.Errorf("position %d: incremental has entity %d, recompute has %d -- the "+
				"order is not reproducible from the vote log, so §2.5 is false",
				i, incremental[i].EntityID, recomputed[i].EntityID)
		}
	}
}

func TestVoteLogIsReadableAndCarriesTheComparison(t *testing.T) {
	// §2.5's verification requires the log to be readable, so there is no
	// permission gate on this read.
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	other := newFeature(t, d, pid, uid, "opponent")
	for _, id := range []int64{fid, other} {
		_ = d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, id)
	}
	stranger := createStranger(t, d)
	if _, err := d.CastArenaVote(ctx, arena.ID, stranger, EntityFeature, fid,
		EntityFeature, other, "a", "my reason", 1.0); err != nil {
		t.Fatalf("CastArenaVote: %v", err)
	}
	// A skip must not appear in the ranked history: it is not a judgement.
	if _, err := d.CastArenaVote(ctx, arena.ID, stranger, EntityFeature, other,
		EntityFeature, fid, "skip", "unclear", 1.0); err != nil {
		t.Fatalf("CastArenaVote(skip): %v", err)
	}

	votes, err := d.ListArenaVotes(ctx, arena.ID, 50)
	if err != nil {
		t.Fatalf("ListArenaVotes: %v", err)
	}
	if len(votes) != 1 {
		t.Fatalf("vote log has %d entries, want 1 (the skip is not a judgement)", len(votes))
	}
	if votes[0].Reason != "my reason" {
		t.Errorf("reason = %q, want it preserved: §5.1 lets a vote carry a reason", votes[0].Reason)
	}
	if votes[0].A.EntityType != EntityFeature || votes[0].B.EntityType != EntityFeature {
		t.Errorf("log entries carry entity types %q/%q; a verifier cannot tell what "+
			"was compared without them", votes[0].A.EntityType, votes[0].B.EntityType)
	}
	if votes[0].A.EntityID == 0 || votes[0].B.EntityID == 0 {
		t.Error("the log does not name both competitors, so the comparison is unverifiable")
	}
}

func TestComparingDifferentEntityTypesIsRefused(t *testing.T) {
	// A solution against a feature in one arena is meaningless, and it would
	// silently produce a plausible number.
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	_ = d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, fid)

	if _, err := d.CastArenaVote(ctx, arena.ID, uid, EntityFeature, fid,
		EntityProject, pid, "a", "", 1.0); err == nil {
		t.Error("a feature was compared against a project in the same arena")
	}
}

func TestComparingAnEntityWithItselfIsRefused(t *testing.T) {
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	_ = d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, fid)

	if _, err := d.CastArenaVote(ctx, arena.ID, uid, EntityFeature, fid,
		EntityFeature, fid, "a", "", 1.0); err != ErrSameEntityVote {
		t.Errorf("self-comparison: err = %v, want ErrSameEntityVote", err)
	}
}

func TestUnknownOutcomeIsRefused(t *testing.T) {
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	other := newFeature(t, d, pid, uid, "opponent")
	for _, id := range []int64{fid, other} {
		_ = d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, id)
	}
	if _, err := d.CastArenaVote(ctx, arena.ID, uid, EntityFeature, fid,
		EntityFeature, other, "maybe", "", 1.0); err == nil {
		t.Error("an unknown outcome was accepted; the CHECK constraint is the last line of defence")
	}
}

func TestVotingOnAnEntryThatIsNotInTheArenaIsRefused(t *testing.T) {
	// Otherwise the rating update writes to a row that does not exist and the log
	// records a comparison the arena can never score.
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	other := newFeature(t, d, pid, uid, "opponent")
	_ = d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, fid)

	if _, err := d.CastArenaVote(ctx, arena.ID, uid, EntityFeature, fid,
		EntityFeature, other, "a", "", 1.0); err == nil {
		t.Error("a vote was accepted for an entry never added to the arena")
	}
}

func TestFeatureVoteStillMirrorsOntoTheFeatureRow(t *testing.T) {
	// The project priority endpoint reads features.elo_r, not arena_entries, so
	// the two have to stay in step or priority and the leaderboard disagree.
	d, uid, pid, fid := arenaFixture(t)
	ctx := context.Background()
	arena, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	other := newFeature(t, d, pid, uid, "opponent")
	for _, id := range []int64{fid, other} {
		_ = d.UpsertArenaEntry(ctx, arena.ID, EntityFeature, id)
	}
	before, err := d.GetFeature(ctx, fid)
	if err != nil {
		t.Fatalf("GetFeature: %v", err)
	}
	if _, err := d.CastArenaVote(ctx, arena.ID, createStranger(t, d), EntityFeature, fid,
		EntityFeature, other, "a", "", 1.0); err != nil {
		t.Fatalf("CastArenaVote: %v", err)
	}
	after, err := d.GetFeature(ctx, fid)
	if err != nil {
		t.Fatalf("GetFeature: %v", err)
	}
	if after.EloR == before.EloR {
		t.Error("features.elo_r did not move: the priority endpoint would contradict the arena leaderboard")
	}
}

// --- Alternatives arenas (spec §5, §7.3) ---------------------------------
//
// AddArenaCompetitor is the one arena method that was reachable from the HTTP
// layer and called by NO test in the repository. Its six rules were documented in
// comments and proved by nothing, so the S2 mutation gate reported 0/6 killed
// against 22 armed tests -- which was the gate telling the truth about the code,
// not about itself. These tests are what it was asking for.
//
// The rules under test, each stated by a comment on the method:
//
//  1. only alternatives and use-case arenas take competing projects;
//  2. the candidate id must be positive;
//  3. the arena's own project cannot compete against itself;
//  4. the candidate project must exist;
//  5. the entry is stored as a PROJECT pair, not a feature pair;
//  6. adding is idempotent and does not overwrite an existing rating.

// alternativesFixture gives an alternatives arena over a project, plus a second
// project that can compete against it.
func alternativesFixture(t *testing.T) (*DB, int64, int64, int64, int64) {
	t.Helper()
	d, uid, pid := setupWithProject(t)
	arena, err := d.EnsureArena(context.Background(), ArenaAlternatives, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena(alternatives): %v", err)
	}
	return d, uid, pid, arena.ID, newCompetingProject(t, d, uid, "other-tool")
}

// newCompetingProject makes a second project owned by `uid`.
//
// `slug` really is a slug: CreateProject validates it as lowercase kebab-case, and
// passing a human title here fails the test for a reason that has nothing to do
// with the arena.
//
// A project, not a feature: an alternatives arena ranks PROJECTS (§5), and a
// fixture that produced features would let a mutant that stores a feature pair
// pass -- the entry would resolve, just against the wrong table.
func newCompetingProject(t *testing.T, d *DB, uid int64, slug string) int64 {
	t.Helper()
	created, err := d.CreateProject(context.Background(), uid, slug, "A different tool",
		"body", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject(%q): %v", slug, err)
	}
	return created.ID
}

func TestAnAlternativesArenaAcceptsAnotherProject(t *testing.T) {
	d, _, _, arenaID, rivalID := alternativesFixture(t)
	if err := d.AddArenaCompetitor(context.Background(), arenaID, rivalID); err != nil {
		t.Fatalf("AddArenaCompetitor: %v", err)
	}
	e, err := d.GetArenaEntry(context.Background(), arenaID, EntityProject, rivalID)
	if err != nil {
		t.Fatalf("GetArenaEntry: %v", err)
	}
	if e.EntityID != rivalID {
		t.Fatalf("entry is for entity %d, want %d", e.EntityID, rivalID)
	}
}

// The load-bearing one, and the plan's: the pair must be stored as a project.
// A feature pair resolves against the feature table, so the leaderboard shows a
// rating for a row that is not the competing project at all -- and every other
// assertion in this file still passes.
func TestAnAlternativesArenaRanksProjectsNotFeatures(t *testing.T) {
	d, _, _, arenaID, rivalID := alternativesFixture(t)
	if err := d.AddArenaCompetitor(context.Background(), arenaID, rivalID); err != nil {
		t.Fatalf("AddArenaCompetitor: %v", err)
	}
	e, err := d.GetArenaEntry(context.Background(), arenaID, EntityProject, rivalID)
	if err != nil {
		t.Fatalf("GetArenaEntry as a project: %v", err)
	}
	if e.EntityType != EntityProject {
		t.Fatalf("entry is a %q, want %q", e.EntityType, EntityProject)
	}
	// The same id must NOT resolve as a feature. Entity id spaces are separate,
	// so this is the check that distinguishes the two rather than a restatement.
	if _, err := d.GetArenaEntry(context.Background(), arenaID, EntityFeature, rivalID); err == nil {
		t.Fatalf("entry %d also resolves as a %s", rivalID, EntityFeature)
	}
}

// The arena's own project is the subject, not a competitor to itself: §7.3's
// honest "do nothing" is keep using X.
func TestAProjectCannotCompeteAgainstItself(t *testing.T) {
	d, _, pid, arenaID, _ := alternativesFixture(t)
	err := d.AddArenaCompetitor(context.Background(), arenaID, pid)
	if err == nil {
		t.Fatal("the arena's own project was accepted as its own competitor")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}

// A feature-priority arena ranks features. Posting a project to it is a category
// error, and the message says what that arena does rank instead -- a reader who
// got this wrong is not asking for a technical error.
func TestAnArenaOfTheWrongTypeRefusesACompetingProject(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	arena, err := d.EnsureArena(context.Background(), ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	arenaID := arena.ID
	rivalID := newCompetingProject(t, d, uid, "some-other-tool")
	err = d.AddArenaCompetitor(context.Background(), arenaID, rivalID)
	if err == nil {
		t.Fatal("a project was added to a feature-priority arena")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "features") {
		t.Errorf("the message does not say what the arena ranks: %v", err)
	}
}

// Without the existence check a typo'd id becomes an entry pointing at nothing,
// and the leaderboard skips it -- so the arena looks like it ranked fewer projects
// than were added, with nothing in the response to say why.
func TestACandidateProjectThatDoesNotExistIsRefused(t *testing.T) {
	d, _, _, arenaID, _ := alternativesFixture(t)
	missing := int64(987654)
	if _, err := d.GetProjectByID(context.Background(), missing); err == nil {
		t.Skip("that project id exists; the fixture is wrong, not the code")
	}
	err := d.AddArenaCompetitor(context.Background(), arenaID, missing)
	if err == nil {
		t.Fatalf("a nonexistent candidate project %d was accepted", missing)
	}
}

func TestACandidateIDOfZeroIsRefused(t *testing.T) {
	d, _, _, arenaID, _ := alternativesFixture(t)
	for _, id := range []int64{0, -1} {
		if err := d.AddArenaCompetitor(context.Background(), arenaID, id); err == nil {
			t.Fatalf("candidate id %d was accepted", id)
		}
	}
}

// Re-registering a competitor must not reset a 400-vote rating to the prior.
// UpsertArenaEntry refresh semantics are the reason, and this is the test that
// says so at the level a user meets it.
func TestAddingTheSameCompetitorTwiceKeepsItsRating(t *testing.T) {
	d, _, _, arenaID, rivalID := alternativesFixture(t)
	ctx := context.Background()
	if err := d.AddArenaCompetitor(ctx, arenaID, rivalID); err != nil {
		t.Fatalf("first add: %v", err)
	}
	// Stand in for a rated history.
	if _, err := d.ExecContext(ctx,
		`UPDATE arena_entries SET r = 1723, games = 412
		 WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		arenaID, EntityProject, rivalID); err != nil {
		t.Fatalf("seed a rating: %v", err)
	}
	if err := d.AddArenaCompetitor(ctx, arenaID, rivalID); err != nil {
		t.Fatalf("second add: %v", err)
	}
	e, err := d.GetArenaEntry(ctx, arenaID, EntityProject, rivalID)
	if err != nil {
		t.Fatalf("GetArenaEntry: %v", err)
	}
	if e.R != 1723 || e.Games != 412 {
		t.Fatalf("re-adding reset the rating to %v/%d games, want 1723/412", e.R, e.Games)
	}
}

// Adding twice must leave ONE entry, not two -- the arena would otherwise show the
// same competitor twice in a leaderboard.
func TestAddingTheSameCompetitorTwiceLeavesOneEntry(t *testing.T) {
	d, _, _, arenaID, rivalID := alternativesFixture(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := d.AddArenaCompetitor(ctx, arenaID, rivalID); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	board, err := d.ArenaLeaderboard(ctx, arenaID, 50)
	if err != nil {
		t.Fatalf("ArenaLeaderboard: %v", err)
	}
	n := 0
	for _, e := range board {
		if e.EntityID == rivalID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the competitor appears %d times in the leaderboard, want 1", n)
	}
}

// §5.2's sibling rule: a voter cannot judge an entry they authored. It is
// enforced at the vote end by refuseOwnVote, and this covers the store level --
// the HTTP layer has its own test.
func TestAProjectOwnerCannotVoteOnTheirOwnProjectInTheArena(t *testing.T) {
	d, uid, _, arenaID, _ := alternativesFixture(t)
	ctx := context.Background()
	// Two DIFFERENT projects, one of them owned by `uid`. The first version of this
	// test voted a project against ITSELF, which the "a comparison needs two
	// different entries" check refuses first -- so it passed while
	// `refuseOwnVote` was never reached, and the mutation that deleted that call
	// survived. A test that cannot fail for the right reason is worse than none:
	// it reports the rule as covered.
	mine := newCompetingProject(t, d, uid, "a-third-tool")
	theirs := newCompetingProject(t, d, createStranger(t, d), "a-fourth-tool")
	alsoTheirs := newCompetingProject(t, d, createStranger(t, d), "a-fifth-tool")
	for _, id := range []int64{mine, theirs, alsoTheirs} {
		if err := d.AddArenaCompetitor(ctx, arenaID, id); err != nil {
			t.Fatalf("AddArenaCompetitor(%d): %v", id, err)
		}
	}

	// `uid` owns `mine`, so judging it is a self-vote.
	_, err := d.CastArenaVote(ctx, arenaID, uid,
		EntityProject, mine, EntityProject, theirs, "a", "", 1.0)
	if !errors.Is(err, ErrSelfVote) {
		t.Fatalf("got %v, want ErrSelfVote", err)
	}

	// And the refusal is authorship, not a blanket ban on owners taking part in
	// their own arena: `uid` owns neither side here, so this must be allowed.
	//
	// Outcomes are lowercase ("a", "b", "both", "neither", "skip"). The uppercase
	// "A" was refused by the outcome switch, which runs before refuseOwnVote --
	// the same ordering trap as the self-comparison above: the test reports the
	// rule as covered while the rule was never reached.
	//
	// Both sides matter -- `refuseOwnVote` runs on each of them, so a vote between
	// one of `uid`'s projects and somebody else's is refused whichever order it is
	// cast in. Only a pair `uid` is unrelated to can be judged.
	if _, err := d.CastArenaVote(ctx, arenaID, uid,
		EntityProject, theirs, EntityProject, alsoTheirs, "a", "", 1.0); err != nil {
		t.Fatalf("refused a vote by an owner on two projects they did not author: %v", err)
	}
}

// The baseline is ONE representation. S2.0 was ordered to remove the ambiguity
// between arena_entries.is_baseline and the unwritten arenas.baseline_entry_id
// before an alternatives arena shipped; if the leaderboard starts reading a
// literal, the flag stops reaching it.
func TestTheLeaderboardCarriesTheBaselineFlag(t *testing.T) {
	d, _, _, arenaID, rivalID := alternativesFixture(t)
	ctx := context.Background()
	if err := d.AddArenaCompetitor(ctx, arenaID, rivalID); err != nil {
		t.Fatalf("AddArenaCompetitor: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE arena_entries SET is_baseline = 1
		 WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		arenaID, EntityProject, rivalID); err != nil {
		t.Fatalf("flag the baseline: %v", err)
	}
	board, err := d.ArenaLeaderboard(ctx, arenaID, 50)
	if err != nil {
		t.Fatalf("ArenaLeaderboard: %v", err)
	}
	found := false
	for _, e := range board {
		if e.EntityID == rivalID {
			found = true
			if !e.IsBaseline {
				t.Fatal("the leaderboard row lost the baseline flag")
			}
		}
	}
	if !found {
		t.Fatal("the competitor is missing from the leaderboard entirely")
	}
}
