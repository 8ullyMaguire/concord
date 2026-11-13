//! S1's store: projects, their metrics, the board, and documents.
//!
//! The column list here is the port of `projectColumns` in `internal/store/store.go:337`, and the
//! `COALESCE`s in it are part of the contract, not tidiness: the Go handler serialises
//! `description` as `""` rather than `null`, `visibility` as `public`, `newcomer_sample` as `0`.

use serde::Serialize;
use serde_json::Number;
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
pub(crate) fn ser_unix<S: serde::Serializer>(value: &f64, serializer: S) -> Result<S::Ok, S::Error> {
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
///
/// A card is either a row in `board_cards` (somebody placed it) or *derived* from the feature's own
/// status. A derived card has no row, so `id` and `column_id` are zero and it is addressed by
/// `feature_id`; `derived` is what says so, and `title` is carried only for derived cards because a
/// placed card's title is the feature's and the board page reads it from the feature.
#[derive(Debug, Clone, Serialize)]
pub struct BoardCard {
    pub id: i64,
    pub project_id: i64,
    pub feature_id: i64,
    pub column: String,
    #[serde(serialize_with = "ser_u64_unix")]
    pub entered_at: f64,
    pub column_id: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub derived: bool,
}

/// A feature's workflow status mapped onto the board phase it belongs in.
///
/// The two vocabularies are not the same and an identity match gets it badly wrong: the live
/// database has seven feature statuses and nine board phases and only four names appear in both, so
/// matching by name would place 60% of the work nowhere. Spelled out rather than derived, because
/// each pair is a decision about what the board means — a draft is untriaged work, so it belongs in
/// `inbox`, not in a column called `draft` that does not exist. A status absent from this table is
/// omitted: guessing a placement is the same fabrication as guessing any other.
pub fn status_phase(status: &str) -> Option<&'static str> {
    Some(match status {
        "draft" => "inbox",
        "discussion" => "triaged",
        "consensus" => "consensus",
        "ready" => "ready",
        "in_progress" => "in_progress",
        "review" => "review",
        "shipped" => "done",
        "rejected" => "rejected",
        _ => return None,
    })
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
         order by position",
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

    let mut cards = sqlx::query(
        "select c.id, c.project_id, coalesce(c.feature_id, 0), coalesce(col.phase, ''), \
                extract(epoch from c.entered_at)::double precision, c.column_id \
         from board_cards c left join board_columns col on col.id = c.column_id \
         where c.project_id = $1 order by c.entered_at asc",
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
            title: None,
            derived: false,
        })
    })
    .collect::<Result<Vec<_>, sqlx::Error>>()?;

    // The board shows the work the project has, not the work somebody has placed: `board_cards` was
    // empty for all 70 projects for months, and every board rendered nine columns of "0". So a
    // feature with no card is derived into the phase its status names.
    //
    // `placed` is computed from the cards BEFORE any filtering, which is why the milestone filter is
    // not applied here: a filtered-out feature would look unplaced and its derived twin would appear
    // in its status's column, under a filter that claims to show less.
    let placed: std::collections::HashSet<i64> = cards
        .iter()
        .filter(|card| card.feature_id != 0)
        .map(|card| card.feature_id)
        .collect();
    let known: std::collections::HashSet<&str> =
        columns.iter().map(|column| column.name.as_str()).collect();

    let features = sqlx::query(
        "select id, status, coalesce(title, '') from features \
         where project_id = $1 and status is not null and status <> '' order by id",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;
    for row in &features {
        let id: i64 = row.try_get(0)?;
        let status: String = row.try_get(1)?;
        let title: String = row.try_get(2)?;
        if placed.contains(&id) {
            continue;
        }
        let Some(phase) = status_phase(&status) else {
            continue;
        };
        if !known.contains(phase) {
            continue;
        }
        cards.push(BoardCard {
            id: 0,
            project_id,
            feature_id: id,
            column: phase.to_string(),
            entered_at: 0.0,
            column_id: 0,
            title: if title.is_empty() { None } else { Some(title) },
            derived: true,
        });
    }

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
/// One document of a project, by its integer id — the key the Go route uses
/// (`/projects/{slug}/documents/{doc_id}`), not the slug.
///
/// # Errors
/// [`sqlx::Error`] when the read fails. A missing document is `None`, which the route answers as the
/// same 404 a project that does not exist gets.
pub async fn document(
    pool: &PgPool,
    project_id: i64,
    id: i64,
) -> Result<Option<Document>, sqlx::Error> {
    // The column list is written for a bare table and cannot be reused through a join: prefixing
    // every entry with an alias turns `extract(epoch from created_at)` into `d.extract(epoch from …)`,
    // which is a syntax error at `from`. That is what the first version did, and nothing caught it
    // because no corpus shape asked for this route — the mutation run is what pointed at it.
    let sql = format!(
        "select {DOCUMENT_COLUMNS} from project_documents where project_id = $1 and id = $2"
    );
    sqlx::query(&sql)
        .bind(project_id)
        .bind(id)
        .fetch_optional(pool)
        .await?
        .map(|row| document_from_row(&row))
        .transpose()
}

/// Why a card could not be moved.
#[derive(Debug)]
pub enum MoveError {
    /// The phase names no column of this project.
    NoSuchColumn(String),
    /// The card or the feature is not in this project.
    NotFound(String),
    /// The column's WIP limit is full.
    ColumnFull(String),
    /// Both a card id and a feature id were given, or neither was.
    Invalid(String),
    Database(sqlx::Error),
}

impl From<sqlx::Error> for MoveError {
    fn from(error: sqlx::Error) -> Self {
        MoveError::Database(error)
    }
}

async fn column_id(pool: &PgPool, project_id: i64, phase: &str) -> Result<i64, MoveError> {
    sqlx::query_scalar("select id from board_columns where project_id = $1 and phase = $2")
        .bind(project_id)
        .bind(phase)
        .fetch_optional(pool)
        .await?
        .ok_or_else(|| MoveError::NoSuchColumn(format!("phase {phase:?}")))
}

/// `checkWIPLimit`: refuse a move into a column that is already at its limit.
///
/// A WIP limit that is only displayed is worse than none: it teaches people the board is policed
/// and then lets the board grow. The count excludes the card being moved, so re-dropping a card on
/// the column it already occupies is not a refusal — otherwise every retry of a successful move
/// would report a full column.
async fn check_wip(
    pool: &PgPool,
    column: i64,
    card_id: i64,
    project_id: i64,
    phase: &str,
) -> Result<(), MoveError> {
    let limit: Option<i64> =
        sqlx::query_scalar("select wip_limit from board_columns where id = $1 and project_id = $2")
            .bind(column)
            .bind(project_id)
            .fetch_optional(pool)
            .await?
            .ok_or_else(|| MoveError::NoSuchColumn(format!("board column {column}")))?;
    let Some(limit) = limit else {
        return Ok(()); // no limit set
    };
    let already: i64 =
        sqlx::query_scalar("select count(*) from board_cards where column_id = $1 and id = $2")
            .bind(column)
            .bind(card_id)
            .fetch_one(pool)
            .await?;
    if already > 0 {
        return Ok(());
    }
    let count: i64 = sqlx::query_scalar("select count(*) from board_cards where column_id = $1")
        .bind(column)
        .fetch_one(pool)
        .await?;
    if count >= limit {
        return Err(MoveError::ColumnFull(format!(
            "{phase:?} holds {count} of its {limit} permitted -- finish or move something out \
             before adding another"
        )));
    }
    Ok(())
}

/// Move a card by row id, or place a feature that has no row yet.
///
/// The two are one function because they are one endpoint: `card_id` identifies a `board_cards`
/// row, `feature_id` identified a derived card, and naming both (or neither) is a caller error.
///
/// # Errors
/// [`MoveError`], mapped by the caller onto the contract's codes.
pub async fn move_card(
    pool: &PgPool,
    project_id: i64,
    card_id: i64,
    feature_id: i64,
    to: &str,
) -> Result<(), MoveError> {
    match (card_id, feature_id) {
        (c, f) if c > 0 && f > 0 => Err(MoveError::Invalid(
            "name a card by id or by feature, not both".into(),
        )),
        (_, f) if f > 0 => place_feature(pool, project_id, f, to).await,
        _ => move_row(pool, project_id, card_id, to).await,
    }
}

async fn move_row(
    pool: &PgPool,
    project_id: i64,
    card_id: i64,
    to: &str,
) -> Result<(), MoveError> {
    if card_id <= 0 {
        return Err(MoveError::Invalid("no card identified".into()));
    }
    let column = column_id(pool, project_id, to).await?;
    check_wip(pool, column, card_id, project_id, to).await?;
    // RowsAffected separates "moved" from "no such card": without it a bogus id is reported as a
    // success, which is a lie the board cannot tell from a move.
    let affected = sqlx::query(
        "update board_cards set column_id = $1 where id = $2 and project_id = $3",
    )
    .bind(column)
    .bind(card_id)
    .bind(project_id)
    .execute(pool)
    .await?
    .rows_affected();
    if affected == 0 {
        return Err(MoveError::NotFound(format!("board card {card_id}")));
    }
    Ok(())
}

async fn place_feature(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
    to: &str,
) -> Result<(), MoveError> {
    let column = column_id(pool, project_id, to).await?;
    // The feature must belong to this project, or a caller could place any feature on any board.
    let owned: Option<i64> =
        sqlx::query_scalar("select id from features where id = $1 and project_id = $2")
            .bind(feature_id)
            .bind(project_id)
            .fetch_optional(pool)
            .await?;
    if owned.is_none() {
        return Err(MoveError::NotFound(format!(
            "feature {feature_id} in project {project_id}"
        )));
    }
    // The limit applies here too: place_feature is the path taken by every feature with no row, so
    // a limit checked only in move_row would be bypassed by exactly the cards nobody dragged.
    // Both ids, because they are different keys and the first version compared a *card* id to a
    // *column* id — a guard that is always true and therefore never took the branch it was written
    // for. The mutation run reported three survivors on this line, which is how it was found: a
    // comparison of two things that are never equal kills nothing.
    let existing: Option<(i64, Option<i64>)> = sqlx::query_as(
        "select id, column_id from board_cards where project_id = $1 and feature_id = $2",
    )
    .bind(project_id)
    .bind(feature_id)
    .fetch_optional(pool)
    .await?;
    match existing {
        None => check_wip(pool, column, 0, project_id, to).await?,
        Some((card, current)) if current != Some(column) => {
            check_wip(pool, column, card, project_id, to).await?
        }
        _ => {}
    }
    sqlx::query(
        "insert into board_cards (project_id, kind, feature_id, column_id, entered_at) \
         values ($1, 'feature', $2, $3, now()) \
         on conflict (project_id, feature_id) do update set column_id = excluded.column_id",
    )
    .bind(project_id)
    .bind(feature_id)
    .bind(column)
    .execute(pool)
    .await?;
    Ok(())
}

/// The nine board phases, in the order `governance.BoardPhases` lists them — the column `position`
/// is this order and the board sorts on it, so a different order is a differently-shaped board.
const BOARD_PHASES: [&str; 9] = [
    "inbox",
    "triaged",
    "solution_draft",
    "consensus",
    "ready",
    "in_progress",
    "review",
    "done",
    "rejected",
];

/// Why a project could not be created.
#[derive(Debug)]
pub enum CreateError {
    /// `ErrInvalid`: the slug or the governance model.
    Invalid(String),
    /// `ErrDuplicate`: the slug is taken.
    Duplicate(String),
    Database(sqlx::Error),
}

impl From<sqlx::Error> for CreateError {
    fn from(error: sqlx::Error) -> Self {
        CreateError::Database(error)
    }
}

/// `validSlug`: lowercase kebab-case, up to 100 characters.
fn valid_slug(slug: &str) -> bool {
    !slug.is_empty()
        && slug.len() <= 100
        && slug
            .chars()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, '-' | '_' | '.'))
}

/// Create a project and everything that makes one usable.
///
/// The Go store does five writes in one transaction and so does this: the project, its charter, its
/// nine board columns, the creator's maintainer membership, and its metrics row. The charter is the
/// project's whole policy surface — quorum, consent, how long votes run, what the WIP limits are —
/// and a project without one has no rules to read, so the two are never separate.
///
/// There is no `projects_fts` write here, unlike the Go store's `ReindexProject`: the baseline makes
/// `search_tsv` a GENERATED column of `projects`, so the index maintains itself and a reindex call
/// would be writing a table that does not exist.
///
/// # Errors
/// [`CreateError::Invalid`] for a bad slug or governance model, [`CreateError::Duplicate`] for a
/// taken slug, [`CreateError::Database`] otherwise.
pub async fn create_project(
    pool: &PgPool,
    slug: &str,
    name: &str,
    description: &str,
    governance_model: &str,
    license: &str,
    user_id: i64,
) -> Result<Project, CreateError> {
    if !valid_slug(slug) {
        return Err(CreateError::Invalid(
            "slug must be lowercase kebab-case".into(),
        ));
    }
    let maintainer_led = match governance_model {
        "collective" => false,
        "maintainer_led" => true,
        other => {
            return Err(CreateError::Invalid(format!(
                "governance_model must be collective or maintainer_led, not {other:?}"
            )))
        }
    };
    // `DefaultCharter`, number for number. `MergeRequiresQuorum` is the one field the model changes,
    // and it is the whole point of the two models: a maintainer-led project merges on a maintainer's
    // say-so, a collective one needs the room.
    let merge_requires_quorum = !maintainer_led;

    let mut tx = pool.begin().await?;
    let id = sqlx::query_scalar::<_, i64>(
        "insert into projects (slug, name, description, governance_model, license, created_at, \
                               updated_at, public_id) \
         values ($1, $2, $3, $4, $5, now(), now(), $6) returning id",
    )
    .bind(slug)
    .bind(name)
    .bind(description)
    .bind(governance_model)
    .bind(license)
    .bind(ulid::Ulid::new().to_string())
    .fetch_one(&mut *tx)
    .await
    .map_err(|error| match error {
        sqlx::Error::Database(ref database) if database.code().as_deref() == Some("23505") => {
            CreateError::Duplicate(slug.to_string())
        }
        other => CreateError::Database(other),
    })?;

    sqlx::query(
        "insert into charters (project_id, quorum_ratio, quorum_min, consent_ratio, \
             support_ratio_min, override_ratio, vote_window_days, merge_requires_quorum, \
             merge_quorum_min, merge_quorum_ratio, require_reviewer_approval, wip_in_progress, \
             wip_review, lam, mu, pain_halflife_days, rep_halflife_days, glicko_tau, \
             vote_weight_cap, theme_lifetime_days, solution_call_min_voters, \
             solution_call_confidence, solution_stable_hours) \
         values ($1, 0.2, 3, 0.7, 0.5, 0.8, 7, $2, 2, 0.25, true, 3, 4, 20.0, 100.0, 90, 180, \
                 0.5, 3.0, 90, 3, 0.80, 72)",
    )
    .bind(id)
    .bind(merge_requires_quorum)
    .execute(&mut *tx)
    .await?;

    for (position, phase) in BOARD_PHASES.iter().enumerate() {
        let wip: Option<i64> = match *phase {
            "in_progress" => Some(3),
            "review" => Some(4),
            _ => None,
        };
        sqlx::query(
            "insert into board_columns (project_id, phase, position, wip_limit) \
             values ($1, $2, $3, $4)",
        )
        .bind(id)
        .bind(*phase)
        .bind(position as i64)
        .bind(wip)
        .execute(&mut *tx)
        .await?;
    }

    sqlx::query(
        "insert into members (project_id, user_id, role, joined_at) values ($1, $2, 'maintainer', now())",
    )
    .bind(id)
    .bind(user_id)
    .execute(&mut *tx)
    .await?;

    sqlx::query("insert into project_metrics (project_id, computed_at) values ($1, now())")
        .bind(id)
        .execute(&mut *tx)
        .await?;

    tx.commit().await?;
    project_by_slug(pool, slug)
        .await
        .map_err(CreateError::Database)?
        .ok_or_else(|| CreateError::Invalid(format!("project {slug:?} vanished after insert")))
}

/// A project's governance model, `collective` when the row has no valid value.
///
/// The charters table is not consulted: the model lives on `projects`, and it decides which defaults
/// apply — a maintainer-led project merges without a quorum, and every threshold read from the charter
/// has to agree with that.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn governance_model(pool: &PgPool, project_id: i64) -> Result<String, sqlx::Error> {
    let model: Option<String> =
        sqlx::query_scalar("select governance_model from projects where id = $1")
            .bind(project_id)
            .fetch_optional(pool)
            .await?;
    Ok(model
        .filter(|value| {
            concord_governance::GovernanceModel::parse(value).is_some()
        })
        .unwrap_or_else(|| "collective".to_string()))
}

/// A user's role in a project, `None` when they are not a member.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn member_role(
    pool: &PgPool,
    project_id: i64,
    user_id: i64,
) -> Result<Option<String>, sqlx::Error> {
    sqlx::query_scalar("select role from members where project_id = $1 and user_id = $2")
        .bind(project_id)
        .bind(user_id)
        .fetch_optional(pool)
        .await
}
