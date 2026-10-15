package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// The objections table's status CHECK has permitted exactly five values since
// migration 0001: open, withdrawn, resolved, overridden, vetoed. The endpoint that
// writes it passed whatever the caller sent straight into the UPDATE, so the
// constraint -- not the API -- was the thing refusing a junk status, and a refusal
// from a constraint arrives as a 500 carrying a SQLite message. A client cannot act
// on that, and the page that now offers the control (consensus.js) would report a
// server fault rather than "that is not a status".
//
// Same shape as §16.3: a rule that existed in one place and was never enforced
// where the write happens.
type objectionFixture struct {
	ts     *httptest.Server
	db     *store.DB
	slug   string
	pid    int64
	token  string
	callID int64
}

func seedObjection(t *testing.T) objectionFixture {
	t.Helper()
	ts, db, slug, pid, _ := seedPanels(t)
	ctx := context.Background()

	name := "objection-maintainer"
	uid := userIDNamed(t, db, name)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)`,
		pid, uid, "maintainer", float64(1)); err != nil {
		t.Fatalf("add maintainer: %v", err)
	}
	call, err := db.CreateConsensusCall(ctx, pid, 0, 1, "Adopt X?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	return objectionFixture{ts: ts, db: db, slug: slug, pid: pid,
		token: tokenFor(t, db, name), callID: call.ID}
}

// anOpenObjection inserts one, because the objection is not what these tests are
// about: the row is a fixture, the endpoint under test is the resolution.
func (f objectionFixture) anOpenObjection(t *testing.T, principle string) int64 {
	t.Helper()
	res, err := f.db.ExecContext(context.Background(),
		`INSERT INTO objections (call_id, user_id, principle, violation, remedy, status, created_at)
		 VALUES (?, ?, ?, 'the chart is rewritten nightly', 'keep it', 'open', ?)`,
		f.callID, userIDNamed(t, f.db, "testuser"), principle, float64(1))
	if err != nil {
		t.Fatalf("insert objection: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	return id
}

func (f objectionFixture) resolve(t *testing.T, objID int64, body string) (*http.Response, map[string]any) {
	t.Helper()
	return putJSON(t, f.ts,
		"/api/v1/projects/"+f.slug+"/consensus/objections/"+itoa(objID)+"/resolve",
		body, "Authorization", "Bearer "+f.token)
}

func (f objectionFixture) statusOf(t *testing.T, objID int64) string {
	t.Helper()
	var status string
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT status FROM objections WHERE id = ?`, objID).Scan(&status); err != nil {
		t.Fatalf("read back objection: %v", err)
	}
	return status
}

func TestAResolutionStatusOutsideTheSchemaVocabularyIsRefused(t *testing.T) {
	f := seedObjection(t)
	objID := f.anOpenObjection(t, "a maintainer decides alone")

	resp, body := f.resolve(t, objID, `{"status":"banana","resolution":"because I said so"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("resolving an objection with status \"banana\" answered %d (%v); want "+
			"400 naming the statuses the schema permits, not a 500 straight from the "+
			"constraint", resp.StatusCode, body)
	}
	if got := f.statusOf(t, objID); got != "open" {
		t.Errorf("status = %q after a refused resolution; want it untouched at open -- "+
			"a 400 that still wrote is worse than a 500", got)
	}
}

// TestTheFourResolutionStatusesTheSchemaAllowsAreAccepted keeps the guard from
// becoming a rule that refuses everything: each non-open status the CHECK permits
// must be accepted.
func TestTheFourResolutionStatusesTheSchemaAllowsAreAccepted(t *testing.T) {
	f := seedObjection(t)
	for _, status := range []string{"resolved", "overridden", "vetoed", "withdrawn"} {
		objID := f.anOpenObjection(t, "blocked on "+status)
		resp, body := f.resolve(t, objID,
			fmt.Sprintf(`{"status":%q,"resolution":"recorded"}`, status))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status %q was refused with %d (%v); the schema permits it",
				status, resp.StatusCode, body)
			continue
		}
		if got := f.statusOf(t, objID); got != status {
			t.Errorf("status = %q; want %q", got, status)
		}
	}
}

// TestResolvingAnObjectionBackToOpenIsRefused: `open` is in the CHECK because it is
// the DEFAULT, not because it is an outcome. Setting a resolved objection back to
// open reopens a call's blocker with no record that anything changed.
func TestResolvingAnObjectionBackToOpenIsRefused(t *testing.T) {
	f := seedObjection(t)
	objID := f.anOpenObjection(t, "a maintainer decides alone")
	resp, body := f.resolve(t, objID, `{"status":"open","resolution":"never mind"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("resolving to \"open\" answered %d (%v); want 400 -- open is the "+
			"default, not an outcome", resp.StatusCode, body)
	}
}

// TestAResolutionNeedsAResolution: a status alone records a decision with no
// reasoning, which is the audit trail §6.5 asks for on the other three outcomes.
func TestAResolutionNeedsAResolution(t *testing.T) {
	f := seedObjection(t)
	objID := f.anOpenObjection(t, "a maintainer decides alone")
	resp, body := f.resolve(t, objID, `{"status":"overridden","resolution":"  "}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an overrule with a blank resolution answered %d (%v); want 400",
			resp.StatusCode, body)
	}
	if got := f.statusOf(t, objID); got != "open" {
		t.Errorf("status = %q after a refused resolution; want open", got)
	}
}

// TestTheResponseEchoesTheStatusThatWasWritten pins the second half of the same bug:
// the endpoint accepted any of the four statuses and answered "resolved" for all of
// them, so "vetoed" was recorded and reported as resolved.
func TestTheResponseEchoesTheStatusThatWasWritten(t *testing.T) {
	f := seedObjection(t)
	objID := f.anOpenObjection(t, "a maintainer decides alone")
	resp, body := f.resolve(t, objID,
		`{"status":"vetoed","resolution":"it contradicts the licence"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve: %d %v", resp.StatusCode, body)
	}
	if got, _ := body["status"].(string); got != "vetoed" {
		t.Errorf("the response reports %q after writing vetoed", got)
	}
	if got := f.statusOf(t, objID); got != "vetoed" {
		t.Errorf("stored status is %q; want vetoed", got)
	}
}

// TestSomebodyWhoIsNotAMaintainerCannotResolve keeps the standing rule pinned: the
// handler has always required maintainer, and the page now offers the control to any
// signed-in reader, reporting whatever the server says.
func TestSomebodyWhoIsNotAMaintainerCannotResolve(t *testing.T) {
	f := seedObjection(t)
	objID := f.anOpenObjection(t, "a maintainer decides alone")
	if _, err := f.db.ExecContext(context.Background(),
		`INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)`,
		f.pid, userIDNamed(t, f.db, "objection-contributor"), "contributor", float64(1)); err != nil {
		t.Fatalf("add contributor: %v", err)
	}
	resp, _ := putJSON(t, f.ts,
		"/api/v1/projects/"+f.slug+"/consensus/objections/"+itoa(objID)+"/resolve",
		`{"status":"overridden","resolution":"nope"}`,
		"Authorization", "Bearer "+tokenFor(t, f.db, "objection-contributor"))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a contributor resolved an objection: %d; want 403", resp.StatusCode)
	}
}

// TestTheResolutionIsRecordedWithItsReason is the happy path, asserted through the
// read the page uses: a 200 that wrote a different status than the one asked for
// would leave the page showing a resolution nobody made.
func TestTheResolutionIsRecordedWithItsReason(t *testing.T) {
	f := seedObjection(t)
	objID := f.anOpenObjection(t, "a maintainer decides alone")
	resp, body := f.resolve(t, objID,
		`{"status":"resolved","resolution":"the chart now runs at 03:00 instead"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve: %d %v", resp.StatusCode, body)
	}
	// The RESPONSE says what was written. It used to say the literal "resolved"
	// whatever the caller sent, so a page that rendered the answer showed a
	// resolution nobody had made -- and a client had no way to tell.
	if got, _ := body["status"].(string); got != "resolved" {
		t.Errorf("the response reports status %q for a resolve; want the status written",
			got)
	}
	var status, resolution string
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT status, COALESCE(resolution,'') FROM objections WHERE id = ?`, objID).
		Scan(&status, &resolution); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "resolved" || resolution != "the chart now runs at 03:00 instead" {
		t.Errorf("recorded status %q resolution %q; want resolved and the reason given",
			status, resolution)
	}
}

var _ = json.Marshal
