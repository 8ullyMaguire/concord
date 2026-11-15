//! The bucket map, and the one call a middleware makes.

use std::collections::HashMap;
use std::sync::Mutex;
use std::time::{Duration, Instant};

use crate::limits::Limits;

/// How long an untouched bucket is kept. Ten minutes is several refill windows at any budget this estate uses.
pub const DEFAULT_IDLE: Duration = Duration::from_secs(600);

/// The most buckets held at once. A bucket is a string and two numbers; ten thousand is a rounding error next
/// to the connection pool, and the cap is what stops a spoofed client key from being a memory leak.
pub const DEFAULT_MAX_BUCKETS: usize = 10_000;

#[derive(Debug, Clone, PartialEq)]
pub struct Decision {
    pub allowed: bool,
    /// Permits left after this request, rounded down.
    pub remaining: u32,
    /// The budget this request drew from.
    pub limit: u32,
    /// For a refusal: how long until one permit exists. `None` when allowed.
    pub retry_after: Option<Duration>,
}

#[derive(Debug, Clone)]
struct Bucket {
    tokens: f64,
    last: Instant,
}

pub struct Limiter {
    limits: Limits,
    buckets: Mutex<HashMap<(String, String), Bucket>>,
    idle: Duration,
    max_buckets: usize,
}

impl std::fmt::Debug for Limiter {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Limiter")
            .field("classes", &self.limits.names().collect::<Vec<_>>())
            .field("buckets", &self.bucket_count())
            .finish()
    }
}

impl Limiter {
    pub fn new(limits: Limits) -> Self {
        Self::with_capacity(limits, DEFAULT_MAX_BUCKETS, DEFAULT_IDLE)
    }

    pub fn with_capacity(limits: Limits, max_buckets: usize, idle: Duration) -> Self {
        Self {
            limits,
            buckets: Mutex::new(HashMap::new()),
            idle,
            max_buckets: max_buckets.max(1),
        }
    }

    pub fn limits(&self) -> &Limits {
        &self.limits
    }

    pub fn bucket_count(&self) -> usize {
        self.buckets.lock().map(|m| m.len()).unwrap_or(0)
    }

    /// Draw one permit for `key` in `class`.
    ///
    /// The clock is an argument, not `Instant::now()`: a limiter that reads the clock cannot be tested, and an
    /// untested limiter is a 429 nobody can predict.
    pub fn check_at(&self, key: &str, class: &str, now: Instant) -> Decision {
        let quota = self.limits.quota(class);
        // A zero budget is a deliberate lock-out, not a bypass: the class cannot be used until the deployment
        // raises the number. It is refused before the arithmetic, because a rate of 0.0 makes the wait infinite
        // and `Duration::from_secs_f64(inf)` panics — a configuration typo must not abort the request.
        if quota.permits == 0 {
            return Decision {
                allowed: false,
                remaining: 0,
                limit: 0,
                retry_after: Some(Duration::from_secs(60)),
            };
        }
        let rate = quota.per_second_rate();
        let capacity = f64::from(quota.permits);

        let mut map = match self.buckets.lock() {
            Ok(map) => map,
            // A poisoned mutex must not become a total denial *or* a total allowance. Refusing is the safer
            // half: the edge still has its own bucket, and a limiter that fails open is not a limiter.
            Err(_) => {
                return Decision {
                    allowed: false,
                    remaining: 0,
                    limit: quota.permits,
                    retry_after: Some(Duration::from_secs(1)),
                }
            }
        };

        if map.len() >= self.max_buckets {
            map.retain(|_, b| now.saturating_duration_since(b.last) < self.idle);
            if map.len() >= self.max_buckets {
                // Still full: drop the least recently seen, but never the caller in hand.
                if let Some(oldest) = map
                    .iter()
                    .filter(|((k, _), _)| k.as_str() != key)
                    .min_by_key(|(_, b)| b.last)
                    .map(|(k, _)| k.clone())
                {
                    map.remove(&oldest);
                }
            }
        }

        let bucket = map
            .entry((key.to_string(), class.to_string()))
            .or_insert(Bucket {
                tokens: capacity,
                last: now,
            });

        let elapsed = now.saturating_duration_since(bucket.last).as_secs_f64();
        bucket.tokens = (bucket.tokens + elapsed * rate).min(capacity);
        bucket.last = now;

        // The epsilon is not politeness: a budget of 3 per 7 s refills one permit every 2.333… s, and Duration
        // truncates that to 2_333_333_333 ns — 0.999999999857 permits' worth after multiplication. Without the
        // epsilon the limiter refuses a request that is due, which in production is a 429 on a client that
        // waited exactly as long as `Retry-After` told it to.
        if bucket.tokens + 1e-9 >= 1.0 {
            bucket.tokens -= 1.0;
            Decision {
                allowed: true,
                remaining: bucket.tokens.floor() as u32,
                limit: quota.permits,
                retry_after: None,
            }
        } else {
            let missing = 1.0 - bucket.tokens;
            let wait = Duration::from_secs_f64(missing / rate);
            Decision {
                allowed: false,
                remaining: 0,
                limit: quota.permits,
                // Rounded up to a whole second: `Retry-After` has no sub-second form, and a client that waits
                // 0.4 s on a header saying 0 retries immediately.
                retry_after: Some(Duration::from_secs(wait.as_secs().max(1))),
            }
        }
    }
}
