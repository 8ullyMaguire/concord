//! Scout: the answer to "what should I build on" (step 3, ADR 0014).
//!
//! The last section of `lib.rs`, moved whole: report handler, coverage
//! application helper and query struct.

use axum::{
    extract::{Query, State},
    http::header::HeaderMap,
    Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::Value;

use crate::{bearer_user, internal, AppState};

// ── Scout: the answer to "what should I build on" ───────────────────────────────────────────────────────────────

/// `GET /scout?idea=…&max=…&exclude_licenses=…`
///
/// **Not scoped to a project**, deliberately: the answer spans the catalogue, and scoping it to one project would make the feature
/// meaningless. Access control is per candidate inside the read, so a project the viewer cannot read is filtered out, not counted in
/// `candidates_considered`, and contributes nothing to any coverage state — which is what stops the response being an oracle for which
/// private projects exist.
///
/// **Health is read live, never from `project_metrics.health_score`.** That column is 0 for every project on this instance, so a rule keyed
/// on it calls the whole catalogue abandoned; the store returns `Some` only when something was actually measured, and `None` is the whole
/// difference between `avoid` and `adopt`.
async fn scout_report(
    State(state): State<AppState>,
    headers: HeaderMap,
    Query(query): Query<ScoutQuery>,
) -> Result<Json<Value>, ApiError> {
    const MAX_IDEA: usize = 4096;
    let idea = query.idea.unwrap_or_default().trim().to_string();
    if idea.is_empty() {
        return Err(ApiError::invalid_request("an idea is required"));
    }
    // Bytes, not characters: the limit is about how much text one request may ask the decomposer to chew through, and the decomposer's
    // cost is per UTF-8 byte.
    if idea.len() > MAX_IDEA {
        return Err(ApiError::invalid_request(format!(
            "the idea is too long (limit {MAX_IDEA} bytes)"
        )));
    }
    let max_verdicts = match query.max.as_deref().map(str::trim) {
        None | Some("") => 0,
        Some(raw) => match raw.parse::<i64>() {
            Ok(value) if value > 0 => usize::try_from(value).unwrap_or(usize::MAX),
            Ok(_) => return Err(ApiError::invalid_request("max must be positive")),
            Err(_) => return Err(ApiError::invalid_request("max must be a number")),
        },
    };
    // An empty or absent parameter means NO constraint — not "exclude everything", which is why this yields an empty list rather than a
    // list holding one empty string.
    let constraints: Vec<String> = query
        .exclude_licenses
        .unwrap_or_default()
        .split(',')
        .map(str::trim)
        .filter(|part| !part.is_empty())
        .map(str::to_string)
        .collect();

    let viewer = bearer_user(&state, &headers).await?;
    let catalog = concord_db::scout_reads::catalog(&state.pool)
        .await
        .map_err(internal)?;
    let candidates = concord_db::scout_reads::candidates(&state.pool, viewer.as_ref().map(|user| user.id))
        .await
        .map_err(internal)?;

    let mut capabilities = concord_scout::decompose::decompose(&idea, &catalog);
    apply_coverage(&state, &mut capabilities, &candidates).await?;

    let mut health = std::collections::BTreeMap::new();
    let mut pain = std::collections::BTreeMap::new();
    for candidate in &candidates {
        // `Some` means "measured"; `None` is "no telemetry", and reporting it as 0 would call the project abandoned.
        let measured = concord_db::ops_reads::project_health(&state.pool, candidate.project_id)
            .await
            .map_err(internal)?;
        // `scout-health-assumed` reports "no telemetry" as a measured zero, which is the exact defect that calls the whole catalogue
        // abandoned on this instance — the reference's own `project_metrics.health_score` is 0 everywhere.
        let assumed = std::env::var("CONCORD_MUTANT")
            .unwrap_or_default()
            .split(',')
            .any(|armed| armed.trim() == "scout-health-assumed");
        health.insert(
            candidate.project_id,
            concord_scout::Health {
                value: measured.unwrap_or(0.0),
                known: measured.is_some() || assumed,
            },
        );
        let known = concord_db::scout_reads::pain(&state.pool, candidate.project_id, 5)
            .await
            .map_err(internal)?;
        if !known.is_empty() {
            pain.insert(candidate.project_id, known);
        }
    }

    // `scout-ignore-license` drops the caller's constraints, so the report neither refuses the licence nor echoes that it applied one.
    let constraints = if std::env::var("CONCORD_MUTANT")
        .unwrap_or_default()
        .split(',')
        .any(|armed| armed.trim() == "scout-ignore-license")
    {
        Vec::new()
    } else {
        constraints
    };
    let report = concord_scout::classify(
        &concord_scout::Input { idea, capabilities, candidates, health, pain },
        concord_scout::Options {
            health_floor: 0.0,
            dead_floor: 0.0,
            license_constraints: constraints,
            max_verdicts,
        },
    );
    Ok(Json(serde_json::to_value(report).map_err(internal)?))
}

/// Fill each decomposed capability's three-state coverage from the assertions across the **visible** candidate set.
///
/// The counts come from `capability_confirmations`, not from the candidates' values. Those are different questions — what a project *claims*
/// versus whether anyone *backed* it — and reading coverage out of the values makes `covered` unreachable, with every capability coming back
/// `partial`.
///
/// A proposed capability has no catalogue row and so nothing can be asserted about it: `open` is the honest state, and it is more useful to
/// a reader than an empty field.
async fn apply_coverage(
    state: &AppState,
    capabilities: &mut [concord_scout::Capability],
    candidates: &[concord_scout::Candidate],
) -> Result<(), ApiError> {
    let ids = concord_db::scout_reads::ids_of(candidates);
    let counts = concord_db::scout_reads::state_counts(&state.pool, &ids)
        .await
        .map_err(internal)?;
    for capability in capabilities {
        if !capability.in_catalog {
            capability.coverage = "open".to_string();
            continue;
        }
        let count = counts.get(&capability.key).copied().unwrap_or_default();
        capability.coverage = if count.confirmed > 0 {
            "covered".to_string()
        } else if count.total() > 0 {
            "partial".to_string()
        } else {
            "open".to_string()
        };
    }
    Ok(())
}

#[derive(Debug, Deserialize)]
struct ScoutQuery {
    idea: Option<String>,
    max: Option<String>,
    exclude_licenses: Option<String>,
}

/// The scout route, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new().route("/api/v1/scout", axum::routing::get(scout_report))
}
