//! Field reports: §4.7's structured experience record, and the owner's reply to one.
//!
//! "A field report is a structured experience record, designed to replace the rants and 'does anyone use X?' threads." That
//! sentence is the design constraint, and it has three consequences this module enforces.
//!
//! **`outcome` is one of four values and is required.** A free-text outcome would be the rant the section rules out: "works fine
//! mostly" parses, reads like evidence and cannot be aggregated. `migrated-away` additionally requires `migrated_to`, because the
//! successor is the most useful thing in that report — it is a second candidate for whoever asked the question.
//!
//! **Author and project come from the token and the URL.** A report whose author is whatever the body said is not evidence, so
//! there is no `user_id` field in the form at all: [`NewReport`] has no place to put one, which is stronger than validating it.
//!
//! **`is_owner` is read from the membership table at write time.** It is never taken from the request: the flag's entire value is
//! that the server sets it, and a client that could send it would make every bystander sound like a maintainer.
//!
//! **A removed report takes no new replies, and answers `NotFound`.** The report is not there for the caller, and a distinct
//! refusal would confirm it exists to somebody who cannot see it. Removal is a flag and never a delete: §4.7 says abusive reports
//! are removed by quorum *with appeal*, and the appeal is conducted on the record.

use sqlx::PgPool;

use crate::projects::Project;

/// The four outcomes, in the order §4.7 lists them.
pub const OUTCOMES: [&str; 4] = ["worked", "worked-with-caveats", "abandoned", "migrated-away"];

/// The filing form. There is no author and no project here on purpose — both come from the request, not from its body.
#[derive(Debug, Default, Clone)]
pub struct NewReport {
    pub version: String,
    pub use_case: String,
    pub environment: String,
    pub scale: String,
    pub duration: String,
    pub outcome: String,
    pub migrated_to: String,
    pub caveats: String,
    pub workaround: String,
    pub advice: String,
}

/// One stored report, as the reference serialises it.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Report {
    pub id: i64,
    pub project_id: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub project_slug: Option<String>,
    pub user_id: i64,
    pub version: String,
    pub use_case: String,
    pub environment: String,
    pub scale: String,
    pub duration: String,
    pub outcome: String,
    pub migrated_to: String,
    pub caveats: String,
    pub workaround: String,
    pub advice: String,
    pub removed: bool,
    pub removed_by: i64,
    pub removal_reason: String,
    /// Seconds since the epoch, like the reference's `time.Time` marshalled by `encoding/json`.
    pub created_at: f64,
}

/// One owner reply.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Response {
    pub id: i64,
    pub report_id: i64,
    pub user_id: i64,
    pub body: String,
    pub is_owner: bool,
    pub created_at: f64,
}

/// A deliberate defect reachable only by environment variable, so a probe's control does not cost a rebuild.
///
/// This is a test seam: nothing in the product sets it, and each name below is asserted to make one specific probe check FAIL.
fn mutant(name: &str) -> bool {
    std::env::var("CONCORD_MUTANT")
        .unwrap_or_default()
        .split(',')
        .any(|candidate| candidate.trim() == name)
}

/// Why a write was refused.
#[derive(Debug)]
pub enum Refused {
    /// The caller's input is wrong; the message names the field.
    Invalid(String),
    /// The thing is not there — for a reason the caller is not told apart from "never existed".
    NotFound(String),
}

/// One row read back into [`Report`].
///
/// Read by column NAME rather than as a positional tuple: this table has more columns than `sqlx`'s tuple impls cover, and a name
/// is also the thing a reader can check against the schema.
fn row_to_report(project_slug: &str, row: &sqlx::postgres::PgRow) -> Result<Report, sqlx::Error> {
    use sqlx::Row;
    Ok(Report {
        id: row.try_get("id")?,
        project_id: row.try_get("project_id")?,
        project_slug: Some(project_slug.to_string()),
        user_id: row.try_get("user_id")?,
        version: row.try_get("version")?,
        use_case: row.try_get("use_case")?,
        environment: row.try_get("environment")?,
        scale: row.try_get("scale")?,
        duration: row.try_get("duration")?,
        outcome: row.try_get("outcome")?,
        migrated_to: row.try_get("migrated_to")?,
        caveats: row.try_get("caveats")?,
        workaround: row.try_get("workaround")?,
        advice: row.try_get("advice")?,
        // `removed` is stored as a NUMBER in this schema (`bigint`), not a boolean; the query turns it into one and the API
        // answers a boolean either way.
        removed: row.try_get::<bool, _>("removed")?,
        removed_by: row.try_get("removed_by")?,
        removal_reason: row.try_get("removal_reason")?,
        created_at: row.try_get("created_at")?,
    })
}

const SELECT_REPORT: &str = "select r.id, r.project_id, r.user_id, coalesce(r.version,'') as version,
        coalesce(r.use_case,'') as use_case, coalesce(r.environment,'') as environment,
        coalesce(r.scale,'') as scale, coalesce(r.duration,'') as duration,
        coalesce(r.outcome,'') as outcome, coalesce(r.migrated_to,'') as migrated_to,
        coalesce(r.caveats,'') as caveats, coalesce(r.workaround,'') as workaround,
        coalesce(r.advice,'') as advice, (coalesce(r.removed,0) <> 0) as removed,
        coalesce(r.removed_by,0) as removed_by, coalesce(r.removal_reason,'') as removal_reason,
        extract(epoch from r.created_at)::double precision as created_at
   from field_reports r";

/// Validate and store one report.
///
/// # Errors
/// [`Refused::Invalid`] naming the field, or `sqlx::Error`.
pub async fn create(
    pool: &PgPool,
    project: &Project,
    user_id: i64,
    form: &NewReport,
) -> Result<Result<Report, Refused>, sqlx::Error> {
    let outcome = form.outcome.trim();
    if outcome.is_empty() {
        return Ok(Err(Refused::Invalid(format!(
            "outcome is required and must be one of {}",
            OUTCOMES.join(", ")
        ))));
    }
    if !OUTCOMES.contains(&outcome) {
        return Ok(Err(Refused::Invalid(format!(
            "outcome {outcome:?} must be one of {}",
            OUTCOMES.join(", ")
        ))));
    }
    // §4.7 asks "migrated away (to what?)". A report that does not answer is the one report whose most useful field is missing.
    if outcome == "migrated-away" && form.migrated_to.trim().is_empty() {
        return Ok(Err(Refused::Invalid(
            "a migrated-away report must name the successor in migrated_to".to_string(),
        )));
    }

    let id: i64 = sqlx::query_scalar(
        "insert into field_reports
             (project_id, user_id, version, use_case, environment, scale, duration, outcome, migrated_to,
              caveats, workaround, advice, created_at)
         values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12, now())
         returning id",
    )
    .bind(project.id)
    .bind(user_id)
    .bind(form.version.trim())
    .bind(form.use_case.trim())
    .bind(form.environment.trim())
    .bind(form.scale.trim())
    .bind(form.duration.trim())
    .bind(outcome)
    .bind(form.migrated_to.trim())
    .bind(form.caveats.trim())
    .bind(form.workaround.trim())
    .bind(form.advice.trim())
    .fetch_one(pool)
    .await?;
    let report = load(pool, project, id)
        .await?
        .ok_or(sqlx::Error::RowNotFound)?;
    Ok(Ok(report))
}

/// One report, but only if it belongs to this project.
///
/// The project is checked in the WHERE clause rather than compared afterwards: report ids are global, so a route that read only
/// the id would let any project's URL address any report.
///
/// # Errors
/// `sqlx::Error`.
pub async fn load(pool: &PgPool, project: &Project, report_id: i64) -> Result<Option<Report>, sqlx::Error> {
    let row = sqlx::query(&format!("{SELECT_REPORT} where r.id = $1 and r.project_id = $2"))
        .bind(report_id)
        .bind(project.id)
        .fetch_optional(pool)
        .await?;
    row.as_ref().map(|row| row_to_report(&project.slug, row)).transpose()
}

/// Reply to a report.
///
/// # Errors
/// [`Refused::Invalid`] for an empty body, [`Refused::NotFound`] for a report that is gone or was removed, or `sqlx::Error`.
pub async fn respond(
    pool: &PgPool,
    project: &Project,
    report_id: i64,
    user_id: i64,
    body: &str,
) -> Result<Result<Response, Refused>, sqlx::Error> {
    if body.trim().is_empty() {
        return Ok(Err(Refused::Invalid("a response needs a body".to_string())));
    }
    let Some(report) = load(pool, project, report_id).await? else {
        return Ok(Err(Refused::NotFound(format!("no such field report {report_id}"))));
    };
    // A removed report is hidden from every reader, so a reply to one is written where nobody will see it — and worse, it
    // re-opens a decision: the quorum removed the report and the thread would carry on underneath a row the page does not render.
    if report.removed && !mutant("replies-after-removal") {
        return Ok(Err(Refused::NotFound(format!(
            "field report {report_id} was removed and takes no new replies"
        ))));
    }
    // Read from the membership table, at write time, never from the request. `guest` is the literal a non-member gets, which is
    // why this is a role comparison and not an existence check.
    let role = crate::feed_reads::role_for(pool, project.id, user_id).await?;
    let is_owner = role.is_some_and(|role| role != "guest");

    // `is_owner` is a bigint in this schema — a flag stored as a number, like `removed`. Binding a bool would be rejected by
    // Postgres outright, which is how the port learned it.
    let row: (i64, f64) = sqlx::query_as(
        "insert into field_report_responses (report_id, user_id, body, is_owner, created_at)
         values ($1,$2,$3,$4, now())
         returning id, extract(epoch from created_at)::double precision",
    )
    .bind(report_id)
    .bind(user_id)
    .bind(body.trim())
    .bind(i64::from(is_owner))
    .fetch_one(pool)
    .await?;
    // The seam is on the VALUE the API answers, so a control moves the observable rather than a private variable.
    // Control A: a bystander's reply claims to be the owner's — the defect the flag exists to prevent.
    let is_owner = if mutant("owner-from-request") { true } else { is_owner };
    Ok(Ok(Response {
        id: row.0,
        report_id,
        user_id,
        body: body.trim().to_string(),
        is_owner,
        created_at: row.1,
    }))
}
