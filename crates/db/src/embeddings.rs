//! Vectors in the database: write one, find the near ones, hydrate what matched.
//!
//! Four rules here are not obvious and each one cost the service a real defect:
//!
//! * **two fields per entity, and the max over them.** A filer checks with the title they have typed; the stored row was
//!   indexed as title+body. Compared against the concatenation an identical complaint scored 0.58 — below the advisory
//!   band — while title-to-title scored 1.00. So the title is embedded separately and a match is the **maximum** over
//!   fields, never an average: averaging lets a strong title match be cancelled by a weak body.
//! * **`HasContent`, not a length check.** The hashed embedder always returns a full-width slice, and stop-words-only text
//!   produces all zeros. Storing that vector occupies an index slot forever, scores 0 against everything, and makes "is
//!   this indexed?" checks that count rows lie.
//! * **the visibility clause is not a caller's business.** Without it `/api/v1/similar` was an oracle: anonymous callers
//!   could submit a sentence, learn whether a matching row existed in *any* project including private ones, and read back
//!   its title, status and id. On the write path it was worse — a filer was refused a filing (409) and shown the title of
//!   something they cannot read.
//! * **an empty or unindexable query returns nothing, not everything.** Every candidate would score 0, so the honest
//!   answer to "does this exist?" for text with no vector is "I cannot tell", which is an empty list.

use concord_embed::{cosine, decode, encode, has_content, rank, thresholds_for, Embedder, Similarity};
use sqlx::{PgPool, Row};

/// A complaint.
pub const KIND_COMPLAINT: &str = "complaint";
/// A feature.
pub const KIND_FEATURE: &str = "feature";
/// A request.
pub const KIND_REQUEST: &str = "request";
/// A project.
pub const KIND_PROJECT: &str = "project";

/// The title-only vector.
pub const FIELD_TITLE: &str = "title";
/// The title+body vector.
pub const FIELD_FULL: &str = "full";

/// The projects whose rows this actor may read, as a SQL predicate over the alias `p`.
///
/// One predicate, used by both the similarity search and the visibility rules, because two copies of a visibility rule
/// disagree eventually and the disagreement is a leak.
///
/// The actor is `$3`: `$1` is the kind, `$2` the model id. The reference passed the actor three times because SQLite's
/// placeholder style made that free; here the number is written once and every use of it must agree with the bind order,
/// or PostgreSQL answers `operator does not exist: bigint = text` — which is how this was found.
const VISIBLE_PROJECTS: &str = " (p.visibility = 'public'
       OR (p.visibility IN ('unlisted','private') AND EXISTS (
             SELECT 1 FROM members mm WHERE mm.project_id = p.id AND mm.user_id = $3))
       OR (p.visibility = 'protected' AND (
             EXISTS (SELECT 1 FROM members mm2 WHERE mm2.project_id = p.id AND mm2.user_id = $3)
          OR EXISTS (SELECT 1 FROM invite_redemptions ir
                       JOIN project_invites i ON i.id = ir.invite_id
                      WHERE i.project_id = p.id AND ir.user_id = $3))))";

/// Store or replace one vector for one entity field.
///
/// Replace rather than accumulate is the point of the `(kind, entity_id, field)` key: a re-embed after an edit must not
/// leave the previous vector competing with the current one.
///
/// # Errors
/// `sqlx::Error`.
pub async fn upsert_field(
    pool: &PgPool,
    embedder: &dyn Embedder,
    kind: &str,
    entity_id: i64,
    project_id: i64,
    field: &str,
    source: &str,
) -> Result<(), sqlx::Error> {
    let vector = embedder.embed(source).await;
    if !has_content(&vector) {
        // No vector means no searchable content. Removing the row is better than leaving a stale one: a previous title
        // would keep matching after the text became empty.
        sqlx::query("delete from embeddings where kind = $1 and entity_id = $2 and field = $3")
            .bind(kind)
            .bind(entity_id)
            .bind(field)
            .execute(pool)
            .await?;
        return Ok(());
    }
    // `0` means "no project" and must be NULL, not a zero foreign value: the search joins `projects` on this column, and
    // a 0 would silently make an instance-wide row unmatchable *and* unaccounted for.
    let project: Option<i64> = if project_id > 0 { Some(project_id) } else { None };
    sqlx::query(
        "insert into embeddings (kind, entity_id, field, project_id, vector, dims, model_id, source, updated_at)
         values ($1, $2, $3, $4, $5, $6, $7, $8, now())
         on conflict (kind, entity_id, field) do update set
             project_id = excluded.project_id,
             vector     = excluded.vector,
             dims       = excluded.dims,
             model_id   = excluded.model_id,
             source     = excluded.source,
             updated_at = excluded.updated_at",
    )
    .bind(kind)
    .bind(entity_id)
    .bind(field)
    .bind(project)
    .bind(encode(&vector))
    .bind(i64::try_from(vector.len()).unwrap_or(i64::MAX))
    .bind(embedder.id())
    .bind(source)
    .execute(pool)
    .await?;
    Ok(())
}

/// Index both the title and the full text of an entity, which is what every create path wants.
///
/// # Errors
/// `sqlx::Error`.
pub async fn upsert_entity_text(
    pool: &PgPool,
    embedder: &dyn Embedder,
    kind: &str,
    entity_id: i64,
    project_id: i64,
    title: &str,
    body: &str,
) -> Result<(), sqlx::Error> {
    upsert_field(pool, embedder, kind, entity_id, project_id, FIELD_TITLE, title).await?;
    upsert_field(
        pool,
        embedder,
        kind,
        entity_id,
        project_id,
        FIELD_FULL,
        &format!("{title} {body}"),
    )
    .await
}

/// Remove an entity's vectors, for a delete path: similarity must not keep pointing at rows that no longer exist.
///
/// # Errors
/// `sqlx::Error`.
pub async fn delete_for(pool: &PgPool, kind: &str, entity_id: i64) -> Result<(), sqlx::Error> {
    sqlx::query("delete from embeddings where kind = $1 and entity_id = $2")
        .bind(kind)
        .bind(entity_id)
        .execute(pool)
        .await?;
    Ok(())
}

/// The entities most similar to `text`.
///
/// * `project_id > 0` restricts to that project; `0` searches the whole instance, which is §6.1's *"this was fixed in
///   X"* case;
/// * `actor_id` is what stops the instance-wide case from being an oracle — only projects the actor may read contribute
///   candidates. `0` is anonymous, which still sees public and unlisted projects: the panel is unauthenticated on
///   purpose and must keep working;
/// * `field` is `Some(FIELD_TITLE)` to compare against title vectors, `Some(FIELD_FULL)` for full text, `None` for the
///   best score over all fields (what the pre-submit panel wants, since the filer usually holds only a title);
/// * `limit == 0` means no limit.
///
/// Results below the **current model's** weak band are omitted: a panel of ten weak candidates teaches people to ignore
/// it. The band is per model because the hashed and nomic scales are not comparable.
///
/// # Errors
/// `sqlx::Error`.
pub async fn find_similar(
    pool: &PgPool,
    embedder: &dyn Embedder,
    kind: &str,
    text: &str,
    project_id: i64,
    actor_id: i64,
    field: Option<&str>,
    limit: usize,
) -> Result<Vec<Similarity>, sqlx::Error> {
    let query = embedder.embed(text).await;
    if !has_content(&query) {
        return Ok(Vec::new());
    }

    // Every vector is loaded before any scoring: one query, then arithmetic. Scoring inside the row loop would hold the
    // cursor open across more queries for no gain.
    let mut sql = format!(
        "select e.entity_id, e.field, e.vector, e.dims
           from embeddings e
           join projects p on p.id = e.project_id
          where e.kind = $1 and e.model_id = $2 and {VISIBLE_PROJECTS}"
    );
    if project_id > 0 {
        sql.push_str(" and e.project_id = $4");
    }
    let mut statement = sqlx::query(&sql).bind(kind).bind(embedder.id()).bind(actor_id);
    if project_id > 0 {
        statement = statement.bind(project_id);
    }
    let rows = statement.fetch_all(pool).await?;

    // Max over fields, keyed by entity. Max and not average, for the reason in the module note.
    let mut best: std::collections::BTreeMap<i64, f64> = std::collections::BTreeMap::new();
    for row in rows {
        let entity_id: i64 = row.try_get("entity_id")?;
        let row_field: String = row.try_get("field")?;
        let blob: Vec<u8> = row.try_get("vector")?;
        let dims: i64 = row.try_get("dims")?;
        // A dimension mismatch is a model change that was not re-backfilled: skip it rather than score nonsense.
        if dims != i64::try_from(query.len()).unwrap_or(-1) {
            continue;
        }
        if let Some(wanted) = field {
            if row_field != wanted {
                continue;
            }
        }
        let vector = decode(&blob);
        if vector.is_empty() {
            continue;
        }
        let score = cosine(&query, &vector);
        let entry = best.entry(entity_id).or_insert(f64::MIN);
        if score > *entry {
            *entry = score;
        }
    }

    let (_, _, weak) = thresholds_for(&embedder.id());
    let scored: Vec<Similarity> = best
        .into_iter()
        .filter(|(_, score)| *score >= weak)
        .map(|(entity_id, score)| Similarity {
            kind: kind.to_string(),
            entity_id,
            score,
            title: String::new(),
            status: String::new(),
            url: None,
        })
        .collect();
    let ranked = rank(scored, limit);
    hydrate(pool, kind, ranked).await
}

/// Fill in the title, status and URL that make a match actionable.
///
/// A missing row is **skipped** rather than returned with an empty title: the table is denormalised, so an entity deleted
/// by a path that skipped [`delete_for`] leaves a stale row, and showing "complaint #412 — " is worse than not showing it.
///
/// The URL is a **page** the filer can open, built from the row's own project slug rather than guessed at the API's shape.
/// It used to be `/api/v1/complaints/%d` and friends — none of which exists; the panel this endpoint is for had not been
/// built, so nothing ever followed one.
///
/// # Errors
/// `sqlx::Error`.
pub async fn hydrate(
    pool: &PgPool,
    kind: &str,
    sims: Vec<Similarity>,
) -> Result<Vec<Similarity>, sqlx::Error> {
    let mut out = Vec::with_capacity(sims.len());
    for mut sim in sims {
        let (sql, url_template): (&str, &str) = match kind {
            KIND_COMPLAINT => (
                "select c.title, c.status, p.slug from complaints c
                   join projects p on p.id = c.project_id where c.id = $1",
                "/projects/{slug}/complaints/{id}",
            ),
            KIND_FEATURE => (
                "select f.title, f.status, p.slug from features f
                   join projects p on p.id = f.project_id where f.id = $1",
                "/projects/{slug}/features/{id}",
            ),
            KIND_REQUEST => (
                // A request may be instance-wide and there is no page for one: LEFT JOIN so the row still hydrates, and
                // the URL stays empty rather than pointing at a route that does not exist.
                "select r.title, r.status, coalesce(p.slug, '') from requests r
                   left join projects p on p.id = r.project_id where r.id = $1",
                "/projects/{slug}/requests/{id}",
            ),
            // A kind with no hydration table: returned with its score so the caller still learns a match exists.
            _ => {
                out.push(sim);
                continue;
            }
        };
        let row = sqlx::query(sql).bind(sim.entity_id).fetch_optional(pool).await?;
        let Some(row) = row else {
            continue;
        };
        let title: Option<String> = row.try_get(0)?;
        let status: Option<String> = row.try_get(1)?;
        let slug: String = row.try_get(2)?;
        sim.title = title.unwrap_or_default();
        sim.status = status.unwrap_or_default();
        if !slug.is_empty() {
            sim.url = Some(
                url_template
                    .replace("{slug}", &slug)
                    .replace("{id}", &sim.entity_id.to_string()),
            );
        }
        out.push(sim);
    }
    Ok(out)
}

/// How much of a kind is indexed, as the panel reports it.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Coverage {
    /// The entity kind.
    pub kind: String,
    /// The model whose vectors were counted. A re-embed that has not run yet leaves old rows that the search skips.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub model_id: Option<String>,
    /// Rows that could be embedded. Rows with nothing to embed are **excluded**, see the note below.
    pub total: i64,
    /// Distinct entities with at least one vector under this model.
    pub embedded: i64,
    /// Rows with nothing to embed, reported so "555/556" is explainable rather than mysterious.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub unembeddable: Option<i64>,
    /// `embedded / total`, as a fraction.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub coverage: Option<f64>,
    /// Whether every embeddable row is indexed.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub complete: Option<bool>,
    /// Set when there is no backing table for the kind.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub indexed: Option<bool>,
    /// Why there is no number.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}

/// Count how much of `kind` the index covers.
///
/// Two decisions that make the number mean something:
///
/// * rows with **nothing to embed** are excluded from the denominator and reported separately. Counting them made
///   coverage permanently unreachable — one complaint with an empty title *and* body held the whole instance at 99.8%
///   forever, and a permanent "incomplete" warning trains an operator to ignore the warning;
/// * `count(distinct entity_id)`, not `count(*)`: each entity has up to two vectors, so a row count reports double the
///   coverage and then calls an unindexed entity covered because its sibling field is.
///
/// # Errors
/// `sqlx::Error`.
pub async fn coverage(
    pool: &PgPool,
    embedder: &dyn Embedder,
    kind: &str,
) -> Result<Coverage, sqlx::Error> {
    let table = match kind {
        KIND_COMPLAINT => "complaints",
        KIND_FEATURE => "features",
        KIND_REQUEST => "requests",
        KIND_PROJECT => "projects",
        _ => {
            return Ok(Coverage {
                kind: kind.to_string(),
                model_id: None,
                total: 0,
                embedded: 0,
                unembeddable: None,
                coverage: None,
                complete: None,
                indexed: Some(false),
                note: Some("no backing table for this kind yet".to_string()),
            });
        }
    };
    // The same `trim(...) = ''` test the writer uses, so the denominator matches the writer exactly rather than
    // approximately: two predicates that disagree about which rows are skippable produce a number that lies both ways.
    let text = match kind {
        KIND_PROJECT => "name || ' ' || coalesce(description, '')",
        _ => "title || ' ' || coalesce(body, '')",
    };
    let mut total: i64 =
        sqlx::query_scalar(&format!("select count(*) from {table}"))
            .fetch_one(pool)
            .await?;
    let unembeddable: i64 =
        sqlx::query_scalar(&format!("select count(*) from {table} where trim({text}) = ''"))
            .fetch_one(pool)
            .await?;
    total = (total - unembeddable).max(0);
    let embedded: i64 = sqlx::query_scalar(
        "select count(distinct entity_id) from embeddings where kind = $1 and model_id = $2",
    )
    .bind(kind)
    .bind(embedder.id())
    .fetch_one(pool)
    .await?;
    let fraction = if total > 0 {
        #[allow(clippy::cast_precision_loss)]
        {
            embedded as f64 / total as f64
        }
    } else {
        0.0
    };
    Ok(Coverage {
        kind: kind.to_string(),
        model_id: Some(embedder.id()),
        total,
        embedded,
        unembeddable: Some(unembeddable),
        coverage: Some(fraction),
        complete: Some(embedded >= total),
        indexed: Some(true),
        note: None,
    })
}
