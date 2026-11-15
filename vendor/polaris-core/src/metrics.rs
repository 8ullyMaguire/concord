//! A metrics registry small enough to be read in one sitting, in the format Prometheus scrapes.
//!
//! # Why this exists
//!
//! Every service in the estate answers `/healthz` and nothing else, so "is it healthy" is the only question a
//! monitor can ask — and a service can be healthy while refusing every write, deduping everything, or hitting
//! its daily cap. Measured 2026-11-05: `curl 127.0.0.1:8081/metrics` answered **200 with the SPA shell**, so a
//! monitor that checked status codes believed darkhold published metrics and it published HTML. That is the
//! failure this module is built to make impossible: the renderer only ever emits the exposition format, and a
//! product that has not mounted it gets a 404 rather than a page.
//!
//! # Three decisions
//!
//! * **Handlers, not a middleware tower.** A counter is incremented where the thing happened, in the branch that
//!   knows what happened (`deduped`, `refused`, `accepted`) — not by a layer that can only see that a request
//!   arrived. The numbers this module publishes are the ones the code already decides on.
//! * **No dependency.** `prometheus` and `metrics` both bring a registry design, a proc macro and a feature
//!   matrix for what is, here, two atomics and a `format!`. The exposition format is five grammar rules.
//! * **Names are validated at registration, in debug and release.** A metric named `inbox events` renders a
//!   file no scraper can parse, and the failure lands in the monitoring system rather than in the service.
//!
//! # The format, as NFKD
//!
//! ```text
//! # HELP polaris_inbox_events_total Events accepted for delivery.
//! # TYPE polaris_inbox_events_total counter
//! polaris_inbox_events_total 42
//! ```
//!
//! Labels are rendered in the order given, which keeps a diff between two scrapes readable. Families are
//! sorted by name so two scrapes of the same state produce byte-identical text — otherwise every scrape looks
//! like a change and nothing can be cached, diffed or asserted in a test.

use std::collections::BTreeMap;
use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::sync::{Mutex, OnceLock};

/// A monotonically increasing count of something that happened.
#[derive(Debug)]
pub struct Counter {
    name: &'static str,
    help: &'static str,
    value: AtomicU64,
}

impl Counter {
    /// Add one. Saturates rather than wrapping: a wrapped counter reads as a reset to a scraper.
    pub fn inc(&self) {
        self.add(1);
    }

    /// Add `n`.
    pub fn add(&self, n: u64) {
        let _ = self
            .value
            .try_update(Ordering::Relaxed, Ordering::Relaxed, |current| {
                Some(current.saturating_add(n))
            });
    }

    /// The current value.
    #[must_use]
    pub fn get(&self) -> u64 {
        self.value.load(Ordering::Relaxed)
    }

    /// Name, as it appears on the wire.
    #[must_use]
    pub fn name(&self) -> &'static str {
        self.name
    }
}

/// A number that goes up and down, like "entries in the queue right now".
///
/// Signed: a gauge that can only go up is a counter with a confusing name, and the estate has enough of those.
#[derive(Debug)]
pub struct Gauge {
    name: &'static str,
    help: &'static str,
    value: AtomicI64,
}

impl Gauge {
    pub fn set(&self, value: i64) {
        self.value.store(value, Ordering::Relaxed);
    }

    pub fn add(&self, delta: i64) {
        self.value.fetch_add(delta, Ordering::Relaxed);
    }

    #[must_use]
    pub fn get(&self) -> i64 {
        self.value.load(Ordering::Relaxed)
    }

    #[must_use]
    pub fn name(&self) -> &'static str {
        self.name
    }
}

/// Everything registered in this process.
#[derive(Default)]
struct Registry {
    counters: BTreeMap<&'static str, &'static Counter>,
    gauges: BTreeMap<&'static str, &'static Gauge>,
}

fn registry() -> &'static Mutex<Registry> {
    static REGISTRY: OnceLock<Mutex<Registry>> = OnceLock::new();
    REGISTRY.get_or_init(|| Mutex::new(Registry::default()))
}

/// Whether a name is legal in the exposition format: `[a-zA-Z_:][a-zA-Z0-9_:]*`.
///
/// Checked rather than silently sanitised, because a sanitised name is a metric nobody's dashboard queries.
#[must_use]
pub fn valid_name(name: &str) -> bool {
    let mut characters = name.chars();
    match characters.next() {
        Some(first) if first.is_ascii_alphabetic() || first == '_' || first == ':' => {}
        _ => return false,
    }
    characters.all(|c| c.is_ascii_alphanumeric() || c == '_' || c == ':')
}

/// Register a counter once. Calling it again with the same name returns the same counter.
///
/// # Panics
///
/// When the name is not a legal Prometheus metric name. That is a programming error, and it is cheaper to fail
/// at first use than to publish a file no scraper can read.
pub fn counter(name: &'static str, help: &'static str) -> &'static Counter {
    assert!(valid_name(name), "not a Prometheus metric name: {name:?}");
    let mut registry = registry().lock().expect("metrics registry poisoned");
    if let Some(existing) = registry.counters.get(name) {
        return existing;
    }
    let entry: &'static Counter = Box::leak(Box::new(Counter {
        name,
        help,
        value: AtomicU64::new(0),
    }));
    registry.counters.insert(name, entry);
    entry
}

/// Register a gauge once. Same contract as [`counter`].
///
/// # Panics
///
/// When the name is not a legal Prometheus metric name.
pub fn gauge(name: &'static str, help: &'static str) -> &'static Gauge {
    assert!(valid_name(name), "not a Prometheus metric name: {name:?}");
    let mut registry = registry().lock().expect("metrics registry poisoned");
    if let Some(existing) = registry.gauges.get(name) {
        return existing;
    }
    let entry: &'static Gauge = Box::leak(Box::new(Gauge {
        name,
        help,
        value: AtomicI64::new(0),
    }));
    registry.gauges.insert(name, entry);
    entry
}

/// The whole registry as a Prometheus exposition document.
///
/// Deterministic: families sorted by name, counters before gauges only because both maps are sorted and
/// counters are written first. Two calls with no traffic in between are byte-identical, which is what lets a
/// test assert the format instead of eyeballing it.
#[must_use]
pub fn render() -> String {
    let registry = registry().lock().expect("metrics registry poisoned");
    let mut out = String::new();
    for (name, counter) in &registry.counters {
        out.push_str(&format!("# HELP {name} {}\n", escape_help(counter.help)));
        out.push_str(&format!("# TYPE {name} counter\n"));
        out.push_str(&format!("{name} {}\n", counter.get()));
    }
    for (name, gauge) in &registry.gauges {
        out.push_str(&format!("# HELP {name} {}\n", escape_help(gauge.help)));
        out.push_str(&format!("# TYPE {name} gauge\n"));
        out.push_str(&format!("{name} {}\n", gauge.get()));
    }
    out
}

/// The help string is one line: a newline in it would start a new metric family.
fn escape_help(help: &str) -> String {
    help.replace('\\', "\\\\").replace('\n', " ")
}

/// The `Content-Type` Prometheus's own text format declares.
pub const CONTENT_TYPE: &str = "text/plain; version=0.0.4; charset=utf-8";

/// An axum handler: the registry, as a body.
///
/// Mounted by each service as `GET /metrics`. Kept here so that four services cannot disagree about the
/// content type, which is the one thing a scraper dispatches on.
pub async fn handler() -> impl axum::response::IntoResponse {
    ([(axum::http::header::CONTENT_TYPE, CONTENT_TYPE)], render())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_name_must_be_scrapable() {
        assert!(valid_name("polaris_inbox_events_total"));
        assert!(valid_name("a"));
        assert!(valid_name("_private"));
        assert!(valid_name("ns:metric"));
        assert!(!valid_name("inbox events"));
        assert!(!valid_name("1metric"));
        assert!(!valid_name("metric-name"));
        assert!(!valid_name(""));
    }

    #[test]
    fn help_cannot_start_a_new_family() {
        assert_eq!(escape_help("two\nlines"), "two lines");
        assert_eq!(escape_help("a\\b"), "a\\\\b");
    }

    #[test]
    fn counters_only_go_up() {
        let counter = counter("test_saturation_total", "Saturating counter.");
        counter.add(u64::MAX);
        counter.inc();
        assert_eq!(
            counter.get(),
            u64::MAX,
            "a wrapped counter looks like a reset"
        );
    }

    #[test]
    fn gauge_takes_a_signed_delta() {
        let gauge = gauge("test_queue_depth", "Signed gauge.");
        gauge.set(5);
        gauge.add(-7);
        assert_eq!(gauge.get(), -2);
    }
}
