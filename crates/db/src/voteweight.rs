//! A voter's influence in a project — §8.3's formula, in the one place it is computed.
//!
//! The tempting shortcut is a simpler weight for the smaller surfaces ("it is only an answer vote"). That is exactly how
//! an account manufactures influence: §8.3's terms exist so reputation, role and account age all have to be earned, and
//! a vote weight is a vote weight whatever it is voting on.
//!
//! Two deliberate omissions, both ported:
//!
//! * **no expertise term for answers.** §8.3 multiplies in an expertise multiplier scoped to the items being compared.
//!   Solutions have `expertise_tags`; a request answer has no tags and there is no reader behind `request_tags`. Reusing
//!   the solution's tags or the voter's whole tag reputation would apply a multiplier computed from an unrelated
//!   question, which is worse than omitting one the data cannot support;
//! * **reputation is a plain `sum(points)`**, not decayed by `rep_halflife_days`. The charter carries a half-life but the
//!   reference's `GetReputation` never applies it; the port follows the reference and this note exists so the difference
//!   is a decision rather than a discovery.

use sqlx::PgPool;

use concord_governance::Charter;

/// This voter's weight on a request answer in `project_id`.
///
/// # Errors
/// `sqlx::Error`.
pub async fn answer_vote_weight(
    pool: &PgPool,
    project_id: i64,
    actor: i64,
    charter: &Charter,
) -> Result<f64, sqlx::Error> {
    let reputation: f64 = sqlx::query_scalar(
        "select coalesce(sum(points), 0)::double precision from reputation_events
          where project_id = $1 and user_id = $2",
    )
    .bind(project_id)
    .bind(actor)
    .fetch_one(pool)
    .await?;
    let role: Option<String> =
        sqlx::query_scalar("select role from members where project_id = $1 and user_id = $2")
            .bind(project_id)
            .bind(actor)
            .fetch_optional(pool)
            .await?;
    let mut weight = concord_ranking::vote_weight(
        reputation,
        concord_ranking::role_multiplier(role.as_deref().unwrap_or("user")),
        charter.vote_weight_cap,
    );

    // §12.3's Sybil defence, applied LAST: a defence an expertise multiplier can halve is not a defence.
    let created_at: Option<f64> = sqlx::query_scalar(
        "select extract(epoch from created_at)::double precision from users where id = $1",
    )
    .bind(actor)
    .fetch_optional(pool)
    .await?;
    if let Some(created_at) = created_at {
        weight *= concord_ranking::age_gate_multiplier(concord_ranking::account_age_days(
            created_at,
            crate::now_unix(),
        ));
    }
    Ok(weight.clamp(0.0, charter.vote_weight_cap))
}

/// This voter's weight on a **solution** in `project_id`, given the tags the two solutions being compared declare.
///
/// This is [`answer_vote_weight`] plus §8.3's expertise term, and the expertise term is the reason the two functions exist
/// separately rather than one taking an `Option`: an answer has no tags and no reader behind its tags, so reusing this
/// formula there would apply a multiplier computed from an unrelated question. A solution's author DOES declare
/// `expertise_tags`, so §6.4's "a Rust-heavy design is weighed by Rust-reputed voters" is computable here and is therefore
/// owed.
///
/// Order matters and is the reference's: reputation × role, then expertise, then the age gate **last** — a Sybil defence an
/// expertise multiplier can halve is not a defence.
///
/// # Errors
/// `sqlx::Error`.
pub async fn solution_vote_weight(
    pool: &PgPool,
    project_id: i64,
    actor: i64,
    charter: &Charter,
    tags: &[String],
) -> Result<f64, sqlx::Error> {
    let reputation: f64 = sqlx::query_scalar(
        "select coalesce(sum(points), 0)::double precision from reputation_events
          where project_id = $1 and user_id = $2",
    )
    .bind(project_id)
    .bind(actor)
    .fetch_one(pool)
    .await?;
    let role: Option<String> =
        sqlx::query_scalar("select role from members where project_id = $1 and user_id = $2")
            .bind(project_id)
            .bind(actor)
            .fetch_optional(pool)
            .await?;
    let mut weight = concord_ranking::vote_weight(
        reputation,
        concord_ranking::role_multiplier(role.as_deref().unwrap_or("user")),
        charter.vote_weight_cap,
    );

    if !tags.is_empty() {
        let per_tag = tag_reputation(pool, project_id, actor).await?;
        let (best, required) = concord_ranking::best_tag_reputation(&per_tag, tags);
        weight *= concord_ranking::expertise_multiplier(best, required);
    }

    let created_at: Option<f64> = sqlx::query_scalar(
        "select extract(epoch from created_at)::double precision from users where id = $1",
    )
    .bind(actor)
    .fetch_optional(pool)
    .await?;
    if let Some(created_at) = created_at {
        weight *= concord_ranking::age_gate_multiplier(concord_ranking::account_age_days(
            created_at,
            crate::now_unix(),
        ));
    }
    // The charter's cap is the product bound and it is applied after everything, including the age gate: the gate may lower
    // a weight below the cap, and nothing may raise it above.
    Ok(weight.clamp(0.0, charter.vote_weight_cap))
}

/// §8.3's reputation **per tag**, for one member in one project.
///
/// Derived, not stored: there is no table for it, and adding one would be a second representation of the contribution
/// history that has to be kept in step with the first. The points per act match what `AddReputation` awards for the same
/// act, so a tag reputation of 90 and a project reputation of 90 mean the same amount of work — which is what makes the
/// multiplier's saturation points meaningful rather than arbitrary.
///
/// Four sources, and each has a reason to be in the list:
///
/// * **validated complaints**, by the complaint's tags — a validated bug is work in that area;
/// * **merged merge requests**, by the tags of the feature they implement — an unmerged one is worth nothing here, because
///   counting openings lets a member farm reputation by filing and abandoning;
/// * **solutions that reached consensus**, by their own declared tags — `draft` and `discussion` are proposals, `consensus`
///   onward are decisions, and a rejected solution earns nothing so nobody can lose an argument and keep the credit;
/// * **votes cast on tagged solutions**, excluding `skip` — a skip says the write-up is unclear, which is not participation
///   in the decision.
///
/// # Errors
/// `sqlx::Error`.
pub async fn tag_reputation(
    pool: &PgPool,
    project_id: i64,
    user_id: i64,
) -> Result<std::collections::HashMap<String, f64>, sqlx::Error> {
    let mut out: std::collections::HashMap<String, f64> = std::collections::HashMap::new();
    let complaint_rows: Vec<(String, f64)> = sqlx::query_as(
        "select lower(t.name), sum($1)::double precision
           from complaints c
           join complaint_tags ct on ct.complaint_id = c.id
           join tags t on t.id = ct.tag_id
          where c.project_id = $2 and c.author_id = $3 and c.status in ('validated','linked','closed')
          group by lower(t.name)",
    )
    .bind(5.0_f64)
    .bind(project_id)
    .bind(user_id)
    .fetch_all(pool)
    .await?;
    let merged_rows: Vec<(String, f64)> = sqlx::query_as(
        "select lower(t.name), sum($1)::double precision
           from merge_requests m
           join feature_tags ft on ft.feature_id = m.feature_id
           join tags t on t.id = ft.tag_id
          where m.project_id = $2 and m.author_id = $3 and m.status = 'merged'
          group by lower(t.name)",
    )
    .bind(3.0_f64)
    .bind(project_id)
    .bind(user_id)
    .fetch_all(pool)
    .await?;
    for (tag, points) in complaint_rows.into_iter().chain(merged_rows) {
        let tag = tag.trim().to_lowercase();
        if !tag.is_empty() {
            *out.entry(tag).or_insert(0.0) += points;
        }
    }

    // The last two sources are keyed by a comma-separated column, so they are expanded here rather than taught to SQL: doing
    // it in the query makes the tag grammar a query language, which is worse than one loop over the handful of rows a
    // person has.
    let shipped: Vec<String> = sqlx::query_scalar(
        "select s.expertise_tags from solutions s join features f on f.id = s.feature_id
          where f.project_id = $1 and s.author_id = $2
            and s.status in ('consensus','ready','in_progress','review','shipped')
            and coalesce(s.expertise_tags, '') <> ''",
    )
    .bind(project_id)
    .bind(user_id)
    .fetch_all(pool)
    .await?;
    for raw in shipped {
        for tag in concord_ranking::parse_expertise_tags(&raw) {
            *out.entry(tag).or_insert(0.0) += 5.0;
        }
    }
    let judged: Vec<String> = sqlx::query_scalar(
        "select s.expertise_tags
           from pairwise_votes v
           join arenas ar on ar.id = v.arena_id
           join solutions s on s.id = v.a
          where ar.project_id = $1 and v.voter_id = $2 and v.outcome <> 'skip'
            and coalesce(s.expertise_tags, '') <> ''",
    )
    .bind(project_id)
    .bind(user_id)
    .fetch_all(pool)
    .await?;
    for raw in judged {
        for tag in concord_ranking::parse_expertise_tags(&raw) {
            *out.entry(tag).or_insert(0.0) += 1.0;
        }
    }
    Ok(out)
}
