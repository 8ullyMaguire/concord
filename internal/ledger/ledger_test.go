package ledger

import (
	"errors"
	"testing"
	"time"
)

// The vectors below are the contract with the Rust crate. When the crate gains a case, this
// table gains the same case in the same commit, or the mirror has drifted.
func TestValidate(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	reason := "spam: repeated link drops"
	blank := "   "
	future := at.Add(time.Hour)
	past := at.Add(-time.Hour)

	cases := []struct {
		name  string
		act   Act
		rules Rules
		want  error
	}{
		{"permanent without reason", Act{At: at}, Permanent, ReasonRequiredDefect},
		{"permanent with blank reason", Act{At: at, Reason: &blank}, Permanent, ReasonRequiredDefect},
		{"permanent with reason", Act{At: at, Reason: &reason}, Permanent, nil},
		{"housekeeping without reason", Act{At: at}, Housekeeping, nil},
		{"bounded without expiry", Act{At: at, Reason: &reason}, Bounded, ExpiryRequiredDefect},
		{"bounded with future expiry", Act{At: at, Reason: &reason, ExpiresAt: &future}, Bounded, nil},
		{"bounded already expired", Act{At: at, Reason: &reason, ExpiresAt: &past}, Bounded, ExpiryInvalid},
		{"unbounded with an expiry is allowed", Act{At: at, Reason: &reason, ExpiresAt: &future}, Permanent, nil},
	}
	for _, c := range cases {
		got := Validate(c.act, c.rules)
		if !errors.Is(got, c.want) && got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestProject(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	reason := "spam"
	base := Act{
		Action:  "arena.remove",
		Subject: OfObject("feature", "123"),
		Actor:   Person("acct-9", "ana"),
		Reason:  &reason,
		At:      at,
	}

	published := Project(base, PublishHandle)
	if published.Actor == nil || *published.Actor != "ana" {
		t.Errorf("a published handle must name the actor, got %v", published.Actor)
	}
	if published.Subject != "123" || published.SubjectKind != "feature" || published.SubjectResolved {
		t.Errorf("object subject: got %+v", published)
	}

	withheld := Project(base, WithholdActor)
	if withheld.Actor != nil || withheld.Panel {
		t.Errorf("a withheld actor must be absent and not a panel: %+v", withheld)
	}

	panel := Project(Act{Action: "panel.vote", Subject: OfAccount("acct-9", "ana"), Actor: Panel(), At: at}, PublishHandle)
	if panel.Actor != nil || !panel.Panel {
		t.Errorf("a panel is anonymised whatever the rule says: %+v", panel)
	}
	if panel.Subject != "ana" || !panel.SubjectResolved {
		t.Errorf("an account subject publishes its public face: %+v", panel)
	}
}

func TestCursorRoundTripsAndOrdersStrictly(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	a := NewCursor(at, "10")
	b := NewCursor(at, "11")
	c := NewCursor(at.Add(time.Second), "1")

	if !a.Before(b) || b.Before(a) {
		t.Errorf("the same instant orders by id: %v vs %v", a, b)
	}
	if !b.Before(c) || c.Before(b) {
		t.Errorf("a later instant wins: %v vs %v", b, c)
	}
	if a.Before(a) {
		t.Error("a cursor is not before itself")
	}

	raw := a.Encode()
	back, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if !back.At.Equal(a.At) || back.ID != a.ID {
		t.Errorf("round trip: got %v, want %v", back, a)
	}
}

func TestDecodeRefusesRatherThanStartingAtPageOne(t *testing.T) {
	for _, raw := range []string{"", "nonsense", "2026-11-11T00:00:00Z", "2026-11-11T00:00:00Z|", "not-a-time|7"} {
		if _, err := Decode(raw); !errors.Is(err, ErrMalformedCursor) {
			t.Errorf("decode %q: got %v, want ErrMalformedCursor", raw, err)
		}
	}
}
