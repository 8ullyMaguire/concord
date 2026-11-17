//! Project documents (step 3, ADR 0014): list, read, upsert and delete.
//!
//! The reads lived near the top of `lib.rs` and the writes inside the §5.7
//! source section; both halves now sit in one module with one `routes()`.

use axum::{
    extract::{DefaultBodyLimit, Path, Query, State},
    http::HeaderMap,
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::Value;

use crate::{internal, readable_project_segment, require_role, AppState};

async fn list_documents(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Query(query): Query<DocumentListQuery>,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let documents = concord_db::projects::list_documents(&state.pool, project.id)
        .await
        .map_err(internal)?;
    let mut value = serde_json::to_value(documents).map_err(internal)?;
    // `?fields=meta` drops the bodies. The default shape is unchanged, which matters: the Go route returns bodies and the corpus replay
    // compares this response against the recorded one. What the browser needs is different from what the corpus needs — one project's
    // documents are 11.7 MB of prose, and the documents page measured 30+ seconds before it drew anything because it asked for all of it
    // to display one document. The reader fetches the single document at `/documents/{id}` instead.
    if query.fields.as_deref() == Some("meta") {
        if let Some(items) = value.as_array_mut() {
            for item in items {
                if let Some(object) = item.as_object_mut() {
                    object.remove("body");
                }
            }
        }
    }
    Ok(Json(value))
}

/// `?fields=` on the document list. Only `meta` is understood, and anything else is ignored rather than refused: an unknown field set is a
/// client asking for something this route does not do, and answering with the full document is the honest reading of that request.
#[derive(Debug, Deserialize, Default)]
struct DocumentListQuery {
    fields: Option<String>,
}

/// `GET /api/v1/projects/{slug}/documents/{doc_id}` — one document, body and all.
///
/// By integer id, because that is the Go route's key. A non-integer is a 400 with the Go handler's
/// own message rather than a 404: the path is well-formed and the id is not.
async fn get_document(
    State(state): State<AppState>,
    Path((slug, document)): Path<(String, String)>,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let Ok(id) = document.parse::<i64>() else {
        return Err(ApiError::invalid_request("document id must be an integer"));
    };
    match concord_db::projects::document(&state.pool, project.id, id)
        .await
        .map_err(internal)?
    {
        Some(doc) => Ok(Json(serde_json::to_value(doc).map_err(internal)?)),
        None => Err(ApiError::not_found(format!("document {id}"))),
    }
}

/// The document body limit: 8 MB, the reference's own, raised for specifications rather than comments.
const DOCUMENT_BODY_LIMIT: usize = 8 << 20;

/// What a document write sends.
#[derive(serde::Deserialize, Default)]
struct PutDocumentRequest {
    #[serde(default)]
    kind: String,
    #[serde(default)]
    slug: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    body: String,
}

/// `PUT /projects/{id}/documents` — create or replace the document at `(kind, slug)`.
///
/// Writing needs the **contributor** role, not merely an account: a document states what a project *is*, so it is not something
/// any signed-in visitor gets to rewrite. The reply is the stored document with its new `revision` — the one number that tells a
/// reader whether the spec in front of them is the one that was agreed.
async fn put_document(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<PutDocumentRequest>,
) -> Result<Json<serde_json::Value>, ApiError> {
    // Visibility before anything is read or written: a private project's spec is exactly the content that must not leak, and
    // this handler resolves its project by slug rather than through a project-id guard.
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let document = concord_db::document_writes::put(
        &state.pool,
        project.id,
        user.id,
        &body.kind,
        &body.slug,
        &body.title,
        &body.body,
    )
    .await
    .map_err(document_refusal)?;
    let _ = sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,$2,'put_document','project_document',$3,$4,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .bind(document.id)
    .bind(format!("{}/{}", document.kind, document.slug))
    .execute(&state.pool)
    .await;
    Ok(Json(serde_json::to_value(&document).map_err(internal)?))
}

/// `DELETE /projects/{id}/documents/{doc}` — remove a document. **Maintainer**, not contributor.
async fn delete_document(
    State(state): State<AppState>,
    Path((project_segment, document)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let Ok(id) = document.parse::<i64>() else {
        return Err(ApiError::invalid_request("document id must be an integer"));
    };
    concord_db::document_writes::delete(&state.pool, project.id, id)
        .await
        .map_err(document_refusal)?;
    let _ = sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,$2,'delete_document','project_document',$3,null,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .bind(id)
    .execute(&state.pool)
    .await;
    Ok(Json(serde_json::json!({ "status": "deleted" })))
}

/// A document refusal, as the status it deserves. An unknown kind is a 400 with the list of kinds, because the caller's next
/// question is which ones exist.
fn document_refusal(refused: concord_db::document_writes::Refused) -> ApiError {
    use concord_db::document_writes::Refused;
    match refused {
        Refused::Invalid(reason) => ApiError::invalid_request(reason),
        Refused::NotFound => ApiError::not_found("no such document"),
        Refused::Database(error) => internal(error),
    }
}



/// Both document paths, typed on `AppState` (D-18). The list path keeps the
/// 8 MB body limit in the same MethodRouter as the old chain declared it.
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/documents",
            axum::routing::get(list_documents)
                .put(put_document)
                .layer(DefaultBodyLimit::max(DOCUMENT_BODY_LIMIT)),
        )
        .route(
            "/api/v1/projects/{slug}/documents/{document}",
            axum::routing::get(get_document).delete(delete_document),
        )
}
