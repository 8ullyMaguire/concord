use polaris_ratelimit::{Limiter, Limits, Quota};
use std::time::{Duration, Instant};

fn limiter() -> Limiter {
    Limiter::new(Limits::new(Quota::per_minute(60)).class("auth", Quota::per_minute(2)))
}

#[test]
fn two_permits_are_two_requests_and_the_third_says_when_to_come_back() {
    let l = limiter();
    let t0 = Instant::now();
    assert!(l.check_at("1.2.3.4", "auth", t0).allowed);
    assert!(l.check_at("1.2.3.4", "auth", t0).allowed);
    let third = l.check_at("1.2.3.4", "auth", t0);
    assert!(!third.allowed);
    let wait = third.retry_after.expect("a refusal says when");
    assert!(
        wait > Duration::ZERO && wait <= Duration::from_secs(30),
        "{wait:?}"
    );
    assert_eq!(third.remaining, 0);
}

#[test]
fn the_bucket_refills_at_the_declared_rate() {
    let l = limiter();
    let t0 = Instant::now();
    assert!(l.check_at("k", "auth", t0).allowed);
    assert!(l.check_at("k", "auth", t0).allowed);
    assert!(!l.check_at("k", "auth", t0).allowed);
    // Two permits a minute: one comes back after thirty seconds.
    assert!(!l
        .check_at("k", "auth", t0 + Duration::from_secs(29))
        .allowed);
    assert!(l
        .check_at("k", "auth", t0 + Duration::from_secs(30))
        .allowed);
}

#[test]
fn two_clients_do_not_share_a_budget() {
    let l = limiter();
    let t0 = Instant::now();
    assert!(l.check_at("a", "auth", t0).allowed);
    assert!(l.check_at("a", "auth", t0).allowed);
    assert!(!l.check_at("a", "auth", t0).allowed);
    assert!(
        l.check_at("b", "auth", t0).allowed,
        "a second client starts with a full bucket"
    );
}

#[test]
fn a_class_nobody_declared_uses_the_default_and_is_not_a_refusal() {
    let l = limiter();
    let t0 = Instant::now();
    let d = l.check_at("k", "search", t0);
    assert!(d.allowed);
    assert_eq!(d.limit, 60);
}

#[test]
fn a_flood_of_keys_cannot_grow_the_map_without_bound() {
    let l = Limiter::with_capacity(Limits::new(Quota::per_minute(1)), 8, Duration::from_secs(600));
    let t0 = Instant::now();
    for i in 0..50 {
        l.check_at(&format!("k{i}"), "default", t0);
    }
    assert!(l.bucket_count() <= 8, "held {} buckets", l.bucket_count());
}

#[test]
fn a_permit_that_is_due_is_not_refused_by_a_truncated_nanosecond() {
    // 3 per 7 s: one permit every 2.333… s, which Duration truncates to 2_333_333_333 ns. Multiply that by the
    // refill rate and you get 0.999999999857 permits — due, and refused without the epsilon.
    let l = Limiter::with_capacity(
        Limits::new(Quota::per_minute(60)).class(
            "odd",
            Quota {
                permits: 3,
                per: Duration::from_secs(7),
            },
        ),
        16,
        Duration::from_secs(600),
    );
    let t0 = Instant::now();
    for _ in 0..3 {
        assert!(l.check_at("k", "odd", t0).allowed);
    }
    assert!(!l.check_at("k", "odd", t0).allowed, "the bucket is empty");
    let due = t0 + Duration::from_secs_f64(7.0 / 3.0);
    let decision = l.check_at("k", "odd", due);
    assert!(
        decision.allowed,
        "a permit due after a full refill interval must not be refused: {:?}",
        decision.retry_after
    );
}

#[test]
fn a_zero_budget_locks_the_class_out_without_dividing_by_zero() {
    let l = Limiter::with_capacity(
        Limits::new(Quota::per_minute(60)).class("off", Quota::per_minute(0)),
        16,
        Duration::from_secs(600),
    );
    let d = l.check_at("k", "off", Instant::now());
    assert!(!d.allowed);
    assert_eq!(d.limit, 0);
    assert_eq!(d.retry_after, Some(Duration::from_secs(60)));
}

#[test]
fn the_active_key_is_never_the_one_evicted() {
    let l = Limiter::with_capacity(Limits::new(Quota::per_minute(2)), 2, Duration::from_secs(0));
    let t0 = Instant::now();
    l.check_at("old", "default", t0);
    l.check_at("older", "default", t0);
    // Both are past the idle window, and the map is full: the caller below must still be tracked.
    assert!(l.check_at("current", "default", t0).allowed);
    assert!(
        l.check_at("current", "default", t0).allowed,
        "the active key kept its bucket"
    );
    assert!(
        !l.check_at("current", "default", t0).allowed,
        "and its budget is real"
    );
}
