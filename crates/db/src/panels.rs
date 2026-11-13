//! Two small reads the feature page and the document reader need: the feature's tags and the document kinds.
//!
//! `documents/kinds` is a STATIC list on purpose — the kinds are the schema's own vocabulary (`store.DocumentKinds`),
//! and deriving them with `select distinct kind` would let a typo in one row invent a kind the writer would then
//! validate against. The route exists so the frontend stops hard-coding the list in a second place.

use sqlx::PgPool;

/// The document kinds, in the Go's order.
pub const DOCUMENT_KINDS: [&str; 7] = ["readme", "spec", "plan", "wiki", "adr", "changelog", "scout"];

/// A feature's tags, alphabetical — `ORDER BY t.name` is the Go's, and it is the order a reader scans.
///
/// # Errors
/// `sqlx::Error`.
pub async fn feature_tags(pool: &PgPool, feature_id: i64) -> Result<Vec<String>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (String,)>(
        "select t.name from feature_tags ft join tags t on t.id = ft.tag_id
          where ft.feature_id = $1 order by t.name",
    )
    .bind(feature_id)
    .fetch_all(pool)
    .await?;
    Ok(rows.into_iter().map(|(name,)| name).collect())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_kind_list_is_the_schema_vocabulary_and_is_ordered_as_the_go_had_it() {
        assert_eq!(
            DOCUMENT_KINDS,
            ["readme", "spec", "plan", "wiki", "adr", "changelog", "scout"]
        );
    }
}
