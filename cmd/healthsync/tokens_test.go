package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A GITHUB TOKEN MUST NEVER REACH THE SELF-HOSTED FORGE.
//
// This is the regression the live pass found on 2026-11-12: `CONCORD_FORGE_TOKEN` held a GitHub
// personal access token, the fetcher set `Authorization: token <pat>` on EVERY request, and
// git.polarisocial.xyz answered 401 `user does not exist [uid: 0, name: , keyid: 0]` for all 17
// of its repositories — which had measured fine anonymously the day before. Adding a credential
// turned "failed 0 of 23" into "failed 17 of 23".
//
// The test uses a real server and reads the header the server actually received, because the
// defect was not "the wrong value in a variable" — it was the header being sent at all.
func TestAGitHubTokenNeverTravelsToAnotherHost(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// The map is what the environment produces from a GitHub PAT alone. Nothing in it names
	// this test server, so nothing may be sent to it.
	f := newFetcher(forgeTokens(func(k string) string {
		if k == "CONCORD_FORGE_TOKEN" {
			return "ghp_github_only"
		}
		return ""
	}), 0)
	if err := f.get(context.Background(), srv.URL+"/api/v1/version", nil); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("expected one request, saw %d", len(seen))
	}
	if seen[0] != "" {
		t.Fatalf("a GitHub token was sent to %s: %q", srv.URL, seen[0])
	}

	// And the positive half: a credential registered FOR this host is sent, in the form the
	// forges accept. Without this the test above would also pass on a fetcher that never
	// authenticated anything.
	host := srv.Listener.Addr().String()
	f2 := newFetcher(map[string]string{host: "issued_here"}, 0)
	if err := f2.get(context.Background(), srv.URL+"/x", nil); err != nil {
		t.Fatalf("get with a token: %v", err)
	}
	if got := seen[len(seen)-1]; got != "token issued_here" {
		t.Fatalf("header for the issuing host = %q, want %q", got, "token issued_here")
	}
}

// The second call site, for the same reason: getTotal counts pages and set the same header.
func TestThePageCounterSendsNoForeignTokenEither(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Header().Set("X-Total-Count", "7")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	f := newFetcher(map[string]string{"api.github.com": "ghp_github_only"}, 0)
	n, err := f.getTotal(context.Background(), srv.URL+"/commits?limit=1")
	if err != nil {
		t.Fatalf("getTotal: %v", err)
	}
	if n != 7 {
		t.Fatalf("count = %d, want 7", n)
	}
	if seen != "" {
		t.Fatalf("getTotal sent %q to a host its token was not issued for", seen)
	}
}

func TestForgeTokensReadsBothShapes(t *testing.T) {
	env := map[string]string{
		"CONCORD_FORGE_TOKEN": "ghp_github_only",
		// One good pair, one without a token, one with empty host, one with spaces: the
		// parser is fed operator typing, so the malformed entries must be skipped rather
		// than producing a host with an empty credential.
		"CONCORD_FORGE_TOKENS": " git.polarisocial.xyz = fj_token , broken ,=emptyhost, nokey ",
	}
	tokens := forgeTokens(func(k string) string { return env[k] })

	want := map[string]string{
		"api.github.com":       "ghp_github_only",
		"github.com":           "ghp_github_only",
		"git.polarisocial.xyz": "fj_token",
	}
	for host, wantTok := range want {
		if got := tokens[host]; got != wantTok {
			t.Errorf("token for %s = %q, want %q", host, got, wantTok)
		}
	}
	if len(tokens) != len(want) {
		t.Fatalf("parsed %d hosts, want %d: %v", len(tokens), len(want), tokens)
	}
}

// A private forge needs its own entry, and until it has one the reads stay anonymous.
func TestNoCredentialsInTheEnvironmentMeansNoHeader(t *testing.T) {
	if got := forgeTokens(func(string) string { return "" }); len(got) != 0 {
		t.Fatalf("an empty environment produced %v", got)
	}
}

// The PR sampling depth follows the same rule: a token for another host must not make the pass
// ask a forge that never authenticated it for ten pull requests instead of three.
func TestSamplingDepthFollowsTheIssuingHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("authorised a request to a host with no token: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode([]any{})
	}))
	defer srv.Close()

	f := newFetcher(map[string]string{"api.github.com": "ghp_github_only"}, time.Millisecond)
	if got := f.authFor(srv.URL); got != "" {
		t.Fatalf("authFor(%s) = %q, want empty", srv.URL, got)
	}
	if got := f.authFor("https://api.github.com/repos/x/y"); got != "ghp_github_only" {
		t.Fatalf("authFor(github) = %q", got)
	}
}
