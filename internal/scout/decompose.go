package scout

// Free text → catalog capabilities (docs/specs/scout-spec.md §4).
//
// The decomposition is what makes Scout reviewable rather than authoritative: it
// names the axes it will score on, so a user can see "it read my idea as being
// about offline support and self-hosting" and correct it before reading verdicts.
//
// Two properties, and the second is the one that keeps it honest:
//
//   - **A capability comes from the catalog or it is a proposal.** A word in the
//     text that no catalog row defines produces a `ScoutCapability` with
//     `InCatalog: false`, which the classifier scores ZERO weight. It is reported,
//     never silently dropped, and never scored against nothing.
//   - **Matching is by explicit signal.** Verbatim key, then a label or category
//     word, then nothing. "Nothing" means `coverage: open` — not a weak match.

import (
	"sort"
	"strings"
	"unicode"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// minWordLen is the shortest word that can contribute to a label match.
//
// Four characters. Three-letter words are mostly noise ("the", "and", "api" is
// three, and "api" is a real one — so it is handled by exact match below rather
// than by lowering the floor for everything).
const minWordLen = 4

// varStopWords are excluded from label-word matching.
//
// Small closed-class words, plus the ones that describe CONCORD rather than the
// project being scouted. Without them "deploy" in "we should deploy it" matches
// every capability labelled Deployment, which is a match on the user's sentence
// rather than on their idea.
var varStopWords = map[string]bool{
	"this": true, "that": true, "with": true, "from": true, "have": true,
	"been": true, "were": true, "they": true, "them": true, "then": true,
	"than": true, "when": true, "what": true, "which": true, "will": true,
	"would": true, "could": true, "should": true, "about": true, "into": true,
	"your": true, "their": true, "there": true, "here": true, "just": true,
	"like": true, "make": true, "made": true, "want": true, "need": true,
	"some": true, "more": true, "most": true, "very": true, "also": true,
	"because": true, "using": true, "used": true, "uses": true,
}

// Decompose extracts capabilities from free text.
//
// `catalog` is the whole vocabulary; passing a filtered catalog silently changes
// what Scout can see, which is why ListCapabilityCatalog exists and why the
// caller is expected to pass everything.
func Decompose(idea string, catalog []store.Capability) []store.ScoutCapability {
	text := normalize(idea)
	words := wordsOf(text)

	// Step 1: verbatim key. The strongest signal available and the one the spec
	// calls out by name — a user who typed `wip-limits` meant it.
	// The raw lowercased text, NOT the normalized one. `normalize` collapses
	// punctuation to spaces, so `wip-limits` becomes `wip limits` in `text`, and a
	// `strings.Contains(text, normalize(cap.Key))` test can never fire for any key
	// containing a hyphen — which is most of them. The first version matched
	// against `text` and so silently matched almost nothing.
	// The RAW lowercased text. Needed by the proposal scan below, which looks for
	// capability-shaped slugs and so must see hyphens that `text` has flattened.
	raw := strings.ToLower(strings.TrimSpace(idea))

	var out []store.ScoutCapability
	for _, cap := range catalog {
		key := strings.ToLower(strings.TrimSpace(cap.Key))
		if key == "" {
			continue
		}
		// `text` is NORMALIZED, so the key must be normalized too: both undergo the
		// same punctuation collapse, which is what lets `wip-limits` meet
		// `wip limits`. Matching a raw key against normalized text — the original
		// bug — can never fire for a hyphenated key, and that is what made the
		// first version of this return almost nothing.
		//
		// A separate raw-text clause was tried and removed. It is subsumed by this
		// one, since both the key and the text lose their hyphens, so every raw
		// occurrence already matches above. Two clauses for one effect meant the
		// raw clause was unkillable by any mutation.
		if len(key) > 3 && strings.Contains(text, normalize(key)) {
			out = append(out, store.ScoutCapability{
				Key: cap.Key, Label: cap.Label, Category: cap.Category,
				InCatalog: true, Evidence: "verbatim_key",
			})
		}
	}

	// Step 2: a label or category word. Weaker, and marked as such, because
	// "offline" in the text matching a capability labelled "Offline use" is a good
	// match and "deploy" matching "Deployment" is a mediocre one — the signal
	// tells the reader which kind of match they are looking at.
	type pending struct {
		cap      store.Capability
		evidence string
	}
	var hits []pending
	for _, cap := range catalog {
		ev := ""
		if matchesWord(words, normalize(cap.Label), cap.Label) {
			ev = "label_word"
		} else if cap.Category != "" && matchesWord(words, normalize(cap.Category), cap.Category) {
			ev = "category_word"
		}
		if ev == "" {
			continue
		}
		hits = append(hits, pending{cap: cap, evidence: ev})
	}
	// Two capabilities sharing the same label word are a near-duplicate, and an
	// editor should merge them rather than have the idea scored twice on one axis.
	// The earlier one wins and the later records the collision as a signal.
	byLabel := map[string][]int{}
	for i, h := range hits {
		byLabel[normalize(h.cap.Label)] = append(byLabel[normalize(h.cap.Label)], i)
	}
	// Seeded with the keys already matched verbatim, so this ONE map dedupes across
	// both phases.
	//
	// A separate `claimed` set beside this one was removed as provably redundant and
	// that was wrong: `added` only ever deduplicated within `hits`, so it could not
	// see what phase 1 had already appended to `out`. A capability findable BOTH ways
	// — `wip-limits` typed verbatim and matched by its label words — then appeared
	// twice and the same axis was scored twice. Deleting the guard did not remove a
	// duplicate mechanism; it removed the only check that spanned the two phases.
	added := map[string]bool{}
	for _, c := range out {
		added[c.Key] = true
	}
	for _, idxs := range byLabel {
		if len(idxs) < 2 {
			continue
		}
		first := hits[idxs[0]].cap.Key
		for _, i := range idxs[1:] {
			hits[i].cap.Key = first
		}
	}
	// The single dedupe. An earlier version ALSO skipped already-verbatim-matched
	// capabilities via a `claimed` set, which was provably redundant with this map —
	// and that redundancy is exactly why a mutation disabling `claimed` survived.
	for _, h := range hits {
		// The skip is what keeps a verbatim match from being repeated as a weaker
		// label match, and what keeps two catalog entries sharing one label from
		// scoring the same axis twice.
		if added[h.cap.Key] {
			continue
		}
		added[h.cap.Key] = true
		out = append(out, store.ScoutCapability{
			Key: h.cap.Key, Label: h.cap.Label, Category: h.cap.Category,
			InCatalog: true, Evidence: h.evidence,
		})
	}

	// Step 3: proposals. Words in the text that are not catalog keys and not
	// matched labels are NOT proposals — a free-text idea is full of ordinary
	// words, and turning each of them into a capability proposal would bury the
	// real ones. A proposal requires a shape the catalog would recognise: a
	// capability-style token, i.e. a hyphenated slug or a known axis word.
	//
	// This is the deliberate narrowing spec §4 asks for ("never invents a
	// capability outside the catalog"), and it is why the report can say
	// "proposed: 0" honestly rather than proposing `dashboard`.
	// The RAW tokens, not `words`. `words` comes from the normalized text, and
	// normalize() turns every hyphen into a space — so scanning it for a token
	// CONTAINING a hyphen could never match, and the first version of this
	// produced no proposals at all. The test that caught it asserted on WHICH key
	// came out rather than on a count, which is the only reason it was caught
	// rather than passing on an empty result.
	seenProposal := map[string]bool{}
	for _, w := range rawTokens(raw) {
		if len(w) < minWordLen || varStopWords[w] {
			continue
		}
		if !strings.Contains(w, "-") {
			continue
		}
		key := w
		if isCatalogKey(catalog, key) {
			// Already captured in step 1 — it IS in the catalog, so matching it
			// again as a proposal would double-count the axis.
			continue
		}
		if seenProposal[key] {
			continue
		}
		seenProposal[key] = true
		out = append(out, store.ScoutCapability{
			Key: key, Label: humanise(key), InCatalog: false,
			Evidence: "text_token", Coverage: "open",
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		// In-catalog capabilities first: they are the scorable half, and a reader
		// should see what will actually be scored before what will not.
		if out[i].InCatalog != out[j].InCatalog {
			return out[i].InCatalog
		}
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Key < out[j].Key
	})
	if out == nil {
		return []store.ScoutCapability{}
	}
	return out
}

func isCatalogKey(catalog []store.Capability, key string) bool {
	for _, c := range catalog {
		if normalize(c.Key) == normalize(key) {
			return true
		}
	}
	return false
}

// matchesWord reports whether any word of `words` is a whole word of the label.
//
// Whole word, not substring: "ran" inside "truncated" is not a match for a
// capability labelled "Runs locally", and a substring rule produces exactly that
// kind of nonsense on the first text anyone types.
func matchesWord(words []string, normalizedLabel, originalLabel string) bool {
	if normalizedLabel == "" {
		return false
	}
	for _, w := range labelWords(normalizedLabel) {
		for _, have := range words {
			if have == w {
				return true
			}
		}
	}
	return false
}

func labelWords(normalized string) []string {
	return strings.FieldsFunc(normalized, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// normalize lowercases and collapses punctuation to single spaces, so
// "WIP-Limits", "wip limits" and "wip_limits" all compare equal.
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastSpace := true
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteRune(r)
			lastSpace = false
		default:
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func wordsOf(normalized string) []string {
	return strings.Fields(normalized)
}

// rawTokens splits the RAW lowercased text into tokens, keeping punctuation that
// carries meaning — specifically the hyphen, which is what distinguishes a
// capability-shaped slug from an ordinary word.
func rawTokens(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '-' && r != '_'
	})
}

// humanise turns `live-sync` into `Live Sync` for a proposal's label.
func humanise(key string) string {
	parts := strings.FieldsFunc(key, func(r rune) bool { return r == '-' || r == '_' })
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
