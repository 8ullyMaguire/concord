package finder

import "testing"

// R11's vocabulary. These are the four things a contribution feed has to say that the Finder
// catalog cannot, and each test here exists because the wrong version of it is silent: a bucket
// that classifies an unstated budget as fifteen minutes, or a key that cannot be mapped back to a
// row, both produce a page that looks fine and sends somebody to the wrong task.

func TestMinutesBucket(t *testing.T) {
	fifteen, twenty, sixty, sixtyOne, allDay := 15, 20, 60, 61, 600
	cases := []struct {
		in   *int
		want string
		why  string
	}{
		{nil, "", "unstated is not a bucket; the caller must leave the attribute out"},
		{&fifteen, Minutes15, "exactly fifteen fits fifteen"},
		{&twenty, Minutes60, "twenty minutes fits in an hour"},
		{&sixty, Minutes60, "the boundary is inclusive"},
		{&sixtyOne, Minutes480, "sixty-one minutes does not fit in an hour"},
		{&allDay, Minutes480, "ten hours needs the longest bucket"},
		{new(int), "", "a zero budget is refused by the schema; treat it as unstated here too"},
	}
	for _, c := range cases {
		if got := MinutesBucket(c.in); got != c.want {
			t.Errorf("MinutesBucket(%v) = %q, want %q (%s)", c.in, got, c.want, c.why)
		}
	}
}

func TestWorkItemCandidateOmitsAnUnstatedBudget(t *testing.T) {
	none := WorkItemCandidate(7, "proj", "Unsized task", 3, nil, false, "",
		map[string]string{"language": "go", "cap:wip-limits": "yes"})

	if _, present := none.Attrs[DimMinutes]; present {
		t.Fatalf("an unsized item carries minutes=%q; a guessed bucket would exclude it from "+
			"every other answer", none.Attrs[DimMinutes])
	}
	if _, present := none.Attrs[DimDifficulty]; present {
		t.Fatal("an unlabelled item carries a difficulty nobody stated")
	}
	if none.Attrs["language"] != "go" || none.Attrs["cap:wip-limits"] != "yes" {
		t.Fatalf("the item did not inherit the project's dimensions: %v", none.Attrs)
	}
	// No mentor attached is a FACT, not an absence, so it is written either way.
	if none.Attrs[DimMentor] != "no" {
		t.Fatalf("a mentored-less item has mentor=%q, want %q", none.Attrs[DimMentor], "no")
	}
	if none.ProjectID != 7 || none.Name != "Unsized task" {
		t.Fatalf("candidate = %+v, want the item's own identity", none)
	}
}

func TestDifficultyWordTranslatesTheStoresShorthand(t *testing.T) {
	// S/M/L is what the label column holds; easy/medium/hard is what the feed asks and what a
	// URL says. A STORED "S" that reached the page as "S" would filter against an answer set
	// that has no "S" in it, and every difficulty filter would return nothing.
	for stored, want := range map[string]string{"S": "easy", "M": "medium", "L": "hard", "": ""} {
		if got := DifficultyWord(stored); got != want {
			t.Errorf("DifficultyWord(%q) = %q, want %q", stored, got, want)
		}
	}
	// Not guessed into a bucket: an unknown word passes through so it can be seen and fixed,
	// rather than being silently sized as "medium".
	if got := DifficultyWord("XXL"); got != "xxl" {
		t.Fatalf("DifficultyWord(XXL) = %q, want the value passed through", got)
	}
}

func TestWorkItemCandidateOverridesInheritedValuesWithItsOwn(t *testing.T) {
	// A project-wide "easy" would be a lie about one hard task, so the item's own label must win.
	ten := 10
	c := WorkItemCandidate(1, "proj", "Rework the parser", 9, &ten, true, "L",
		map[string]string{"language": "go", DimDifficulty: "easy", DimMentor: "no"})
	if c.Attrs[DimDifficulty] != "hard" {
		t.Fatalf("difficulty = %q, want the item's own label", c.Attrs[DimDifficulty])
	}
	if c.Attrs[DimMentor] != "yes" {
		t.Fatalf("mentor = %q, want the item's own value", c.Attrs[DimMentor])
	}
	if c.Attrs[DimMinutes] != Minutes15 {
		t.Fatalf("minutes = %q, want the item's own budget bucketed", c.Attrs[DimMinutes])
	}
}

func TestItemKeyRoundTrips(t *testing.T) {
	key := ItemKey("stash-box", 42)
	slug, id, ok := ItemIDFromKey(key)
	if !ok || slug != "stash-box" || id != 42 {
		t.Fatalf("ItemIDFromKey(%q) = (%q, %d, %v), want (stash-box, 42, true)", key, slug, id, ok)
	}
	// A slug with a dash or a dot must survive: slugs are user-facing text, not identifiers.
	key = ItemKey("git.polarisocial.xyz-project", 7)
	if slug, id, ok := ItemIDFromKey(key); !ok || slug != "git.polarisocial.xyz-project" || id != 7 {
		t.Fatalf("a slug with punctuation did not round-trip: (%q, %d, %v)", slug, id, ok)
	}
}

func TestItemIDFromKeyRefusesGarbage(t *testing.T) {
	for _, in := range []string{"", "proj", "proj#", "proj#x", "#7", "proj#7#9x"} {
		if _, _, ok := ItemIDFromKey(in); ok {
			t.Errorf("ItemIDFromKey(%q) claimed to parse; a guessed id renders somebody else's task", in)
		}
	}
}

func TestOrderWorkItemsPutsUnmeasuredLast(t *testing.T) {
	health := map[int64]*float64{}
	good, poor := 0.9, 0.1
	health[1] = &good
	health[2] = &poor
	// Project 3 has no measurement at all.

	items := []Ranked{
		{Candidate: Candidate{ProjectID: 3, Slug: "c#1"}, Fit: 0.5},
		{Candidate: Candidate{ProjectID: 2, Slug: "b#1"}, Fit: 0.5},
		{Candidate: Candidate{ProjectID: 1, Slug: "a#1"}, Fit: 0.5},
	}
	OrderWorkItems(items, health)

	if items[0].ProjectID != 1 || items[1].ProjectID != 2 || items[2].ProjectID != 3 {
		t.Fatalf("order = %d,%d,%d, want health desc with the unmeasured project LAST",
			items[0].ProjectID, items[1].ProjectID, items[2].ProjectID)
	}

	// Fit outranks health: a genuinely better match in a sick project still comes first.
	items = []Ranked{
		{Candidate: Candidate{ProjectID: 1, Slug: "a#1"}, Fit: 0.2},
		{Candidate: Candidate{ProjectID: 2, Slug: "b#1"}, Fit: 0.9},
	}
	OrderWorkItems(items, health)
	if items[0].ProjectID != 2 {
		t.Fatalf("fit did not outrank health: %v then %v", items[0], items[1])
	}
}

func TestContributeQuestionsAreTheKeysTheCandidatesCarry(t *testing.T) {
	// The page renders these questions and the handler turns the answers into engine keys. If a
	// question key is not one the candidate builder writes, the filter matches nothing and the
	// feed answers "no results" for every choice -- silently.
	written := map[string]bool{
		"language":    true,
		DimMinutes:    true,
		DimDifficulty: true,
		DimMentor:     true,
	}
	for _, q := range ContributeQuestions() {
		if !written[q.Key] {
			t.Errorf("question %q is not a key any candidate carries", q.Key)
		}
		if q.Family == "" || q.Label == "" {
			t.Errorf("question %q has no family or label: the page cannot render it", q.Key)
		}
		if len(q.Options) == 0 {
			t.Errorf("question %q offers no answers", q.Key)
		}
	}
}

func TestContributeFamiliesAreClassified(t *testing.T) {
	// classify() is what turns a dimension key into a question family. These two keys must not
	// fall through to the capability default, or the feed's questions show up labelled as
	// capabilities in anything that lists dimensions.
	for key, want := range map[string]string{
		DimMinutes:    FamilyEffort,
		DimDifficulty: FamilyEffort,
		DimMentor:     FamilySupport,
	} {
		if got, _ := classify(key); got != want {
			t.Errorf("classify(%q) = %q, want %q", key, got, want)
		}
	}
}
