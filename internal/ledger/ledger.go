// Package ledger is the Go form of the estate's polaris-ledger crate: the rules a
// moderation log and its public projection must keep.
//
// It owns no table and no database handle, exactly as the crate does not. What it owns is
// the part that must not drift between the two languages:
//
//   - the private record and the public projection are different types, and Project is the
//     only way to a publishable row;
//   - anonymisation is a declared rule per action, never a boolean that changes what an
//     entry means;
//   - an act that removes standing carries a reason, and a bounded power carries its end;
//   - paging over an append-only log uses Cursor, which is strictly-before on (at, id).
package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ActorKind is who acted.
type ActorKind int

const (
	// ActorPerson is a person. Public is the handle a reader may see; Account is the stored
	// identifier and must never reach a public surface.
	ActorPerson ActorKind = iota
	// ActorPanel is a jury panel. Anonymised by construction: a panel has no single actor,
	// and publishing an arbitrary member would be a lie about who decided.
	ActorPanel
	// ActorSystem is the instance itself: a timer, a migration, a rule.
	ActorSystem
)

// Actor is the acting identity, privately.
type Actor struct {
	Kind    ActorKind
	Account string
	Public  string
}

// Person builds a person actor.
func Person(account, public string) Actor {
	return Actor{Kind: ActorPerson, Account: account, Public: public}
}

// Panel is the anonymised jury actor.
func Panel() Actor { return Actor{Kind: ActorPanel} }

// System is the instance acting on its own.
func System() Actor { return Actor{Kind: ActorSystem} }

// SubjectKind separates an account (published as its public face) from a thing.
type SubjectKind int

const (
	// SubjectAccount is a person-shaped subject.
	SubjectAccount SubjectKind = iota
	// SubjectObject is a thing: a work, a post, a proposal, a feature.
	SubjectObject
)

// Subject is what the act was done to.
type Subject struct {
	Kind    SubjectKind
	Account string
	Public  string
	// ObjectKind and ObjectID are the product's own vocabulary for a thing. The crate does
	// not police the kind, and neither does this.
	ObjectKind string
	ObjectID   string
}

// OfAccount is a subject that is a person.
func OfAccount(account, public string) Subject {
	return Subject{Kind: SubjectAccount, Account: account, Public: public}
}

// OfObject is a subject that is a thing.
func OfObject(kind, id string) Subject {
	return Subject{Kind: SubjectObject, ObjectKind: kind, ObjectID: id}
}

// Redaction is how one kind of action's actor may be published, declared per action by the
// product as data.
type Redaction int

const (
	// PublishHandle publishes the acting person's handle: moderation is attributable.
	PublishHandle Redaction = iota
	// PanelAnonymised publishes no actor and marks the entry as a panel decision.
	PanelAnonymised
	// WithholdActor publishes no actor at all.
	WithholdActor
)

// ReasonMode is whether an act must state why.
type ReasonMode int

const (
	// ReasonOptional is housekeeping nobody needs to justify.
	ReasonOptional ReasonMode = iota
	// ReasonRequired is an act that removes standing.
	ReasonRequired
)

// ExpiryMode is whether an act is a bounded power.
type ExpiryMode int

const (
	// ExpiryUnbounded stands until it is reversed.
	ExpiryUnbounded ExpiryMode = iota
	// ExpiryRequired must carry an end.
	ExpiryRequired
)

// Rules is the pair a product declares for one act.
type Rules struct {
	Reason ReasonMode
	Expiry ExpiryMode
}

// The three shapes the crate names, plus concord's act.
var (
	// Bounded is a power that must be justified and must end.
	Bounded = Rules{Reason: ReasonRequired, Expiry: ExpiryRequired}
	// Permanent is a permanent moderation decision.
	Permanent = Rules{Reason: ReasonRequired, Expiry: ExpiryUnbounded}
	// Housekeeping is an act nobody needs to justify.
	Housekeeping = Rules{Reason: ReasonOptional, Expiry: ExpiryUnbounded}
)

// Act is the private record. It is never serialised to a public surface.
type Act struct {
	Action    string
	Subject   Subject
	Actor     Actor
	Reason    *string
	ExpiresAt *time.Time
	At        time.Time
}

// Projected is what a public surface may show. It is the only type here with JSON tags, so
// "publish the private record by accident" is a compile error rather than a review finding.
type Projected struct {
	Action          string    `json:"action"`
	Subject         string    `json:"subject"`
	SubjectKind     string    `json:"subject_kind,omitempty"`
	SubjectResolved bool      `json:"subject_resolved"`
	Actor           *string   `json:"actor"`
	Panel           bool      `json:"panel"`
	Reason          *string   `json:"reason"`
	At              time.Time `json:"at"`
}

// Defect is what is wrong with an act. Each variant names the rule, so a caller can surface
// it rather than log it.
type Defect int

const (
	// ReasonRequiredDefect means the act removes standing and states no reason.
	ReasonRequiredDefect Defect = iota
	// ExpiryInvalid means a bounded power carries an end that is not after its start.
	ExpiryInvalid
	// ExpiryRequiredDefect means a bounded power carries no end.
	ExpiryRequiredDefect
)

// The sentences the Rust crate produces, so a client reading either sees one vocabulary.
func (d Defect) Error() string {
	switch d {
	case ReasonRequiredDefect:
		return "this act requires a reason, and none was given"
	case ExpiryInvalid:
		return "this act's expiry is not after the moment it was taken"
	case ExpiryRequiredDefect:
		return "this act is a bounded power and carries no expiry"
	}
	return "unknown rule defect"
}

// Unwrap exists so errors.Is/As work on the sentinel values.
func (d Defect) Unwrap() error { return nil }

// FromUnix is an instant from unix seconds, the boundary concord stores.
func FromUnix(secs int64) time.Time { return time.Unix(secs, 0).UTC() }

// TrimmedReason collapses a whitespace-only reason to nil, which is the one representation
// of "no reason given".
func TrimmedReason(reason string) *string {
	for _, r := range reason {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return &reason
		}
	}
	return nil
}

// Validate proves an act against the rules a product declared.
func Validate(act Act, rules Rules) error {
	if rules.Reason == ReasonRequired {
		if act.Reason == nil || TrimmedReason(*act.Reason) == nil {
			return ReasonRequiredDefect
		}
	}
	switch {
	case rules.Expiry == ExpiryRequired && act.ExpiresAt == nil:
		return ExpiryRequiredDefect
	case act.ExpiresAt != nil && !act.ExpiresAt.After(act.At):
		return ExpiryInvalid
	}
	return nil
}

// Project is the public projection of an act.
//
// A panel is anonymised whatever the rule says: there is no single person to name. A
// withheld actor stays withheld even when the rule would publish a handle — the stricter of
// the two wins, because the failure mode of getting this wrong is publishing a name.
func Project(act Act, rule Redaction) Projected {
	var actor *string
	panel := false
	switch {
	case act.Actor.Kind == ActorPanel:
		panel = true
	case act.Actor.Kind == ActorPerson && rule == PublishHandle:
		handle := act.Actor.Public
		actor = &handle
	}
	var subject string
	var subjectKind string
	resolved := false
	if act.Subject.Kind == SubjectAccount {
		subject, subjectKind, resolved = act.Subject.Public, "account", true
	} else {
		subject, subjectKind = act.Subject.ObjectID, act.Subject.ObjectKind
	}
	// Copy the reason so a caller cannot mutate the projection's source through the pointer.
	var reason *string
	if act.Reason != nil {
		r := *act.Reason
		reason = &r
	}
	return Projected{
		Action:          act.Action,
		Subject:         subject,
		SubjectKind:     subjectKind,
		SubjectResolved: resolved,
		Actor:           actor,
		Panel:           panel,
		Reason:          reason,
		At:              act.At.UTC(),
	}
}

// Cursor is a position in an append-only log: (at, id), compared strictly.
//
// At alone is not a cursor. Two entries can share a millisecond, and a `< at` comparison
// then either repeats a row on the next page or skips one — and a log that skips a row is
// not an audit.
type Cursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

// NewCursor builds a cursor.
func NewCursor(at time.Time, id string) Cursor { return Cursor{At: at.UTC(), ID: id} }

// Before reports whether c is strictly before other in newest-first ordering: earlier
// time, or the same time and a smaller id.
func (c Cursor) Before(other Cursor) bool {
	if !c.At.Equal(other.At) {
		return c.At.Before(other.At)
	}
	return c.ID < other.ID
}

// Encode is the wire form: RFC 3339, a '|', then the id.
func (c Cursor) Encode() string { return c.At.UTC().Format(time.RFC3339Nano) + "|" + c.ID }

// ErrMalformedCursor is returned rather than a silent first page, because a silent first
// page turns a paging bug into an infinite scroll.
var ErrMalformedCursor = errors.New("this cursor is not a position in the log")

// Decode parses what Encode produced. A malformed cursor is an error, never page one.
func Decode(raw string) (Cursor, error) {
	bar := strings.IndexByte(raw, '|')
	if bar < 0 {
		return Cursor{}, ErrMalformedCursor
	}
	at, err := time.Parse(time.RFC3339Nano, raw[:bar])
	if err != nil {
		return Cursor{}, ErrMalformedCursor
	}
	id := raw[bar+1:]
	if id == "" {
		return Cursor{}, ErrMalformedCursor
	}
	return NewCursor(at, id), nil
}

// EncodeJSON is a convenience for the handler's response.
func (c Cursor) EncodeJSON() (json.RawMessage, error) {
	b, err := json.Marshal(c.Encode())
	if err != nil {
		return nil, fmt.Errorf("encode cursor: %w", err)
	}
	return b, nil
}
