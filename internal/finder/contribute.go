package finder

import (
	"sort"
	"strconv"
	"strings"
)

// The contribution feed (R11, plan CF-2).
//
// The feed answers "what can I do today, on something healthy, with the skills I have?" -- and
// the whole design constraint is that it answers with the SAME engine as Finder. There is no
// second ranker here: a work item is turned into a Candidate, the existing Apply removes the
// ones the contributor's constraints exclude, and the existing Score orders them. What this file
// adds is VOCABULARY -- the dimensions a contribution feed needs and Finder's catalog does not
// have -- because a task's time budget is not a capability and inventing a parallel scoring path
// for it is how the two surfaces would come to disagree.
const (
	// FamilyEffort covers "how much of my evening does this cost".
	FamilyEffort = "effort"
	// FamilySupport covers "can I get unstuck".
	FamilySupport = "support"
)

// The dimension keys the feed writes into Candidate.Attrs. They are plain dimension keys to the
// engine, so a contribution question gets the same unknown-keeps, tri-state and tie handling as
// any other -- no special case in Apply or Score.
const (
	DimMinutes    = "minutes"
	DimMentor     = "mentor"
	DimDifficulty = "difficulty"
)

// The buckets are the answer set, not an estimate of the work. A budget of 20 minutes buckets to
// "60" because a contributor who has an hour can do it; the bucket is the coarsest statement
// that is still true. An item with NO stated budget carries no attribute at all, which the engine
// treats as unknown and keeps (finder-spec §2.7) -- "nobody sized this" is not "this will not fit".
const (
	Minutes15  = "15"
	Minutes60  = "60"
	Minutes480 = "480"
	MinutesAny = "any"
)

// ContributeQuestions is the feed's declared question set, in the order the page offers it. It
// exists so the page, the API and this file cannot drift apart about the vocabulary: the handler
// serves it, the page renders it, and a test asserts the three agree.
func ContributeQuestions() []Dimension {
	return []Dimension{
		{
			Key:    "language",
			Family: FamilyPlatform,
			Label:  "What do you write?",
			Kind:   "single",
			// Options are filled from the live candidate set by the handler: a hard-coded
			// language list would offer answers no project in this instance can satisfy.
			Options: []Option{{ID: ModeAny, Label: "Any language"}},
		},
		{
			Key:    DimMinutes,
			Family: FamilyEffort,
			Label:  "How long do you have?",
			Kind:   "single",
			Options: []Option{
				{ID: Minutes15, Label: "About fifteen minutes"},
				{ID: Minutes60, Label: "Up to an hour"},
				{ID: Minutes480, Label: "Most of a day"},
				{ID: ModeAny, Label: "Any length"},
			},
		},
		{
			Key:    DimDifficulty,
			Family: FamilyEffort,
			Label:  "How much of a run-up do you want?",
			Kind:   "single",
			Options: []Option{
				{ID: "easy", Label: "Something gentle"},
				{ID: "medium", Label: "A real puzzle"},
				{ID: "hard", Label: "Throw me in"},
				{ID: ModeAny, Label: "Any difficulty"},
			},
		},
		{
			Key:    DimMentor,
			Family: FamilySupport,
			Label:  "Do you want someone to ask?",
			Kind:   "single",
			Options: []Option{
				{ID: "yes", Label: "Yes — I'd rather not be alone"},
				{ID: "no", Label: "No, I'll find my way"},
				{ID: ModeAny, Label: "Either"},
			},
		},
	}
}

// MinutesBucket collapses a stated budget onto the answer set. It returns "" for an unstated
// budget, which callers must NOT write into Attrs: an empty value is indistinguishable from
// unknown in Apply, but a PRESENT key with a guess would silently exclude the item from every
// other answer.
func MinutesBucket(minutes *int) string {
	if minutes == nil || *minutes <= 0 {
		return ""
	}
	switch {
	case *minutes <= 15:
		return Minutes15
	case *minutes <= 60:
		return Minutes60
	default:
		return Minutes480
	}
}

// WorkItemCandidate builds the engine's Candidate for one work item.
//
// Slug carries "project#item" because Score returns Ranked values and the caller has to map each
// one back to the row it came from; ProjectID alone cannot do that when four items come from the
// same project. It is display-only here -- nothing in the engine parses it.
func WorkItemCandidate(projectID int64, projectSlug, title string, itemID int64, minutes *int, needsMentor bool, difficulty string, inherited map[string]string) Candidate {
	attrs := map[string]string{}
	// The item inherits the PROJECT's dimensions: a task in a project written in Go is Go work,
	// and a project with a confirmed capability is a place to work on that capability. Only the
	// task's own dimensions are overridden below.
	for k, v := range inherited {
		attrs[k] = v
	}
	if b := MinutesBucket(minutes); b != "" {
		attrs[DimMinutes] = b
	}
	if w := DifficultyWord(difficulty); w != "" {
		attrs[DimDifficulty] = w
	}
	// needs_mentor is a fact either way: false is "nobody is attached", which is a real answer
	// and must not be confused with the missing attribute the engine keeps as unknown.
	if needsMentor {
		attrs[DimMentor] = "yes"
	} else {
		attrs[DimMentor] = "no"
	}
	return Candidate{
		ProjectID: projectID,
		Slug:      ItemKey(projectSlug, itemID),
		Name:      title,
		Attrs:     attrs,
	}
}

// DifficultyWord translates the store's shorthand onto the feed's vocabulary.
//
// The label column holds S/M/L -- maintainers' shorthand, chosen when the work-item contract
// shipped -- and a newcomer looking for their first task should not have to learn that "S" means
// "gentle". Mapping here rather than widening the column keeps the API's URLs readable
// (?difficulty=medium) without a migration, and keeps ONE vocabulary rather than two.
//
// An unrecognised value is passed through lowercased rather than mapped to something plausible:
// a filter that then excludes it is correct, and the unfiltered feed still shows it. Guessing
// "medium" for a value nobody defined would put a newcomer on work sized wrong for them.
func DifficultyWord(stored string) string {
	switch strings.ToUpper(strings.TrimSpace(stored)) {
	case "S":
		return "easy"
	case "M":
		return "medium"
	case "L":
		return "hard"
	case "":
		return ""
	default:
		return strings.ToLower(strings.TrimSpace(stored))
	}
}

// ItemKey is the identity Score echoes back for one work item.
func ItemKey(projectSlug string, itemID int64) string {
	return projectSlug + "#" + strconv.FormatInt(itemID, 10)
}

// ItemIDFromKey reverses ItemKey. It returns false rather than a zero for anything it did not
// build, because a caller that guessed here would render somebody else's task.
func ItemIDFromKey(key string) (string, int64, bool) {
	i := strings.LastIndex(key, "#")
	// i == 0 is refused as well: an empty slug would render a link to nothing, and the only
	// way to produce one is a key this file did not build.
	if i <= 0 || i == len(key)-1 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(key[i+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return key[:i], n, true
}

// OrderWorkItems applies the feed's FINAL tiebreak after the engine has ranked.
//
// Fit desc, then project health desc with unmeasured last, then the newer item first. The health
// term is the one that makes this a contribution FEED rather than a task list: two tasks that fit
// equally are not equal if one is in a project whose last commit was in 2023, and an unmeasured
// project must never outrank a measured one on the strength of having no data.
func OrderWorkItems(items []Ranked, health map[int64]*float64) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Fit != b.Fit {
			return a.Fit > b.Fit
		}
		ha, hb := health[a.ProjectID], health[b.ProjectID]
		if (ha == nil) != (hb == nil) {
			return ha != nil
		}
		if ha != nil && *ha != *hb {
			return *ha > *hb
		}
		return a.Slug > b.Slug
	})
}
