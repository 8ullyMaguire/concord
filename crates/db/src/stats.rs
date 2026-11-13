//! §3's statistics: what exists (the census), how much of it is instrumented, and the three latencies.
//!
//! Three rules from the spec, each of which is a way an honest-looking number lies:
//!
//! * **The provenance split is three-way, and the third column is the point.** `origin` is `api`/`ui`
//!   (organic), `seed`/`import` (seeded), or `unknown` — and `unknown` is not "organic", it is the population
//!   that predates the backfill. A two-way split reports the seed population as organic.
//! * **A median carries its `n`, and an even sample uses the lower-middle value, never an interpolation.**
//!   With two samples {10, 20} the median is 10, not 15: the published number must be a value the instance
//!   actually produced.
//! * **A row with no start is never a zero-hour episode.** Absent is not fast, so `unfinished` and
//!   `unmeasured` are reported beside `n` and never folded into it.

use serde::Serialize;
use sqlx::{PgPool, Row};

/// One value of a three-way provenance facet.
#[derive(Debug, Clone, Serialize)]
pub struct ProvenanceCount {
    /// The facet's value (`draft`, `M`, …).
    pub value: String,
    /// Every row with that value.
    pub total: i64,
    /// `origin IN ('api','ui')` — written through the product.
    pub organic: i64,
    /// `origin IN ('seed','import')` — brought in by a batch.
    pub seeded: i64,
    /// `origin = 'unknown'` — predates the provenance backfill. NOT organic.
    pub unknown: i64,
}

/// One value of a plain facet: a table that cannot be filled by an import.
#[derive(Debug, Clone, Serialize)]
pub struct FacetCount {
    /// The facet's value.
    pub value: String,
    /// How many rows carry it.
    pub count: i64,
}

/// The composition of the instance: what exists, not what happened.
#[derive(Debug, Clone, Serialize)]
pub struct Census {
    /// Features, by status, with the provenance split.
    pub features_by_status: Vec<ProvenanceCount>,
    /// Features, by effort, with the provenance split.
    pub features_by_effort: Vec<ProvenanceCount>,
    /// Complaints, by status, with the provenance split.
    pub complaints_by_status: Vec<ProvenanceCount>,
    /// Projects, by language (25 at most).
    pub projects_by_language: Vec<FacetCount>,
    /// Projects, by tag (25 at most).
    pub projects_by_tag: Vec<FacetCount>,
    /// Projects, by license, with `''` reported as `unknown` (25 at most).
    pub projects_by_license: Vec<FacetCount>,
    /// Projects, by governance model.
    pub projects_by_governance: Vec<FacetCount>,
    /// Projects, by visibility.
    pub projects_by_visibility: Vec<FacetCount>,
    /// Members, by role.
    pub members_by_role: Vec<FacetCount>,
    /// Documents, by kind.
    pub documents_by_kind: Vec<FacetCount>,
    /// Arenas, by type.
    pub arenas_by_type: Vec<FacetCount>,
}

/// How much of the instance is instrumented, as opposed to how much exists.
#[derive(Debug, Clone, Serialize)]
pub struct Measured {
    /// Every project.
    pub projects: i64,
    /// Projects whose `health_score` has been computed.
    pub with_health: i64,
    /// Features a writer that names its source has touched (`origin <> 'unknown'`).
    pub with_origin: i64,
}

/// One latency measure.
#[derive(Debug, Clone, Serialize)]
pub struct Latency {
    /// Median hours, and zero when `n` is zero — never a median of nothing.
    pub median_hours: f64,
    /// How many episodes contributed a duration.
    pub n: i64,
    /// Started, never ended.
    pub unfinished: i64,
    /// Ended, never started — finished work with no recorded beginning.
    pub unmeasured: i64,
    /// Always `hours`, so a reader never has to guess the unit.
    pub unit: &'static str,
}

/// The three latencies, each carrying its own `n`.
#[derive(Debug, Clone, Serialize)]
pub struct LatencySection {
    /// Complaint created → first `validate_complaint`.
    pub triage: Latency,
    /// Feature entering `ready` → entering `shipped`.
    pub delivery: Latency,
    /// Consensus call opened → closed.
    pub decision: Latency,
}

/// `GET /api/v1/stats` — the one payload the statistics page renders from.
#[derive(Debug, Clone, Serialize)]
pub struct StatsPayload {
    /// The census.
    pub census: Census,
    /// The three medians.
    pub latency: LatencySection,
    /// How much is instrumented.
    pub measured: Measured,
    /// When this was computed, in unix seconds.
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub generated_at: f64,
}

/// One row's own timeline, with both ends optional.
///
/// The optionals are the whole point: `HasStart && HasEnd` is measured, `!HasStart && HasEnd` is unmeasured
/// (finished, but no recorded start), `HasStart && !HasEnd` is unfinished, and neither is a row with no fact to
/// report at all.
#[derive(Debug, Clone, Copy)]
pub struct Episode {
    /// When it started.
    pub started_at: f64,
    /// When it ended.
    pub ended_at: f64,
    /// Whether the start is known.
    pub has_start: bool,
    /// Whether the end is known.
    pub has_end: bool,
}

/// Reduce episodes to a [`Latency`].
///
/// The rules the spec fixes, in one place:
///
/// * an even sample uses the **lower**-middle value, never an interpolation;
/// * a row with no start is **never** a zero-hour episode;
/// * a negative duration is clamped to zero and still counted, so one corrupt row cannot drag the median
///   negative and cannot hide either.
#[must_use]
pub fn measure(episodes: &[Episode]) -> Latency {
    let mut durations = Vec::with_capacity(episodes.len());
    let mut unfinished = 0;
    let mut unmeasured = 0;
    for episode in episodes {
        match (episode.has_start, episode.has_end) {
            (true, true) => {
                let hours = (episode.ended_at - episode.started_at) / 3600.0;
                durations.push(if hours < 0.0 { 0.0 } else { hours });
            }
            (true, false) => unfinished += 1,
            (false, true) => unmeasured += 1,
            (false, false) => {}
        }
    }
    let n = durations.len() as i64;
    Latency {
        median_hours: median_lower_middle(&mut durations),
        n,
        unfinished,
        unmeasured,
        unit: "hours",
    }
}

/// The lower of the two middle values of an even-length sample. Sorts in place.
#[must_use]
pub fn median_lower_middle(values: &mut [f64]) -> f64 {
    let n = values.len();
    if n == 0 {
        return 0.0;
    }
    values.sort_by(f64::total_cmp);
    if n % 2 == 1 {
        values[n / 2]
    } else {
        values[n / 2 - 1]
    }
}

/// The instance census. Nine grouped queries, and no join that could multiply a facet.
///
/// # Errors
/// `sqlx::Error`.
pub async fn census(pool: &PgPool) -> Result<Census, sqlx::Error> {
    Ok(Census {
        features_by_status: provenance_facet(
            pool,
            "select status, count(*), \
                    sum(case when origin in ('api','ui') then 1 else 0 end), \
                    sum(case when origin in ('seed','import') then 1 else 0 end), \
                    sum(case when origin = 'unknown' then 1 else 0 end) \
               from features group by status order by status",
        )
        .await?,
        features_by_effort: provenance_facet(
            pool,
            "select effort, count(*), \
                    sum(case when origin in ('api','ui') then 1 else 0 end), \
                    sum(case when origin in ('seed','import') then 1 else 0 end), \
                    sum(case when origin = 'unknown' then 1 else 0 end) \
               from features group by effort order by effort",
        )
        .await?,
        complaints_by_status: provenance_facet(
            pool,
            "select status, count(*), \
                    sum(case when origin in ('api','ui') then 1 else 0 end), \
                    sum(case when origin in ('seed','import') then 1 else 0 end), \
                    sum(case when origin = 'unknown' then 1 else 0 end) \
               from complaints group by status order by status",
        )
        .await?,
        projects_by_language: count_facet(
            pool,
            "select language, count(*) from project_languages \
             group by language order by count(*) desc, language limit 25",
        )
        .await?,
        projects_by_tag: count_facet(
            pool,
            "select t.name, count(*) from project_tags pt join tags t on t.id = pt.tag_id \
             group by t.name order by count(*) desc, t.name limit 25",
        )
        .await?,
        projects_by_license: count_facet(
            pool,
            "select coalesce(nullif(license, ''), 'unknown'), count(*) from projects \
             group by 1 order by count(*) desc, 1 limit 25",
        )
        .await?,
        projects_by_governance: count_facet(
            pool,
            "select governance_model, count(*) from projects \
             group by governance_model order by count(*) desc, governance_model",
        )
        .await?,
        projects_by_visibility: count_facet(
            pool,
            "select visibility, count(*) from projects \
             group by visibility order by count(*) desc, visibility",
        )
        .await?,
        members_by_role: count_facet(
            pool,
            "select role, count(*) from members group by role order by count(*) desc, role",
        )
        .await?,
        // `project_documents`, not `documents`: a query against the plan's shorter name is a runtime error
        // rather than a compile error, which is how this line was found the first time.
        documents_by_kind: count_facet(
            pool,
            "select kind, count(*) from project_documents group by kind order by count(*) desc, kind",
        )
        .await?,
        arenas_by_type: count_facet(
            pool,
            "select type, count(*) from arenas group by type order by count(*) desc, type",
        )
        .await?,
    })
}

async fn provenance_facet(pool: &PgPool, sql: &str) -> Result<Vec<ProvenanceCount>, sqlx::Error> {
    let rows = sqlx::query(sql).fetch_all(pool).await?;
    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        out.push(ProvenanceCount {
            value: row.try_get(0)?,
            total: row.try_get(1)?,
            organic: row.try_get(2)?,
            seeded: row.try_get(3)?,
            unknown: row.try_get(4)?,
        });
    }
    Ok(out)
}

async fn count_facet(pool: &PgPool, sql: &str) -> Result<Vec<FacetCount>, sqlx::Error> {
    let rows = sqlx::query(sql).fetch_all(pool).await?;
    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        out.push(FacetCount {
            value: row.try_get(0)?,
            count: row.try_get(1)?,
        });
    }
    Ok(out)
}

/// How much of the instance is instrumented.
///
/// # Errors
/// `sqlx::Error`.
pub async fn measured(pool: &PgPool) -> Result<Measured, sqlx::Error> {
    let projects: i64 = sqlx::query_scalar("select count(*) from projects")
        .fetch_one(pool)
        .await?;
    // `health_score` lives on `project_metrics`, not `projects`: the other spelling is a runtime error.
    let with_health: i64 =
        sqlx::query_scalar("select count(*) from project_metrics where health_score is not null")
            .fetch_one(pool)
            .await?;
    // `origin <> 'unknown'` is "a writer that names its source has touched this row" — `unknown` is the
    // backfill, not an absence of provenance.
    let with_origin: i64 =
        sqlx::query_scalar("select count(*) from features where origin <> 'unknown'")
            .fetch_one(pool)
            .await?;
    Ok(Measured {
        projects,
        with_health,
        with_origin,
    })
}

/// Complaint created → first `validate_complaint` audit row.
///
/// The audit row is written by the HTTP layer, not by the store: `ValidateComplaint` only moves the status, so
/// a reader looking for the transition in the store will not find it.
///
/// # Errors
/// `sqlx::Error`.
pub async fn triage_latency(pool: &PgPool) -> Result<Latency, sqlx::Error> {
    let rows = sqlx::query(
        "select extract(epoch from c.created_at)::double precision, \
                (select min(extract(epoch from a.created_at))::double precision from audit_log a \
                  where a.action = 'validate_complaint' and a.entity = 'complaint' \
                    and a.entity_id = c.id) \
           from complaints c",
    )
    .fetch_all(pool)
    .await?;
    let mut episodes = Vec::with_capacity(rows.len());
    for row in rows {
        let created: f64 = row.try_get(0)?;
        let validated: Option<f64> = row.try_get(1)?;
        episodes.push(Episode {
            started_at: created,
            has_start: true,
            ended_at: validated.unwrap_or_default(),
            has_end: validated.is_some(),
        });
    }
    Ok(measure(&episodes))
}

/// Feature entering `ready` → entering `shipped`, from `set_feature_status` audit rows.
///
/// **The transition is in the detail string**: `trust.go` writes `"<from> -> <to>: <reason>"`, so the target
/// status is text after `" -> "` and before `":"`. Parsing it in SQL with a `LIKE` would also match a reason
/// that happens to contain `-> shipped`.
///
/// **Current episode, not first**: the start is the LAST transition into `ready`, and the end the FIRST
/// transition into `shipped` after it. A feature that shipped, was reopened and shipped again measures the
/// second episode.
///
/// # Errors
/// `sqlx::Error`.
pub async fn delivery_latency(pool: &PgPool) -> Result<Latency, sqlx::Error> {
    let rows = sqlx::query(
        "select entity_id, detail, extract(epoch from created_at)::double precision \
           from audit_log where action = 'set_feature_status' and entity = 'feature' \
          order by entity_id, created_at",
    )
    .fetch_all(pool)
    .await?;
    let mut by_feature: std::collections::BTreeMap<i64, Vec<(f64, String)>> = Default::default();
    for row in rows {
        let feature_id: i64 = row.try_get(0)?;
        let detail: Option<String> = row.try_get(1)?;
        let at: f64 = row.try_get(2)?;
        let Some(to) = parse_transition_target(detail.as_deref().unwrap_or("")) else {
            continue;
        };
        by_feature.entry(feature_id).or_default().push((at, to));
    }
    let episodes: Vec<Episode> = by_feature.values().map(|ts| episode_for_delivery(ts)).collect();
    Ok(measure(&episodes))
}

/// `<from> -> <to>: <reason>` → the target status. `None` when the detail does not have that shape, which the
/// caller treats as "not a status transition" rather than as an error.
#[must_use]
pub fn parse_transition_target(detail: &str) -> Option<String> {
    let (_, rest) = detail.split_once(" -> ")?;
    let to = match rest.find(':') {
        Some(index) => &rest[..index],
        None => rest,
    };
    Some(to.trim().to_string())
}

/// Apply the current-episode rule to one feature's ordered transitions.
fn episode_for_delivery(transitions: &[(f64, String)]) -> Episode {
    let last_ready = transitions.iter().rposition(|(_, to)| to == "ready");
    let shipped_at_all = transitions.iter().any(|(_, to)| to == "shipped");
    let Some(last_ready) = last_ready else {
        // Never entered `ready`. If it nevertheless shipped, the row is finished with no recorded start:
        // unmeasured, not unfinished, because reporting a shipped feature as in progress would be false.
        return Episode {
            started_at: 0.0,
            ended_at: 0.0,
            has_start: false,
            has_end: shipped_at_all,
        };
    };
    for (at, to) in &transitions[last_ready + 1..] {
        if to == "shipped" {
            return Episode {
                started_at: transitions[last_ready].0,
                ended_at: *at,
                has_start: true,
                has_end: true,
            };
        }
    }
    Episode {
        started_at: transitions[last_ready].0,
        ended_at: 0.0,
        has_start: true,
        has_end: false,
    }
}

/// Consensus call opened → closed. A call that is not `closed` is unfinished, not a zero-hour decision.
///
/// # Errors
/// `sqlx::Error`.
pub async fn decision_latency(pool: &PgPool) -> Result<Latency, sqlx::Error> {
    let rows = sqlx::query(
        "select extract(epoch from opens_at)::double precision, \
                extract(epoch from closes_at)::double precision, status \
           from consensus_calls",
    )
    .fetch_all(pool)
    .await?;
    let mut episodes = Vec::with_capacity(rows.len());
    for row in rows {
        let opens: f64 = row.try_get(0)?;
        let closes: Option<f64> = row.try_get(1)?;
        let status: String = row.try_get(2)?;
        let closed = status == "closed";
        episodes.push(Episode {
            started_at: opens,
            ended_at: closes.unwrap_or_default(),
            has_start: true,
            has_end: closed,
        });
    }
    Ok(measure(&episodes))
}

/// `GET /api/v1/stats` — the whole payload, computed once.
///
/// It returns an error rather than a partial payload: a statistics document with a missing section is a
/// document that looks complete.
///
/// # Errors
/// `sqlx::Error`.
pub async fn payload(pool: &PgPool) -> Result<StatsPayload, sqlx::Error> {
    Ok(StatsPayload {
        census: census(pool).await?,
        latency: LatencySection {
            triage: triage_latency(pool).await?,
            delivery: delivery_latency(pool).await?,
            decision: decision_latency(pool).await?,
        },
        measured: measured(pool).await?,
        generated_at: now_unix(),
    })
}

/// Unix seconds, from the system clock.
fn now_unix() -> f64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|elapsed| elapsed.as_secs_f64())
        .unwrap_or(0.0)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn measured(start: f64, end: f64) -> Episode {
        Episode {
            started_at: start,
            ended_at: end,
            has_start: true,
            has_end: true,
        }
    }

    /// An even sample takes the LOWER-middle value. `{10, 20}` is 10, not 15: the published number must be
    /// one the instance produced.
    #[test]
    fn an_even_sample_uses_the_lower_middle() {
        assert_eq!(median_lower_middle(&mut [20.0, 10.0]), 10.0);
        assert_eq!(median_lower_middle(&mut [1.0, 2.0, 3.0, 4.0]), 2.0);
        assert_eq!(median_lower_middle(&mut [5.0]), 5.0);
        assert_eq!(median_lower_middle(&mut []), 0.0);
    }

    #[test]
    fn absent_is_not_fast() {
        let lat = measure(&[
            Episode {
                started_at: 0.0,
                ended_at: 3600.0,
                has_start: false,
                has_end: true,
            },
            Episode {
                started_at: 0.0,
                ended_at: 0.0,
                has_start: false,
                has_end: false,
            },
        ]);
        assert_eq!(lat.n, 0);
        assert_eq!(lat.median_hours, 0.0);
        assert_eq!(lat.unmeasured, 1);
        assert_eq!(lat.unfinished, 0);
    }

    #[test]
    fn a_negative_duration_is_clamped_and_still_counted() {
        let lat = measure(&[measured(7200.0, 3600.0), measured(0.0, 3600.0)]);
        assert_eq!(lat.n, 2);
        assert_eq!(lat.median_hours, 0.0);
    }

    /// The current-episode rule: the LAST `ready` is the start, and only a `shipped` after it ends the episode.
    #[test]
    fn delivery_measures_the_current_episode() {
        let transitions = vec![
            (100.0, "ready".to_string()),
            (200.0, "shipped".to_string()),
            (300.0, "ready".to_string()),
        ];
        let episode = episode_for_delivery(&transitions);
        assert!(episode.has_start && !episode.has_end);
        assert_eq!(episode.started_at, 300.0);

        let transitions = vec![
            (100.0, "ready".to_string()),
            (200.0, "shipped".to_string()),
            (300.0, "ready".to_string()),
            (400.0, "shipped".to_string()),
        ];
        let episode = episode_for_delivery(&transitions);
        assert_eq!((episode.started_at, episode.ended_at), (300.0, 400.0));
        assert_eq!(episode.ended_at - episode.started_at, 100.0);
    }

    /// A feature that shipped without ever being `ready` is UNMEASURED, not unfinished.
    #[test]
    fn a_feature_that_never_was_ready_is_unmeasured() {
        let transitions = vec![(100.0, "shipped".to_string())];
        let episode = episode_for_delivery(&transitions);
        assert!(!episode.has_start && episode.has_end);
    }

    #[test]
    fn the_transition_detail_is_parsed_not_matched() {
        assert_eq!(
            parse_transition_target("ready -> shipped: done"),
            Some("shipped".to_string())
        );
        // A reason that contains "-> shipped" must not be read as the transition itself.
        assert_eq!(
            parse_transition_target("draft -> ready: waiting on ready -> shipped notes"),
            Some("ready".to_string())
        );
        assert_eq!(parse_transition_target("no arrow here"), None);
    }
}

/// One recorded day of instance statistics.
#[derive(Debug, Clone, Serialize)]
pub struct Snapshot {
    /// The day, as `YYYY-MM-DD`.
    pub day: String,
    /// Projects that existed.
    pub projects: i64,
    /// Projects with at least one measured signal.
    pub projects_measured: i64,
    /// Features in total.
    pub features_total: i64,
    /// Features that came from a seed or an import.
    pub features_seed: i64,
    /// Complaints in total.
    pub complaints_total: i64,
    /// Complaints that came from a seed or an import.
    pub complaints_seed: i64,
    /// The triage median, or `null` when the day had no sample. A median of zero is not the same answer.
    pub triage_median_h: Option<f64>,
    /// How many episodes the triage median rests on.
    pub triage_n: i64,
    /// The delivery median, or `null`.
    pub delivery_median_h: Option<f64>,
    /// How many episodes the delivery median rests on.
    pub delivery_n: i64,
    /// The decision median, or `null`.
    pub decision_median_h: Option<f64>,
    /// How many episodes the decision median rests on.
    pub decision_n: i64,
    /// Features with no provenance at all.
    pub no_origin: i64,
    /// When the row was written.
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub computed_at: f64,
}

/// Recorded days, newest first.
///
/// `days` is clamped to `[1, 365]`: a request for the whole table is a scrape, and a series longer than a year
/// is a chart nobody reads. The cap is a constant rather than a setting because the alternative — an unbounded
/// series route — is how a database ends up serialized into somebody's RSS reader.
///
/// # Errors
/// `sqlx::Error`.
pub async fn history(pool: &PgPool, days: i64) -> Result<Vec<Snapshot>, sqlx::Error> {
    let days = if days <= 0 {
        30
    } else if days > 365 {
        365
    } else {
        days
    };
    let rows = sqlx::query(
        // `day` is TEXT in this baseline (SQLite stored a string, and the generator kept the type): a
        // `to_char(day, …)` is `function to_char(text, unknown) does not exist`, which is how this was found.
        "select day, projects, projects_measured, features_total, \
                features_seed, complaints_total, complaints_seed, triage_median_h, triage_n, \
                delivery_median_h, delivery_n, decision_median_h, decision_n, no_origin, \
                extract(epoch from computed_at)::double precision \
           from instance_stats order by day desc limit $1",
    )
    .bind(days)
    .fetch_all(pool)
    .await?;
    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        out.push(Snapshot {
            day: row.try_get(0)?,
            projects: row.try_get(1)?,
            projects_measured: row.try_get(2)?,
            features_total: row.try_get(3)?,
            features_seed: row.try_get(4)?,
            complaints_total: row.try_get(5)?,
            complaints_seed: row.try_get(6)?,
            triage_median_h: row.try_get(7)?,
            triage_n: row.try_get(8)?,
            delivery_median_h: row.try_get(9)?,
            delivery_n: row.try_get(10)?,
            decision_median_h: row.try_get(11)?,
            decision_n: row.try_get(12)?,
            no_origin: row.try_get(13)?,
            computed_at: row.try_get(14)?,
        });
    }
    Ok(out)
}

/// One row of the newcomer-responsiveness ranking.
#[derive(Debug, Clone, Serialize)]
pub struct ResponsivenessRow {
    /// The project.
    pub project_id: i64,
    /// Its slug.
    pub slug: String,
    /// Its name.
    pub name: String,
    /// Median hours from a newcomer's first PR to the first response by somebody else.
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub hours: f64,
    /// How many first PRs the median rests on. Part of the row, because two projects at the same median are
    /// not the same claim when one rests on 5 PRs and the other on 500.
    pub n: i64,
}

/// The sample floor below which no number is published.
///
/// Five is not a statistical threshold, it is an editorial one: a median over three PRs is a number nobody
/// should act on, and publishing it invites a reader to rank projects on it.
pub const NEWCOMER_MIN_SAMPLE: i64 = 5;

/// Projects ranked by how fast they answer a newcomer's first PR.
///
/// Three rules, each of which is a defect if dropped:
///
/// * **a project under the floor is ABSENT, never last** — "we have not measured this" and "this is the worst"
///   are different facts, and a leaderboard that renders `0.0` for an unmeasured project ranks it BEST, which
///   is the one direction a reader will act on;
/// * **the sample rides along** (see [`ResponsivenessRow::n`]);
/// * **private projects are not ranked.** Every other statistics route publishes an aggregate, which a private
///   project contributes to invisibly; this one publishes a ranking of NAMED projects, so an unlisted
///   project's position would be a disclosure its owner did not make.
///
/// The tiebreak on slug is not cosmetic: without it two projects at the same median swap places between two
/// requests, and a ranking that is not stable cannot be compared with its own previous rendering.
///
/// # Errors
/// `sqlx::Error`.
pub async fn responsiveness_leaderboard(pool: &PgPool) -> Result<Vec<ResponsivenessRow>, sqlx::Error> {
    let rows = sqlx::query(
        "select p.id, p.slug, p.name, pm.newcomer_response_hours, pm.newcomer_sample \
           from project_metrics pm \
           join projects p on p.id = pm.project_id \
          where pm.newcomer_response_hours is not null \
            and pm.newcomer_sample >= $1 \
            and p.visibility = 'public' \
          order by pm.newcomer_response_hours asc, p.slug asc",
    )
    .bind(NEWCOMER_MIN_SAMPLE)
    .fetch_all(pool)
    .await?;
    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        out.push(ResponsivenessRow {
            project_id: row.try_get(0)?,
            slug: row.try_get(1)?,
            name: row.try_get(2)?,
            hours: row.try_get(3)?,
            n: row.try_get(4)?,
        });
    }
    Ok(out)
}
