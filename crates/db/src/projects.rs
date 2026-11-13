//! S1's store: projects, their metrics, the board, and documents.
//!
//! The column list here is the port of `projectColumns` in `internal/store/store.go:337`, and the
//! `COALESCE`s in it are part of the contract, not tidiness: the Go handler serialises
//! `description` as `""` rather than `null`, `visibility` as `public`, `newcomer_sample` as `0`.

use serde::Serialize;
use serde_json::{Number, Value};
use sqlx::PgPool;

/// A row of `projects` joined to its metrics, in the shape `/api/v1/projects/{slug}` serves.
#[derive(Debug, Clone, Serialize)]
pub struct Project {
    pub id: i64,
    pub slug: String,
    pub name: String,
    pub description: String,
    pub governance_model: String,
    // Absent, not null: the recorded body has no `license` key at all for a project whose licence
    // is empty, which is `omitempty` on the Go field. `newcomer_response_hours` below is the other
    // way round — present and null — and the difference is only visible in the recorded JSON.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub license: Option<String>,
    pub visibility: String,
    pub intake_mode: String,
    #[serde(serialize_with = "ser_unix")]
    pub created_at: f64,
    #[serde(serialize_with = "ser_unix")]
    pub updated_at: f64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub health_score: Option<f64>,
    pub newcomer_response_hours: Option<f64>,
    pub newcomer_sample: i64,
}

/// Go writes these timestamps as `float64` unix seconds, and `encoding/json` prints a whole value
/// without a decimal point. `serde_json` prints `1758000000.0`, which is the same number and a
/// different JSON — the replay tool compares numbers by *type*, so the difference is a failure.
/// This keeps whole seconds integral and fractional seconds fractional.
fn ser_unix<S: serde::Serializer>(value: &f64, serializer: S) -> Result<S::Ok, S::Error> {
    let number = if value.fract() == 0.0 && value.abs() < 9.007_199_254_740_992e15 {
        Number::from(*value as i64)
    } else {
        Number::from_f64(*value).unwrap_or_else(|| Number::from(0))
    };
    number.serialize(serializer)
}

const PROJECT_COLUMNS: &str = "p.id, p.slug, p.name, coalesce(p.description, ''), \
    p.governance_model, coalesce(p.license, ''), coalesce(p.visibility, 'public'), \
    p.intake_mode, extract(epoch from p.created_at)::double precision, \
    extract(epoch from p.updated_at)::double precision, m.health_score, \
    m.newcomer_response_hours, coalesce(m.newcomer_sample, 0)";

fn project_from_row(row: &sqlx::postgres::PgRow) -> Result<Project, sqlx::Error> {
    use sqlx::Row;
    let license: String = row.try_get(5)?;
    Ok(Project {
        id: row.try_get(0)?,
        slug: row.try_get(1)?,
        name: row.try_get(2)?,
        description: row.try_get(3)?,
        governance_model: row.try_get(4)?,
        // The Go struct carries `license,omitempty`, so an empty string is absent — and the column
        // is COALESCEd to '' precisely so an absent licence never reaches JSON as null.
        license: if license.is_empty() { None } else { Some(license) },
        visibility: row.try_get(6)?,
        intake_mode: row.try_get(7)?,
        created_at: row.try_get(8)?,
        updated_at: row.try_get(9)?,
        health_score: row.try_get(10)?,
        newcomer_response_hours: row.try_get(11)?,
        newcomer_sample: row.try_get(12)?,
    })
}

/// One project by slug, `None` when absent.
///
/// # Errors
/// `sqlx::Error` for anything the database reports that is not "no rows": a refusal is not an
/// absence, and turning one into the other is how a broken query becomes a 404 nobody notices.
pub async fn project_by_slug(pool: &PgPool, slug: &str) -> Result<Option<Project>, sqlx::Error> {
    let sql = format!(
        "select {PROJECT_COLUMNS} from projects p \
         left join project_metrics m on m.project_id = p.id where p.slug = $1"
    );
    let row = sqlx::query(&sql).bind(slug).fetch_optional(pool).await?;
    row.as_ref().map(project_from_row).transpose()
}

/// Every project, in the order the Go handler lists them.
///
/// # Errors
/// `sqlx::Error`.
pub async fn list_projects(pool: &PgPool) -> Result<Vec<Project>, sqlx::Error> {
    let sql = format!(
        "select {PROJECT_COLUMNS} from projects p \
         left join project_metrics m on m.project_id = p.id order by p.id"
    );
    let rows = sqlx::query(&sql).fetch_all(pool).await?;
    rows.iter().map(project_from_row).collect()
}

/// One column of the board.
#[derive(Debug, Clone, Serialize)]
pub struct BoardColumn {
    pub name: String,
    pub wip: Option<i64>,
    pub position: i64,
}

/// One card on the board, with the name of the column it sits in.
#[derive(Debug, Clone, Serialize)]
pub struct BoardCard {
    pub id: i64,
    pub project_id: i64,
    pub feature_id: i64,
    pub column: String,
    #[serde(serialize_with = "ser_u64_unix")]
    pub entered_at: f64,
    pub column_id: i64,
}

fn ser_u64_unix<S: serde::Serializer>(value: &f64, serializer: S) -> Result<S::Ok, S::Error> {
    ser_unix(value, serializer)
}

/// The board payload: `{cards, columns}` for one project slug.
#[derive(Debug, Clone, Serialize)]
pub struct Board {
    pub cards: Vec<BoardCard>,
    pub columns: Vec<BoardColumn>,
}

/// The board of one project, `None` when the slug names no project.
///
/// Ordering is by column phase and then card id. The Go query's ordering is the thing to check
/// against the corpus: a board that lists the same cards in a different order is a parity failure
/// the shape comparison catches, and it has not been checked yet here.
///
/// # Errors
/// `sqlx::Error`.
pub async fn board_by_slug(pool: &PgPool, slug: &str) -> Result<Option<Board>, sqlx::Error> {
    use sqlx::Row;
    let project: Option<i64> = sqlx::query_scalar("select id from projects where slug = $1")
        .bind(slug)
        .fetch_optional(pool)
        .await?;
    let Some(project_id) = project else {
        return Ok(None);
    };

    let columns = sqlx::query(
        "select phase, wip_limit, position from board_columns where project_id = $1 \
         order by position, id",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?
    .iter()
    .map(|row| {
        Ok::<_, sqlx::Error>(BoardColumn {
            name: row.try_get("phase")?,
            wip: row.try_get("wip_limit")?,
            position: row.try_get("position")?,
        })
    })
    .collect::<Result<Vec<_>, _>>()?;

    let cards = sqlx::query(
        "select c.id, c.project_id, coalesce(c.feature_id, 0), coalesce(col.phase, ''), \
                extract(epoch from c.entered_at)::double precision, c.column_id \
         from board_cards c left join board_columns col on col.id = c.column_id \
         where c.project_id = $1 order by c.entered_at",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?
    .iter()
    .map(|row| {
        Ok::<_, sqlx::Error>(BoardCard {
            id: row.try_get(0)?,
            project_id: row.try_get(1)?,
            feature_id: row.try_get(2)?,
            column: row.try_get(3)?,
            entered_at: row.try_get(4)?,
            column_id: row.try_get(5)?,
        })
    })
    .collect::<Result<Vec<_>, _>>()?;

    Ok(Some(Board { cards, columns }))
}

/// A document as `GET /api/v1/projects/{slug}/documents` lists it.
#[derive(Debug, Clone, Serialize)]
pub struct Document {
    pub id: i64,
    pub project_id: i64,
    pub kind: String,
    pub slug: String,
    pub title: String,
    /// The whole document text. The Go list shape carries it, so this one does too — a list that
    /// omits the body is a different API, and `/documents` is a small list of long strings.
    pub body: String,
    pub revision: i64,
    pub author_id: Option<i64>,
    #[serde(serialize_with = "ser_unix")]
    pub created_at: f64,
    #[serde(serialize_with = "ser_unix")]
    pub updated_at: f64,
}

const DOCUMENT_COLUMNS: &str = "id, project_id, kind, slug, title, body, revision, author_id, \
    extract(epoch from created_at)::double precision, extract(epoch from updated_at)::double precision";

fn document_from_row(row: &sqlx::postgres::PgRow) -> Result<Document, sqlx::Error> {
    use sqlx::Row;
    Ok(Document {
        id: row.try_get(0)?,
        project_id: row.try_get(1)?,
        kind: row.try_get(2)?,
        slug: row.try_get(3)?,
        title: row.try_get(4)?,
        body: row.try_get(5)?,
        revision: row.try_get(6)?,
        author_id: row.try_get(7)?,
        created_at: row.try_get(8)?,
        updated_at: row.try_get(9)?,
    })
}

/// Every document of a project, in id order.
///
/// # Errors
/// `sqlx::Error`.
pub async fn list_documents(pool: &PgPool, project_id: i64) -> Result<Vec<Document>, sqlx::Error> {
    // `order by kind, slug` is the Go query's order and it is visible in the recorded body: the
    // first document is the `adr`/`known-issues`, not the lowest id.
    let sql = format!(
        "select {DOCUMENT_COLUMNS} from project_documents where project_id = $1 order by kind, slug"
    );
    let rows = sqlx::query(&sql).bind(project_id).fetch_all(pool).await?;
    rows.iter().map(document_from_row).collect()
}

/// One document by its project slug and document slug.
///
/// # Errors
/// `sqlx::Error`.
pub async fn document(
    pool: &PgPool,
    project_slug: &str,
    document_slug: &str,
) -> Result<Option<Document>, sqlx::Error> {
    let sql = format!(
        "select {} from project_documents d \
         join projects p on p.id = d.project_id \
         where p.slug = $1 and d.slug = $2",
        DOCUMENT_COLUMNS
            .split(", ")
            .map(|c| format!("d.{c}"))
            .collect::<Vec<_>>()
            .join(", ")
    );
    let row = sqlx::query(&sql)
        .bind(project_slug)
        .bind(document_slug)
        .fetch_optional(pool)
        .await?;
    row.as_ref().map(document_from_row).transpose()
}

/// The body of a document, which the list shape above deliberately does not carry.
///
/// # Errors
/// `sqlx::Error`.
pub async fn document_body(
    pool: &PgPool,
    project_slug: &str,
    document_slug: &str,
) -> Result<Option<Value>, sqlx::Error> {
    use sqlx::Row;
    let row = sqlx::query(
        "select d.body from project_documents d join projects p on p.id = d.project_id \
         where p.slug = $1 and d.slug = $2",
    )
    .bind(project_slug)
    .bind(document_slug)
    .fetch_optional(pool)
    .await?;
    let Some(row) = row else { return Ok(None) };
    let body: String = row.try_get(0)?;
    Ok(Some(Value::String(body)))
}
