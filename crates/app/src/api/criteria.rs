//! Criteria readers, writes, profiles and charter (step 3, ADR 0014).
//! get_charter stays root-owned because another module imports it (D-41/D-48).

use axum::{
    extract::{Path, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;
use crate::{
    bearer_user, get_charter, internal, project_by_id, readable_project,
    readable_project_segment, require_role, write_audit, AppState,
};

// ── criteria writes (§4.10, §6.5) ──────────────────────────────────────────────────────────────────────────────────

fn criteria_refusal(refused: concord_db::criteria_writes::Refused) -> ApiError {
    use concord_db::criteria_writes::Refused;
    match refused {
        Refused::Invalid(message) => ApiError::invalid_request(message),
        Refused::Scope => ApiError::invalid_request(
            "that criterion or feature belongs to another project".to_string(),
        ),
        Refused::NotFound => ApiError::not_found("no such criterion"),
    }
}

/// `GET /api/v1/projects/{id}/criteria` — active first, then slug.
async fn list_criteria(
    State(state): State<AppState>,
    Path(project): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
    let project = readable_project(&state, project_id, &headers).await?;
    let rows = concord_db::criteria::list(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

/// `GET /api/v1/projects/{id}/criteria-profiles`
async fn list_criteria_profiles(
    State(state): State<AppState>,
    Path(project): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
    let project = readable_project(&state, project_id, &headers).await?;
    let rows = concord_db::criteria::profiles(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

/// `POST /projects/{id}/criteria` — add one.
///
/// The two defaults are silent because a caller leaving a field out is not making a mistake: an absent `direction` is
/// `higher_is_better`, and a non-positive `default_weight` becomes `1.0` — a criterion that counts for zero would be a
/// criterion that is off, which is a different thing with its own route. An explicit **bad** direction is refused,
/// because that one changes what the ranking means.
async fn create_criterion(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let slug = body.get("slug").and_then(|v| v.as_str()).unwrap_or_default();
    let name = body.get("name").and_then(|v| v.as_str()).unwrap_or_default();
    let description = body.get("description").and_then(|v| v.as_str()).unwrap_or_default();
    let direction = body.get("direction").and_then(|v| v.as_str()).unwrap_or_default();
    let default_weight = body
        .get("default_weight")
        .and_then(serde_json::Value::as_f64)
        .unwrap_or(1.0);
    let criterion = concord_db::criteria_writes::create(
        &state.pool,
        project.id,
        user.id,
        slug,
        name,
        description,
        direction,
        default_weight,
    )
    .await
    .map_err(internal)?
    .map_err(criteria_refusal)?;
    Ok((StatusCode::CREATED, Json(serde_json::to_value(criterion).map_err(internal)?)))
}

/// `PUT /projects/{id}/criteria/{criterion}/active` — switch it off without deleting it.
///
/// Inactive is **not** hidden: the read sorts `active DESC, slug`, so a criterion that stopped counting stays visible
/// after the live set. Deleting it would make "why did this stop counting?" unanswerable from the page.
async fn set_criterion_active(
    State(state): State<AppState>,
    Path((project_segment, criterion)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let id: i64 = criterion
        .parse()
        .map_err(|_| ApiError::not_found("no such criterion"))?;
    let active = body
        .get("active")
        .and_then(serde_json::Value::as_bool)
        .unwrap_or(false);
    concord_db::criteria_writes::set_active(&state.pool, project.id, id, active)
        .await
        .map_err(internal)?
        .map_err(criteria_refusal)?;
    let updated = concord_db::criteria_writes::get(&state.pool, project.id, id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such criterion"))?;
    Ok(Json(serde_json::to_value(updated).map_err(internal)?))
}

/// `POST /projects/{id}/criteria/{criterion}/vote` — one comparison, and the two ratings it moved.
///
/// The response carries **both ratings** because a voter should see what their comparison did, not a bare 200. A neutral
/// outcome (`skip`, `both`, `neither`) is accepted and recorded and moves nothing — abstention is data — so its response
/// simply reports the unchanged ratings.
async fn cast_criterion_vote(
    State(state): State<AppState>,
    Path((project_segment, criterion)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to vote"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let id: i64 = criterion
        .parse()
        .map_err(|_| ApiError::not_found("no such criterion"))?;
    let feature_a = body
        .get("feature_a")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or_default();
    let feature_b = body
        .get("feature_b")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or_default();
    let outcome = body.get("outcome").and_then(|v| v.as_str()).unwrap_or_default();
    let weight = body
        .get("weight")
        .and_then(serde_json::Value::as_f64)
        .unwrap_or(1.0);
    concord_db::criteria_writes::cast_vote(
        &state.pool,
        project.id,
        id,
        user.id,
        feature_a,
        feature_b,
        outcome,
        weight,
        0.5,
    )
    .await
    .map_err(internal)?
    .map_err(criteria_refusal)?;
    let mut out = serde_json::Map::new();
    for (key, feature) in [("feature_a", feature_a), ("feature_b", feature_b)] {
        if let Some(rating) = concord_db::criteria_writes::rating(&state.pool, project.id, id, feature)
            .await
            .map_err(internal)?
        {
            out.insert(key.to_string(), serde_json::to_value(rating).map_err(internal)?);
        }
    }
    Ok(Json(serde_json::Value::Object(out)))
}

/// `POST /projects/{id}/criteria-profiles` — save a weight profile.
///
/// `weights` is keyed by criterion id as a **string**, because JSON object keys are strings and the panel builds them
/// from ids; a key that is not a number is a 400 naming the key rather than a silent skip, since a skipped weight is a
/// profile that does not mean what its author wrote. Re-saving an existing slug **replaces** the weights wholesale.
async fn save_criteria_profile(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to save a profile"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let slug = body.get("slug").and_then(|v| v.as_str()).unwrap_or_default();
    let name = body.get("name").and_then(|v| v.as_str()).unwrap_or_default();
    let mut weights = Vec::new();
    if let Some(map) = body.get("weights").and_then(|v| v.as_object()) {
        for (key, value) in map {
            let criterion_id: i64 = key
                .parse()
                .map_err(|_| ApiError::invalid_request(format!("weights keys must be criterion ids: {key}")))?;
            let weight = value.as_f64().unwrap_or_default();
            weights.push(concord_db::criteria_writes::Weight {
                criterion_id,
                weight,
            });
        }
    }
    let id = concord_db::criteria_writes::save_profile(
        &state.pool, project.id, user.id, slug, name, &weights,
    )
    .await
    .map_err(internal)?
    .map_err(criteria_refusal)?;
    Ok((StatusCode::CREATED, Json(serde_json::json!({ "id": id, "slug": slug }))))
}


/// `PUT /projects/{id}/charter` — the numbers every threshold in the system reads.
///
/// **Authenticate before resolving the project**, so an anonymous caller gets 401 whether or not the slug exists; the
/// reverse order makes the status code depend on whether the slug is registered, which is a slug oracle.
///
/// The four bounds are refusals, not clamps, and each one guards a switch that can otherwise be set to a value which
/// silently disables a mechanism for the whole project — the failure mode where nothing errors and nothing works. They
/// live in the store beside the `UPDATE`, because a second copy here would be the one that goes stale.
///
/// **A partial body is filled from the current charter rather than zeroed.** The reference decodes the body straight
/// into its `Charter` and an omitted `solution_call_confidence` arrives as `0`, which fails its own bound and answers
/// "must be between 0 and 1 exclusive" to a request that never mentioned the field. The settings panel posts the whole
/// form, so no real client sees a difference; a curl with three fields now gets the sane result. Decision recorded in the
/// ledger, dated, rather than left as a surprise.
async fn update_charter(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let mut charter = concord_db::charter::charter(&state.pool, project.id)
        .await
        .map_err(internal)?;
    apply_charter_fields(&mut charter, &body);
    concord_db::charter::set_charter(&state.pool, project.id, &charter)
        .await
        .map_err(internal)?
        .map_err(|refused| ApiError::invalid_request(refused.0))?;
    let detail = body
        .as_object()
        .map(|map| map.keys().cloned().collect::<Vec<_>>().join(","))
        .unwrap_or_default();
    write_audit(&state, Some(project.id), user.id, "charter.update", "project", project.id, &detail).await?;
    Ok(Json(serde_json::json!({ "status": "charter updated" })))
}

/// Overlay whichever charter fields the body carries, leaving the rest as they were.
fn apply_charter_fields(charter: &mut concord_governance::Charter, body: &serde_json::Value) {
    macro_rules! floats {
        ($($field:ident),* $(,)?) => {
            $(
                if let Some(value) = body.get(stringify!($field)).and_then(serde_json::Value::as_f64) {
                    charter.$field = value;
                }
            )*
        };
    }
    macro_rules! ints {
        ($($field:ident),* $(,)?) => {
            $(
                if let Some(value) = body.get(stringify!($field)).and_then(serde_json::Value::as_i64) {
                    charter.$field = value;
                }
            )*
        };
    }
    macro_rules! bools {
        ($($field:ident),* $(,)?) => {
            $(
                if let Some(value) = body.get(stringify!($field)).and_then(serde_json::Value::as_bool) {
                    charter.$field = value;
                }
            )*
        };
    }
    floats!(
        quorum_ratio,
        consent_ratio,
        support_ratio_min,
        override_ratio,
        vote_window_days,
        merge_quorum_ratio,
        lam,
        mu,
        theme_lifetime_days,
        pain_halflife_days,
        rep_halflife_days,
        glicko_tau,
        vote_weight_cap,
        solution_call_confidence,
        solution_stable_hours,
    );
    ints!(
        quorum_min,
        merge_quorum_min,
        wip_in_progress,
        wip_review,
        solution_call_min_voters,
    );
    bools!(merge_requires_quorum, require_reviewer_approval);
}



/// Criteria, profile and charter routes, typed on `AppState` (D-18).
/// GET+POST pairs stay in one MethodRouter (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{project_id}/criteria",
            axum::routing::get(list_criteria).post(create_criterion),
        )
        .route(
            "/api/v1/projects/{project_id}/criteria/",
            axum::routing::get(list_criteria),
        )
        .route(
            "/api/v1/projects/{project_id}/criteria-profiles",
            axum::routing::get(list_criteria_profiles).post(save_criteria_profile),
        )
        .route(
            "/api/v1/projects/{project_id}/criteria-profiles/",
            axum::routing::get(list_criteria_profiles),
        )
        .route(
            "/api/v1/projects/{project_id}/criteria/{criterion}/active",
            axum::routing::put(set_criterion_active),
        )
        .route(
            "/api/v1/projects/{project_id}/criteria/{criterion}/vote",
            axum::routing::post(cast_criterion_vote),
        )
        .route(
            "/api/v1/projects/{project_id}/charter",
            axum::routing::get(get_charter).put(update_charter),
        )
}
