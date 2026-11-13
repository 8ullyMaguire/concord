//! Requests — the question board, its answers, and its tags.
//!
//! Three things in this module are not obvious, and each one was a bug in the service being ported:
//!
//! * **`project_id` is nullable, and NULL means "instance-wide".** A plain `i64` that scanned the column directly
//!   fails on exactly the rows that make the feature work — `converting NULL to int64` — so it is read as an
//!   `Option<i64>` and normalised to `0` afterwards. `0` is the sentinel rather than a pointer because no valid project
//!   has id 0 and every caller wants a number it can compare; the JSON omits it when 0, so a client can tell
//!   "instance-wide" from "project 0" without the key ever appearing.
//! * **an instance-wide request is PUBLIC by construction.** It belongs to no project, so there is no project whose
//!   visibility could hide it. The unported handler called `GetProjectByID(0)` unconditionally and answered 404, which
//!   made every instance-wide request unreadable through every route that resolves one — the feature was dead on arrival
//!   for exactly the requests it exists to serve.
//! * **the answer ranking is a GLICKO RATING, not a vote score.** `request_answers` has `proposed_by` and
//!   `elo_r/elo_rd/elo_vol`; it has no `author_id` and no `score`. The old struct kept the wrong names, so even a
//!   corrected query would have scanned into fields that do not correspond to columns.

use sqlx::PgPool;

/// One request for help, advice or a comparison.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Request {
    /// The row.
    pub id: i64,
    /// Its project, **absent** for an instance-wide request. Omitted from the JSON when 0.
    #[serde(skip_serializing_if = "is_zero")]
    pub project_id: i64,
    /// Its title.
    pub title: String,
    /// Its body.
    pub body: String,
    /// `open`, `answered`, `closed`, …
    pub status: String,
    /// When, unix seconds.
    pub created_at: f64,
    /// §17's accepted-fit badge: set only by the request's author, **displayed but does not override the ranking**.
    /// Absent (not 0) when no answer has been accepted.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub accepted_answer_id: Option<i64>,
}

fn is_zero(value: &i64) -> bool {
    *value == 0
}

/// One answer to a request.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Answer {
    /// The row.
    pub id: i64,
    /// Its request.
    pub request_id: i64,
    /// Its project — `0` for an answer on an instance-wide request.
    pub project_id: i64,
    /// What it says.
    pub body: String,
    /// `proposed`, `accepted`, …
    pub status: String,
    /// Glicko rating.
    pub elo_r: f64,
    /// Glicko deviation.
    pub elo_rd: f64,
    /// Glicko volatility.
    pub elo_vol: f64,
    /// Who proposed it — the column is `proposed_by`, not `author_id`.
    pub proposed_by: i64,
    /// When, unix seconds.
    pub created_at: f64,
    /// When it last changed, unix seconds.
    pub updated_at: f64,
}

/// A project's requests, newest first.
///
/// The tiebreak is written down because PostgreSQL has no stable order inside a tie and SQLite does: `created_at` is
/// floating seconds and two requests made in the same second must not swap between reads.
///
/// # Errors
/// `sqlx::Error`.
pub async fn by_project(pool: &PgPool, project_id: i64) -> Result<Vec<Request>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, Option<i64>, String, String, String, f64, Option<i64>)>(
        "select id, project_id, title, body, status,
                extract(epoch from created_at)::double precision, accepted_answer_id
           from requests where project_id = $1 order by created_at desc, id asc",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;
    Ok(rows.into_iter().map(row_to_request).collect())
}

/// The instance-wide board: every request with no project, newest first.
///
/// Public by construction and therefore unguarded — the one place in this module where "no access check" is the correct
/// code rather than an oversight.
///
/// # Errors
/// `sqlx::Error`.
pub async fn instance_board(pool: &PgPool) -> Result<Vec<Request>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, Option<i64>, String, String, String, f64, Option<i64>)>(
        "select id, project_id, title, body, status,
                extract(epoch from created_at)::double precision, accepted_answer_id
           from requests where project_id is null order by created_at desc, id asc",
    )
    .fetch_all(pool)
    .await?;
    Ok(rows.into_iter().map(row_to_request).collect())
}

fn row_to_request(row: (i64, Option<i64>, String, String, String, f64, Option<i64>)) -> Request {
    let (id, project_id, title, body, status, created_at, accepted_answer_id) = row;
    Request {
        id,
        // NULL becomes 0 rather than an error: see the module note.
        project_id: project_id.unwrap_or(0),
        title,
        body,
        status,
        created_at,
        accepted_answer_id,
    }
}

/// One request, or `None`.
///
/// # Errors
/// `sqlx::Error`.
pub async fn get(pool: &PgPool, request_id: i64) -> Result<Option<Request>, sqlx::Error> {
    let row = sqlx::query_as::<_, (i64, Option<i64>, String, String, String, f64, Option<i64>)>(
        "select id, project_id, title, body, status,
                extract(epoch from created_at)::double precision, accepted_answer_id
           from requests where id = $1",
    )
    .bind(request_id)
    .fetch_optional(pool)
    .await?;
    Ok(row.map(row_to_request))
}

/// A request's answers, best rating first.
///
/// Ordered by `elo_r DESC, id` — the rating, not a vote score, because the ranking IS the rating. An answer that has
/// never been compared keeps its prior and sits wherever that puts it; that is the honest shape of "no evidence yet".
///
/// # Errors
/// `sqlx::Error`.
pub async fn answers(pool: &PgPool, request_id: i64) -> Result<Vec<Answer>, sqlx::Error> {
    let rows = sqlx::query_as::<
        _,
        (i64, i64, i64, String, String, f64, f64, f64, i64, f64, f64),
    >(
        "select id, request_id, coalesce(project_id, 0), body, status,
                elo_r, elo_rd, elo_vol, proposed_by,
                extract(epoch from created_at)::double precision,
                extract(epoch from updated_at)::double precision
           from request_answers where request_id = $1 order by elo_r desc, id",
    )
    .bind(request_id)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(
            |(id, request_id, project_id, body, status, elo_r, elo_rd, elo_vol, proposed_by, created_at, updated_at)| {
                Answer {
                    id,
                    request_id,
                    project_id,
                    body,
                    status,
                    elo_r,
                    elo_rd,
                    elo_vol,
                    proposed_by,
                    created_at,
                    updated_at,
                }
            },
        )
        .collect())
}

/// A request's tag names, alphabetical.
///
/// Names and not ids: the board renders tags, and a client that had to resolve ids would need a second read per tag.
///
/// # Errors
/// `sqlx::Error`.
pub async fn tags(pool: &PgPool, request_id: i64) -> Result<Vec<String>, sqlx::Error> {
    sqlx::query_scalar::<_, String>(
        "select t.name from request_tags rt join tags t on t.id = rt.tag_id
          where rt.request_id = $1 order by t.name",
    )
    .bind(request_id)
    .fetch_all(pool)
    .await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_instance_wide_request_says_zero_and_omits_the_key() {
        let request = Request {
            id: 1,
            project_id: 0,
            title: "t".into(),
            body: "b".into(),
            status: "open".into(),
            created_at: 0.0,
            accepted_answer_id: None,
        };
        let json = serde_json::to_string(&request).expect("serialises");
        assert!(!json.contains("project_id"), "{json}");
        assert!(!json.contains("accepted_answer_id"), "{json}");
    }

    #[test]
    fn a_scoped_request_carries_its_project_and_a_badge_only_once_accepted() {
        let request = Request {
            id: 2,
            project_id: 7,
            title: "t".into(),
            body: "b".into(),
            status: "open".into(),
            created_at: 0.0,
            accepted_answer_id: Some(9),
        };
        let json = serde_json::to_string(&request).expect("serialises");
        assert!(json.contains("\"project_id\":7"), "{json}");
        assert!(json.contains("\"accepted_answer_id\":9"), "{json}");
    }
}
