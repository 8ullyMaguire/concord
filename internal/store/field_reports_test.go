package store

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

// Field report AC tests (docs/plans/finder.md step 3, spec §4.3).
//
// The two that would be easy to write vacuously:
//
//   - TestTheProjectOwnerCannotRemoveAReport: a check written `role == ""` never
//     fires, because GetRoleForProject returns "guest" for a non-member. This
//     exact mistake left every member-only action open on this codebase once.
//   - TestOutcomeRateIsWeightedByReporterReputation: a test with one report at
//     100% cannot tell weighting from no weighting, so the fixture uses a
//     minority heavy-reputation report against a majority of light ones.

func reportFixture(t *testing.T) (*DB, int64, int64) {
	t.Helper()
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	r, err := store.CreateFieldReport(ctx, FieldReport{
		ProjectSlug: "test-project",
		UserID:      uid,
		Outcome:     ReportWorked,
		Environment: "arm64",
		UseCase:     "small team kanban",
	})
	if err != nil {
		t.Fatalf("CreateFieldReport: %v", err)
	}
	if r.ID == 0 {
		t.Fatal("CreateFieldReport returned no id")
	}
	return store, uid, pid
}

func TestMigratedAwayWithoutASuccessorIsRefused(t *testing.T) {
	store, uid, _ := reportFixture(t)
	ctx := context.Background()

	_, err := store.CreateFieldReport(ctx, FieldReport{
		ProjectSlug: "test-project", UserID: uid, Outcome: ReportMigrated,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("migrated-away with no successor = %v, want ErrInvalid", err)
	}
	// The message must name the field, so the reporter knows what to fill in.
	if err == nil || !strings.Contains(err.Error(), "migrated_to") {
		t.Errorf("error %q does not name the field that is missing", err)
	}
}

func TestAnOutcomeThatIsNotOneOfTheFourIsRefused(t *testing.T) {
	store, uid, _ := reportFixture(t)
	ctx := context.Background()

	// "it works fine mostly" is exactly the rant §4.7 rules out.
	_, err := store.CreateFieldReport(ctx, FieldReport{
		ProjectSlug: "test-project", UserID: uid, Outcome: "works fine mostly",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("free-text outcome = %v, want ErrInvalid", err)
	}
}

func TestAnOwnerResponseIsMarkedAsOwners(t *testing.T) {
	store, uid, _ := reportFixture(t)
	ctx := context.Background()

	reports, err := store.ListFieldReports(ctx, mustProjectID(t, store, "test-project"), false)
	if err != nil || len(reports) == 0 {
		t.Fatalf("ListFieldReports: %v (%d reports)", err, len(reports))
	}

	// uid created the project, so it is a member.
	resp, err := store.RespondToFieldReport(ctx, reports[0].ID, uid, "thanks for the report")
	if err != nil {
		t.Fatalf("RespondToFieldReport: %v", err)
	}
	if !resp.IsOwner {
		t.Error("a project member's response was not marked as the owner's")
	}

	// A stranger's is not, which is what makes the badge worth anything.
	other, err := store.RespondToFieldReport(ctx, reports[0].ID,
		newUserNamed(t, store, "stranger"), "I had the same problem")
	if err != nil {
		t.Fatalf("RespondToFieldReport: %v", err)
	}
	if other.IsOwner {
		t.Error("a non-member's response was marked as the owner's")
	}
}

func TestTheProjectOwnerCannotRemoveAReport(t *testing.T) {
	store, uid, _ := reportFixture(t)
	ctx := context.Background()
	id := mustProjectID(t, store, "test-project")

	// The author created the project; §4.7 gives owners respond rights and
	// explicitly withholds delete.
	err := store.RemoveFieldReport(ctx, firstReportID(t, store, id), uid, "I disagree with this")
	if !errors.Is(err, ErrPerm) {
		t.Fatalf("owner removing a report = %v, want ErrPerm", err)
	}
}

func TestARemovedReportIsHiddenNotDeleted(t *testing.T) {
	store, _, _ := reportFixture(t)
	ctx := context.Background()
	pid := mustProjectID(t, store, "test-project")
	id := firstReportID(t, store, pid)
	moderator := newUserNamed(t, store, "moderator")

	if err := store.RemoveFieldReport(ctx, id, moderator, "fabricated: no such version exists"); err != nil {
		t.Fatalf("RemoveFieldReport: %v", err)
	}

	// Hidden from readers...
	visible, err := store.ListFieldReports(ctx, pid, false)
	if err != nil {
		t.Fatalf("ListFieldReports: %v", err)
	}
	if len(visible) != 0 {
		t.Errorf("removed report still visible to readers: %d shown", len(visible))
	}

	// ...but still present for the appeal, which is conducted on the report.
	all, err := store.ListFieldReports(ctx, pid, true)
	if err != nil {
		t.Fatalf("ListFieldReports(includeRemoved): %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d reports with removed included, want 1", len(all))
	}
	got, err := store.GetFieldReport(ctx, all[0].ID)
	if err != nil {
		t.Fatalf("GetFieldReport: %v", err)
	}
	if !got.Removed {
		t.Error("report is not flagged as removed")
	}
	if got.RemovalReason != "fabricated: no such version exists" {
		t.Errorf("removal reason = %q, want it recorded — the appeal is conducted on it", got.RemovalReason)
	}
}

func TestRemovingAReportRequiresAReason(t *testing.T) {
	store, _, _ := reportFixture(t)
	ctx := context.Background()
	pid := mustProjectID(t, store, "test-project")

	err := store.RemoveFieldReport(ctx, firstReportID(t, store, pid),
		newUserNamed(t, store, "moderator2"), "   ")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("removal with no reason = %v, want ErrInvalid", err)
	}
}

func TestOutcomeRateIsWeightedByReporterReputation(t *testing.T) {
	store, uid, pid := reportFixture(t)
	ctx := context.Background()

	// The fixture's one 'worked' report is from the project owner. Give them
	// enormous reputation, then add three 'abandoned' reports from accounts
	// with none. Unweighted, the rate is 1.0 — the three are outvoted.
	if err := store.AddReputation(ctx, pid, uid, "field-report", 1000); err != nil {
		t.Fatalf("AddReputation: %v", err)
	}
	for _, name := range []string{"r1", "r2", "r3"} {
		other := newUserNamed(t, store, name)
		if _, err := store.CreateFieldReport(ctx, FieldReport{
			ProjectSlug: "test-project", UserID: other, Outcome: ReportAbandoned,
		}); err != nil {
			t.Fatalf("CreateFieldReport(%s): %v", name, err)
		}
	}

	rate, n, err := store.FieldReportOutcomeRate(ctx, pid)
	if err != nil {
		t.Fatalf("FieldReportOutcomeRate: %v", err)
	}
	if n != 4 {
		t.Errorf("sample size = %d, want 4", n)
	}
	// The heavy report is capped at weight 2; each light one weighs 1. So
	// (2·1 + 1·0 + 1·0 + 1·0) / 5 = 0.4, and certainly not 1.0.
	if rate >= 0.6 {
		t.Errorf("outcome rate = %.2f, want the three unreputed abandonments to outweigh one capped report (<0.6)", rate)
	}
	if rate <= 0 {
		t.Errorf("outcome rate = %.2f, want a positive rate — the 'worked' report counts", rate)
	}
}

func TestOutcomeRateReturnsItsSampleSize(t *testing.T) {
	store, _, pid := reportFixture(t)
	ctx := context.Background()

	rate, n, err := store.FieldReportOutcomeRate(ctx, pid)
	if err != nil {
		t.Fatalf("FieldReportOutcomeRate: %v", err)
	}
	if rate != 1.0 {
		t.Errorf("rate = %.2f, want 1.0 for a single clean report", rate)
	}
	if n != 1 {
		t.Errorf("sample size = %d, want 1 — a 100%% rate from one report must be distinguishable from 11", n)
	}
}

// Asserts the RULE, not the value of CaveatCredit.
//
// The first version of this test computed its expectation as
// (1.0 + CaveatCredit) / 2 -- the same constant the code reads -- so it passed
// for 0.5, for 0.25 and for 1.0-CaveatCredit. Verified: mutating the constant
// left the suite green. A test that recomputes the answer from the constant
// cannot fail, so the bounds are stated here instead: caveated success must sit
// strictly between abandonment and clean success, which is the property §4.7's
// "worked for 83% of reporters" depends on being able to express.
func TestCaveatedSuccessCountsBetweenCleanSuccessAndAbandonment(t *testing.T) {
	store, _, pid := reportFixture(t)
	ctx := context.Background()
	other := newUserNamed(t, store, "caveater")

	if _, err := store.CreateFieldReport(ctx, FieldReport{
		ProjectSlug: "test-project", UserID: other, Outcome: ReportCaveats,
	}); err != nil {
		t.Fatalf("CreateFieldReport: %v", err)
	}

	rate, _, err := store.FieldReportOutcomeRate(ctx, pid)
	if err != nil {
		t.Fatalf("FieldReportOutcomeRate: %v", err)
	}
	// One clean report, one caveated: the average is strictly inside (0, 1).
	if rate <= 0 {
		t.Errorf("rate = %.3f, want strictly above abandonment", rate)
	}
	if rate >= 1 {
		t.Errorf("rate = %.3f, want strictly below clean success — caveats are not a clean success", rate)
	}

	// And symmetric: swapping which report came first cannot change the rate,
	// so the credit is a property of the outcome rather than of report order.
	if again, _, err := store.FieldReportOutcomeRate(ctx, pid); err != nil {
		t.Fatalf("FieldReportOutcomeRate: %v", err)
	} else if again != rate {
		t.Errorf("rate changed from %.3f to %.3f between identical calls", rate, again)
	}
}

// The boundary the test above cannot reach.
//
// That test averages a clean report with a caveated one, so credit=0.5 gives
// 0.75 and credit=0.0 gives 0.50 — both strictly inside (0,1), so the assertion
// passed for both. Confirmed by mutation: `return CaveatCredit` -> `return 0.0`
// survived. This fixture has ONLY caveated reports, so the rate IS the credit
// and the two are no longer confounded.
func TestACaveatOnlyProjectReportsAHalfRateNotZeroOrOne(t *testing.T) {
	store, _, _ := reportFixture(t)
	ctx := context.Background()
	other := newUserNamed(t, store, "onlycaveats")

	if _, err := store.CreateFieldReport(ctx, FieldReport{
		ProjectSlug: "test-project", UserID: other, Outcome: ReportCaveats,
	}); err != nil {
		t.Fatalf("CreateFieldReport: %v", err)
	}
	// Remove the fixture's clean report so every remaining report is caveated.
	pid := mustProjectID(t, store, "test-project")
	clean, err := store.ListFieldReports(ctx, pid, false)
	if err != nil {
		t.Fatalf("ListFieldReports: %v", err)
	}
	if err := store.RemoveFieldReport(ctx, clean[len(clean)-1].ID,
		newUserNamed(t, store, "mod4"), "superseded"); err != nil {
		t.Fatalf("RemoveFieldReport: %v", err)
	}

	rate, n, err := store.FieldReportOutcomeRate(ctx, pid)
	if err != nil {
		t.Fatalf("FieldReportOutcomeRate: %v", err)
	}
	if n != 1 {
		t.Fatalf("sample size = %d, want 1", n)
	}
	if math.Abs(rate-0.5) > 0.001 {
		t.Errorf("caveat-only outcome rate = %.3f, want 0.5 — the credit IS the rate when "+
			"every report is caveated, and a project that always needs a workaround "+
			"has not 'not worked'", rate)
	}
}

// Pins CaveatCredit to a value the decision can be argued about, rather than
// leaving it to the next reader to infer from arithmetic.
//
// Separate from the test above on purpose: that one states the rule, this one
// records the number and its reason. Together they fail in opposite directions —
// this one catches a silent change, the other catches a wrong relationship.
func TestCaveatCreditIsHalfAndSaysWhy(t *testing.T) {
	if CaveatCredit != 0.5 {
		t.Errorf("CaveatCredit = %v, want 0.5: caveated success is real success, so it must "+
			"count for something, and it must not equal clean success or readers of "+
			"'worked for 83%% of reporters' cannot tell some of those 83%% needed a workaround",
			CaveatCredit)
	}
}

func TestOutcomeRateByEnvironmentAnswersTheArm64Question(t *testing.T) {
	store, _, pid := reportFixture(t)
	ctx := context.Background()

	// The fixture's report is arm64/worked. Add an x86 abandonment.
	other := newUserNamed(t, store, "x86user")
	if _, err := store.CreateFieldReport(ctx, FieldReport{
		ProjectSlug: "test-project", UserID: other,
		Outcome: ReportAbandoned, Environment: "x86",
	}); err != nil {
		t.Fatalf("CreateFieldReport: %v", err)
	}

	byEnv, err := store.FieldReportOutcomeRateByEnvironment(ctx, pid)
	if err != nil {
		t.Fatalf("FieldReportOutcomeRateByEnvironment: %v", err)
	}
	if byEnv["arm64"] != 1.0 {
		t.Errorf("arm64 rate = %.2f, want 1.0", byEnv["arm64"])
	}
	if byEnv["x86"] != 0.0 {
		t.Errorf("x86 rate = %.2f, want 0.0", byEnv["x86"])
	}
}

func TestARemovedReportDoesNotCountTowardTheOutcomeRate(t *testing.T) {
	store, _, pid := reportFixture(t)
	ctx := context.Background()
	other := newUserNamed(t, store, "remover-target")

	if _, err := store.CreateFieldReport(ctx, FieldReport{
		ProjectSlug: "test-project", UserID: other, Outcome: ReportAbandoned,
	}); err != nil {
		t.Fatalf("CreateFieldReport: %v", err)
	}

	reports, err := store.ListFieldReports(ctx, pid, false)
	if err != nil {
		t.Fatalf("ListFieldReports: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("got %d reports, want 2", len(reports))
	}
	// Remove the abandonment.
	if err := store.RemoveFieldReport(ctx, reports[0].ID, newUserNamed(t, store, "mod3"), "duplicate"); err != nil {
		t.Fatalf("RemoveFieldReport: %v", err)
	}

	rate, n, err := store.FieldReportOutcomeRate(ctx, pid)
	if err != nil {
		t.Fatalf("FieldReportOutcomeRate: %v", err)
	}
	if n != 1 {
		t.Errorf("sample size = %d, want 1 — a removed report must not count", n)
	}
	if rate != 1.0 {
		t.Errorf("rate = %.2f, want 1.0 — a removed report must not count", rate)
	}
}

// The environment index needs an environment. A report filed without one must
// not create a "" bucket, or "worked for 83% of reporters on ”" is a sentence
// the UI can render.
func TestAReportWithNoEnvironmentDoesNotCreateAnEmptyBucket(t *testing.T) {
	store, _, pid := reportFixture(t)
	ctx := context.Background()
	other := newUserNamed(t, store, "noenv")

	if _, err := store.CreateFieldReport(ctx, FieldReport{
		ProjectSlug: "test-project", UserID: other, Outcome: ReportWorked,
	}); err != nil {
		t.Fatalf("CreateFieldReport: %v", err)
	}

	byEnv, err := store.FieldReportOutcomeRateByEnvironment(ctx, pid)
	if err != nil {
		t.Fatalf("FieldReportOutcomeRateByEnvironment: %v", err)
	}
	if _, present := byEnv[""]; present {
		t.Error("a report with no environment produced an empty-string bucket")
	}
	if len(byEnv) != 1 {
		t.Errorf("got %d environment buckets (%v), want only the one real environment", len(byEnv), byEnv)
	}
}

func TestAReportNeedsAnAuthorAndAProject(t *testing.T) {
	store, uid, _ := reportFixture(t)
	ctx := context.Background()

	if _, err := store.CreateFieldReport(ctx, FieldReport{
		ProjectSlug: "test-project", Outcome: ReportWorked,
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("report with no author = %v, want ErrInvalid", err)
	}
	if _, err := store.CreateFieldReport(ctx, FieldReport{
		UserID: uid, Outcome: ReportWorked,
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("report with no project = %v, want ErrInvalid", err)
	}
	if _, err := store.CreateFieldReport(ctx, FieldReport{
		ProjectSlug: "no-such-project", UserID: uid, Outcome: ReportWorked,
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("report against an unknown project = %v, want ErrNotFound", err)
	}
}

func firstReportID(t *testing.T, store *DB, projectID int64) int64 {
	t.Helper()
	reports, err := store.ListFieldReports(context.Background(), projectID, false)
	if err != nil {
		t.Fatalf("ListFieldReports: %v", err)
	}
	if len(reports) == 0 {
		t.Fatal("no reports to act on")
	}
	return reports[0].ID
}

// An owner's response is a published statement and cannot be edited or deleted
// -- not even by its author. A bystander's reply can be both, because it is
// only their own account.
//
// §4.3 AC 2 requires the "cannot be edited or deleted by its author
// afterwards" half, and neither the method nor the test existed: RespondToField-
// Report was the only way to touch the row, so the rule was unwritten rather
// than merely untested.
//
// Why the badge makes it immutable: an owner response is quoted as the
// project's own account of a problem. If it could be rewritten after the fact,
// a project could publish a bland "known issue" reply and then quietly revise it
// into a promise. Retraction happens by posting another response.
func TestAnOwnersResponseCannotBeEditedOrDeletedEvenByItsAuthor(t *testing.T) {
	store, uid, _ := reportFixture(t)
	ctx := context.Background()
	pid := mustProjectID(t, store, "test-project")

	reports, err := store.ListFieldReports(ctx, pid, false)
	if err != nil || len(reports) == 0 {
		t.Fatalf("ListFieldReports: %v (%d)", err, len(reports))
	}
	// uid created the project, so this response carries the owner badge.
	owner, err := store.RespondToFieldReport(ctx, reports[0].ID, uid, "yes, this happens on arm64")
	if err != nil {
		t.Fatalf("RespondToFieldReport: %v", err)
	}
	if !owner.IsOwner {
		t.Fatal("fixture did not produce an owner response; the rest of this test is vacuous")
	}

	if _, err := store.EditFieldOwnerResponse(ctx, owner.ID, uid, "no longer true"); !errors.Is(err, ErrPerm) {
		t.Errorf("the author editing their own owner response = %v, want ErrPerm", err)
	}
	if err := store.DeleteFieldOwnerResponse(ctx, owner.ID, uid); !errors.Is(err, ErrPerm) {
		t.Errorf("the author deleting their own owner response = %v, want ErrPerm", err)
	}
	// Unchanged, and still there.
	after, err := store.getFieldReportResponse(ctx, owner.ID)
	if err != nil {
		t.Fatalf("the owner response is gone: %v", err)
	}
	if after.Body != owner.Body {
		t.Errorf("body = %q, want the original %q", after.Body, owner.Body)
	}

	// A bystander's reply is theirs to correct.
	stranger := newUserNamed(t, store, "bystander")
	plain, err := store.RespondToFieldReport(ctx, reports[0].ID, stranger, "I had this on my machine")
	if err != nil {
		t.Fatalf("RespondToFieldReport: %v", err)
	}
	if plain.IsOwner {
		t.Fatal("a non-member's response was marked as the owner's")
	}

	edited, err := store.EditFieldOwnerResponse(ctx, plain.ID, stranger, "I had this on arm64 only")
	if err != nil {
		t.Errorf("a bystander could not edit their own reply: %v", err)
	} else if edited.Body != "I had this on arm64 only" {
		t.Errorf("edited body = %q", edited.Body)
	}
	// And another account cannot edit or delete even a non-owner reply.
	thief := newUserNamed(t, store, "thief")
	if _, err := store.EditFieldOwnerResponse(ctx, plain.ID, thief, "mine now"); !errors.Is(err, ErrPerm) {
		t.Errorf("a third party editing someone else's reply = %v, want ErrPerm", err)
	}
	if err := store.DeleteFieldOwnerResponse(ctx, plain.ID, thief); !errors.Is(err, ErrPerm) {
		t.Errorf("a third party deleting someone else's reply = %v, want ErrPerm", err)
	}
	if err := store.DeleteFieldOwnerResponse(ctx, plain.ID, stranger); err != nil {
		t.Errorf("the author could not delete their own non-owner reply: %v", err)
	}
}

// Deleting a project deletes its field reports, and their responses with them.
//
// §4.3 AC 5. The CASCADE is in the schema, so this is the test that would catch
// it being dropped in a future migration -- the same shape as the capability
// cascade test.
func TestDeletingAProjectRowCascadesToItsFieldReports(t *testing.T) {
	store, uid, _ := reportFixture(t)
	ctx := context.Background()
	pid := mustProjectID(t, store, "test-project")

	reports, err := store.ListFieldReports(ctx, pid, false)
	if err != nil || len(reports) == 0 {
		t.Fatalf("ListFieldReports: %v (%d)", err, len(reports))
	}
	if _, err := store.RespondToFieldReport(ctx, reports[0].ID, uid, "acknowledged"); err != nil {
		t.Fatalf("RespondToFieldReport: %v", err)
	}
	rid := reports[0].ID

	if _, err := d0(store).ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, pid); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	var reportsLeft, responsesLeft int
	if err := d0(store).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM field_reports WHERE project_id = ?`, pid).Scan(&reportsLeft); err != nil {
		t.Fatalf("count field_reports: %v", err)
	}
	if reportsLeft != 0 {
		t.Errorf("%d field reports survived the project delete, want 0", reportsLeft)
	}
	// The responses CASCADE from the reports, which is the second hop.
	if err := d0(store).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM field_report_responses WHERE report_id = ?`, rid).Scan(&responsesLeft); err != nil {
		t.Fatalf("count responses: %v", err)
	}
	if responsesLeft != 0 {
		t.Errorf("%d owner responses survived the project delete, want 0", responsesLeft)
	}
}
