package discovery

import "sort"

// ForgeEvent is one thing that happened in a repository. It is the input to
// newcomerResponse, and it lives here rather than in store because store
// already imports discovery (`UpdateProjectMetrics` takes a discovery.Metrics)
// and the other direction would be an import cycle.
//
// The table it mirrors is `forge_events` (0031_repo_identity.sql). The audit
// log answers "what did the instance do"; this answers "what happened in the
// repository". Collapsing the two loses the ability to recompute a median
// after a rule change, which is the whole reason the event table exists.
type ForgeEvent struct {
	RepoFull string
	Kind     string // push | pr_opened | pr_closed | pr_review | pr_comment
	Number   int    // PR number, 0 for push
	Actor    string // who did it: the PR author for pr_opened, the responder otherwise
	At       float64
}

// Kind values the table's CHECK constraint permits.
const (
	ForgeEventPush      = "push"
	ForgeEventPROpened  = "pr_opened"
	ForgeEventPRClosed  = "pr_closed"
	ForgeEventPRReview  = "pr_review"
	ForgeEventPRComment = "pr_comment"
)

// NewcomerMinSample is the sample size below which no number is published.
//
// Five is not a statistical threshold, it is an editorial one: a median over
// three PRs is a number nobody should act on, and publishing it invites a
// reader to rank projects on it. Below five the project is ABSENT from the
// ranking, never ranked last -- "we do not know" and "worst" are different
// facts and only one of them is true.
const NewcomerMinSample = 5

// NewcomerResponse computes the median hours from a newcomer's first PR in a
// repository to the first response by somebody else.
//
// EXPORTED because cmd/healthsync is the only caller and it is a different
// package: the rule belongs here (it is a scoring rule, and discovery already
// holds the health weights) while the reading of `forge_events` belongs to
// store. Keeping it unexported left the two newcomer columns in
// project_metrics unwritten by any code path, which is the state R12 was
// half-built in.
//
// THREE RULES, EACH OF WHICH CHANGED THE NUMBER WHEN IT WAS WRONG:
//
//  1. ONLY A FIRST PR COUNTS. A contributor's second PR is not a newcomer
//     experience; they already know whether the maintainers answer.
//  2. A SELF-COMMENT IS NOT A RESPONSE. Counting the author's own follow-up
//     would publish a responsiveness number that no contributor experienced.
//  3. THE LOWER-MIDDLE MEDIAN, NOT AN INTERPOLATED ONE. With {2h, 10h} the
//     answer is 2, not 6 -- a median that interpolates invents a response time
//     nobody had.
//
// ok is false under NewcomerMinSample observations. hours is meaningless then
// and the caller must publish NULL rather than 0: a zero-hour median claims
// the instance answered instantly, which is the best possible score.
func NewcomerResponse(events []ForgeEvent) (hours float64, n int, ok bool) {
	// A PR's author, keyed by (repo, number). Only pr_opened carries it; a
	// review or comment arriving for a PR we never saw opened is skipped,
	// because without the author we cannot tell whether they are a newcomer
	// and without the open time there is no interval to measure.
	type prKey struct {
		repo string
		num  int
	}
	author := map[prKey]string{}
	openedAt := map[prKey]float64{}

	for _, e := range events {
		if e.Kind != ForgeEventPROpened {
			continue
		}
		k := prKey{e.RepoFull, e.Number}
		author[k] = e.Actor
		openedAt[k] = e.At
	}

	// First observed PR time per (repo, author), so "first PR" is decided by
	// the earliest opening, not by slice order -- a webhook delivers out of
	// order after a redelivery and slice order is not a fact about time.
	firstPR := map[string]map[string]float64{} // repo -> author -> earliest at
	for k, a := range author {
		if firstPR[k.repo] == nil {
			firstPR[k.repo] = map[string]float64{}
		}
		if cur, seen := firstPR[k.repo][a]; !seen || openedAt[k] < cur {
			firstPR[k.repo][a] = openedAt[k]
		}
	}

	// Earliest response by somebody else, per PR.
	responseAt := map[prKey]float64{}
	for _, e := range events {
		if e.Kind != ForgeEventPRReview && e.Kind != ForgeEventPRComment {
			continue
		}
		k := prKey{e.RepoFull, e.Number}
		a, known := author[k]
		if !known {
			continue // a response to a PR we never saw opened
		}
		if e.Actor == a {
			continue // rule 2: a self-comment is not a response
		}
		if cur, seen := responseAt[k]; !seen || e.At < cur {
			responseAt[k] = e.At
		}
	}

	samples := make([]float64, 0, len(responseAt))
	for k, resp := range responseAt {
		a := author[k]
		// rule 1: only the author's first PR in this repository.
		if openedAt[k] != firstPR[k.repo][a] {
			continue
		}
		h := (resp - openedAt[k]) / 3600
		if h < 0 {
			// A response timestamped before the opening is a clock skew or a
			// mis-delivered payload, not a negative wait. Skipping it is the
			// only honest option: including it would pull the median down with
			// an event that cannot have happened in that order.
			continue
		}
		samples = append(samples, h)
	}

	if len(samples) < NewcomerMinSample {
		return 0, len(samples), false
	}
	sort.Float64s(samples)
	// rule 3: lower-middle.
	return samples[(len(samples)-1)/2], len(samples), true
}
