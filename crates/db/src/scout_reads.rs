//! Scout's reads: the catalogue vocabulary, the readable candidates with their assertions, the coverage counts, and the pain.
//!
//! Four reads that exist only for the one Scout route, kept together because each of them is a different answer to a different question
//! and merging them would produce a query nobody can read:
//!
//! * the **catalogue** is the vocabulary the decomposition matches against, and passing a filtered one silently changes what Scout can
//!   see — so it is always the whole list;
//! * the **candidates** are the projects the viewer may read, with `capability_assertions.value` attached (the string a project claims);
//! * the **state counts** are `capability_confirmations` (whether anyone backed the claim). Coverage needs the *second* question: a first
//!   version read states out of the values, so `covered` was unreachable and every capability came back `partial`;
//! * **pain** is what is currently wrong with a project — unresolved complaints only.
//!
//! Two distinctions this module exists to preserve, both learned the hard way elsewhere in the port:
//!
//! * a project that cannot be read is not in the list **and not counted**, so the report cannot be used to infer that a private project
//!   exists;
//! * `reports == 0` is not a 0% outcome rate. The sample size travels beside the rate, and the caller renders it.

use sqlx::PgPool;
use std::collections::{BTreeMap, HashSet};

use concord_scout::{Capability, Candidate, Pain};

use crate::capabilities::{CONFIRM_QUORUM, DISPUTE_QUORUM};

/// A named deliberate defect, armed at runtime so a control run needs no rebuild.
///
/// Same seam as `ops_reads`, `holds` and `taxonomy`: the probe runs green, the environment names one of these, the probe names the
/// observation it expected to move. A mutant that no observation kills is a rule nothing tests.
fn mutant(name: &str) -> bool {
    std::env::var("CONCORD_MUTANT")
        .unwrap_or_default()
        .split(',')
        .any(|armed| armed.trim() == name)
}

/// The whole capability vocabulary, in catalogue order.
///
/// # Errors
/// `sqlx::Error`.
pub async fn catalog(pool: &PgPool) -> Result<Vec<Capability>, sqlx::Error> {
    let rows: Vec<(String, String, String)> =
        sqlx::query_as("select key, label, coalesce(category, '') from capabilities order by category, key")
            .fetch_all(pool)
            .await?;
    Ok(rows
        .into_iter()
        .map(|(key, label, category)| {
            let mut capability = Capability::catalog(key, label, category);
            // The catalogue row is not a proposal; `coverage` is filled in by the caller across the candidate set.
            capability.coverage = String::new();
            capability
        })
        .collect())
}

/// Every project the viewer may read, with the attributes Scout scores against.
///
/// **The visibility filter is in the WHERE, not applied afterwards.** Applying it afterwards is the same result and one extra `continue`
/// — but the row has already been read, so a later edit that adds a field to the map would leak it for a project the caller cannot see.
/// Unknown visibility fails closed for the same reason it does in `readable` on the app side: the CHECK constraint makes it unreachable
/// from the database, and fail-open is the wrong direction for an access rule.
///
/// # Errors
/// `sqlx::Error`.
pub async fn candidates(pool: &PgPool, viewer: Option<i64>) -> Result<Vec<Candidate>, sqlx::Error> {
    // The visibility clause is removed — not widened — under `scout-private-leak`, because the observation that has to move is "a
    // project the viewer cannot read is not counted", and widening to `true` would also have to explain `unlisted`.
    let visible = if mutant("scout-private-leak") {
        "true"
    } else {
        "p.visibility in ('public', 'unlisted')
             or ($1::bigint is not null and exists (
                   select 1 from members m where m.project_id = p.id and m.user_id = $1))"
    };
    let rows: Vec<(i64, String, String, String)> = sqlx::query_as(&format!(
        "select p.id, p.slug, p.name, coalesce(p.license, '')
           from projects p
          where {visible}
          order by p.id"
    ))
    .bind(viewer)
    .fetch_all(pool)
    .await?;
    if rows.is_empty() {
        return Ok(Vec::new());
    }
    let ids: Vec<i64> = rows.iter().map(|row| row.0).collect();

    // One query for every project's assertions rather than one per project: the N+1 here is over the entire catalogue on every Scout
    // request.
    let assertions: Vec<(i64, String, String)> = sqlx::query_as(
        "select project_id, capability, coalesce(value, '') from capability_assertions where project_id = any($1)",
    )
    .bind(&ids)
    .fetch_all(pool)
    .await?;
    let mut by_project: BTreeMap<i64, BTreeMap<String, String>> = BTreeMap::new();
    for (project_id, capability, value) in assertions {
        by_project.entry(project_id).or_default().insert(capability, value);
    }

    let rates = crate::finder_reads::report_rates(pool, &ids).await?;

    Ok(rows
        .into_iter()
        .map(|(id, slug, name, license)| {
            let (outcome_rate, reports) = rates.get(&id).copied().unwrap_or((0.0, 0));
            Candidate {
                project_id: id,
                slug,
                name,
                license,
                attrs: by_project.remove(&id).unwrap_or_default(),
                reports,
                outcome_rate,
            }
        })
        .collect())
}

/// How many assertions of each state exist per capability, across a set of projects.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, serde::Serialize)]
pub struct StateCounts {
    /// Assertions another user confirmed.
    pub confirmed: i64,
    /// Assertions nobody has backed, whatever they say.
    pub asserted: i64,
    /// Assertions another user disputed.
    pub disputed: i64,
}

impl StateCounts {
    /// How many projects said anything at all, in any state — the number that separates `partial` from `open`.
    #[must_use]
    pub fn total(self) -> i64 {
        self.confirmed + self.asserted + self.disputed
    }
}

/// The assertion-state counts per capability across the given projects.
///
/// The confirm/dispute subqueries are copied from the panel's expression **verbatim**, including `c.user_id <> a.asserted_by` — the author
/// does not get to confirm their own claim. A re-typed variant that dropped that clause would let an author reach quorum alone, so the
/// expression is reused rather than paraphrased.
///
/// Precedence matches the panel's: a dispute outranks a confirmation, because two of each is contested.
///
/// # Errors
/// `sqlx::Error`.
pub async fn state_counts(
    pool: &PgPool,
    project_ids: &[i64],
) -> Result<BTreeMap<String, StateCounts>, sqlx::Error> {
    let mut out: BTreeMap<String, StateCounts> = BTreeMap::new();
    if project_ids.is_empty() {
        return Ok(out);
    }
    let ids: Vec<i64> = project_ids.to_vec();
    let rows: Vec<(String, i64, i64)> = sqlx::query_as(
        "select a.capability,
                (select count(*) from capability_confirmations c
                  where c.assertion_id = a.id and c.confirmed <> 0 and c.user_id <> a.asserted_by),
                (select count(*) from capability_confirmations c
                  where c.assertion_id = a.id and c.confirmed = 0 and c.user_id <> a.asserted_by)
           from capability_assertions a
          where a.project_id = any($1)",
    )
    .bind(&ids)
    .fetch_all(pool)
    .await?;
    for (key, confirms, disputes) in rows {
        let entry = out.entry(key).or_default();
        if disputes >= DISPUTE_QUORUM {
            entry.disputed += 1;
        } else if confirms >= CONFIRM_QUORUM {
            entry.confirmed += 1;
        } else {
            entry.asserted += 1;
        }
    }
    Ok(out)
}

/// The known trouble with one project: unresolved complaints, worst first.
///
/// `open` and `validated` are the unresolved states; `closed`, `rejected` and `merged_into` are history and would read as current problems.
///
/// The body is truncated to 160 **runes**, not bytes: a byte cut through a multi-byte character produces invalid UTF-8, which becomes a
/// replacement character in the middle of a complaint a reader is trying to read.
///
/// # Errors
/// `sqlx::Error`.
pub async fn pain(pool: &PgPool, project_id: i64, limit: i64) -> Result<Vec<Pain>, sqlx::Error> {
    let limit = if limit <= 0 { 5 } else { limit };
    let rows: Vec<(String, String, i64)> = sqlx::query_as(
        "select title, coalesce(body, ''), coalesce(severity, 0)
           from complaints
          where project_id = $1 and status in ('open', 'validated')
          order by severity desc, id desc
          limit $2",
    )
    .bind(project_id)
    .bind(limit)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(title, body, severity)| Pain {
            kind: "complaint".to_string(),
            title,
            detail: truncate_runs(&body, 160),
            severity: severity as f64,
        })
        .collect())
}

/// Shorten to at most `limit` runes, marking the cut.
fn truncate_runs(text: &str, limit: usize) -> String {
    let trimmed = text.trim();
    let mut out = String::with_capacity(trimmed.len().min(limit * 4));
    for (index, character) in trimmed.chars().enumerate() {
        if index >= limit {
            out.push('…');
            return out;
        }
        out.push(character);
    }
    out
}

/// The project ids of a candidate set, for the coverage query.
#[must_use]
pub fn ids_of(candidates: &[Candidate]) -> Vec<i64> {
    let mut seen = HashSet::new();
    candidates
        .iter()
        .filter(|candidate| seen.insert(candidate.project_id))
        .map(|candidate| candidate.project_id)
        .collect()
}
