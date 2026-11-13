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

/// One document, as the search returns it.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Document {
    /// The row.
    pub id: i64,
    /// Its project.
    pub project_id: i64,
    /// Its kind.
    pub kind: String,
    /// Its slug.
    pub slug: String,
    /// Its title.
    pub title: String,
    /// Its body.
    pub body: String,
    /// Its revision.
    pub revision: i64,
    /// Its author.
    pub author_id: i64,
    /// Created, unix seconds.
    pub created_at: f64,
    /// Updated, unix seconds.
    pub updated_at: f64,
}

/// The FTS operators the Go's pass-through rule looks for. A query containing any of them is a REAL expression and is
/// not rewritten: rewriting it would silently change what the caller asked for.
const FTS_OPERATORS: [&str; 6] = ["AND", "OR", "NOT", "NEAR", "(", ")"];

/// Whether a query should be treated as an FTS expression rather than a term.
#[must_use]
pub fn looks_like_expression(query: &str) -> bool {
    if query.contains(' ') || query.contains('"') || query.contains('(') || query.contains(')') {
        return true;
    }
    let upper = query.to_uppercase();
    FTS_OPERATORS.iter().any(|op| upper.contains(op))
}

/// The search: full text over a project's documents, best match first.
///
/// Three rules, all of them the Go's:
///
/// * **a bare term is a PHRASE, not an expression.** `language-neutral` under FTS5 means "language AND NOT neutral",
///   which silently returns the wrong rows for the most common query shape there is. A term with no space, quote,
///   paren or operator is quoted as a phrase. Anything else is passed through: a caller who writes `"exact phrase"
///   AND tag:x` must get expression semantics, and no inspection can tell the two apart.
/// * **limit**: `<= 0` or `> 100` becomes 20. The Go's, and the reason a page cannot ask for the whole corpus.
/// * **an empty query is an empty result, not an error** — but the ROUTE answers 400 for a missing `q`, because the
///   two are different requests.
///
/// **A CONTROL THAT DID NOT FAIL, and why that is the interesting result.** Forcing every query down the expression
/// path changes NO assertion: PostgreSQL's `websearch_to_tsquery` already reads a bare hyphen as a phrase separator,
/// where FTS5 reads it as NOT. So the quoting rule is REDUNDANT on this engine for the hyphen case that motivated it —
/// the engines already agree — and it is kept because `phraseto_tsquery` pins that agreement explicitly instead of
/// leaving it to be re-derived if the expression builder ever changes. The divergence to watch is elsewhere: FTS5 and
/// tsquery differ over stopwords, stemming and operator syntax, and only a corpus comparison can say whether that
/// matters.
///
/// The port searches `search_tsv` (a generated tsvector) where the Go searched its FTS5 table. The STOPWORDS differ
/// between the two engines and so does stemming — `simple` is used on both sides of the comparison so that a word
/// that matches matches for the same reason, and the parity corpus is what says whether that held.
///
/// # Errors
/// `sqlx::Error`, or a classified `ErrInvalidQuery` as a string when the search expression is malformed.
pub async fn search_documents(
    pool: &PgPool,
    project_id: i64,
    query: &str,
    limit: i64,
) -> Result<Result<Vec<Document>, String>, sqlx::Error> {
    let query = query.trim();
    if query.is_empty() {
        return Ok(Ok(Vec::new()));
    }
    let limit = if limit <= 0 || limit > 100 { 20 } else { limit };
    let expression = if looks_like_expression(query) {
        // A real expression goes to the expression parser; a failure there is the caller's mistake, not ours.
        format!("websearch_to_tsquery('simple', {})", quote_literal(query))
    } else {
        // Quoted, so a hyphen stays a hyphen instead of becoming NOT.
        format!("phraseto_tsquery('simple', {})", quote_literal(query))
    };
    let sql = format!(
        "select d.id, d.project_id, d.kind, d.slug, d.title, coalesce(d.body, ''), d.revision, d.author_id,
                extract(epoch from d.created_at)::double precision,
                extract(epoch from d.updated_at)::double precision
           from project_documents d
          where d.project_id = $1 and d.search_tsv @@ {expression}
          order by ts_rank(d.search_tsv, {expression}) desc, d.id asc
          limit $2"
    );
    let rows = match sqlx::query_as::<_, (i64, i64, String, String, String, String, i64, i64, f64, f64)>(&sql)
        .bind(project_id)
        .bind(limit)
        .fetch_all(pool)
        .await
    {
        Ok(rows) => rows,
        Err(error) => {
            let text = error.to_string();
            if text.contains("syntax error in tsquery") || text.contains("tsquery") {
                return Ok(Err(text));
            }
            return Err(error);
        }
    };
    Ok(Ok(rows
        .into_iter()
        .map(
            |(id, project_id, kind, slug, title, body, revision, author_id, created_at, updated_at)| Document {
                id,
                project_id,
                kind,
                slug,
                title,
                body,
                revision,
                author_id,
                created_at,
                updated_at,
            },
        )
        .collect()))
}

/// A SQL string literal. The query text is embedded rather than bound because it becomes part of a tsquery EXPRESSION,
/// not a value; doubling the quotes is the only escape PostgreSQL needs in a standard literal.
fn quote_literal(text: &str) -> String {
    format!("'{}'", text.replace('\'', "''"))
}

#[cfg(test)]
mod search_tests {
    use super::*;

    #[test]
    fn a_bare_hyphenated_term_is_a_phrase_and_an_expression_is_left_alone() {
        // The failure this exists to prevent: `language-neutral` read as a boolean returns every "language" document
        // that never mentions neutrality.
        assert!(!looks_like_expression("language-neutral"));
        assert!(looks_like_expression("language neutral"));
        assert!(looks_like_expression("\"exact phrase\""));
        assert!(looks_like_expression("tag:x AND tag:y"));
        assert!(looks_like_expression("NOT archived"));
        assert!(looks_like_expression("(a OR b)"));
    }

    #[test]
    fn a_quote_in_the_term_cannot_break_out_of_the_literal() {
        assert_eq!(quote_literal("it's"), "'it''s'");
    }
}
