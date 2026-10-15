package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// The claims a §6.4 challenge names used to be unreadable.
//
// store.ContestCoverage has existed with a handler, a reason requirement, a
// self-vote refusal and its own tests, and NO READ exposed which claims a solution
// makes -- so "coverage claims are challengeable" was enforced against a table no
// client could see, and the control that would contest one had nothing to name.
//
// The fix is a field on the solutions payload the page already reads. This pins it,
// because the failure mode is silent in both directions: without the field the page
// renders no claims (which looks like a solution nobody claimed anything for), and
// with an empty list for a claimed solution it renders the same nothing.
func TestTheSolutionsReadCarriesTheCoverageClaims(t *testing.T) {
	ts, db, slug, pid, authorID := seedPanels(t)
	ctx := context.Background()

	complaintID := seedValidatedComplaint(t, db, pid, authorID)
	featureID := seedFeatureLinkedTo(t, db, pid, authorID, complaintID)
	solutionID := seedSolutionFor(t, db, featureID, authorID)

	if _, err := db.ExecContext(ctx,
		`INSERT INTO solution_coverage (solution_id, complaint_id, claim, created_at)
		 VALUES (?,?,'resolves',?)`, solutionID, complaintID, float64(1)); err != nil {
		t.Fatalf("insert coverage claim: %v", err)
	}
	seedSolutionArena(t, db, pid, featureID, solutionID)

	code, raw := authGet(t, ts,
		"/api/v1/projects/"+slug+"/features/"+itoa(featureID)+"/solutions", anonAuth)
	if code != http.StatusOK {
		t.Fatalf("read solutions: %d %s", code, raw)
	}
	var payload struct {
		Solutions []struct {
			SolutionID int64 `json:"solution_id"`
			Claims     []struct {
				ComplaintID int64  `json:"complaint_id"`
				Claim       string `json:"claim"`
				Contested   bool   `json:"contested"`
			} `json:"claims"`
		} `json:"solutions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if len(payload.Solutions) != 1 {
		t.Fatalf("the arena returned %d solutions; want the one seeded (%s)",
			len(payload.Solutions), raw)
	}
	claims := payload.Solutions[0].Claims
	if len(claims) != 1 {
		t.Fatalf("the solution carries %d claims; want 1 -- without it the page has "+
			"nothing to contest (%s)", len(claims), raw)
	}
	if claims[0].ComplaintID != complaintID || claims[0].Claim != "resolves" {
		t.Errorf("claim = %+v; want complaint %d resolved", claims[0], complaintID)
	}
	if claims[0].Contested {
		t.Errorf("a fresh claim reports itself contested")
	}

	// And a contested claim says so, which is what the page renders: the difference
	// between "this claim counts" and "this claim is under challenge" is the whole
	// point of the column.
	contestorID := userIDNamed(t, db, "claim-contestor")
	if _, err := db.ExecContext(ctx,
		`INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)`,
		pid, contestorID, "contributor", float64(1)); err != nil {
		t.Fatalf("add contestor: %v", err)
	}
	if err := db.ContestCoverage(ctx, solutionID, complaintID, contestorID,
		"it only fixes the first page"); err != nil {
		t.Fatalf("ContestCoverage: %v", err)
	}
	code, raw = authGet(t, ts,
		"/api/v1/projects/"+slug+"/features/"+itoa(featureID)+"/solutions", anonAuth)
	if code != http.StatusOK {
		t.Fatalf("read solutions after the contest: %d %s", code, raw)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.Solutions[0].Claims[0].Contested {
		t.Errorf("the claim is contested in the table and not in the payload (%s)", raw)
	}
}

// seedValidatedComplaint creates the complaint a feature needs, directly.
func seedValidatedComplaint(t *testing.T, db *store.DB, pid, authorID int64) int64 {
	t.Helper()
	res, err := db.ExecContext(context.Background(),
		`INSERT INTO complaints (project_id, author_id, title, body, status, severity,
		 frequency, created_at, updated_at)
		 VALUES (?,?,?,?,'validated',4,1.0,1,1)`,
		pid, authorID, "the export cannot resume", "body")
	if err != nil {
		t.Fatalf("insert complaint: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("complaint id: %v", err)
	}
	return id
}

func seedFeatureLinkedTo(t *testing.T, db *store.DB, pid, authorID, complaintID int64) int64 {
	t.Helper()
	ctx := context.Background()
	res, err := db.ExecContext(ctx,
		`INSERT INTO features (project_id, author_id, title, body, effort, status,
		 strategic_weight, created_at, updated_at)
		 VALUES (?,?,?,?,'M','consensus',1,1,1)`,
		pid, authorID, "resumable export", "body")
	if err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("feature id: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO feature_complaints (feature_id, complaint_id) VALUES (?,?)`,
		id, complaintID); err != nil {
		t.Fatalf("link complaint: %v", err)
	}
	return id
}

func seedSolutionFor(t *testing.T, db *store.DB, featureID, authorID int64) int64 {
	t.Helper()
	res, err := db.ExecContext(context.Background(),
		`INSERT INTO solutions (feature_id, author_id, title, body, type, relationship,
		 status, created_at, updated_at)
		 VALUES (?,?,?,?,'build-new','exclusive','draft',1,1)`,
		featureID, authorID, "checkpoint the stream", "body")
	if err != nil {
		t.Fatalf("insert solution: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("solution id: %v", err)
	}
	return id
}

// seedSolutionArena puts a solution in the arena ListSolutions reads.
//
// It reads the ARENA LEADERBOARD, not the solutions table, so a solution with no
// entry is invisible on the page -- which is how the first version of the browser
// fixture rendered no claims at all and looked like a broken control.
func seedSolutionArena(t *testing.T, db *store.DB, pid, featureID, solutionID int64) {
	t.Helper()
	ctx := context.Background()
	res, err := db.ExecContext(ctx,
		`INSERT INTO arenas (type, project_id, feature_id, question, created_at)
		 VALUES ('solution',?,?,?,1)`, pid, featureID, "Which approach should we build?")
	if err != nil {
		t.Fatalf("insert arena: %v", err)
	}
	arena, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("arena id: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO arena_entries (arena_id, entity_type, entity_id, is_baseline, updated_at)
		 VALUES (?,'solution',?,0,1)`, arena, solutionID); err != nil {
		t.Fatalf("insert arena entry: %v", err)
	}
}
