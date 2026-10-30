package store

import (
	"context"
	"database/sql"

	"fmt"
	"time"
)

// Project repository identity — R9, spec §4.5.7.1.
//
// Two methods and one type. The type exists because the ingest needs three fields and does
// not need a Project: loading the whole row (with its metrics join) for every repository in
// the catalog is the kind of query that works and then gets slower for a reason nobody looks
// at.

// ProjectForge is a project that has a stated repository.
type ProjectForge struct {
	ID       int64  `json:"id"`
	Slug     string `json:"slug"`
	RepoURL  string `json:"repo_url"`
	RepoHost string `json:"repo_host"`
	// Owner and Repo are derived from RepoURL at read time by the caller, not stored: two
	// spellings of one repository in the database is the bug ParseRepoURL exists to prevent.
}

// SetProjectRepo records where a project lives.
//
// The host must AGREE with the URL: `github.com/...` with `repoHost=forgejo` is refused, not
// silently corrected. The ingest switches on the host, so a project stored with the wrong one
// is never fetched and never reports an error -- it just has no health, forever.
//
// The URL is normalised (no `.git`, no trailing slash, https) before it is written, so the
// catalog cannot hold two spellings of one repository.
func (d *DB) SetProjectRepo(ctx context.Context, slug, repoURL, repoHost string) (ProjectForge, error) {
	fields, err := ParseRepoURL(repoURL)
	if err != nil {
		return ProjectForge{}, fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	if repoHost != fields.Host {
		return ProjectForge{}, fmt.Errorf("%w: host %q disagrees with URL %q (it is %q)",
			ErrInvalid, repoHost, repoURL, fields.Host)
	}
	id, err := d.projectIDBySlug(ctx, slug)
	if err != nil {
		return ProjectForge{}, err
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE projects SET repo_url=?, repo_host=?, updated_at=? WHERE id=?`,
		fields.URL, fields.Host, float64(time.Now().Unix()), id); err != nil {
		return ProjectForge{}, fmt.Errorf("set repo: %w", err)
	}
	return ProjectForge{ID: id, Slug: slug, RepoURL: fields.URL, RepoHost: fields.Host}, nil
}

// ClearProjectRepo removes a project's repository, for a mapping that has become a skip.
//
// Separate from SetProjectRepo because there is nothing to parse: an empty URL is not a
// repository, and teaching ParseRepoURL to accept "" would put a value in the caller's hand that
// every other path treats as invalid. A mapping that stops stating a repository must leave the
// row EMPTY, or the hourly ingest keeps fetching a URL the mapping no longer contains -- which is
// what happened on the instance when a remote began answering 404.
func (d *DB) ClearProjectRepo(ctx context.Context, slug string) error {
	id, err := d.projectIDBySlug(ctx, slug)
	if err != nil {
		return err
	}
	if _, err := d.ExecContext(ctx,
		// NULL for both: the CHECK on repo_host allows NULL or a known forge, and '' is neither.
		// An empty string here would be a value that looks like "no forge" to a reader and like
		// a violation to the database.
		`UPDATE projects SET repo_url=NULL, repo_host=NULL, updated_at=? WHERE id=?`,
		float64(time.Now().Unix()), id); err != nil {
		return fmt.Errorf("clear repo: %w", err)
	}
	return nil
}

// ListProjectsWithRepo returns every project that has a repository, for the ingest.
//
// NOT visibility-filtered, deliberately: a private or unlisted project still has a repository
// whose health is a fact about the project, and filtering here would leave it permanently
// unmeasured -- the same "unknown is not zero" rule that keeps a failed fetch from writing
// zeros. Publishing that health is a separate question, answered by the surfaces that read it.
func (d *DB) ListProjectsWithRepo(ctx context.Context) ([]ProjectForge, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, slug, repo_url, repo_host FROM projects
		WHERE repo_url IS NOT NULL AND repo_url <> ''
		ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("list projects with repo: %w", err)
	}
	defer rows.Close()
	out := []ProjectForge{}
	for rows.Next() {
		var p ProjectForge
		var host sql.NullString
		if err := rows.Scan(&p.ID, &p.Slug, &p.RepoURL, &host); err != nil {
			return nil, fmt.Errorf("list projects with repo: scan: %w", err)
		}
		p.RepoHost = host.String
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects with repo: %w", err)
	}
	return out, nil
}

// ProjectSlugs returns every slug in the catalog, for the seed gate: cmd/forgeseed refuses a
// mapping that does not account for all of them, which is what turns "a new project was added"
// into a refusal instead of a project that is silently never measured.
func (d *DB) ProjectSlugs(ctx context.Context) ([]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT slug FROM projects ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("list project slugs: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("list project slugs: scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// §27's rule: a store method with no caller is not shipped. GetProjectRepo (the read half for
// a surface that links out to the repository) is deliberately ABSENT until R11's page needs
// it, rather than added here and kept alive by its own test.
