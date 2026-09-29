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
		cc := ConsensusCounts{Consent: 2, StandAside: 1, Block: 0, Participants: 4, Eligible: 10}
		// ratio 2/3 ≈ 0.667 < 0.7, quorum met
		if got := EvaluateConsensus(cc, c); got != ResultRejected {
			t.Fatalf("got %q, want rejected", got)
		}
	})
	t.Run("blocked by unresolved objection", func(t *testing.T) {
		// 6/8 = 0.75 ≥ consent 0.7 but < override 0.8 with a block open
		cc := ConsensusCounts{Consent: 6, StandAside: 2, Participants: 8, Eligible: 10, OpenObjections: 1}
		if got := EvaluateConsensus(cc, c); got != ResultBlocked {
			t.Fatalf("got %q, want blocked (ratio %.3f)", got, 6.0/8.0)
		}
	})
	t.Run("supermajority overrides a block", func(t *testing.T) {
		cc := ConsensusCounts{Consent: 8, StandAside: 1, Participants: 9, Eligible: 10, OpenObjections: 1}
		// 8/9 ≈ 0.889 ≥ 0.8
		if got := EvaluateConsensus(cc, c); got != ResultAcceptedOverridden {
			t.Fatalf("got %q, want accepted_overridden", got)
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
