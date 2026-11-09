package db

import (
	"database/sql"
	"strings"
	"testing"
)

// The conformance table for the ledger rules Polarised's `polaris-ledger` crate states in Rust.
//
// concord is Go: it cannot depend on that crate, so it is NOT an adopter and must not be reported as one
// (ADR 0008). What it has instead is the stronger of the two enforcements — a BEFORE INSERT trigger, which survives a
// migration replay, a second client and a manual `psql`/`sqlite3` insert, none of which a library rule does. This file
// holds it to the same cases the crate's `validate` refuses, one test per case, named so the two tables can be read
// side by side:
//
//	crate `a_bounded_power_must_carry_an_expiry_and_an_expired_one_is_a_defect`  ↔ case "expiry equal to creation"
//	crate `a_required_reason_that_is_missing_or_blank_is_a_defect`               ↔ case "reason NULL"
//	concord's `grounds CHECK`                                                   ↔ case "grounds outside the vocabulary"
//
// Writing the bad row is the only assertion that means anything: a test that reads the trigger's SQL out of
// sqlite_master passes on a trigger whose WHEN clause is always false.

type holdCase struct {
	name      string
	createdAt float64
	expiresAt float64
	reason    sql.NullString
	grounds   string
	wantErr   string // substring the store must refuse with; "" means the row must be accepted
}

func holdCases() []holdCase {
	const t0, hour = 1_760_000_000.0, 3600.0
	return []holdCase{
		{
			name:      "expiry equal to creation is refused",
			createdAt: t0, expiresAt: t0,
			reason: sql.NullString{String: "security review", Valid: true}, grounds: "security",
			wantErr: "expiry must be after creation",
		},
		{
			name:      "expiry before creation is refused",
			createdAt: t0, expiresAt: t0 - hour,
			reason: sql.NullString{String: "legal request", Valid: true}, grounds: "legal",
			wantErr: "expiry must be after creation",
		},
		{
			name:      "a hold with no reason is refused",
			createdAt: t0, expiresAt: t0 + hour,
			reason: sql.NullString{}, grounds: "security",
			wantErr: "NOT NULL",
		},
		{
			name:      "grounds outside the vocabulary are refused",
			createdAt: t0, expiresAt: t0 + hour,
			reason: sql.NullString{String: "we disagreed in chat", Valid: true}, grounds: "disagreement",
			wantErr: "CHECK",
		},
		{
			name:      "a bounded hold with a future expiry is accepted",
			createdAt: t0, expiresAt: t0 + hour,
			reason: sql.NullString{String: "security review", Valid: true}, grounds: "security",
			wantErr: "",
		},
	}
}

func TestEmergencyHoldRulesMatchTheCrate(t *testing.T) {
	for _, c := range holdCases() {
		t.Run(c.name, func(t *testing.T) {
			d := openMigrated(t)
			projectID, callID, userID := seedHoldParents(t, d)

			_, err := d.Exec(
				`INSERT INTO emergency_holds (project_id, call_id, held_by, reason, grounds, created_at, expires_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				projectID, callID, userID, c.reason, c.grounds, c.createdAt, c.expiresAt,
			)

			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("the store refused a valid hold: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("the store accepted a row the rules refuse (%s) — the trigger or constraint is not doing its job", c.name)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(c.wantErr)) {
				t.Fatalf("refused, but for the wrong reason: want %q, got %q", c.wantErr, err)
			}
		})
	}
}

// seedHoldParents creates the rows emergency_holds references. Written through SQL rather than the application layer
// on purpose: this file is about the store's own refusals, and going through a service would test the service.
func seedHoldParents(t *testing.T, d *sql.DB) (projectID, callID, userID int64) {
	t.Helper()
	const t0 = 1_760_000_000.0

	res, err := d.Exec(`INSERT INTO users (username, display_name, created_at) VALUES (?, ?, ?)`,
		"hold-tester", "Hold Tester", t0)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userID, _ = res.LastInsertId()

	res, err = d.Exec(`INSERT INTO projects (slug, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		"hold-project", "Hold Project", t0, t0)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	projectID, _ = res.LastInsertId()

	res, err = d.Exec(
		`INSERT INTO consensus_calls (project_id, opened_by, opens_at, closes_at, status)
		 VALUES (?, ?, ?, ?, 'open')`,
		projectID, userID, t0, t0+72*3600,
	)
	if err != nil {
		t.Fatalf("seed consensus call: %v", err)
	}
	callID, _ = res.LastInsertId()

	return projectID, callID, userID
}
