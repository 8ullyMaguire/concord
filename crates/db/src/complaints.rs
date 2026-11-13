//! S2's complaints: what the work is for.
//!
//! `merged_into` is the one field here whose *absence* is part of the shape: the Go struct tags it
//! `json:"merged_into,omitempty"`, so an unmerged complaint has no key at all, while a feature's
//! `milestone_id` (also nullable) is present and `null`. Two nullable fields, two conventions, both
//! visible in the recorded bodies — and getting this backwards is invisible until a client asks for
//! `merged_into` and decides the complaint was merged.

use serde::Serialize;
use sqlx::PgPool;

use crate::projects::ser_unix;

/// One complaint, as `/api/v1/projects/{slug}/complaints` returns it.
#[derive(Debug, Clone, Serialize)]
pub struct Complaint {
    pub id: i64,
    pub project_id: i64,
    pub author_id: i64,
    pub title: String,
    pub body: String,
    pub severity: i64,
    #[serde(serialize_with = "ser_unix")]
    pub frequency: f64,
    #[serde(serialize_with = "ser_unix")]
    pub strategic_multiplier: f64,
    pub status: String,
    /// Absent, not null, when the complaint is not merged — `omitempty` in the Go struct.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub merged_into: Option<i64>,
    #[serde(serialize_with = "ser_unix")]
    pub created_at: f64,
    #[serde(serialize_with = "ser_unix")]
    pub updated_at: f64,
}

const COMPLAINT_COLUMNS: &str = "id, project_id, author_id, title, body, severity, frequency, \
     strategic_multiplier, status, merged_into, \
     extract(epoch from created_at)::double precision, \
     extract(epoch from updated_at)::double precision";

fn complaint_from_row(row: &sqlx::postgres::PgRow) -> Result<Complaint, sqlx::Error> {
    use sqlx::Row;
    Ok(Complaint {
        id: row.try_get(0)?,
        project_id: row.try_get(1)?,
        author_id: row.try_get(2)?,
        title: row.try_get(3)?,
        body: row.try_get(4)?,
        severity: row.try_get(5)?,
        frequency: row.try_get(6)?,
        strategic_multiplier: row.try_get(7)?,
        status: row.try_get(8)?,
        merged_into: row.try_get(9)?,
        created_at: row.try_get(10)?,
        updated_at: row.try_get(11)?,
    })
}

/// Every complaint of a project, newest first, optionally filtered by status.
///
/// The `, id asc` tiebreak is there for determinism and is **unverified against the corpus**: the 49
/// complaints in the live data share no `created_at` to the second, so the recording cannot say what
/// the Go service does inside a tie. Features did have 16 tied pairs and the evidence there was
/// unanimous (smaller id first), which is SQLite returning rowid order inside a tie; the same rule is
/// applied here rather than leaving the order to the planner, and this note is the marker that it is
/// an inference, not a measurement.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn list_complaints(
    pool: &PgPool,
    project_id: i64,
    status: &str,
) -> Result<Vec<Complaint>, sqlx::Error> {
    let sql = if status.is_empty() {
        format!(
            "select {COMPLAINT_COLUMNS} from complaints where project_id = $1 \
             order by created_at desc, id asc"
        )
    } else {
        format!(
            "select {COMPLAINT_COLUMNS} from complaints where project_id = $1 and status = $2 \
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
        .map(complaint_from_row)
        .collect()
}

/// One complaint by id, `None` when it does not exist.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn complaint(pool: &PgPool, id: i64) -> Result<Option<Complaint>, sqlx::Error> {
    let sql = format!("select {COMPLAINT_COLUMNS} from complaints where id = $1");
    sqlx::query(&sql)
        .bind(id)
        .fetch_optional(pool)
        .await?
        .map(|row| complaint_from_row(&row))
        .transpose()
}

/// The complaints linked to a feature, newest first.
///
/// The direction matters: `feature_complaints` has two columns and the two read routes answer different questions.
/// This one asks "which complaints justify THIS feature" and joins on `fc.feature_id`; its mirror asks "which work
/// answers this complaint" and joins on `fc.complaint_id`. A copy-paste that swaps them returns a plausible list —
/// the feature's own complaints versus every feature touching the same complaint — and only an asymmetric fixture
/// tells them apart.
///
/// An EMPTY slice, never nil: `nil` marshals to `null`, and a client that expects an array breaks on a feature
/// nobody has complained about — which is the most common case, not the edge.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn feature_complaints(pool: &PgPool, feature_id: i64) -> Result<Vec<Complaint>, sqlx::Error> {
    // The aliased column list is written out rather than derived from COMPLAINT_COLUMNS: that constant contains
    // `extract(epoch from …)` expressions, and a mechanical `c.` prefix produces `c.extract(…)`, which is not SQL.
    let sql = "select c.id, c.project_id, c.author_id, c.title, c.body, c.severity, c.frequency,
                      c.strategic_multiplier, c.status, c.merged_into,
                      extract(epoch from c.created_at)::double precision,
                      extract(epoch from c.updated_at)::double precision
                 from feature_complaints fc join complaints c on c.id = fc.complaint_id
                where fc.feature_id = $1 order by c.created_at desc, c.id asc";
    sqlx::query(&sql)
        .bind(feature_id)
        .fetch_all(pool)
        .await?
        .iter()
        .map(complaint_from_row)
        .collect()
}
