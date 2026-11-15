use chrono::{Duration, TimeZone, Utc};
use polaris_ledger::{
    project, validate, Act, Actor, Cursor, CursorError, Defect, Redaction, Rules, Subject,
};

fn at(secs: i64) -> chrono::DateTime<Utc> {
    Utc.with_ymd_and_hms(2026, 11, 9, 12, 0, 0).unwrap() + Duration::seconds(secs)
}

fn sanction() -> Act {
    Act {
        action: "sanction.create".into(),
        subject: Subject::Account {
            account: "acc-0001".into(),
            public: "ash".into(),
        },
        actor: Actor::Person {
            account: Some("acc-9999".into()),
            public: "mod-kestrel".into(),
        },
        reason: Some("harassment in a review thread".into()),
        expires_at: None,
        at: at(0),
    }
}

#[test]
fn an_account_is_never_published() {
    // The rule darkhold's modlog has kept since it was written: `audit_log.actor` holds the account, and a public
    // surface publishing it would publish the link every other surface refuses to publish.
    let act = sanction();
    let published = serde_json::to_string(&project(&act, Redaction::PublishHandle)).expect("json");
    assert!(published.contains("mod-kestrel"), "the public handle travels");
    assert!(
        !published.contains("acc-9999") && !published.contains("acc-0001"),
        "no account id may appear in a public projection: {published}"
    );
}

#[test]
fn a_panel_action_publishes_no_actor_and_says_it_was_a_panel() {
    // Metamoderation: "the actor is the jury panel, not the voter". A null actor would read as unknown, which is a
    // different and false claim.
    let mut act = sanction();
    act.actor = Actor::Panel;
    let published = project(&act, Redaction::PublishHandle);
    assert_eq!(published.actor, None);
    assert!(published.panel, "a panel decision must be distinguishable from an anonymised one");
}

#[test]
fn a_withheld_actor_stays_withheld_even_when_the_rule_would_publish() {
    let act = sanction();
    let published = project(&act, Redaction::WithholdActor);
    assert_eq!(published.actor, None);
    assert!(!published.panel, "withholding is not a panel decision");
    assert_eq!(published.reason.as_deref(), Some("harassment in a review thread"));
}

#[test]
fn a_subject_that_is_an_account_resolves_to_its_public_face() {
    let published = project(&sanction(), Redaction::PublishHandle);
    assert_eq!(published.subject, "ash");
    assert!(!published.subject.contains("acc-0001"));
    assert_eq!(published.subject_kind.as_deref(), Some("account"));
    assert!(published.subject_resolved, "a reader must know the subject was resolved to a public face");
}

#[test]
fn an_account_with_no_public_face_is_empty_and_says_so() {
    // darkhold publishes an account whose pseudonyms are all gone as an empty subject rather than the id. The crate
    // must keep the two facts apart: the subject is empty AND the resolution happened.
    let mut act = sanction();
    act.subject = Subject::Account {
        account: "acc-gone".into(),
        public: String::new(),
    };
    let published = project(&act, Redaction::PublishHandle);
    assert_eq!(published.subject, "");
    assert!(published.subject_resolved);
    assert_eq!(published.subject_kind.as_deref(), Some("account"));
}

#[test]
fn an_object_subject_keeps_its_kind_and_id() {
    let mut act = sanction();
    act.subject = Subject::Object {
        kind: "work".into(),
        id: "81b641c9".into(),
    };
    let published = project(&act, Redaction::PublishHandle);
    assert_eq!(published.subject, "81b641c9", "the id alone; the product formats if it wants a path");
    assert_eq!(published.subject_kind.as_deref(), Some("work"));
    assert!(!published.subject_resolved, "an object's id is what the product stored, not a resolution");
}

#[test]
fn a_required_reason_that_is_missing_or_blank_is_a_defect() {
    // darkhold's contract, in one place: "the modlog's contract says every entry carries one, and an age decision
    // recorded without a reason is [not reviewable]".
    let mut act = sanction();
    act.reason = None;
    assert_eq!(validate(&act, Rules::PERMANENT), Err(Defect::ReasonRequired));
    act.reason = Some("   ".into());
    assert_eq!(validate(&act, Rules::PERMANENT), Err(Defect::ReasonRequired));
    act.reason = Some("ban evasion".into());
    assert_eq!(validate(&act, Rules::PERMANENT), Ok(()));
}

#[test]
fn housekeeping_may_omit_a_reason() {
    let mut act = sanction();
    act.reason = None;
    assert_eq!(validate(&act, Rules::HOUSEKEEPING), Ok(()));
}

#[test]
fn a_bounded_power_must_carry_an_expiry_and_an_expired_one_is_a_defect() {
    // concord's trigger, in code: an emergency hold whose expiry is not after its creation cannot exist, and the
    // crate refuses the same two rows the trigger refuses.
    let mut act = sanction();
    assert_eq!(validate(&act, Rules::BOUNDED), Err(Defect::ExpiryRequired));

    act.expires_at = Some(at(0));
    assert_eq!(validate(&act, Rules::BOUNDED), Err(Defect::ExpiryInvalid), "an expiry equal to the act's moment is not a bound");

    act.expires_at = Some(at(-60));
    assert_eq!(validate(&act, Rules::BOUNDED), Err(Defect::ExpiryInvalid));

    act.expires_at = Some(at(3600));
    assert_eq!(validate(&act, Rules::BOUNDED), Ok(()));
}

#[test]
fn a_permanent_act_may_have_no_expiry_but_an_expiry_in_the_past_is_still_wrong() {
    let mut act = sanction();
    assert_eq!(validate(&act, Rules::PERMANENT), Ok(()));
    act.expires_at = Some(at(-1));
    assert_eq!(
        validate(&act, Rules::PERMANENT),
        Err(Defect::ExpiryInvalid),
        "a permanent act carrying an expiry is a bounded act; a past one is a typo that must not be stored"
    );
}

#[test]
fn cursor_pages_do_not_overlap_or_skip_on_equal_timestamps() {
    // The property darkhold's composite cursor exists for: two rows in the same millisecond. A `created_at <` cursor
    // either repeats the first or skips the second; `(at, id)` strictly-before does neither.
    let a = Cursor::new(at(0), "a");
    let b = Cursor::new(at(0), "b");
    assert!(a.before(&b));
    assert!(!b.before(&a));
    assert!(!a.before(&Cursor::new(at(0), "a")), "a cursor is not before itself");

    // Newest-first log: b, a, older. Page one hands back b; the next page takes everything strictly before b.
    let older = Cursor::new(at(-1), "z");
    let after_b: Vec<&Cursor> = [&b, &a, &older].into_iter().filter(|c| c.before(&b)).collect();
    assert_eq!(after_b, vec![&a, &older], "page two starts at the row after b, with no repeat and no gap");
}

#[test]
fn a_cursor_round_trips_through_the_wire_and_refuses_a_malformed_one() {
    let cursor = Cursor::new(at(42), "entry-7");
    assert_eq!(Cursor::decode(&cursor.encode()), Ok(cursor));
    assert_eq!(Cursor::decode("not-a-cursor"), Err(CursorError::Malformed));
    assert_eq!(Cursor::decode("2026-11-09T12:00:42+00:00|"), Err(CursorError::Malformed));
    assert_eq!(Cursor::decode("|entry-7"), Err(CursorError::Malformed));
}

#[test]
fn a_reader_that_never_selected_the_account_cannot_publish_one() {
    // darkhold's modlog query selects `actor_pseud` and never `audit_log.actor`. That is the strongest form of the
    // rule: the account is not in memory, so there is nothing to leak. The crate must let such a reader say so
    // instead of inventing an empty string that a later refactor could start trusting.
    let mut act = sanction();
    act.actor = Actor::Person { account: None, public: "pseud-7".into() };
    let published = project(&act, Redaction::PublishHandle);
    assert_eq!(published.actor.as_deref(), Some("pseud-7"));
    let json = serde_json::to_string(&published).expect("json");
    assert!(!json.contains("null"), "no field may serialise as null where the reader simply did not look: {json}");
}

#[test]
fn an_adopter_with_another_date_library_needs_no_conversion_type() {
    // darkhold's crates/db uses `time`; a ledger that demanded chrono at its boundary would make every adopter pull a
    // second date library in. (seconds, nanos) is what both expose natively.
    let base = at(0);
    let instant = polaris_ledger::from_unix(base.timestamp(), 500_000_000);
    assert_eq!(instant, base + Duration::nanoseconds(500_000_000));
    assert_eq!(instant.timestamp_subsec_nanos(), 500_000_000);
    assert_eq!(polaris_ledger::from_unix(0, 0).timestamp(), 0, "the epoch is the epoch");
}
