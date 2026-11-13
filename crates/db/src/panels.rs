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

/// One piece of evidence that a feature is worth building: a document or a field report.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Evidence {
    /// `document` or `field_report` — which table `ref_id` points at.
    pub kind: String,
    /// The row in that table.
    pub ref_id: i64,
    /// The document's title, or the report's version. EMPTY when the thing it names is gone.
    pub title: String,
    /// Linked, unix seconds.
    pub created_at: f64,
}

/// A feature's evidence, newest first.
///
/// **A `LEFT JOIN` on purpose.** Evidence whose document or report was deleted keeps its row and is listed with an
/// empty title: the link is a historical fact ("this is why the feature exists"), and an inner join would silently
/// rewrite that history by making the evidence vanish. The panel then reads as "nobody linked anything", which is a
/// different and wrong claim.
///
/// # Errors
/// `sqlx::Error`.
pub async fn evidence(pool: &PgPool, feature_id: i64) -> Result<Vec<Evidence>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (String, i64, String, f64)>(
        "select ev.kind, ev.ref_id,
                case ev.kind when 'document' then coalesce(d.title, '')
                              else coalesce(r.version, '') end,
                extract(epoch from ev.created_at)::double precision
           from feature_evidence ev
           left join project_documents d on ev.kind = 'document'    and d.id = ev.ref_id
           left join field_reports     r on ev.kind = 'field_report' and r.id = ev.ref_id
          where ev.feature_id = $1
          order by ev.created_at desc, ev.ref_id desc",
    )
    .bind(feature_id)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(kind, ref_id, title, created_at)| Evidence {
            kind,
            ref_id,
            title,
            created_at,
        })
        .collect())
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
