package httpapi

import (
	"strings"
	"testing"
)

// Regression test: a form without an explicit method submits as GET.
//
// Found 2026-10-03, during the F1-F7 re-verification pass. Neither auth form
// declared a method, so the browser's default is GET. auth.js intercepts submit
// and posts JSON, so the bug is invisible while that script loads — which is
// every time a human tests it. It appears the moment auth.js is blocked, fails to
// load, or throws before attaching its handler, and then the native submission
// puts the plaintext password into the query string, where it is captured by
// browser history, the server access log, and any Referer sent on the redirect.
//
// Reproduced with CDP Network.setBlockedURLs on *auth.js*, submitting the real
// form, and reading location.href:
//
//   /register?username=leakprobe&display_name=&password=SECRET-abc-123
//
// The assertion below is on the markup, because the markup is what determines the
// fallback. Asserting the JS path instead would pass while the leak is live.

// TestAuthFormsCannotFallBackToGET pins the method on the auth pages.
//
// method="post" is the fix. action="#" means the native POST has nowhere to go,
// so the worst case is a self-referential POST that creates no account rather than
// a GET that publishes the password.
func TestAuthFormsCannotFallBackToGET(t *testing.T) {
	ts := newAuthServer(t)

	for _, tc := range []struct{ path, formID string }{
		{"/login", "login-form"},
		{"/register", "register-form"},
	} {
		body := htmlBody(t, ts, tc.path)
		form := formTag(t, body, tc.formID)
		if form == "" {
			t.Fatalf("%s: no <form id=%q> in the page", tc.path, tc.formID)
		}
		if !strings.Contains(strings.ToLower(form), `method="post"`) {
			t.Errorf("%s: %s has no method=\"post\", so it submits as GET and "+
				"puts the password in the URL when auth.js does not load. Found: %s",
				tc.path, tc.formID, form)
		}
	}
}

// TestProjectFormsDeclarePost covers the complaint and feature forms, which
// project.js builds with innerHTML and so cannot be asserted from a template file.
// Same failure mode: these carry no secret, but a GET fallback puts a filer's
// complaint text into the URL and the access log.
func TestProjectFormsDeclarePost(t *testing.T) {
	ts := newTestServer(t)
	for _, asset := range []string{"project.js"} {
		body := assetBody(t, ts, "/assets/js/"+asset)
		for _, formID := range []string{"complaint-form", "feature-form"} {
			tag := formTag(t, body, formID)
			if tag == "" {
				t.Errorf("%s: no <form id=%q> in the asset", asset, formID)
				continue
			}
			if !strings.Contains(strings.ToLower(tag), `method="post"`) {
				t.Errorf("%s: %s has no method=\"post\"; without it the form "+
					"submits as GET if this script fails to attach its handler. Found: %s",
					asset, formID, tag)
			}
		}
	}
}

// TestSearchFormKeepsItsExplicitMethod guards the one form that legitimately is a
// GET: search. It must keep method="GET", so this test fails if someone "fixes"
// every form at once. A search query is not a secret, and GET is what makes the
// results page bookmarkable.
//
// Identified by action="/search", NOT by an id: the form has no id, and an earlier
// version of this test looked for id="search-box", found nothing, skipped, and so
// stayed green when the mutation was applied. A guard test that cannot find its
// target is worse than no guard test.
func TestSearchFormKeepsItsExplicitMethod(t *testing.T) {
	ts := newTestServer(t)
	body := htmlBody(t, ts, "/search")

	tag := formWithAction(t, body, "/search")
	if tag == "" {
		t.Fatal("the search page has no <form action=\"/search\">; this test " +
			"cannot pin the method it does not find")
	}
	if !strings.Contains(strings.ToLower(tag), `method="get"`) {
		t.Errorf("the search form lost method=\"GET\": %s. A GET search is "+
			"intentional -- it makes results bookmarkable and shareable, and the "+
			"query is not a secret.", tag)
	}
}

// formWithAction returns the single <form ...> tag whose action attribute is
// exactly action. Used where the form carries no id.
func formWithAction(t *testing.T, body, action string) string {
	t.Helper()
	const prefix = "<form"
	rest := body
	for {
		i := strings.Index(rest, prefix)
		if i < 0 {
			return ""
		}
		end := strings.Index(rest[i:], ">")
		if end < 0 {
			return ""
		}
		tag := rest[i : i+end+1]
		if strings.Contains(tag, `action="`+action+`"`) {
			return tag
		}
		rest = rest[i+end+1:]
	}
}

// formTag returns the single <form ...> tag that declares id=formID.
func formTag(t *testing.T, body, formID string) string {
	t.Helper()
	marker := `id="` + formID + `"`
	idx := strings.Index(body, marker)
	if idx < 0 {
		return ""
	}
	start := strings.LastIndex(body[:idx], "<form")
	if start < 0 {
		return ""
	}
	end := strings.Index(body[start:], ">")
	if end < 0 {
		return ""
	}
	return body[start : start+end+1]
}
