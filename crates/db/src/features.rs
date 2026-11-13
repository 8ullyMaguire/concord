//! S2's features: the rows the ranking is computed over.
//!
//! The shape is the Go `store.Feature` field for field, including which fields are pointers: `impact`,
//! `effort_score`, `impact_ratio` and `milestone_id` are nullable and the Go struct makes them
//! pointers, so a row without a milestone serialises `"milestone_id": null` rather than leaving the
//! key out — and a backlog is exactly the set of features with no milestone, which is a real answer.

use serde::Serialize;
use sqlx::PgPool;

use crate::projects::ser_unix;

/// One feature, as `/api/v1/projects/{slug}/features` and `/features/{id}` return it.
#[derive(Debug, Clone, Serialize)]
pub struct Feature {
    pub id: i64,
    pub project_id: i64,
    pub author_id: i64,
    pub title: String,
    pub body: String,
    pub effort: String,
    // bigint in the baseline (SQLite INTEGER), so i64 here: decoding an INT8 column into i32 fails
    // with "mismatched types" at runtime and not at build time, which is how a schema mapping
    // difference becomes a 500.
    pub impact: Option<i64>,
    pub effort_score: Option<i64>,
    // Every float here is printed by Go's encoder the way `ser_unix` prints: `350`, not `350.0`.
    // serde_json keeps the decimal, and the corpus compares what the wire carries.
    #[serde(serialize_with = "ser_go_float_opt")]
    pub impact_ratio: Option<f64>,
    pub status: String,
    #[serde(serialize_with = "ser_unix")]
    pub elo_r: f64,
    #[serde(serialize_with = "ser_unix")]
    pub elo_rd: f64,
    #[serde(serialize_with = "ser_unix")]
    pub elo_vol: f64,
    #[serde(serialize_with = "ser_unix")]
    pub strategic_weight: f64,
    pub milestone_id: Option<i64>,
    pub milestone_position: i64,
    #[serde(serialize_with = "ser_unix")]
    pub created_at: f64,
    #[serde(serialize_with = "ser_unix")]
    pub updated_at: f64,
}

const FEATURE_COLUMNS: &str = "id, project_id, author_id, title, body, effort, impact, effort_score, \
     impact_ratio, status, elo_r, elo_rd, elo_vol, strategic_weight, \
     extract(epoch from created_at)::double precision, \
     extract(epoch from updated_at)::double precision, milestone_id, milestone_position";

/// The same rule as [`ser_unix`], for a field that may be absent: Go prints `nil` as `null` and a
/// whole `float64` without a decimal point, and `Option<f64>` needs its own serializer to say so.
fn ser_go_float_opt<S: serde::Serializer>(
    value: &Option<f64>,
    serializer: S,
) -> Result<S::Ok, S::Error> {
    match value {
        Some(number) => ser_unix(number, serializer),
        None => serializer.serialize_none(),
    }
}

fn feature_from_row(row: &sqlx::postgres::PgRow) -> Result<Feature, sqlx::Error> {
    use sqlx::Row;
    Ok(Feature {
        id: row.try_get(0)?,
        project_id: row.try_get(1)?,
        author_id: row.try_get(2)?,
        title: row.try_get(3)?,
        body: row.try_get(4)?,
        effort: row.try_get(5)?,
        impact: row.try_get(6)?,
        effort_score: row.try_get(7)?,
        impact_ratio: row.try_get(8)?,
        status: row.try_get(9)?,
        elo_r: row.try_get(10)?,
        elo_rd: row.try_get(11)?,
        elo_vol: row.try_get(12)?,
        strategic_weight: row.try_get(13)?,
        milestone_id: row.try_get(16)?,
        milestone_position: row.try_get(17)?,
        created_at: row.try_get(14)?,
        updated_at: row.try_get(15)?,
    })
}

/// Every feature of a project, newest first, optionally filtered by status.
///
/// # Errors
/// [`sqlx::Error`] when the read fails. A project with no features is an empty list, not an error:
/// the corpus records `[]` and an error there would be a different answer to the same question.
pub async fn list_features(
    pool: &PgPool,
    project_id: i64,
    status: &str,
) -> Result<Vec<Feature>, sqlx::Error> {
    // `, id asc` is not a preference: `created_at` is REAL seconds and 16 pairs of features in the
    // live data share one, so the tiebreak decides what the list actually looks like. Measured on
    // the recorded body — 16 of 16 tied pairs list the smaller id first, which is SQLite returning
    // rows in rowid order inside a tie. PostgreSQL has no such order, so it is written down.
    let sql = if status.is_empty() {
        format!(
            "select {FEATURE_COLUMNS} from features where project_id = $1 \
             order by created_at desc, id asc"
        )
    } else {
        format!(
            "select {FEATURE_COLUMNS} from features where project_id = $1 and status = $2 \
             order by created_at desc, id asc"
        )
    };
    let mut query = sqlx::query(&sql).bind(project_id);
    if !status.is_empty() {
        query = query.bind(status);
    }
    query
        .fetch_all(pool)
        .await?
        .iter()
        .map(feature_from_row)
        .collect()
}

/// One feature by id, `None` when it does not exist.
///
/// The route that calls this resolves the project first and refuses a feature whose `project_id`
/// differs: reading by the numeric id alone lets an anonymous caller count private projects' work,
/// and the list endpoint beside it answer 404 for the same project.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn feature(pool: &PgPool, id: i64) -> Result<Option<Feature>, sqlx::Error> {
    let sql = format!("select {FEATURE_COLUMNS} from features where id = $1");
    sqlx::query(&sql)
        .bind(id)
        .fetch_optional(pool)
        .await?
        .map(|row| feature_from_row(&row))
        .transpose()
}
