//! §4.10's field reports panel: "worked for 83% of reporters on arm64".
//!
//! The half of that sentence that carries meaning is **"of reporters"**, so this read model puts the denominator
//! beside the rate and separates three facts a single number would merge:
//!
//! * **no reports** — `outcome_rate` absent, `sample_size: 0`. Not a zero: a zero says reporters tried and it did
//!   not work, which is a claim about a project nobody has tried;
//! * **all failures** — `outcome_rate: 0` with a sample size, which is a real answer;
//! * **one report** — `100%` with `sample_size: 1`, which is established by nobody. The sample size is the only
//!   thing that stops a caller presenting it as settled, so it is always present.
//!
//! The rate is **reputation-weighted** (`1 + min(rep, 100)/100`) and `worked-with-caveats` counts **half**: a
//! report that says "it worked, with these three caveats" is neither a success nor a failure, and scoring it as
//! either erases the caveat. Environment breakdown skips reports that named no environment rather than grouping
//! them under `""` — an unnamed environment is not an environment.

use sqlx::PgPool;

/// Reputation above which extra points stop adding weight. Unbounded weighting turns the panel into a
/// popularity contest between two prolific users.
pub const MAX_REPORT_REPUTATION: f64 = 100.0;

/// Credit for `worked-with-caveats`.
pub const CAVEAT_CREDIT: f64 = 0.5;

/// How many reports the page renders.
pub const PAGE_LIMIT: usize = 10;

/// Weight for one reporter's reputation.
#[must_use]
pub fn report_weight(reputation: f64) -> f64 {
    1.0 + reputation.max(0.0).min(MAX_REPORT_REPUTATION) / MAX_REPORT_REPUTATION
}

/// Credit for one outcome.
#[must_use]
pub fn outcome_credit(outcome: &str) -> f64 {
    match outcome {
        "worked" => 1.0,
        "worked-with-caveats" => CAVEAT_CREDIT,
        _ => 0.0,
    }
}

/// One report.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Report {
    /// The row.
    pub id: i64,
    /// The project.
    pub project_id: i64,
    /// Who filed it.
    pub user_id: i64,
    /// The version tried.
    pub version: String,
    /// What for.
    pub use_case: String,
    /// Where.
    pub environment: String,
    /// How much.
    pub scale: String,
    /// How long.
    pub duration: String,
    /// What happened.
    pub outcome: String,
    /// Where they went instead, when it failed.
    pub migrated_to: String,
    /// The caveats.
    pub caveats: String,
    /// The workaround.
    pub workaround: String,
    /// The advice.
    pub advice: String,
    /// Whether the panel hides it. Always `false` here: a removed report is hidden, not deleted, and serving it
    /// would undo the quorum decision that removed it.
    pub removed: bool,
    /// Who removed it.
    pub removed_by: i64,
    /// Why.
    pub removal_reason: String,
    /// Filed, unix seconds.
    pub created_at: f64,
    /// The owner's replies, in order.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub responses: Vec<Response>,
}

/// One owner reply.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Response {
    /// The row.
    pub id: i64,
    /// The report it answers.
    pub report_id: i64,
    /// Who wrote it.
    pub user_id: i64,
    /// What they said.
    pub body: String,
    /// Whether they held an owner badge at the time — the project speaking, or a reader answering.
    pub is_owner: bool,
    /// Written, unix seconds.
    pub created_at: f64,
}

/// The panel.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Panel {
    /// The project.
    pub project_id: i64,
    /// The weighted success rate, absent when nothing was reported. Never `0` for no data.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub outcome_rate: Option<f64>,
    /// The denominator: how many reports the rate rests on. Always present, including at zero.
    pub sample_size: i64,
    /// Per-environment rate, absent when no report named an environment.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub by_environment: Option<std::collections::BTreeMap<String, f64>>,
    /// The rendered page, newest first.
    pub reports: Vec<Report>,
    /// How many are on the page.
    pub shown: i64,
    /// How many are not. Silently truncating a list of 400 reads as "there are ten".
    pub omitted: i64,
}

type Row = (i64, i64, String, String, String, String, String, String, String, String, String, String, i64, i64, String, f64);

/// Read the panel.
///
/// # Errors
/// `sqlx::Error`.
pub async fn panel(pool: &PgPool, project_id: i64) -> Result<Panel, sqlx::Error> {
    // Removal is a HIDE, not a delete, so the filter is here and not in the schema.
    let rows = sqlx::query_as::<_, Row>(
        "select id, user_id, version, use_case, environment, scale, duration, outcome, migrated_to,
                coalesce(caveats, ''), coalesce(workaround, ''), coalesce(advice, ''), 0::bigint,
                coalesce(removed_by, 0), coalesce(removal_reason, ''),
                extract(epoch from created_at)::double precision
           from field_reports
          where project_id = $1 and removed = 0
          order by created_at desc, id desc",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;

    // Weights come from the same rows, so the rate and the list cannot disagree about which reports count.
    let reputations = sqlx::query_as::<_, (String, String, f64)>(
        "select r.environment, r.outcome,
                coalesce((select sum(points) from reputation_events e
                           where e.project_id = r.project_id and e.user_id = r.user_id), 0)
           from field_reports r
          where r.project_id = $1 and r.removed = 0",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;

    let mut weighted = 0.0;
    let mut weights = 0.0;
    let mut env_weighted: std::collections::BTreeMap<String, f64> = std::collections::BTreeMap::new();
    let mut env_weights: std::collections::BTreeMap<String, f64> = std::collections::BTreeMap::new();
    for (environment, outcome, reputation) in &reputations {
        let weight = report_weight(*reputation);
        let credit = outcome_credit(outcome);
        weighted += weight * credit;
        weights += weight;
        if environment.is_empty() {
            continue;
        }
        *env_weighted.entry(environment.clone()).or_insert(0.0) += weight * credit;
        *env_weights.entry(environment.clone()).or_insert(0.0) += weight;
    }

    let mut panel = Panel {
        project_id,
        // The rate exists only when something was reported: `sample_size > 0` is the test, not `rate > 0`.
        outcome_rate: if weights > 0.0 { Some(weighted / weights) } else { None },
        sample_size: i64::try_from(reputations.len()).unwrap_or(0),
        by_environment: if env_weights.is_empty() {
            None
        } else {
            Some(
                env_weights
                    .iter()
                    .filter(|(_, w)| **w > 0.0)
                    .map(|(env, w)| (env.clone(), env_weighted[env] / w))
                    .collect(),
            )
        },
        reports: Vec::new(),
        shown: 0,
        omitted: 0,
    };

    let total = i64::try_from(rows.len()).unwrap_or(0);
    for (index, row) in rows.into_iter().enumerate() {
        let (id, user_id, version, use_case, environment, scale, duration, outcome, migrated_to, caveats,
             workaround, advice, _, removed_by, removal_reason, created_at) = row;
        panel.shown += if index < PAGE_LIMIT { 1 } else { 0 };
        if index >= PAGE_LIMIT {
            continue;
        }
        let report = id;
        let responses = sqlx::query_as::<_, (i64, i64, String, i64, f64)>(
            "select id, user_id, body, is_owner,
                    extract(epoch from created_at)::double precision
               from field_report_responses where report_id = $1
              order by created_at, id",
        )
        .bind(id)
        .fetch_all(pool)
        .await?
        .into_iter()
        .map(|(id, user_id, body, is_owner, created_at)| Response {
            id,
            report_id: report,
            user_id,
            body,
            is_owner: is_owner != 0,
            created_at,
        })
        .collect();
        panel.reports.push(Report {
            id,
            project_id,
            user_id,
            version,
            use_case,
            environment,
            scale,
            duration,
            outcome,
            migrated_to,
            caveats,
            workaround,
            advice,
            removed: false,
            removed_by,
            removal_reason,
            created_at,
            responses,
        });
    }
    panel.omitted = total - panel.shown;
    Ok(panel)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn weight_is_one_plus_the_capped_share_and_never_below_one() {
        assert_eq!(report_weight(0.0), 1.0);
        assert_eq!(report_weight(-40.0), 1.0);
        assert_eq!(report_weight(50.0), 1.5);
        assert_eq!(report_weight(100.0), 2.0);
        // Above the cap the weight stops growing: otherwise two prolific users decide the panel.
        assert_eq!(report_weight(1_000.0), 2.0);
    }

    #[test]
    fn caveats_count_half_and_everything_else_zero() {
        assert_eq!(outcome_credit("worked"), 1.0);
        assert_eq!(outcome_credit("worked-with-caveats"), 0.5);
        assert_eq!(outcome_credit("abandoned"), 0.0);
        assert_eq!(outcome_credit("migrated-away"), 0.0);
        assert_eq!(outcome_credit(""), 0.0);
    }

    #[test]
    fn no_reports_is_absent_and_never_zero() {
        let panel = Panel {
            project_id: 1,
            outcome_rate: None,
            sample_size: 0,
            by_environment: None,
            reports: Vec::new(),
            shown: 0,
            omitted: 0,
        };
        let json = serde_json::to_value(&panel).unwrap();
        assert!(json.get("outcome_rate").is_none(), "a zero would claim reporters tried it");
        assert_eq!(json["sample_size"], 0);
    }
}
