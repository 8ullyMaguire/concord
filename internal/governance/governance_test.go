package governance

import "testing"

func testCharter() Charter {
	return DefaultCharter(Collective)
}

func TestRoleLevels(t *testing.T) {
	if !(Guest < User) {
		t.Fatal("guest should rank below user")
	}
	for _, less := range []Role{Guest, User, Contributor, Reviewer, Maintainer} {
		if !less.AtLeast(less) {
			t.Fatalf("%s should satisfy its own minimum", less)
		}
		if !Maintainer.AtLeast(less) && less != Maintainer {
			t.Fatalf("maintainer should outrank %s", less)
		}
	}
	if Maintainer.AtLeast(Owner) {
		t.Fatal("maintainer must not outrank owner")
	}
}

func TestQuorumThreshold(t *testing.T) {
	c := testCharter()
	cases := []struct {
		eligible, want int
	}{
		{0, 0},
		{3, 3},  // tiny project: everyone active
		{4, 3},  // ceil(0.8)=1 → floor of 3 applies
		{10, 3}, // ceil(2)=2 → floor of 3 applies
		{20, 4}, // ceil(4)=4
		{100, 20},
	}
	for _, tc := range cases {
		if got := QuorumThreshold(tc.eligible, c); got != tc.want {
			t.Errorf("QuorumThreshold(%d) = %d, want %d", tc.eligible, got, tc.want)
		}
	}
}

func TestEvaluateConsensus(t *testing.T) {
	c := testCharter() // quorum min 3, ratio 0.2; consent 0.7; override 0.8

	t.Run("accepted", func(t *testing.T) {
		cc := ConsensusCounts{Consent: 5, Abstain: 1, Participants: 6, Eligible: 10}
		if got := EvaluateConsensus(cc, c); got != ResultAccepted {
			t.Fatalf("got %q, want accepted (quorum %d)", got, QuorumThreshold(10, c))
		}
	})
	t.Run("insufficient quorum even with unanimous consent", func(t *testing.T) {
		cc := ConsensusCounts{Consent: 2, Participants: 2, Eligible: 10}
		if got := EvaluateConsensus(cc, c); got != ResultInsufficientQuorum {
			t.Fatalf("got %q, want insufficient_quorum", got)
		}
	})
	t.Run("rejected below consent ratio", func(t *testing.T) {
		// Decisive 2/5 = 0.40 < 0.7. Blocks, not stand-asides, are what count
		// against consent here -- see the stand-aside cases below for why that
		// distinction is the point.
		cc := ConsensusCounts{Consent: 2, Block: 3, Participants: 5, Eligible: 10}
		if got := EvaluateConsensus(cc, c); got != ResultRejected {
			t.Fatalf("got %q, want rejected", got)
		}
	})
	t.Run("stand-asides do not count against consent", func(t *testing.T) {
		// 2 consent, 1 stand-aside, no block: decisive 2/2 = 1.0, support
		// 2/3 = 0.67. Passes both thresholds.
		//
		// This case read 2/3 against the single 0.7 threshold and was REJECTED
		// before revision 4, which meant the one person who withheld consent
		// because they had reservations was the person who sank the proposal.
		cc := ConsensusCounts{Consent: 2, StandAside: 1, Block: 0, Participants: 4, Eligible: 10}
		if got := EvaluateConsensus(cc, c); got != ResultAccepted {
			t.Fatalf("got %q, want accepted: a stand-aside is not a vote against", got)
		}
	})
	t.Run("support floor catches a thin majority", func(t *testing.T) {
		// Decisive passes (3/3 = 1.0) but support fails: 3 consent against 5
		// reservations is 0.375 < 0.5. This is the case a single consent
		// ratio cannot express, and the reason support_ratio_min exists.
		cc := ConsensusCounts{Consent: 3, StandAside: 5, Participants: 8, Eligible: 10}
		if got := EvaluateConsensus(cc, c); got != ResultRejected {
			t.Fatalf("got %q, want rejected on the support floor", got)
		}
	})
	t.Run("reluctant consensus is flagged", func(t *testing.T) {
		cc := ConsensusCounts{Consent: 3, StandAside: 4, Participants: 7, Eligible: 10}
		if !cc.Reluctant() {
			t.Fatal("stand-asides outnumber consents, want Reluctant")
		}
		cc = ConsensusCounts{Consent: 4, StandAside: 3, Participants: 7, Eligible: 10}
		if cc.Reluctant() {
			t.Fatal("consents outnumber stand-asides, want not Reluctant")
		}
	})
	t.Run("blocked by unresolved objection", func(t *testing.T) {
		// 6 consent, 2 block: decisive 6/8 = 0.75 -- which clears consent 0.7 but
		// is under override 0.8. Since the eighth pass this is blocked whatever
		// the ratios say, because §6.6's pass conditions end with "No unresolved
		// block" and the override belongs to the second window.
		cc := ConsensusCounts{Consent: 6, Block: 2, Participants: 8, Eligible: 10, OpenObjections: 1}
		if got := EvaluateConsensus(cc, c); got != ResultBlocked {
			t.Fatalf("got %q, want blocked (decisive ratio %.3f)", got, 6.0/8.0)
		}
	})
	t.Run("an open block is blocked even at unanimous consent", func(t *testing.T) {
		// The point of the eighth pass: a first-window close never overrides, so
		// no ratio makes it do so. A runner who deletes the block branch and lets
		// the consent ratio decide would return accepted here.
		cc := ConsensusCounts{Consent: 9, Participants: 9, Eligible: 10, OpenObjections: 1}
		if got := EvaluateConsensus(cc, c); got != ResultBlocked {
			t.Fatalf("got %q, want blocked: 100%% consent does not override a block, the second window does", got)
		}
	})
	t.Run("abstains count toward quorum but not the ratio", func(t *testing.T) {
		cc := ConsensusCounts{Consent: 3, Abstain: 3, Participants: 6, Eligible: 10}
		// quorum 3 met by 6 participants; ratio 3/3 = 1.0
		if got := EvaluateConsensus(cc, c); got != ResultAccepted {
			t.Fatalf("got %q, want accepted", got)
		}
	})
}

// TestEvaluateOverride is §6.6's second window: "≥ 80% of non-abstain
// participants vote to override, with the objector's remedy answered in writing".
// The denominator is the whole point of these fixtures.
func TestEvaluateOverride(t *testing.T) {
	c := testCharter() // quorum min 3, ratio 0.2; override 0.8

	t.Run("supermajority overrides a block", func(t *testing.T) {
		// 8 consent, 1 stand-aside: 8/9 = 0.889 >= 0.8 non-abstain.
		cc := ConsensusCounts{Consent: 8, StandAside: 1, Participants: 9, Eligible: 10, OpenObjections: 1}
		if got := EvaluateOverride(cc, c); got != ResultOverridden {
			t.Fatalf("got %q, want overridden (non-abstain %.3f)", got, cc.SupportRatio())
		}
	})
	t.Run("the discriminating fixture: half a room is not 80% of it", func(t *testing.T) {
		// 4 consent, 3 stand-aside, 1 block. Non-abstain 4/8 = 0.50, decisive
		// 4/5 = 0.80 -- the only shape in the suite where the two candidate
		// denominators disagree about the verdict. The spec's reading says the
		// block stands, and this is why the override moved to its own window.
		cc := ConsensusCounts{Consent: 4, StandAside: 3, Block: 1, Participants: 8, Eligible: 10, OpenObjections: 1}
		if got := EvaluateOverride(cc, c); got != ResultUpheld {
			t.Fatalf("got %q, want upheld: 4 of 8 non-abstain participants is %.3f, under %.2f (the decisive denominator would read %.3f and carry it)", got, cc.SupportRatio(), c.OverrideRatio, cc.DecisiveRatio())
		}
	})
	t.Run("stand-asides raise the bar without vetoing", func(t *testing.T) {
		// The intent the old decisive-denominator test protected survives in the
		// form the spec states it: 8 consent against 2 reservations is exactly
		// 0.80 of non-abstain and carries; add one block and it is 0.727 and the
		// reservation plus the block between them are enough.
		//
		// A stand-aside is not a veto -- 8 consent still overrides 2 of them --
		// but it is a participant who did not consent, and the ratio counts them.
		carries := ConsensusCounts{Consent: 8, StandAside: 2, Participants: 10, Eligible: 10}
		if got := EvaluateOverride(carries, c); got != ResultOverridden {
			t.Fatalf("got %q, want overridden: 8/10 = %.3f is exactly the ratio", got, carries.SupportRatio())
		}
		short := ConsensusCounts{Consent: 8, StandAside: 2, Block: 1, Participants: 11, Eligible: 11}
		if got := EvaluateOverride(short, c); got != ResultUpheld {
			t.Fatalf("got %q, want upheld: 8/11 = %.3f is under the ratio", got, short.SupportRatio())
		}
	})
	t.Run("a reluctant consensus can never override", func(t *testing.T) {
		// More reservations than consents (Reluctant) puts the ratio under 0.5,
		// so no threshold above a half can ever be reached. Under the decisive
		// denominator this same room read 5/6 = 0.833 and carried.
		cc := ConsensusCounts{Consent: 5, StandAside: 6, Block: 1, Participants: 12, Eligible: 12, OpenObjections: 1}
		if !cc.Reluctant() {
			t.Fatal("fixture is not reluctant; it must be to make the point")
		}
		if got := EvaluateOverride(cc, c); got != ResultUpheld {
			t.Fatalf("got %q, want upheld: a room that could not pass a proposal cannot override a block", got)
		}
	})
	t.Run("the second window needs quorum like the first", func(t *testing.T) {
		cc := ConsensusCounts{Consent: 2, Participants: 2, Eligible: 10}
		if got := EvaluateOverride(cc, c); got != ResultInsufficientQuorum {
			t.Fatalf("got %q, want insufficient_quorum: 2 voters is not a window", got)
		}
	})
	t.Run("abstains are the only position that leaves the denominator", func(t *testing.T) {
		// 4 consent, 6 abstain: abstains are neutral (§6.6) so they are not part
		// of non-abstain; 4/4 = 1.0. If abstains were counted the reading would be
		// 0.4 and the block would stand, so this fixture distinguishes them.
		cc := ConsensusCounts{Consent: 4, Abstain: 6, Participants: 10, Eligible: 10, OpenObjections: 1}
		if got := EvaluateOverride(cc, c); got != ResultOverridden {
			t.Fatalf("got %q, want overridden: abstains do not count against the ratio", got)
		}
	})
}

func TestAnEmptyRoomIsNotUnanimous(t *testing.T) {
	// 0 consent, 0 block, 0 participants: the ratios are 0/0 and the answer is ZERO.
	//
	// This is a mutation's finding, not a hypothetical. `DecisiveRatio` returned 1 in the
	// empty case for four passes -- reported as a "killed" mutant because the mutation had
	// to be rewritten to compile, and once it compiled it SURVIVED: nothing in the suite
	// asked what an empty room scores. It matters because the two places a ratio is read
	// (EvaluateConsensus, EvaluateOverride) both check quorum FIRST, which masks an empty
	// room there and leaves the helper's contract unmeasured. A number nobody read is how
	// a denominator drifts.
	var empty ConsensusCounts
	for _, tc := range []struct {
		name string
		got  float64
	}{
		{"SupportRatio", empty.SupportRatio()},
		{"DecisiveRatio", empty.DecisiveRatio()},
	} {
		if tc.got != 0 {
			t.Errorf("%s() of an empty room = %v, want 0: nobody voting is not unanimity, "+
				"and a 1 here reads as a full room agreeing", tc.name, tc.got)
		}
	}
	if n := empty.Decisive(); n != 0 {
		t.Errorf("Decisive() of an empty room = %d, want 0", n)
	}
	if empty.Reluctant() {
		t.Error("an empty room reported a reluctant consensus")
	}
}

func TestCheckMergeGate(t *testing.T) {
	c := testCharter() // merge quorum min 2, ratio 0.25, reviewer required

	t.Run("fails without quorum", func(t *testing.T) {
		g := CheckMergeGate(1, 10, true, c)
		if g.Passed() {
			t.Fatal("1/4 approvals must not pass")
		}
	})
	t.Run("passes with quorum and reviewer", func(t *testing.T) {
		g := CheckMergeGate(3, 10, true, c)
		if !g.Passed() {
			t.Fatalf("3 approvals + reviewer should pass: %+v", g)
		}
	})
	t.Run("fails without reviewer approval", func(t *testing.T) {
		g := CheckMergeGate(4, 10, false, c)
		if g.Passed() {
			t.Fatal("missing reviewer approval must not pass")
		}
	})
	t.Run("maintainer-led charter can disable the quorum", func(t *testing.T) {
		mled := DefaultCharter(MaintainerLed)
		if mled.MergeRequiresQuorum {
			t.Fatal("maintainer_led default should disable merge quorum")
		}
		g := CheckMergeGate(0, 10, true, mled)
		if !g.Passed() {
			t.Fatalf("bypass should pass with reviewer approval: %+v", g)
		}
	})
}

func TestDefaultCharterCollectiveIsQuorumGated(t *testing.T) {
	c := DefaultCharter(Collective)
	if !c.MergeRequiresQuorum {
		t.Fatal("collective projects must gate merges behind quorum by default")
	}
	if !c.RequireReviewerApproval {
		t.Fatal("technical approval must be required by default")
	}
}

func TestDeadlineExpiry(t *testing.T) {
	const day = 24 * 3600
	opens := 1000.0
	closes := opens + 7*day // the default 7-day vote window

	t.Run("a call inside its window is not expired", func(t *testing.T) {
		d := Deadline{OpensAt: opens, ClosesAt: closes}
		if d.Expired(opens + day) {
			t.Error("a call with time left must not be expired")
		}
		if d.Expired(opens) {
			t.Error("a call that has not even opened must not be expired")
		}
	})

	t.Run("past the window with no extensions used, it is extendable not expired", func(t *testing.T) {
		d := Deadline{OpensAt: opens, ClosesAt: closes}
		if d.Expired(closes + 1) {
			t.Error("a call that may still be extended must not be expired")
		}
		if !d.Extendable() {
			t.Error("expected the first extension to be available")
		}
	})

	t.Run("running out of extensions freezes rather than expires", func(t *testing.T) {
		d := Deadline{OpensAt: opens, ClosesAt: closes, Extensions: MaxWindowExtensions}
		// Still inside the final window: alive, and can be closed explicitly.
		if d.Expired(opens + day) {
			t.Error("a frozen call with time left must not be expired")
		}
		// Past the final deadline: over.
		if !d.Expired(closes + 1) {
			t.Error("a call past its final deadline must be expired")
		}
		if d.Extendable() {
			t.Error("a call at the extension cap must not be extendable")
		}
	})

	t.Run("extensions preserve the original window length", func(t *testing.T) {
		d := Deadline{OpensAt: opens, ClosesAt: opens + 3*day} // a custom 3-day window
		d2, ok := d.Extend()
		if !ok {
			t.Fatal("first extension should be granted")
		}
		if want := opens + 6*day; d2.ClosesAt != want {
			t.Errorf("ClosesAt = %v, want %v (original 3-day span, not a shortened one)", d2.ClosesAt, want)
		}
		if d2.Extensions != 1 {
			t.Errorf("Extensions = %d, want 1", d2.Extensions)
		}
		if d2.OpensAt != d.OpensAt {
			t.Error("extending must not move opens_at")
		}
	})

	t.Run("at most MaxWindowExtensions are granted", func(t *testing.T) {
		d := Deadline{OpensAt: opens, ClosesAt: closes}
		granted := 0
		for {
			next, ok := d.Extend()
			if !ok {
				break
			}
			d, granted = next, granted+1
		}
		if granted != MaxWindowExtensions {
			t.Errorf("granted %d extensions, want exactly MaxWindowExtensions=%d", granted, MaxWindowExtensions)
		}
		// And the call is now genuinely over rather than merely frozen.
		if !d.Expired(d.ClosesAt + 1) {
			t.Error("a call that exhausted every extension should be expired past its deadline")
		}
	})

	t.Run("a degenerate zero-length window still gets a real span", func(t *testing.T) {
		d := Deadline{OpensAt: opens, ClosesAt: opens}
		d2, ok := d.Extend()
		if !ok {
			t.Fatal("expected an extension")
		}
		if d2.ClosesAt <= opens {
			t.Errorf("a zero-span window must still be extended by a positive amount, got %v", d2.ClosesAt)
		}
	})
}
