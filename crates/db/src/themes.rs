//! §6.2's third steering input: a tag's weight, changed only by consensus, with a public changelog and an
//! EXPIRY.
//!
//! Every rule below is a way this becomes a hand-steering lever wearing a vote:
//!
//! * **Proposing applies nothing.** A change that took effect on proposal is exactly the maintainer-only dial the
//!   consensus route exists to remove, so the proposal records the weight the tag carries NOW
//!   (`weight_before`) and writes no weight anywhere else.
//! * **The tag must already exist**, and the refusal names the taxonomy route rather than creating the tag as a
//!   side effect — inventing taxonomy through a steering dial is how the global tag list became a write-only
//!   surface the first time.
//! * **A rationale is required.** A weight change with no stated reason is the lever, undocumented.
//! * **A theme EXPIRES**, and a charter whose `theme_lifetime_days` is not positive is refused rather than
//!   written: a zero lifetime writes a row that is born expired — ratified, listed by nothing, worth 0.
//! * **A renewal is an UPSERT**, not a second row: ratifying a change to a tag already in force moves
//!   `expires_at` forward, or `ThemeWeightTotal` counts the same theme twice.

use sqlx::{PgPool, Row};

use concord_governance::{evaluate_consensus, ConsensusCounts};

use crate::consensus::eligible_collaborators;

/// The bounds a theme weight may take, from `internal/store/strategy.go`.
pub const THEME_WEIGHT_MIN: f64 = -2.0;
/// The upper bound.
pub const THEME_WEIGHT_MAX: f64 = 3.0;

/// One proposed theme change.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Proposal {
    /// The proposal.
    pub id: i64,
    /// The project.
    pub project_id: i64,
    /// The tag whose weight it changes.
    pub theme_tag: String,
    /// The tag's namespace.
    pub theme_namespace: String,
    /// Who proposed it.
    pub proposed_by: i64,
    /// The weight in force when it was proposed, `null` when the tag carries none.
    pub weight_before: Option<f64>,
    /// The weight it would carry.
    pub weight_after: f64,
    /// Why.
    pub rationale: String,
    /// `pending`, `ratified`, `withdrawn`, `stale`.
    pub status: String,
    /// When it was opened, unix seconds.
    pub created_at: f64,
    /// When it was decided, unix seconds, `null` while pending.
    pub decided_at: Option<f64>,
}

/// One theme in force.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Theme {
    /// The row.
    pub id: i64,
    /// The tag.
    pub tag: String,
    /// The namespace.
    pub namespace: String,
    /// The weight it carries until it expires.
    pub weight: f64,
    /// Why it was set.
    pub rationale: String,
    /// Who ratified it.
    pub set_by: i64,
    /// When it took effect, unix seconds.
    pub created_at: f64,
    /// When it lapses, unix seconds.
    pub expires_at: f64,
}

/// Why a proposal or consent was refused.
#[derive(Debug, Clone)]
pub enum Refused {
    /// No such proposal.
    NotFound,
    /// Already decided.
    Decided(String),
    /// The proposal is not valid on its face: bounds, a missing rationale, a tag that does not exist.
    Invalid(String),
    /// The proposer tried to consent to their own proposal.
    SelfConsent,
    /// Not in §8.2's eligible population.
    NotEligible,
    /// Another pending proposal already exists for this tag, or this consent is already recorded.
    Duplicate(String),
    /// The threshold is not met; the consent is recorded.
    NotYet {
        /// The verdict's word.
        verdict: String,
        /// Consents recorded.
        consents: i64,
        /// The eligible population.
        eligible: i64,
    },
    /// The charter's lifetime is not positive, so the theme would be born expired.
    NoLifetime(f64),
    /// Only the proposer or a maintainer may withdraw.
    NotYours,
    /// The store failed.
    Failed(String),
}

const COLUMNS: &str = "id, project_id, theme_tag, theme_namespace, proposed_by, weight_before, \
    weight_after, coalesce(rationale, ''), status, extract(epoch from created_at)::double precision, \
    extract(epoch from decided_at)::double precision";

fn from_row(row: &sqlx::postgres::PgRow) -> Result<Proposal, sqlx::Error> {
    Ok(Proposal {
        id: row.try_get(0)?,
        project_id: row.try_get(1)?,
        theme_tag: row.try_get(2)?,
        theme_namespace: row.try_get(3)?,
        proposed_by: row.try_get(4)?,
        weight_before: row.try_get(5)?,
        weight_after: row.try_get(6)?,
        rationale: row.try_get(7)?,
        status: row.try_get(8)?,
        created_at: row.try_get(9)?,
        decided_at: row.try_get(10)?,
    })
}

/// One proposal, or `None`.
///
/// # Errors
/// `sqlx::Error`.
pub async fn proposal(pool: &PgPool, id: i64) -> Result<Option<Proposal>, sqlx::Error> {
    let sql = format!("select {COLUMNS} from strategic_theme_proposals where id = $1");
    let row = sqlx::query(&sql).bind(id).fetch_optional(pool).await?;
    match row {
        Some(row) => Ok(Some(from_row(&row)?)),
        None => Ok(None),
    }
}

/// A project's proposals, newest first.
///
/// # Errors
/// `sqlx::Error`.
pub async fn proposals(pool: &PgPool, project_id: i64) -> Result<Vec<Proposal>, sqlx::Error> {
    let sql = format!(
        "select {COLUMNS} from strategic_theme_proposals where project_id = $1 \
         order by created_at desc, id desc"
    );
    let rows = sqlx::query(&sql).bind(project_id).fetch_all(pool).await?;
    rows.iter().map(from_row).collect()
}

/// The themes in force — unexpired, newest expiry first.
///
/// # Errors
/// `sqlx::Error`.
pub async fn themes(pool: &PgPool, project_id: i64) -> Result<Vec<Theme>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, String, String, f64, String, i64, f64, f64)>(
        "select id, tag, namespace, weight, coalesce(rationale, ''), set_by,
                extract(epoch from created_at)::double precision,
                extract(epoch from expires_at)::double precision
           from strategic_themes
          where project_id = $1 and expires_at > now()
          order by expires_at desc, id desc",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(id, tag, namespace, weight, rationale, set_by, created_at, expires_at)| Theme {
            id,
            tag,
            namespace,
            weight,
            rationale,
            set_by,
            created_at,
            expires_at,
        })
        .collect())
}

/// Open a theme proposal. Writes NOTHING except the proposal.
///
/// # Errors
/// `sqlx::Error`, or a [`Refused`] wrapped in it.
pub async fn propose(
    pool: &PgPool,
    project_id: i64,
    user_id: i64,
    tag: &str,
    namespace: &str,
    weight: f64,
    rationale: &str,
) -> Result<Proposal, Refused> {
    let inside = move |error: sqlx::Error| Refused::Failed(error.to_string());
    if !(THEME_WEIGHT_MIN..=THEME_WEIGHT_MAX).contains(&weight) {
        return Err(Refused::Invalid(format!(
            "theme weight must be between {THEME_WEIGHT_MIN} and {THEME_WEIGHT_MAX}"
        )));
    }
    let tag = tag.trim().to_lowercase();
    if tag.is_empty() {
        return Err(Refused::Invalid("a theme needs a tag".to_string()));
    }
    let namespace = {
        let trimmed = namespace.trim();
        if trimmed.is_empty() {
            "topic".to_string()
        } else {
            trimmed.to_string()
        }
    };
    let rationale = rationale.trim();
    if rationale.is_empty() {
        // A weight change with no stated reason is the steering lever this route exists to remove.
        return Err(Refused::Invalid("a rationale is required".to_string()));
    }
    // §4.3: an unknown tag is refused naming the proposal route, not created as a side effect.
    let known: Option<i64> = sqlx::query_scalar("select id from tags where name = $1")
        .bind(&tag)
        .fetch_optional(pool)
        .await
        .map_err(inside)?;
    if known.is_none() {
        return Err(Refused::Invalid(format!(
            "no such tag {tag:?} -- propose it with POST /api/v1/taxonomy/proposals (action=create)"
        )));
    }
    let before: Option<f64> = sqlx::query_scalar(
        "select weight from strategic_themes
          where project_id = $1 and tag = $2 and namespace = $3 and expires_at > now()",
    )
    .bind(project_id)
    .bind(&tag)
    .bind(&namespace)
    .fetch_optional(pool)
    .await
    .map_err(inside)?;
    let now = crate::now_unix();
    let id: i64 = sqlx::query_scalar(
        "insert into strategic_theme_proposals (project_id, theme_tag, theme_namespace, proposed_by,
            weight_before, weight_after, rationale, status, created_at)
         values ($1, $2, $3, $4, $5, $6, $7, 'pending', to_timestamp($8)) returning id",
    )
    .bind(project_id)
    .bind(&tag)
    .bind(&namespace)
    .bind(user_id)
    .bind(before)
    .bind(weight)
    .bind(rationale)
    .bind(now)
    .fetch_one(pool)
    .await
    .map_err(|error| {
        // The partial unique index is the enforcement point; translate it into the answer a caller can present.
        if error.to_string().contains("idx_theme_one_pending") {
            Refused::Duplicate("a proposal is already pending for this tag".to_string())
        } else {
            Refused::Failed(error.to_string())
        }
    })?;
    audit(
        pool,
        project_id,
        user_id,
        "propose_strategic_theme",
        id,
        &format!("proposal {id}: tag {tag:?} -> {weight} ({rationale})"),
    )
    .await
    .map_err(inside)?;
    proposal(pool, id).await.map_err(inside)?.ok_or(Refused::NotFound)
}

/// Record one consent, ratifying and applying the theme if the charter's threshold is met.
///
/// # Errors
/// `sqlx::Error`, or a [`Refused`] wrapped in it.
pub async fn consent(pool: &PgPool, proposal_id: i64, user_id: i64) -> Result<(Proposal, bool), Refused> {
    let inside = move |error: sqlx::Error| Refused::Failed(error.to_string());
    let Some(pending) = proposal(pool, proposal_id).await.map_err(inside)? else {
        return Err(Refused::NotFound);
    };
    if pending.status != "pending" {
        return Err(Refused::Decided(pending.status));
    }
    if pending.proposed_by == user_id {
        return Err(Refused::SelfConsent);
    }
    let charter = crate::charter::charter(pool, pending.project_id)
        .await
        .map_err(inside)?;
    let eligible_ids = eligible_collaborators(pool, pending.project_id, 0.0)
        .await
        .map_err(inside)?;
    if !eligible_ids.contains(&user_id) {
        return Err(Refused::NotEligible);
    }
    let eligible = eligible_ids.len() as i64;
    // A repeated click is one voice: the insert ignores a duplicate, and the tally reads the row count.
    sqlx::query(
        "insert into strategic_theme_consents (proposal_id, user_id, consented_at)
         values ($1, $2, to_timestamp($3)) on conflict (proposal_id, user_id) do nothing",
    )
    .bind(proposal_id)
    .bind(user_id)
    .bind(crate::now_unix())
    .execute(pool)
    .await
    .map_err(inside)?;
    let consents: i64 = sqlx::query_scalar(
        "select count(*) from strategic_theme_consents where proposal_id = $1",
    )
    .bind(proposal_id)
    .fetch_one(pool)
    .await
    .map_err(inside)?;

    let counts = ConsensusCounts {
        consent: consents,
        participants: consents,
        eligible,
        ..ConsensusCounts::default()
    };
    let verdict = evaluate_consensus(&counts, &charter);
    if verdict != concord_governance::Result::Accepted {
        audit(
            pool,
            pending.project_id,
            user_id,
            "strategic_theme_consent",
            proposal_id,
            &format!("proposal {proposal_id}: {consents}/{eligible} consents ({})", verdict.as_str()),
        )
        .await
        .map_err(inside)?;
        return Err(Refused::NotYet {
            verdict: verdict.as_str().to_string(),
            consents,
            eligible,
        });
    }

    // A zero lifetime writes a row that is born expired: ratified, listed by nothing, worth 0.
    if charter.theme_lifetime_days <= 0.0 {
        return Err(Refused::NoLifetime(charter.theme_lifetime_days));
    }
    apply(pool, &pending, user_id, charter.theme_lifetime_days)
        .await
        .map_err(inside)?;
    sqlx::query(
        "update strategic_theme_proposals set status = 'ratified', decided_at = to_timestamp($1)
          where id = $2 and status = 'pending'",
    )
    .bind(crate::now_unix())
    .bind(proposal_id)
    .execute(pool)
    .await
    .map_err(inside)?;
    audit(
        pool,
        pending.project_id,
        user_id,
        "ratify_strategic_theme",
        proposal_id,
        &format!(
            "proposal {proposal_id} ratified by quorum: tag {:?} -> {} for {} days",
            pending.theme_tag, pending.weight_after, charter.theme_lifetime_days
        ),
    )
    .await
    .map_err(inside)?;
    let decided = proposal(pool, proposal_id).await.map_err(inside)?.ok_or(Refused::NotFound)?;
    Ok((decided, true))
}

/// Put a ratified theme in force: ONE live row per (project, tag, namespace), with an expiry.
///
/// Upsert rather than insert, because a renewal is the same act: ratifying a second change to a tag already in
/// force must move its `expires_at` forward, not create a row `ThemeWeightTotal` would count twice.
async fn apply(
    pool: &PgPool,
    proposal: &Proposal,
    user_id: i64,
    lifetime_days: f64,
) -> Result<(), sqlx::Error> {
    let now = crate::now_unix();
    let expires = now + lifetime_days * 86400.0;
    sqlx::query(
        "insert into strategic_themes (project_id, tag, namespace, weight, rationale, set_by,
            created_at, expires_at)
         values ($1, $2, $3, $4, $5, $6, to_timestamp($7), to_timestamp($8))
         on conflict (project_id, tag, namespace) do update set
            weight = excluded.weight, rationale = excluded.rationale,
            set_by = excluded.set_by, expires_at = excluded.expires_at",
    )
    .bind(proposal.project_id)
    .bind(&proposal.theme_tag)
    .bind(&proposal.theme_namespace)
    .bind(proposal.weight_after)
    .bind(&proposal.rationale)
    .bind(user_id)
    .bind(now)
    .bind(expires)
    .execute(pool)
    .await?;
    audit(
        pool,
        proposal.project_id,
        user_id,
        "set_strategic_theme",
        proposal.id,
        &format!(
            "tag {:?} is now {} until {}",
            proposal.theme_tag,
            proposal.weight_after,
            // The date only, as the Go prints it: `2006-01-02` is a day a reader can act on.
            civil_date(expires)
        ),
    )
    .await
}

/// Cancel a pending proposal. The proposer may withdraw their own; a maintainer may withdraw any, which is
/// cleanup rather than steering because a withdrawn proposal changes no weight.
///
/// # Errors
/// `sqlx::Error`, or a [`Refused`] wrapped in it.
pub async fn withdraw(
    pool: &PgPool,
    proposal_id: i64,
    user_id: i64,
    is_maintainer: bool,
) -> Result<(), Refused> {
    let inside = move |error: sqlx::Error| Refused::Failed(error.to_string());
    let Some(pending) = proposal(pool, proposal_id).await.map_err(inside)? else {
        return Err(Refused::NotFound);
    };
    if pending.status != "pending" {
        return Err(Refused::Decided(pending.status));
    }
    if pending.proposed_by != user_id && !is_maintainer {
        return Err(Refused::NotYours);
    }
    sqlx::query(
        "update strategic_theme_proposals set status = 'withdrawn', decided_at = to_timestamp($1)
          where id = $2 and status = 'pending'",
    )
    .bind(crate::now_unix())
    .bind(proposal_id)
    .execute(pool)
    .await
    .map_err(inside)?;
    audit(
        pool,
        pending.project_id,
        user_id,
        "withdraw_strategic_theme",
        proposal_id,
        &format!("proposal {proposal_id} withdrawn by {user_id}"),
    )
    .await
    .map_err(inside)?;
    Ok(())
}

/// Unix seconds as `YYYY-MM-DD`, UTC, without pulling a date library into the store crate for one audit line.
fn civil_date(unix: f64) -> String {
    let days = (unix / 86400.0).floor() as i64;
    // Howard Hinnant's days-from-civil, inverted.
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    format!("{y:04}-{m:02}-{d:02}")
}

async fn audit(
    pool: &PgPool,
    project_id: i64,
    actor: i64,
    action: &str,
    entity_id: i64,
    detail: &str,
) -> Result<(), sqlx::Error> {
    sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values (nullif($1,0), $2, $3, 'theme', $4, $5, to_timestamp($6))",
    )
    .bind(project_id)
    .bind(actor)
    .bind(action)
    .bind(entity_id)
    .bind(detail)
    .bind(crate::now_unix())
    .execute(pool)
    .await?;
    Ok(())
}
