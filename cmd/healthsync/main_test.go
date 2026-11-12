package main

import (
	"encoding/json"
	"errors"
	"testing"
)

// The live shape: `author` is null because the push email is not a Forgejo account, and the only
// identity in the payload is inside `commit`. Reading only `author.login` counted zero people in a
// repository with hundreds of commits, and the project page rendered that zero.
func TestCountForgejoAuthorsFallsBackToTheCommitIdentity(t *testing.T) {
	raw := []byte(`[
	  {"sha":"a","author":null,"commit":{"author":{"name":"hirrolot19","email":"hirrolot19@post.cc"}}},
	  {"sha":"b","author":null,"commit":{"author":{"name":"hirrolot","email":"hirrolot19@post.cc"}}},
	  {"sha":"c","author":null,"commit":{"author":{"name":"someone else","email":"other@example.org"}}}
	]`)
	var commits []forgejoCommit
	if err := json.Unmarshal(raw, &commits); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Two people: the same email twice under two display names is ONE contributor, and a
	// different email is a second. Grouping by name would double a single author who renamed.
	if got := countForgejoAuthors(commits); got != 2 {
		t.Fatalf("counted %d contributors, want 2", got)
	}
}

// When the forge DOES link an account, the login wins: it is the identity the site publishes,
// and a commit made under a different mail address from the same account is still one person.
func TestCountForgejoAuthorsPrefersTheLinkedAccount(t *testing.T) {
	raw := []byte(`[
	  {"sha":"a","author":{"login":"hirrolot19"},"commit":{"author":{"name":"x","email":"one@example.org"}}},
	  {"sha":"b","author":{"login":"hirrolot19"},"commit":{"author":{"name":"y","email":"two@example.org"}}},
	  {"sha":"c","author":{"login":"guest"},"commit":{"author":{"name":"x","email":"one@example.org"}}}
	]`)
	var commits []forgejoCommit
	if err := json.Unmarshal(raw, &commits); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := countForgejoAuthors(commits); got != 2 {
		t.Fatalf("counted %d contributors, want 2 (hirrolot19 and guest)", got)
	}
}

// A page with nothing identifiable counts nobody rather than one anonymous person: a repository
// whose first fifty commits carry no identity at all has no measured breadth, and 0 is the
// honest floor.
func TestCountForgejoAuthorsIgnoresEmptyIdentities(t *testing.T) {
	var commits []forgejoCommit
	raw := []byte(`[{"sha":"a","author":null,"commit":{"author":{"name":"","email":""}}}]`)
	if err := json.Unmarshal(raw, &commits); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := countForgejoAuthors(commits); got != 0 {
		t.Fatalf("counted %d contributors for a page with no identities", got)
	}
}

// The classification that decides whether the timer's journal shows a failure. GitHub's live text
// is reproduced verbatim from the run that made this necessary; the 404 is the case that must
// never be softened, because it means the seeded URL is wrong.
func TestIsRateLimited(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New(`https://api.github.com/repos/x/y -> 403 Forbidden: {"message":"API rate limit exceeded for 90.1.2.3. (But here's the good news: Authenticated requests get a higher rate limit.)"}`), true},
		{errors.New(`https://git.example/api/v1/repos/x/y -> 403 Forbidden: {"message":"rate limit exceeded"}`), true},
		{errors.New(`https://api.github.com/repos/x/y -> 404 Not Found: {"message":"Not Found"}`), false},
		{errors.New(`https://git.polarisocial.xyz/api/v1/repos/a/b -> 404 Not Found: {"error":{"code":"not_found","message":"No such API route."}}`), false},
		{errors.New("dial tcp: lookup api.github.com: i/o timeout"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isRateLimited(c.err); got != c.want {
			t.Errorf("isRateLimited(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
