package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/discovery"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// ---------------------------------------------------------------- projects

type createProjectRequest struct {
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	GovernanceModel string `json:"governance_model"` // optional; default collective
	License         string `json:"license"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.GovernanceModel == "" {
		req.GovernanceModel = "collective"
	}
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	p, err := s.Store.CreateProject(r.Context(), actorID, req.Slug, req.Name,
		req.Description, req.GovernanceModel, req.License)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), p.ID, actorID, "create_project", "project", p.ID, p.Slug)
	writeJSON(w, http.StatusCreated, p)
}

// handleListProjects returns the projects this caller may see: every public one,
// plus anything private or protected the caller is a member of. An anonymous
// caller therefore sees exactly the public set, and a signed-in user does not
// lose sight of their own private projects just because they are not listed.
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	var (
		projects []store.Project
		err      error
	)
	if uid := getActorID(r); uid != 0 {
		projects, err = s.Store.ListProjectsVisibleTo(r.Context(), uid)
	} else {
		projects, err = s.Store.ListProjects(r.Context())
	}
	if err != nil {
		mapError(w, err)
		return
	}
	if projects == nil {
		projects = []store.Project{}
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		mapError(w, err)
		return
	}
	if !s.projectReadable(r, p) {
		mapError(w, store.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// ---------------------------------------------------------------- visibility

type setVisibilityRequest struct {
	Visibility string `json:"visibility"`
}

// handleSetProjectVisibility changes a project's level.
//
// Requires a member of that project, not merely a signed-in caller: anyone who
// could reach this route could hide a project they do not own. Trust level is
// deliberately NOT the gate -- the owner of a private project is usually the
// only member, and requiring a global trust level would lock them out of their
// own setting.
func (s *Server) handleSetProjectVisibility(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}

	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	role, err := s.Store.GetRoleForProject(r.Context(), proj.ID, actorID)
	if err != nil || !s.isProjectMember(role) {
		// 404, not 403: see requireProjectID. A non-member must not learn that
		// this slug exists.
		mapError(w, store.ErrNotFound)
		return
	}

	var req setVisibilityRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !store.ValidVisibility(req.Visibility) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "unknown visibility " + req.Visibility +
				"; valid: public, unlisted, protected, private",
		})
		return
	}

	before := proj.Visibility
	updated, err := s.Store.SetProjectVisibility(r.Context(), slug, req.Visibility)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), proj.ID, actorID, "set_visibility", "project",
		proj.ID, before+" -> "+updated.Visibility)
	writeJSON(w, http.StatusOK, updated)
}

// ---------------------------------------------------------------- invites

type createInviteRequest struct {
	ExpiresInSeconds int `json:"expires_in_seconds"` // <= 0 means no expiry
	MaxUses          int `json:"max_uses"`           // <= 0 means unlimited
}

// handleCreateProjectInvite mints an access link. Member-only, for the same
// reason as the visibility setter: a stranger must not be able to mint their own
// way into a project.
func (s *Server) handleCreateProjectInvite(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	role, err := s.Store.GetRoleForProject(r.Context(), proj.ID, actorID)
	if err != nil || !s.isProjectMember(role) {
		mapError(w, store.ErrNotFound)
		return
	}

	var req createInviteRequest
	if !readJSON(w, r, &req) {
		return
	}
	inv, err := s.Store.CreateInvite(r.Context(), proj.ID, actorID,
		req.ExpiresInSeconds, req.MaxUses)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), proj.ID, actorID, "create_invite", "invite",
		inv.ID, slug)
	writeJSON(w, http.StatusCreated, inv)
}

func (s *Server) handleListProjectInvites(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	role, err := s.Store.GetRoleForProject(r.Context(), proj.ID, actorID)
	if err != nil || !s.isProjectMember(role) {
		mapError(w, store.ErrNotFound)
		return
	}
	invites, err := s.Store.ListInvites(r.Context(), proj.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	if invites == nil {
		invites = []store.Invite{}
	}
	writeJSON(w, http.StatusOK, invites)
}

// handleRevokeProjectInvite withdraws a link while keeping the record of it.
func (s *Server) handleRevokeProjectInvite(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	inviteID, err := strconv.ParseInt(chi.URLParam(r, "invite_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}

	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	role, err := s.Store.GetRoleForProject(r.Context(), proj.ID, actorID)
	if err != nil || !s.isProjectMember(role) {
		mapError(w, store.ErrNotFound)
		return
	}

	// Confirm the invite belongs to this project before revoking it. Without
	// this, a member of one project could revoke an invite belonging to another
	// project entirely.
	inv, err := s.Store.GetInvite(r.Context(), inviteID)
	if err != nil {
		mapError(w, err)
		return
	}
	if inv.ProjectID != proj.ID {
		mapError(w, store.ErrNotFound)
		return
	}

	if err := s.Store.RevokeInvite(r.Context(), inviteID); err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), proj.ID, actorID, "revoke_invite", "invite",
		inviteID, slug)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

type redeemInviteRequest struct {
	Token string `json:"token"`
}

// handleRedeemInvite turns a token into a membership.
//
// The token is in the body, not the URL, so it does not end up in access logs,
// referrer headers or browser history. The response reports the project slug so
// the caller can navigate, and returns 404 for every failure mode -- expired,
// revoked, exhausted and unknown are indistinguishable on purpose, or the
// endpoint becomes an oracle for testing tokens.
func (s *Server) handleRedeemInvite(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	var req redeemInviteRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Token == "" {
		mapError(w, store.ErrInvalid)
		return
	}

	projectID, err := s.Store.RedeemInvite(r.Context(), req.Token, actorID)
	if err != nil {
		mapError(w, err)
		return
	}
	proj, err := s.Store.GetProjectByID(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, actorID, "redeem_invite", "invite",
		proj.ID, proj.Slug)
	writeJSON(w, http.StatusOK, map[string]string{
		"project":  proj.Slug,
		"slug":     proj.Slug,
		"status":   "joined",
		"role":     "contributor",
	})
}

// ---------------------------------------------------------------- discovery

type tagsRequest struct {
	Tags      []string `json:"tags"`
	AppliedBy string   `json:"applied_by"` // username; auth maps this later (PLAN M8)
}

func (s *Server) handleProjectTags(w http.ResponseWriter, r *http.Request) {
	var req tagsRequest
	if !readJSON(w, r, &req) {
		return
	}
	slug := chi.URLParam(r, "slug")
	var appliedBy int64
	if req.AppliedBy != "" {
		u, err := s.Store.GetUser(r.Context(), req.AppliedBy)
		if err != nil {
			mapError(w, err)
			return
		}
		appliedBy = u.ID
	}
	for _, tag := range req.Tags {
		if err := s.Store.ApplyProjectTag(r.Context(), slug, tag, appliedBy); err != nil {
			mapError(w, err)
			return
		}
	}
	p, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// handleGetProjectTags returns the names of a project's tags.
//
// The write half existed and the read half did not, which made `PUT /tags`
// unverifiable from a client: it answered 200 with the project body, and the
// project body has no tags in it. Found while adding Tessera's 13 tags -- all
// correctly stored, none of them readable.
func (s *Server) handleGetProjectTags(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	// Resolve and check the project first. ProjectTags takes a slug and returns
	// only tags, so without this the route answers 200 with an empty list for a
	// private project -- a 200 that confirms the slug exists, which is the leak
	// the other handlers were fixed for.
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	if !s.projectReadable(r, proj) {
		mapError(w, store.ErrNotFound)
		return
	}
	tags, err := s.Store.ProjectTags(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"tags": tags})
}

type languagesRequest struct {
	Languages []store.Language `json:"languages"`
}

func (s *Server) handleProjectLanguages(w http.ResponseWriter, r *http.Request) {
	var req languagesRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.SetProjectLanguages(r.Context(), chi.URLParam(r, "slug"), req.Languages); err != nil {
		mapError(w, err)
		return
	}
	p, err := s.Store.GetProject(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type metricsRequest struct {
	Stars             int     `json:"stars"`
	Forks             int     `json:"forks"`
	OpenIssues        int     `json:"open_issues"`
	CommitCount       int     `json:"commit_count"`
	Contributors      int     `json:"contributors"`
	LastCommitAgeDays float64 `json:"last_commit_age_days"`
	MedianReviewHours float64 `json:"median_review_hours"`
	Releases90d       int     `json:"releases_90d"`
	License           string  `json:"license"`
}

func (s *Server) handleProjectMetrics(w http.ResponseWriter, r *http.Request) {
	var req metricsRequest
	if !readJSON(w, r, &req) {
		return
	}
	m := discovery.Metrics{
		LastCommitAgeDays: req.LastCommitAgeDays,
		MedianReviewHours: req.MedianReviewHours,
		Contributors:      req.Contributors,
		Releases90d:       req.Releases90d,
	}
	p, err := s.Store.UpdateProjectMetrics(r.Context(), chi.URLParam(r, "slug"), m,
		req.Stars, req.Forks, req.OpenIssues, req.CommitCount, req.License)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
