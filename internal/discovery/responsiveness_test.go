package discovery

import "testing"

func prOpened(repo string, num int, author string, at float64) ForgeEvent {
	return ForgeEvent{RepoFull: repo, Kind: ForgeEventPROpened, Number: num, Actor: author, At: at}
}

func prResponse(repo string, num int, actor string, at float64) ForgeEvent {
	return ForgeEvent{RepoFull: repo, Kind: ForgeEventPRReview, Number: num, Actor: actor, At: at}
}

// seedN builds n first-time PRs by distinct authors, opened at 0 and answered
// `hours` later by somebody else.
func seedN(n int, hours ...float64) []ForgeEvent {
	var evs []ForgeEvent
	for i := 0; i < n; i++ {
		author := string(rune('a' + i))
		evs = append(evs,
			prOpened("o/r", i+1, author, 0),
			prResponse("o/r", i+1, "maintainer", hours[i]*3600),
		)
	}
	return evs
}

func TestNewcomerResponseUnderFiveSamplesIsNotPublished(t *testing.T) {
	_, n, ok := NewcomerResponse(seedN(4, 1, 2, 3, 4))
	if ok {
		t.Fatalf("4 samples published a number; the floor is %d", NewcomerMinSample)
	}
	if n != 4 {
		t.Fatalf("n = %d, want 4 (the caller still reports how far off it is)", n)
	}
}

func TestNewcomerResponseExactlyFiveSamplesIsPublished(t *testing.T) {
	hours, n, ok := NewcomerResponse(seedN(5, 1, 2, 3, 4, 5))
	if !ok {
		t.Fatalf("5 samples did not publish; the floor is %d", NewcomerMinSample)
	}
	if n != 5 {
		t.Fatalf("n = %d, want 5", n)
	}
	if hours != 3 {
		t.Fatalf("median = %v, want 3 (lower-middle of 1,2,3,4,5)", hours)
	}
}

func TestNewcomerResponseEvenSampleUsesLowerMiddle(t *testing.T) {
	// Six samples of 1..6h. Lower-middle picks index 2 (3h). An interpolating
	// median would answer 3.5, a response time nobody experienced.
	hours, n, ok := NewcomerResponse(seedN(6, 1, 2, 3, 4, 5, 6))
	if !ok || n != 6 {
		t.Fatalf("ok=%v n=%d, want true/6", ok, n)
	}
	if hours != 3 {
		t.Fatalf("median = %v, want 3 (lower-middle, not the interpolated 3.5)", hours)
	}
}

func TestNewcomerResponseSelfCommentIsNotAResponse(t *testing.T) {
	// Five authors open PRs; each answers their own. Nothing was responded to.
	var evs []ForgeEvent
	for i := 0; i < 5; i++ {
		author := string(rune('a' + i))
		evs = append(evs,
			prOpened("o/r", i+1, author, 0),
			ForgeEvent{RepoFull: "o/r", Kind: ForgeEventPRComment, Number: i + 1, Actor: author, At: 3600},
		)
	}
	_, n, ok := NewcomerResponse(evs)
	if ok || n != 0 {
		t.Fatalf("ok=%v n=%d, want false/0: a self-comment is not a response", ok, n)
	}
}

func TestNewcomerResponseSecondPRBySameAuthorIsExcluded(t *testing.T) {
	// One author opens two PRs, both answered. Only the first is a newcomer
	// experience, so n is 1 -- and with a floor of five that is not published.
	evs := []ForgeEvent{
		prOpened("o/r", 1, "alice", 0),
		prResponse("o/r", 1, "maint", 3600),
		prOpened("o/r", 2, "alice", 7200),
		prResponse("o/r", 2, "maint", 10800),
	}
	_, n, ok := NewcomerResponse(evs)
	if ok || n != 1 {
		t.Fatalf("ok=%v n=%d, want false/1: only the author's first PR counts", ok, n)
	}
}

func TestNewcomerResponseToUnseenPRIsIgnored(t *testing.T) {
	// A review for a PR whose opening we never saw: without the author we
	// cannot tell whether they are a newcomer, and without the open time
	// there is no interval.
	evs := []ForgeEvent{prResponse("o/r", 99, "maint", 3600)}
	_, n, ok := NewcomerResponse(evs)
	if ok || n != 0 {
		t.Fatalf("ok=%v n=%d, want false/0", ok, n)
	}
}

func TestNewcomerResponseFirstTimeIsByEarliestOpeningNotSliceOrder(t *testing.T) {
	// Alice's PRs arrive out of order: the webhook delivered #2 first. Her
	// FIRST PR is still #1 (opened earlier), so the sample must be #1's 100h
	// response -- not #2's 1h one.
	//
	// The numbers are chosen so the two paths give DIFFERENT medians. Alice's
	// two possible samples are 100h (correct) and 1h (slice order); with the
	// four 1..4h padding samples that is a median of 3 versus 2. A test where
	// both paths agree would pass while the rule was absent.
	evs := []ForgeEvent{
		prOpened("o/r", 2, "alice", 7200),
		prResponse("o/r", 2, "maint", 7200+1*3600),
		prOpened("o/r", 1, "alice", 0),
		prResponse("o/r", 1, "maint", 100*3600),
		prOpened("o/r", 3, "bob", 0), prResponse("o/r", 3, "maint", 1*3600),
		prOpened("o/r", 4, "carol", 0), prResponse("o/r", 4, "maint", 2*3600),
		prOpened("o/r", 5, "dave", 0), prResponse("o/r", 5, "maint", 3*3600),
		prOpened("o/r", 6, "erin", 0), prResponse("o/r", 6, "maint", 4*3600),
	}
	hours, n, ok := NewcomerResponse(evs)
	if !ok || n != 5 {
		t.Fatalf("ok=%v n=%d, want true/5 (alice contributes one sample)", ok, n)
	}
	// Correct samples {1,2,3,4,100} -> lower-middle 3. Slice order would give
	// alice 1h, {1,1,2,3,4} -> 2.
	if hours != 3 {
		t.Fatalf("median = %v, want 3: the first PR is decided by the earliest opening (slice order gives 2)", hours)
	}
}

func TestNewcomerResponseNegativeIntervalIsSkipped(t *testing.T) {
	// A response timestamped before the opening is clock skew, not a negative
	// wait. Including it would pull the published median down.
	evs := seedN(4, 1, 2, 3, 4)
	evs = append(evs, prOpened("o/r", 50, "zed", 7200), prResponse("o/r", 50, "maint", 3600))
	_, n, ok := NewcomerResponse(evs)
	if ok || n != 4 {
		t.Fatalf("ok=%v n=%d, want false/4: the negative interval is not a sample", ok, n)
	}
}
