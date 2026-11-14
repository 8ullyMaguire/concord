//! §6.4's arena: what a solution is scored against, and §6.5's three conditions for calling one.
//!
//! This module exists because row 9 could not answer three routes and said so rather than filling a
//! `SolutionScore` with zeroes: `GET /{feature}/solution-call`, `POST /{feature}/solution-call` and
//! `GET /{call}/fallback` all read the arena, and the arena is where the numbers come from.
//!
//! The rules that keep this honest, each a way a ranking can look right and be wrong:
//!
//! * **order on `r - 2*RD`, not on `r`.** A brand-new entry has RD 350 and cannot outrank a well-attested
//!   one on the strength of not having lost yet.
//! * **an arena that holds only a baseline has no leader.** §6.3's baseline is the "do nothing"
//!   competitor; calling a decision on it would be asking the room to vote for inaction.
//! * **a leader with no baseline is compared with itself**, giving 0.5, which never reaches the confidence
//!   bar. That is the safe direction: §6.3 says every arena has a baseline, so an arena without one is
//!   malformed, and refusing to manufacture a decision beats assuming the baseline is weak.
//! * **coverage is undefined, not zero and not one, when the feature has no pain.** Zero would flatten
//!   every score; one would credit a solution for covering nothing.
//! * **the baseline sorts last** whatever its score, because a list whose first row is "do nothing" reads
//!   as a recommendation to do nothing.

use serde::Serialize;
use sqlx::{PgPool, Row};

use concord_ranking::conservative;

/// §6.3's default coverage weight, used when the charter has never been edited.
pub const DEFAULT_SOLUTION_KAPPA: f64 = 200.0;

/// §8.2's activity window: a contribution older than this is not standing.
pub const ACTIVITY_WINDOW_DAYS: f64 = 365.0;

/// One row of `arena_entries`, in the arena's own vocabulary.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Entry {
    /// What the entry ranks: a solution.
    pub entity_id: i64,
    /// Glicko-2 rating.
    pub r: f64,
    /// Rating deviation.
    pub rd: f64,
    /// Volatility.
    pub sigma: f64,
    /// Games played.
    pub games: i64,
    /// §6.3's "do nothing" competitor.
    pub is_baseline: bool,
    /// Last time the rating moved, which is what §6.5's stability condition reads.
    pub updated_at: f64,
}

impl Entry {
    /// §5.1's displayed score.
    #[must_use]
    pub fn conservative(&self) -> f64 {
        conservative(self.r, self.rd)
    }
}

/// §6.4's per-complaint claim, so a client can offer a challenge without a second read.
#[derive(Debug, Clone, Serialize)]
pub struct CoverageClaim {
    /// The complaint the claim is about.
    pub complaint_id: i64,
    /// `resolves` or `leaves-unresolved` — the second is not a lesser claim.
    pub claim: String,
    /// A contested claim is still what the solution said; it just stops counting.
    pub contested: bool,
    /// Why it was contested.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub contest_reason: String,
}

/// A solution's standing in §6.4, exactly as the Go struct serializes.
#[derive(Debug, Clone, Serialize)]
pub struct SolutionScore {
    /// The solution.
    pub solution_id: i64,
    /// Its title, so a ranking is actionable.
    pub title: String,
    /// Conservative Glicko score, `r - 2*RD`.
    pub rating: f64,
    /// 0..1, the share of the feature's pain the solution claims to resolve.
    pub coverage: f64,
    /// The charter's weight for coverage.
    pub kappa: f64,
    /// The claims the score is computed from.
    pub claims: Vec<CoverageClaim>,
    /// `rating + kappa * coverage`.
    pub score: f64,
    /// §6.3's do-nothing competitor.
    pub is_baseline: bool,
    /// How many different people judged it.
    pub distinct_voters: i64,
    /// How long it has held its position. Left at 0 on the list path, as the Go store does: it is
    /// computed by [`stable_hours`] for the readiness check, and computing it per row would be a second
    /// query per entry for a field the list does not use.
    pub stable_hours: f64,
}

/// One of §6.5's conditions, with the numbers that make a refusal legible.
#[derive(Debug, Clone, Serialize)]
pub struct CallCondition {
    /// The spec's own phrase.
    pub name: String,
    /// Whether it currently holds.
    pub met: bool,
    /// The value reached.
    pub actual: f64,
    /// The bar it had to reach.
    pub required: f64,
    /// One sentence naming both.
    pub detail: String,
}

/// §6.5's readiness report.
#[derive(Debug, Clone, Serialize)]
pub struct CallReadiness {
    /// The feature a call would be about.
    pub feature_id: i64,
    /// The solution that leads the arena, 0 when nothing leads.
    pub leader_id: i64,
    /// The next-ranked, which is the first fallback.
    pub runner_up_id: i64,
    /// The do-nothing competitor.
    pub baseline_id: i64,
    /// The conjunction of every condition.
    pub ready: bool,
    /// Every condition, in spec order, so a caller can show why not.
    pub conditions: Vec<CallCondition>,
    /// The leader's title, for a one-line agenda item.
    pub leader_title: String,
    /// Declared by the Go struct and never assigned there; kept so the shape matches.
    pub leader_score: f64,
    /// The effective threshold after scaling, which a charter amendment changes.
    pub min_voters: i64,
}

/// The feature's solution arena, if one exists.
///
/// No arena is not an error: a feature nobody has proposed a solution to has nothing to rank.
pub async fn find_solution_arena(
    pool: &PgPool,
    feature_id: i64,
) -> Result<Option<i64>, sqlx::Error> {
    sqlx::query_scalar("select id from arenas where type = 'solution' and feature_id = $1")
        .bind(feature_id)
        .fetch_optional(pool)
        .await
}

/// The arena's board, ordered as §5.1 orders it.
///
/// The clamp is the Go store's: a limit outside `1..=200` becomes 50, so a caller cannot ask for the whole
/// table by passing 0 or a negative.
pub async fn leaderboard(pool: &PgPool, arena_id: i64, limit: i64) -> Result<Vec<Entry>, sqlx::Error> {
    let limit = if limit <= 0 || limit > 200 { 50 } else { limit };
    let rows = sqlx::query(
        "select entity_id, r, rd, sigma, games, is_baseline, \
                extract(epoch from updated_at)::double precision \
         from arena_entries where arena_id = $1 \
         order by (r - 2 * rd) desc, r desc, entity_id asc limit $2",
    )
    .bind(arena_id)
    .bind(limit)
    .fetch_all(pool)
    .await?;
    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        out.push(Entry {
            entity_id: row.try_get(0)?,
            r: row.try_get(1)?,
            rd: row.try_get(2)?,
            sigma: row.try_get(3)?,
            games: row.try_get(4)?,
            is_baseline: row.try_get::<bool, _>(5)?,
            updated_at: row.try_get(6)?,
        });
    }
    Ok(out)
}

/// The charter's coverage weight, or §6.4's default.
pub async fn kappa(pool: &PgPool, project_id: i64) -> Result<f64, sqlx::Error> {
    let value: Option<Option<f64>> =
        sqlx::query_scalar("select solution_kappa from charters where project_id = $1")
            .bind(project_id)
            .fetch_optional(pool)
            .await?;
    Ok(value.flatten().unwrap_or(DEFAULT_SOLUTION_KAPPA))
}

/// How many different people judged a solution, skips excluded.
pub async fn distinct_voters(
    pool: &PgPool,
    arena_id: i64,
    solution_id: i64,
) -> Result<i64, sqlx::Error> {
    sqlx::query_scalar(
        "select count(distinct voter_id) from pairwise_votes \
         where arena_id = $1 and outcome != 'skip' \
           and ((entity_type = 'solution' and a = $2) or (entity_type = 'solution' and b = $2))",
    )
    .bind(arena_id)
    .bind(solution_id)
    .fetch_one(pool)
    .await
}

/// How long the leader has held the top, from the last time its rating moved.
///
/// `arena_entries.updated_at` is written by every game, so this is the last time the leader was tested
/// rather than the last time it was filed. A negative span is reported as 0: a clock that went backwards
/// is not evidence of stability.
pub async fn stable_hours(
    pool: &PgPool,
    arena_id: i64,
    solution_id: i64,
    now: f64,
) -> Result<f64, sqlx::Error> {
    let updated: Option<f64> = sqlx::query_scalar(
        "select extract(epoch from updated_at)::double precision from arena_entries \
         where arena_id = $1 and entity_type = 'solution' and entity_id = $2",
    )
    .bind(arena_id)
    .bind(solution_id)
    .fetch_optional(pool)
    .await?;
    let Some(updated) = updated else {
        return Ok(0.0);
    };
    let hours = (now - updated) / 3600.0;
    Ok(if hours < 0.0 { 0.0 } else { hours })
}

/// Every claim a solution makes, contested ones included.
pub async fn coverage_claims(pool: &PgPool, solution_id: i64) -> Result<Vec<CoverageClaim>, sqlx::Error> {
    let rows = sqlx::query(
        "select complaint_id, claim, contested, coalesce(contest_reason, '') \
         from solution_coverage where solution_id = $1 order by complaint_id",
    )
    .bind(solution_id)
    .fetch_all(pool)
    .await?;
    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        out.push(CoverageClaim {
            complaint_id: row.try_get(0)?,
            claim: row.try_get(1)?,
            contested: row.try_get::<bool, _>(2)?,
            contest_reason: row.try_get(3)?,
        });
    }
    Ok(out)
}

/// §6.4's coverage: the share of the feature's pain a solution claims to resolve.
///
/// The denominator is every complaint linked to the feature, contested or not — a contested claim stays in
/// it, because the complaint is still the pain the feature exists to address. Pain is read through the
/// project's one definition of it (the charter's half-life decay and recorded impacts), so coverage agrees
/// with every other pain-based ranking instead of quietly meaning something else.
pub async fn coverage(
    pool: &PgPool,
    feature_id: i64,
    solution_id: i64,
) -> Result<f64, sqlx::Error> {
    use sqlx::Row;
    let halflife: Option<Option<f64>> = sqlx::query_scalar(
        "select pain_halflife_days from charters where project_id = \
         (select project_id from features where id = $1)",
    )
    .bind(feature_id)
    .fetch_optional(pool)
    .await?;
    let halflife = halflife.flatten().unwrap_or(30.0);

    let ids: Vec<i64> =
        sqlx::query_scalar("select complaint_id from feature_complaints where feature_id = $1")
            .bind(feature_id)
            .fetch_all(pool)
            .await?;
    let mut pain = std::collections::HashMap::new();
    let mut total = 0.0;
    for id in ids {
        let value = crate::priorities::complaint_pain(pool, id, halflife).await?;
        total += value;
        pain.insert(id, value);
    }
    // Undefined rather than zero or one.
    if total == 0.0 {
        return Ok(0.0);
    }

    let claims = sqlx::query(
        "select complaint_id, claim from solution_coverage \
         where solution_id = $1 and contested = 0",
    )
    .bind(solution_id)
    .fetch_all(pool)
    .await?;
    let mut resolved = 0.0;
    for row in claims {
        let complaint_id: i64 = row.try_get(0)?;
        let claim: String = row.try_get(1)?;
        // A claim on a complaint the feature no longer links is not an error: unlinking is legitimate and
        // the claim is simply no longer part of any denominator.
        let Some(value) = pain.get(&complaint_id) else {
            continue;
        };
        if claim == "resolves" {
            resolved += value;
        }
    }
    // A solution cannot resolve more pain than the feature has; clamping rather than refusing, because
    // the honest reading is that the over-claim does not count.
    Ok((resolved / total).clamp(0.0, 1.0))
}

/// §6.4's ranking for a feature, baseline last.
pub async fn list_solutions(
    pool: &PgPool,
    feature_id: i64,
    limit: i64,
) -> Result<Vec<SolutionScore>, sqlx::Error> {
    use sqlx::Row;
    let Some(arena_id) = find_solution_arena(pool, feature_id).await? else {
        return Ok(Vec::new());
    };
    let project_id: Option<i64> = sqlx::query_scalar("select project_id from features where id = $1")
        .bind(feature_id)
        .fetch_optional(pool)
        .await?;
    let Some(project_id) = project_id else {
        return Ok(Vec::new());
    };
    let kappa = kappa(pool, project_id).await?;
    let entries = leaderboard(pool, arena_id, limit).await?;

    let mut out: Vec<SolutionScore> = Vec::with_capacity(entries.len());
    for entry in entries {
        // The arena can hold an entry whose solution was deleted, or a baseline row from before solutions
        // existed. Both are skipped rather than reported as a zero-score solution.
        let existing: Option<(String, f64)> = sqlx::query_as(
            "select title, coalesce(extract(epoch from updated_at)::double precision, 0) \
             from solutions where id = $1",
        )
        .bind(entry.entity_id)
        .fetch_optional(pool)
        .await?;
        let Some((title, _updated)) = existing else {
            continue;
        };
        let coverage = coverage(pool, feature_id, entry.entity_id).await?;
        let voters = distinct_voters(pool, arena_id, entry.entity_id).await?;
        let claims = coverage_claims(pool, entry.entity_id).await?;
        let rating = entry.conservative();
        out.push(SolutionScore {
            solution_id: entry.entity_id,
            title,
            rating,
            coverage,
            kappa,
            claims,
            score: rating + kappa * coverage,
            is_baseline: entry.is_baseline,
            distinct_voters: voters,
            stable_hours: 0.0,
        });
    }
    sort_baseline_last(&mut out);
    Ok(out)
}

/// Order by score, but never let the baseline win.
///
/// The baseline is the thing to beat, not a competitor: showing it first reads as a recommendation to do
/// nothing, which is the opposite of what §6.3 means by including it.
fn sort_baseline_last(scores: &mut [SolutionScore]) {
    for i in 1..scores.len() {
        let mut j = i;
        while j > 0 {
            let swap = {
                let (a, b) = (&scores[j - 1], &scores[j]);
                if a.is_baseline && !b.is_baseline {
                    true
                } else if !a.is_baseline && b.is_baseline {
                    // Already in the order we want. `clippy::if_same_then_else` caught this branch comparing
                    // `!b.is_baseline && a.is_baseline` -- the same condition as the one above, so it never
                    // fired and a pair (ordinary, baseline) fell through to the score comparison. A baseline
                    // with a LOW score then bubbled forward, which is exactly what this function exists to
                    // prevent: the baseline is the thing to beat, not a recommendation to do nothing.
                    false
                } else {
                    a.score < b.score
                }
            };
            if !swap {
                break;
            }
            scores.swap(j - 1, j);
            j -= 1;
        }
    }
}

/// §8.2's eligible collaborators: role AND recent contribution, both necessary.
///
/// Counting roles alone makes an inactive maintainer eligible; counting activity alone makes an active
/// guest eligible. §8.2 makes them a conjunction, and every threshold in the system reads this.
pub async fn eligible_collaborators(
    pool: &PgPool,
    project_id: i64,
    window_days: f64,
) -> Result<Vec<i64>, sqlx::Error> {
    let window_days = if window_days <= 0.0 { ACTIVITY_WINDOW_DAYS } else { window_days };
    let cutoff: f64 = now_unix() - window_days * 86400.0;
    // The five routes of §8.2 that this schema can represent. "Seated by charter consensus" has no table,
    // so it is genuinely unimplemented rather than counted as zero; agents have no flag either. Both gaps
    // were recorded in the Go source and are recorded here rather than inferred from a COUNT.
    sqlx::query_scalar(
        "select q.user_id from ( \
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
                 where ma.user_id != m.author_id \
                union all \
                select al.project_id, al.actor_id, al.created_at from admin_ledger al \
                 where al.actor_id is not null and al.project_id is not null \
                   and al.justification is not null and trim(al.justification) != '' \
            ) contributions group by project_id, user_id \
         ) q \
         where q.project_id = $1 \
           and extract(epoch from q.last_qualifying) >= $2 \
           and exists (select 1 from members m where m.project_id = q.project_id \
                        and m.user_id = q.user_id \
                        and m.role in ('contributor','reviewer','maintainer','owner')) \
         order by q.user_id",
    )
    .bind(project_id)
    .bind(cutoff)
    .fetch_all(pool)
    .await
}

/// §6.5's three conditions, reported individually so a refusal can say which one failed.
pub async fn readiness(pool: &PgPool, feature_id: i64) -> Result<CallReadiness, sqlx::Error> {
    let mut out = CallReadiness {
        feature_id,
        leader_id: 0,
        runner_up_id: 0,
        baseline_id: 0,
        ready: false,
        conditions: Vec::new(),
        leader_title: String::new(),
        leader_score: 0.0,
        min_voters: 0,
    };
    let Some((project_id, _feature_title)): Option<(i64, String)> =
        sqlx::query_as("select project_id, title from features where id = $1")
            .bind(feature_id)
            .fetch_optional(pool)
            .await?
    else {
        return Err(sqlx::Error::RowNotFound);
    };
    let Some(arena_id) = find_solution_arena(pool, feature_id).await? else {
        out.conditions.push(CallCondition {
            name: "a solution to call".into(),
            met: false,
            actual: 0.0,
            required: 1.0,
            detail: "no solution has been filed for this feature".into(),
        });
        return Ok(out);
    };

    let entries = leaderboard(pool, arena_id, 200).await?;
    let mut leader: Option<&Entry> = None;
    let mut runner_up: Option<&Entry> = None;
    let mut baseline: Option<&Entry> = None;
    for entry in &entries {
        if entry.is_baseline && baseline.is_none() {
            baseline = Some(entry);
        } else if leader.is_none() {
            leader = Some(entry);
        } else if runner_up.is_none() {
            runner_up = Some(entry);
        }
    }
    let Some(leader) = leader else {
        out.conditions.push(CallCondition {
            name: "a solution to call".into(),
            met: false,
            actual: 0.0,
            required: 1.0,
            detail: "the arena holds only a baseline, so there is nothing to call".into(),
        });
        if let Some(baseline) = baseline {
            out.baseline_id = baseline.entity_id;
        }
        return Ok(out);
    };
    out.leader_id = leader.entity_id;
    out.runner_up_id = runner_up.map_or(0, |entry| entry.entity_id);
    out.baseline_id = baseline.map_or(0, |entry| entry.entity_id);
    out.leader_title = sqlx::query_scalar("select title from solutions where id = $1")
        .bind(leader.entity_id)
        .fetch_optional(pool)
        .await?
        .unwrap_or_default();

    let charter = crate::charter::charter(pool, project_id).await?;

    // Condition 1: beats the runner-up AND the baseline. Both, not either: a solution that beats every
    // rival but loses to doing nothing has not won.
    let against = baseline.unwrap_or(leader);
    let mut worst = concord_ranking::win_probability(leader.r, leader.rd, against.r, against.rd);
    if let Some(runner_up) = runner_up {
        let probability =
            concord_ranking::win_probability(leader.r, leader.rd, runner_up.r, runner_up.rd);
        if probability < worst {
            worst = probability;
        }
    }
    let confidence = charter.solution_call_confidence;
    out.conditions.push(CallCondition {
        name: "beats runner-up and baseline".into(),
        met: worst >= confidence,
        actual: worst,
        required: confidence,
        detail: format!(
            "{:.0}% confidence against the strongest alternative, {:.0}% required",
            worst * 100.0,
            confidence * 100.0
        ),
    });

    // Condition 2: enough distinct voters, scaled to project size — min(charter, eligible), so a
    // two-person project is not permanently un-callable and a large one cannot decide on two votes.
    let eligible = eligible_collaborators(pool, project_id, 0.0).await?.len() as i64;
    let mut min_voters = charter.solution_call_min_voters.max(0);
    if eligible > 0 && min_voters > eligible {
        min_voters = eligible;
    }
    let voters = distinct_voters(pool, arena_id, leader.entity_id).await?;
    out.min_voters = min_voters;
    out.conditions.push(CallCondition {
        name: "enough distinct voters".into(),
        met: voters >= min_voters,
        actual: voters as f64,
        required: min_voters as f64,
        detail: format!("{voters} of {min_voters} distinct voters ({eligible} eligible in this project)"),
    });

    // Condition 3: stable for N hours.
    let stable = stable_hours(pool, arena_id, leader.entity_id, now_unix()).await?;
    let want = charter.solution_stable_hours;
    out.conditions.push(CallCondition {
        name: "stable position".into(),
        met: stable >= want,
        actual: stable,
        required: want,
        detail: format!("leading for {stable:.0} hours, {want:.0} required"),
    });

    out.ready = out.conditions.iter().all(|condition| condition.met);
    Ok(out)
}

/// §6.5's fallback chain: everything ranked except the solution the call is about.
///
/// Excluding it is the rule: the chain is what you move TO when the proposal on the table fails, and
/// including it makes "fall back" a no-op the store has to special-case later.
pub async fn fallback(pool: &PgPool, call_id: i64) -> Result<Vec<SolutionScore>, sqlx::Error> {
    let row: Option<(Option<i64>, Option<i64>)> =
        sqlx::query_as("select feature_id, solution_id from consensus_calls where id = $1")
            .bind(call_id)
            .fetch_optional(pool)
            .await?;
    let Some((feature_id, solution_id)) = row else {
        return Err(sqlx::Error::RowNotFound);
    };
    let Some(feature_id) = feature_id else {
        return Ok(Vec::new());
    };
    let scores = list_solutions(pool, feature_id, 50).await?;
    Ok(scores
        .into_iter()
        .filter(|score| Some(score.solution_id) != solution_id)
        .collect())
}

/// Wall clock, in the unix unit the store's timestamps use.
fn now_unix() -> f64 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0.0, |elapsed| elapsed.as_secs_f64())
}

#[cfg(test)]
mod baseline_order_tests {
    use super::*;

    fn score(id: i64, score: f64, is_baseline: bool) -> SolutionScore {
        SolutionScore {
            solution_id: id,
            title: format!("solution {id}"),
            rating: score,
            coverage: 0.0,
            kappa: 0.0,
            claims: Vec::new(),
            score,
            is_baseline,
            distinct_voters: 0,
            stable_hours: 0.0,
        }
    }

    /// The baseline goes last even when its score is the WORST.
    ///
    /// This is the assertion `clippy::if_same_then_else` earned: the second branch of `sort_baseline_last`
    /// compared the same condition as the first, so it never ran, the pair fell through to the score
    /// comparison, and a low-scoring baseline moved to the front. The live probe passed because its fixture
    /// gave the baseline the highest score -- the case where the bug is invisible.
    #[test]
    fn a_worst_scoring_baseline_still_sorts_last() {
        let mut scores = vec![score(1, 0.10, false), score(2, 0.90, true), score(3, 0.50, false)];
        sort_baseline_last(&mut scores);
        // Descending by score, with the baseline moved to the end regardless of where its score puts it.
        assert_eq!(scores.iter().map(|s| s.solution_id).collect::<Vec<_>>(), vec![3, 1, 2]);
    }

    #[test]
    fn a_best_scoring_baseline_also_sorts_last() {
        let mut scores = vec![score(1, 0.10, false), score(2, 0.99, true), score(3, 0.50, false)];
        sort_baseline_last(&mut scores);
        // THE CASE THE BUG WAS INVISIBLE IN, and the one that exposes it: a baseline with the HIGHEST score
        // used to fall through to the score comparison and swap forward to the front.
        assert_eq!(scores.iter().map(|s| s.solution_id).collect::<Vec<_>>(), vec![3, 1, 2]);
    }

    #[test]
    fn without_a_baseline_the_order_is_the_score_order() {
        let mut scores = vec![score(1, 0.80, false), score(2, 0.10, false), score(3, 0.40, false)];
        sort_baseline_last(&mut scores);
        assert_eq!(scores.iter().map(|s| s.solution_id).collect::<Vec<_>>(), vec![1, 3, 2]);
    }
}

/// One side of a comparison as the vote log reports it.
///
/// The ratings are ZERO here, and that is the Go's behaviour, not an oversight of the port: `ListArenaVotes` selects
/// only `v.a`/`v.b` (entry ids) and never joins the entries, so `R`, `RD`, `Games` and `IsBaseline` are whatever
/// `setDerived` makes of two zeros. An audit view that cannot show what a vote CHANGED is a poor audit view — §5.2 calls
/// this one "the appeals view" — and it is recorded as an open defect for row 13 rather than quietly answered better
/// than the service being replaced.
#[derive(Debug, Clone, serde::Serialize)]
pub struct VoteSide {
    /// `feature`, `project`, `solution` or `list_entry`.
    pub entity_type: String,
    /// The entity's row.
    pub entity_id: i64,
    /// Its display name — empty when the entity row is gone, which keeps the ranking and the log in agreement.
    pub label: String,
    /// Its slug, only for a project.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub slug: String,
    /// Its rating: zero, see the type's note.
    pub r: f64,
    /// Its deviation: zero.
    pub rd: f64,
    /// `r - 2*rd`: zero.
    pub conservative: f64,
    /// Games played: zero.
    pub games: i64,
    /// Whether it is the arena's baseline: false, and a solution that says otherwise overrides it.
    pub is_baseline: bool,
}

/// One row of the vote log.
#[derive(Debug, Clone, serde::Serialize)]
pub struct VoteRow {
    /// The vote.
    pub id: i64,
    /// Who cast it.
    pub voter_id: i64,
    /// `a`, `b`, `tie`, `neither` (never `skip`: a skip is "I don't understand this", not a verdict).
    pub outcome: String,
    /// How much it counted.
    pub weight: f64,
    /// Why, if the voter said.
    pub reason: String,
    /// When, unix seconds.
    pub created_at: f64,
    /// The `a` side.
    pub a: VoteSide,
    /// The `b` side.
    pub b: VoteSide,
}

/// The vote log of one arena, newest first, skips excluded.
///
/// Rules: skips are not votes; `limit <= 0 or > 500` becomes 100 (the handler asks for 50); the result is never null,
/// because a client should not have to tell "no votes" from "null".
///
/// # Errors
/// `sqlx::Error`.
pub async fn list_votes(pool: &PgPool, arena_id: i64, limit: i64) -> Result<Vec<VoteRow>, sqlx::Error> {
    let limit = if limit <= 0 || limit > 500 { 100 } else { limit };
    let rows = sqlx::query_as::<_, (i64, i64, String, i64, i64, String, f64, String, f64)>(
        "select v.id, v.voter_id, v.entity_type, v.a, v.b, v.outcome, v.weight,
                coalesce(v.reason, ''), extract(epoch from v.created_at)::double precision
           from pairwise_votes v
          where v.arena_id = $1 and v.outcome <> 'skip'
          order by v.id desc
          limit $2",
    )
    .bind(arena_id)
    .bind(limit)
    .fetch_all(pool)
    .await?;

    let mut labels: std::collections::HashMap<(String, i64), (String, String, bool)> =
        std::collections::HashMap::new();
    for (_, _, entity_type, a, b, _, _, _, _) in &rows {
        for entity_id in [*a, *b] {
            let key = (entity_type.clone(), entity_id);
            if labels.contains_key(&key) {
                continue;
            }
            let found = label_of(pool, entity_type, entity_id).await?;
            labels.insert(key, found);
        }
    }

    Ok(rows
        .into_iter()
        .map(|(id, voter_id, entity_type, a, b, outcome, weight, reason, created_at)| {
            let side = |entity_id: i64| {
                let (label, slug, baseline) = labels
                    .get(&(entity_type.clone(), entity_id))
                    .cloned()
                    .unwrap_or_default();
                VoteSide {
                    entity_type: entity_type.clone(),
                    entity_id,
                    label,
                    slug,
                    // Two zeros, as the Go computes them: see `VoteSide`.
                    conservative: 0.0,
                    r: 0.0,
                    rd: 0.0,
                    games: 0,
                    is_baseline: baseline,
                }
            };
            VoteRow {
                id,
                voter_id,
                outcome,
                weight,
                reason,
                created_at,
                a: side(a),
                b: side(b),
            }
        })
        .collect())
}

/// A competitor's display label, its slug when it has one, and whether it is a solution marked as the baseline.
///
/// A missing entity answers `("", "", false)` rather than an error: the ranking still contains it and dropping the row
/// would make the leaderboard disagree with its own vote log.
pub async fn label_of(
    pool: &PgPool,
    entity_type: &str,
    entity_id: i64,
) -> Result<(String, String, bool), sqlx::Error> {
    match entity_type {
        "feature" => {
            let title = sqlx::query_scalar::<_, String>("select title from features where id = $1")
                .bind(entity_id)
                .fetch_optional(pool)
                .await?;
            Ok((title.unwrap_or_default(), String::new(), false))
        }
        "project" => {
            let row = sqlx::query_as::<_, (String, String)>("select name, slug from projects where id = $1")
                .bind(entity_id)
                .fetch_optional(pool)
                .await?;
            Ok(row
                .map(|(name, slug)| (name, slug, false))
                .unwrap_or_default())
        }
        "solution" => {
            let row = sqlx::query_as::<_, (String, bool)>(
                "select title, coalesce(is_baseline, 0) <> 0 from solutions where id = $1",
            )
            .bind(entity_id)
            .fetch_optional(pool)
            .await?;
            Ok(row
                .map(|(title, baseline)| (title, String::new(), baseline))
                .unwrap_or_default())
        }
        "list_entry" => {
            let title = sqlx::query_scalar::<_, String>("select title from list_entries where id = $1")
                .bind(entity_id)
                .fetch_optional(pool)
                .await?;
            Ok((title.unwrap_or_default(), String::new(), false))
        }
        _ => Ok((String::new(), String::new(), false)),
    }
}

/// One arena, as its routes need it.
#[derive(Debug, Clone)]
pub struct Arena {
    /// The row.
    pub id: i64,
    /// Its project.
    pub project_id: i64,
    /// What it ranks: `feature`, `project`, `solution`, `list_entry`.
    pub entity_type: String,
    /// The question the voter is asked.
    pub question: String,
    /// What the arena is FOR — `call`, `triage`, and so on — which the pair response echoes back.
    pub use_case: String,
}

/// One arena by id.
///
/// # Errors
/// `sqlx::Error`.
pub async fn find(pool: &PgPool, arena_id: i64) -> Result<Option<Arena>, sqlx::Error> {
    let row = sqlx::query_as::<_, (i64, i64, String, String, String)>(
        "select id, project_id, type, coalesce(question, ''), coalesce(use_case, '') from arenas where id = $1",
    )
    .bind(arena_id)
    .fetch_optional(pool)
    .await?;
    Ok(row.map(|(id, project_id, entity_type, question, use_case)| Arena {
        id,
        project_id,
        entity_type,
        question,
        use_case,
    }))
}

/// Why no pair could be proposed. The two are different answers to the caller: one is a data problem the project can
/// fix, the other is a voter who is done.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NoPair {
    /// Fewer than two competitors: nothing to compare.
    TooFewEntries,
    /// This voter has judged (or skipped past) every pair.
    AllJudged,
}

/// The active-learning cost of asking about a pair. LOWER is the better pair to ask about.
///
/// Three terms, and the sign of the middle one was a bug the Go's own tests caught: §5.1 asks about the comparisons the
/// arena knows LEAST, so uncertainty is a DISCOUNT (subtracted), not a bonus. With `350 - mean(RD)` as a bonus, two
/// thoroughly-tested entries looked more informative than two untouched ones and the arena kept re-asking answered
/// questions while new entries sat at their prior.
#[must_use]
pub fn pair_selection_cost(a: &Entry, b: &Entry) -> f64 {
    let distance = (a.r - b.r).abs();
    let uncertainty = (a.rd + b.rd) / 2.0;
    let mut cost = distance - uncertainty * 0.5;
    // "Beats doing nothing" is the question every solution has to answer before it is compared with rivals (§6.3/§6.5).
    if a.is_baseline || b.is_baseline {
        cost -= 150.0;
    }
    cost
}

/// An order-independent identity for a pair, so `(a,b)` and `(b,a)` are the same judgement.
fn pair_key(a: i64, b: i64) -> (i64, i64) {
    if a <= b {
        (a, b)
    } else {
        (b, a)
    }
}

/// The pairs this voter has already settled in this arena.
///
/// A SKIP is not a judgement: the voter did not understand the pair, so re-asking later is correct and the slot stays
/// open. Read in one query rather than one per candidate pair.
///
/// # Errors
/// `sqlx::Error`.
pub async fn judged_pairs(
    pool: &PgPool,
    arena_id: i64,
    voter_id: i64,
) -> Result<std::collections::HashSet<(i64, i64)>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, i64, String)>(
        "select a, b, outcome from pairwise_votes where arena_id = $1 and voter_id = $2",
    )
    .bind(arena_id)
    .bind(voter_id)
    .fetch_all(pool)
    .await?;
    let mut out = std::collections::HashSet::new();
    for (a, b, outcome) in rows {
        if outcome == "skip" {
            continue;
        }
        out.insert(pair_key(a, b));
    }
    Ok(out)
}

/// Which comparison this voter should judge next.
///
/// The arena is read first so a bad id fails as "no such arena" rather than as "needs two entries", which would send a
/// caller looking for a data problem they do not have.
///
/// # Errors
/// `sqlx::Error`, or `NoPair` when there is nothing to ask.
pub async fn next_pair(
    pool: &PgPool,
    arena_id: i64,
    voter_id: i64,
) -> Result<Result<(Entry, Entry), NoPair>, sqlx::Error> {
    if find(pool, arena_id).await?.is_none() {
        return Ok(Err(NoPair::TooFewEntries));
    }
    let entries = leaderboard(pool, arena_id, 200).await?;
    if entries.len() < 2 {
        return Ok(Err(NoPair::TooFewEntries));
    }
    let judged = judged_pairs(pool, arena_id, voter_id).await?;
    let mut best = f64::MAX;
    let mut chosen: Option<(usize, usize)> = None;
    for i in 0..entries.len() {
        for j in (i + 1)..entries.len() {
            if judged.contains(&pair_key(entries[i].entity_id, entries[j].entity_id)) {
                continue;
            }
            let cost = pair_selection_cost(&entries[i], &entries[j]);
            if cost < best {
                best = cost;
                chosen = Some((i, j));
            }
        }
    }
    match chosen {
        Some((i, j)) => Ok(Ok((entries[i].clone(), entries[j].clone()))),
        None => Ok(Err(NoPair::AllJudged)),
    }
}

#[cfg(test)]
mod pair_tests {
    use super::*;

    fn entry(id: i64, r: f64, rd: f64, baseline: bool) -> Entry {
        Entry {
            entity_id: id,
            r,
            rd,
            sigma: 0.06,
            games: 0,
            is_baseline: baseline,
            updated_at: 0.0,
        }
    }

    #[test]
    fn an_untouched_pair_is_cheaper_to_ask_about_than_a_well_measured_one() {
        // The sign this exists to protect: uncertainty is a DISCOUNT. If it were a bonus, the pair of measured entries
        // would look more informative and the arena would re-ask what it already knows.
        let measured = pair_selection_cost(&entry(1, 1600.0, 80.0, false), &entry(2, 1620.0, 80.0, false));
        let untouched = pair_selection_cost(&entry(1, 1600.0, 350.0, false), &entry(2, 1620.0, 350.0, false));
        assert!(untouched < measured, "untouched {untouched} should be below measured {measured}");
    }

    #[test]
    fn a_pair_including_the_baseline_is_discounted_by_150() {
        let without = pair_selection_cost(&entry(1, 1500.0, 100.0, false), &entry(2, 1500.0, 100.0, false));
        let with = pair_selection_cost(&entry(1, 1500.0, 100.0, true), &entry(2, 1500.0, 100.0, false));
        assert!((without - with - 150.0).abs() < 1e-9);
    }

    #[test]
    fn a_pair_identity_does_not_depend_on_which_side_it_came_from() {
        assert_eq!(pair_key(3, 7), pair_key(7, 3));
    }
}

/// One competitor's rating after replaying the arena's vote log.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Replayed {
    /// `feature`, `project`, `solution`, `list_entry`.
    pub entity_type: String,
    /// The entity.
    pub entity_id: i64,
    /// Replayed rating.
    pub r: f64,
    /// Replayed deviation.
    pub rd: f64,
    /// Replayed volatility.
    pub sigma: f64,
    /// Replayed game count.
    pub games: i64,
}

/// One entry's stored rating beside the replay of the log it should have come from.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Verification {
    /// `feature`, `project`, …
    pub entity_type: String,
    /// The entity.
    pub entity_id: i64,
    /// Whether this entry is the arena's baseline.
    pub is_baseline: bool,
    /// What the arena stores.
    pub stored_r: f64,
    /// What the arena stores.
    pub stored_rd: f64,
    /// What the arena stores.
    pub stored_games: i64,
    /// What the log produces.
    pub replayed_r: f64,
    /// What the log produces.
    pub replayed_rd: f64,
    /// What the log produces.
    pub replayed_games: i64,
    /// Exact equality, as the Go compares it — see `verify`.
    pub agrees: bool,
}

/// Rebuild every rating in an arena from its vote log, WITHOUT writing.
///
/// Two properties are load-bearing, and both are what the incremental path does:
///
/// * votes are applied in `(created_at, id)` order — the update depends on the ratings the pair had when the vote was
///   cast, so the order IS the answer. Grouping by (voter, day) batched two voters' same-day votes together, which is
///   not the order they arrived in;
/// * `neither` in an arena WITH a baseline is **two losses to the baseline**, not a draw between the competitors, and
///   the branch below mirrors the incremental one game for game, including the case where a side IS the baseline.
///
/// A vote about an entity that is not in the arena is not scored: it cannot happen through the vote route, so it is a
/// log written by something else.
///
/// # Errors
/// `sqlx::Error`.
pub async fn replay(pool: &PgPool, arena_id: i64) -> Result<Vec<Replayed>, sqlx::Error> {
    let entries = sqlx::query_as::<_, (String, i64, bool)>(
        "select entity_type, entity_id, coalesce(is_baseline, false) from arena_entries
          where arena_id = $1 order by entity_id",
    )
    .bind(arena_id)
    .fetch_all(pool)
    .await?;
    let votes = sqlx::query_as::<_, (i64, i64, String, f64)>(
        "select a, b, outcome, weight from pairwise_votes
          where arena_id = $1 and outcome <> 'skip'
          order by created_at asc, id asc",
    )
    .bind(arena_id)
    .fetch_all(pool)
    .await?;

    let mut ratings: std::collections::HashMap<i64, (f64, f64, f64)> = std::collections::HashMap::new();
    let mut games: std::collections::HashMap<i64, i64> = std::collections::HashMap::new();
    for (_, entity_id, _) in &entries {
        ratings.insert(
            *entity_id,
            (
                concord_ranking::DEFAULT_R,
                concord_ranking::DEFAULT_RD,
                concord_ranking::DEFAULT_VOL,
            ),
        );
        games.insert(*entity_id, 0);
    }
    let baseline = entries
        .iter()
        .find(|(_, _, is_baseline)| *is_baseline)
        .map(|(_, id, _)| *id);

    for (a, b, outcome, weight) in votes {
        if a == b {
            continue;
        }
        let (Some(&(ra, rda, sa)), Some(&(rb, rdb, sb))) = (ratings.get(&a), ratings.get(&b)) else {
            continue;
        };
        let score = outcome_score(&outcome);
        if outcome == "neither" {
            if let Some(baseline_id) = baseline {
                // Mirrors the incremental path game for game, including the case where a SIDE is the baseline. That case
                // is why this is written as two numbered steps rather than "the baseline beats both": when `a` is the
                // baseline the first step rates the baseline against `b` as a LOSS, and the second step rates it against
                // `b` again as a win. Odd, and it is the behaviour the two paths must share — the alternative is a
                // replay that disagrees with the live path in exactly the arenas that have a baseline.
                let counted = concord_ranking::games_for_weight(weight);
                // Step one: the `a` side against the baseline.
                if baseline_id == a {
                    let (ra_now, rb_now) = (
                        ratings.get(&a).copied().unwrap_or((ra, rda, sa)),
                        ratings.get(&b).copied().unwrap_or((rb, rdb, sb)),
                    );
                    let (new_a, new_b) = rate_pair(ra_now, rb_now, 0.0, weight);
                    ratings.insert(a, new_a);
                    ratings.insert(b, new_b);
                    *games.entry(a).or_insert(0) += counted;
                    *games.entry(b).or_insert(0) += counted;
                } else if let Some(&baseline_now) = ratings.get(&baseline_id) {
                    let (new_baseline, new_a) = rate_pair(baseline_now, (ra, rda, sa), 1.0, weight);
                    ratings.insert(baseline_id, new_baseline);
                    ratings.insert(a, new_a);
                    *games.entry(baseline_id).or_insert(0) += counted;
                    *games.entry(a).or_insert(0) += counted;
                }
                // Step two: the `b` side against the baseline, with whatever step one left behind.
                if baseline_id == b {
                    let (ra_now, rb_now) = (
                        ratings.get(&a).copied().unwrap_or((ra, rda, sa)),
                        ratings.get(&b).copied().unwrap_or((rb, rdb, sb)),
                    );
                    let (new_a, new_b) = rate_pair(ra_now, rb_now, 1.0, weight);
                    ratings.insert(a, new_a);
                    ratings.insert(b, new_b);
                    *games.entry(a).or_insert(0) += counted;
                    *games.entry(b).or_insert(0) += counted;
                } else if let Some(&baseline_now) = ratings.get(&baseline_id) {
                    let (rb_now, _) = (
                        ratings.get(&b).copied().unwrap_or((rb, rdb, sb)),
                        (),
                    );
                    let (new_baseline, new_b) = rate_pair(baseline_now, rb_now, 1.0, weight);
                    ratings.insert(baseline_id, new_baseline);
                    ratings.insert(b, new_b);
                    *games.entry(baseline_id).or_insert(0) += counted;
                    *games.entry(b).or_insert(0) += counted;
                }
                continue;
            }
        }
        let (new_a, new_b) = rate_pair((ra, rda, sa), (rb, rdb, sb), score, weight);
        ratings.insert(a, new_a);
        ratings.insert(b, new_b);
        let counted = concord_ranking::games_for_weight(weight);
        *games.entry(a).or_insert(0) += counted;
        *games.entry(b).or_insert(0) += counted;
    }

    Ok(entries
        .into_iter()
        .map(|(entity_type, entity_id, _)| {
            let (r, rd, sigma) = ratings
                .get(&entity_id)
                .copied()
                .unwrap_or((concord_ranking::DEFAULT_R, concord_ranking::DEFAULT_RD, concord_ranking::DEFAULT_VOL));
            Replayed {
                entity_type,
                entity_id,
                r,
                rd,
                sigma,
                games: games.get(&entity_id).copied().unwrap_or(0),
            }
        })
        .collect())
}

/// The score the `a` side earned: a win is 1, a loss 0, a tie 0.5. `neither` is 0 here and only means something special
/// in an arena with a baseline (see `replay`).
fn outcome_score(outcome: &str) -> f64 {
    match outcome {
        "a" => 1.0,
        "b" => 0.0,
        "tie" | "neither" => 0.5,
        _ => 0.5,
    }
}

/// One weighted comparison, as both sides' new ratings.
fn rate_pair(
    a: (f64, f64, f64),
    b: (f64, f64, f64),
    a_score: f64,
    weight: f64,
) -> ((f64, f64, f64), (f64, f64, f64)) {
    let games_a = concord_ranking::repeated_game(b.0, b.1, a_score, weight);
    let games_b = concord_ranking::repeated_game(a.0, a.1, 1.0 - a_score, weight);
    (
        concord_ranking::update_arena_rating(a.0, a.1, a.2, &games_a, weight),
        concord_ranking::update_arena_rating(b.0, b.1, b.2, &games_b, weight),
    )
}

/// Replay the log and compare with what the arena stores. Writes nothing, which is what makes it readable by anyone.
///
/// `agrees` is **exact float equality**, as the Go compares it: a stored rating is either the one the log produces or it
/// is not. Comparisons are bit-exact because both sides run the same IEEE-754 operations in the same order, and the
/// golden tests are what keep that true — a tolerance here would hide precisely the drift this route exists to find.
///
/// # Errors
/// `sqlx::Error`.
pub async fn verify(pool: &PgPool, arena_id: i64) -> Result<Vec<Verification>, sqlx::Error> {
    let replayed = replay(pool, arena_id).await?;
    let stored = sqlx::query_as::<_, (String, i64, f64, f64, i64, bool)>(
        "select entity_type, entity_id, r, rd, games, coalesce(is_baseline, false) from arena_entries
          where arena_id = $1 order by entity_id",
    )
    .bind(arena_id)
    .fetch_all(pool)
    .await?;
    let by_id: std::collections::HashMap<i64, Replayed> =
        replayed.into_iter().map(|row| (row.entity_id, row)).collect();
    Ok(stored
        .into_iter()
        .map(|(entity_type, entity_id, stored_r, stored_rd, stored_games, is_baseline)| {
            let replay = by_id.get(&entity_id);
            Verification {
                entity_type,
                entity_id,
                is_baseline,
                stored_r,
                stored_rd,
                stored_games,
                replayed_r: replay.map(|row| row.r).unwrap_or_default(),
                replayed_rd: replay.map(|row| row.rd).unwrap_or_default(),
                replayed_games: replay.map(|row| row.games).unwrap_or_default(),
                agrees: replay.is_some_and(|row| {
                    row.r == stored_r && row.rd == stored_rd && row.games == stored_games
                }),
            }
        })
        .collect())
}

/// How many solutions exist for a feature — the denominator of the standings panel's "omitted" count.
///
/// `COUNT(*) FROM solutions WHERE feature_id = $1`, which is NOT the same set the standings render: the standings walk
/// the arena and skip entries whose solution row is gone, so a solution that was deleted but whose arena entry survives
/// is counted here and not shown there. That asymmetry is the Go's, and the panel's own comment explains why an explicit
/// count exists at all ("one more row" is not a witness of "one more solution").
///
/// # Errors
/// `sqlx::Error`.
pub async fn count_solutions(pool: &PgPool, feature_id: i64) -> Result<i64, sqlx::Error> {
    sqlx::query_scalar::<_, i64>("select count(*) from solutions where feature_id = $1")
        .bind(feature_id)
        .fetch_one(pool)
        .await
}

/// One solution, whole, with the arena entry joined in for display.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Solution {
    /// The row.
    pub id: i64,
    /// Its feature.
    pub feature_id: i64,
    /// Who proposed it.
    pub author_id: i64,
    /// Its title.
    pub title: String,
    /// Its body.
    pub body: String,
    /// `code`, `design`, `do-nothing`, …
    #[serde(rename = "type")]
    pub kind: String,
    /// A link to where it lives.
    pub external_ref: Option<String>,
    /// Who it comes from.
    pub affiliation: Option<String>,
    /// `exclusive` or `complementary` (§6.3).
    pub relationship: String,
    /// Set for a forked proposal — §6.3's "same, but with X".
    pub parent_solution_id: Option<i64>,
    /// `proposed`, `withdrawn`, …
    pub status: String,
    /// A comma-separated list of the judgement this design needs (§6.4).
    pub expertise_tags: String,
    /// The arena entry for this solution, zeros when it is not in an arena yet.
    pub rating: EntryRating,
    /// Created, unix seconds.
    pub created_at: f64,
    /// Updated, unix seconds.
    pub updated_at: f64,
}

/// A solution's arena entry as the single-solution read reports it.
#[derive(Debug, Clone, serde::Serialize)]
pub struct EntryRating {
    /// The arena, 0 when the solution is not in one.
    pub arena_id: i64,
    /// Always `solution`.
    pub entity_type: String,
    /// The solution.
    pub entity_id: i64,
    /// Rating.
    pub r: f64,
    /// Deviation.
    pub rd: f64,
    /// Volatility.
    pub sigma: f64,
    /// Games.
    pub games: i64,
    /// Whether it is the arena's do-nothing competitor.
    pub is_baseline: bool,
    /// `r - 2*rd`, derived for display.
    pub conservative: f64,
    /// When the rating last moved.
    pub updated_at: f64,
}

/// `do-nothing` is the type that makes a solution the baseline — by its TYPE, not by a separate flag: two ways of saying
/// the same thing is how the arena ended up consulting a column nothing populated and scoring every `neither` as a draw.
pub const SOLUTION_DO_NOTHING: &str = "do-nothing";

/// One solution by id, with its arena rating joined in.
///
/// The join is a LEFT JOIN on purpose: a solution that has not been added to an arena yet is still a solution, and an
/// inner join would answer 404 for a proposal that exists.
///
/// # Errors
/// `sqlx::Error`.
pub async fn get_solution(pool: &PgPool, solution_id: i64) -> Result<Option<Solution>, sqlx::Error> {
    use sqlx::Row;
    // Read through `Row` rather than a tuple: sqlx implements `FromRow` for tuples up to sixteen elements and this
    // projection is twenty-one, so the tuple form fails to compile rather than reading short.
    let row = sqlx::query(
        "select s.id, s.feature_id, s.author_id, s.title, s.body, s.type,
                s.external_ref, s.affiliation, s.relationship, s.parent_solution_id,
                s.status, s.expertise_tags,
                extract(epoch from s.created_at)::double precision as created_at,
                extract(epoch from s.updated_at)::double precision as updated_at,
                e.arena_id, coalesce(e.r, 0) as r, coalesce(e.rd, 0) as rd, coalesce(e.sigma, 0) as sigma,
                coalesce(e.games, 0) as games, coalesce(e.is_baseline, false) as is_baseline,
                coalesce(extract(epoch from e.updated_at)::double precision, 0) as rating_updated_at
           from solutions s
           left join arena_entries e on e.entity_type = 'solution' and e.entity_id = s.id
          where s.id = $1",
    )
    .bind(solution_id)
    .fetch_optional(pool)
    .await?;
    let Some(row) = row else {
        return Ok(None);
    };
    let id: i64 = row.try_get("id")?;
    let arena_id: Option<i64> = row.try_get("arena_id")?;
    let r: f64 = row.try_get("r")?;
    let rd: f64 = row.try_get("rd")?;
    Ok(Some(Solution {
        id,
        feature_id: row.try_get("feature_id")?,
        author_id: row.try_get("author_id")?,
        title: row.try_get("title")?,
        body: row.try_get("body")?,
        kind: row.try_get("type")?,
        external_ref: row.try_get("external_ref")?,
        affiliation: row.try_get("affiliation")?,
        relationship: row.try_get("relationship")?,
        parent_solution_id: row.try_get("parent_solution_id")?,
        status: row.try_get("status")?,
        expertise_tags: row.try_get("expertise_tags")?,
        rating: EntryRating {
            arena_id: arena_id.unwrap_or(0),
            entity_type: "solution".to_string(),
            entity_id: id,
            r,
            rd,
            sigma: row.try_get("sigma")?,
            games: row.try_get("games")?,
            is_baseline: row.try_get("is_baseline")?,
            conservative: r - 2.0 * rd,
            updated_at: row.try_get("rating_updated_at")?,
        },
        created_at: row.try_get("created_at")?,
        updated_at: row.try_get("updated_at")?,
    }))
}

/// One coverage claim a solution makes about a complaint.
#[derive(Debug, Clone, serde::Serialize)]
pub struct SolutionCoverage {
    /// The solution.
    pub solution_id: i64,
    /// The complaint it claims to resolve.
    pub complaint_id: i64,
    /// What it claims to do about it.
    pub claim: String,
    /// Whether the claim has been challenged (§6.4).
    pub contested: bool,
    /// Who challenged it.
    pub contested_by: Option<i64>,
    /// When.
    pub contested_at: Option<f64>,
    /// Why.
    pub contest_reason: Option<String>,
    /// When the claim was made.
    pub created_at: f64,
}

/// A solution's coverage claims, ordered by complaint.
///
/// # Errors
/// `sqlx::Error`.
pub async fn list_coverage(pool: &PgPool, solution_id: i64) -> Result<Vec<SolutionCoverage>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, i64, String, bool, Option<i64>, Option<f64>, Option<String>, f64)>(
        "select solution_id, complaint_id, claim, coalesce(contested, 0) <> 0, contested_by,
                extract(epoch from contested_at)::double precision, contest_reason,
                extract(epoch from created_at)::double precision
           from solution_coverage where solution_id = $1 order by complaint_id",
    )
    .bind(solution_id)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(
            |(solution_id, complaint_id, claim, contested, contested_by, contested_at, contest_reason, created_at)| {
                SolutionCoverage {
                    solution_id,
                    complaint_id,
                    claim,
                    contested,
                    contested_by,
                    contested_at,
                    contest_reason,
                    created_at,
                }
            },
        )
        .collect())
}


/// Every entry in an arena, ordered by rating. Unlike the leaderboard this is not windowed: a recompute reports what it wrote, and a
/// window would let an entry it repaired be reported as unchanged by omission.
///
/// # Errors
/// `sqlx::Error`.
pub async fn entries(pool: &PgPool, arena_id: i64) -> Result<Vec<Entry>, sqlx::Error> {
    let rows = sqlx::query(
        "select entity_id, r, rd, coalesce(sigma, 0.06) as sigma, games,
                coalesce(is_baseline, false) as is_baseline,
                coalesce(extract(epoch from updated_at)::double precision, 0) as updated_at
           from arena_entries where arena_id = $1 order by r desc, entity_id",
    )
    .bind(arena_id)
    .fetch_all(pool)
    .await?;
    let mut out = Vec::with_capacity(rows.len());
    for row in &rows {
        use sqlx::Row;
        out.push(Entry {
            entity_id: row.try_get("entity_id")?,
            r: row.try_get("r")?,
            rd: row.try_get("rd")?,
            sigma: row.try_get("sigma")?,
            games: row.try_get("games")?,
            is_baseline: row.try_get("is_baseline")?,
            updated_at: row.try_get("updated_at")?,
        });
    }
    Ok(out)
}
