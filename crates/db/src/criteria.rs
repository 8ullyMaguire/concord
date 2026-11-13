//! §4.10's criteria and their profiles, and the panels a project page reads.
//!
//! Two orderings are rules here rather than conveniences:
//!
//! * **criteria sort `active DESC, slug`** — an inactive criterion is not hidden, it is listed after the live ones.
//!   Dropping it would make "why did this stop counting?" unanswerable from the page, and sorting by name alone
//!   would interleave the two states so a reader cannot see where the live set ends.
//! * **languages sort `pct DESC, language ASC`** — the tie-break is the language name, so two languages at the same
//!   percentage do not swap places between requests.

use sqlx::PgPool;

/// One ranking criterion.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Criterion {
    /// The row.
    pub id: i64,
    /// The project.
    pub project_id: i64,
    /// A stable slug.
    pub slug: String,
    /// The displayed name.
    pub name: String,
    /// What it means.
    pub description: String,
    /// `higher_is_better` or `lower_is_better`, round-tripped as text.
    pub direction: String,
    /// The weight it carries when nobody sets one.
    pub default_weight: f64,
    /// Whether it counts.
    pub active: bool,
}

/// One saved weight profile.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Profile {
    /// A stable slug.
    pub slug: String,
    /// The displayed name.
    pub name: String,
}

/// One language and its share of the codebase.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Language {
    /// The language's name.
    pub language: String,
    /// Its percentage of the repository.
    pub pct: f64,
}

/// A project's criteria: active first, then by slug.
///
/// # Errors
/// `sqlx::Error`.
pub async fn list(pool: &PgPool, project_id: i64) -> Result<Vec<Criterion>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, i64, String, String, String, String, f64, bool)>(
        // `active` is a bigint in the PostgreSQL baseline (SQLite stored 0/1), so it is compared rather than
        // cast: `(active <> 0)` decodes as bool and the ORDER BY still uses the column, which is the same order.
        "select id, project_id, slug, name, coalesce(description, ''), direction, default_weight,
                (active <> 0)
           from criteria where project_id = $1
          order by active desc, slug",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(id, project_id, slug, name, description, direction, default_weight, active)| Criterion {
            id,
            project_id,
            slug,
            name,
            description,
            direction,
            default_weight,
            active,
        })
        .collect())
}

/// A project's saved weight profiles, by slug.
///
/// # Errors
/// `sqlx::Error`.
pub async fn profiles(pool: &PgPool, project_id: i64) -> Result<Vec<Profile>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (String, String)>(
        "select slug, name from criteria_profiles where project_id = $1 order by slug",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(slug, name)| Profile { slug, name })
        .collect())
}

/// A project's language mix, highest percentage first.
///
/// # Errors
/// `sqlx::Error`.
pub async fn languages(pool: &PgPool, project_id: i64) -> Result<Vec<Language>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (String, f64)>(
        "select language, pct from project_languages where project_id = $1
          order by pct desc, language asc",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(language, pct)| Language { language, pct })
        .collect())
}

/// One list of projects.
#[derive(Debug, Clone, serde::Serialize)]
pub struct ProjectList {
    /// The row.
    pub id: i64,
    /// The project it belongs to, `null` for an instance-wide list — a scope §16 makes first class, and the
    /// reason this is an `Option` rather than `0`.
    pub project_id: Option<i64>,
    /// A stable slug.
    pub slug: String,
    /// The title.
    pub title: String,
    /// The description.
    pub description: String,
    /// Its status.
    pub status: String,
    /// Created, unix seconds.
    pub created_at: f64,
    /// Updated, unix seconds.
    pub updated_at: f64,
}

/// The lists a project page shows: its own first, then the instance-wide ones, each group newest first.
///
/// # Errors
/// `sqlx::Error`.
pub async fn lists(pool: &PgPool, project_id: i64) -> Result<Vec<ProjectList>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, Option<i64>, String, String, String, String, f64, f64)>(
        "select id, project_id, slug, title, coalesce(description, ''), status,
                extract(epoch from created_at)::double precision,
                extract(epoch from updated_at)::double precision
           from lists
          where project_id = $1 or project_id is null
          order by (project_id is null), updated_at desc, id desc",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(
            |(id, project_id, slug, title, description, status, created_at, updated_at)| ProjectList {
                id,
                project_id,
                slug,
                title,
                description,
                status,
                created_at,
                updated_at,
            },
        )
        .collect())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_instance_wide_list_is_a_scope_and_not_a_zero() {
        // The Go bug this exists to prevent: `project_id` as an int64 turned NULL into a scan failure, so an
        // instance-wide list was unreachable from the API while every `project_id=?` query looked healthy.
        let scoped = ProjectList {
            id: 1,
            project_id: Some(7),
            slug: "a".to_string(),
            title: "A".to_string(),
            description: String::new(),
            status: "open".to_string(),
            created_at: 0.0,
            updated_at: 0.0,
        };
        let global = ProjectList { project_id: None, ..scoped.clone() };
        assert_eq!(serde_json::to_value(&global).unwrap()["project_id"], serde_json::Value::Null);
        assert_eq!(serde_json::to_value(&scoped).unwrap()["project_id"], 7);
    }
}
