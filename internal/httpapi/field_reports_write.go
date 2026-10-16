package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// §4.7's write half (docs/specs/field-reports-write-spec.md).
//
// The read half has been routed since phase3: the project page renders the panel, its outcome rate,
// the denominator beside it, the per-environment breakdown and the owner replies. Nothing could
// create a report or reply to one, so the panel's "No field reports yet" was not a state the
// instance happened to be in -- it was the state the code guaranteed, permanently. §27's inventory
// measured five store functions in that position.
//
// WHO MAY WRITE, and why it is not a project role:
//
// A field report is an experience record about USING the project. §4.7's sentence is "worked for
// 83% of reporters", and a report that only members could file would make that number a poll of
// people who already joined -- the people most likely to have made it work. The owner's REPLY is
// distinguished from a bystander's by `is_owner`, which RespondToFieldReport reads from the
// membership table; requiring membership to reply would erase the distinction the flag exists to
// draw.
//
// So both writes need authentication and nothing else. The project is still resolved through
// requireProjectID, so a private project's reports are not reachable (and the refusal is a 404, not
// a 403 -- an enumerable instance is the failure mode requireProjectID documents).

// createFieldReportReq is the filing form.
//
// UserID and ProjectID are deliberately absent: both come from the request's context (the token and
// the path), because a report whose author is whatever the body said is not evidence. Outcome is the
// only field with a value set, and it is validated in the store.
type createFieldReportReq struct {
	Version     string `json:"version"`
	UseCase     string `json:"use_case"`
	Environment string `json:"environment"`
	Scale       string `json:"scale"`
	Duration    string `json:"duration"`
	Outcome     string `json:"outcome"`
	MigratedTo  string `json:"migrated_to"`
	Caveats     string `json:"caveats"`
	Workaround  string `json:"workaround"`
	Advice      string `json:"advice"`
}

// respondToFieldReportReq is the reply form. There is no `is_owner` field and there must never be
// one: the flag's whole value is that the server sets it.
type respondToFieldReportReq struct {
	Body string `json:"body"`
}

// handleCreateFieldReport files a report against the URL's project.
func (s *Server) handleCreateFieldReport(w http.ResponseWriter, r *http.Request) {
	// Auth first, then the project: an anonymous POST to a real project and to a nonexistent one
	// must both answer 401, or the response code enumerates the slugs on the instance (the
	// ordering rule in internal/httpapi/auth.go).
	actorID, ok := s.requireWriteActor(w, r)
	if !ok {
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}

	var req createFieldReportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		mapError(w, store.ErrInvalid)
		return
	}

	report, err := s.Store.CreateFieldReport(r.Context(), store.FieldReport{
		ProjectID:   projectID,
		UserID:      actorID,
		Version:     req.Version,
		UseCase:     req.UseCase,
		Environment: req.Environment,
		Scale:       req.Scale,
		Duration:    req.Duration,
		Outcome:     req.Outcome,
		MigratedTo:  req.MigratedTo,
		Caveats:     req.Caveats,
		Workaround:  req.Workaround,
		Advice:      req.Advice,
	})
	if err != nil {
		mapError(w, err)
		return
	}

	// The audit detail carries the outcome, not the prose: the outcome is the field that decides
	// what this report counts for in §4.2's rate, and it is the one worth reconstructing later.
	_ = s.Store.AddAudit(r.Context(), projectID, actorID, "file_field_report",
		"field_report", report.ID, report.Outcome)

	writeJSON(w, http.StatusCreated, report)
}

// handleRespondToFieldReport replies to a report.
//
// The route is project-scoped even though the report carries its own project, so that the
// visibility check is requireProjectID's and cannot be forgotten -- and the handler then proves the
// report is IN that project, because report ids are global: without that proof, any project's URL
// would answer for any report (the nested-route defect §26 fixed one layer down, where a URL names
// one id and acts on another).
func (s *Server) handleRespondToFieldReport(w http.ResponseWriter, r *http.Request) {
	actorID, ok := s.requireWriteActor(w, r)
	if !ok {
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	report, ok := s.requireReportInProject(w, r, projectID)
	if !ok {
		return
	}

	var req respondToFieldReportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		mapError(w, store.ErrInvalid)
		return
	}

	resp, err := s.Store.RespondToFieldReport(r.Context(), report.ID, actorID, req.Body)
	if err != nil {
		mapError(w, err)
		return
	}

	_ = s.Store.AddAudit(r.Context(), projectID, actorID, "respond_field_report",
		"field_report", report.ID, "is_owner="+strconv.FormatBool(resp.IsOwner))

	writeJSON(w, http.StatusCreated, resp)
}

// requireReportInProject resolves {report_id} and proves it belongs to the URL's project.
//
// Both halves are load-bearing: report ids are global, so a route that read only {report_id} would
// let any project's URL address any report.
func (s *Server) requireReportInProject(w http.ResponseWriter, r *http.Request, projectID int64) (store.FieldReport, bool) {
	var report store.FieldReport
	id, err := strconv.ParseInt(chi.URLParam(r, "report_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return report, false
	}
	report, err = s.Store.GetFieldReport(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return report, false
	}
	if report.ProjectID != projectID {
		mapError(w, store.ErrNotFound)
		return report, false
	}
	return report, true
}
