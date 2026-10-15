package httpapi

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This gate exists because the class it guards was invisible for the life of the
// project: a page's reads went out anonymous. On a public project that returns
// exactly what a signed-in reader gets, so nothing could fail -- until the
// navigation cookie let a member reach a PROTECTED project's page, whose panels
// then 404ed one by one because the page's own fetches carried no credential.
// 26 call sites in 10 files, and five hand-built copies of "add Authorization if
// there is a token" which is five chances to leave the token out (two had).
//
// The rule: every fetch() whose URL names an /api/v1 path must present a token
// carrier IN ITS OWN ARGUMENTS -- apiAuth(...), authHeaders(...), Authorization,
// or a file-local headers() that delegates to the shared helper. One call site is
// exempt and enumerated below, and the exemption is asserted to be exactly one, so
// a second anonymous call cannot hide behind it.
//
// What this rule is NOT, stated so the test is not read as stronger than it is:
//
//   - It is a source check, not a behavioural one. It cannot see through a
//     variable, which is why the helper call has to be AT the call site rather
//     than hoisted -- a hoisted `var headers = apiAuth(...)` followed by
//     `headers: headers` is correct code that this test cannot verify, so every
//     such site was inlined instead.
//   - A fetch whose URL arrives in a variable is not examined at all. This is a
//     floor, not a census.
//   - It says nothing about whether the server accepts the token, or about a
//     route whose handler ignores the actor it was given.
func TestEveryAPIFetchPresentsAToken(t *testing.T) {
	carriers := []string{"apiAuth", "authHeaders", "Authorization", "headers()"}

	// The one legitimate anonymous call site: the login and registration submit.
	// It cannot present a token -- it is the request that mints one -- and the
	// path is built from a variable, which is also why it is spelled out here by
	// file and count rather than matched by URL.
	const exemptFile = "auth.js"
	const exemptCount = 1

	dir := filepath.Join("assets", "js")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("cannot read %s: %v", dir, err)
	}

	total, exempted := 0, 0
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read %s: %v", path, err)
		}
		s := string(src)
		for _, call := range fetchCalls(s) {
			if !strings.Contains(call.arg, "/api/v1") {
				continue
			}
			total++
			carried := false
			for _, c := range carriers {
				if strings.Contains(call.arg, c) {
					carried = true
					break
				}
			}
			if carried {
				continue
			}
			if e.Name() == exemptFile {
				exempted++
				continue
			}
			names = append(names, e.Name())
			t.Errorf("%s:%d fetches an API path with no token carrier: %s\n"+
				"Add apiAuth(...) to the call's headers (see search.js for the shape).",
				e.Name(), call.line, oneLine(call.arg))
		}
	}

	if total == 0 {
		t.Fatal("no /api/v1 fetch call sites were examined: the scanner is broken, " +
			"which would make this test pass while guarding nothing")
	}
	if exempted > exemptCount {
		t.Errorf("%s has %d anonymous API call sites, and the exemption is %d. The "+
			"exemption is the login/registration submit; a second one is a read that "+
			"forgot its credential (files: %v)", exemptFile, exempted, exemptCount, names)
	}
	t.Logf("examined %d authenticated API call sites, %d exempted (login)", total-exempted, exempted)
}

type fetchCall struct {
	line int
	arg  string
}

var fetchRe = regexp.MustCompile(`\bfetch\(`)

// fetchCalls returns every fetch( call site in s with its argument-list text,
// found by matching parentheses so a nested call or an object literal does not
// cut the argument short.
func fetchCalls(s string) []fetchCall {
	var out []fetchCall
	for _, m := range fetchRe.FindAllStringIndex(s, -1) {
		start := m[1]
		depth := 1
		i := start
		for i < len(s) && depth > 0 {
			switch s[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
			i++
		}
		if depth != 0 {
			continue // unbalanced: not a call we can read
		}
		out = append(out, fetchCall{
			line: strings.Count(s[:m[0]], "\n") + 1,
			arg:  s[start : i-1],
		})
	}
	return out
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
