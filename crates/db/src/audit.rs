//! The audit log's reader — `/api/v1/projects/{slug}/audit`.
//!
//! Four rules from the Go store are carried across, and each exists because the obvious version was
//! wrong in a way that produced a plausible page:
//!
//! 1. **Two clamps, not one.** An absent `limit` is the default; a `limit` above the ceiling is the
//!    ceiling. Folding them (`if f.Limit <= 0 || f.Limit > max { f.Limit = default }`) sends a caller
//!    that asked for the ceiling back to 50, and the only symptom is a page that looks like it lost
//!    rows.
//! 2. **An empty search emits no `LIKE` at all.** `LIKE '%%'` matches everything, so a blanket clause
//!    would return exactly the unfiltered rows at the cost of a scan that reads as a slow endpoint.
//! 3. **Wildcards in the search are escaped, backslash first.** Escaping last would double the
//!    backslashes this function just inserted, turning `\%` into a literal backslash followed by a
//!    live wildcard — the exact bug `ESCAPE` exists to prevent, reintroduced by the fix.
//! 4. **The total is counted before the rows are read.** The Go pool had one connection, so a second
//!    query under an open cursor deadlocked; here it is a rule about correctness instead: a client
//!    that pages on `limit` cannot tell "past the end" from "nothing matched" without it.

use serde::Serialize;
use sqlx::PgPool;

/// The default page.
pub const AUDIT_DEFAULT_LIMIT: i64 = 50;
/// The ceiling a caller cannot exceed.
pub const AUDIT_MAX_LIMIT: i64 = 200;

/// Every field is optional; the zero value matches everything for the project.
#[derive(Debug, Clone, Default)]
pub struct Filter {
    /// Whose log.
    pub project_id: i64,
    /// One action, exactly.
    pub action: String,
    /// A set of actions — `kind=moderation` is a family, not a name.
    pub actions: Vec<String>,
    /// One actor.
    pub actor_id: i64,
    /// One entity type.
    pub entity_type: String,
    /// One entity.
    pub entity_id: i64,
    /// Free text across `detail` and `action`.
    pub text: String,
    /// Unix seconds, inclusive.
    pub since: f64,
    /// Rows to skip.
    pub offset: i64,
    /// Rows to return.
    pub limit: i64,
}

/// One audit row.
#[derive(Debug, Clone, Serialize)]
pub struct Entry {
    pub id: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub project_id: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub actor_id: Option<i64>,
    pub action: String,
    pub entity: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub entity_id: Option<i64>,
    pub detail: String,
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub created_at: f64,
    /// The act this entry reverses, when it is a reversal.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub undoes: Option<i64>,
    /// Some later act has reversed THIS one.
    pub undone: bool,
    /// The actor's display name — never the username, never the email.
    ///
    /// `users` has an email column and this log is meant to be read by anyone, so an email here would
    /// be a permanent exposure. A numeric actor id alone is useless to a reader, which is why the
    /// name is resolved at all.
    pub actor_display_name: Option<String>,
    /// Whether this caller may reverse the act. Always false for the anonymous reader: the flag is a
    /// hint for a viewer, and the restore route re-checks it, because a flag computed in a read is not
    /// a permission.
    pub can_undo: bool,
}

/// One page plus the total it was drawn from.
#[derive(Debug, Clone, Serialize)]
pub struct Page {
    pub entries: Vec<Entry>,
    pub total: i64,
    pub offset: i64,
    pub limit: i64,
}

/// The action families `kind` names. An unknown kind is an error rather than an empty filter: a log
/// that silently ignores `kind=moderaton` shows every row, which is indistinguishable from a filter
/// that matched everything.
#[must_use]
pub fn kind_actions(kind: &str) -> Option<Vec<String>> {
    match kind {
        "" => Some(Vec::new()),
        "moderation" => Some(
            ["arena.remove", "arena.restore", "feature.reject", "feature.unreject"]
                .iter()
                .map(|action| (*action).to_string())
                .collect(),
        ),
        _ => None,
    }
}

fn escape_like(text: &str) -> String {
    // Backslashes FIRST. Escaping them last would double the ones just inserted and turn `\%` into a
    // literal backslash followed by a live wildcard.
    text.replace('\\', "\\\\")
        .replace('%', "\\%")
        .replace('_', "\\_")
}

/// One page of a project's audit log, newest first.
///
/// Every parameter is bound as text and **cast in the SQL** (`$2::text`, `$3::bigint`,
/// `$4::bigint[]`). sqlx sends a bound `String` with the text type, and PostgreSQL has no
/// `bigint = text` operator, so a clause that compared an id to a text parameter would fail at
/// runtime rather than at compile time. The cast puts the type in the query where it can be read.
///
/// # Errors
/// [`sqlx::Error`] when a read fails.
pub async fn list(pool: &PgPool, filter: &Filter) -> Result<Page, sqlx::Error> {
    use sqlx::Row;
    let mut filter = filter.clone();
    // Two clamps. See the module comment: folding them silently answers the wrong page size.
    if filter.limit <= 0 {
        filter.limit = AUDIT_DEFAULT_LIMIT;
    }
    if filter.limit > AUDIT_MAX_LIMIT {
        filter.limit = AUDIT_MAX_LIMIT;
    }
    if filter.offset < 0 {
        filter.offset = 0;
    }

    let mut where_clauses = vec!["a.project_id = $1".to_string()];
    let mut texts: Vec<String> = Vec::new();
    let mut action_set: Option<Vec<String>> = None;
    let mut next = 2;
    if !filter.action.is_empty() {
        where_clauses.push(format!("a.action = ${next}::text"));
        texts.push(filter.action.clone());
        next += 1;
    }
    if !filter.actions.is_empty() {
        // One placeholder for the whole set: a placeholder per element invites the count of bind
        // values and the count of placeholders to disagree, which reads as a broken endpoint rather
        // than a broken filter.
        where_clauses.push(format!("a.action = any(${next}::text[])"));
        action_set = Some(filter.actions.clone());
        next += 1;
    }
    if filter.actor_id != 0 {
        where_clauses.push(format!("a.actor_id = ${next}::bigint"));
        texts.push(filter.actor_id.to_string());
        next += 1;
    }
    if !filter.entity_type.is_empty() {
        where_clauses.push(format!("a.entity = ${next}::text"));
        texts.push(filter.entity_type.clone());
        next += 1;
    }
    if filter.entity_id != 0 {
        where_clauses.push(format!("a.entity_id = ${next}::bigint"));
        texts.push(filter.entity_id.to_string());
        next += 1;
    }
    if filter.since > 0.0 {
        where_clauses.push(format!(
            "a.created_at >= to_timestamp(${next}::double precision)"
        ));
        texts.push(format!("{}", filter.since));
        next += 1;
    }
    if !filter.text.is_empty() {
        // An empty text emits no `LIKE` at all: `LIKE '%%'` matches every row, so a blanket clause
        // would return exactly the unfiltered rows at the cost of a scan.
        let escaped = format!("%{}%", escape_like(&filter.text));
        where_clauses.push(format!(
            "(a.detail like ${next}::text escape '\\' or a.action like ${next}::text escape '\\')"
        ));
        texts.push(escaped);
        next += 1;
    }
    let clause = where_clauses.join(" and ");

    // Counted BEFORE the rows are read. A client that pages on `limit` cannot tell "past the end"
    // from "nothing matched" without the total.
    let count_sql = format!("select count(*) from audit_log a where {clause}");
    let mut count = sqlx::query(&count_sql).bind(filter.project_id);
    for text in &texts {
        count = count.bind(text.clone());
    }
    if let Some(set) = &action_set {
        count = count.bind(set.clone());
    }
    let total: i64 = count.fetch_one(pool).await?.try_get(0)?;

    let rows_sql = format!(
        "select a.id, a.project_id, a.actor_id, a.action, a.entity, a.entity_id, a.detail, \
                extract(epoch from a.created_at)::double precision, a.undoes, \
                exists (select 1 from audit_log u where u.undoes = a.id) \
         from audit_log a where {clause} \
         order by a.created_at desc, a.id desc limit ${next} offset ${offset}",
        offset = next + 1,
    );
    let mut rows = sqlx::query(&rows_sql).bind(filter.project_id);
    for text in &texts {
        rows = rows.bind(text.clone());
    }
    if let Some(set) = &action_set {
        rows = rows.bind(set.clone());
    }
    let rows = rows.bind(filter.limit).bind(filter.offset).fetch_all(pool).await?;

    let mut entries = Vec::with_capacity(rows.len());
    let mut actor_ids: Vec<i64> = Vec::new();
    for row in &rows {
        let actor_id: Option<i64> = row.try_get(2)?;
        if let Some(actor) = actor_id {
            if !actor_ids.contains(&actor) {
                actor_ids.push(actor);
            }
        }
        entries.push(Entry {
            id: row.try_get(0)?,
            project_id: row.try_get(1)?,
            actor_id,
            action: row.try_get(3)?,
            entity: row.try_get::<Option<String>, _>(4)?.unwrap_or_default(),
            entity_id: row.try_get(5)?,
            // `detail` is nullable and writers pass "" as often as text, so NULL and "" are both live
            // states. Scanning NULL into a bare String fails the whole read, which would take the
            // viewer down over one row.
            detail: row.try_get::<Option<String>, _>(6)?.unwrap_or_default(),
            created_at: row.try_get(7)?,
            undoes: row.try_get(8)?,
            undone: row.try_get(9)?,
            actor_display_name: None,
            can_undo: false,
        });
    }

    // Names resolved after the rows are read, in ONE query for the whole page: a per-row lookup would
    // be N+1 on a page an auditor pages through repeatedly.
    if !actor_ids.is_empty() {
        let names: Vec<(i64, Option<String>)> =
            sqlx::query_as("select id, display_name from users where id = any($1)")
                .bind(&actor_ids)
                .fetch_all(pool)
                .await?;
        for entry in &mut entries {
            if let Some(actor) = entry.actor_id {
                // An id with no `users` row leaves the name null rather than inventing one.
                if let Some((_, name)) = names.iter().find(|(id, _)| *id == actor) {
                    entry.actor_display_name = name.clone();
                }
            }
        }
    }

    Ok(Page {
        entries,
        total,
        offset: filter.offset,
        limit: filter.limit,
    })
}

/// The distinct action names present in a project's log, alphabetical.
///
/// Alphabetical, not by frequency: a filter dropdown whose order shifts every time a vote lands is one
/// a user cannot learn, and the count of each is available from the unfiltered first page anyway.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn actions(pool: &PgPool, project_id: i64) -> Result<Vec<String>, sqlx::Error> {
    sqlx::query_scalar(
        "select distinct action from audit_log where project_id = $1 and action <> '' \
         order by action",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await
}
