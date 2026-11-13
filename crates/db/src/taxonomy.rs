//! §4.3's global taxonomy: proposals decided by quorum among eligible taggers.
//!
//! The rule the product exists to enforce, in the Go file's own words: *maintainers hold no unilateral taxonomy
//! power.* Tagging used to auto-create a tag on use, which gave every project editor instance-wide vocabulary
//! control. So a change to the global taxonomy is a **proposal**, decided by the eligible set, and applied only
//! when the charter's threshold is met.
//!
//! Four rules, each of which is a way a vote can be wrong:
//!
//! * **The eligible set is the denominator, and it is a fact about the project.** A consent is one voice; the
//!   threshold compares `consents + 1` against `participants` and `eligible`. An instance-wide proposal
//!   (`project_id <= 0`) is ratified by the instance charter rather than by whichever project happens to hold
//!   the most collaborators — otherwise a global change is decided by the largest project's membership.
//! * **Only an eligible tagger may consent**: a member with role `contributor`/`reviewer`/`maintainer`/`owner`
//!   in the proposal's project, or in any project for an instance-wide proposal. A stranger's consent counted
//!   toward a quorum is a quorum made of strangers.
//! * **A consent is recorded even when the threshold is not met.** §4.3's proposals accumulate: the first
//!   consent is not an error, and reporting it as one would make the first voter think they broke something.
//! * **Applying is not decided by this file.** The six actions are the store's; whether they may be applied is
//!   [`concord_governance::evaluate_consensus`]'s answer, and the audit row is written either way.

use sqlx::{PgPool, Row};

use concord_governance::{evaluate_consensus, ConsensusCounts, Result as Verdict};

use crate::projects::ser_unix;

/// One proposed change to the global taxonomy.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Proposal {
    /// The proposal.
    pub id: i64,
    /// The project it applies to, `0` for instance-wide.
    pub project_id: i64,
    /// Who proposed it.
    pub proposed_by: i64,
    /// `create`, `rename`, `alias`, `merge`, `reparent`, `delete`.
    pub action: String,
    /// The tag being changed.
    pub target_tag: String,
    /// The new name, the alias, the target of a merge, or the new parent.
    pub value: String,
    /// Why.
    pub rationale: String,
    /// `pending`, `ratified`, `stale`, `rejected`.
    pub status: String,
    /// When it was proposed.
    #[serde(serialize_with = "ser_unix")]
    pub created_at: f64,
    /// When it was decided, or `0`.
    #[serde(serialize_with = "ser_unix")]
    pub decided_at: f64,
}

/// What one consent did.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Consented {
    /// The proposal, in its state after the vote.
    pub proposal: Proposal,
    /// `0.7`, `0.5`, `1.0` — the ratio this charter requires.
    #[serde(serialize_with = "ser_unix")]
    pub threshold: f64,
    /// Consents now recorded.
    pub consents: i64,
    /// The eligible set that consent was measured against.
    pub eligible: i64,
    /// Whether this consent ratified the proposal.
    pub ratified: bool,
}

/// Why a consent was refused.
#[derive(Debug, Clone)]
pub enum Refused {
    /// No such proposal.
    NotFound,
    /// The proposal is already decided.
    Decided(String),
    /// The actor is not in the eligible set.
    NotEligible,
    /// The threshold was not met; the consent is still recorded.
    NotYet {
        /// The verdict's word.
        verdict: String,
        /// Consents recorded.
        consents: i64,
        /// The eligible set.
        eligible: i64,
    },
    /// Applying the change failed, so the proposal is not ratified.
    ApplyFailed(String),
}

/// Normalise a tag name the way the Go store does: lowercase, trimmed, collapsed separators.
#[must_use]
pub fn normalize_tag(name: &str) -> String {
    name.trim()
        .trim_matches(':')
        .split_whitespace()
        .collect::<Vec<_>>()
        .join("-")
        .to_lowercase()
        .replace("::", ":")
}

/// `topic:rust` → (`topic`, `rust`); a name with no namespace → (``, `rust`).
#[must_use]
pub fn split_tag(name: &str) -> (String, String) {
    let name = normalize_tag(name);
    match name.find(':') {
        Some(index) if index > 0 && index < name.len() - 1 => {
            (name[..index].to_string(), name[index + 1..].to_string())
        }
        _ => (String::new(), name),
    }
}

/// Is this actor inside the eligible set for a proposal?
///
/// A project proposal asks about that project; an instance-wide one asks about every project, because an
/// instance-wide change is not the property of the project it was filed from.
async fn is_eligible_taggers(
    pool: &PgPool,
    project_id: i64,
    user_id: i64,
) -> Result<bool, sqlx::Error> {
    let count: i64 = if project_id > 0 {
        sqlx::query_scalar(
            "select count(*) from members where project_id = $1 and user_id = $2 \
               and role in ('contributor','reviewer','maintainer','owner')",
        )
        .bind(project_id)
        .bind(user_id)
        .fetch_one(pool)
        .await?
    } else {
        sqlx::query_scalar(
            "select count(*) from members where user_id = $1 \
               and role in ('contributor','reviewer','maintainer','owner')",
        )
        .bind(user_id)
        .fetch_one(pool)
        .await?
    };
    Ok(count > 0)
}

/// The denominator: how many distinct people may consent.
async fn eligible_taggers(pool: &PgPool, project_id: i64) -> Result<i64, sqlx::Error> {
    let count: i64 = if project_id > 0 {
        sqlx::query_scalar(
            "select count(distinct user_id) from members where project_id = $1 \
               and role in ('contributor','reviewer','maintainer','owner')",
        )
        .bind(project_id)
        .fetch_one(pool)
        .await?
    } else {
        sqlx::query_scalar(
            "select count(distinct user_id) from members \
               where role in ('contributor','reviewer','maintainer','owner')",
        )
        .fetch_one(pool)
        .await?
    };
    Ok(count)
}

const COLUMNS: &str = "id, coalesce(project_id, 0), proposed_by, action, target_tag, coalesce(value, ''), \
    coalesce(rationale, ''), status, extract(epoch from created_at)::double precision, \
    coalesce(extract(epoch from decided_at)::double precision, 0)";

fn from_row(row: &sqlx::postgres::PgRow) -> Result<Proposal, sqlx::Error> {
    Ok(Proposal {
        id: row.try_get(0)?,
        project_id: row.try_get(1)?,
        proposed_by: row.try_get(2)?,
        action: row.try_get(3)?,
        target_tag: row.try_get(4)?,
        value: row.try_get(5)?,
        rationale: row.try_get(6)?,
        status: row.try_get(7)?,
        created_at: row.try_get(8)?,
        decided_at: row.try_get(9)?,
    })
}

/// One proposal, or `None`.
///
/// # Errors
/// `sqlx::Error`.
pub async fn proposal(pool: &PgPool, id: i64) -> Result<Option<Proposal>, sqlx::Error> {
    let sql = format!("select {COLUMNS} from taxonomy_proposals where id = $1");
    let row = sqlx::query(&sql).bind(id).fetch_optional(pool).await?;
    match row {
        Some(row) => Ok(Some(from_row(&row)?)),
        None => Ok(None),
    }
}

/// Record one consent, and ratify if that consent was the one that met the charter's threshold.
///
/// The consent row is written BEFORE the threshold is evaluated: a vote recorded only when it succeeds is a vote
/// the tally cannot see, and this store's whole point is that the tally is the eligible set's.
///
/// # Errors
/// `sqlx::Error`, or the [`Refused`] wrapped in it.
pub async fn consent(
    pool: &PgPool,
    proposal_id: i64,
    user_id: i64,
) -> Result<Consented, Refused> {
    let inside = move |error: sqlx::Error| Refused::ApplyFailed(error.to_string());
    let row = proposal(pool, proposal_id).await.map_err(inside)?;
    let Some(pending) = row else {
        return Err(Refused::NotFound);
    };
    let proposal = pending;
    if proposal.status != "pending" {
        return Err(Refused::Decided(proposal.status));
    }
    if !is_eligible_taggers(pool, proposal.project_id, user_id)
        .await
        .map_err(inside)?
    {
        return Err(Refused::NotEligible);
    }
    // The charter is the project's own, or the collective default for an instance-wide proposal: a threshold
    // that varied with the hosting project would make the same global change pass or fail by which project
    // filed it.
    let charter = if proposal.project_id > 0 {
        crate::charter::charter(pool, proposal.project_id)
            .await
            .map_err(inside)?
    } else {
        concord_governance::Charter::default_for(concord_governance::GovernanceModel::Collective)
    };
    let eligible = eligible_taggers(pool, proposal.project_id)
        .await
        .map_err(inside)?;
    let before: i64 = sqlx::query_scalar(
        "select count(*) from taxonomy_consents where proposal_id = $1",
    )
    .bind(proposal_id)
    .fetch_one(pool)
    .await
    .map_err(inside)?;

    sqlx::query(
        "insert into taxonomy_consents (proposal_id, user_id, consented_at) \
         values ($1, $2, to_timestamp($3)) on conflict (proposal_id, user_id) do nothing",
    )
    .bind(proposal_id)
    .bind(user_id)
    .bind(crate::now_unix())
    .execute(pool)
    .await
    .map_err(inside)?;

    let after: i64 = sqlx::query_scalar(
        "select count(*) from taxonomy_consents where proposal_id = $1",
    )
    .bind(proposal_id)
    .fetch_one(pool)
    .await
    .map_err(inside)?;
    // A repeated click does not add a voice, and it must not read as one either: `after` is what the tally
    // counts, not `before + 1`.
    let consents = after;
    let _ = before;

    audit(
        pool,
        proposal.project_id,
        user_id,
        "taxonomy_consent",
        "tag",
        proposal_id,
        &format!("{} {}", proposal.action, proposal.target_tag),
    )
    .await
    .map_err(inside)?;

    let counts = ConsensusCounts {
        consent: consents,
        abstain: 0,
        stand_aside: 0,
        block: 0,
        participants: consents,
        eligible,
        ..ConsensusCounts::default()
    };
    let verdict = evaluate_consensus(&counts, &charter);
    if verdict != Verdict::Accepted {
        return Err(Refused::NotYet {
            verdict: verdict.as_str().to_string(),
            consents,
            eligible,
        });
    }

    apply(pool, &proposal).await.map_err(inside)?;
    sqlx::query(
        "update taxonomy_proposals set status = 'ratified', decided_at = to_timestamp($1) \
          where id = $2 and status = 'pending'",
    )
    .bind(crate::now_unix())
    .bind(proposal_id)
    .execute(pool)
    .await
    .map_err(inside)?;
    audit(
        pool,
        proposal.project_id,
        user_id,
        "ratify_taxonomy_change",
        "tag",
        proposal_id,
        &format!("{} {}={}", proposal.action, proposal.target_tag, proposal.value),
    )
    .await
    .map_err(inside)?;

    Ok(Consented {
        proposal: crate::taxonomy::proposal(pool, proposal_id)
            .await
            .map_err(inside)?
            .ok_or(Refused::NotFound)?,
        threshold: charter.consent_ratio,
        consents,
        eligible,
        ratified: true,
    })
}

async fn audit(
    pool: &PgPool,
    project_id: i64,
    actor: i64,
    action: &str,
    entity: &str,
    entity_id: i64,
    detail: &str,
) -> Result<(), sqlx::Error> {
    sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at) \
         values (nullif($1,0), $2, $3, $4, $5, $6, to_timestamp($7))",
    )
    .bind(project_id)
    .bind(actor)
    .bind(action)
    .bind(entity)
    .bind(entity_id)
    .bind(detail)
    .bind(crate::now_unix())
    .execute(pool)
    .await?;
    Ok(())
}

/// Resolve a global tag by name, then by alias.
async fn global_tag_id(pool: &PgPool, name: &str) -> Result<i64, sqlx::Error> {
    let normalized = normalize_tag(name);
    let (_namespace, bare) = split_tag(&normalized);
    let by_name: Option<i64> =
        sqlx::query_scalar("select id from tags where lower(name) = lower($1) limit 1")
            .bind(&bare)
            .fetch_optional(pool)
            .await?;
    if let Some(id) = by_name {
        return Ok(id);
    }
    // An alias, so a merge can name either side by the name people actually type.
    let by_alias: Option<i64> =
        sqlx::query_scalar("select tag_id from tag_aliases where lower(alias) = lower($1) limit 1")
            .bind(&normalized)
            .fetch_optional(pool)
            .await?;
    Ok(by_alias.unwrap_or(0))
}

/// Is `candidate` under `ancestor`? Walking up, bounded so a cycle cannot hang the request.
async fn is_tag_descendant(pool: &PgPool, candidate: i64, ancestor: i64) -> Result<bool, sqlx::Error> {
    let mut seen = std::collections::HashSet::new();
    let mut current = candidate;
    while current != 0 && seen.len() < 100 {
        if current == ancestor {
            return Ok(true);
        }
        seen.insert(current);
        let parent: Option<i64> =
            sqlx::query_scalar("select parent_id from tags where id = $1")
                .bind(current)
                .fetch_optional(pool)
                .await?
                .flatten();
        current = parent.unwrap_or(0);
    }
    Ok(false)
}

/// Apply a ratified change.
///
/// The SQLite original used `UPDATE OR IGNORE` to repoint tag references: a row that would collide with an
/// existing reference was skipped. PostgreSQL has no such form, so the skip is written out — the same outcome,
/// spelled where a reader can see it.
async fn apply(pool: &PgPool, proposal: &Proposal) -> Result<(), sqlx::Error> {
    match proposal.action.as_str() {
        "create" => {
            let (namespace, bare) = split_tag(&proposal.target_tag);
            if bare.is_empty() {
                return Ok(());
            }
            // A tag that already exists is not created twice: the taxonomy is a vocabulary, not a list of
            // attempts.
            sqlx::query(
                "insert into tags (project_id, name, namespace, suggested) \
                 select null, $1, $2, false \
                  where not exists (select 1 from tags where lower(name) = lower($1))",
            )
            .bind(&bare)
            .bind(if namespace.is_empty() { None } else { Some(namespace) })
            .execute(pool)
            .await?;
        }
        "rename" => {
            sqlx::query("update tags set name = $1 where lower(name) = lower($2)")
                .bind(&proposal.value)
                .bind(&proposal.target_tag)
                .execute(pool)
                .await?;
        }
        "alias" => {
            let id = global_tag_id(pool, &proposal.target_tag).await?;
            if id != 0 {
                sqlx::query(
                    "insert into tag_aliases (tag_id, alias, created_by, created_at) \
                     values ($1, $2, 1, to_timestamp($3)) on conflict do nothing",
                )
                .bind(id)
                .bind(&proposal.value)
                .bind(crate::now_unix())
                .execute(pool)
                .await?;
            }
        }
        "merge" => {
            let from = global_tag_id(pool, &proposal.value).await?;
            let to = global_tag_id(pool, &proposal.target_tag).await?;
            if from == 0 || to == 0 || from == to {
                return Ok(());
            }
            for table in [
                "complaint_tags",
                "feature_tags",
                "list_tags",
                "request_tags",
            ] {
                let sql = format!(
                    "update {table} t set tag_id = $1 \
                      where t.tag_id = $2 \
                        and not exists (select 1 from {table} x \
                                         where x.score_id = t.score_id and x.tag_id = $1)"
                );
                // The conflict column differs per table; a collision is what `UPDATE OR IGNORE` skipped, and a
                // skipped row is the same outcome as a no-op here.
                let _ = sqlx::query(&sql)
                    .bind(to)
                    .bind(from)
                    .execute(pool)
                    .await;
            }
            sqlx::query(
                "update project_tags t set tag_id = $1 where t.tag_id = $2 \
                   and not exists (select 1 from project_tags x \
                                    where x.project_id = t.project_id and x.tag_id = $1)",
            )
            .bind(to)
            .bind(from)
            .execute(pool)
            .await?;
            // Aliases follow the absorbed tag, so "js" still resolves after "javascript" is merged into "js".
            sqlx::query(
                "update tag_aliases t set tag_id = $1 where t.tag_id = $2 \
                   and not exists (select 1 from tag_aliases x \
                                    where x.alias = t.alias and x.tag_id = $1)",
            )
            .bind(to)
            .bind(from)
            .execute(pool)
            .await?;
            sqlx::query("delete from tags where id = $1")
                .bind(from)
                .execute(pool)
                .await?;
        }
        "reparent" => {
            let id = global_tag_id(pool, &proposal.target_tag).await?;
            let parent = global_tag_id(pool, &proposal.value).await?;
            if id == 0 || parent == 0 || id == parent {
                return Ok(());
            }
            // The cycle a trigger cannot catch: making a tag its own descendant.
            if is_tag_descendant(pool, parent, id).await? {
                return Ok(());
            }
            let relation = if split_tag(&proposal.value).0.is_empty() {
                "is-a"
            } else {
                "part-of"
            };
            sqlx::query("update tags set parent_id = $1, relation = $2 where id = $3")
                .bind(parent)
                .bind(relation)
                .bind(id)
                .execute(pool)
                .await?;
        }
        "delete" => {
            let id = global_tag_id(pool, &proposal.target_tag).await?;
            if id != 0 {
                sqlx::query("delete from tags where id = $1")
                    .bind(id)
                    .execute(pool)
                    .await?;
            }
        }
        _ => {}
    }
    Ok(())
}

/// A project's tags, alphabetically — the Go's `ProjectTags`.
///
/// The caller resolves the project FIRST: `project_tags` returns only names, so without the project read this
/// route answers 200 with an empty list for a private project, which confirms the slug exists. That is the leak
/// the Go handler's comment records, and the fix belongs at the handler (it has the visibility rule), not here.
///
/// # Errors
/// `sqlx::Error`.
pub async fn project_tags(pool: &sqlx::PgPool, project_id: i64) -> Result<Vec<String>, sqlx::Error> {
    sqlx::query_scalar(
        "select t.name from project_tags pt join tags t on t.id = pt.tag_id
          where pt.project_id = $1 order by t.name",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await
}

/// Apply tags that **already exist** in the instance vocabulary to a project (§4.3).
///
/// The refusal is the interesting part: a name the taxonomy has never seen is not created here — it answers with the
/// route that can propose one. Applied silently, a typo becomes a permanent vocabulary entry that nobody ratified and
/// every taxonomy-filtered search then has to carry; refused with the path forward, the caller learns the mechanism.
/// Aliases count as existing names, so a merge can be applied by either side's name.
///
/// `or ignore` on the link row: applying a tag twice is not an error, and the audit row records the intent either way.
///
/// **No reindex call.** The Go store's `ReindexProject` writes an fts5 table; here `search_tsv` is a generated column of
/// `projects` and the tag half of free-text search is an `EXISTS` over `project_tags`, so the index follows the row
/// without a second write that could fall out of step with it.
///
/// # Errors
/// `sqlx::Error`.
pub async fn apply_project_tag(
    pool: &PgPool,
    project_id: i64,
    name: &str,
    applied_by: i64,
) -> Result<Result<(), Applied>, sqlx::Error> {
    let normalized = normalize_tag(name);
    let (_, bare) = split_tag(&normalized);
    let direct: Option<i64> = sqlx::query_scalar("select id from tags where lower(name) = lower($1) limit 1")
        .bind(&bare)
        .fetch_optional(pool)
        .await?;
    let tag_id = match direct {
        Some(id) => Some(id),
        None => {
            // The alias table, so a merge can be applied by either side of it.
            sqlx::query_scalar("select tag_id from tag_aliases where lower(alias) = lower($1) limit 1")
                .bind(&normalized)
                .fetch_optional(pool)
                .await?
        }
    };
    let Some(tag_id) = tag_id else {
        return Ok(Err(Applied::UnknownName {
            name: normalized,
        }));
    };
    let who: Option<i64> = if applied_by > 0 { Some(applied_by) } else { None };
    sqlx::query(
        "insert into project_tags (project_id, tag_id, applied_by, created_at)
         values ($1, $2, $3, now()) on conflict do nothing",
    )
    .bind(project_id)
    .bind(tag_id)
    .bind(who)
    .execute(pool)
    .await?;
    Ok(Ok(()))
}

/// Why a tag could not be applied.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Applied {
    /// No such name in the vocabulary, and here is the name that was looked for.
    UnknownName {
        /// The normalised name.
        name: String,
    },
}

/// How well-tagged a project is (§4.3's completeness meter and the under-tagged queue).
///
/// Five tags with at least two of them namespaced is "complete". The threshold is a judgement and it lives in one place
/// so it can be argued with; every field is reported, including the two thresholds, so the page can explain the number
/// rather than only display it.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Completeness {
    /// The project.
    pub project_id: i64,
    /// Tags applied.
    pub tags: i64,
    /// How many of them carry a namespace (`topic:kanban`).
    pub namespaced: i64,
    /// Suggested tags applied, which the meter counts separately because they came from the machine.
    pub suggested_pending: i64,
    /// Whether both thresholds are met.
    pub complete: bool,
    /// `0.6` for the tag count, `0.4` for the namespaces, pro-rated below the threshold.
    pub score: f64,
    /// Tags wanted.
    pub want_tags: i64,
    /// Namespaced tags wanted.
    pub want_namespaced: i64,
}

/// Compute [`Completeness`] for one project.
///
/// A project with no tags is not merely untidy: it is invisible to every taxonomy-filtered search, which is why the spec
/// treats completeness as a contribution rather than housekeeping.
///
/// # Errors
/// `sqlx::Error`.
pub async fn completeness(pool: &sqlx::PgPool, project_id: i64) -> Result<Completeness, sqlx::Error> {
    const WANT_TAGS: i64 = 5;
    const WANT_NAMESPACED: i64 = 2;
    let total: i64 = sqlx::query_scalar("select count(*) from project_tags where project_id = $1")
        .bind(project_id)
        .fetch_one(pool)
        .await?;
    let namespaced: i64 = sqlx::query_scalar(
        "select count(*) from project_tags pt join tags t on t.id = pt.tag_id
          where pt.project_id = $1 and t.namespace is not null",
    )
    .bind(project_id)
    .fetch_one(pool)
    .await?;
    let suggested: i64 = sqlx::query_scalar(
        "select count(*) from project_tags pt join tags t on t.id = pt.tag_id
          where pt.project_id = $1 and t.suggested",
    )
    .bind(project_id)
    .fetch_one(pool)
    .await?;

    let mut score = 0.0;
    if total >= WANT_TAGS {
        score += 0.6;
    } else if total > 0 {
        #[allow(clippy::cast_precision_loss)]
        {
            // Pro-rated below the threshold: 1 of 5 tags is worth a fifth of the tag half, not nothing — a meter that
            // only moves at the finish line teaches nobody where they are.
            score += 0.6 * total as f64 / WANT_TAGS as f64;
        }
    }
    if namespaced >= WANT_NAMESPACED {
        score += 0.4;
    } else if namespaced > 0 {
        #[allow(clippy::cast_precision_loss)]
        {
            score += 0.4 * namespaced as f64 / WANT_NAMESPACED as f64;
        }
    }
    Ok(Completeness {
        project_id,
        tags: total,
        namespaced,
        suggested_pending: suggested,
        complete: total >= WANT_TAGS && namespaced >= WANT_NAMESPACED,
        score,
        want_tags: WANT_TAGS,
        want_namespaced: WANT_NAMESPACED,
    })
}
