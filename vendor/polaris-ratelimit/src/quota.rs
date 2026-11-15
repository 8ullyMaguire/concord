//! A budget: `permits` requests every `per`.

use std::fmt;
use std::str::FromStr;
use std::time::Duration;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Quota {
    pub permits: u32,
    pub per: Duration,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct QuotaError {
    pub raw: String,
    pub why: &'static str,
}

impl fmt::Display for QuotaError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{:?} is not a quota: {}", self.raw, self.why)
    }
}

impl std::error::Error for QuotaError {}

impl Quota {
    pub fn per_minute(permits: u32) -> Self {
        Self {
            permits,
            per: Duration::from_secs(60),
        }
    }

    pub fn per_second(permits: u32) -> Self {
        Self {
            permits,
            per: Duration::from_secs(1),
        }
    }

    /// Permits per second, as a float. The bucket refills at this rate.
    pub fn per_second_rate(&self) -> f64 {
        f64::from(self.permits) / self.per.as_secs_f64().max(1e-9)
    }

    /// The rate, for the message a refusal carries.
    pub fn describe(&self) -> String {
        if self.per == Duration::from_secs(60) {
            format!("{}/min", self.permits)
        } else {
            format!("{}/{}s", self.permits, self.per.as_secs())
        }
    }

    /// Parse the spelling the configuration uses: `120/min`, `30/min`, `5/sec`.
    pub fn parse(raw: &str) -> Result<Self, QuotaError> {
        raw.parse()
    }
}

impl FromStr for Quota {
    type Err = QuotaError;

    fn from_str(raw: &str) -> Result<Self, Self::Err> {
        let (count, unit) = raw.split_once('/').ok_or(QuotaError {
            raw: raw.to_string(),
            why: "a quota is `<permits>/<min|sec>`, e.g. `120/min`",
        })?;
        let permits: u32 = count.trim().parse().map_err(|_| QuotaError {
            raw: raw.to_string(),
            why: "the permits are a whole number",
        })?;
        if permits == 0 {
            return Err(QuotaError {
                raw: raw.to_string(),
                why: "a zero quota permits nobody",
            });
        }
        let per = match unit.trim() {
            "min" | "minute" => Duration::from_secs(60),
            "sec" | "second" => Duration::from_secs(1),
            _ => {
                return Err(QuotaError {
                    raw: raw.to_string(),
                    why: "the unit is `min` or `sec`",
                })
            }
        };
        Ok(Self { permits, per })
    }
}
