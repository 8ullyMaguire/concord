//! Where each project's upstream repository lives: the read and the two writes behind `forgeseed`.
//!
//! The column is **stated, never derived** — this is not a function that guesses a URL from a slug. It exists because `repo_host` is the
//! switch the health sweep dispatches on, and an empty string is a *different answer* from a missing row: "we have not stated this one" has
//! to be written down rather than inferred from absence, or the next person cannot tell a skip from an oversight.
//!
//! Clearing is a write, not a delete: a project that becomes a skip must lose the URL it used to have. Found on the instance the port
//! replaces — a remote started answering 404, the mapping was changed to a skip, and the row kept its `repo_url`, so the hourly ingest kept
//! failing against a URL the mapping no longer contained. "Not stated" has to mean nothing is set.

use sqlx::PgPool;

/// One project's stated repository.
#[derive(Debug, Clone)]
pub struct ProjectRepo {
    /// The project's slug.
    pub slug: String,
    /// The canonical URL, empty when none is stated.
    pub url: String,
    /// The forge family (`github`, `forgejo`, `codeberg`), empty when none is stated.
    pub host: String,
}

/// Every project the catalogue can offer work for, ordered by slug.
///
/// # Errors
/// `sqlx::Error`.
pub async fn project_slugs(pool: &PgPool) -> Result<Vec<String>, sqlx::Error> {
    sqlx::query_scalar("select slug from projects order by slug")
        .fetch_all(pool)
        .await
}

/// Every project with its stated repository, including the ones with none — the caller needs the difference between "not stated" and
/// "not listed".
///
/// # Errors
/// `sqlx::Error`.
pub async fn projects_with_repo(pool: &PgPool) -> Result<Vec<ProjectRepo>, sqlx::Error> {
    let rows: Vec<(String, Option<String>, Option<String>)> =
        sqlx::query_as("select slug, repo_url, repo_host from projects order by slug")
            .fetch_all(pool)
            .await?;
    Ok(rows
        .into_iter()
        .map(|(slug, url, host)| ProjectRepo {
            slug,
            url: url.unwrap_or_default(),
            host: host.unwrap_or_default(),
        })
        .collect())
}

/// State a project's repository. Returns `false` when the project does not exist.
///
/// # Errors
/// `sqlx::Error`.
pub async fn set_repo(pool: &PgPool, slug: &str, url: &str, host: &str) -> Result<bool, sqlx::Error> {
    let done = sqlx::query("update projects set repo_url = $1, repo_host = $2 where slug = $3")
        .bind(url)
        .bind(host)
        .bind(slug)
        .execute(pool)
        .await?;
    Ok(done.rows_affected() > 0)
}

/// Clear a project's repository — the write behind a skip. Returns `false` when the project does not exist.
///
/// Both columns go to `NULL`, not to `''`: an empty string is a value a `coalesce` will happily return, and the difference between "no
/// repository" and "a repository whose URL is the empty string" is the difference between a skip and a bug.
///
/// # Errors
/// `sqlx::Error`.
pub async fn clear_repo(pool: &PgPool, slug: &str) -> Result<bool, sqlx::Error> {
    let done = sqlx::query("update projects set repo_url = null, repo_host = null where slug = $1")
        .bind(slug)
        .execute(pool)
        .await?;
    Ok(done.rows_affected() > 0)
}
