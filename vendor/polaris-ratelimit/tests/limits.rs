use polaris_ratelimit::{Limits, Quota};
use std::time::Duration;

#[test]
fn a_quota_is_permits_over_a_window() {
    let q = Quota::per_minute(120);
    assert_eq!(q.permits, 120);
    assert_eq!(q.per, Duration::from_secs(60));
    assert_eq!(q.per_second_rate(), 2.0);
}

#[test]
fn a_quota_parses_the_spelling_the_config_uses() {
    assert_eq!(Quota::parse("120/min").unwrap().permits, 120);
    assert_eq!(Quota::parse("01/sec").unwrap().permits, 1);
    assert!(Quota::parse("120").unwrap_err().to_string().contains("/min"));
    assert!(Quota::parse("0/min")
        .unwrap_err()
        .to_string()
        .contains("zero"));
}

#[test]
fn a_class_is_data_and_falls_back_to_the_default() {
    let limits = Limits::new(Quota::per_minute(60))
        .class("auth", Quota::per_minute(20))
        .class("write", Quota::per_minute(30));
    assert_eq!(limits.quota("auth").permits, 20);
    assert_eq!(
        limits.quota("search").permits,
        60,
        "an undeclared class uses the default"
    );
    let names: Vec<&str> = limits.names().collect();
    assert_eq!(names, vec!["default", "auth", "write"]);
}

#[test]
fn a_declared_class_may_be_zeroed_deliberately() {
    let limits = Limits::new(Quota::per_minute(60)).class("heavyload", Quota::per_minute(0));
    assert!(limits.validate().is_ok(), "zeroing a class is a decision, not a defect");
}

#[test]
fn an_empty_default_is_refused_because_it_would_limit_everything_to_nothing() {
    let err = Limits::new(Quota::per_minute(0)).validate().unwrap_err();
    assert!(err.to_string().contains("default"), "{err}");
}
