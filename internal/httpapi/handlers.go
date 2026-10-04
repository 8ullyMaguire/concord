package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/embed"
	"git.polarisocial.xyz/concord/concord/internal/governance"
	"git.polarisocial.xyz/concord/concord/internal/ranking"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

type createComplaintRequest struct {
	ProjectID     int64   `json:"project_id"`
	Title         string  `json:"title"`
	Body          string  `json:"body"`
	Severity      int     `json:"severity"`
	Frequency     float64 `json:"frequency"`
	StrategicMult float64 `json:"strategic_multiplier"`

	// ConfirmDuplicate acknowledges that a near-identical complaint already
	// exists and proceeds anyway. §6.1 calls duplicate detection advisory, so
	// this is the escape hatch: the filer is told, and may disagree with the
	// score. Without it, a false positive silently loses a real report.
	ConfirmDuplicate bool `json:"confirm_duplicate"`
}

func (s *Server) handleCreateComplaint(w http.ResponseWriter, r *http.Request) {
	// This handler had no authentication check while every sibling did, so a
	// complaint could be filed anonymously: getActorID returned 0 and it was
	// written with author_id 0, attributing it to nobody. A complaint is a
	// claim about a project and is the main input to the ranking, so it needs
	// an author the same way a feature does.
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	var req createComplaintRequest
	if !readJSON(w, r, &req) {
		return
	}
	// Default the strategic multiplier to 1.0 rather than leaving it at the
	// zero value of an omitted field.
	//
	// PainScore multiplies by strategicMult, so a caller that sends severity
	// and frequency but not the multiplier — the obvious thing to do, since
	// only the first two appear in the complaint form — gets a pain score of
	// exactly 0. The complaint is stored, validated, and linked to a feature,
	// and it contributes nothing to that feature's priority with no indication
	// of why. A zero multiplier is not a meaningful value; a neutral one is.
	if req.StrategicMult <= 0 {
		req.StrategicMult = 1.0
	}

	// §6.1: duplicate detection before submit. A strong match returns 409 with the
	// candidates and the filing proceeds only with confirm_duplicate.
	sims, proceed := s.checkDuplicates(w, r, store.KindComplaint,
		req.Title, req.Body, req.ProjectID, req.ConfirmDuplicate)
	if !proceed {
		return
	}

	c, err := s.Store.CreateComplaint(r.Context(), req.ProjectID, getActorID(r), req.Title, req.Body, req.Severity, req.Frequency, req.StrategicMult)
	if err != nil {
		mapError(w, err)
		return
	}
	// Indexed immediately so the next filer sees this one. A failure here costs
	// detection quality, not the complaint, so it is logged rather than fatal.
	if err := s.Store.UpsertEntityText(r.Context(), store.KindComplaint, c.ID, req.ProjectID,
		req.Title, req.Body); err != nil {
		_ = s.Store.AddAudit(r.Context(), req.ProjectID, getActorID(r), "embed_failed", "complaint", c.ID, err.Error())
	}
	_ = s.Store.AddAudit(r.Context(), req.ProjectID, getActorID(r), "create_complaint", "complaint", c.ID, req.Title)
	if len(sims) > 0 {
		// Recording that a filer was warned, and filed anyway, is what makes the
		// threshold arguable with evidence rather than intuition.
		_ = s.Store.AddAudit(r.Context(), req.ProjectID, getActorID(r), "duplicate_warning_shown",
			"complaint", c.ID, fmt.Sprintf("%d similar entries, top score %.3f", len(sims), sims[0].Score))
	}
	_ = s.Store.AddReputation(r.Context(), req.ProjectID, getActorID(r), "submit_complaint", 5.0)
	// Taking part is what makes someone a participant; see JoinProject.
	_ = s.Store.JoinProject(r.Context(), req.ProjectID, getActorID(r))
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleListComplaints(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	complaints, err := s.Store.ListComplaints(r.Context(), projectID, status)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, complaints)
}

func (s *Server) handleGetComplaint(w http.ResponseWriter, r *http.Request) {
	// requireProjectID checks the caller's access to the project named in the
	// path. It was missing here, so a complaint of a private project was readable
	// by anyone who counted to its id.
	//
	// The ParseInt error was also discarded (`id, _ :=`): a non-numeric id became
	// id 0 and asked the store about a row that does not exist, which is a 404
	// for the wrong reason. It is now ErrInvalid, which is what it is.
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	c, err := s.Store.GetComplaint(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	// The complaint must belong to the project in the path, or one project's
	// complaint is served under another's URL.
	if c.ProjectID != projectID {
		mapError(w, store.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleAddImpact(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	severity, _ := strconv.Atoi(r.URL.Query().Get("severity"))
	if severity == 0 {
		severity = 1
	}
	if err := s.Store.AddImpact(r.Context(), id, getActorID(r), severity); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "impact recorded"})
}

func (s *Server) handleValidateComplaint(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	if err := s.Store.ValidateComplaint(r.Context(), id); err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), 0, getActorID(r), "validate_complaint", "complaint", id, "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "validated"})
}

func (s *Server) handleMergeComplaints(w http.ResponseWriter, r *http.Request) {
	sourceID, _ := strconv.ParseInt(chi.URLParam(r, "source_id"), 10, 64)
	targetID, _ := strconv.ParseInt(chi.URLParam(r, "target_id"), 10, 64)
	if err := s.Store.MergeComplaints(r.Context(), sourceID, targetID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "merged"})
}

type createFeatureRequest struct {
	ProjectID        int64   `json:"project_id"`
	Title            string  `json:"title"`
	Body             string  `json:"body"`
	Effort           string  `json:"effort"`
	Impact           *int    `json:"impact"`
	EffortScore      *int    `json:"effort_score"`
	LinkedComplaints []int64 `json:"linked_complaints"`

	// ConfirmDuplicate, as on a complaint: a feature that restates an existing one
	// is usually a sign the existing one is not linked to a complaint, so the
	// filer may legitimately proceed.
	ConfirmDuplicate bool `json:"confirm_duplicate"`
}

func (s *Server) handleCreateFeature(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	var req createFeatureRequest
	if !readJSON(w, r, &req) {
		return
	}
	sims, proceed := s.checkDuplicates(w, r, store.KindFeature,
		req.Title, req.Body, req.ProjectID, req.ConfirmDuplicate)
	if !proceed {
		return
	}

	f, err := s.Store.CreateFeature(r.Context(), req.ProjectID, getActorID(r),
		req.Title, req.Body, req.Effort, req.Impact, req.EffortScore, req.LinkedComplaints)
	if err != nil {
		mapError(w, err)
		return
	}
	if err := s.Store.UpsertEntityText(r.Context(), store.KindFeature, f.ID, req.ProjectID,
		req.Title, req.Body); err != nil {
		_ = s.Store.AddAudit(r.Context(), req.ProjectID, getActorID(r), "embed_failed", "feature", f.ID, err.Error())
	}
	_ = s.Store.AddAudit(r.Context(), req.ProjectID, getActorID(r), "create_feature", "feature", f.ID, req.Title)
	if len(sims) > 0 {
		_ = s.Store.AddAudit(r.Context(), req.ProjectID, getActorID(r), "duplicate_warning_shown",
			"feature", f.ID, fmt.Sprintf("%d similar entries, top score %.3f", len(sims), sims[0].Score))
	}
	_ = s.Store.AddReputation(r.Context(), req.ProjectID, getActorID(r), "submit_feature", 5.0)
	_ = s.Store.JoinProject(r.Context(), req.ProjectID, getActorID(r))
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) handleListFeatures(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	features, err := s.Store.ListFeatures(r.Context(), projectID, status)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, features)
}

func (s *Server) handleFeaturePriorities(w http.ResponseWriter, r *http.Request) {
	// Deliberately public. This is the output of a process the public is
	// invited to take part in; requiring a token to read the result while
	// anyone may read the proposals would make the site a black box. The
	// private act is casting a vote, not reading the ranking it produces.
	//
	// {project_id} is a slug, not a numeric id. This used to parse it as an
	// integer, discard the error, and then pass that 0 to the ranking query
	// while the project lookup immediately below did the right thing and
	// produced proj.ID. Every caller with a non-numeric slug therefore ranked
	// project 0. Resolve the project once and use its real id.
	proj, err := s.Store.GetProject(r.Context(), chi.URLParam(r, "project_id"))
	if err != nil {
		mapError(w, err)
		return
	}
	charter, err := s.Store.GetCharterForProject(r.Context(), proj.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	priorities, err := s.Store.GetFeaturePriorities(r.Context(), proj.ID, charter)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, priorities)
}

// handleListFeatureComplaints returns the validated complaints behind a feature.
//
// This is the READ side of linked_complaints, which until now was write-only: the
// ids were accepted on create and stored in feature_complaints, and no route ever
// read them back. So the evidence for a feature's rank existed in the database and
// in no response -- which is why the feature page's complaints section showed
// nothing and read as "this feature has no pain behind it" rather than as missing.
//
// Both checks are load-bearing and neither substitutes for the other:
//
//   - requireProjectID, so a caller with no access to the project gets nothing;
//   - the ownership check, so a feature is not read through another project's URL.
//     It is the same pair handleGetFeature needs, and the same reason it survived
//     mutation testing when only the first was present.
//
// store.GetFeatureComplaints already existed; only the route and handler did not.
func (s *Server) handleListFeatureComplaints(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	f, err := s.Store.GetFeature(r.Context(), id)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}
	if f.ProjectID != projectID {
		mapError(w, store.ErrNotFound)
		return
	}
	complaints, err := s.Store.GetFeatureComplaints(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	// No nil guard here: store.GetFeatureComplaints returns an empty slice, never
	// nil, so the response is always a JSON array. The nil-guard that used to be
	// in this handler was treating a symptom the store now prevents at the source.
	writeJSON(w, http.StatusOK, complaints)
}

func (s *Server) handleGetFeature(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	f, err := s.Store.GetFeature(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}

	// Two checks, and both are load-bearing.
	//
	// The project one closes an enumeration hole: this handler read the row by its
	// numeric id alone, so an anonymous caller could read any feature of any private
	// project by counting. The list endpoint beside it was already guarded, so the
	// API answered 404 for a private project's features and 200 for the same
	// project's feature 1 -- the list hidden, its contents not.
	//
	// The ownership one stops feature A being served under project B's URL, which
	// is the same class of mistake handleGetSolution guards against for solutions.
	if f.ProjectID != projectID {
		mapError(w, store.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// handleSetStrategicWeight now opens a PROPOSAL rather than applying the change
// (spec revision 4 §6.2).
//
// It used to require the maintainer role and write the weight immediately.
// strategic_weight multiplies into the priority formula via Mu, so that was a
// steering lever: a maintainer could pin a feature to the top of the roadmap by
// hand, which §5.3 forbids outright ("no role can steer by hand in collective
// mode"). The route is kept at the same path so callers get a proposal rather
// than a silent no-op, and the response says which proposal to consent to.
//
// Any member with contributor or above may propose. Eligibility for ratifying
// is separate and is counted inside RatifyStrategicWeightProposal, so the gate
// lives in one place rather than being restated here.
func (s *Server) handleSetStrategicWeight(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWriteActor(w, r); !ok {
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(projectID, "contributor", r); err != nil {
		mapError(w, err)
		return
	}
	var req struct {
		Weight    float64 `json:"weight"`
		Rationale string  `json:"rationale"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	// The store layer requires a rationale; this message names the field rather
	// than letting a bare "invalid" be the whole answer.
	if strings.TrimSpace(req.Rationale) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "a rationale is required: a weight change is a steering decision, so it must say why",
		})
		return
	}
	p, err := s.Store.ProposeStrategicWeight(r.Context(), projectID, id, getActorID(r), req.Weight, req.Rationale)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "propose_strategic_weight", "feature", id,
		fmt.Sprintf("proposal %d: weight=%g", p.ID, req.Weight))
	writeJSON(w, http.StatusCreated, map[string]any{
		"proposal": p,
		"message":  "strategic weight changes are ratified by consensus; consent to proposal " + strconv.FormatInt(p.ID, 10),
	})
}

// handleConsentStrategicWeight casts a consent on a pending weight proposal.
// Ratification happens inside the store once the charter thresholds are met, so
// a partial consent is a normal 200 rather than an error.
func (s *Server) handleConsentStrategicWeight(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWriteActor(w, r); !ok {
		return
	}
	proposalID, _ := strconv.ParseInt(chi.URLParam(r, "proposal_id"), 10, 64)
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	// Contributors and above are the eligible population (§8.2); ratifying
	// needs no higher role than proposing, because a proposal is a proposal.
	if err := s.requireRole(projectID, "contributor", r); err != nil {
		mapError(w, err)
		return
	}
	p, err := s.Store.RatifyStrategicWeightProposal(r.Context(), proposalID, getActorID(r))
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) || errors.Is(err, store.ErrProposalStale) {
			// Not enough consents yet, or the target moved. Both are ordinary
			// intermediate states of a consensus process, so the current state
			// is returned with 202 rather than an error the client retries.
			writeJSON(w, http.StatusAccepted, map[string]any{
				"proposal": p,
				"message":  err.Error(),
			})
			return
		}
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "ratify_strategic_weight", "feature", p.FeatureID,
		fmt.Sprintf("proposal %d ratified", proposalID))
	writeJSON(w, http.StatusOK, map[string]any{"proposal": p, "status": "ratified"})
}

// handleListStrategicWeightProposals returns the project's weight proposals.
func (s *Server) handleListStrategicWeightProposals(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	props, err := s.Store.ListStrategicWeightProposals(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, props)
}

// handlePlaceEmergencyHold suspends a consensus call (spec revision 4 §6.6).
//
// This is the emergency hold, NOT the veto the older spec described as an
// override for blocks. It cannot cancel a block, force an outcome, or close a
// call -- it refuses to let a call resolve, expires on its own, and triggers a
// confirmation vote. A maintainer reaching for this to win an argument finds
// that it does nothing except make the argument public.
func (s *Server) handlePlaceEmergencyHold(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWriteActor(w, r); !ok {
		return
	}
	callID, _ := strconv.ParseInt(chi.URLParam(r, "call_id"), 10, 64)
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	// §8.1 limits the maintainer role to "run security embargo, emergency hold",
	// so this is the only content-level power a maintainer keeps.
	if err := s.requireRole(projectID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	var req struct {
		Grounds string `json:"grounds"`
		Reason  string `json:"reason"`
		Days    int    `json:"days"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Grounds != "security" && req.Grounds != "legal" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "grounds must be \"security\" or \"legal\": the emergency power is not for ordinary disagreement",
		})
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "a written reason is required: a hold nobody can review is not accountable",
		})
		return
	}
	call, err := s.Store.GetConsensusCall(r.Context(), callID)
	if err != nil {
		mapError(w, err)
		return
	}
	hold, err := s.Store.PlaceEmergencyHold(r.Context(), call.ProjectID, callID,
		getActorID(r), req.Grounds, req.Reason, req.Days)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"hold": hold,
		"note": "this suspends the call only; it cannot override a block, force a result, or close the call. It expires, and expiry opens a confirmation vote.",
	})
}

// handleReleaseEmergencyHold lifts a hold early, recording why.
func (s *Server) handleReleaseEmergencyHold(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWriteActor(w, r); !ok {
		return
	}
	holdID, _ := strconv.ParseInt(chi.URLParam(r, "hold_id"), 10, 64)
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(projectID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.ReleaseEmergencyHold(r.Context(), holdID, getActorID(r), req.Reason); err != nil {
		mapError(w, err)
		return
	}
	hold, err := s.Store.GetEmergencyHold(r.Context(), holdID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hold": hold, "status": "released"})
}

// handleGetCallHold returns the hold on a call, if any, so a client can show
// why a call is not resolving.
func (s *Server) handleGetCallHold(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireProjectID(w, r); !ok {
		return
	}
	callID, _ := strconv.ParseInt(chi.URLParam(r, "call_id"), 10, 64)
	hold, err := s.Store.ActiveHoldForCall(r.Context(), callID)
	if err != nil {
		mapError(w, err)
		return
	}
	if hold == nil {
		writeJSON(w, http.StatusOK, map[string]any{"hold": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hold": hold})
}

// handleListAdminLedger serves the public admin-action ledger (§9.3).
//
// No project filter and no role gate, on purpose: §9.3 makes admin actions
// publicly visible because an admin with no content authority is only a real
// constraint when anyone can watch them.
func (s *Server) handleListAdminLedger(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	entries, err := s.Store.ListAdminLedger(r.Context(), limit)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// ---------------------------------------------------------------- duplicates (§6.1)

// checkDuplicates finds near-duplicates for a filing and decides whether to
// refuse it.
//
// §6.1 calls duplicate detection advisory, so the split is deliberate:
//
//   - a strong match (>= 0.85) returns 409 and the filer must confirm, because
//     at that score it is almost always the same report filed twice;
//   - a plausible match (>= 0.62) is returned as a hint and the filing proceeds,
//     because a wrong refusal loses a real complaint and the cost of that is
//     higher than the cost of a duplicate in the queue.
//
// Either way the candidates are returned in the response body, so a client that
// ignores the status code still learns what it collided with.
//
// Returns (nil, false, nil) when no embedder is configured or the index is empty:
// unavailable duplicate detection must not block filing.
func (s *Server) checkDuplicates(w http.ResponseWriter, r *http.Request, kind, title, body string, projectID int64, confirmed bool) ([]embed.Similarity, bool) {
	if s.Store.EmbedderID() == "" {
		return nil, true
	}
	ctx := r.Context()

	// Cross-project scope, and this is the §6.1 line: "Duplicate detection at
	// filing time uses semantic similarity before submit, and ALSO searches other
	// projects ('this was fixed in X')".
	//
	// The store already supports instance-wide search -- FindSimilar documents
	// projectID == 0 as "the whole instance, which is the 'this was fixed in X'
	// case" -- and this call was passing the current project, so a verbatim copy of
	// a complaint filed elsewhere was invisible: same text, score 1.0, no match.
	// The comment above the store method described behaviour the handler did not
	// use, which is the worst kind of drift.
	//
	// Instance-wide rather than per-project, so one query covers both the likelier
	// same-project duplicate and the §6.1 cross-project gift. Each result carries
	// its own project_id, so a filer can tell which is which.
	//
	// 0 is the store's documented "whole instance" sentinel, not a missing
	// argument: FindSimilarField reads projectID > 0 as a filter and anything else
	// as no filter.
	scope := int64(0)

	// Two queries, two fields.
	//
	// The title is checked on its own because that is the strongest single signal
	// and the thing a filer is actually looking at. Measured with two complaints
	// sharing a title and differing bodies:
	//
	//	title query   vs title vector      1.00
	//	title+body    vs title+body        0.58
	//
	// Concatenating both sides buried the title under body words and scored a
	// literal duplicate below the advisory threshold -- so it was filed twice with
	// no warning. The full-text query is still run, because two complaints can
	// share a vague title and an identical description, which the title alone
	// would miss.
	sims, err := s.Store.FindSimilarField(ctx, kind, title, scope, store.FieldTitle, 5)
	if err != nil {
		// A failure to check must not block the filing: refusing on an internal
		// error would lose a real complaint to a bug in the similarity code.
		return nil, true
	}
	if len(sims) == 0 && strings.TrimSpace(body) != "" {
		if full, ferr := s.Store.FindSimilarField(ctx, kind, title+" "+body, scope, store.FieldFull, 5); ferr == nil {
			sims = full
		}
	}
	if len(sims) == 0 {
		return nil, true
	}
	strongThr, _, _ := embed.ThresholdsFor(s.Store.EmbedderID())
	strong := embed.IsStrongDuplicateFor(sims[0].Score, s.Store.EmbedderID())
	if strong && !confirmed {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":         "an almost identical entry already exists",
			"similar":       sims,
			"top_score":     sims[0].Score,
			"threshold":     strongThr,
			"resubmit_with": map[string]any{"confirm_duplicate": true},
			"note":          "if this is a genuinely different problem, resubmit with confirm_duplicate=true; the existing entry stays untouched either way",
		})
		return sims, false
	}
	return sims, true
}

// mergeSimilarities unions two similarity lists, keeping the higher score per
// entity.
//
// A union rather than a concatenation: the same complaint appears in both the
// title query and the full-text query, and listing it twice would pad the panel
// with the same entry and make the count meaningless.
func mergeSimilarities(a, b []embed.Similarity, limit int) []embed.Similarity {
	best := make(map[int64]embed.Similarity, len(a)+len(b))
	for _, s := range append(append([]embed.Similarity{}, a...), b...) {
		if existing, ok := best[s.EntityID]; !ok || s.Score > existing.Score {
			best[s.EntityID] = s
		}
	}
	out := make([]embed.Similarity, 0, len(best))
	for _, s := range best {
		out = append(out, s)
	}
	return embed.Rank(out, limit)
}

// handleFindSimilar answers "does this already exist?" before anything is
// filed, which is the workflow §6.1 describes: the panel appears while the filer
// is still writing, not after they press submit.
//
// Scope: ?project_id= searches one project; omit it (or pass 0) to search the
// whole instance, which is the "this was fixed in X" case.
func (s *Server) handleFindSimilar(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	switch kind {
	case store.KindComplaint, store.KindFeature, store.KindRequest, store.KindProject:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "kind must be one of complaint, feature, request, project",
		})
		return
	}
	if s.Store.EmbedderID() == "" {
		// Reported as its own state rather than an empty result: an empty list
		// would read as "nothing similar exists".
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"kind":      kind,
			"similar":   []embed.Similarity{},
			"note":      "duplicate detection is not configured on this instance",
		})
		return
	}

	text := r.URL.Query().Get("text")
	if strings.TrimSpace(text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "text is required",
		})
		return
	}
	var projectID int64
	if v := r.URL.Query().Get("project_id"); v != "" {
		projectID, _ = strconv.ParseInt(v, 10, 64)
	}
	limit := 5
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	// The panel queries the title, because that is what a filer has typed while
	// the panel is useful. Passing the whole text against the full-text vector
	// dilutes the title signal -- measured 1.00 for title-to-title against 0.58
	// for the concatenated pair -- so the panel would miss exact duplicates.
	//
	// Best score over both fields is kept, for the case where the filer has
	// written a description that matches an existing complaint whose title
	// differs.
	sims, err := s.Store.FindSimilarField(r.Context(), kind, text, projectID, store.FieldTitle, limit)
	if err != nil {
		mapError(w, err)
		return
	}
	if full, ferr := s.Store.FindSimilarField(r.Context(), kind, text, projectID, store.FieldFull, limit); ferr == nil {
		sims = mergeSimilarities(sims, full, limit)
	}
	if sims == nil {
		sims = []embed.Similarity{}
	}

	// Bands travel with the answer, and they are per model: nomic's paraphrase
	// scores run 0.61-0.79 where the hashed embedder's reach 0.37, so a client
	// that hardcodes one set of bands misreads the other.
	thrStrong, thrLikely, thrWeak := embed.ThresholdsFor(s.Store.EmbedderID())

	resp := map[string]any{
		"available":     true,
		"kind":          kind,
		"model_id":      s.Store.EmbedderID(),
		"similar":       sims,
		"has_duplicate": len(sims) > 0 && embed.IsStrongDuplicateFor(sims[0].Score, s.Store.EmbedderID()),
		"thresholds": map[string]any{
			"strong": thrStrong,
			"likely": thrLikely,
			"weak":   thrWeak,
		},
	}
	// Coverage travels with the answer. A filer told "no duplicates" on a corpus
	// that is 12% indexed has been told nothing useful, and this is the only
	// place they can find out.
	if cov, err := s.Store.EmbeddingCoverage(r.Context(), kind); err == nil {
		resp["coverage"] = cov
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------- taxonomy (§4.3)

type taxonomyRequest struct {
	Action    string `json:"action"`
	TargetTag string `json:"target_tag"`
	Value     string `json:"value"`
	Rationale string `json:"rationale"`
	// ProjectID scopes the change. 0 (or omitted) means instance-wide, which is
	// the case §4.3 puts squarely under quorum: maintainers hold no unilateral
	// taxonomy power.
	ProjectID int64 `json:"project_id"`
}

// handleProposeTaxonomyChange opens a taxonomy proposal (§4.3).
//
// Anyone with contributor or above may propose. Who can ratify is decided inside
// the store layer, so the gate lives in one place.
func (s *Server) handleProposeTaxonomyChange(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWriteActor(w, r); !ok {
		return
	}
	var req taxonomyRequest
	if !readJSON(w, r, &req) {
		return
	}
	actor := getActorID(r)
	// A project-scoped proposal requires membership of that project. An
	// instance-wide one requires contributor somewhere, since the change affects
	// every project.
	if req.ProjectID > 0 {
		if err := s.requireRole(req.ProjectID, "contributor", r); err != nil {
			mapError(w, err)
			return
		}
	} else if !s.isContributorSomewhere(r, actor) {
		mapError(w, store.ErrPerm)
		return
	}
	p, err := s.Store.ProposeTaxonomyChange(r.Context(), req.ProjectID, actor,
		req.Action, req.TargetTag, req.Value, req.Rationale)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"proposal": p,
		"message":  "taxonomy changes are ratified by quorum; consent to this proposal to proceed",
	})
}

// isContributorSomewhere reports whether an actor holds contributor or above in
// any project.
func (s *Server) isContributorSomewhere(r *http.Request, actorID int64) bool {
	if actorID == 0 {
		return false
	}
	role, err := s.Store.GetHighestRole(r.Context(), actorID)
	if err != nil {
		return false
	}
	rank, ok := roleHierarchy[role]
	return ok && rank >= roleHierarchy["contributor"]
}

// handleConsentTaxonomyProposal consents to a pending taxonomy proposal.
func (s *Server) handleConsentTaxonomyProposal(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWriteActor(w, r); !ok {
		return
	}
	proposalID, _ := strconv.ParseInt(chi.URLParam(r, "proposal_id"), 10, 64)
	p, err := s.Store.RatifyTaxonomyProposal(r.Context(), proposalID, getActorID(r))
	if err != nil {
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrPerm) {
			// Below quorum, or the proposer trying to consent to their own
			// proposal. Both are ordinary states of a consensus process, so the
			// current state comes back with 202 rather than an error to retry.
			writeJSON(w, http.StatusAccepted, map[string]any{"proposal": p, "message": err.Error()})
			return
		}
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposal": p, "status": "ratified"})
}

// handleListTaxonomyProposals lists proposals for a project, including the
// instance-wide ones (project_id absent means all).
func (s *Server) handleListTaxonomyProposals(w http.ResponseWriter, r *http.Request) {
	var projectID int64
	if v := r.URL.Query().Get("project_id"); v != "" {
		projectID, _ = strconv.ParseInt(v, 10, 64)
	}
	props, err := s.Store.ListTaxonomyProposals(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, props)
}

// handleListTags backs the suggest-before-create UI (§4.3) and the
// cap:/tag: filter facets.
func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.Store.ListTags(r.Context(),
		r.URL.Query().Get("namespace"),
		r.URL.Query().Get("include_suggested") == "true")
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tags)
}

// handleTagCompleteness returns the §4.3 completeness meter for a project.
func (s *Server) handleTagCompleteness(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	out, err := s.Store.TagCompleteness(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListStrategicThemes returns the project's live (unexpired) themes (§6.2).
func (s *Server) handleListStrategicThemes(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	themes, err := s.Store.ListStrategicThemes(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, themes)
}

type castVoteRequest struct {
	FeatureA int64   `json:"feature_a"`
	FeatureB int64   `json:"feature_b"`
	Outcome  string  `json:"outcome"`
	Weight   float64 `json:"weight"`
}

func (s *Server) handleCastVote(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	// A first vote enrols the voter, so the guest check cannot run before it.
	// The order matters and was wrong at first: rejecting guests here made the
	// very first vote impossible, because nobody is a member until they have
	// voted or contributed. The one thing a guest must not do is vote *before*
	// being let in, and JoinProject is what lets them in.
	if err := s.Store.JoinProject(r.Context(), projectID, actorID); err != nil {
		mapError(w, err)
		return
	}
	var req castVoteRequest
	if !readJSON(w, r, &req) {
		return
	}
	// Validate both features belong to the project
	a, err := s.Store.GetFeature(r.Context(), req.FeatureA)
	if err != nil {
		mapError(w, err)
		return
	}
	b, err := s.Store.GetFeature(r.Context(), req.FeatureB)
	if err != nil {
		mapError(w, err)
		return
	}
	if a.ProjectID != projectID || b.ProjectID != projectID {
		mapError(w, store.ErrPerm)
		return
	}
	// Refuse a vote on your own feature, in either position. Glicko-2 cannot
	// discount self-preference: a rating inflated by its own author's vote is
	// not a measurement of anyone else's preference, and the author may vote
	// for the same feature against every competitor, without limit. Both
	// features are checked because the pair is unordered as presented.
	//
	// The message names the reason. A bare "permission denied" for an action
	// the interface offered is indistinguishable from a bug to whoever hit it.
	for _, fid := range []int64{req.FeatureA, req.FeatureB} {
		isAuthor, err := s.Store.IsFeatureAuthor(r.Context(), fid, actorID)
		if err != nil {
			mapError(w, err)
			return
		}
		if isAuthor {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "you proposed one of these features",
				"detail": "a feature cannot be ranked by the person who proposed it; ask someone who has no stake in the outcome",
			})
			return
		}
	}
	// Load the project's charter for Glicko-2 tau and vote weight cap
	charter, err := s.Store.GetCharterForProject(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	// Compute weight server-side from voter reputation — never trust client-supplied weight
	voterReputation, _ := s.Store.GetReputation(r.Context(), projectID, actorID)
	weight := ranking.VoteWeight(voterReputation, 1.0, charter.VoteWeightCap)
	// Ramp a new account's weight from zero over its first week (spec §12.3).
	// A throwaway account cannot buy influence; a real new contributor is not
	// locked out of the vote it came to cast.
	createdAt, err := s.Store.GetUserCreatedAt(r.Context(), actorID)
	if err == nil {
		age := ranking.AccountAgeDays(createdAt, float64(time.Now().Unix()))
		weight *= ranking.AgeGateMultiplier(age)
	}
	vote, err := s.Store.RecordVote(r.Context(), projectID, actorID, req.FeatureA, req.FeatureB, req.Outcome, weight, charter)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, actorID, "cast_vote", "vote", projectID, fmt.Sprintf("feature_a=%d feature_b=%d outcome=%s", req.FeatureA, req.FeatureB, req.Outcome))
	_ = s.Store.AddReputation(r.Context(), projectID, actorID, "vote", 1.0)
	// Enrol only after the vote has been recorded. Enrolling someone whose
	// vote was refused would record a participation that did not happen.
	_ = s.Store.JoinProject(r.Context(), projectID, actorID)
	writeJSON(w, http.StatusCreated, vote)
}

// handleJoinProject enrols the caller as a contributor.
//
// Almost nobody needs this: filing a complaint, proposing a feature and voting
// all enrol you as a side effect, which is the intended path. It exists for
// the person who wants to vote before contributing anything — to rank a
// backlog they did not write — and it is deliberately idempotent, so calling it
// repeatedly never demotes a maintainer.
func (s *Server) handleJoinProject(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	role, _ := s.Store.GetRoleForProject(r.Context(), projectID, actorID)
	// A guest and a member get the same 200: the caller's goal is "I can take
	// part", and telling a non-member that they were already enrolled would
	// leak membership state for no benefit.
	_ = role
	if err := s.Store.JoinProject(r.Context(), projectID, actorID); err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, actorID, "join_project", "project", projectID, "")
	after, _ := s.Store.GetRoleForProject(r.Context(), projectID, actorID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "joined", "role": after})
}

// handleListMembers returns the project's membership.
//
// Public, like the ranking. The roster answers "who is taking part", which is
// the first question anyone asks of a community, and a project whose
// membership is hidden reads as empty.
// handleFeatureTallies returns how much attention each feature has received.
//
// Public, like the ranking. A rating without a count beside it is not
// interpretable: 1500 from four votes and 1500 from four hundred are the same
// number and mean very different things.
func (s *Server) handleFeatureTallies(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	tallies, err := s.Store.FeatureTallies(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	if tallies == nil {
		tallies = []store.FeatureTally{}
	}
	writeJSON(w, http.StatusOK, tallies)
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	members, err := s.Store.ListMembers(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	if members == nil {
		members = []store.Member{}
	}
	writeJSON(w, http.StatusOK, members)
}

func (s *Server) handleGetNextPair(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	// The same check handleCastVote applies, and for the same reason: this
	// endpoint used to serve a comparison to any authenticated account, which
	// was then refused with 403 when it tried to answer. The API was inviting
	// a vote it had already decided to reject. Better to refuse here, before
	// the pair is shown, than after someone has read both descriptions.
	if err := s.Store.JoinProject(r.Context(), projectID, actorID); err != nil {
		mapError(w, err)
		return
	}
	a, b, err := s.Store.GetNextPair(r.Context(), projectID, actorID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feature_a": a, "feature_b": b})
}

type createConsensusRequest struct {
	FeatureID   int64  `json:"feature_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

func (s *Server) handleCreateConsensus(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	var req createConsensusRequest
	if !readJSON(w, r, &req) {
		return
	}
	// openedBy is the authenticated caller. It used to be hardcoded to 1, so
	// every consensus call in the database claimed user 1 opened it.
	call, err := s.Store.CreateConsensusCall(r.Context(), projectID, req.FeatureID, getActorID(r), req.Title, req.Description)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "create_consensus_call", "consensus_call", call.ID, req.Title)
	_ = s.Store.AddReputation(r.Context(), projectID, getActorID(r), "create_consensus_call", 3.0)
	writeJSON(w, http.StatusCreated, call)
}

// requireCallInProject resolves BOTH the project in the path and the call in the
// path, and refuses unless the caller may read the project.
//
// Every consensus handler here used to parse call_id and load the call by id alone.
// That left two holes, both measured in
// TestAConsensusCallCannotBeReadThroughAnotherProjectsURL:
//
//   - a private project's call was readable by anyone who could guess a small
//     integer, because nothing asked whether the caller may read the project;
//   - a call in project A was readable through project B's URL, so the project
//     segment of the path was decorative -- naming any project at all sufficed.
//
// The project check is requireProjectID, the same guard every other project-scoped
// handler uses, so a private project stays indistinguishable from a nonexistent one:
// a generic 404 body naming no slug.
//
// handleGetConsensus additionally compares the call's project to this one. This
// helper does not, because a write's own store call is what surfaces the mismatch;
// doing the check in one place for reads and in another for writes is how the two
// drift apart. handleRecordCallOutcome already did both by hand and is left alone.
func (s *Server) requireCallInProject(w http.ResponseWriter, r *http.Request) (callID, projectID int64, ok bool) {
	projectID, ok = s.requireProjectID(w, r)
	if !ok {
		return 0, 0, false
	}
	callID, _ = strconv.ParseInt(chi.URLParam(r, "call_id"), 10, 64)
	if callID <= 0 {
		// A non-numeric or missing id is "not found", not a 400: the caller learns
		// nothing about which ids exist.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return 0, 0, false
	}
	return callID, projectID, true
}

func (s *Server) handleGetConsensus(w http.ResponseWriter, r *http.Request) {
	callID, projectID, ok := s.requireCallInProject(w, r)
	if !ok {
		return
	}
	c, err := s.Store.GetConsensusCall(r.Context(), callID)
	if err != nil {
		mapError(w, err)
		return
	}
	if c.ProjectID != projectID {
		// The call exists but not under this project, so the answer is the same 404
		// as for a call that does not exist. Anything else confirms the id is real.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	positions, err := s.Store.GetPositions(r.Context(), callID)
	if err != nil {
		mapError(w, err)
		return
	}
	objections, err := s.Store.GetObjections(r.Context(), callID)
	if err != nil {
		mapError(w, err)
		return
	}
	// The tally, computed by the SAME method a close uses. The page must not derive
	// these numbers: §6.6 requires two ratios with different denominators, and a
	// client that recomputes them can be wrong about what consent means without
	// anything looking broken. store.ConsensusThresholdsSummary is what makes both
	// visible in one response; it existed with no caller until this.
	counts, err := s.Store.TallyConsensus(r.Context(), c)
	if err != nil {
		mapError(w, err)
		return
	}
	var modelStr string
	if err := s.Store.QueryRowContext(r.Context(),
		`SELECT governance_model FROM projects WHERE id = ?`, c.ProjectID).Scan(&modelStr); err != nil {
		mapError(w, err)
		return
	}
	gm := governance.GovernanceModel(modelStr)
	if !gm.Valid() {
		gm = governance.Collective
	}
	// §6.6's HIDDEN TALLY: "Running counts are hidden until the call closes, to
	// prevent bandwagoning. Participation progress and the quorum bar remain
	// visible."
	//
	// The first version of this page ignored that and showed live counts and ratios,
	// which is the bandwagon the rule exists to prevent. It is enforced HERE, on the
	// server, and not in consensus.js: a client-side rule is bypassed by devtools,
	// which defeats it instead of implementing it.
	//
	// What stays visible is exactly what §6.6 preserves -- participation and the
	// quorum bar -- plus the caller's OWN position, because the rule hides the tally,
	// not a voter's own act. Nothing else leaks a stance.
	tally := store.ConsensusThresholdsSummary(counts, governance.DefaultCharter(gm))
	payload := map[string]any{
		"call":       c,
		"objections": objections,
		"tally":      tally,
	}
	if c.Status == "closed" {
		payload["tally_visible"] = true
		payload["positions"] = positions
	} else {
		payload["tally_visible"] = false
		payload["positions"] = ownPositionsOnly(positions, getActorID(r))
		for k := range tally {
			switch k {
			case "participants", "eligible", "quorum_required":
				// Kept: §6.6 preserves the quorum bar.
			default:
				tally[k] = nil
			}
		}
	}
	writeJSON(w, http.StatusOK, payload)
}

// ownPositionsOnly reduces a call's positions to the caller's own row.
//
// An empty slice rather than nil, so the JSON is `[]` and a client iterating it does
// not have to handle null. A caller with no position gets an empty list, which is
// also what "you have not voted" looks like.
func ownPositionsOnly(positions []store.Position, actorID int64) []store.Position {
	out := []store.Position{}
	if actorID == 0 {
		return out
	}
	for _, p := range positions {
		if p.UserID == actorID {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) handleCastConsensusPosition(w http.ResponseWriter, r *http.Request) {
	callID, projectID, ok := s.requireCallInProject(w, r)
	if !ok {
		return
	}
	_ = projectID
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	var req struct {
		Position string `json:"position"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	pos, err := s.Store.CastPosition(r.Context(), callID, getActorID(r), req.Position)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), 0, getActorID(r), "cast_position", "position", callID, fmt.Sprintf("stance=%s", req.Position))
	writeJSON(w, http.StatusCreated, pos)
}

func (s *Server) handleCreateObjection(w http.ResponseWriter, r *http.Request) {
	callID, projectID, ok := s.requireCallInProject(w, r)
	if !ok {
		return
	}
	_ = projectID
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	var req struct {
		Principle string `json:"principle"`
		Violation string `json:"violation"`
		Remedy    string `json:"remedy"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	obj, err := s.Store.CreateObjection(r.Context(), callID, getActorID(r), req.Principle, req.Violation, req.Remedy)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), 0, getActorID(r), "create_objection", "objection", obj.ID, req.Principle)
	writeJSON(w, http.StatusCreated, obj)
}

func (s *Server) handleResolveObjection(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	// Load objection to get project via call
	obj, err := s.Store.GetObjection(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	call, err := s.Store.GetConsensusCall(r.Context(), obj.CallID)
	if err != nil {
		mapError(w, err)
		return
	}
	if err := s.requireRole(call.ProjectID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	var req struct {
		Status     string `json:"status"`
		Resolution string `json:"resolution"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.ResolveObjection(r.Context(), id, req.Status, req.Resolution); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

func (s *Server) handleCloseConsensus(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	callID, _, ok := s.requireCallInProject(w, r)
	if !ok {
		return
	}
	summary, err := s.Store.CloseConsensusCall(r.Context(), callID)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), summary.Call.ProjectID, getActorID(r), "close_consensus", "consensus_call", callID, summary.Result)
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleGetBoard(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	columns, cards, err := s.Store.GetBoard(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"columns": columns, "cards": cards})
}

// handleMoveCard places a card in a phase.
//
// The card is named two ways, because a card has two kinds. A placed card is a
// board_cards row and is addressed by {card_id}. A DERIVED card -- a feature the
// project has but nobody has placed -- has no row and therefore no id, so it is
// addressed by ?feature_id=. That distinction is the whole point: before this,
// moving a derived card sent id=0, the update matched nothing, and the handler
// still answered 200, so the move was silently discarded.
//
// A phase that does not exist is a 404, not a 500: the NOT NULL constraint on
// column_id used to turn a typo in ?to= into a server fault.
func (s *Server) handleMoveCard(w http.ResponseWriter, r *http.Request) {
	cardID, _ := strconv.ParseInt(chi.URLParam(r, "card_id"), 10, 64)
	// feature_id arrives as a query parameter. Parsed rather than ignored because
	// a malformed value has to be rejected, not silently treated as absent.
	var featureID int64
	if raw := r.URL.Query().Get("feature_id"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			mapError(w, fmt.Errorf("%w: feature_id must be a number", store.ErrInvalid))
			return
		}
		featureID = v
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	newColumn := r.URL.Query().Get("to")
	if newColumn == "" {
		mapError(w, fmt.Errorf("%w: to is required", store.ErrInvalid))
		return
	}
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	if err := s.Store.MoveCard(r.Context(), cardID, featureID, newColumn, projectID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "moved"})
}

func (s *Server) handleCreateMergeRequest(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	var req struct {
		FeatureID int64  `json:"feature_id"`
		Title     string `json:"title"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	mr, err := s.Store.CreateMergeRequest(r.Context(), projectID, req.FeatureID, getActorID(r), req.Title)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddReputation(r.Context(), mr.ProjectID, getActorID(r), "create_merge_request", 3.0)
	writeJSON(w, http.StatusCreated, mr)
}

func (s *Server) handleApproveMerge(w http.ResponseWriter, r *http.Request) {
	mrID, _ := strconv.ParseInt(chi.URLParam(r, "mr_id"), 10, 64)
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	if err := s.Store.ApproveMerge(r.Context(), mrID, getActorID(r)); err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), 0, getActorID(r), "approve_merge", "merge_request", mrID, "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

func (s *Server) handleExecuteMerge(w http.ResponseWriter, r *http.Request) {
	mrID, _ := strconv.ParseInt(chi.URLParam(r, "mr_id"), 10, 64)
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	if err := s.Store.ExecuteMerge(r.Context(), mrID, projectID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "merged"})
}

func (s *Server) handleRejectMerge(w http.ResponseWriter, r *http.Request) {
	mrID, _ := strconv.ParseInt(chi.URLParam(r, "mr_id"), 10, 64)
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	if err := s.Store.RejectMerge(r.Context(), mrID, projectID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

// List and request handlers are in lists.go

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "index", s.pageFor(r, "Home"))
}

func (s *Server) handleSearchPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	s.render(w, http.StatusOK, "search", struct {
		pageData
		Query string
	}{s.pageFor(r, "Search"), q})
}

func (s *Server) handleProjectPage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	// The page itself is a template shell, but it still must not render for a
	// project the caller cannot see: a 200 for a private project confirms the
	// slug exists, and the title alone is enough to enumerate. The data behind
	// it is fetched from the API, which enforces visibility independently.
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		// A miss on a page route gets the styled 404, not a JSON body. The
		// status is still 404 in both branches below, and the reason string is
		// deliberately the same for "no such project" and "not yours" so the page
		// cannot be used to probe which slugs exist.
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	if !s.projectReadable(r, proj) {
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	s.render(w, http.StatusOK, "project", struct {
		pageData
		Slug string
	}{s.pageFor(r, slug), slug})
}

// handleDocumentsPage renders the document viewer for a project.
//
// The visibility check is not decoration. This page renders for anonymous
// callers, and a 200 for a private project confirms the slug exists and puts
// its name in the title bar, which is enough to enumerate the instance. The
// documents themselves are fetched from the API, which enforces visibility
// independently -- so a private project's documents 404 on the data fetch even
// though the shell rendered.
func (s *Server) handleDocumentsPage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		// A miss on a page route gets the styled 404, not a JSON body. The
		// status is still 404 in both branches below, and the reason string is
		// deliberately the same for "no such project" and "not yours" so the page
		// cannot be used to probe which slugs exist.
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	if !s.projectReadable(r, proj) {
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	s.render(w, http.StatusOK, "documents", struct {
		pageData
		Slug string
	}{s.pageFor(r, "Documents - "+slug), slug})
}

func (s *Server) handleBoardPage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	// The page itself is a template shell, but it still must not render for a
	// project the caller cannot see: a 200 for a private project confirms the
	// slug exists, and the title alone is enough to enumerate. The data behind
	// it is fetched from the API, which enforces visibility independently.
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		// A miss on a page route gets the styled 404, not a JSON body. The
		// status is still 404 in both branches below, and the reason string is
		// deliberately the same for "no such project" and "not yours" so the page
		// cannot be used to probe which slugs exist.
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	if !s.projectReadable(r, proj) {
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	s.render(w, http.StatusOK, "board", struct {
		pageData
		Slug string
	}{s.pageFor(r, "Board - "+slug), slug})
}

// handleConsensusPage renders /projects/{slug}/consensus?call=<id>.
//
// The same visibility discipline as handleRankPage, and for the same reason: a 200
// for a private project confirms the slug exists, and the title alone is enough to
// enumerate. The reason string is identical for "no such project" and "not yours",
// so the page cannot be used to probe which slugs exist.
func (s *Server) handleConsensusPage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	if !s.projectReadable(r, proj) {
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}

	// The call id is read here only so the page can tell the script which call to
	// fetch. It is NOT validated against the project: the API enforces that
	// independently (requireCallInProject), and validating it twice would let the
	// page and the API disagree about which calls exist.
	callID := r.URL.Query().Get("call")

	// `page`, NOT `pageWithScript`: consensus.html already carries its own <script>
	// tag, exactly as scout.html does for scout.js. pageWithScript would add a
	// second tag for the same file and the tally would render twice.
	s.render(w, http.StatusOK, "consensus", struct {
		pageData
		Slug   string
		CallID string
	}{s.pageFor(r, "Consensus - "+slug), slug, callID})
}

// handleRankPage and handleRankingPage render the two halves of the ranking
// loop: cast a comparison, then read the result. They share a shape and
// deliberately nothing else — the pair and the order are different questions.
func (s *Server) handleRankPage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	// The page itself is a template shell, but it still must not render for a
	// project the caller cannot see: a 200 for a private project confirms the
	// slug exists, and the title alone is enough to enumerate. The data behind
	// it is fetched from the API, which enforces visibility independently.
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		// A miss on a page route gets the styled 404, not a JSON body. The
		// status is still 404 in both branches below, and the reason string is
		// deliberately the same for "no such project" and "not yours" so the page
		// cannot be used to probe which slugs exist.
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	if !s.projectReadable(r, proj) {
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	s.render(w, http.StatusOK, "rank", struct {
		pageData
		Slug string
	}{s.pageFor(r, "Rank features - "+slug), slug})
}

func (s *Server) handleRankingPage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	// The page itself is a template shell, but it still must not render for a
	// project the caller cannot see: a 200 for a private project confirms the
	// slug exists, and the title alone is enough to enumerate. The data behind
	// it is fetched from the API, which enforces visibility independently.
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		// A miss on a page route gets the styled 404, not a JSON body. The
		// status is still 404 in both branches below, and the reason string is
		// deliberately the same for "no such project" and "not yours" so the page
		// cannot be used to probe which slugs exist.
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	if !s.projectReadable(r, proj) {
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	s.render(w, http.StatusOK, "ranking", struct {
		pageData
		Slug string
	}{s.pageFor(r, "Ranking - "+slug), slug})
}

func (s *Server) handleAuditPage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	// The page is a template shell, but it still must not render for a project
	// the caller cannot see: a 200 for a private project confirms the slug
	// exists, and the title alone is enough to enumerate. The data behind it is
	// fetched from the API, which enforces visibility independently.
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	if !s.projectReadable(r, proj) {
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	s.render(w, http.StatusOK, "audit", struct {
		pageData
		Slug string
	}{s.pageFor(r, "Audit log - "+slug), slug})
}

// handleFeaturePage renders the feature detail shell for /projects/{slug}/features/{id}.
//
// frontend-spec.md 3.1 ranks this third: a feature is what gets ranked, so it is the
// unit a reader lands on when they ask "why is this first?".
//
// The visibility check is not decoration, and it is the same rule the documents and
// audit pages follow: the shell renders for logged-out visitors, so a 200 for a
// private project confirms the slug exists and puts it in the title bar. The data
// behind the page is fetched from the API, which enforces visibility independently.
//
// The feature id is NOT looked up here. The page fetches its data client-side, so
// the server has no reason to load a row just to decide whether to render a shell,
// and a wrong id in a URL is a reader's typo rather than an attack. feature.js
// renders the "no such feature" state for it.
func (s *Server) handleFeaturePage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	if !s.projectReadable(r, proj) {
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	s.render(w, http.StatusOK, "feature", struct {
		pageData
		Slug string
	}{s.pageFor(r, "Feature - "+slug), slug})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "login", s.pageFor(r, "Sign in - Concord"))
}

func (s *Server) handleRegisterPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "register", s.pageFor(r, "Create an account - Concord"))
}

// getActorID returns the authenticated user id, or 0 when anonymous.
//
// It delegates to actorID, which reads the typed actorKey that the
// authenticate middleware sets. The old implementation read the string key
// "actor_id", which nothing ever set: grep found no context.WithValue anywhere
// in non-test code, so every caller saw 0 and every write returned 401.
//
// Kept as a separate name because 62 call sites depend on it, and a missed
// rename would be a handler that silently stopped authenticating.
func getActorID(r *http.Request) int64 {
	return actorID(r)
}

// roleHierarchy maps roles to numeric ranks for comparison.
var roleHierarchy = map[string]int{
	"guest":       0,
	"user":        1,
	"contributor": 2,
	"reviewer":    3,
	"maintainer":  4,
	"owner":       5,
}

// requireRole returns an error if the actor's role in the project
// is below the minimum required role. Returns nil if authorized.
func (s *Server) requireRole(projectID int64, minRole string, r *http.Request) error {
	actorID := getActorID(r)
	if actorID == 0 {
		return store.ErrAuth
	}
	role, _ := s.Store.GetRoleForProject(r.Context(), projectID, actorID)
	actorRank, ok := roleHierarchy[role]
	if !ok {
		actorRank = 0
	}
	minRank, ok := roleHierarchy[minRole]
	if !ok {
		minRank = 2 // default to contributor
	}
	if actorRank < minRank {
		return store.ErrPerm
	}
	return nil
}

// getActorIDOr401 returns the actor ID or nil if not authenticated.

// ---------------------------------------------------------------- health

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------- list pages

func (s *Server) handleProjectsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "projects", s.pageFor(r, "Projects"))
}

// handleListRequests is in lists.go
