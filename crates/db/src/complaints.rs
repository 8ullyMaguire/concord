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

/// A refusal from a complaint write, as a verdict rather than a database failure.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Refused {
    /// No such complaint.
    NotFound,
    /// The request is well formed and the row exists, but this act does not apply to it — a non-open complaint, a merge
    /// into itself, or a merge that would cross projects. 400 in every case: the id was real.
    Invalid(String),
}

/// File a complaint.
///
/// `strategic_multiplier` is defaulted by the caller, not here: the reference defaults it in the handler, because the
/// complaint form sends severity and frequency only, and a multiplier of 0 multiplies the pain score to exactly 0 — the
/// complaint is stored, validated, linked to a feature, and contributes nothing, with no indication why.
///
/// The duplicate path is dead and ported as dead: the reference checks `isUniqueViolation` on this insert and answers
/// `ErrDuplicate`, but `complaints` carries **no unique constraint** — only its primary key and `public_id`. So that
/// branch cannot fire, and a duplicate complaint is caught by the embedder's §6.1 gate instead, before the insert.
///
/// # Errors
/// `sqlx::Error`.
pub async fn create(
    pool: &PgPool,
    project_id: i64,
    author_id: i64,
    title: &str,
    body: &str,
    severity: i64,
    frequency: f64,
    strategic_multiplier: f64,
) -> Result<Complaint, sqlx::Error> {
    let id: i64 = sqlx::query_scalar(
        // `public_id` is NOT NULL with no default on every API-facing table, so the writer supplies one; every existing
        // write path in this crate does the same. Omitting it fails as a 500 at insert time, which is how this was found.
        "insert into complaints (project_id, author_id, title, body, severity, frequency,
                                 strategic_multiplier, status, created_at, updated_at, public_id)
         values ($1, $2, $3, $4, $5, $6, $7, 'open', now(), now(), $8) returning id",
    )
    .bind(project_id)
    .bind(author_id)
    .bind(title)
    .bind(body)
    .bind(severity)
    .bind(frequency)
    .bind(strategic_multiplier)
    .bind(ulid::Ulid::new().to_string())
    .fetch_one(pool)
    .await?;
    complaint(pool, id).await?.ok_or(sqlx::Error::RowNotFound)
}

/// Record that one person is affected, at a severity.
///
/// `or ignore`, not an increment: a person says how badly this hurts **once**, and clicking twice must not double their
/// influence. The reference relies on the primary key for that, and the `updated_at` bump afterwards is what makes the
/// panel show the complaint as recently alive.
///
/// # Errors
/// `sqlx::Error`.
pub async fn add_impact(
    pool: &PgPool,
    complaint_id: i64,
    user_id: i64,
    severity: i64,
) -> Result<Result<(), Refused>, sqlx::Error> {
    if complaint(pool, complaint_id).await?.is_none() {
        return Ok(Err(Refused::NotFound));
    }
    sqlx::query(
        "insert into complaint_impacts (complaint_id, user_id, severity, created_at)
         values ($1, $2, $3, now()) on conflict do nothing",
    )
    .bind(complaint_id)
    .bind(user_id)
    .bind(severity)
    .execute(pool)
    .await?;
    sqlx::query("update complaints set updated_at = now() where id = $1")
        .bind(complaint_id)
        .execute(pool)
        .await?;
    Ok(Ok(()))
}

/// §M1's verdict: a complaint is real. Only an `open` complaint can be validated, and the author gains reputation for
/// having reported something true.
///
/// The reputation event is written **after** the status change and outside a transaction in the reference too — and that
/// is a wart worth naming rather than quietly fixing: a crash between the two leaves a validated complaint that earned
/// nothing. It is carried as-is because it is not a behaviour this row is asked to change, and it is visible here so the
/// cutover checklist can decide.
///
/// # Errors
/// `sqlx::Error`.
pub async fn validate(pool: &PgPool, complaint_id: i64) -> Result<Result<Complaint, Refused>, sqlx::Error> {
    let Some(current) = complaint(pool, complaint_id).await? else {
        return Ok(Err(Refused::NotFound));
    };
    if current.status != "open" {
        return Ok(Err(Refused::Invalid(
            "only open complaints can be validated".to_string(),
        )));
    }
    sqlx::query("update complaints set status = 'validated', updated_at = now() where id = $1")
        .bind(complaint_id)
        .execute(pool)
        .await?;
    sqlx::query(
        "insert into reputation_events (project_id, user_id, kind, points, created_at)
         values ($1, $2, 'complaint_validated', 1, now())",
    )
    .bind(current.project_id)
    .bind(current.author_id)
    .execute(pool)
    .await?;
    Ok(Ok(complaint(pool, complaint_id)
        .await?
        .ok_or(sqlx::Error::RowNotFound)?))
}

/// The other verdict: this was spam. Closes the complaint, takes the §M1 penalty from the author, and **tells them who
/// decided** — which is why the actor is a parameter and not "the system".
///
/// One transaction for all three. Split into three statements, a failure in the middle leaves a rejected complaint whose
/// author was never told, and nothing afterwards can tell that it happened. The notification is skipped when the author
/// rejected their own complaint: notifying somebody of their own action is noise.
///
/// # Errors
/// `sqlx::Error`.
pub async fn reject(
    pool: &PgPool,
    complaint_id: i64,
    actor_id: i64,
) -> Result<Result<Complaint, Refused>, sqlx::Error> {
    let Some(current) = complaint(pool, complaint_id).await? else {
        return Ok(Err(Refused::NotFound));
    };
    if current.status != "open" {
        return Ok(Err(Refused::Invalid(
            "only open complaints can be rejected".to_string(),
        )));
    }
    let mut tx = pool.begin().await?;
    sqlx::query("update complaints set status = 'rejected', updated_at = now() where id = $1")
        .bind(complaint_id)
        .execute(&mut *tx)
        .await?;
    sqlx::query(
        "insert into reputation_events (project_id, user_id, kind, points, created_at)
         values ($1, $2, 'complaint_rejected', -1, now())",
    )
    .bind(current.project_id)
    .bind(current.author_id)
    .execute(&mut *tx)
    .await?;
    if current.author_id != actor_id {
        sqlx::query(
            "insert into notifications (user_id, kind, project_id, subject_id, title, reason, actor_id, created_at)
             values ($1, 'complaint_rejected', $2, $3, $4, $5, $6, now())",
        )
        .bind(current.author_id)
        .bind(current.project_id)
        .bind(complaint_id)
        .bind(&current.title)
        .bind("rejected as spam")
        .bind(actor_id)
        .execute(&mut *tx)
        .await?;
    }
    tx.commit().await?;
    Ok(Ok(complaint(pool, complaint_id)
        .await?
        .ok_or(sqlx::Error::RowNotFound)?))
}

/// Fold one complaint into another, in the same project.
///
/// Three refusals, and only the first is obvious:
///
/// * **same project only.** This check did not exist in the reference, and loading both rows is not a guard on either:
///   a maintainer of one project could fold its complaint into another project's, and the source's impacts were then
///   written against a foreign complaint — an `INSERT OR IGNORE` no later reader can tell from a legitimate impact;
/// * **not into itself**, which would set `merged_into` to its own id and make every reader that follows the chain loop;
/// * **the target must exist**, checked by loading it.
///
/// The impacts are copied `on conflict do nothing`: the same person may already have said they are affected by both, and
/// after the merge that is one opinion, not two.
///
/// The audit row is written here rather than by the handler, with the **real actor** — the reference's store records the
/// actor and the handler deliberately does not add a second row, because it used to record actor 0.
///
/// # Errors
/// `sqlx::Error`.
pub async fn merge(
    pool: &PgPool,
    source_id: i64,
    target_id: i64,
    actor_id: i64,
) -> Result<Result<(), Refused>, sqlx::Error> {
    let Some(source) = complaint(pool, source_id).await? else {
        return Ok(Err(Refused::NotFound));
    };
    let Some(target) = complaint(pool, target_id).await? else {
        return Ok(Err(Refused::NotFound));
    };
    if target.project_id != source.project_id {
        return Ok(Err(Refused::Invalid(format!(
            "complaint {source_id} belongs to project {} and complaint {target_id} to project {}; a merge cannot cross projects",
            source.project_id, target.project_id
        ))));
    }
    if source_id == target_id {
        return Ok(Err(Refused::Invalid(format!(
            "complaint {source_id} cannot be merged into itself"
        ))));
    }
    let mut tx = pool.begin().await?;
    sqlx::query("update complaints set merged_into = $1, updated_at = now() where id = $2")
        .bind(target_id)
        .bind(source_id)
        .execute(&mut *tx)
        .await?;
    sqlx::query(
        "insert into complaint_impacts (complaint_id, user_id, severity, created_at)
         select $1, user_id, severity, created_at from complaint_impacts where complaint_id = $2
         on conflict do nothing",
    )
    .bind(target_id)
    .bind(source_id)
    .execute(&mut *tx)
    .await?;
    sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1, $2, 'merge_complaints', 'complaint', $3, $4, now())",
    )
    .bind(source.project_id)
    .bind(actor_id)
    .bind(source_id)
    .bind(format!("merged into {target_id}"))
    .execute(&mut *tx)
    .await?;
    tx.commit().await?;
    Ok(Ok(()))
}
