package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

// The global tag taxonomy (§4.3).
//
// The existing tag handling had two problems this file addresses.
//
// ensureTag silently INSERTed a new global tag whenever one was not found, so
// the global taxonomy was a side effect of editing one project. §4.3 says
// tagging must suggest existing tags before allowing new ones, and that global
// taxonomy changes are proposals decided by quorum among eligible taggers, with
// maintainers holding no unilateral taxonomy power. Auto-create-on-use gave
// every project editor instance-wide vocabulary control.
//
// handleProjectTags had no authentication at all and took applied_by from the
// request body, so any caller could tag any project and forge the attribution.
// That is fixed in internal/httpapi/projects.go, not here: a route that never
// checked identity cannot be rescued by a store signature.

// KnownNamespaces are the tag namespaces §4.3 names. Restricted to a set
// because an unvalidated namespace string is how a taxonomy fragments itself --
// `Topic:`, `topic:`, and `topics:` would all be separate subtrees.
var KnownNamespaces = []string{"topic", "domain", "platform", "lang", "role", "deploy", "audience"}

// TaxonomyProposal is a pending or decided change to the tag taxonomy.
type TaxonomyProposal struct {
	ID         int64   `json:"id"`
	ProjectID  int64   `json:"project_id"` // 0 for instance-wide
	ProposedBy int64   `json:"proposed_by"`
	Action     string  `json:"action"`
	TargetTag  string  `json:"target_tag"`
	Value      string  `json:"value"`
	Rationale  string  `json:"rationale"`
	Status     string  `json:"status"`
	CreatedAt  float64 `json:"created_at"`
	DecidedAt  float64 `json:"decided_at"`
}

// Tag is a namespaced, hierarchical label.
type Tag struct {
	ID        int64    `json:"id"`
	ProjectID int64    `json:"project_id"` // 0 for the global namespace
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	ParentID  int64    `json:"parent_id"`
	Relation  string   `json:"relation"`
	Suggested bool     `json:"suggested"`
	Aliases   []string `json:"aliases,omitempty"`
}

// NormalizeTag lowercases, trims, and collapses internal whitespace to hyphens.
//
// A tag is a URL-ish identifier used in query syntax (`tag:self-hosted`), so
// spaces and case variants have to normalize or `Tag:Kanban` and `tag:kanban`
// become two tags.
func NormalizeTag(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.Join(strings.Fields(name), "-")
}

// SplitTag separates a namespaced tag into its namespace and name.
//
// "topic:kanban" -> ("topic", "kanban"). A bare "kanban" has no namespace: it is
// not assumed to be `topic:`, because guessing would make the un-namespaced tags
// silently gain a parent taxonomy on the next migration.
func SplitTag(name string) (namespace, bare string) {
	name = NormalizeTag(name)
	if i := strings.Index(name, ":"); i > 0 && i < len(name)-1 {
		return name[:i], name[i+1:]
	}
	return "", name
}

// TaxonomyAction names the §4.3 operations that go through quorum.
const (
	TaxonomyCreate   = "create"
	TaxonomyRename   = "rename"
	TaxonomyAlias    = "alias"
	TaxonomyMerge    = "merge"
	TaxonomyReparent = "reparent"
	TaxonomyDelete   = "delete"
)

// ProposeTaxonomyChange opens a proposal to change the taxonomy.
//
// projectID 0 means an instance-wide change, which is the case §4.3 puts
// squarely under quorum: "maintainers hold no unilateral taxonomy power".
func (d *DB) ProposeTaxonomyChange(ctx context.Context, projectID, userID int64, action, target, value, rationale string) (TaxonomyProposal, error) {
	switch action {
	case TaxonomyCreate, TaxonomyRename, TaxonomyAlias, TaxonomyMerge, TaxonomyReparent, TaxonomyDelete:
	default:
		return TaxonomyProposal{}, fmt.Errorf("%w: unknown taxonomy action %q", ErrInvalid, action)
	}
	target = NormalizeTag(target)
	if target == "" {
		return TaxonomyProposal{}, fmt.Errorf("%w: a taxonomy proposal needs a target tag", ErrInvalid)
	}
	value = NormalizeTag(value)
	// Only the actions with an operand require one. create and delete take just
	// the target; requiring a value from them would make every create proposal
	// invalid.
	switch action {
	case TaxonomyCreate, TaxonomyDelete:
	case TaxonomyRename, TaxonomyAlias, TaxonomyMerge, TaxonomyReparent:
		if value == "" {
			return TaxonomyProposal{}, fmt.Errorf("%w: taxonomy action %q needs a value", ErrInvalid, action)
		}
		// Compare the bare forms, because "topic:thing" and "thing" name the same
		// tag: globalTagID strips the namespace before looking up. Comparing the
		// raw strings let a rename-to-itself through whenever the two spellings
		// differed only by the prefix.
		if _, bareTarget := SplitTag(target); bareTarget == value {
			return TaxonomyProposal{}, fmt.Errorf("%w: taxonomy action %q would be a no-op", ErrInvalid, action)
		}
	}
	if ns, _ := SplitTag(target); ns != "" && !validNamespace(ns) {
		return TaxonomyProposal{}, fmt.Errorf("%w: unknown namespace %q (known: %s)",
			ErrInvalid, ns, strings.Join(KnownNamespaces, ", "))
	}
	rationale = strings.TrimSpace(rationale)
	if rationale == "" {
		return TaxonomyProposal{}, fmt.Errorf("%w: a taxonomy change needs a rationale", ErrInvalid)
	}

	now := float64(time.Now().Unix())
	var pid any
	if projectID > 0 {
		pid = projectID
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO taxonomy_proposals
			(project_id, proposed_by, action, target_tag, value, rationale, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?)`,
		pid, userID, action, target, value, rationale, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return TaxonomyProposal{}, fmt.Errorf("%w: an identical proposal is already pending", ErrDuplicate)
		}
		return TaxonomyProposal{}, err
	}
	id, _ := res.LastInsertId()
	scope := "instance"
	if projectID > 0 {
		scope = fmt.Sprintf("project %d", projectID)
	}
	_ = d.AddAudit(ctx, projectID, userID, "propose_taxonomy_change", "tag", id,
		fmt.Sprintf("%s %s=%s (%s scope)", action, target, value, scope))
	return d.GetTaxonomyProposal(ctx, id)
}

func validNamespace(ns string) bool {
	for _, k := range KnownNamespaces {
		if k == ns {
			return true
		}
	}
	return false
}

func scanTaxonomyProposal(rows interface{ Scan(...any) error }) (TaxonomyProposal, error) {
	var p TaxonomyProposal
	var pid sql.NullInt64
	var decided sql.NullFloat64
	err := rows.Scan(&p.ID, &pid, &p.ProposedBy, &p.Action, &p.TargetTag,
		&p.Value, &p.Rationale, &p.Status, &p.CreatedAt, &decided)
	p.ProjectID = pid.Int64
	if decided.Valid {
		p.DecidedAt = decided.Float64
	}
	return p, err
}

// GetTaxonomyProposal reads one proposal.
func (d *DB) GetTaxonomyProposal(ctx context.Context, id int64) (TaxonomyProposal, error) {
	p, err := scanTaxonomyProposal(d.QueryRowContext(ctx, `
		SELECT id, project_id, proposed_by, action, target_tag, value,
		       rationale, status, created_at, decided_at
		FROM taxonomy_proposals WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return TaxonomyProposal{}, fmt.Errorf("%w: taxonomy proposal %d", ErrNotFound, id)
	}
	return p, err
}

// ListTaxonomyProposals returns pending then recent proposals, newest first.
func (d *DB) ListTaxonomyProposals(ctx context.Context, projectID int64) ([]TaxonomyProposal, error) {
	var pid any
	if projectID > 0 {
		pid = projectID
	}
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, proposed_by, action, target_tag, value,
		       rationale, status, created_at, decided_at
		FROM taxonomy_proposals
		WHERE project_id IS NULL OR project_id = ?
		ORDER BY (status = 'pending') DESC, created_at DESC`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaxonomyProposal
	for rows.Next() {
		p, err := scanTaxonomyProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RatifyTaxonomyProposal consents to a proposal and applies it once the charter
// thresholds are met.
//
// Eligibility is deliberately broader than consensus-eligible collaborators: §4.3
// grants taxonomy authority to "eligible taggers: users with reputation in the
// tag's namespace". Counting anyone with contributor or above in the project is
// the tractable approximation, and it is documented rather than pretended: a
// namespace-reputation table does not exist yet.
func (d *DB) RatifyTaxonomyProposal(ctx context.Context, proposalID, userID int64) (TaxonomyProposal, error) {
	p, err := d.GetTaxonomyProposal(ctx, proposalID)
	if err != nil {
		return TaxonomyProposal{}, err
	}
	if p.Status != "pending" {
		return TaxonomyProposal{}, fmt.Errorf("%w: proposal %d is %s", ErrInvalid, proposalID, p.Status)
	}
	if p.ProposedBy == userID {
		// §5.1: no voting on your own items. Without this, in a small project the
		// proposer is the only eligible tagger and ratifies alone.
		return p, fmt.Errorf("%w: the proposer cannot consent to their own proposal", ErrPerm)
	}

	// An instance-wide proposal is ratified by the instance charter rather than a
	// project's, so its thresholds do not vary with whichever project happens to
	// host the most collaborators.
	charter := governance.DefaultCharter(governance.Collective)
	if p.ProjectID > 0 {
		if c, err := d.GetCharterForProject(ctx, p.ProjectID); err == nil {
			charter = c
		}
	}
	eligible, err := d.eligibleTaggers(ctx, p.ProjectID)
	if err != nil {
		return TaxonomyProposal{}, err
	}
	consents, err := d.taxonomyConsents(ctx, proposalID)
	if err != nil {
		return TaxonomyProposal{}, err
	}

	now := float64(time.Now().Unix())
	if _, err := d.ExecContext(ctx, `
		INSERT INTO taxonomy_consents (proposal_id, user_id, consented_at)
		VALUES (?, ?, ?)
		ON CONFLICT(proposal_id, user_id) DO NOTHING`, proposalID, userID, now); err != nil {
		return TaxonomyProposal{}, err
	}
	_ = d.AddAudit(ctx, p.ProjectID, userID, "taxonomy_consent", "tag", proposalID, p.Action+" "+p.TargetTag)

	counts := governance.ConsensusCounts{
		Consent: consents + 1, Participants: consents + 1, Eligible: eligible,
	}
	if result := governance.EvaluateConsensus(counts, charter); result != governance.ResultAccepted {
		return p, fmt.Errorf("%w: proposal %d is %s (%d/%d consents)", ErrConflict, proposalID, result, counts.Consent, counts.Participants)
	}

	if err := d.applyTaxonomyChange(ctx, p); err != nil {
		// Applying failed, so the proposal is not ratified: recording it as
		// decided would hide a change that never happened.
		return p, err
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE taxonomy_proposals SET status='ratified', decided_at=? WHERE id=?`,
		float64(time.Now().Unix()), proposalID); err != nil {
		return TaxonomyProposal{}, err
	}
	_ = d.AddAudit(ctx, p.ProjectID, userID, "ratify_taxonomy_change", "tag", proposalID,
		fmt.Sprintf("%s %s=%s", p.Action, p.TargetTag, p.Value))
	return d.GetTaxonomyProposal(ctx, proposalID)
}

// applyTaxonomyChange performs the ratified mutation.
func (d *DB) applyTaxonomyChange(ctx context.Context, p TaxonomyProposal) error {
	now := float64(time.Now().Unix())
	switch p.Action {
	case TaxonomyCreate:
		ns, bare := SplitTag(p.TargetTag)
		if bare == "" {
			return fmt.Errorf("%w: cannot create a namespaceless empty tag", ErrInvalid)
		}
		var exists int
		if err := d.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tags WHERE lower(name)=lower(?)`, bare).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return fmt.Errorf("%w: tag %q already exists", ErrDuplicate, bare)
		}
		var nsArg any
		if ns != "" {
			nsArg = ns
		}
		_, err := d.ExecContext(ctx,
			`INSERT INTO tags (project_id, name, namespace, suggested) VALUES (NULL, ?, ?, 0)`,
			bare, nsArg)
		return err

	case TaxonomyRename:
		_, err := d.ExecContext(ctx, `UPDATE tags SET name=? WHERE lower(name)=lower(?)`, p.Value, p.TargetTag)
		return err

	case TaxonomyAlias:
		id, err := d.globalTagID(ctx, p.TargetTag)
		if err != nil {
			return err
		}
		_, err = d.ExecContext(ctx, `
			INSERT INTO tag_aliases (tag_id, alias, created_by, created_at)
			VALUES (?, ?, 1, ?)`, id, p.Value, now)
		return err

	case TaxonomyMerge:
		// Absorb `value` into `target_tag`: repoint every reference at the
		// survivor, then delete the absorbed row.
		from, err := d.globalTagID(ctx, p.Value)
		if err != nil {
			return err
		}
		to, err := d.globalTagID(ctx, p.TargetTag)
		if err != nil {
			return err
		}
		if from == to {
			return fmt.Errorf("%w: cannot merge a tag into itself", ErrInvalid)
		}
		if _, err := d.ExecContext(ctx,
			`UPDATE OR IGNORE project_tags SET tag_id=? WHERE tag_id=?`, to, from); err != nil {
			return err
		}
		for _, q := range []struct{ table string }{
			{"complaint_tags"}, {"feature_tags"}, {"list_tags"}, {"request_tags"},
		} {
			if _, err := d.ExecContext(ctx,
				fmt.Sprintf(`UPDATE OR IGNORE %s SET tag_id=? WHERE tag_id=?`, q.table), to, from); err != nil {
				return err
			}
		}
		// Aliases of the absorbed tag follow it, so "js" still resolves after
		// "javascript" is merged into "js".
		if _, err := d.ExecContext(ctx,
			`UPDATE OR IGNORE tag_aliases SET tag_id=? WHERE tag_id=?`, to, from); err != nil {
			return err
		}
		_, err = d.ExecContext(ctx, `DELETE FROM tags WHERE id=?`, from)
		return err

	case TaxonomyReparent:
		id, err := d.globalTagID(ctx, p.TargetTag)
		if err != nil {
			return err
		}
		parent, err := d.globalTagID(ctx, p.Value)
		if err != nil {
			return err
		}
		if id == parent {
			return fmt.Errorf("%w: a tag cannot be its own parent", ErrInvalid)
		}
		// Refuse a descendant, which is the cycle the trigger cannot catch.
		if d.isTagDescendant(ctx, parent, id) {
			return fmt.Errorf("%w: %q is a descendant of %q, so reparenting would create a cycle",
				ErrInvalid, p.Value, p.TargetTag)
		}
		relation := "is-a"
		if ns, _ := SplitTag(p.Value); ns != "" {
			relation = "part-of"
		}
		_, err = d.ExecContext(ctx,
			`UPDATE tags SET parent_id=?, relation=? WHERE id=?`, parent, relation, id)
		return err

	case TaxonomyDelete:
		id, err := d.globalTagID(ctx, p.TargetTag)
		if err != nil {
			return err
		}
		_, err = d.ExecContext(ctx, `DELETE FROM tags WHERE id=?`, id)
		return err
	}
	return fmt.Errorf("%w: unhandled taxonomy action %q", ErrInvalid, p.Action)
}

// isTagDescendant reports whether candidate lies under ancestor, walking up.
// Bounded so a pre-existing cycle cannot hang the request.
func (d *DB) isTagDescendant(ctx context.Context, candidate, ancestor int64) bool {
	seen := map[int64]bool{}
	for cur := candidate; cur != 0 && len(seen) < 100; {
		if cur == ancestor {
			return true
		}
		seen[cur] = true
		var parent sql.NullInt64
		if err := d.QueryRowContext(ctx, `SELECT parent_id FROM tags WHERE id=?`, cur).Scan(&parent); err != nil {
			return false
		}
		cur = parent.Int64
	}
	return false
}

// globalTagID resolves a tag name or alias to a tag id.
func (d *DB) globalTagID(ctx context.Context, name string) (int64, error) {
	name = NormalizeTag(name)
	_, bare := SplitTag(name)
	var id int64
	if err := d.QueryRowContext(ctx,
		`SELECT id FROM tags WHERE lower(name)=lower(?)`, bare).Scan(&id); err == nil {
		return id, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	// Fall back to the alias table, so a merge can name either side by its alias.
	if err := d.QueryRowContext(ctx,
		`SELECT tag_id FROM tag_aliases WHERE lower(alias)=lower(?)`, name).Scan(&id); err == nil {
		return id, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return 0, fmt.Errorf("%w: tag %q", ErrNotFound, name)
}

// eligibleTaggers counts who may consent to a taxonomy proposal.
//
// Project-scoped: contributors and above in that project. Instance-wide: any
// contributor in any project, because the change affects every project and the
// §4.3 population is "users with reputation in the tag's namespace" -- with no
// namespace reputation modelled yet, membership anywhere is the closest honest
// approximation.
func (d *DB) eligibleTaggers(ctx context.Context, projectID int64) (int, error) {
	var n int
	var err error
	if projectID > 0 {
		err = d.QueryRowContext(ctx, `
			SELECT COUNT(DISTINCT user_id) FROM members
			WHERE project_id = ?
			  AND role IN ('contributor','reviewer','maintainer','owner')`, projectID).Scan(&n)
	} else {
		err = d.QueryRowContext(ctx, `
			SELECT COUNT(DISTINCT user_id) FROM members
			WHERE role IN ('contributor','reviewer','maintainer','owner')`).Scan(&n)
	}
	return n, err
}

// GetHighestRole returns an actor's strongest role across all projects, or
// "guest" when they hold none.
//
// Used for instance-wide permission checks (§4.3 taxonomy authority, which spans
// every project) where there is no single project to resolve the role against.
// Matches the literal "guest" convention GetRoleForProject uses, so a caller
// that forgets to handle guest gets a rank of 0 rather than a default.
func (d *DB) GetHighestRole(ctx context.Context, userID int64) (string, error) {
	if userID == 0 {
		return "guest", nil
	}
	var role string
	err := d.QueryRowContext(ctx, `
		SELECT role FROM members WHERE user_id = ?
		ORDER BY CASE role
			WHEN 'owner' THEN 5 WHEN 'maintainer' THEN 4 WHEN 'reviewer' THEN 3
			WHEN 'contributor' THEN 2 WHEN 'user' THEN 1 ELSE 0 END DESC
		LIMIT 1`, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "guest", nil
	}
	if err != nil {
		return "", err
	}
	return role, nil
}

func (d *DB) taxonomyConsents(ctx context.Context, proposalID int64) (int, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM taxonomy_consents WHERE proposal_id=?`, proposalID).Scan(&n)
	return n, err
}

// ListTags returns tags for browsing and suggest-before-create, optionally
// filtered to one namespace.
//
// suggested=1 rows are machine-inferred and §4.3 requires them to be labeled,
// so the flag is returned rather than filtered out: the caller decides whether
// to offer them.
func (d *DB) ListTags(ctx context.Context, namespace string, includeSuggested bool) ([]Tag, error) {
	q := `SELECT id, project_id, name, namespace, parent_id, relation, suggested FROM tags`
	var args []any
	var where []string
	if namespace != "" {
		where = append(where, "namespace = ?")
		args = append(args, namespace)
	}
	if !includeSuggested {
		where = append(where, "suggested = 0")
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY COALESCE(namespace,''), name"

	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tag{}
	for rows.Next() {
		var t Tag
		var pid, parent sql.NullInt64
		var ns, rel sql.NullString
		if err := rows.Scan(&t.ID, &pid, &t.Name, &ns, &parent, &rel, &t.Suggested); err != nil {
			return nil, err
		}
		t.ProjectID, t.ParentID = pid.Int64, parent.Int64
		t.Namespace, t.Relation = ns.String, rel.String
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return d.attachAliases(ctx, out)
}

func (d *DB) attachAliases(ctx context.Context, tags []Tag) ([]Tag, error) {
	if len(tags) == 0 {
		return tags, nil
	}
	rows, err := d.QueryContext(ctx, `SELECT tag_id, alias FROM tag_aliases ORDER BY alias`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byTag := map[int64][]string{}
	for rows.Next() {
		var id int64
		var a string
		if err := rows.Scan(&id, &a); err != nil {
			return nil, err
		}
		byTag[id] = append(byTag[id], a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range tags {
		tags[i].Aliases = byTag[tags[i].ID]
	}
	return tags, nil
}

// ApplyExistingProjectTag attaches an EXISTING tag to a project.
//
// This replaces ApplyProjectTag's behaviour of silently creating tags: §4.3
// requires suggesting existing tags before allowing new ones, and creating a tag
// is now a quorum-ratified taxonomy proposal. A tag that does not exist is an
// error naming the proposal route, not a side effect.
func (d *DB) ApplyExistingProjectTag(ctx context.Context, slug string, tag string, appliedBy int64) error {
	projectID, err := d.projectIDBySlug(ctx, slug)
	if err != nil {
		return err
	}
	tagID, err := d.globalTagID(ctx, tag)
	if err != nil {
		return fmt.Errorf("%w: propose it with POST /api/v1/taxonomy/proposals (action=create)", err)
	}
	var who any
	if appliedBy > 0 {
		who = appliedBy
	}
	if _, err := d.ExecContext(ctx, `
		INSERT OR IGNORE INTO project_tags (project_id, tag_id, applied_by, created_at)
		VALUES (?, ?, ?, ?)`, projectID, tagID, who, float64(time.Now().Unix())); err != nil {
		return err
	}
	return d.ReindexProject(ctx, projectID)
}

// TagCompleteness reports how well-tagged a project is, for the §4.3
// completeness meter and the "under-tagged projects" queue.
//
// A project with no tags is not merely untidy: it is invisible to every
// taxonomy-filtered search, which is why the spec treats completeness as a
// contribution rather than housekeeping.
func (d *DB) TagCompleteness(ctx context.Context, slug string) (map[string]any, error) {
	id, err := d.projectIDBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	var total int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_tags WHERE project_id=?`, id).Scan(&total); err != nil {
		return nil, err
	}
	var namespaced, suggested int
	if err := d.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM project_tags pt JOIN tags t ON t.id = pt.tag_id
		WHERE pt.project_id = ? AND t.namespace IS NOT NULL`, id).Scan(&namespaced); err != nil {
		return nil, err
	}
	if err := d.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM project_tags pt JOIN tags t ON t.id = pt.tag_id
		WHERE pt.project_id = ? AND t.suggested = 1`, id).Scan(&suggested); err != nil {
		return nil, err
	}

	// 5 tags with at least 2 namespaced is "complete": enough to be findable by
	// both the category filters and the hierarchy queries. The threshold is a
	// judgement, and it is one number in one place so it can be argued with.
	const wantTags, wantNamespaced = 5, 2
	score := 0.0
	if total >= wantTags {
		score += 0.6
	} else if total > 0 {
		score += 0.6 * float64(total) / float64(wantTags)
	}
	if namespaced >= wantNamespaced {
		score += 0.4
	} else if namespaced > 0 {
		score += 0.4 * float64(namespaced) / float64(wantNamespaced)
	}

	return map[string]any{
		"project_id":        id,
		"tags":              total,
		"namespaced":        namespaced,
		"suggested_pending": suggested,
		"complete":          total >= wantTags && namespaced >= wantNamespaced,
		"score":             score,
		"want_tags":         wantTags,
		"want_namespaced":   wantNamespaced,
	}, nil
}
