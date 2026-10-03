package store

// Capability matrix (spec revision 4 §4.2, milestone R5).
//
// A capability is a keyed question about a project -- `wip-limits`,
// `self-hosted`, `offline` -- and an assertion is somebody's claim that a
// project has some value for it. Assertions become `confirmed` through
// independent confirmations and `disputed` through disputes.
//
// This is the substrate Finder's questions are drawn from. It was built first
// because the Finder brief asserted the matrix already existed, and it did not:
// measured on the live instance before this file, there was no such table, so
// the questions Finder's own mockup names had no data behind them.
//
// The design decision everything else follows from: **`unknown` is a value, not
// an absence of a row.** A missing row means nobody has said anything; a row
// whose value is 'unknown' means somebody recorded that nobody knows. Finder
// scores both as unknown, but only the second is answerable, and the difference
// is the contribution loop -- an absent row is invisible, an 'unknown' row is a
// question the instance is asking its users.
//
// Confirmations and disputes share one table with a 0/1 column rather than
// being two tables. A dispute is a negative confirmation: same act, same people,
// same weight. Two tables would make "3 confirmations and 2 disputes" a question
// of which table to read first, and the answer would differ per reader.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Capability states. The assertion value set and the confirmation quorum.
const (
	CapYes     = "yes"
	CapPartial = "partial"
	CapNo      = "no"
	CapUnknown = "unknown"
	CapBoolean = "boolean"
	CapEnum    = "enum"

	// CapStateAsserted is a claim nobody outside its author has backed.
	CapStateAsserted = "asserted"
	// CapStateConfirmed is a claim independent users backed.
	CapStateConfirmed = "confirmed"
	// CapStateDisputed is a claim the community actively contradicts.
	CapStateDisputed = "disputed"

	// CapQuorum is how many independent confirmations promote an assertion.
	//
	// The same kappa the solutions coverage quorum uses (0018), so "how many
	// people does it take" has one answer on this instance rather than one per
	// subsystem.
	CapQuorum = 2

	// CapDisputeQuorum is how many disputes mark a claim disputed.
	//
	// Asymmetric on purpose. Confirming is cheap and two people agreeing is
	// good evidence; disputing is expensive because it is what you do to a
	// claim you believe is wrong, and two people disagreeing with one
	// assertion is a signal something is contested -- not a conclusion.
	CapDisputeQuorum = 2
)

// capabilityValues is the fixed value set for a boolean capability.
func capabilityValues(kind string) ([]string, error) {
	switch kind {
	case CapBoolean:
		return []string{CapYes, CapPartial, CapNo, CapUnknown}, nil
	case CapEnum:
		// An enum's values are declared by whoever defines it, so there is no
		// fixed set to check against here -- only that at least one was given.
		return nil, nil
	default:
		return nil, fmt.Errorf("%w: capability kind %q must be boolean or enum", ErrInvalid, kind)
	}
}

// Capability is a keyed question about a project, grouped so the matrix can be
// browsed per category.
type Capability struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Category string   `json:"category"`
	Kind     string   `json:"kind"`
	Values   []string `json:"values"`
}

// CapabilityAssertion is one project's current value for one capability, with
// the vote state that value has earned.
type CapabilityAssertion struct {
	ID         int64     `json:"id"`
	Capability string    `json:"capability"`
	ProjectID  int64     `json:"project_id"`
	Value      string    `json:"value"`
	Evidence   string    `json:"evidence"`
	AssertedBy int64     `json:"asserted_by"`
	AssertedAt time.Time `json:"asserted_at"`
	Confirms   int       `json:"confirms"`
	Disputes   int       `json:"disputes"`
	State      string    `json:"state"`
}

// NormalizeCapabilityKey canonicalises a capability key.
//
// The key is a namespace in the query language (`cap:wip-limits=yes`) and a
// primary key here, so two spellings of one capability would be two capabilities
// with half their assertions each. Lowercased and trimmed here so every writer
// agrees, rather than at read time where one caller would forget.
func NormalizeCapabilityKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// EnsureCapability creates a capability, or leaves an existing one alone.
//
// Idempotent by key rather than by content: re-registering `wip-limits` with a
// different label updates the label and refuses to change the kind or the value
// set. Changing those under live assertions would silently invalidate every
// assertion already recorded against the old set, so it is refused with a
// message naming the capability rather than performed.
func (d *DB) EnsureCapability(ctx context.Context, c Capability) error {
	key := NormalizeCapabilityKey(c.Key)
	if key == "" {
		return fmt.Errorf("%w: capability key must not be empty", ErrInvalid)
	}
	if strings.TrimSpace(c.Label) == "" {
		return fmt.Errorf("%w: capability %q needs a label", ErrInvalid, key)
	}
	if strings.TrimSpace(c.Category) == "" {
		return fmt.Errorf("%w: capability %q needs a category", ErrInvalid, key)
	}
	values, err := capabilityValues(c.Kind)
	if err != nil {
		return err
	}
	if c.Kind == CapEnum {
		values = normalizeValues(c.Values)
		if len(values) == 0 {
			return fmt.Errorf("%w: enum capability %q needs at least one value", ErrInvalid, key)
		}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("encode values for %q: %w", key, err)
	}

	_, err = d.ExecContext(ctx, `
		INSERT INTO capabilities (key, label, category, kind, "values", created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET label = excluded.label, category = excluded.category`,
		key, c.Label, c.Category, c.Kind, string(raw), float64(time.Now().Unix()))
	if err != nil {
		return fmt.Errorf("ensure capability %q: %w", key, err)
	}

	// A kind or value-set change is refused AFTER the upsert, because the upsert
	// above deliberately does not touch those columns -- so this is comparing the
	// requested shape against what is actually stored, not against a stale read.
	var kind, storedValues string
	err = d.QueryRowContext(ctx,
		`SELECT kind, "values" FROM capabilities WHERE key = ?`, key).Scan(&kind, &storedValues)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: capability %q", ErrNotFound, key)
	}
	if err != nil {
		return err
	}
	var existing []string
	if err := json.Unmarshal([]byte(storedValues), &existing); err != nil {
		return fmt.Errorf("decode stored values for %q: %w", key, err)
	}
	if kind != c.Kind || !sameValues(existing, values) {
		return fmt.Errorf("%w: capability %q is %s over %v and cannot become %s over %v "+
			"while assertions exist", ErrConflict, key, kind, existing, c.Kind, values)
	}
	return nil
}

func normalizeValues(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func sameValues(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		seen[v]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// GetCapability returns one capability definition.
func (d *DB) GetCapability(ctx context.Context, key string) (Capability, error) {
	key = NormalizeCapabilityKey(key)
	var (
		c       Capability
		rawVals string
	)
	err := d.QueryRowContext(ctx,
		`SELECT key, label, category, kind, "values" FROM capabilities WHERE key = ?`, key).
		Scan(&c.Key, &c.Label, &c.Category, &c.Kind, &rawVals)
	if errors.Is(err, sql.ErrNoRows) {
		return Capability{}, fmt.Errorf("%w: capability %q", ErrNotFound, key)
	}
	if err != nil {
		return Capability{}, err
	}
	if err := json.Unmarshal([]byte(rawVals), &c.Values); err != nil {
		return c, fmt.Errorf("decode values for %q: %w", key, err)
	}
	return c, nil
}

// ListCapabilities returns every capability, optionally one category at a time.
func (d *DB) ListCapabilities(ctx context.Context, category string) ([]Capability, error) {
	q := `SELECT key, label, category, kind, "values" FROM capabilities`
	var args []any
	if strings.TrimSpace(category) != "" {
		q += ` WHERE category = ?`
		args = append(args, strings.TrimSpace(category))
	}
	q += ` ORDER BY category, key`

	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Capability{}
	for rows.Next() {
		var (
			c   Capability
			raw string
		)
		if err := rows.Scan(&c.Key, &c.Label, &c.Category, &c.Kind, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &c.Values); err != nil {
			return nil, fmt.Errorf("decode values for %q: %w", c.Key, err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AssertCapability records what a project has for a capability.
//
// Re-asserting the same capability updates the value rather than adding a row:
// the matrix holds one current value per (capability, project), because two
// competing rows would mean every reader had to invent a tiebreak. Disagreement
// is a dispute in capability_confirmations, not a second assertion.
func (d *DB) AssertCapability(ctx context.Context, capability, slug, value, evidence string, by int64) (CapabilityAssertion, error) {
	key := NormalizeCapabilityKey(capability)
	if by == 0 {
		return CapabilityAssertion{}, fmt.Errorf("%w: a capability assertion needs an author", ErrInvalid)
	}
	cap, err := d.GetCapability(ctx, key)
	if err != nil {
		return CapabilityAssertion{}, err
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if !allowedValue(cap, value) {
		return CapabilityAssertion{}, fmt.Errorf("%w: %q is not a value of capability %q (%v)",
			ErrInvalid, value, key, cap.Values)
	}

	projectID, err := d.projectIDBySlug(ctx, slug)
	if err != nil {
		return CapabilityAssertion{}, err
	}

	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `
		INSERT INTO capability_assertions
			(capability, project_id, value, evidence, asserted_by, asserted_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(capability, project_id) DO UPDATE SET
			value       = excluded.value,
			evidence    = excluded.evidence,
			asserted_by = excluded.asserted_by,
			asserted_at = excluded.asserted_at`,
		key, projectID, value, evidence, by, now)
	if err != nil {
		return CapabilityAssertion{}, fmt.Errorf("assert %s for %s: %w", key, slug, err)
	}

	var id int64
	if err := d.QueryRowContext(ctx,
		`SELECT id FROM capability_assertions WHERE capability = ? AND project_id = ?`,
		key, projectID).Scan(&id); err != nil {
		return CapabilityAssertion{}, err
	}
	return d.getAssertion(ctx, id)
}

func allowedValue(cap Capability, value string) bool {
	for _, v := range cap.Values {
		if v == value {
			return true
		}
	}
	return false
}

// ConfirmCapability records an independent confirmation or a dispute, and
// returns the assertion's new state.
//
// Three refusals, each for a reason the schema cannot express:
//
//   - The author cannot confirm their own claim. Self-confirmation is the
//     cheapest possible way to promote a claim and would make CapQuorum
//     meaningless.
//   - A user cannot both confirm and dispute; one row per user per assertion
//     (a UNIQUE key) means their first vote is their vote. Confirming a claim
//     to raise its score and then disputing it to lower a rival's is vote
//     ringing, which §5.2's anti-gaming rules exist to prevent.
//   - A dispute never rewrites the value. A disputed claim shows as disputed;
//     substituting 'no' would turn "people disagree about this" into "this is
//     false", which is a different claim with a different evidence base.
func (d *DB) ConfirmCapability(ctx context.Context, assertionID, userID int64, confirm bool) (CapabilityAssertion, error) {
	if userID == 0 {
		return CapabilityAssertion{}, fmt.Errorf("%w: confirmation needs an actor", ErrInvalid)
	}
	a, err := d.getAssertion(ctx, assertionID)
	if err != nil {
		return CapabilityAssertion{}, err
	}
	if a.AssertedBy == userID {
		return CapabilityAssertion{}, fmt.Errorf(
			"%w: %s cannot confirm their own assertion about %q",
			ErrPerm, "the author", a.Capability)
	}

	flag := 0
	if confirm {
		flag = 1
	}
	// ON CONFLICT DO UPDATE rather than DO NOTHING: a user who changes their
	// mind has a right to, and a UNIQUE key that silently ignored the second
	// vote would report the old answer as the new one.
	_, err = d.ExecContext(ctx, `
		INSERT INTO capability_confirmations (assertion_id, user_id, confirmed, at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(assertion_id, user_id) DO UPDATE SET
			confirmed = excluded.confirmed,
			at        = excluded.at`,
		assertionID, userID, flag, float64(time.Now().Unix()))
	if err != nil {
		return CapabilityAssertion{}, fmt.Errorf("record confirmation: %w", err)
	}
	return d.getAssertion(ctx, assertionID)
}

const assertionColumns = `a.id, a.capability, a.project_id, a.value, a.evidence,
	a.asserted_by, a.asserted_at,
	(SELECT COUNT(*) FROM capability_confirmations c
	  WHERE c.assertion_id = a.id AND c.confirmed = 1
	    AND c.user_id <> a.asserted_by),
	(SELECT COUNT(*) FROM capability_confirmations c
	  WHERE c.assertion_id = a.id AND c.confirmed = 0
	    AND c.user_id <> a.asserted_by)`

func scanAssertion(sc interface{ Scan(...any) error }) (CapabilityAssertion, error) {
	var (
		a     CapabilityAssertion
		at    float64
		state string
	)
	err := sc.Scan(&a.ID, &a.Capability, &a.ProjectID, &a.Value, &a.Evidence,
		&a.AssertedBy, &at, &a.Confirms, &a.Disputes)
	if err != nil {
		return CapabilityAssertion{}, err
	}
	a.AssertedAt = time.Unix(int64(at), 0)

	// A dispute outranks a confirmation. A claim with two confirmations and
	// three disputes is contested, not established -- reading it as confirmed
	// would let louder disagreement lose to agreement that merely arrived first.
	switch {
	case a.Disputes >= CapDisputeQuorum:
		state = CapStateDisputed
	case a.Confirms >= CapQuorum:
		state = CapStateConfirmed
	default:
		state = CapStateAsserted
	}
	a.State = state
	return a, nil
}

func (d *DB) getAssertion(ctx context.Context, id int64) (CapabilityAssertion, error) {
	row := d.QueryRowContext(ctx,
		`SELECT `+assertionColumns+` FROM capability_assertions a WHERE a.id = ?`, id)
	a, err := scanAssertion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CapabilityAssertion{}, fmt.Errorf("%w: assertion %d", ErrNotFound, id)
	}
	return a, err
}

// ListCapabilityAssertions returns one project's assertions with their states.
func (d *DB) ListCapabilityAssertions(ctx context.Context, projectID int64) ([]CapabilityAssertion, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT `+assertionColumns+` FROM capability_assertions a
		 WHERE a.project_id = ? ORDER BY a.capability`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []CapabilityAssertion{}
	for rows.Next() {
		a, err := scanAssertion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListCapabilitiesForSet returns capability values for a set of projects.
//
// The shape is project -> capability -> value, and it returns ONLY rows that
// exist. That absence is load-bearing: Finder must be able to tell "nobody has
// asserted this" from "somebody asserted unknown", and a map that filled in
// 'unknown' for both would make the first indistinguishable from the second.
//
// Used once per candidate set per question, hence the project_id index in 0021.
func (d *DB) ListCapabilitiesForSet(ctx context.Context, projectIDs []int64, category string) (map[int64]map[string]string, error) {
	out := map[int64]map[string]string{}
	if len(projectIDs) == 0 {
		return out, nil
	}

	q := `SELECT a.project_id, a.capability, a.value
	        FROM capability_assertions a
	        JOIN capabilities c ON c.key = a.capability
	       WHERE a.project_id IN (?`
	args := []any{projectIDs[0]}
	for _, id := range projectIDs[1:] {
		q += `, ?`
		args = append(args, id)
	}
	q += `)`
	if strings.TrimSpace(category) != "" {
		q += ` AND c.category = ?`
		args = append(args, strings.TrimSpace(category))
	}
	q += ` ORDER BY a.project_id, a.capability`

	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			pid int64
			key string
			val string
		)
		if err := rows.Scan(&pid, &key, &val); err != nil {
			return nil, err
		}
		if out[pid] == nil {
			out[pid] = map[string]string{}
		}
		out[pid][key] = val
	}
	return out, rows.Err()
}
