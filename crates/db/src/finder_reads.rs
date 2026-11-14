//! The reads the finder needs about a set of projects, in one query each.
//!
//! These are batched by project id rather than written per project, because the engine's candidate set is "every project a
//! visitor can see" — 73 rows on the live instance — and forty of these per project is a page that times out at seventy.
//!
//! The engine itself lives in `concord-finder` and reads no database. This module is the boundary: it turns rows into the plain
//! values the engine takes, and owns no decisions of its own except the ones the reference also puts here.

use std::collections::BTreeMap;

use sqlx::PgPool;

/// One project's finder attributes: `cap:<key>`, `language`, `governance`, `license`.
///
/// A key that is absent means nobody has asserted it, which is different from a key present with the value `"unknown"` (§3.1).
/// That distinction is why the value is inserted verbatim rather than normalised: `store::cap` records `"unknown"` when a
/// contributor has said "nobody knows", and the engine collapes it to unknown at read time (in three places, deliberately).
///
/// # Errors
/// `sqlx::Error`.
pub async fn attributes(
    pool: &PgPool,
    project_ids: &[i64],
) -> Result<BTreeMap<i64, BTreeMap<String, String>>, sqlx::Error> {
    let mut out: BTreeMap<i64, BTreeMap<String, String>> = BTreeMap::new();
    for id in project_ids {
        out.insert(*id, BTreeMap::new());
    }
    if project_ids.is_empty() {
        return Ok(out);
    }
    let ids: Vec<i64> = project_ids.to_vec();

    // Capabilities, as `cap:<capability>`. The assertion table answers at most one row per (project, capability) — every
    // writer upserts — so there is nothing to de-duplicate here.
    let capabilities: Vec<(i64, String, String)> = sqlx::query_as(
        "select project_id, capability, value from capability_assertions where project_id = any($1)",
    )
    .bind(&ids)
    .fetch_all(pool)
    .await?;
    for (project_id, capability, value) in capabilities {
        if let Some(attrs) = out.get_mut(&project_id) {
            attrs.insert(format!("cap:{capability}"), value);
        }
    }

    // The dominant language. `there is no list-project-languages reader` was the reference's note: `SetProjectLanguages`
    // existed with no reader, and the Finder was the reader that was missing. Highest percentage wins, with the language name as
    // the tiebreak, so two languages at 50/50 cannot return a different "primary" on each request and change the question asked.
    let languages: Vec<(i64, String)> = sqlx::query_as(
        "select distinct on (project_id) project_id, language
           from project_languages
          where project_id = any($1)
          order by project_id, pct desc, language asc",
    )
    .bind(&ids)
    .fetch_all(pool)
    .await?;
    for (project_id, language) in languages {
        if let Some(attrs) = out.get_mut(&project_id) {
            if !language.trim().is_empty() {
                attrs.insert("language".to_string(), language);
            }
        }
    }

    // Governance and licence are columns on the project itself; an empty string is absence, and absence is not an attribute.
    let scalars: Vec<(i64, String, Option<String>)> =
        sqlx::query_as("select id, governance_model, license from projects where id = any($1)")
            .bind(&ids)
            .fetch_all(pool)
            .await?;
    for (project_id, governance, license) in scalars {
        if let Some(attrs) = out.get_mut(&project_id) {
            if !governance.trim().is_empty() {
                attrs.insert("governance".to_string(), governance);
            }
            if let Some(license) = license {
                if !license.trim().is_empty() {
                    attrs.insert("license".to_string(), license);
                }
            }
        }
    }
    Ok(out)
}

/// Each project's reputation-weighted field-report outcome rate and sample size.
///
/// The rate and the sample size travel together because they answer different questions: `0.9` over three reports and `0.9` over
/// three hundred are the same number and not the same evidence, and the engine EXCLUDES the term entirely at zero reports rather
/// than scoring it zero.
///
/// # Errors
/// `sqlx::Error`.
pub async fn report_rates(
    pool: &PgPool,
    project_ids: &[i64],
) -> Result<BTreeMap<i64, (f64, i64)>, sqlx::Error> {
    let mut out: BTreeMap<i64, (f64, i64)> = BTreeMap::new();
    if project_ids.is_empty() {
        return Ok(out);
    }
    let ids: Vec<i64> = project_ids.to_vec();
    // Removal is a HIDE (`removed = 0`), not a delete, so the filter is here and not in the schema.
    let rows: Vec<(i64, String, f64)> = sqlx::query_as(
        "select r.project_id, r.outcome,
                coalesce((select sum(points) from reputation_events e
                           where e.project_id = r.project_id and e.user_id = r.user_id), 0)
           from field_reports r
          where r.project_id = any($1) and r.removed = 0",
    )
    .bind(&ids)
    .fetch_all(pool)
    .await?;
    let mut weighted: BTreeMap<i64, f64> = BTreeMap::new();
    let mut weights: BTreeMap<i64, f64> = BTreeMap::new();
    let mut counts: BTreeMap<i64, i64> = BTreeMap::new();
    for (project_id, outcome, reputation) in rows {
        let weight = crate::field_reports::report_weight(reputation);
        let credit = crate::field_reports::outcome_credit(&outcome);
        *weighted.entry(project_id).or_insert(0.0) += weight * credit;
        *weights.entry(project_id).or_insert(0.0) += weight;
        *counts.entry(project_id).or_insert(0) += 1;
    }
    for (project_id, count) in counts {
        let weight = weights.get(&project_id).copied().unwrap_or(0.0);
        let rate = if weight > 0.0 {
            weighted.get(&project_id).copied().unwrap_or(0.0) / weight
        } else {
            0.0
        };
        out.insert(project_id, (rate, count));
    }
    Ok(out)
}

/// Each project's best arena standing, normalised to 0..1 — `0` when it has no entry in any arena that ranks projects.
///
/// Computed rather than hardcoded: `0` today is a measurement (the live arenas all rank features), and the term starts working
/// the moment an alternatives arena holds project entries. The normalisation uses the arena's own conservative bound, the number
/// the ranking engine already shows to a human, rather than a fixed 3000 that would make every present-day rating look like 0.5.
///
/// # Errors
/// `sqlx::Error`.
pub async fn arena_standings(
    pool: &PgPool,
    project_ids: &[i64],
) -> Result<BTreeMap<i64, f64>, sqlx::Error> {
    let mut out: BTreeMap<i64, f64> = BTreeMap::new();
    if project_ids.is_empty() {
        return Ok(out);
    }
    let ids: Vec<i64> = project_ids.to_vec();
    let rows: Vec<(i64, f64, f64)> = sqlx::query_as(
        "select entity_id, r, rd from arena_entries
          where entity_type = 'project' and entity_id = any($1)",
    )
    .bind(&ids)
    .fetch_all(pool)
    .await?;
    for (project_id, r, rd) in rows {
        let standing = (concord_ranking::conservative(r, rd) / 2000.0).clamp(0.0, 1.0);
        let slot = out.entry(project_id).or_insert(0.0);
        if standing > *slot {
            *slot = standing;
        }
    }
    Ok(out)
}

/// Build the engine's candidate set for a list of projects.
///
/// One function so the three reads above are always taken from the same moment: a candidate built from a rate read before a
/// report landed and an attribute read after it is a candidate that never existed.
///
/// # Errors
/// `sqlx::Error`.
pub async fn candidates(
    pool: &PgPool,
    projects: &[crate::projects::Project],
) -> Result<Vec<concord_finder::Candidate>, sqlx::Error> {
    let ids: Vec<i64> = projects.iter().map(|p| p.id).collect();
    let attrs = attributes(pool, &ids).await?;
    let rates = report_rates(pool, &ids).await?;
    let standings = arena_standings(pool, &ids).await?;
    let mut out = Vec::with_capacity(projects.len());
    for project in projects {
        let mut candidate = concord_finder::Candidate::new(project.id, &project.slug, &project.name);
        if let Some(map) = attrs.get(&project.id) {
            candidate.attrs = map.clone();
        }
        if let Some((rate, count)) = rates.get(&project.id) {
            candidate.report = *rate;
            candidate.reports = *count;
        }
        candidate.arena_r = standings.get(&project.id).copied().unwrap_or(0.0);
        out.push(candidate);
    }
    Ok(out)
}
