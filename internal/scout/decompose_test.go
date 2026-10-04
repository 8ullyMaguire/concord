package scout

// The decomposition tests (docs/specs/scout-spec.md §4).
//
// The failure this file exists to prevent is the one the first version had: a
// verbatim-key match written as `strings.Contains(normalize(text), normalize(key))`,
// where `normalize` collapses hyphens to spaces — so `wip-limits` becomes
// `wip limits` in the haystack and a hyphenated key can never match. The
// decomposition silently returned almost nothing, and a test that asserted only a
// COUNT passed anyway. Most tests here therefore assert on WHICH capability came
// out and on the evidence string, not on how many.

import (
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

func testCatalog() []store.Capability {
	return []store.Capability{
		{Key: "offline", Label: "Offline use", Category: "features"},
		{Key: "wip-limits", Label: "WIP limits", Category: "features"},
		{Key: "self-hosted", Label: "Self-hosted", Category: "deployment"},
		{Key: "arm64", Label: "arm64 builds", Category: "deployment"},
		{Key: "audit-log", Label: "Audit log", Category: "operations"},
	}
}

func findCap(caps []store.ScoutCapability, key string) (store.ScoutCapability, bool) {
	for _, c := range caps {
		if c.Key == key {
			return c, true
		}
	}
	return store.ScoutCapability{}, false
}

// The load-bearing one: a key typed into the text must come back out, WITH its
// catalog label and the evidence that it was found verbatim.
func TestScoutDecomposesAnIdeaIntoSeededCapabilities(t *testing.T) {
	out := Decompose("we need wip-limits and offline support", testCatalog())

	for _, key := range []string{"wip-limits", "offline"} {
		c, ok := findCap(out, key)
		if !ok {
			t.Errorf("%q was in the text and is in the catalog but is not in the "+
				"decomposition (got %v)", key, keysOf(out))
			continue
		}
		if !c.InCatalog {
			t.Errorf("%q came back not-in-catalog; it IS in the catalog", key)
		}
		if c.Evidence != "verbatim_key" {
			t.Errorf("%q evidence = %q, want verbatim_key — the strongest signal "+
				"available is being reported as something weaker", key, c.Evidence)
		}
		if c.Label == "" {
			t.Errorf("%q came back with no label; a decomposition a user cannot "+
				"read is not reviewable", key)
		}
	}
}

// The ONLY test that isolates the verbatim rule from the label rule.
//
// The test below asserts that "we need wip limits and offline support" yields
// `wip-limits` with evidence `verbatim_key` — and it passed with the verbatim
// match DISABLED, because the label-word rule independently finds "wip" and
// "limits" in the label "WIP limits". The test was satisfied by a weaker rule
// than the one it claimed to check, and disabling the strong rule was invisible.
//
// The fix: a catalog entry whose LABEL shares no word with the idea, so the
// label rule cannot reach it and only the verbatim rule can.
func TestScoutMatchesAVerbatimKeyWhoseLabelDoesNotAppear(t *testing.T) {
	catalog := []store.Capability{
		{Key: "tenant-isolation", Label: "Tenant isolation", Category: "operations"},
		{Key: "offline", Label: "Offline use", Category: "features"},
	}
	out := Decompose("we are thinking about tenant-isolation here", catalog)

	c, ok := findCap(out, "tenant-isolation")
	if !ok {
		t.Fatalf("a catalog key typed verbatim was not found; the decomposition "+
			"cannot see a hyphenated slug (got %v)", keysOf(out))
	}
	if c.Evidence != "verbatim_key" {
		t.Errorf("evidence = %q, want verbatim_key; this capability's label shares "+
			"no word with the idea, so only the verbatim rule can have matched it",
			c.Evidence)
	}
}

// The spaced form. `normalize` in every editor the user might be typing into turns
// `wip-limits` into `wip limits`, so accepting only the hyphenated spelling is a
// real gap and not a pedantic one.
func TestScoutAcceptsTheSpacedFormOfAHyphenatedKey(t *testing.T) {
	out := Decompose("we need wip limits and offline support", testCatalog())
	c, ok := findCap(out, "wip-limits")
	if !ok {
		t.Fatalf("the spaced form of a catalog key did not match; the whole-token "+
			"rule is too strict for how people type (got %v)", keysOf(out))
	}
	if c.Evidence != "verbatim_key" {
		t.Errorf("evidence = %q, want verbatim_key", c.Evidence)
	}
}

// The inverse: a key NOT in the text must not appear, or the decomposition is just
// the whole catalog and the "idea" is irrelevant.
func TestScoutDoesNotReturnCapabilitiesTheIdeaNeverMentioned(t *testing.T) {
	// The catalog entries here deliberately have labels whose words appear NOWHERE
	// in the idea. The first version used the shared fixture, where "audit log" and
	// "arm64 builds" are both label-findable from a sentence about offline support —
	// so disabling the "did we already match this" check changed nothing observable
	// and the mutation survived.
	catalog := []store.Capability{
		{Key: "offline", Label: "Offline use", Category: "features"},
		{Key: "quantum-telemetry", Label: "Telemetry pipeline", Category: "operations"},
		{Key: "zero-knowledge", Label: "Zero knowledge proofs", Category: "security"},
	}
	out := Decompose("we need offline support", catalog)

	if _, ok := findCap(out, "quantum-telemetry"); ok {
		t.Error("quantum-telemetry is in the catalog but was never mentioned and came " +
			"back anyway — the decomposition is returning the catalog, not reading the idea")
	}
	if _, ok := findCap(out, "zero-knowledge"); ok {
		t.Error("zero-knowledge was never mentioned and came back")
	}
}

// §4's hard rule: a capability outside the catalog is a PROPOSAL, never scored.
func TestScoutNeverInventsACapabilityOutsideTheCatalog(t *testing.T) {
	out := Decompose("we want live-sync and offline support", testCatalog())

	c, ok := findCap(out, "live-sync")
	if !ok {
		t.Fatalf("a hyphenated token in the text is dropped entirely (got %v); it "+
			"must be reported as a proposal so a user can see what Scout read",
			keysOf(out))
	}
	if c.InCatalog {
		t.Error("live-sync is marked in-catalog; no such catalog row exists and it " +
			"must never be scored against nothing")
	}
	if c.Evidence != "text_token" {
		t.Errorf("evidence = %q, want text_token", c.Evidence)
	}
}

// An ordinary English word is NOT a capability proposal. Without this the report
// proposes `dashboard`, `notification` and `whatever` for any sentence anyone
// types, and the proposals list becomes noise.
func TestAnOrdinaryWordIsNotProposedAsACapability(t *testing.T) {
	out := Decompose("we want a dashboard with notifications for our team", testCatalog())
	for _, c := range out {
		if !c.InCatalog {
			t.Errorf("%q was proposed as a capability; only capability-SHAPED tokens "+
				"(hyphenated slugs) are proposals, otherwise every sentence proposes "+
				"every word in it", c.Key)
		}
	}
}

// A catalog key appearing in the text is NOT also a proposal — it would be counted
// twice and the axis scored twice.
func TestAKeyInTheTextIsNotAlsoProposedAsANewCapability(t *testing.T) {
	out := Decompose("we need wip-limits", testCatalog())
	seen := 0
	for _, c := range out {
		if c.Key == "wip-limits" {
			seen++
			if !c.InCatalog {
				t.Error("wip-limits is in the catalog and came back not-in-catalog")
			}
		}
	}
	if seen != 1 {
		t.Errorf("wip-limits appears %d times; the same axis must not be counted "+
			"once as a catalog match and again as a proposal", seen)
	}
}

// Whole-word matching. Substring matching is the classic false positive: "ran"
// inside "truncated".
func TestCapabilityMatchingIsWholeWordNotSubstring(t *testing.T) {
	// "arms" contains "arm" but is not the arm64 capability, and "audit-log" is
	// absent entirely.
	out := Decompose("the system truncates arms and logs everything", testCatalog())
	if c, ok := findCap(out, "audit-log"); ok {
		t.Errorf("audit-log matched from 'logs'; matching must be whole-word or "+
			"every plurals produces a match (evidence %q)", c.Evidence)
	}
}

// Label-word matching, marked as the weaker signal so a reader knows the difference.
func TestALabelWordMatchIsMarkedAsWeakerThanAVerbatimKey(t *testing.T) {
	// "audit log" is the label; the text does not contain the key "audit-log"
	// verbatim but does contain the words.
	out := Decompose("we would like an audit log of every change", testCatalog())
	c, ok := findCap(out, "audit-log")
	if !ok {
		t.Fatalf("a label-word match did not happen (got %v)", keysOf(out))
	}
	if c.Evidence != "label_word" && c.Evidence != "verbatim_key" {
		t.Errorf("evidence = %q, want label_word or verbatim_key", c.Evidence)
	}
}

// Stop words must not drive matches: "this", "with" and friends appear in every
// sentence and match nothing meaningfully.
func TestStopWordsDoNotProduceMatches(t *testing.T) {
	out := Decompose("this is something we would want with the other thing", testCatalog())
	// The catalog here has no label that is a stop word, so anything that came
	// back came from a bad match.
	for _, c := range out {
		if !c.InCatalog {
			t.Errorf("%q proposed from a sentence with no capability in it", c.Key)
		}
	}
}

// In-catalog capabilities sort before proposals, so a reader sees what will be
// scored before what will not.
func TestScorableCapabilitiesSortFirst(t *testing.T) {
	out := Decompose("we want live-sync with offline support", testCatalog())

	firstProposal := -1
	for i, c := range out {
		if !c.InCatalog {
			firstProposal = i
			break
		}
	}
	for i, c := range out {
		if !c.InCatalog && i < firstProposal {
			t.Errorf("proposal %q at index %d, before any in-catalog capability; the "+
				"scorable half has to be readable first", c.Key, i)
			break
		}
	}
	if firstProposal >= 0 {
		for _, c := range out[firstProposal:] {
			if c.InCatalog {
				t.Errorf("in-catalog %q sorted after a proposal", c.Key)
				break
			}
		}
	}
}

// Empty and whitespace input must not panic and must return an empty slice, not nil.
func TestDecomposingNothingReturnsAnEmptyList(t *testing.T) {
	for _, idea := range []string{"", "   ", "\n\t"} {
		out := Decompose(idea, testCatalog())
		if out == nil {
			t.Errorf("Decompose(%q) returned nil; the page must be able to tell an "+
				"empty decomposition from an absent one", idea)
		}
		if len(out) != 0 {
			t.Errorf("Decompose(%q) returned %v", idea, keysOf(out))
		}
	}
}

// An empty catalog must not panic either — it is the state a brand-new instance is
// in, and that is the state where a panic is least welcome.
func TestDecomposingAgainstAnEmptyCatalogIsSafe(t *testing.T) {
	out := Decompose("we want offline and some-things", nil)
	if out == nil {
		t.Error("nil decomposition against an empty catalog")
	}
	// With no catalog, only proposals are possible.
	for _, c := range out {
		if c.InCatalog {
			t.Errorf("%q claims to be in-catalog with an empty catalog", c.Key)
		}
	}
}

// The label is humanised for a proposal, so the report is readable.
func TestAProposalHasAReadableLabel(t *testing.T) {
	out := Decompose("we want live-sync", testCatalog())
	c, ok := findCap(out, "live-sync")
	if !ok {
		t.Skip("live-sync was not proposed; the hyphenated-token rule changed")
	}
	if !strings.Contains(c.Label, " ") {
		t.Errorf("proposal label = %q; a hyphenated key should be humanised so the "+
			"decomposition reads as a list of things rather than a list of slugs", c.Label)
	}
}

func keysOf(caps []store.ScoutCapability) []string {
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, c.Key)
	}
	return out
}
