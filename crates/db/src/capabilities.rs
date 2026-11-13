//! §4.10's capabilities panel: the whole catalogue, each entry carrying this project's claim where one exists.
//!
//! Three rules, and the panel is meaningless without them:
//!
//! * **The catalogue is rendered whole.** A capability nobody has asserted is the contribution queue; a panel that
//!   listed only the asserted ones would remove the reason the panel exists.
//! * **`asserted: false` means nobody has said anything — NOT that the answer is no.** The value is empty rather
//!   than "unknown", and the counts say how many of each there are, so a reader can tell "nobody looked" from
//!   "somebody said no".
//! * **A dispute outranks a confirmation.** Two confirmations and two disputes is *disputed*, not confirmed:
//!   reading it as confirmed would let agreement that merely arrived first outrank louder disagreement. A disputed
//!   claim also keeps its ORIGINAL value — substituting "no" would turn "people disagree" into "this is false",
//!   a different claim resting on a different evidence base.

use std::collections::HashMap;

use sqlx::PgPool;

/// Confirmations needed before a claim reads as confirmed.
pub const CONFIRM_QUORUM: i64 = 2;

/// Disputes needed before a claim reads as disputed.
pub const DISPUTE_QUORUM: i64 = 2;

/// One catalogue entry, as this project stands on it.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Row {
    /// The capability's key.
    pub key: String,
    /// Its label.
    pub label: String,
    /// Its category.
    pub category: String,
    /// Whether this project has a claim at all. `false` means nobody has said anything — not that the answer is
    /// no.
    pub asserted: bool,
    /// The claimed value, absent when nothing is asserted. Never substituted with "unknown".
    #[serde(skip_serializing_if = "String::is_empty")]
    pub value: String,
    /// `asserted`, `confirmed` or `disputed`, absent when nothing is asserted.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub state: String,
    /// Confirmations by somebody other than the asserter.
    #[serde(skip_serializing_if = "is_zero")]
    pub confirms: i64,
    /// Disputes by somebody other than the asserter.
    #[serde(skip_serializing_if = "is_zero")]
    pub disputes: i64,
    /// The evidence offered with the claim.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub evidence: String,
}

fn is_zero(value: &i64) -> bool {
    *value == 0
}

/// The panel: every capability, this project's state where one exists.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Panel {
    /// The project.
    pub project_id: i64,
    /// Every catalogue capability, in catalogue order.
    pub capabilities: Vec<Row>,
    /// How many have no claim.
    pub unknown_count: i64,
    /// How many have one, whatever its state: a disputed claim is an assertion, not an absence.
    pub asserted_count: i64,
}

/// Read the panel.
///
/// # Errors
/// `sqlx::Error`.
pub async fn panel(pool: &PgPool, project_id: i64) -> Result<Panel, sqlx::Error> {
    let catalogue = sqlx::query_as::<_, (String, String, String)>(
        "select key, label, category from capabilities order by category, key",
    )
    .fetch_all(pool)
    .await?;
    // The store answers at most one assertion per (project, capability) — AssertCapability upserts — so a plain
    // map cannot drop a row, and two rows would be a store bug rather than an input.
    // `confirmed` is a BIGINT on this schema (the Go stored 0/1 and the generator kept INTEGER affinity), so it
    // is COMPARED rather than used as a boolean: PostgreSQL refuses AND on a bigint, and the failure surfaces as
    // "argument of AND must be type boolean" — an error that names the type and not the column.
    let assertions = sqlx::query_as::<_, (String, String, String, i64, i64)>(
        "select a.capability, a.value, coalesce(a.evidence, ''),
                (select count(*) from capability_confirmations c
                  where c.assertion_id = a.id and c.confirmed <> 0 and c.user_id <> a.asserted_by),
                (select count(*) from capability_confirmations c
                  where c.assertion_id = a.id and c.confirmed = 0 and c.user_id <> a.asserted_by)
           from capability_assertions a where a.project_id = $1
          order by a.capability",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;
    let by_key: HashMap<String, (String, String, i64, i64)> = assertions
        .into_iter()
        .map(|(capability, value, evidence, confirms, disputes)| {
            (capability, (value, evidence, confirms, disputes))
        })
        .collect();

    let mut panel = Panel {
        project_id,
        capabilities: Vec::with_capacity(catalogue.len()),
        unknown_count: 0,
        asserted_count: 0,
    };
    for (key, label, category) in catalogue {
        match by_key.get(&key) {
            Some((value, evidence, confirms, disputes)) => {
                let (confirms, disputes) = (*confirms, *disputes);
                // A dispute outranks a confirmation, at the same quorum: two of each is contested.
                let state = if disputes >= DISPUTE_QUORUM {
                    "disputed"
                } else if confirms >= CONFIRM_QUORUM {
                    "confirmed"
                } else {
                    "asserted"
                };
                panel.asserted_count += 1;
                panel.capabilities.push(Row {
                    key,
                    label,
                    category,
                    asserted: true,
                    value: value.clone(),
                    state: state.to_string(),
                    confirms,
                    disputes,
                    evidence: evidence.clone(),
                });
            }
            None => {
                panel.unknown_count += 1;
                panel.capabilities.push(Row {
                    key,
                    label,
                    category,
                    asserted: false,
                    value: String::new(),
                    state: String::new(),
                    confirms: 0,
                    disputes: 0,
                    evidence: String::new(),
                });
            }
        }
    }
    Ok(panel)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_unasserted_capability_serialises_without_a_value_or_a_state() {
        let row = Row {
            key: "cap:ci=yes".to_string(),
            label: "CI".to_string(),
            category: "process".to_string(),
            asserted: false,
            value: String::new(),
            state: String::new(),
            confirms: 0,
            disputes: 0,
            evidence: String::new(),
        };
        let json = serde_json::to_value(&row).unwrap();
        // The absence is the point: "unknown" as a VALUE would read as an answer.
        assert!(json.get("value").is_none());
        assert!(json.get("state").is_none());
        assert_eq!(json["asserted"], false);
    }

    #[test]
    fn the_quorums_are_two_and_a_dispute_wins_a_tie() {
        assert_eq!(CONFIRM_QUORUM, DISPUTE_QUORUM);
        // The rule is a `>=` on disputes and a `>=` on confirms, in that order, so equal counts are disputed.
        let confirms = 2;
        let disputes = 2;
        let state = if disputes >= DISPUTE_QUORUM {
            "disputed"
        } else if confirms >= CONFIRM_QUORUM {
            "confirmed"
        } else {
            "asserted"
        };
        assert_eq!(state, "disputed");
    }
}
