package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// The unlink at the edge. Reuses the link fixture deliberately: the two routes act on
// the same table, and a fixture of its own would be a second definition of "a feature
// with one validated complaint attached", free to drift from the first.
//
// What the route tests add over the store tests is the STANDING: unlinking is a
// contributor edit, like linking, and the route must refuse a stranger before the store
// is touched. A handler that checked nothing and let the store refuse would pass on a
// missing row and be wrong on an existing one.

func (fx linkFixture) unlinkPath(complaintID int64) string {
	return fx.path() + "/" + strconv.FormatInt(complaintID, 10)
}

func TestUnlinkingAComplaintRemovesItAndAnswersWithTheRest(t *testing.T) {
	fx := setupLinkRouteFixture(t, "unlink-a")
	// Two attached complaints, so "the rest" is a set with something in it: an unlink
	// that answered with an empty list from a feature that still cites another complaint
	// would pass a count-of-zero assertion.
	code, body := authJSON(t, fx.ts, http.MethodPost, fx.path(), fx.ownerToken(t),
		map[string]any{"complaint_id": fx.spare})
	if code != http.StatusOK {
		t.Fatalf("link: %d %s", code, body)
	}

	code, body = authJSON(t, fx.ts, http.MethodDelete, fx.unlinkPath(fx.linked), fx.ownerToken(t), nil)
	if code != http.StatusOK {
		t.Fatalf("unlink: %d %s", code, body)
	}

	// The answer is the remaining list, because the section renders its own count and must
	// not re-read to learn what it just caused.
	m := decodedBody(t, body)
	list, _ := m["complaints"].([]any)
	if len(list) != 1 {
		t.Fatalf("response complaints = %v, want one", m["complaints"])
	}
	first, _ := list[0].(map[string]any)
	if id, _ := first["id"].(float64); int64(id) != fx.spare {
		t.Fatalf("response names complaint %v, want %d", first["id"], fx.spare)
	}

	// And the ROW, not only the body: a handler that answered correctly and wrote nothing
	// would pass everything above.
	ids := linkedComplaintIDs(t, fx.st, fx.featID)
	if len(ids) != 1 || ids[0] != fx.spare {
		t.Fatalf("stored links = %v, want [%d]", ids, fx.spare)
	}
}

func TestUnlinkingNeedsTheSameStandingAsLinking(t *testing.T) {
	fx := setupLinkRouteFixture(t, "unlink-b")
	// A signed-in NON-MEMBER, which is the refusal that matters: an anonymous caller is
	// stopped by the answer to "who are you", and never reaches the role check that
	// protects the write.
	if _, err := fx.st.CreateUser(t.Context(), "unlink-b-stranger", "Stranger"); err != nil {
		t.Fatalf("create stranger: %v", err)
	}
	stranger := "Bearer " + tokenFor(t, fx.st, "unlink-b-stranger")

	code, body := authJSON(t, fx.ts, http.MethodDelete, fx.unlinkPath(fx.linked), stranger, nil)
	if code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("a stranger unlinked a feature's evidence: %d %s", code, body)
	}
	if ids := linkedComplaintIDs(t, fx.st, fx.featID); len(ids) != 1 || ids[0] != fx.linked {
		t.Fatalf("the refused request changed the links: %v", ids)
	}
}

func TestUnlinkingRefusesAComplaintFromAnotherProject(t *testing.T) {
	fx := setupLinkRouteFixture(t, "unlink-c")
	// Reachable through this URL only because the id is global: the complaint belongs to
	// a second project the same person owns.
	other := registerAndLogin(t, fx.ts, "unlink-c-other")
	code, body := authJSON(t, fx.ts, http.MethodPost, "/api/v1/projects", other,
		map[string]any{"slug": "unlink-c-second", "name": "second", "description": "other"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create second project: %d %s", code, body)
	}
	otherPID := bodyInt64(t, body, "id")
	foreign := fileComplaint(t, fx.ts, other, "unlink-c-second", otherPID, "belongs elsewhere")
	if err := fx.st.ValidateComplaint(t.Context(), foreign); err != nil {
		t.Fatalf("ValidateComplaint(foreign): %v", err)
	}

	code, body = authJSON(t, fx.ts, http.MethodDelete, fx.unlinkPath(foreign), fx.ownerToken(t), nil)
	if code == http.StatusOK {
		t.Fatalf("another project's complaint was unlinked: %s", body)
	}
	if ids := linkedComplaintIDs(t, fx.st, fx.featID); len(ids) != 1 || ids[0] != fx.linked {
		t.Fatalf("the refusal changed the links: %v", ids)
	}
}

func TestUnlinkingNeedsANumericComplaintID(t *testing.T) {
	fx := setupLinkRouteFixture(t, "unlink-d")
	code, body := authJSON(t, fx.ts, http.MethodDelete, fx.path()+"/not-a-number", fx.ownerToken(t), nil)
	if code != http.StatusBadRequest {
		t.Fatalf("a non-numeric id: %d %s", code, body)
	}
	if !strings.Contains(string(body), "complaint_id") {
		t.Fatalf("the refusal does not name the field: %s", body)
	}
}

func TestUnlinkingWritesAnAuditLine(t *testing.T) {
	fx := setupLinkRouteFixture(t, "unlink-e")
	code, body := authJSON(t, fx.ts, http.MethodDelete, fx.unlinkPath(fx.linked), fx.ownerToken(t), nil)
	if code != http.StatusOK {
		t.Fatalf("unlink: %d %s", code, body)
	}
	// The removed evidence exists nowhere else afterwards, so this line is the record: a
	// silent unlink is a feature that quietly stopped being complaint-backed.
	page, err := fx.st.ListAudit(t.Context(), store.AuditFilter{
		ProjectID: fx.pid, Action: "unlink_complaint"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("audit entries for unlink_complaint = %d, want 1", len(page.Entries))
	}
	if !strings.Contains(page.Entries[0].Detail, "exporter") {
		t.Fatalf("the audit line does not name the complaint it removed: %q", page.Entries[0].Detail)
	}
}
