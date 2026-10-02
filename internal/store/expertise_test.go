package store

import (
	"context"
	"testing"
)

// §6.4/§8.3 expertise weighting, at the point where the per-tag numbers come
// from the database.
//
// The curve itself is in internal/ranking and tested there. What can only be
// tested here is the attribution: which actions earn tag-reputation, which do
// not, and whether the numbers mean the same thing as the project reputation
// they are calibrated against.

// expertiseFixture is a project with one feature, one complaint and three
// accounts: the feature's author, an unrelated voter, and the tag expert.
//
// Returned as a struct so each test can name the account it is asserting about --
// several of these tests are about what one person has *not* done.
type expertiseFixture struct {
	store    *DB
	project  int64
	feature  int64
	author   int64
	outsider int64
	expert   int64
}

func setupExpertise(t *testing.T) expertiseFixture {
	t.Helper()
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	c, err := store.CreateComplaint(ctx, pid, uid, "exporter drops rows", "body", 3, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	// A feature must link a validated complaint, so validate first.
	if _, err := store.ExecContext(ctx, `UPDATE complaints SET status = 'validated' WHERE id = ?`, c.ID); err != nil {
		t.Fatalf("validate: %v", err)
	}
	f, err := store.CreateFeature(ctx, pid, uid, "Lossless export", "body", "M", nil, nil, []int64{c.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	return expertiseFixture{
		store:    store,
		project:  pid,
		feature:  f.ID,
		author:   uid,
		outsider: newUserNamed(t, store, "expertiseoutsider"),
		expert:   newUserNamed(t, store, "expertiseexpert"),
	}
}

// tagFeature attaches a tag to the fixture's feature, which is how a merged
// merge request or a shipped feature acquires tags at all.
func (fx expertiseFixture) tagFeature(t *testing.T, name string) int64 {
	t.Helper()
	tag, err := fx.store.ensureTag(context.Background(), name)
	if err != nil {
		t.Fatalf("ensureTag(%s): %v", name, err)
	}
	if _, err := fx.store.ExecContext(context.Background(),
		`INSERT INTO feature_tags (feature_id, tag_id) VALUES (?, ?)`,
		fx.feature, tag); err != nil {
		t.Fatalf("tag feature: %v", err)
	}
	return tag
}

// shipSolution creates a solution and moves it to a status that counts as
// shipped work. The store has no SetSolutionStatus -- §6.5 walks a solution
// through its statuses as a side effect of consensus -- so the transition is made
// directly, which is the same thing a consensus outcome would have done.
func (fx expertiseFixture) shipSolution(t *testing.T, authorID int64, title, tags, status string) int64 {
	t.Helper()
	ctx := context.Background()
	sol, err := fx.store.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fx.feature, AuthorID: authorID, Title: title,
		Type: SolutionBuildNew, ExpertiseTags: tags,
	})
	if err != nil {
		t.Fatalf("CreateSolution(%s): %v", title, err)
	}
	if _, err := fx.store.ExecContext(ctx,
		`UPDATE solutions SET status = ? WHERE id = ?`, status, sol.ID); err != nil {
		t.Fatalf("set status %s: %v", status, err)
	}
	return sol.ID
}

// TestTagReputationIsEmptyForANonContributor is the base case, and the one that
// would be silently wrong if the derivation fell back to project reputation: a
// stranger must score zero in every tag, or §8.3's "expertise is tag-scoped"
// becomes "expertise is a longer resume".
func TestTagReputationIsEmptyForANonContributor(t *testing.T) {
	fx := setupExpertise(t)
	perTag, err := fx.store.TagReputation(context.Background(), fx.project, fx.outsider)
	if err != nil {
		t.Fatalf("TagReputation: %v", err)
	}
	if len(perTag) != 0 {
		t.Errorf("a user who has done nothing scores %v in tags; want empty", perTag)
	}
}

// TestTagReputationIsNotProjectReputation is the property §8.3 states outright.
// A generalist with a long history in the project must not outrank an expert in
// the tag being judged.
func TestTagReputationIsNotProjectReputation(t *testing.T) {
	fx := setupExpertise(t)
	ctx := context.Background()

	// The generalist banks a lot of project reputation in a tag the expert does
	// not have. If TagReputation fell back to GetReputation, they would win.
	if err := fx.store.AddReputation(ctx, fx.project, fx.outsider, "vote", 200); err != nil {
		t.Fatalf("AddReputation: %v", err)
	}
	// The expert's solution must be one that counts: a draft is a proposal, and
	// this test is about attribution, not about the status boundary.
	fx.shipSolution(t, fx.expert, "Rust rewrite", "rust", "shipped")

	perTag, err := fx.store.TagReputation(ctx, fx.project, fx.expert)
	if err != nil {
		t.Fatalf("TagReputation: %v", err)
	}
	// The expert's own project reputation is near zero, so the tag figure is a
	// small positive number and cannot have come from the project sum.
	projRep, err := fx.store.GetReputation(ctx, fx.project, fx.expert)
	if err != nil {
		t.Fatalf("GetReputation: %v", err)
	}
	if perTag["rust"] <= 0 {
		t.Fatalf("the expert scores %.2f in rust; want a positive figure", perTag["rust"])
	}
	if perTag["rust"] >= 200 {
		t.Errorf("rust tag reputation %.2f tracks the 200-point outsider's project total, so it is not tag-scoped",
			perTag["rust"])
	}
	if projRep >= 200 {
		t.Fatalf("fixture broken: the expert's project reputation is %.2f, so the comparison proves nothing", projRep)
	}
}

// TestTagReputationCreditsShippedSolutionsAndNotProposals is the status boundary.
// A draft is a proposal; a consensus is a decision. Counting drafts would let a
// member mint tag-reputation by filing and abandoning, which is the Sybil
// pressure §8.3's "time decay prevents permanent aristocracy" is defending
// against.
func TestTagReputationCreditsShippedSolutionsAndNotProposals(t *testing.T) {
	fx := setupExpertise(t)
	ctx := context.Background()

	if _, err := fx.store.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fx.feature, AuthorID: fx.expert, Title: "Just an idea",
		Type: SolutionBuildNew, ExpertiseTags: "draft-tag",
	}); err != nil {
		t.Fatalf("CreateSolution: %v", err)
	}
	perTag, err := fx.store.TagReputation(ctx, fx.project, fx.expert)
	if err != nil {
		t.Fatalf("TagReputation: %v", err)
	}
	if perTag["draft-tag"] != 0 {
		t.Errorf("a draft solution earns %.2f in its tag; want 0 until it reaches consensus", perTag["draft-tag"])
	}

	// A rejected solution is worth nothing either -- the argument was lost.
	if _, err := fx.store.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fx.feature, AuthorID: fx.expert, Title: "Lost the argument",
		Type: SolutionBuildNew, ExpertiseTags: "rejected-tag",
	}); err != nil {
		t.Fatalf("CreateSolution: %v", err)
	}
	if _, err := fx.store.ExecContext(ctx,
		`UPDATE solutions SET status = 'rejected' WHERE title = 'Lost the argument'`); err != nil {
		t.Fatalf("mark rejected: %v", err)
	}
	perTag, err = fx.store.TagReputation(ctx, fx.project, fx.expert)
	if err != nil {
		t.Fatalf("TagReputation: %v", err)
	}
	if perTag["rejected-tag"] != 0 {
		t.Errorf("a rejected solution earns %.2f in its tag; want 0", perTag["rejected-tag"])
	}
}

// TestTagReputationCreditsValidatedComplaints checks the complaint path and its
// status boundary together: §8.3 counts shipped work, and an unvalidated
// complaint is a guess.
func TestTagReputationCreditsValidatedComplaints(t *testing.T) {
	fx := setupExpertise(t)
	ctx := context.Background()
	tag, err := fx.store.ensureTag(ctx, "exporters")
	if err != nil {
		t.Fatalf("ensureTag: %v", err)
	}
	c, err := fx.store.CreateComplaint(ctx, fx.project, fx.outsider, "another one", "b", 2, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if _, err := fx.store.ExecContext(ctx,
		`INSERT INTO complaint_tags (complaint_id, tag_id) VALUES (?, ?)`, c.ID, tag); err != nil {
		t.Fatalf("tag complaint: %v", err)
	}

	perTag, err := fx.store.TagReputation(ctx, fx.project, fx.outsider)
	if err != nil {
		t.Fatalf("TagReputation: %v", err)
	}
	if perTag["exporters"] != 0 {
		t.Errorf("an open complaint earns %.2f; want 0 until validated", perTag["exporters"])
	}

	if _, err := fx.store.ExecContext(ctx, `UPDATE complaints SET status = 'validated' WHERE id = ?`, c.ID); err != nil {
		t.Fatalf("validate: %v", err)
	}
	perTag, err = fx.store.TagReputation(ctx, fx.project, fx.outsider)
	if err != nil {
		t.Fatalf("TagReputation: %v", err)
	}
	if perTag["exporters"] != tagRepValidatedComplaint {
		t.Errorf("validated complaint earns %.2f, want %.2f", perTag["exporters"], tagRepValidatedComplaint)
	}
}

// TestTagReputationCreditsOnlyMergedMergeRequests is the Sybil boundary on the
// code path. An opened PR is an opening; §8.3 counts merged work.
func TestTagReputationCreditsOnlyMergedMergeRequests(t *testing.T) {
	fx := setupExpertise(t)
	ctx := context.Background()
	fx.tagFeature(t, "storage")

	mr, err := fx.store.CreateMergeRequest(ctx, fx.project, fx.feature, fx.expert,
		"implement it")
	if err != nil {
		t.Fatalf("CreateMergeRequest: %v", err)
	}
	perTag, err := fx.store.TagReputation(ctx, fx.project, fx.expert)
	if err != nil {
		t.Fatalf("TagReputation: %v", err)
	}
	if perTag["storage"] != 0 {
		t.Errorf("an open merge request earns %.2f; want 0 until merged", perTag["storage"])
	}

	if _, err := fx.store.ExecContext(ctx,
		`UPDATE merge_requests SET status = 'merged' WHERE id = ?`, mr.ID); err != nil {
		t.Fatalf("merge: %v", err)
	}
	perTag, err = fx.store.TagReputation(ctx, fx.project, fx.expert)
	if err != nil {
		t.Fatalf("TagReputation: %v", err)
	}
	if perTag["storage"] != tagRepMergedPR {
		t.Errorf("merged PR earns %.2f, want %.2f", perTag["storage"], tagRepMergedPR)
	}
}

// TestTagReputationDoesNotDoubleCountRepeatedTags is arithmetic, not lookup: a
// solution that happens to write "rust,rust" would otherwise bank double credit
// and outrank an otherwise identical one.
func TestTagReputationDoesNotDoubleCountRepeatedTags(t *testing.T) {
	fx := setupExpertise(t)
	ctx := context.Background()
	if _, err := fx.store.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fx.feature, AuthorID: fx.expert, Title: "Repeated tag",
		Type: SolutionBuildNew, ExpertiseTags: " Rust , rust ,RUST,",
	}); err != nil {
		t.Fatalf("CreateSolution: %v", err)
	}
	if _, err := fx.store.ExecContext(ctx,
		`UPDATE solutions SET status = 'shipped' WHERE title = 'Repeated tag'`); err != nil {
		t.Fatalf("ship: %v", err)
	}
	perTag, err := fx.store.TagReputation(ctx, fx.project, fx.expert)
	if err != nil {
		t.Fatalf("TagReputation: %v", err)
	}
	if perTag["rust"] != tagRepShippedSolution {
		t.Errorf("rust credit %.2f, want exactly one solution's %.2f", perTag["rust"], tagRepShippedSolution)
	}
	if _, dup := perTag[""]; dup {
		t.Error("an empty tag appears in the result; the trailing comma was not dropped")
	}
}

// TestTagReputationIsCaseInsensitive is what keeps a lookup from failing on
// capitalisation: ranking.BestTagReputation looks up normalised names, so a
// capitalised "Rust" must land in the same bucket as a lowercase "rust".
func TestTagReputationIsCaseInsensitive(t *testing.T) {
	fx := setupExpertise(t)
	ctx := context.Background()
	if _, err := fx.store.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fx.feature, AuthorID: fx.expert, Title: "Mixed case",
		Type: SolutionBuildNew, ExpertiseTags: "RUST,Embedded",
	}); err != nil {
		t.Fatalf("CreateSolution: %v", err)
	}
	if _, err := fx.store.ExecContext(ctx,
		`UPDATE solutions SET status = 'shipped' WHERE title = 'Mixed case'`); err != nil {
		t.Fatalf("ship: %v", err)
	}
	perTag, err := fx.store.TagReputation(ctx, fx.project, fx.expert)
	if err != nil {
		t.Fatalf("TagReputation: %v", err)
	}
	if perTag["rust"] == 0 {
		t.Error("\"RUST\" did not land in the \"rust\" bucket; a mixed-case tag scores zero")
	}
	if perTag["embedded"] == 0 {
		t.Error("\"Embedded\" did not land in the \"embedded\" bucket")
	}
}
