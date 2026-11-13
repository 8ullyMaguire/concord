//! `/priorities` — spec §6.2's ranking, and the three inputs a reader has to be able to take apart.
//!
//! `priority = (r − 2·RD) + λ·ln(1 + painSum) + μ·(strategic_weight + theme_weight)`.
//!
//! The two things the Go store learned the hard way are carried across:
//!
//! * **the theme half is summed, not chosen.** §366 names only themes, and dropping the per-feature
//!   half would make the weight-proposal mechanism inert — `docs/QUESTIONS.md` Q2 records the choice.
//!   Both are reported separately as well, because a score a reader cannot take apart is the defect
//!   this repository keeps finding.
//! * **pain is computed per complaint, not joined into one query.** The Go version held a rows cursor
//!   across two more queries on a pool with one connection, deadlocked, and every feature ranked with
//!   pain 0 — a wrong number that looked like a working page. Here the ids are read first and the
//!   rows are closed before any per-complaint work, and the arithmetic lives in `concord_ranking`
//!   where it has tests.

use serde::Serialize;
use sqlx::PgPool;

use concord_ranking as ranking;

/// One feature's priority, with its inputs.
#[derive(Debug, Clone, Serialize)]
pub struct FeaturePriority {
    pub id: i64,
    pub title: String,
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub elo_r: f64,
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub elo_rd: f64,
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub pain_score: f64,
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub strategic_weight: f64,
    /// §6.2's tag-scoped half of the μ term, summed from the live themes this feature's tags match.
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub theme_weight: f64,
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub priority_score: f64,
}

/// The charter weights the score needs. Defaults are the baseline's, for a project whose charter has
/// not been written yet — a project without a charter is not an error, it is a project with defaults.
#[derive(Debug, Clone, Copy)]
pub struct Weights {
    /// The `λ` on the pain term.
    pub lam: f64,
    /// The `μ` on the weight term.
    pub mu: f64,
    /// How fast a complaint's pain decays.
    pub pain_halflife_days: f64,
}

impl Default for Weights {
    fn default() -> Self {
        Self {
            lam: 20.0,
            mu: 100.0,
            pain_halflife_days: 90.0,
        }
    }
}

/// A project's charter weights, falling back to the defaults when there is no charter row.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn weights(pool: &PgPool, project_id: i64) -> Result<Weights, sqlx::Error> {
    use sqlx::Row;
    let row = sqlx::query(
        "select lam, mu, pain_halflife_days from charters where project_id = $1",
    )
    .bind(project_id)
    .fetch_optional(pool)
    .await?;
    match row {
        Some(row) => Ok(Weights {
            lam: row.try_get(0)?,
            mu: row.try_get(1)?,
            pain_halflife_days: row.try_get(2)?,
        }),
        None => Ok(Weights::default()),
    }
}

/// §6.1's pain for one complaint.
///
/// `affected` is the reporter plus a simplified reputation weighting per impact: the Go store adds
/// `1 + log10(2)` per impact row. Kept identical rather than "improved", because a different
/// formula would make the two services' rankings disagree while both claimed to implement §6.1.
///
/// # Errors
/// [`sqlx::Error`] when a read fails.
pub async fn complaint_pain(
    pool: &PgPool,
    complaint_id: i64,
    halflife_days: f64,
) -> Result<f64, sqlx::Error> {
    use sqlx::Row;
    let row = sqlx::query(
        "select severity, frequency, strategic_multiplier, \
                extract(epoch from created_at)::double precision, \
                (select count(*) from complaint_impacts where complaint_id = $1) \
         from complaints where id = $1",
    )
    .bind(complaint_id)
    .fetch_optional(pool)
    .await?;
    let Some(row) = row else { return Ok(0.0) };
    let severity: i64 = row.try_get(0)?;
    let frequency: f64 = row.try_get(1)?;
    let strategic_multiplier: f64 = row.try_get(2)?;
    let created_at: f64 = row.try_get(3)?;
    let impacts: i64 = row.try_get(4)?;
    let affected = 1.0 + impacts as f64 * (1.0 + 2.0f64.log10());
    let now = now_unix();
    let age_days = (now - created_at) / 86_400.0;
    Ok(ranking::pain_score(
        severity,
        frequency,
        strategic_multiplier,
        affected,
        age_days,
        halflife_days,
    ))
}

/// §6.2's theme weight: the sum of the live themes whose tag the feature carries, compared
/// case-insensitively.
///
/// # Errors
/// [`sqlx::Error`] when a read fails.
pub async fn theme_weight(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
) -> Result<f64, sqlx::Error> {
    let total: Option<f64> = sqlx::query_scalar(
        "select sum(st.weight) from strategic_themes st \
         where st.project_id = $1 and st.expires_at > now() \
           and lower(st.tag) in ( \
               select lower(t.name) from feature_tags ft join tags t on t.id = ft.tag_id \
               where ft.feature_id = $2 \
           )",
    )
    .bind(project_id)
    .bind(feature_id)
    .fetch_one(pool)
    .await?;
    Ok(total.unwrap_or(0.0))
}

/// Every feature of a project scored and ordered, highest priority first.
///
/// The order is `priority_score desc`, as in the Go store, applied stably over the Go store's input
/// order. Two equal scores genuinely are equal, so the tiebreak below the score is `created_at desc,
/// id asc` by construction rather than a claim about what the live service does.
///
/// # Errors
/// [`sqlx::Error`] when a read fails.
pub async fn feature_priorities(
    pool: &PgPool,
    project_id: i64,
) -> Result<Vec<FeaturePriority>, sqlx::Error> {
    use sqlx::Row;
    let weights = weights(pool, project_id).await?;
    // Read in the Go store's own order (`ListFeatures`: newest first, id ascending inside a tie) and
    // sort stably by score. Go's `sort.Slice` is unstable, so inside a block of exactly equal scores
    // the two services may permute that block differently — measured: every inversion between the
    // live order and this one is a pair whose scores are equal to within 1e-9 relative, and the live
    // service cannot be asked to do better because the score itself moves between requests.
    let rows = sqlx::query(
        "select id, title, elo_r, elo_rd, strategic_weight from features where project_id = $1 \
         order by created_at desc, id asc",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;

    let mut out = Vec::with_capacity(rows.len());
    for row in &rows {
        let id: i64 = row.try_get(0)?;
        let title: String = row.try_get(1)?;
        let elo_r: f64 = row.try_get(2)?;
        let elo_rd: f64 = row.try_get(3)?;
        let strategic_weight: f64 = row.try_get(4)?;
        let complaint_ids: Vec<i64> = sqlx::query_scalar(
            "select complaint_id from feature_complaints where feature_id = $1 order by complaint_id",
        )
        .bind(id)
        .fetch_all(pool)
        .await?;
        let mut pain_sum = 0.0;
        for complaint in complaint_ids {
            pain_sum += complaint_pain(pool, complaint, weights.pain_halflife_days).await?;
        }
        let theme = theme_weight(pool, project_id, id).await?;
        let priority_score = ranking::priority_score(
            elo_r,
            elo_rd,
            pain_sum,
            strategic_weight + theme,
            weights.lam,
            weights.mu,
        );
        out.push(FeaturePriority {
            id,
            title,
            elo_r,
            elo_rd,
            pain_score: pain_sum,
            strategic_weight,
            theme_weight: theme,
            priority_score,
        });
    }
    out.sort_by(|a, b| {
        b.priority_score
            .partial_cmp(&a.priority_score)
            .unwrap_or(std::cmp::Ordering::Equal)
    });
    Ok(out)
}

fn now_unix() -> f64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|elapsed| elapsed.as_secs_f64())
        .unwrap_or(0.0)
}
