package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlySessionOwnsTheSessionWritePath holds the rule that session.js is where
// the session is WRITTEN, not merely where the key names are spelled.
//
// The defect this exists for was in the tree with a comment describing it:
// auth.js wrote `localStorage.setItem('concord.token', …)` directly, under a
// comment saying that repeating the literals is how the two files drift apart and
// a person ends up "signed in" to a header that cannot see the token that signed
// them in. The guest mint in consent.js then copied the same two lines. A file
// that documents a hazard while committing it is not a lesser problem than one
// that does it silently -- it is the version a reader trusts.
//
// Two directions, because a one-directional gate here would pass for the wrong
// reason: no file outside session.js may write, AND session.js must still write
// both keys (removing the write path entirely must not satisfy the first half),
// AND the key literals must still be the ones the rest of the codebase reads --
// the scattered readers (`session.js`, `consensus.js`, `list.js` and others) are
// left alone by this pass, but a RENAME in the owner file would silently
// sign everybody out.
//
// What this is NOT: it cannot see a write done through a variable
// (`localStorage[method](key, value)`), and it says nothing about the many
// direct READS of `concord.token` elsewhere -- those are a real inconsistency,
// documented in docs/specs/frontend-spec.md §4.4 and deliberately not swept in
// the same change.
func TestOnlySessionOwnsTheSessionWritePath(t *testing.T) {
	const owner = "session.js"
	dir := filepath.Join("assets", "js")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("cannot read %s: %v", dir, err)
	}

	scanned := 0
	var offenders []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		scanned++
		if e.Name() == owner {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, call := range []string{"localStorage.setItem", "localStorage.removeItem"} {
			if strings.Contains(string(src), call) {
				offenders = append(offenders, e.Name()+" calls "+call)
			}
		}
	}
	if scanned < 20 {
		t.Fatalf("scanned %d scripts; there are ~25 and a scan that finds a handful "+
			"is measuring nothing", scanned)
	}
	if len(offenders) > 0 {
		t.Errorf("the session write path is not session.js's alone:\n  %s\n"+
			"Use window.ConcordSession.instance().store(token, user) / .clear().",
			strings.Join(offenders, "\n  "))
	}

	ownerSrc, err := os.ReadFile(filepath.Join(dir, owner))
	if err != nil {
		t.Fatalf("read %s: %v", owner, err)
	}
	for _, want := range []string{
		"var TOKEN_KEY = 'concord.token'",
		"var USER_KEY = 'concord.user'",
		"localStorage.setItem(TOKEN_KEY",
		"localStorage.setItem(USER_KEY",
		"localStorage.removeItem(TOKEN_KEY)",
		"localStorage.removeItem(USER_KEY)",
	} {
		if !strings.Contains(string(ownerSrc), want) {
			t.Errorf("session.js no longer contains %q. It is the only file allowed to "+
				"write the session, so the write path cannot be removed from it -- and the "+
				"key literals are what ~8 other scripts read", want)
		}
	}
}
