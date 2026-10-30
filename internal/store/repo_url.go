package store

import (
	"fmt"
	"strings"
)

// Repository URLs — the parse half of R9 (§4.5.7.1).
//
// The column is STATED, never derived, so this file's job is not to guess a URL from a slug:
// it is to refuse a URL that is not a repository, and to normalise the three shapes a person
// actually types (https with .git, https without, the scp-like `git@host:owner/repo`) into ONE
// canonical value. Without the normalisation the same repository appears twice in the catalog
// under two spellings and the ingest fetches it twice, which is harmless until somebody counts
// contributors.

// repoHosts is the set of forges the ingest knows how to call. A host outside it is REFUSED
// rather than stored: `repo_host` is the switch cmd/healthsync dispatches on, and a host it
// does not know would either never be fetched (silent) or be fetched with the wrong API
// (wrong numbers). The map is the authority; the CHECK constraint agrees with it.
var repoHosts = map[string]string{
	"github.com":           "github",
	"codeberg.org":         "codeberg",
	"git.polarisocial.xyz": "forgejo",
}

// RepoFields is one canonical repository reference.
type RepoFields struct {
	// URL is the canonical https form, no `.git`, no trailing slash.
	URL string
	// Host is one of RepoHostGithub/RepoHostForgejo/RepoHostCodeberg.
	Host string
	// Owner and Repo are the two path segments, for building API URLs.
	Owner string
	Repo  string
	// Full is owner/repo, the key forge events carry (`repo_full`).
	Full string
}

// The host values, matching the migration's CHECK.
const (
	RepoHostGithub   = "github"
	RepoHostForgejo  = "forgejo"
	RepoHostCodeberg = "codeberg"
)

// ParseRepoURL turns what a person typed into one canonical repository reference, or refuses it.
//
// Accepted, all producing `https://github.com/owner/repo`:
//
//	https://github.com/owner/repo
//	https://github.com/owner/repo.git
//	http://github.com/owner/repo/
//	git@github.com:owner/repo.git
//	ssh://git@github.com/owner/repo
//
// Refused, each with the reason a person can act on: a host that is not one of the known
// forges (naming the host it parsed), a path that is not exactly owner/repo, and anything
// that is not a repository URL at all. A permissive parse is how `https://example.com/x`
// becomes a project's "repository" and every later surface links a stranger at a 404.
func ParseRepoURL(raw string) (RepoFields, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return RepoFields{}, fmt.Errorf("empty repository URL")
	}

	var host, path string
	switch {
	case strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://"):
		rest := s[strings.Index(s, "://")+3:]
		host, path, _ = cut(rest, "/")
	case strings.HasPrefix(s, "ssh://"):
		rest := strings.TrimPrefix(s, "ssh://")
		// ssh://git@host/owner/repo -- drop the user, keep host and path.
		if at := strings.Index(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		host, path, _ = cut(rest, "/")
	case strings.Contains(s, "@") && strings.Contains(s, ":"):
		// scp-like: git@host:owner/repo
		rest := s[strings.Index(s, "@")+1:]
		h, p, ok := cut(rest, ":")
		if !ok {
			return RepoFields{}, fmt.Errorf("%q is not a repository URL: no path after the host", raw)
		}
		host, path = h, p
	default:
		return RepoFields{}, fmt.Errorf("%q is not a repository URL: expected https://host/owner/repo", raw)
	}

	host = strings.ToLower(strings.TrimSuffix(host, "/"))
	// Drop a port the way the forge's own URLs do not carry one in the catalog: :443 and :22
	// are transport details, not identity.
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	known, ok := repoHosts[host]
	if !ok {
		return RepoFields{}, fmt.Errorf("host %q is not a known forge (want one of github.com, codeberg.org, %s)",
			host, "git.polarisocial.xyz")
	}

	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return RepoFields{}, fmt.Errorf("%q has %d path segments; a repository is exactly owner/repo", raw, len(parts))
	}
	owner, repo := parts[0], parts[1]
	return RepoFields{
		URL:   fmt.Sprintf("https://%s/%s/%s", host, owner, repo),
		Host:  known,
		Owner: owner,
		Repo:  repo,
		Full:  owner + "/" + repo,
	}, nil
}

// cut splits s at the first occurrence of sep, reporting whether sep was found.
func cut(s, sep string) (before, after string, found bool) {
	if i := strings.Index(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}
