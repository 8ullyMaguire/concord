package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/discovery"
)

// Scout's read model — spec revision 4 §4.5.1, phase3-spec §6,
// docs/specs/scout-spec.md.
//
// Scout answers "I have this idea, what in the catalog could I use, and what
// should I avoid?" Everything it needs already existed except the
// classification, which is what this file is.
//
// The load-bearing thing in here is the ABSENCE of a value. `health_score` is 0
// for every live project, because the only writer is the Phase 0 discovery job
// that does not run. A classifier keyed on that column therefore calls everything
// `avoid` and passes any test asserting "at least one avoid" — the exact trap
// phase3-spec §6.3 warns about by name.
//
// So `ProjectHealth` returns (value, known, error). `known` is false when there
// are no metrics, and an unknown health is NOT zero: an absent telemetry row
// says nothing about whether a project is abandoned, and treating it as zero
// makes every unmeasured project `avoid` and every dead-but-instructive project
// indistinguishable from a bad one.

// ScoutHealthFloor is the total below which a project with KNOWN metrics is
// classified `avoid`.
//
// Named rather than inlined because scout-spec.md §3 lists it as a rule and a
// test has to be able to move it. 0.25 is not a measured threshold — there are
// no measured projects yet — so it is stated as a constant to be tuned against
// real data rather than dressed up as a calibrated number.
const ScoutHealthFloor = 0.25

// ScoutDeadFloor is the total below which a project is dead rather than merely
// unhealthy, which makes it `inspire` instead of `avoid`.
//
// Strictly below ScoutHealthFloor, so the two verdicts cannot both apply: a
// project under 0.1 is `avoid` and never `inspire`, because "abandoned" is the
// more useful thing to tell someone and it must not be the softer one.
const ScoutDeadFloor = 0.10

// The five classifications. scout-spec.md §3 fixes the ORDER these are applied
// in, and the order is the whole rule: a hard license conflict is `avoid` even
// if the project would otherwise be the lead match, and a dead project that is
// nevertheless the lead is `inspire` rather than `base-on` because nobody forks
// a corpse.
const (
	ScoutAdopt   = "adopt"
	ScoutBaseOn  = "base-on"
	ScoutExtend  = "extend"
	ScoutInspire = "inspire"
	ScoutAvoid   = "avoid"
)

// ScoutVerdict is one classified candidate.
type ScoutVerdict struct {
	ProjectID int64  `json:"project_id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Verdict   string `json:"verdict"`
	// Reason is one sentence a human wrote no part of.
	Reason string `json:"reason"`
	// Signals are the named inputs that produced the verdict. NEVER empty: an
	// unexplained classification is a bug, because the surface exists to answer
	// "why did it say that" (scout-spec.md §3).
	Signals []string `json:"signals"`
	// Fit is 0..1 and Coverage is the share of scoring weight that had evidence
	// behind it. Both are always present: a fit number without its coverage is the
	// misleading-reporting failure the Finder fix already established.
	Fit      float64 `json:"fit"`
	Coverage float64 `json:"evidence_coverage"`
	// LeadOn is how many of the idea's capabilities this project is the top match
	// for. Zero means "not the lead anywhere", which is what makes `base-on` a
	// distinct claim from `adopt`.
	LeadOn int `json:"lead_on"`
	// Matched is how many of the idea's capabilities this project satisfies at
	// all. Matched < Total is what makes a verdict `adopt` rather than `base-on`.
	Matched int `json:"matched"`
	Total   int `json:"total"`
	// Health is the computed score and HealthKnown says whether it means
	// anything. A reader must never see "0" without "known: false".
	Health      float64 `json:"health"`
	HealthKnown bool    `json:"health_known"`
	// License is the project's own, empty when unset.
	License string `json:"license,omitempty"`
	// Pain is the known pain attached to this project: unresolved complaints and
	// bad field-report outcomes. Empty is normal and means nothing bad.
	Pain []ScoutPain `json:"pain,omitempty"`
	// Reports is the field-report sample size. Zero is NOT a 0% outcome rate and
	// must never be rendered as one.
	Reports     int     `json:"reports"`
	OutcomeRate float64 `json:"outcome_rate"`
}

// ScoutPain is one piece of known trouble with a candidate project.
type ScoutPain struct {
	Kind     string  `json:"kind"`
	Title    string  `json:"title"`
	Detail   string  `json:"detail,omitempty"`
	Severity float64 `json:"severity,omitempty"`
}

// ScoutCapability is one capability in the decomposition.
type ScoutCapability struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Category string `json:"category"`
	// InCatalog is false for a key the text implied but the catalog does not
	// define. Such a capability is reported as a PROPOSAL and is never scored
	// against (scout-spec.md §4) — `TestScoutNeverInventsACapabilityOutside-
	// TheCatalog`.
	InCatalog bool `json:"in_catalog"`
	// Matched is the coverage state across the candidate set: covered / partial /
	// open, from the assertion states (scout-spec.md §6).
	Coverage string `json:"coverage"`
	// Evidence is why this key was pulled out of the text — "verbatim" for a key
	// appearing literally, "label_word" for a label or category word.
	Evidence string `json:"evidence,omitempty"`
	// Signals names the capabilities this one is near-duplicate of, so an editor
	// can merge them rather than scoring the same thing twice.
	Signals []string `json:"signals,omitempty"`
}

// ScoutReport is the whole answer.
type ScoutReport struct {
	Idea string `json:"idea"`
	// Capabilities is the decomposition, catalog keys plus explicit proposals.
	Capabilities []ScoutCapability `json:"capabilities"`
	// InCatalog and Proposed split the decomposition, because a reader needs to
	// know which half of it can be scored at all.
	InCatalog int `json:"in_catalog"`
	Proposed  int `json:"proposed"`
	// Verdicts is every classified candidate, not only the best few: the whole
	// point is also learning what to avoid.
	Verdicts []ScoutVerdict `json:"verdicts"`
	// Constraints the caller stated, echoed so a reader can see they were applied
	// rather than assumed.
	Constraints []string `json:"constraints,omitempty"`
	// Stats are the counts by verdict, which is the sentence worth reading first.
	Stats map[string]int `json:"stats"`
	// CandidatesConsidered is how many catalog projects were scored, so a short
	// verdict list can be read as "nothing else matched" rather than "we only
	// looked at these".
	CandidatesConsidered int `json:"candidates_considered"`
}

// ProjectHealth returns a project's health and whether that number means
// anything.
//
// (value, known, error). `known` is false when project_metrics has no row for the
// project — which is every project on a fresh instance — and the value is then 0
// with NO claim attached. Computing it through discovery.HealthScore rather than
// reading the stored column is deliberate: the stored column is 0 for all 70 live
// projects (spec §2), and a rule keyed on it classifies the whole catalog avoid.
//
// An error reading metrics is returned rather than swallowed as "unknown", since
// a broken query and absent telemetry are different facts.
func (d *DB) ProjectHealth(ctx context.Context, projectID int64) (float64, bool, error) {
	// discovery.Metrics has exactly four fields, because HealthScore reads exactly
	// four signals. The first version of this scanned all eight project_metrics
	// columns into a store.Metrics that does not exist — stars, forks and open
	// issues have no consumer here because HealthScore ignores them.
	// The signals are read into NULLABLE locals — not COALESCEd, and not straight
	// into discovery.Metrics — for two reasons that each cost a real bug to find:
	//
	//   - Project creation (store.go:382) inserts an EMPTY project_metrics row for
	//     every project, so a row's existence says nothing about whether anything was
	//     ever measured. A COALESCE turns that placeholder into a confident score
	//     reported as KNOWN: the §6.3 trap reintroduced one layer down, where every
	//     brand-new project reads as "measured, mediocre".
	//   - discovery.Metrics holds plain float64s. Scanning into it directly erases
	//     the NULL, so "measured as zero" and "never measured" become
	//     indistinguishable and the distinction above cannot be expressed.
	//
	// So: no row, or a row with no signal in it, is UNKNOWN. Only a row carrying at
	// least one real measurement produces a number.
	var contributors, releases90d sql.NullInt64
	var lastCommit, medianReview sql.NullFloat64

	row := d.QueryRowContext(ctx, `
		SELECT contributors, last_commit_at, median_review_hours, releases_90d
		FROM project_metrics WHERE project_id = ?`, projectID)
	if err := row.Scan(&contributors, &lastCommit, &medianReview,
		&releases90d); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No telemetry at all. Explicitly unknown, and the caller must NOT read
			// 0 as "bad" — see the file header.
			return 0, false, nil
		}
		return 0, false, err
	}

	measured := metricsRow{
		contributors:      nullInt(contributors),
		medianReviewHours: nullFloat(medianReview),
		releases90d:       nullInt(releases90d),
		lastCommit:        lastCommit.Valid && lastCommit.Float64 > 0,
	}
	if !measured.any() {
		// The row exists but is the placeholder project creation wrote. Scoring it
		// would call every untouched project mediocre.
		return 0, false, nil
	}

	m := discovery.Metrics{
		Contributors:      int(measured.contributors),
		MedianReviewHours: measured.medianReviewHours,
		Releases90d:       int(measured.releases90d),
	}
	if measured.lastCommit {
		m.LastCommitAgeDays =
			(float64(time.Now().Unix()) - lastCommit.Float64) / 86400
	}
	return discovery.HealthScore(m, discovery.DefaultWeights).Total, true, nil
}

// metricsRow is the four signals HealthScore reads, with the NULLs already resolved.
type metricsRow struct {
	contributors      int64
	medianReviewHours float64
	releases90d       int64
	lastCommit        bool
}

// any reports whether this row holds at least one real measurement.
//
// A row of all zeros is the placeholder project creation writes, and a placeholder
// is not a measurement. Treating it as one is what made every new project look
// mediocre instead of unknown.
func (m metricsRow) any() bool {
	return m.contributors > 0 || m.releases90d > 0 || m.lastCommit ||
		m.medianReviewHours > 0
}

func nullInt(v sql.NullInt64) int64 {
	if !v.Valid {
		return 0
	}
	return v.Int64
}

func nullFloat(v sql.NullFloat64) float64 {
	if !v.Valid {
		return 0
	}
	return v.Float64
}

// ProjectPain returns the known pain attached to a project: unresolved
// complaints, and field reports whose outcome says it did not work.
//
// A project with no complaints and no reports returns an empty slice, which is
// the normal case and means nothing bad — the caller distinguishes "no pain" from
// "not measured" by the sample size it carries separately.
func (d *DB) ProjectPain(ctx context.Context, projectID int64, limit int) ([]ScoutPain, error) {
	if limit <= 0 {
		limit = 5
	}
	// Complaints first. `open` and `validated` are the unresolved states; `closed`,
	// `rejected` and `merged_into` are history and would read as current problems.
	rows, err := d.QueryContext(ctx, `
		SELECT title, body, severity FROM complaints
		WHERE project_id = ? AND status IN ('open','validated')
		ORDER BY severity DESC, id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	out := []ScoutPain{}
	for rows.Next() {
		var p ScoutPain
		p.Kind = "complaint"
		var body string
		var severity int
		if err := rows.Scan(&p.Title, &body, &severity); err != nil {
			rows.Close()
			return nil, err
		}
		p.Detail = truncateRunes(body, 160)
		p.Severity = float64(severity)
		out = append(out, p)
	}
	// Close before the next query: MaxOpenConns(1) (PLAN.md rule 6).
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return out, nil
}

// ListScoutCandidates returns every readable project with the attributes Scout
// scores on: its capability values and its field-report outcome rate.
//
// Two things this does that the obvious implementation would not:
//
//   - It returns projects the caller can READ. Scout's report is about the
//     catalog, so a private project a stranger cannot read must not appear in
//     their verdict list — that is the same anti-enumeration rule the panels
//     implement, in a different surface.
//   - It attaches a metrics row only when one EXISTS, so the classifier can tell
//     "no telemetry" from "telemetry says zero". See ProjectHealth.
func (d *DB) ListScoutCandidates(ctx context.Context, viewerID int64) ([]ScoutCandidateRow, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT p.id, p.slug, p.name, COALESCE(p.license,''), p.visibility
		FROM projects p
		ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type row struct {
		ScoutCandidateRow
		attrs map[string]string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.Slug, &r.Name, &r.License, &r.Visibility); err != nil {
			return nil, err
		}
		// Visibility, per project: CanAccessProject is the single decision, so
		// Scout cannot drift from the rule the rest of the API enforces.
		//
		// visibility comes from the outer SELECT rather than a second query per
		// project: the first version scanned four columns and then re-read the whole
		// row for every candidate to reach it, which is an N+1 over the entire
		// catalog on every scout request.
		ok, err := d.CanAccessProject(ctx, Project{
			ID: r.ID, Slug: r.Slug, Name: r.Name, License: r.License,
			Visibility: r.Visibility,
		}, viewerID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		r.attrs = map[string]string{}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Closed before the two queries below, for the same MaxOpenConns(1) reason.

	out := make([]ScoutCandidateRow, 0, len(all))
	for _, r := range all {
		attrs, err := d.ListCapabilitiesForSet(ctx, []int64{r.ID}, "")
		if err != nil {
			return nil, err
		}
		if m := attrs[r.ID]; m != nil {
			r.attrs = m
		}
		rate, reps, err := d.FieldReportOutcomeRate(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		r.Attrs = r.attrs
		r.OutcomeRate, r.Reports = rate, reps
		out = append(out, r.ScoutCandidateRow)
	}
	return out, nil
}

// ScoutCandidateRow is one catalog project as Scout sees it.
type ScoutCandidateRow struct {
	ID      int64  `json:"id"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	License string `json:"license,omitempty"`
	// Visibility is carried from the candidate query so the access decision needs
	// no second read per project. Never serialized: it is an input to a decision,
	// not part of the answer.
	Visibility string `json:"-"`
	// Attrs is capability key -> value for THIS project. A missing key means
	// nobody has asserted it, which is distinct from a key present with the value
	// "unknown" — finder.go's Candidate.Attrs carries the same distinction and
	// Scout must not flatten it.
	Attrs map[string]string `json:"attrs,omitempty"`
	// OutcomeRate is 0..1 and Reports is its sample size. Reports == 0 means
	// nobody has filed a report, NOT that every report failed.
	OutcomeRate float64 `json:"outcome_rate"`
	Reports     int     `json:"reports"`
}

// truncateRunes shortens s to at most n runes, marking the cut.
//
// Runes, not bytes: a byte cut through a multi-byte character produces invalid
// UTF-8, which json.Marshal turns into a replacement character in the middle of
// a complaint a reader is trying to read.
func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// ListCapabilityCatalog returns every capability, for the decomposition.
//
// The catalog is the whole vocabulary (scout-spec.md §4): a capability the text
// implies but the catalog does not define is reported as a proposal and never
// scored, so this has to be the complete list rather than a category filter.
func (d *DB) ListCapabilityCatalog(ctx context.Context) ([]Capability, error) {
	caps, err := d.ListCapabilities(ctx, "")
	if err != nil {
		return nil, err
	}
	if caps == nil {
		return []Capability{}, nil
	}
	return caps, nil
}

// CapabilityStateCounts is how many assertions of each state exist for one
// capability across a set of projects (spec §6).
type CapabilityStateCounts struct {
	Confirmed int `json:"confirmed"`
	Asserted  int `json:"asserted"`
	Disputed  int `json:"disputed"`
}

// Total is how many projects said anything at all, in any state.
//
// This is the number that separates `partial` from `open`: a capability nobody has
// mentioned is `open`, and one anybody has mentioned is at least `partial`.
func (c CapabilityStateCounts) Total() int {
	return c.Confirmed + c.Asserted + c.Disputed
}

// CapabilityStateCountsForSet returns the assertion-state counts per capability key
// across the given projects.
//
// A SEPARATE query from ListCapabilitiesForSet, and it has to be: that function
// returns `a.value` — the yes/no/partial string a project claims. Coverage needs
// `a.state` — whether anyone CONFIRMED that claim. The two are different questions,
// and a first version of the coverage code read values from the value map and so
// could never distinguish a confirmed assertion from an unconfirmed one: every
// capability came back `partial` and `covered` was unreachable.
//
// State is derived the same way ConfirmCapability derives it, rather than stored,
// so the quorum arithmetic has exactly one implementation.
func (d *DB) CapabilityStateCountsForSet(ctx context.Context,
	projectIDs []int64) (map[string]CapabilityStateCounts, error) {
	out := map[string]CapabilityStateCounts{}
	if len(projectIDs) == 0 {
		return out, nil
	}

	// The confirm/dispute subqueries are copied from `assertionColumns` VERBATIM,
	// including `c.user_id <> a.asserted_by` — the author does not get to confirm
	// their own claim. A re-typed variant that dropped that clause would let an
	// author reach quorum alone, so the expression is reused rather than
	// paraphrased.
	args := make([]any, 0, len(projectIDs))
	q := `SELECT a.capability,
	      (SELECT COUNT(*) FROM capability_confirmations c
	        WHERE c.assertion_id = a.id AND c.confirmed = 1
	          AND c.user_id <> a.asserted_by),
	      (SELECT COUNT(*) FROM capability_confirmations c
	        WHERE c.assertion_id = a.id AND c.confirmed = 0
	          AND c.user_id <> a.asserted_by)
	      FROM capability_assertions a WHERE a.project_id IN (?`
	args = append(args, projectIDs[0])
	for _, id := range projectIDs[1:] {
		q += `, ?`
		args = append(args, id)
	}
	q += `)`

	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var confirms, disputes int
		if err := rows.Scan(&key, &confirms, &disputes); err != nil {
			return nil, err
		}
		c := out[key]
		// The same precedence as scanAssertion: a dispute outranks a confirmation.
		switch {
		case disputes >= CapDisputeQuorum:
			c.Disputed++
		case confirms >= CapQuorum:
			c.Confirmed++
		default:
			// An assertion nobody has backed is `asserted`, whatever it says.
			c.Asserted++
		}
		out[key] = c
	}
	return out, rows.Err()
}
