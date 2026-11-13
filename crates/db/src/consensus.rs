//! Consensus calls: the read side — `/api/v1/projects/{slug}/consensus[/{call_id}]`.
//!
//! Three rules from the Go store are carried across, and the first is the one a port is most likely to
//! lose because it looks like a display decision rather than a rule:
//!
//! 1. **§6.6's hidden tally.** "Running counts are hidden until the call closes, to prevent
//!    bandwagoning. Participation progress and the quorum bar remain visible." Enforced *on the
//!    server*: a client-side rule is bypassed by devtools, which defeats it instead of implementing
//!    it. What stays visible is exactly what §6.6 preserves — participation, the quorum bar — plus the
//!    caller's own position, because the rule hides the tally, not a voter's own act.
//! 2. **Eligible is computed, not assumed.** `quorum_threshold(eligible)` returns 0 when `eligible` is
//!    0, and the quorum check is `participants < threshold`, so an unset eligible makes every call
//!    satisfy quorum by definition: one member consenting to their own call was enough to accept it.
//! 3. **`feature_id` null means 0, and the key is always present.** §6.6's process decisions (an
//!    emergency-hold confirmation, a charter amendment) have no feature, and `feature_id: null` would
//!    make every consumer handle a second shape for the same absence.

use serde::Serialize;
use serde_json::{json, Value};
use sqlx::PgPool;

use concord_governance as governance;

/// §8.2's stated default trailing window.
pub const DEFAULT_ACTIVITY_WINDOW_DAYS: f64 = 365.0;

/// A consensus call, in the Go struct's wire shape.
#[derive(Debug, Clone, Serialize)]
pub struct Call {
    pub id: i64,
    pub project_id: i64,
    /// 0 for a process decision that is not about one feature.
    pub feature_id: i64,
    pub opened_by: i64,
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub opens_at: f64,
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub closes_at: f64,
    pub status: String,
    /// Present and null when there is no verdict — the Go field has no `omitempty`.
    pub result: Option<String>,
    /// Present and null while the call is open.
    pub summary: Option<String>,
    /// Zero in the list endpoint: the Go feed query does not select it, and the field is not
    /// `omitempty`, so the wire carries `created_at: 0`. Reproduced rather than corrected, because a
    /// client that reads it as a timestamp would otherwise start seeing a value where it saw none.
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub created_at: f64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub closed_at: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub question: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// §6.5's subject: the solution a call opens on. Absent for a process decision.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub solution_id: Option<i64>,
    /// §6.5's "early calls are flagged as such". Absent for a call that predates the column, so it
    /// reads as "not flagged" rather than claiming it was considered.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub opened_early: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub early_reason: Option<String>,
    /// §6.5's decision about the solution, distinct from `result`, which is §6.6's verdict on the call.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub outcome: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub fallback_to_solution_id: Option<i64>,
}

/// One position on a call.
#[derive(Debug, Clone, Serialize)]
pub struct Position {
    pub call_id: i64,
    pub user_id: i64,
    pub position: String,
    pub reason: String,
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub updated_at: f64,
}

/// One objection.
#[derive(Debug, Clone, Serialize)]
pub struct Objection {
    pub id: i64,
    pub call_id: i64,
    pub user_id: i64,
    pub principle: String,
    pub violation: String,
    pub remedy: String,
    pub status: String,
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub created_at: f64,
    /// The maintainer's written answer. Empty while the objection is open: an absent key and an empty
    /// one look the same to a client that does not check for null, so the Go store `COALESCE`s it.
    pub resolution: String,
}

const CALL_COLUMNS: &str = "id, project_id, coalesce(feature_id, 0), coalesce(opened_by, 0), \
     extract(epoch from opens_at)::double precision, \
     extract(epoch from closes_at)::double precision, status, result, summary, \
     case when closed_at is null then null \
          else extract(epoch from closed_at)::double precision end, \
     question, description, solution_id, \
     case when opened_early is null then null else opened_early <> 0 end, \
     early_reason, outcome, fallback_to_solution_id";

fn call_from_row(row: &sqlx::postgres::PgRow) -> Result<Call, sqlx::Error> {
    use sqlx::Row;
    Ok(Call {
        id: row.try_get(0)?,
        project_id: row.try_get(1)?,
        feature_id: row.try_get(2)?,
        opened_by: row.try_get(3)?,
        opens_at: row.try_get(4)?,
        closes_at: row.try_get(5)?,
        status: row.try_get(6)?,
        result: row.try_get(7)?,
        summary: row.try_get(8)?,
        // Not selected by the Go query either, so it is 0 on the wire in both services.
        created_at: 0.0,
        closed_at: row.try_get(9)?,
        question: row.try_get(10)?,
        description: row.try_get(11)?,
        solution_id: row.try_get(12)?,
        opened_early: row.try_get(13)?,
        early_reason: row.try_get(14)?,
        outcome: row.try_get(15)?,
        fallback_to_solution_id: row.try_get(16)?,
    })
}

/// A project's calls, newest first, capped at 50 unless the caller asks for up to 100.
///
/// The cap is the Go feed's: `limit <= 0 || limit > 100` falls back to 50 — one clamp here rather than
/// the audit log's two, because this endpoint's limit is not exposed to the caller at all.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn list_calls(
    pool: &PgPool,
    project_id: i64,
    limit: i64,
) -> Result<Vec<Call>, sqlx::Error> {
    let limit = if limit <= 0 || limit > 100 { 50 } else { limit };
    // The feed's column list, deliberately narrower than [`CALL_COLUMNS`]: it does not select
    // `created_at`, which is why that field is 0 on the wire here.
    let sql = "select id, project_id, coalesce(feature_id, 0), coalesce(opened_by, 0), \
                      extract(epoch from opens_at)::double precision, \
                      extract(epoch from closes_at)::double precision, status, \
                      question, description, result, \
                      case when opened_early is null then null else opened_early <> 0 end, \
                      case when closed_at is null then null \
                           else extract(epoch from closed_at)::double precision end \
               from consensus_calls where project_id = $1 \
               order by opens_at desc, id desc limit $2";
    let rows = sqlx::query(sql)
        .bind(project_id)
        .bind(limit)
        .fetch_all(pool)
        .await?;
    use sqlx::Row;
    rows.iter()
        .map(|row| {
            Ok(Call {
                id: row.try_get(0)?,
                project_id: row.try_get(1)?,
                feature_id: row.try_get(2)?,
                opened_by: row.try_get(3)?,
                opens_at: row.try_get(4)?,
                closes_at: row.try_get(5)?,
                status: row.try_get(6)?,
                result: row.try_get(9)?,
                summary: None,
                created_at: 0.0,
                closed_at: row.try_get(11)?,
                question: row.try_get(7)?,
                description: row.try_get(8)?,
                solution_id: None,
                opened_early: row.try_get(10)?,
                early_reason: None,
                outcome: None,
                fallback_to_solution_id: None,
            })
        })
        .collect()
}

/// One call by id, `None` when it does not exist.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn call(pool: &PgPool, id: i64) -> Result<Option<Call>, sqlx::Error> {
    let sql = format!("select {CALL_COLUMNS} from consensus_calls where id = $1");
    sqlx::query(&sql)
        .bind(id)
        .fetch_optional(pool)
        .await?
        .map(|row| call_from_row(&row))
        .transpose()
}

/// A call's positions, oldest first.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn positions(pool: &PgPool, call_id: i64) -> Result<Vec<Position>, sqlx::Error> {
    use sqlx::Row;
    let rows = sqlx::query(
        "select call_id, user_id, position, coalesce(reason, ''), \
                extract(epoch from updated_at)::double precision \
         from positions where call_id = $1 order by updated_at asc",
    )
    .bind(call_id)
    .fetch_all(pool)
    .await?;
    rows.iter()
        .map(|row| {
            Ok(Position {
                call_id: row.try_get(0)?,
                user_id: row.try_get(1)?,
                position: row.try_get(2)?,
                reason: row.try_get(3)?,
                updated_at: row.try_get(4)?,
            })
        })
        .collect()
}

/// A call's objections, oldest first.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn objections(pool: &PgPool, call_id: i64) -> Result<Vec<Objection>, sqlx::Error> {
    use sqlx::Row;
    let rows = sqlx::query(
        "select id, call_id, user_id, principle, violation, remedy, status, \
                extract(epoch from created_at)::double precision, coalesce(resolution, '') \
         from objections where call_id = $1 order by created_at",
    )
    .bind(call_id)
    .fetch_all(pool)
    .await?;
    rows.iter()
        .map(|row| {
            Ok(Objection {
                id: row.try_get(0)?,
                call_id: row.try_get(1)?,
                user_id: row.try_get(2)?,
                principle: row.try_get(3)?,
                violation: row.try_get(4)?,
                remedy: row.try_get(5)?,
                status: row.try_get(6)?,
                created_at: row.try_get(7)?,
                resolution: row.try_get(8)?,
            })
        })
        .collect()
}

/// §8.2's eligible collaborators: role **and** a qualifying contribution inside the trailing window.
///
/// [`governance::quorum_threshold`] returns 0 when `eligible` is 0, and the quorum check is
/// `participants < threshold`, so an unset eligible makes every call satisfy quorum by definition —
/// one member consenting to their own call was enough to accept it in a project with three eligible
/// voters. The count the page shows and the threshold enforced come from this one function.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn eligible_collaborators(
    pool: &PgPool,
    project_id: i64,
    window_days: f64,
) -> Result<Vec<i64>, sqlx::Error> {
    let window = if window_days <= 0.0 {
        DEFAULT_ACTIVITY_WINDOW_DAYS
    } else {
        window_days
    };
    // §8.2's routes, as one union with the most recent qualifying event per (project, user):
    // (1) merged code, (1) docs written through the API, (2) a validated complaint that led to a
    // shipped solution, (3) an accepted solution, (4) a substantive review (an approval on somebody
    // else's merge request), (5) an upheld moderation action (a justified ledger entry).
    //
    // "Seated by charter consensus" has no table to count, and agents have no flag to exclude — both
    // are gaps in the schema, recorded in the Go source rather than papered over here.
    let sql = "\
        with qualifying as ( \
            select project_id, user_id, max(created_at) as last_qualifying from ( \
                select m.project_id, m.author_id as user_id, coalesce(m.closed_at, m.opened_at) as created_at \
                  from merge_requests m where m.status = 'merged' \
                union all \
                select d.project_id, d.author_id, d.updated_at from project_documents d \
                union all \
                select c.project_id, c.author_id, max(s.updated_at) \
                  from complaints c \
                  join feature_complaints fc on fc.complaint_id = c.id \
                  join features f on f.id = fc.feature_id and f.project_id = c.project_id \
                  join solutions s on s.feature_id = f.id \
                 where c.status in ('validated','linked','closed') \
                   and s.status in ('consensus','ready','in_progress','review','shipped') \
                 group by c.project_id, c.author_id \
                union all \
                select f.project_id, s.author_id, s.updated_at \
                  from solutions s join features f on f.id = s.feature_id \
                 where s.status in ('consensus','ready','in_progress','review','shipped') \
                union all \
                select m.project_id, ma.user_id, ma.created_at \
                  from merge_approvals ma join merge_requests m on m.id = ma.mr_id \
                 where ma.user_id <> m.author_id \
                union all \
                select al.project_id, al.actor_id, al.created_at \
                  from admin_ledger al \
                 where al.actor_id is not null and al.project_id is not null \
                   and al.justification is not null and trim(al.justification) <> '' \
            ) parts group by project_id, user_id \
        ) \
        select q.user_id from qualifying q \
         where q.project_id = $1 \
           and q.last_qualifying >= now() - ($2::double precision * interval '1 day') \
           and exists (select 1 from members m \
                        where m.project_id = q.project_id and m.user_id = q.user_id \
                          and m.role in ('contributor','reviewer','maintainer','owner')) \
         order by q.user_id";
    sqlx::query_scalar(sql)
        .bind(project_id)
        .bind(window)
        .fetch_all(pool)
        .await
}

/// The counts a verdict is computed from.
///
/// # Errors
/// [`sqlx::Error`] when a read fails.
pub async fn tally(
    pool: &PgPool,
    project_id: i64,
    call_id: i64,
) -> Result<governance::ConsensusCounts, sqlx::Error> {
    let mut counts = governance::ConsensusCounts::default();
    for position in positions(pool, call_id).await? {
        counts.participants += 1;
        match position.position.as_str() {
            "consent" => counts.consent += 1,
            "stand_aside" => counts.stand_aside += 1,
            "block" => counts.block += 1,
            "abstain" => counts.abstain += 1,
            _ => {}
        }
    }
    for objection in objections(pool, call_id).await? {
        if objection.status == "open" {
            counts.open_objections += 1;
        }
    }
    counts.eligible = eligible_collaborators(pool, project_id, DEFAULT_ACTIVITY_WINDOW_DAYS)
        .await?
        .len() as i64;
    Ok(counts)
}

/// The thresholds summary — both ratios and both bars, in one object.
///
/// §6.6 requires two ratios with different denominators, and a client that recomputes them can be
/// wrong about what consent means without anything looking broken. This is also what makes the hidden
/// tally expressible: the same object, with the counts blanked.
#[must_use]
pub fn thresholds_summary(
    counts: &governance::ConsensusCounts,
    charter: &governance::Charter,
) -> Value {
    json!({
        "consent": counts.consent,
        "abstain": counts.abstain,
        "stand_aside": counts.stand_aside,
        "block": counts.block,
        "participants": counts.participants,
        "eligible": counts.eligible,
        "quorum_required": governance::quorum_threshold(counts.eligible, charter),
        "support_ratio": counts.support_ratio(),
        "decisive_ratio": counts.decisive_ratio(),
        "support_required": charter.support_ratio_min,
        "decisive_required": charter.consent_ratio,
        "reluctant": counts.reluctant(),
        "open_objections": counts.open_objections,
    })
}

/// Blank the counts while a call is open, keeping only what §6.6 preserves.
///
/// `participants`, `eligible` and `quorum_required` stay: §6.6 hides the tally, not the progress toward
/// being able to decide. Everything the tally would reveal becomes null rather than absent — a key that
/// disappears is a shape a client can miss, and one that is `null` is a statement.
#[must_use]
pub fn hidden_tally(summary: Value) -> Value {
    let Value::Object(mut map) = summary else {
        return summary;
    };
    for key in [
        "consent",
        "abstain",
        "stand_aside",
        "block",
        "support_ratio",
        "decisive_ratio",
        "reluctant",
        "open_objections",
    ] {
        if map.contains_key(key) {
            map.insert(key.to_string(), Value::Null);
        }
    }
    Value::Object(map)
}

/// Reduce a call's positions to the caller's own row.
///
/// An empty list rather than a missing key, so a client iterating it does not have to handle null. A
/// caller with no position gets an empty list, which is also what "you have not voted" looks like.
#[must_use]
pub fn own_positions_only(positions: Vec<Position>, actor_id: i64) -> Vec<Position> {
    if actor_id == 0 {
        return Vec::new();
    }
    positions
        .into_iter()
        .filter(|position| position.user_id == actor_id)
        .collect()
}
