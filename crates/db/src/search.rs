//! §15's discovery: `GET /api/v1/search`.
//!
//! The shape is the Go handler's, field for field — the filters echo back, every hit carries its project row
//! plus a `score`, and the facets are computed over the hits rather than over the whole table (a facet that
//! counts rows the reader cannot see is a leak dressed as a filter).
//!
//! # The one thing that cannot be reproduced, stated rather than hidden
//!
//! The Go index is an fts5 virtual table ranked by **bm25**; PostgreSQL ranks with **`ts_rank`**. Both order
//! documents by how well they match, and the two orders agree on most queries, but the numbers are not the same
//! numbers and no amount of care makes them so. The port keeps the *shape* (a `score` on every hit, hits
//! ordered by it) and declares the values divergent, because a client uses the order and a number forged to
//! look like another database's is worse than an honest one from this one.
//!
//! The other half is coverage: `projects_fts` indexed name, description, **tags** and **languages**, while the
//! baseline's generated `search_tsv` covers only the two columns of `projects`. Free text therefore also
//! matches a tag or a language name here, through an `EXISTS`, so a search for `rust` finds the projects
//! tagged `rust` as the Go index did.

use serde::{Deserialize, Serialize};
use sqlx::{PgPool, Row};

use crate::projects::Project;

/// A bound argument, typed: PostgreSQL refuses to compare a bigint column to a text parameter, and the
/// actor id in the visibility predicate is the one bind that is not a string.
enum Arg {
    /// A row id or a user id.
    Int(i64),
    /// A name, a state, a query, or a pattern.
    Text(String),
}

/// The filters the handler echoes back.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct SearchFilters {
    /// Every tag must match.
    pub tags: Option<Vec<String>>,
    /// Every language must match.
    pub languages: Option<Vec<String>>,
    /// Minimum percentage for each language filter.
    ///
    /// Serialized through `ser_unix` for the same reason every other float here is: Go's encoder prints `0`
    /// and serde prints `0.0`, and a filter that echoes back differently from the one that was sent is a
    /// difference a client notices.
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub min_lang_pct: f64,
    /// Minimum health score in `[0,1]`; zero means any.
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub min_health: f64,
    /// `collective`, `maintainer_led`, or empty for any.
    pub model: String,
    /// An exact license string, or empty.
    pub license: String,
}

/// One hit: the project row, plus how well it matched.
#[derive(Debug, Clone, Serialize)]
pub struct ProjectHit {
    /// The project.
    #[serde(flatten)]
    pub project: Project,
    /// The rank, higher is better. Not comparable to the Go build's bm25 number, by design.
    #[serde(serialize_with = "crate::projects::ser_unix")]
    pub score: f64,
}

/// One facet value and how many of the hits carry it.
#[derive(Debug, Clone, Serialize)]
pub struct FacetCount {
    /// The value.
    pub value: String,
    /// How many hits carry it.
    pub count: i64,
}

/// The facets over the hits.
#[derive(Debug, Clone, Default, Serialize)]
pub struct Facets {
    /// Tags, by frequency.
    pub tags: Vec<FacetCount>,
    /// Languages, by frequency.
    pub languages: Vec<FacetCount>,
    /// Licenses, by frequency.
    pub licenses: Vec<FacetCount>,
    /// Governance models, by frequency.
    pub models: Vec<FacetCount>,
}

/// The whole search answer.
#[derive(Debug, Clone, Serialize)]
pub struct SearchResult {
    /// The query, echoed.
    pub query: String,
    /// The filters, echoed.
    pub filters: SearchFilters,
    /// How many hits.
    pub total: i64,
    /// The sort that was applied.
    pub sort: String,
    /// The hits.
    pub results: Vec<ProjectHit>,
    /// Facets over the hits.
    pub facets: Facets,
}

/// The projects this actor may be shown.
///
/// The rule the Go store keeps in ONE constant and the list, the search and the duplicate panel share: public
/// always; unlisted and private when you are a member; protected when you are a member or hold an unredeemed
/// invite. Three copies of this in the product is how one of them ends up showing a private project.
const VISIBLE: &str = "p.visibility = 'public'
   OR (p.visibility IN ('unlisted','private') AND EXISTS (
         SELECT 1 FROM members mm WHERE mm.project_id = p.id AND mm.user_id = $ACTOR))
   OR (p.visibility = 'protected' AND (
         EXISTS (SELECT 1 FROM members mm2 WHERE mm2.project_id = p.id AND mm2.user_id = $ACTOR)
      OR EXISTS (SELECT 1 FROM invite_redemptions ir
                 JOIN project_invites i ON i.id = ir.invite_id
                 WHERE i.project_id = p.id AND ir.user_id = $ACTOR)))";

/// Does this text match the project's name, description, tags or languages?
///
/// The tag and language halves are what `projects_fts` indexed and the generated tsvector does not, so they are
/// asked separately rather than left out. A miss here is a behaviour change in the direction of "the search
/// got worse", which is the kind of quiet regression a port is supposed to avoid.
const MATCHES: &str = "(p.search_tsv @@ plainto_tsquery('simple', $QUERY)
     OR EXISTS (SELECT 1 FROM project_tags pt JOIN tags t ON t.id = pt.tag_id
                 WHERE pt.project_id = p.id AND t.name ILIKE '%' || $QUERY || '%')
     OR EXISTS (SELECT 1 FROM project_languages pl
                 WHERE pl.project_id = p.id AND pl.language ILIKE '%' || $QUERY || '%'))";

/// Bind a value and answer its placeholder, so a predicate and its argument cannot get out of step.
fn push_arg(binds: &mut Vec<Arg>, value: Arg) -> String {
    binds.push(value);
    format!("${}", binds.len())
}

/// Run one search.
///
/// # Errors
/// `sqlx::Error`.
pub async fn search(
    pool: &PgPool,
    query: &str,
    filters: &SearchFilters,
    sort: &str,
    actor_id: i64,
) -> Result<SearchResult, sqlx::Error> {
    let query = query.trim().to_string();
    let mut wheres: Vec<String> = Vec::new();
    let mut binds: Vec<Arg> = Vec::new();

    if !query.is_empty() {
        let placeholder = push_arg(&mut binds, Arg::Text(query.clone()));
        wheres.push(MATCHES.replace("$QUERY", &placeholder));
    }
    for tag in filters.tags.clone().unwrap_or_default() {
        let placeholder = push_arg(&mut binds, Arg::Text(tag.trim().to_lowercase()));
        wheres.push(format!(
            "EXISTS (SELECT 1 FROM project_tags pt JOIN tags t ON t.id = pt.tag_id \
              WHERE pt.project_id = p.id AND t.name = {placeholder})"
        ));
    }
    for language in filters.languages.clone().unwrap_or_default() {
        let placeholder = push_arg(&mut binds, Arg::Text(language.trim().to_string()));
        wheres.push(format!(
            "EXISTS (SELECT 1 FROM project_languages pl \
              WHERE pl.project_id = p.id AND pl.language = {placeholder} AND pl.pct >= {})",
            filters.min_lang_pct
        ));
    }
    if filters.min_health > 0.0 {
        wheres.push(format!(
            "coalesce(m.health_score, 0) >= {}",
            filters.min_health
        ));
    }
    if !filters.model.is_empty() {
        let placeholder = push_arg(&mut binds, Arg::Text(filters.model.clone()));
        wheres.push(format!("p.governance_model = {placeholder}"));
    }
    if !filters.license.is_empty() {
        let placeholder = push_arg(&mut binds, Arg::Text(filters.license.clone()));
        wheres.push(format!("p.license = {placeholder}"));
    }

    // VISIBILITY GOES LAST, and the ordering is load-bearing rather than cosmetic: placeholders are bound
    // positionally, so appending this predicate earlier would bind the actor id to a filter's slot.
    let visible_placeholder = push_arg(&mut binds, Arg::Int(actor_id));
    wheres.push(format!("({})", VISIBLE.replace("$ACTOR", &visible_placeholder)));

    // The rank reads the SAME placeholder the match used, so the two can never disagree about the query.
    let score = if query.is_empty() {
        "0::double precision".to_string()
    } else {
        format!(
            "ts_rank(p.search_tsv, plainto_tsquery('simple', $1))::double precision as score"
        )
    };
    let order = match sort {
        "updated" => "p.updated_at desc",
        "health" => "coalesce(m.health_score, 0) desc, p.updated_at desc",
        "newest" => "p.created_at desc",
        _ => {
            if query.is_empty() {
                "p.updated_at desc"
            } else {
                "score desc, p.updated_at desc"
            }
        }
    };

    let where_clause = if wheres.is_empty() {
        String::new()
    } else {
        format!(" where {}", wheres.join(" and "))
    };
    let sql = format!(
        "select {}, {score} from projects p \
         left join project_metrics m on m.project_id = p.id{where_clause} order by {order}",
        crate::projects::PROJECT_COLUMNS
    );

    let mut statement = sqlx::query(&sql);
    for bind in &binds {
        statement = match bind {
            Arg::Int(value) => statement.bind(*value),
            Arg::Text(value) => statement.bind(value.clone()),
        };
    }
    let rows = statement.fetch_all(pool).await?;

    let mut results = Vec::with_capacity(rows.len());
    for row in &rows {
        let project = crate::projects::project_from_row(row)?;
        results.push(ProjectHit {
            project,
            score: row.try_get("score")?,
        });
    }
    let total = results.len() as i64;
    let ids: Vec<i64> = results.iter().map(|hit| hit.project.id).collect();
    let facets = facets_for(pool, &ids).await?;

    Ok(SearchResult {
        query,
        filters: filters.clone(),
        total,
        sort: sort.to_string(),
        results,
        facets,
    })
}

/// Facets over the hits, not over the table: a facet that counts rows the reader cannot see is a leak.
async fn facets_for(pool: &PgPool, ids: &[i64]) -> Result<Facets, sqlx::Error> {
    if ids.is_empty() {
        return Ok(Facets::default());
    }
    let tags = facet(
        pool,
        "select t.name, count(*) from project_tags pt join tags t on t.id = pt.tag_id \
          where pt.project_id = any($1) group by t.name order by count(*) desc, t.name",
        ids,
    )
    .await?;
    let languages = facet(
        pool,
        "select language, count(*) from project_languages where project_id = any($1) \
         group by language order by count(*) desc, language",
        ids,
    )
    .await?;
    let licenses = facet(
        pool,
        "select coalesce(nullif(license, ''), 'unknown'), count(*) from projects \
          where id = any($1) group by 1 order by count(*) desc, 1",
        ids,
    )
    .await?;
    let models = facet(
        pool,
        "select governance_model, count(*) from projects where id = any($1) \
         group by governance_model order by count(*) desc, governance_model",
        ids,
    )
    .await?;
    Ok(Facets {
        tags,
        languages,
        licenses,
        models,
    })
}

async fn facet(pool: &PgPool, sql: &str, ids: &[i64]) -> Result<Vec<FacetCount>, sqlx::Error> {
    let rows = sqlx::query(sql).bind(ids).fetch_all(pool).await?;
    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        out.push(FacetCount {
            value: row.try_get(0)?,
            count: row.try_get(1)?,
        });
    }
    Ok(out)
}
