package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/governance"
	"git.polarisocial.xyz/concord/concord/internal/ranking"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// handleGetList returns a single list.
func (s *Server) handleGetList(w http.ResponseWriter, r *http.Request) {
	// requireProjectID checks the caller's access to the project in the path. It
	// was missing, so a list belonging to a private project was readable by anyone
	// who counted to its id.
	//
	// Its result is now DISCARDED, because requireReadableList makes the same decision
	// from the list's own project -- and for an instance-wide list that is the correct
	// authority, since it has none in the path. The call is kept for the access check
	// it performs on the path's project, which the list's own scope cannot substitute.
	if _, ok := s.requireProjectID(w, r); !ok {
		return
	}
	// requireReadableList, rather than a second copy of the same decision.
	//
	// handleGetList and requireReadableList both answered "may this actor read this
	// list", and they DISAGREED: one of them accepted an instance-wide list and the
	// other refused it, because only one was updated when 16's instance-wide scope
	// was fixed. Two copies of a guard is one copy too many.
	l, ok := s.requireReadableList(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// handleGetListEntries returns entries for a list.
func (s *Server) handleGetListEntries(w http.ResponseWriter, r *http.Request) {
	// Entries carry the list's own visibility, so the LIST is checked rather than
	// the project: a protected list inside a public project must not become
	// readable just because its parent is.
	list, ok := s.requireReadableList(w, r)
	if !ok {
		return
	}
	listID := list.ID
	entries, err := s.Store.GetListEntries(r.Context(), listID)
	if err != nil {
		mapError(w, err)
		return
	}
	if entries == nil {
		entries = []store.ListEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleCreateListEntry adds an entry to a list.
func (s *Server) handleCreateListEntry(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	listID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req struct {
		URL         string `json:"url"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	entry, err := s.Store.CreateListEntry(r.Context(), listID, req.URL, req.Title, req.Description)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), 0, getActorID(r), "create_list_entry", "list_entry", entry.ID, req.Title)
	writeJSON(w, http.StatusCreated, entry)
}

// handleGetRequest returns a single request.
func (s *Server) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	// The route is /api/v1/requests/{request_id} -- there is NO project segment --
	// so the project has to be reached THROUGH the request row. Checking a path
	// parameter that does not exist is how this endpoint stayed unguarded: it does
	// not look like a project-scoped route, so the project-scoped guard was never
	// applied to it.
	request, ok := s.requireReadableRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, request)
}

// handleGetRequestAnswers returns answers for a request.
func (s *Server) handleGetRequestAnswers(w http.ResponseWriter, r *http.Request) {
	// The answers belong to the request, so the request is what gets checked --
	// /api/v1/requests/{request_id}/answers carries no project segment of its own.
	request, ok := s.requireReadableRequest(w, r)
	if !ok {
		return
	}
	answers, err := s.Store.GetRequestAnswers(r.Context(), request.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	if answers == nil {
		answers = []store.RequestAnswer{}
	}
	writeJSON(w, http.StatusOK, answers)
}

// handleCreateRequest creates a new request (with auth).
func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		// As on complaints and features: proceed past a near-identical request.
		ConfirmDuplicate bool `json:"confirm_duplicate"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	// A request is a "what fits my need?" question, so a near-duplicate is
	// especially costly: it fragments the answers instead of pooling them.
	_, proceed := s.checkDuplicates(w, r, store.KindRequest,
		req.Title, req.Body, projectID, req.ConfirmDuplicate)
	if !proceed {
		return
	}
	request, err := s.Store.CreateRequest(r.Context(), projectID, getActorID(r), req.Title, req.Body)
	if err != nil {
		mapError(w, err)
		return
	}
	if err := s.Store.UpsertEntityText(r.Context(), store.KindRequest, request.ID, projectID,
		req.Title, req.Body); err != nil {
		_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "embed_failed", "request", request.ID, err.Error())
	}
	_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "create_request", "request", request.ID, req.Title)
	writeJSON(w, http.StatusCreated, request)
}

// handleListRequests returns requests for a project.
func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	requests, err := s.Store.GetRequestsByProject(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	if requests == nil {
		requests = []store.Request{}
	}
	writeJSON(w, http.StatusOK, requests)
}

// handleListLists returns lists for a project.
func (s *Server) handleListLists(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	lists, err := s.Store.GetListsByProjectForDisplay(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	if lists == nil {
		lists = []store.List{}
	}
	writeJSON(w, http.StatusOK, lists)
}

// handleCreateList creates a new list (with auth).
func (s *Server) handleCreateList(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	var req struct {
		Slug        string `json:"slug"`
		Title       string `json:"title"`
		Description string `json:"description"`
		// "instance_wide": true asks for a list with NO project.
		//
		// 16: a list is "attachable to a project or instance-wide". The column is
		// nullable for it and migration 0001 annotates the NULL "NULL =
		// instance-wide" -- but until this field existed there was NO WAY TO CREATE
		// ONE OVER HTTP, because the project came from the path unconditionally.
		//
		// So the store could do it (after the 0-to-NULL translation), a raw INSERT
		// could do it, and the API could not. A spec feature reachable only from SQL.
		//
		// It is not "no project in the path" as a default, because the path's project
		// is also the ACCESS CHECK: requireProjectID has already answered "may this
		// actor write to that project". Defaulting to global would let any project
		// member mint an instance-wide list, so the scope must be asked for.
		InstanceWide bool `json:"instance_wide"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	// 0 is the store's spelling of "no project", which CreateList translates to NULL.
	scope := projectID
	if req.InstanceWide {
		scope = 0
	}
	l, err := s.Store.CreateList(r.Context(), scope, actorID(r), req.Slug, req.Title, req.Description)
	if err != nil {
		mapError(w, err)
		return
	}
	// Audited against the project the actor was checked against, which is the
	// path's -- not the scope written. An instance-wide list has no project to audit
	// under, and the audit trail is what says who may do this.
	_ = s.Store.AddAudit(r.Context(), projectID, actorID(r), "create_list", "list", l.ID, req.Slug)
	writeJSON(w, http.StatusCreated, l)
}

// handleAnswerRequest adds an answer to a request (with auth).
func (s *Server) handleAnswerRequest(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	requestID, err := strconv.ParseInt(chi.URLParam(r, "request_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}

	// The request is checked BEFORE the write, and through the row rather than a
	// path parameter: this route is /api/v1/requests/{request_id}/answers and has NO
	// project segment, so requireProjectID cannot be used here. requireReadableRequest
	// resolves the project through the request itself.
	//
	// The previous version passed getActorID(r) where AnswerRequest expected a
	// project id -- the signature had requestID, authorID, body and the table wanted
	// request_id, project_id, proposed_by. So an answer was attributed to a PROJECT
	// whose id happened to equal the caller's user id, and written to whichever
	// project that was. That is a cross-project write, not just a compile error, and
	// it is why requireReadableRequest is the check, and it is the only one: it
	// resolves the project from the row AND confirms the caller may read it.
	requestRow, ok := s.requireReadableRequest(w, r)
	if !ok {
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	answer, err := s.Store.AnswerRequest(r.Context(), requestID, requestRow.ProjectID,
		getActorID(r), req.Body)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), requestRow.ProjectID, getActorID(r), "answer_request",
		"request_answer", answer.ID, fmt.Sprintf("request=%d", requestID))
	writeJSON(w, http.StatusCreated, answer)
}

// voteAnswerRequest is the pairwise comparison body, matching voteSolutionRequest
// and the feature vote: two items and an outcome, never a direction.
//
// The old body was `{"direction": 1}` against /api/v1/requests/{request_id}/vote,
// which cannot express "which of these two answers fits this request better" and
// named a column the table does not have.
type voteAnswerRequest struct {
	AnswerA int64  `json:"answer_a"`
	AnswerB int64  `json:"answer_b"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
	// Weight is accepted and ignored, for the same reason as everywhere else:
	// §8.3's "money cannot buy weight". The weight is computed server-side from the
	// voter's trust in this project.
	Weight float64 `json:"weight"`
}

// handleVoteAnswer records a pairwise comparison between two answers to a request.
//
// The route has no project segment, so visibility is resolved through the request
// row (requireReadableRequest) rather than a path parameter -- the fourth time this
// shape has appeared, and the reason that helper exists.
func (s *Server) handleVoteAnswer(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	requestRow, ok := s.requireReadableRequest(w, r)
	if !ok {
		return
	}
	var req voteAnswerRequest
	if !readJSON(w, r, &req) {
		return
	}

	// Both answers must belong to THIS request, checked before anything is
	// written -- otherwise a vote naming an answer from another question spends
	// this request's vote and moves an unrelated rating.
	a, err := s.Store.GetRequestAnswer(r.Context(), req.AnswerA)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}
	b, err := s.Store.GetRequestAnswer(r.Context(), req.AnswerB)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}
	if a.RequestID != requestRow.ID || b.RequestID != requestRow.ID {
		mapError(w, store.ErrPerm)
		return
	}

	charter, err := s.Store.GetCharterForProject(r.Context(), requestRow.ProjectID)
	if err != nil {
		mapError(w, err)
		return
	}
	weight, err := s.answerVoteWeight(r.Context(), requestRow.ProjectID, actorID, charter, requestRow)
	if err != nil {
		mapError(w, err)
		return
	}

	if err := s.Store.VoteAnswer(r.Context(), requestRow.ID, a.ID, b.ID, actorID,
		req.Outcome, weight, charter); err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), requestRow.ProjectID, actorID, "vote_request_answer",
		"request", requestRow.ID,
		fmt.Sprintf("a=%d b=%d outcome=%s", a.ID, b.ID, req.Outcome))

	// The ratings back in the response: a vote the caller cannot see take effect is
	// a vote they cannot trust.
	answers, err := s.Store.GetRequestAnswers(r.Context(), requestRow.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "voted", "answers": answers})
}

// answerVoteWeight is this voter's influence on a request answer.
//
// It reuses §8.3's formula -- the same reputation, role, expertise and age-gate
// terms solutionVoteWeight applies -- and deliberately NOT the solution vote's
// signature: that one weighs "how close is this solution to the feature", which is
// meaningless for two answers to the same question. Expertise therefore uses the
// request's own tags when it has any, and falls back to 1.0 when it does not.
//
// Writing this as a separate, simpler formula would have been the tempting choice
// and the wrong one: §8.3's weights exist so an account cannot manufacture
// influence, and a vote weight is a vote weight. Test:
// TestAnswerVoteWeightMatchesTheFeatureVoteFormula.
func (s *Server) answerVoteWeight(ctx context.Context, projectID, actorID int64, charter governance.Charter, req store.Request) (float64, error) {
	reputation, err := s.Store.GetReputation(ctx, projectID, actorID)
	if err != nil {
		return 0, err
	}
	role, err := s.Store.GetRoleForProject(ctx, projectID, actorID)
	if err != nil {
		return 0, err
	}
	weight := ranking.VoteWeight(reputation, ranking.RoleMultiplier(role), charter.VoteWeightCap)

	// NO EXPERTISE TERM, and that is a decision rather than an omission.
	//
	// §8.3's formula multiplies in an expertise term scoped to the items being
	// compared. For solutions that is their expertise_tags, which exist. For request
	// answers there is nothing: request_answers carries no tags, and `requests` has a
	// request_tags table with no store method behind it. Inventing a substitute --
	// reusing the SOLUTION tags, or the voter's whole tag reputation -- would apply a
	// multiplier computed from an unrelated question, which is worse than omitting
	// one the data cannot support.
	//
	// So the reputation, role and age-gate terms apply and the expertise term is 1.0.
	// A test asserts that, so adding a real term later is a visible change rather than
	// a silent one:
	// TestAnswerVoteWeightAppliesReputationRoleAndAgeButNoExpertise.

	// The age gate last, for §12.3's Sybil defence and the same reason as the
	// solution vote: a defence a 2.0x expertise multiplier can halve is not a
	// defence.
	s.applyAgeGate(ctx, actorID, &weight)

	if weight > charter.VoteWeightCap {
		weight = charter.VoteWeightCap
	}
	if weight < 0 {
		weight = 0
	}
	return weight, nil
}

// requireReadableList loads the list named in {id} and confirms the caller may
// read it, answering the request itself when they may not.
//
// Entries are read through this rather than by bare id: a list lives inside a
// project, so checking the project is what actually decides the answer, and a
// numeric id on its own carries no access information at all.
func (s *Server) requireReadableList(w http.ResponseWriter, r *http.Request) (store.List, bool) {
	listID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return store.List{}, false
	}
	l, err := s.Store.GetList(r.Context(), listID)
	if err != nil {
		mapError(w, err)
		return store.List{}, false
	}
	// An instance-wide list has no project to check, and §16 says it is readable from
	// everywhere -- so it passes here, and the CONTENT is public by definition.
	//
	// This nil case is why the function had to change: it used to dereference
	// l.ProjectID unconditionally, which for a NULL column is a nil pointer panic --
	// and the only routes that reach here are the ones an instance-wide list must
	// travel.
	if l.ProjectID == nil {
		return l, true
	}
	proj, err := s.Store.GetProjectByID(r.Context(), *l.ProjectID)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return store.List{}, false
	}
	if !s.projectReadable(r, proj) {
		// 404, not 403: a 403 would confirm the id exists.
		mapError(w, store.ErrNotFound)
		return store.List{}, false
	}
	return l, true
}

// requireReadableRequest loads the request named in {request_id} and confirms the
// caller may read it.
//
// This exists because the route is /api/v1/requests/{request_id}: there is no
// project in the path to run requireProjectID against. Reaching the project through
// the row is the only way to apply the same access rule, and it is what these two
// handlers were missing.
func (s *Server) requireReadableRequest(w http.ResponseWriter, r *http.Request) (store.Request, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "request_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return store.Request{}, false
	}
	req, err := s.Store.GetRequest(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return store.Request{}, false
	}

	// An INSTANCE-WIDE request (requests.project_id IS NULL) belongs to no project,
	// so there is no project whose visibility could hide it -- it is public by
	// construction, exactly like the instance-wide board it sits on.
	//
	// This branch is the fix for a bug the e2e suite found: the original code called
	// GetProjectByID(0) unconditionally, which finds no project, and answered 404.
	// So every instance-wide request was unreadable through every route that uses
	// this helper -- the detail page, the answers, the tags, accepting and voting.
	// The feature I had just finished adding was dead on arrival for exactly the
	// requests it exists to serve.
	if req.ProjectID == 0 {
		return req, true
	}

	proj, err := s.Store.GetProjectByID(r.Context(), req.ProjectID)
	if err != nil {
		// A project-scoped request whose project has been deleted is a 404 rather
		// than a 500: the cascade (migration 0008) should have taken the request with
		// it, and a row that outlived its project is not a request anybody may read.
		mapError(w, store.ErrNotFound)
		return store.Request{}, false
	}
	if !s.projectReadable(r, proj) {
		mapError(w, store.ErrNotFound)
		return store.Request{}, false
	}
	return req, true
}

// handleListInstanceRequests lists the instance-wide request board.
//
// The board is public by construction: a request with project_id NULL belongs to the
// instance, so there is no project whose visibility could hide it. That is why this
// handler has no projectReadable call, unlike everything else in this file -- and it
// is worth stating, because "no guard here" otherwise reads as an oversight.
//
// What it must NOT do is leak the project-scoped requests that happen to share the
// table, which is the whole of GetInstanceRequests' WHERE clause in one line.
func (s *Server) handleListInstanceRequests(w http.ResponseWriter, r *http.Request) {
	requests, err := s.Store.GetInstanceRequests(r.Context())
	if err != nil {
		mapError(w, err)
		return
	}
	if requests == nil {
		requests = []store.Request{}
	}
	writeJSON(w, http.StatusOK, requests)
}

// handleCreateInstanceRequest opens a question on the instance-wide board.
//
// Anyone signed in may ask the instance a question. It has no project, so there is
// nothing to check membership of -- but the caller must still be a real account,
// because a request is authored (requests.author_id is NOT NULL) and authorship is
// what the answer votes exclude.
func (s *Server) handleCreateInstanceRequest(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		mapError(w, store.ErrInvalid)
		return
	}
	request, err := s.Store.CreateInstanceRequest(r.Context(), getActorID(r),
		req.Title, req.Body)
	if err != nil {
		mapError(w, err)
		return
	}
	// Audit with project 0: the request belongs to the instance, and inventing a
	// project here would misfile it under whichever project happened to be first.
	_ = s.Store.AddAudit(r.Context(), 0, getActorID(r), "create_instance_request",
		"request", request.ID, request.Title)
	writeJSON(w, http.StatusCreated, request)
}

// handleListMergeRequests returns a project's merge requests.
//
// Guarded on the project in the path, like every other project-scoped read here.
// The rows also carry a feature_id, and a merge request for a feature in ANOTHER
// project must not be served here -- the same ownership check the feature and
// complaint endpoints need, and the same reason it survives mutation testing only
// once a test exists for it.
// Each row carries §5.7's gate verdict alongside it, because the client's next
// question is always "is this ready to land?" and a page that worked it out for
// itself would be a second implementation of the rule -- the one that gets it
// wrong. The count reported is the COUNTED confirmations, not the rows.
func (s *Server) handleListMergeRequests(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	mrs, err := s.Store.ListMergeRequests(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	views := make([]store.MergeRequestView, 0, len(mrs))
	for _, mr := range mrs {
		gate, err := s.Store.MergeGateFor(r.Context(), mr.ID)
		if err != nil {
			mapError(w, err)
			return
		}
		views = append(views, store.MergeRequestView{MergeRequest: mr, Gate: gate})
	}
	writeJSON(w, http.StatusOK, views)
}

// handleGetRequestTags returns a request's tags.
//
// A request's tags ARE its constraints, and §8.3 scopes a voter's expertise by tag,
// so this is the input that decides how much a comparison against these answers is
// worth.
func (s *Server) handleGetRequestTags(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "request_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	if _, ok := s.requireReadableRequest(w, r); !ok {
		return
	}
	tags, err := s.Store.GetRequestTags(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// handleSetRequestTags replaces a request's tags.
//
// Replace, not merge: a request's deal-breakers are its tags, so "this no longer
// requires self-hosting" has to be expressible and a merge would make it impossible.
func (s *Server) handleSetRequestTags(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "request_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	if _, ok := s.requireReadableRequest(w, r); !ok {
		return
	}
	var req struct {
		Tags []string `json:"tags"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.SetRequestTags(r.Context(), id, getActorID(r), req.Tags); err != nil {
		mapError(w, err)
		return
	}
	tags, err := s.Store.GetRequestTags(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// handleAcceptAnswer marks an answer as the accepted fit.
//
// Author-only, enforced in the store against the request's author_id rather than a
// token claim, and it does NOT touch the rating: §17 says the badge is displayed but
// does not override the ranking.
func (s *Server) handleAcceptAnswer(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "request_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	if _, ok := s.requireReadableRequest(w, r); !ok {
		return
	}
	var req struct {
		AnswerID int64 `json:"answer_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.AcceptAnswer(r.Context(), id, req.AnswerID, getActorID(r)); err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), 0, getActorID(r), "accept_request_answer",
		"request_answer", req.AnswerID, fmt.Sprintf("request=%d", id))

	// The updated request back, so the caller sees the badge they just set.
	updated, err := s.Store.GetRequest(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleUnacceptAnswer clears the accepted badge. Same author-only rule.
func (s *Server) handleUnacceptAnswer(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "request_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	if _, ok := s.requireReadableRequest(w, r); !ok {
		return
	}
	if err := s.Store.UnacceptAnswer(r.Context(), id, getActorID(r)); err != nil {
		mapError(w, err)
		return
	}
	updated, err := s.Store.GetRequest(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// voteListEntryRequest is the pairwise comparison body, matching voteSolutionRequest,
// the feature vote and the answer vote: two items and an outcome, never a direction.
//
// list_entry_votes.outcome is CHECKed to exactly ('a','b','both','neither','skip'), so
// a {"direction": 1} body would be refused by the database -- and because the UI maps a
// failed fetch to an empty state, that reads as "no ranking" rather than an error.
type voteListEntryRequest struct {
	EntryA  int64  `json:"entry_a"`
	EntryB  int64  `json:"entry_b"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
	// Accepted and ignored, for the same reason as everywhere else: §8.3's "money
	// cannot buy weight". The weight comes from the voter's trust in the project.
	Weight float64 `json:"weight"`
}

// handleVoteListEntry records a pairwise comparison between two entries of a list.
//
// The list is resolved from the {id} in the path and must belong to the project in the
// path -- a vote is project-scoped, and the weight formula needs the project. Both
// checks, because requireProjectID guards the project and the store guards the entries'
// membership of the list.
func (s *Server) handleVoteListEntry(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	listID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	list, err := s.Store.GetList(r.Context(), listID)
	if err != nil {
		mapError(w, err)
		return
	}
	// A list belongs to a project or is instance-wide (lists.project_id IS NULL). An
	// instance-wide list cannot be voted on through a project URL, because the weight
	// formula needs a project and there is none: saying so beats computing a weight
	// from an unrelated project.
	if list.ProjectID == nil || *list.ProjectID != projectID {
		mapError(w, store.ErrNotFound)
		return
	}

	var req voteListEntryRequest
	if !readJSON(w, r, &req) {
		return
	}

	charter, err := s.Store.GetCharterForProject(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	weight, err := s.entryVoteWeight(r.Context(), projectID, actorID, charter)
	if err != nil {
		mapError(w, err)
		return
	}

	if err := s.Store.VoteListEntry(r.Context(), listID, req.EntryA, req.EntryB, actorID,
		req.Outcome, weight, charter); err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, actorID, "vote_list_entry", "list",
		listID, fmt.Sprintf("a=%d b=%d outcome=%s", req.EntryA, req.EntryB, req.Outcome))

	// The entries back, so a vote the caller cannot see take effect is a vote they
	// cannot trust.
	entries, err := s.Store.GetListEntries(r.Context(), listID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "voted", "entries": entries})
}

// entryVoteWeight is this voter's influence on a list entry.
//
// §8.3's formula -- reputation and role, capped by the charter -- and NOT the
// solution vote's, because that one weighs proximity to a FEATURE, which is
// meaningless for two library URLs. There is no expertise term: list entries carry no
// tags, so there is nothing for one to be scoped to, and inventing a substitute would
// apply a multiplier computed from an unrelated subject.
func (s *Server) entryVoteWeight(ctx context.Context, projectID, actorID int64, charter governance.Charter) (float64, error) {
	reputation, err := s.Store.GetReputation(ctx, projectID, actorID)
	if err != nil {
		return 0, err
	}
	role, err := s.Store.GetRoleForProject(ctx, projectID, actorID)
	if err != nil {
		return 0, err
	}
	weight := ranking.VoteWeight(reputation, ranking.RoleMultiplier(role), charter.VoteWeightCap)

	// The age gate last, for §12.3's Sybil defence: a defence a reputation multiplier
	// can be cancelled by is not a defence.
	s.applyAgeGate(ctx, actorID, &weight)

	if weight > charter.VoteWeightCap {
		weight = charter.VoteWeightCap
	}
	if weight < 0 {
		weight = 0
	}
	return weight, nil
}

// handleListInstanceLists returns every instance-wide list.
//
// §16's "attachable to a project or instance-wide" needed a route that did not name a
// project: the project-scoped index cannot return one (project_id IS NULL matches no
// ?), so without this the scope was write-only.
func (s *Server) handleListInstanceLists(w http.ResponseWriter, r *http.Request) {
	lists, err := s.Store.GetInstanceLists(r.Context())
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lists)
}
