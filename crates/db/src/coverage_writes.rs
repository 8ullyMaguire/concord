//! §6.3's coverage claims and §6.4's challengeable contests.
//!
//! A coverage claim says "this solution resolves *that* complaint, and leaves *this other one* alone". Two rules make the
//! claim worth anything:
//!
//! * **the complaint must be linked to the solution's own feature.** Otherwise coverage is measured against a denominator
//!   the solution chose itself — file a claim against a trivial complaint you also filed and the percentage rises;
//! * **a contest needs a reason, and the claimant cannot be the contestor.** A contest with no reason is
//!   indistinguishable from a deletion, and the claim's author deserves to know what to answer; and the self-vote rule of
//!   §5.2 applies here for the same reason it applies to rankings.
//!
//! A contested claim **stops counting** until it is restated: restating the claim (`claim_coverage` again) clears the
//! contest, which is what makes the contest answerable rather than permanent.

use sqlx::PgPool;

/// §6.4's coverage score and its parts.
///
/// The raw pain sums are served alongside the ratio because "resolves 340 of 510 pain" is what a reader can act on, and a
/// percentage alone hides both how much is at stake and whether the denominator changed.
#[derive(Debug, Clone, serde::Serialize)]
pub struct CoverageScore {
    /// 0..=1.
    pub coverage: f64,
    /// Pain this solution claims to resolve, and is not currently contested on.
    pub resolved_pain: f64,
    /// Pain across every complaint the feature links, contested or not — the complaint is still the pain the feature
    /// exists to address.
    pub total_pain: f64,
    /// Pain this solution has any claim on (resolve or leave-unresolved), uncontested only.
    pub claimed_pain: f64,
    /// How many of this solution's claims are currently contested, so a reader can say so rather than quietly using a
    /// smaller denominator.
    pub contested: i64,
}

/// Why a claim or a contest was refused.
#[derive(Debug)]
pub enum Refused {
    /// The request does not describe a claim that can be counted.
    Invalid(String),
    /// The solution or the claim is not there.
    NotFound,
    /// §5.2: an author cannot contest their own coverage claim.
    SelfVote,
    /// The database said no.
    Database(sqlx::Error),
}

impl From<sqlx::Error> for Refused {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

/// Record (or restate) a coverage claim.
///
/// Restating **clears the contest**: that is the answer a contest invites, and leaving the claim contested after its author
/// has restated it would make every contest permanent.
///
/// # Errors
/// See [`Refused`].
pub async fn claim_coverage(
    pool: &PgPool,
    solution_id: i64,
    complaint_id: i64,
    claim: &str,
) -> Result<(), Refused> {
    let claim = claim.trim();
    if claim != "resolves" && claim != "leaves-unresolved" {
        return Err(Refused::Invalid(format!(
            "claim must be `resolves` or `leaves-unresolved`, got `{claim}`"
        )));
    }
    let feature: Option<i64> = sqlx::query_scalar("select feature_id from solutions where id = $1")
        .bind(solution_id)
        .fetch_optional(pool)
        .await?;
    let feature = feature.ok_or(Refused::NotFound)?;
    let linked: i64 = sqlx::query_scalar(
        "select count(*) from feature_complaints where feature_id = $1 and complaint_id = $2",
    )
    .bind(feature)
    .bind(complaint_id)
    .fetch_one(pool)
    .await?;
    if linked == 0 {
        return Err(Refused::Invalid(format!(
            "complaint {complaint_id} is not linked to the solution's feature"
        )));
    }
    sqlx::query(
        "insert into solution_coverage (solution_id, complaint_id, claim, created_at)
         values ($1,$2,$3,now())
         on conflict (solution_id, complaint_id) do update
            set claim = excluded.claim, contested = 0, contested_by = null,
                contested_at = null, contest_reason = null",
    )
    .bind(solution_id)
    .bind(complaint_id)
    .bind(claim)
    .execute(pool)
    .await?;
    Ok(())
}

/// Contest an existing claim.
///
/// # Errors
/// See [`Refused`]; a contest of a claim that does not exist is [`Refused::NotFound`], because silently doing nothing leaves
/// the contestor believing the claim is challenged.
pub async fn contest_coverage(
    pool: &PgPool,
    solution_id: i64,
    complaint_id: i64,
    contestor_id: i64,
    reason: &str,
) -> Result<(), Refused> {
    let reason = reason.trim();
    if reason.is_empty() {
        return Err(Refused::Invalid(
            "contesting a coverage claim needs a reason".to_string(),
        ));
    }
    let author: Option<i64> = sqlx::query_scalar("select author_id from solutions where id = $1")
        .bind(solution_id)
        .fetch_optional(pool)
        .await?;
    let author = author.ok_or(Refused::NotFound)?;
    if author == contestor_id {
        return Err(Refused::SelfVote);
    }
    let affected = sqlx::query(
        // `contested` is a COUNT column (0/1), not a boolean: the Go wrote 0 and 1 into it, the schema kept that, and it is
        // summed to report how many claims are contested today.
        "update solution_coverage set contested = 1, contested_by = $1, contested_at = now(),
                contest_reason = $2
          where solution_id = $3 and complaint_id = $4",
    )
    .bind(contestor_id)
    .bind(reason)
    .bind(solution_id)
    .bind(complaint_id)
    .execute(pool)
    .await?
    .rows_affected();
    if affected == 0 {
        return Err(Refused::NotFound);
    }
    Ok(())
}

/// §6.4's coverage term, with the parts the ratio is made of.
///
/// Pain is read through `priorities::complaint_pain`, the project's one definition of it, so a loud new complaint outranks
/// an identical old one here exactly as it does everywhere else. Summing raw `severity × frequency × multiplier` would make
/// coverage disagree with every other pain-based ranking in the product, and the two numbers would silently mean different
/// things.
///
/// # Errors
/// `sqlx::Error`.
pub async fn coverage_score(
    pool: &PgPool,
    feature_id: i64,
    solution_id: i64,
) -> Result<CoverageScore, sqlx::Error> {
    let halflife: Option<Option<f64>> = sqlx::query_scalar(
        "select pain_halflife_days from charters where project_id = \
         (select project_id from features where id = $1)",
    )
    .bind(feature_id)
    .fetch_optional(pool)
    .await?;
    let halflife = halflife.flatten().unwrap_or(30.0);

    let linked: Vec<i64> =
        sqlx::query_scalar("select complaint_id from feature_complaints where feature_id = $1")
            .bind(feature_id)
            .fetch_all(pool)
            .await?;
    let mut pain = std::collections::HashMap::new();
    let mut total_pain = 0.0;
    for id in &linked {
        let value = crate::priorities::complaint_pain(pool, *id, halflife).await?;
        total_pain += value;
        pain.insert(*id, value);
    }

    let mut score = CoverageScore {
        coverage: 0.0,
        resolved_pain: 0.0,
        total_pain,
        claimed_pain: 0.0,
        contested: 0,
    };
    // No linked complaints means coverage is UNDEFINED — not zero and not one. Zero would flatten every solution's score;
    // one would credit a solution for covering nothing.
    if total_pain == 0.0 {
        return Ok(score);
    }

    score.contested = sqlx::query_scalar(
        "select coalesce(sum(contested), 0)::bigint from solution_coverage where solution_id = $1",
    )
    .bind(solution_id)
    .fetch_one(pool)
    .await?;

    let claims: Vec<(i64, String)> = sqlx::query_as(
        "select complaint_id, claim from solution_coverage where solution_id = $1 and contested = 0",
    )
    .bind(solution_id)
    .fetch_all(pool)
    .await?;
    for (complaint_id, claim) in claims {
        // A claim on a complaint the feature no longer links is not an error: unlinking is legitimate, and the claim is
        // simply no longer part of any denominator.
        let Some(value) = pain.get(&complaint_id) else {
            continue;
        };
        score.claimed_pain += value;
        if claim == "resolves" {
            score.resolved_pain += value;
        }
    }
    // A solution cannot resolve more pain than the feature has. Clamping rather than refusing: the claim is the author's,
    // and the honest reading is that the over-claim does not count — not that the whole solution is bad.
    score.coverage = (score.resolved_pain / score.total_pain).clamp(0.0, 1.0);
    Ok(score)
}
