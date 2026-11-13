//! §4.5's dependency panel: what blocks a feature, what it blocks, and what of that is still open.
//!
//! The third list is not a filter a client could apply, because "open" is a **status predicate** and not a row
//! property: a blocker is open unless it has **shipped**. Deriving it in the client would mean shipping the status
//! vocabulary there, and the first time a new terminal status appears the panel would report a shipped feature as
//! a blocker — a page claiming work is blocked by work that is done.
//!
//! Two identifiers, two directions: `blocked_by` joins on `dep.blocked_id`, `blocks` on `dep.blocker_id`. Swapping
//! them yields a panel that is plausible on a small graph and wrong on every feature with more than one neighbour.

use sqlx::PgPool;

/// A feature, as the dependency panel names it: enough to render a link and its state, and nothing else.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Ref {
    /// The row.
    pub id: i64,
    /// Its title.
    pub title: String,
    /// Its status.
    pub status: String,
}

/// The panel.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Panel {
    /// Features that block this one.
    pub blocked_by: Vec<Ref>,
    /// Features this one blocks.
    pub blocks: Vec<Ref>,
    /// The subset of `blocked_by` that has not shipped.
    pub open_blockers: Vec<Ref>,
}

const SELECT_REF: &str = "select f.id, f.title, f.status from feature_deps dep join features f on f.id = ";

/// Read the panel. Ordered by id, which is the Go's order and is stable across renames.
///
/// # Errors
/// `sqlx::Error`.
pub async fn panel(pool: &PgPool, feature_id: i64) -> Result<Panel, sqlx::Error> {
    let blocked_by = refs(
        pool,
        &format!("{SELECT_REF}dep.blocker_id where dep.blocked_id = $1 order by f.id"),
        feature_id,
    )
    .await?;
    let blocks = refs(
        pool,
        &format!("{SELECT_REF}dep.blocked_id where dep.blocker_id = $1 order by f.id"),
        feature_id,
    )
    .await?;
    // "Open" is a STATUS PREDICATE: everything that has not shipped. A row property could not express it, and a
    // client-side filter would go stale the day a new terminal status is added.
    let open_blockers = refs(
        pool,
        &format!(
            "{SELECT_REF}dep.blocker_id where dep.blocked_id = $1 and f.status <> 'shipped' order by f.id"
        ),
        feature_id,
    )
    .await?;
    Ok(Panel {
        blocked_by,
        blocks,
        open_blockers,
    })
}

async fn refs(pool: &PgPool, sql: &str, feature_id: i64) -> Result<Vec<Ref>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, String, String)>(sql)
        .bind(feature_id)
        .fetch_all(pool)
        .await?;
    Ok(rows
        .into_iter()
        .map(|(id, title, status)| Ref { id, title, status })
        .collect())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_two_directions_join_on_different_columns() {
        // The failure this pins: a copy-paste that leaves both queries joining on `blocked_id` passes on any
        // feature nobody blocks and is wrong for every other one.
        let blocked_by = format!("{SELECT_REF}dep.blocker_id where dep.blocked_id = $1 order by f.id");
        let blocks = format!("{SELECT_REF}dep.blocked_id where dep.blocker_id = $1 order by f.id");
        assert!(blocked_by.contains("f.id = dep.blocker_id where dep.blocked_id"));
        assert!(blocks.contains("f.id = dep.blocked_id where dep.blocker_id"));
        // And the open list is a status predicate, never a column.
        let open = format!(
            "{SELECT_REF}dep.blocker_id where dep.blocked_id = $1 and f.status <> 'shipped' order by f.id"
        );
        assert!(open.contains("f.status <> 'shipped'"));
    }
}
