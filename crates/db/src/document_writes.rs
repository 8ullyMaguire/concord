//! Project documents: the README, spec, plan, ADR and wiki that explain what a project *is*.
//!
//! These lived only in a repository somewhere else until this store existed, and two of the rules here come from that:
//!
//! * **a document is versioned by revision, not overwritten.** `put` at an existing `(kind, slug)` bumps `revision` and keeps
//!   the row's identity, so a link to `/documents/41` keeps resolving after the spec is rewritten. Delete-and-insert would
//!   break every such link and every comment threaded on the id;
//! * **a write needs the contributor role and a delete needs the maintainer** — a document states what the project is, so
//!   rewriting one is not something any signed-in account does, and removing one is not something a contributor does.
//!
//! The kinds are enumerated rather than free text: `kind` is the first half of the document's address (`spec/architecture`), and
//! a typo in it makes a document that no page will ever list.

use sqlx::PgPool;

/// The kinds a document may be. Ordered the way the reference lists them, which is the order its refusal message prints.
pub const KINDS: [&str; 7] = ["readme", "spec", "plan", "wiki", "adr", "changelog", "scout"];

/// Whether `kind` is one of [`KINDS`].
#[must_use]
pub fn is_kind(kind: &str) -> bool {
    KINDS.contains(&kind)
}

/// Why a document write was refused.
#[derive(Debug)]
pub enum Refused {
    /// The request describes no document that can be stored.
    Invalid(String),
    /// No such document in this project.
    NotFound,
    /// The database said no.
    Database(sqlx::Error),
}

impl From<sqlx::Error> for Refused {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

/// Create or replace the document at `(kind, slug)`, returning it with its new revision.
///
/// The upsert is one statement, so two writers racing cannot both read revision 3 and both write 4: the increment happens in the
/// database against the row it is updating. `author_id` moves to the last writer, and `created_at` stays where it was — the
/// document was created once.
///
/// # Errors
/// [`Refused::Invalid`] for an unknown kind or an empty slug.
pub async fn put(
    pool: &PgPool,
    project_id: i64,
    author_id: i64,
    kind: &str,
    slug: &str,
    title: &str,
    body: &str,
) -> Result<crate::projects::Document, Refused> {
    let kind = kind.trim().to_ascii_lowercase();
    let slug = slug.trim();
    if project_id == 0 || author_id == 0 {
        return Err(Refused::Invalid(
            "a document needs a project and an author".to_string(),
        ));
    }
    if !is_kind(&kind) {
        return Err(Refused::Invalid(format!(
            "kind must be one of {}",
            KINDS.join(", ")
        )));
    }
    if slug.is_empty() {
        return Err(Refused::Invalid("empty document slug".to_string()));
    }
    // A README has no meaningful title; fall back to the slug rather than storing an empty display name.
    let title = if title.trim().is_empty() {
        slug.to_string()
    } else {
        title.trim().to_string()
    };
    let id: i64 = sqlx::query_scalar(
        // `public_id` is NOT NULL with no default on every API-facing table in this schema (the estate profile's stable
        // handle), so the writer supplies one — and only on insert: the on-conflict branch must not rewrite it, or a
        // document's public address would change every time somebody fixed a typo.
        "insert into project_documents
             (project_id, kind, slug, title, body, revision, author_id, created_at, updated_at, public_id)
         values ($1,$2,$3,$4,$5,1,$6,now(),now(),$7)
         on conflict (project_id, kind, slug) do update set
             title = excluded.title,
             body = excluded.body,
             revision = project_documents.revision + 1,
             author_id = excluded.author_id,
             updated_at = excluded.updated_at
         returning id",
    )
    .bind(project_id)
    .bind(&kind)
    .bind(slug)
    .bind(&title)
    .bind(body)
    .bind(author_id)
    .bind(ulid::Ulid::new().to_string())
    .fetch_one(pool)
    .await?;
    crate::projects::document(pool, project_id, id)
        .await?
        .ok_or(Refused::NotFound)
}

/// Remove a document from **this** project.
///
/// The project predicate is in the `where`, not checked beforehand: a delete that first reads and then writes can lose a race
/// with the row moving, and the predicate is what makes the id meaningless outside its project.
///
/// # Errors
/// [`Refused::NotFound`] when there is no such document here.
pub async fn delete(pool: &PgPool, project_id: i64, document_id: i64) -> Result<(), Refused> {
    let affected = sqlx::query("delete from project_documents where id = $1 and project_id = $2")
        .bind(document_id)
        .bind(project_id)
        .execute(pool)
        .await?
        .rows_affected();
    if affected == 0 {
        return Err(Refused::NotFound);
    }
    Ok(())
}
