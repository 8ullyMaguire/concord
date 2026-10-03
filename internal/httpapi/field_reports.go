package httpapi

import (
	"net/http"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// The field reports panel's read model (spec §4.10 "Field reports",
// phase3-spec §4).
//
// §4.7 promises the project page reads "worked for 83% of reporters on arm64".
// The half of that sentence that carries the meaning is "of reporters", so the
// response shape puts the denominator beside the rate and makes the no-data case
// distinguishable from a zero:
//
//   - OutcomeRate is a *float64. Zero means reporters said it did not work;
//     nil means nobody has reported. Those are different facts and one of them
//     is a claim about a project nobody has tried.
//
// `FieldReportOutcomeRate` already returns `(rate, sampleSize, error)` rather
// than a float for this reason, and a caller that drops the sample size can
// present 100% from one report as if it were established.
type projectFieldReportsRes struct {
	ProjectID int64 `json:"project_id"`
	// OutcomeRate is the reputation-weighted success rate, or nil when there are
	// no reports. Never 0 for "no data".
	OutcomeRate *float64 `json:"outcome_rate,omitempty"`
	// SampleSize is the denominator. Always present, including when the rate is
	// nil — "no reports" is worth stating explicitly.
	SampleSize int `json:"sample_size"`
	// ByEnvironment is §4.7's per-environment breakdown ("on arm64"), nil when
	// no report named an environment.
	ByEnvironment map[string]float64 `json:"by_environment,omitempty"`
	Reports       []reportRow        `json:"reports"`
	// Shown and Omitted bound the rendered list. The page shows the newest ten
	// and says how many it did not show; silently truncating a list of 400
	// reports reads as "there are ten".
	Shown   int `json:"shown"`
	Omitted int `json:"omitted"`
}

// FieldReportPageLimit is how many reports the panel renders. A page that loads
// every report is a page nobody waits for, and the count of what was dropped is
// more useful than the tail.
const FieldReportPageLimit = 10

// reportRow is one report plus the owner's reply, which §4.7 gives a right to
// ("Owners can respond but cannot delete reports") and which therefore belongs
// next to the claim rather than on a separate page the reader has to find.
//
// It deliberately does NOT carry this report's contribution to the rate. The
// weighting is `reportWeight(reputation)` — unexported, computed from a
// reputation sum — and a per-report weight would need either a new store method
// or a duplicated copy of the formula here, and the second is how two
// representations of one fact start. SampleSize plus the rate is the honest
// summary; the weighting is stated in the panel's own copy instead.
type reportRow struct {
	store.FieldReport
	Responses []store.FieldOwnerResponse `json:"responses,omitempty"`
}

func (s *Server) handleListProjectFieldReports(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}

	// includeRemoved=false: a removed report is hidden, not deleted, and its
	// row still exists for the moderation record. Serving it here would undo
	// the quorum decision that removed it.
	reports, err := s.Store.ListFieldReports(r.Context(), projectID, false)
	if err != nil {
		mapError(w, err)
		return
	}

	res := projectFieldReportsRes{
		ProjectID: projectID,
		Reports:   []reportRow{},
	}

	rate, sample, err := s.Store.FieldReportOutcomeRate(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	res.SampleSize = sample
	if sample > 0 {
		r := rate
		res.OutcomeRate = &r
	}

	byEnv, err := s.Store.FieldReportOutcomeRateByEnvironment(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	if len(byEnv) > 0 {
		res.ByEnvironment = byEnv
	}

	shown := reports
	if len(shown) > FieldReportPageLimit {
		shown = shown[:FieldReportPageLimit]
		res.Omitted = len(reports) - len(shown)
	}
	res.Shown = len(shown)

	// Cursor discipline (PLAN.md rule 6): the rows cursor is closed inside
	// ListFieldReports, so querying per-report here is safe. Responses are read
	// one report at a time rather than with a second open cursor over the same
	// single-connection pool.
	for _, rep := range shown {
		responses, err := s.Store.ListFieldReportResponses(r.Context(), rep.ID)
		if err != nil {
			mapError(w, err)
			return
		}
		res.Reports = append(res.Reports, reportRow{
			FieldReport: rep,
			Responses:   responses,
		})
	}

	writeJSON(w, http.StatusOK, res)
}
