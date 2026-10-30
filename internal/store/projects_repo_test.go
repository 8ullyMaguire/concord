package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// R9's parse half. The table is the specification: every spelling a person types collapses to
// one canonical value, and every non-repository is refused with a reason that names what it
// parsed. A permissive parse is how `https://example.com/x` becomes a project's "repository"
// and every surface links a stranger at a 404.

func TestParseRepoURLNormalises(t *testing.T) {
	cases := map[string]string{
		"https://github.com/owner/repo":         "https://github.com/owner/repo",
		"https://github.com/owner/repo.git":     "https://github.com/owner/repo",
		"https://github.com/owner/repo/":        "https://github.com/owner/repo",
		"http://github.com/owner/repo":          "https://github.com/owner/repo",
		"  https://GitHub.com/owner/repo.git  ": "https://github.com/owner/repo",
		"git@github.com:owner/repo.git":         "https://github.com/owner/repo",
		"ssh://git@github.com/owner/repo":       "https://github.com/owner/repo",
		"https://codeberg.org/o/r":              "https://codeberg.org/o/r",
		"https://git.polarisocial.xyz/o/r.git":  "https://git.polarisocial.xyz/o/r",
	}
	for in, want := range cases {
		got, err := ParseRepoURL(in)
		if err != nil {
			t.Errorf("ParseRepoURL(%q) errored: %v; want %s", in, err, want)
			continue
		}
		if got.URL != want {
			t.Errorf("ParseRepoURL(%q).URL = %q, want %q", in, got.URL, want)
		}
		if got.Owner == "" || got.Repo == "" || got.Full != got.Owner+"/"+got.Repo {
			t.Errorf("ParseRepoURL(%q) = %+v, want owner/repo/full filled", in, got)
		}
	}
}

func TestParseRepoURLHostMatchesTheForge(t *testing.T) {
	got, err := ParseRepoURL("https://git.polarisocial.xyz/hirrolot19/tessera")
	if err != nil {
		t.Fatalf("ParseRepoURL: %v", err)
	}
	if got.Host != RepoHostForgejo {
		t.Fatalf("host = %q, want %q: the ingest dispatches on this value", got.Host, RepoHostForgejo)
	}
}

func TestParseRepoURLRefusesAnUnknownHostNamingIt(t *testing.T) {
	_, err := ParseRepoURL("https://example.com/owner/repo")
	if err == nil {
		t.Fatal("example.com was accepted as a forge")
	}
	if !strings.Contains(err.Error(), "example.com") {
		t.Fatalf("error %q does not name the host it parsed; a refusal a person cannot act on "+
			"is a wall", err)
	}
}

func TestParseRepoURLRefusesAPathThatIsNotOwnerRepo(t *testing.T) {
	for _, in := range []string{
		"https://github.com/owner",
		"https://github.com/owner/repo/tree/main",
		"https://github.com/",
		"github.com/owner/repo", // no scheme: a bare host is a search term, not a repository
	} {
		if _, err := ParseRepoURL(in); err == nil {
			t.Errorf("ParseRepoURL(%q) was accepted; a repository is exactly owner/repo", in)
		}
	}
}

func TestSetProjectRepoRefusesAMismatchedHost(t *testing.T) {
	store, _, _ := setupWithProject(t)
	// setupWithProject always creates this slug.
	const slug = "test-project"
	_, err := store.SetProjectRepo(context.Background(), slug, "https://github.com/owner/repo", RepoHostForgejo)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid: the host is what the ingest switches on, so it must "+
			"agree with the URL rather than be corrected", err)
	}
	// control: the same URL with the host it really is must be accepted.
	if _, err := store.SetProjectRepo(context.Background(), slug, "https://github.com/owner/repo", RepoHostGithub); err != nil {
		t.Fatalf("control: SetProjectRepo with the matching host: %v", err)
	}
}

func TestSetProjectRepoStoresTheCanonicalURL(t *testing.T) {
	store, _, _ := setupWithProject(t)
	ctx := context.Background()
	const slug = "test-project"
	got, err := store.SetProjectRepo(ctx, slug, "git@github.com:owner/repo.git", RepoHostGithub)
	if err != nil {
		t.Fatalf("SetProjectRepo: %v", err)
	}
	if got.RepoURL != "https://github.com/owner/repo" {
		t.Fatalf("stored %q, want the canonical form", got.RepoURL)
	}
	rows, err := store.ListProjectsWithRepo(ctx)
	if err != nil {
		t.Fatalf("ListProjectsWithRepo: %v", err)
	}
	if len(rows) != 1 || rows[0].Slug != slug || rows[0].RepoHost != RepoHostGithub {
		t.Fatalf("ListProjectsWithRepo = %+v, want exactly %s on github", rows, slug)
	}
}

func TestListProjectsWithRepoSkipsProjectsWithNoRepository(t *testing.T) {
	store, uid, _ := setupWithProject(t)
	ctx := context.Background()
	if _, err := store.CreateProject(ctx, uid, "no-repo", "No repo", "", "collective", ""); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := store.SetProjectRepo(ctx, "test-project", "https://github.com/owner/repo", RepoHostGithub); err != nil {
		t.Fatalf("SetProjectRepo: %v", err)
	}
	rows, err := store.ListProjectsWithRepo(ctx)
	if err != nil {
		t.Fatalf("ListProjectsWithRepo: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: a project with no stated repository has none to fetch", len(rows))
	}
}

func TestProjectSlugsCoversTheCatalog(t *testing.T) {
	store, uid, _ := setupWithProject(t)
	ctx := context.Background()
	if _, err := store.CreateProject(ctx, uid, "second", "Second", "", "collective", ""); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	slugs, err := store.ProjectSlugs(ctx)
	if err != nil {
		t.Fatalf("ProjectSlugs: %v", err)
	}
	// The seed gate refuses a mapping that misses any of these, so the list has to be the
	// whole catalog and not just the measured part of it.
	if len(slugs) != 2 {
		t.Fatalf("slugs = %v, want both projects", slugs)
	}
	for i := 1; i < len(slugs); i++ {
		if slugs[i-1] > slugs[i] {
			t.Fatalf("slugs are not ordered: %v", slugs)
		}
	}
}

func TestClearProjectRepoEmptiesTheRow(t *testing.T) {
	store, _, _ := setupWithProject(t)
	ctx := context.Background()
	if _, err := store.SetProjectRepo(ctx, "test-project", "https://github.com/owner/repo", RepoHostGithub); err != nil {
		t.Fatalf("SetProjectRepo: %v", err)
	}
	if err := store.ClearProjectRepo(ctx, "test-project"); err != nil {
		t.Fatalf("ClearProjectRepo: %v", err)
	}
	rows, err := store.ListProjectsWithRepo(ctx)
	if err != nil {
		t.Fatalf("ListProjectsWithRepo: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("after clearing, %d project(s) still report a repository: %+v -- the hourly "+
			"ingest would keep fetching a URL the mapping no longer states", len(rows), rows)
	}
	// And it must be idempotent: clearing twice is not an error, because a mapping stays a skip
	// for as long as the owner has not stated a repository.
	if err := store.ClearProjectRepo(ctx, "test-project"); err != nil {
		t.Fatalf("second ClearProjectRepo: %v", err)
	}
}
