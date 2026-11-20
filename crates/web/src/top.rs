//! The pages that are about the INSTANCE rather than one project: search, statistics, tags, and Scout's door.
//!
//! Each of them answers a question a stranger arrives with, and each is a rendered document — the query is in the URL, the answer is in
//! the HTML, and no JavaScript is required to read it. That is not nostalgia for the Go instance: a search result that exists only
//! after hydration is a result no crawler indexes and no reader with a failed script sees.
//!
//! Three rules, and they are rules because each one was nearly a bug:
//!
//! * **an empty result says how empty.** "No projects match" and "your filters excluded everything" are different sentences; the
//!   page prints the filters it applied and the total it found;
//! * **a number with no measurement behind it is an em dash, never a zero** — the same rule the badge and the project pages follow;
//! * **the page is a GET with its whole state in the URL**, so a reader can bookmark, share and reload it.

use axum::{
    extract::{Query, State},
    http::HeaderMap,
    response::Response,
};
use serde::Deserialize;
use sqlx::PgPool;

use crate::html::Html;
use crate::{render, Page};

/// `/search?q=…&tag=…&model=…&sort=…`
#[derive(Debug, Default, Deserialize)]
pub struct SearchQuery {
    #[serde(default)]
    pub q: String,
    #[serde(default)]
    pub tag: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub sort: String,
}

/// `/search` — the catalogue, filtered, with the filters printed back.
pub async fn search(
    State(pool): State<PgPool>,
    Query(request): Query<SearchQuery>,
    headers: HeaderMap,
) -> Response {
    let filters = concord_db::search::SearchFilters {
        tags: if request.tag.trim().is_empty() { None } else { Some(vec![request.tag.clone()]) },
        languages: None,
        min_lang_pct: 0.0,
        min_health: 0.0,
        model: request.model.clone(),
        license: String::new(),
    };
    let sort = if request.sort.is_empty() { "relevance" } else { request.sort.as_str() };
    let result = match concord_db::search::search(&pool, &request.q, &filters, sort, 0).await {
        Ok(result) => result,
        Err(error) => return crate::error_page(&headers, "the search could not be run", &error.to_string()),
    };

    let mut body = Html::new();
    body.open("div", &[("class", "page-head")])
        .open("div", &[("class", "page-head-main")])
        .open("span", &[("class", "page-eyebrow")]).text("Discover").close("span")
        .open("h1", &[("class", "page-title-lg")]).text("Search").close("h1")
        .open("p", &[("class", "page-lede")])
        .text("Find a project by name, description, tag or language.").close("p")
        .close("div")
        .close("div");
    // The form is a GET so the state lives in the URL: a result a reader cannot bookmark is a result they have to re-enter.
    body.open("form", &[("method", "get"), ("action", "/search"), ("class", "search-form"), ("role", "search")])
        .open("label", &[("for", "q")]).text("Search projects").close("label")
        .void("input", &[("type", "search"), ("id", "q"), ("name", "q"), ("value", &request.q),
                         ("placeholder", "name, description, tag or language")])
        .void("input", &[("type", "text"), ("name", "tag"), ("value", &request.tag), ("placeholder", "tag")])
        .open("button", &[("type", "submit"), ("class", "btn btn-primary")]).text("Search").close("button")
        .close("form");

    body.open("p", &[("class", "muted"), ("data-total", &result.total.to_string())])
        // An empty query is a BROWSE, not an error: the search read has no `WHERE` when the query is empty, so the page lists the
        // whole visible catalogue and says that is what it is doing. Telling a reader "nothing has been searched for" while
        // showing 78 projects under it would be a page contradicting itself in two adjacent lines.
        .text(&match (result.total, request.q.trim().is_empty()) {
            (0, _) => format!("No project matches {:?}.", request.q),
            (total, true) => format!("{total} project(s) in the catalogue — every one you may see."),
            (total, false) => format!("{total} project(s) match."),
        })
        .close("p");
    if !request.tag.trim().is_empty() || !request.model.trim().is_empty() {
        body.open("p", &[("class", "muted")])
            .text(&format!(
                "Filters applied: tag={:?} model={:?} sort={:?}",
                request.tag, request.model, result.sort
            ))
            .close("p");
    }

    if !result.results.is_empty() {
        body.open("ul", &[("class", "project-list"), ("data-grid", "browse")]);
        for hit in &result.results {
            body.open("li", &[])
                .open("a", &[("href", &format!("/projects/{}", hit.project.slug))])
                .text(&hit.project.name)
                .close("a");
            // Health only when measured: `min_health` filters on a score, so a hit with no score is a hit whose position the
            // reader cannot check — saying so is more useful than printing 0.00.
            match hit.project.health_score {
                Some(score) => body.open("p", &[("class", "project-card-desc")])
                    .open("span", &[("class", "health-pill")])
                    .text(&format!("health {score:.2}")).close("span").close("p"),
                None => body.open("p", &[("class", "project-card-desc")])
                    .open("span", &[("class", "health-pill health-unmeasured")])
                    .text("health unmeasured").close("span").close("p"),
            };
            body.close("li");
        }
        body.close("ul");
    }

    let bootstrap = serde_json::json!({
        "route": "/search",
        "query": result.query,
        "total": result.total,
        "sort": result.sort,
        "results": result.results.iter().map(|hit| serde_json::json!({
            "slug": hit.project.slug, "name": hit.project.name,
        })).collect::<Vec<_>>(),
    });
    let title = if request.q.trim().is_empty() { "Search".to_string() } else { format!("Search: {}", request.q) };
    let path = if request.q.trim().is_empty() { "/search".to_string() } else { format!("/search?q={}", urlencode(&request.q)) };
    render(&headers, Page {
        title: &title,
        path: &path,
        description: "Search the project catalogue by name, description, tag or language.",
        bootstrap,
    }, body.finish())
}

/// `/stats` — what the instance has been doing, and over what span.
/// `/lists` — lists live inside projects. A link to this route from the home page used to
/// answer raw JSON because the API fallback caught it; the page now says where lists live.
pub async fn lists(headers: HeaderMap) -> Response {
    let mut body = Html::new();
    body.open("h1", &[]).text("Lists").close("h1")
        .open("p", &[]).text("Lists belong to projects. Open a project and use its Lists tab to browse or rank one.").close("p")
        .open("a", &[("href", "/projects")]).text("Browse projects").close("a");
    render(&headers, Page {
        title: "Lists",
        path: "/lists",
        description: "Lists belong to projects.",
        bootstrap: serde_json::json!({"route": "/lists"}),
    }, body.finish())
}

pub async fn stats(State(pool): State<PgPool>, headers: HeaderMap) -> Response {
    let rising = match concord_db::ops_reads::rising(&pool).await {
        Ok(rows) => rows,
        Err(error) => return crate::error_page(&headers, "the statistics could not be read", &error.to_string()),
    };
    let lists = concord_db::ops_reads::instance_lists(&pool).await.unwrap_or_default();
    let mut body = Html::new();
    body.open("h1", &[]).text("Statistics").close("h1");
    // The four sections `stats.js` fills. The page rendered the rising table and loaded no script at all, so three of the
    // four numbers the statistics page exists for — a summary, the latency percentiles and newcomer responsiveness — were
    // never fetched and never shown, and the page measured 3.8 KB of nav with one empty paragraph under it.
    body.open("div", &[("id", "stats-summary"), ("aria-busy", "true")]).close("div");
    body.open("div", &[("id", "stats-latency"), ("aria-busy", "true")]).close("div");
    body.open("div", &[("id", "stats-responsiveness"), ("aria-busy", "true")]).close("div");
    body.open("div", &[("id", "stats-rising"), ("aria-busy", "true")]);
    if rising.is_empty() {
        body.open("p", &[("class", "empty")])
            .text("No project has two snapshots to measure a slope over yet.")
            .close("p");
    } else {
        body.open("p", &[("class", "muted")])
            .text("Rising projects — every row carries the span its slope was taken over.")
            .close("p")
            .open("table", &[("class", "table")])
            .open("thead", &[]).open("tr", &[]);
        for heading in ["Project", "Span (days)", "Features/week", "Complaints/week", "Health"] {
            body.open("th", &[("scope", "col")]).text(heading).close("th");
        }
        body.close("tr").close("thead").open("tbody", &[]);
        for row in &rising {
            body.open("tr", &[])
                .open("td", &[]).open("a", &[("href", &format!("/projects/{}", row.slug))])
                .text(&row.name).close("a").close("td")
                .open("td", &[]).text(&row.days.to_string()).close("td")
                .open("td", &[]).text(&format!("{:.2}", row.features_per_week)).close("td")
                .open("td", &[]).text(&format!("{:.2}", row.complaints_per_week)).close("td");
            match row.health_score {
                Some(score) => body.open("td", &[]).text(&format!("{score:.2}")).close("td"),
                None => body.open("td", &[("class", "unmeasured")]).text("—").close("td"),
            };
            body.close("tr");
        }
        body.close("tbody").close("table");
    }
    body.close("div");
    body.open("script", &[("src", &crate::shell::asset("/assets/js/skeleton.js")), ("defer", "")]).close("script");
    body.open("script", &[("src", &crate::shell::asset("/assets/js/stats.js")), ("defer", "")]).close("script");
    if !lists.is_empty() {
        body.open("h2", &[]).text("Lists").close("h2").open("ul", &[("class", "project-list")]);
        for list in &lists {
            body.open("li", &[])
                .text(&list.title)
                .open("span", &[("class", "muted")])
                .text(&format!(" · {}", list.slug))
                .close("span")
                .close("li");
        }
        body.close("ul");
    }
    let bootstrap = serde_json::json!({
        "route": "/stats",
        "rising": rising.iter().map(|row| serde_json::json!({
            "slug": row.slug, "days": row.days, "features_per_week": row.features_per_week,
            "complaints_per_week": row.complaints_per_week, "health_score": row.health_score,
        })).collect::<Vec<_>>(),
    });
    render(&headers, Page {
        title: "Statistics",
        path: "/stats",
        description: "What the catalogue has been shipping, and how fast.",
        bootstrap,
    }, body.finish())
}

/// `/taxonomy` — the vocabulary the instance is described with.
pub async fn taxonomy(State(pool): State<PgPool>, headers: HeaderMap) -> Response {
    // `include_suggested: false` — a suggested tag is a hint nobody has ratified, and listing it by default is how a vocabulary
    // grows by being mentioned. The JSON route takes it as an explicit opt-in for the same reason.
    let tags = match concord_db::taxonomy::tags(&pool, None, false).await {
        Ok(tags) => tags,
        Err(error) => return crate::error_page(&headers, "the taxonomy could not be read", &error.to_string()),
    };
    let mut body = Html::new();
    body.open("div", &[("class", "page-head")])
        .open("div", &[("class", "page-head-main")])
        .open("span", &[("class", "page-eyebrow")]).text("Vocabulary").close("span")
        .open("h1", &[("class", "page-title-lg")]).text("Taxonomy").close("h1")
        .open("p", &[("class", "page-lede")])
        .text(&format!("{} ratified tag(s), grouped by namespace.", tags.len()))
        .close("p")
        .close("div")
        .close("div");
    if tags.is_empty() {
        body.open("p", &[("class", "empty")]).text("No tags have been ratified yet.").close("p");
    }
    // Grouped by namespace, with the global one last: an empty namespace is "no namespace", not the first alphabetically.
    let mut namespaces: Vec<String> = tags.iter().map(|tag| tag.namespace.clone()).collect();
    namespaces.sort();
    namespaces.dedup();
    namespaces.sort_by_key(|namespace| namespace.is_empty());
    for namespace in &namespaces {
        let in_namespace: Vec<&concord_db::taxonomy::Tag> =
            tags.iter().filter(|tag| &tag.namespace == namespace).collect();
        let heading = if namespace.is_empty() { "(no namespace)" } else { namespace.as_str() };
        body.open("h2", &[]).text(heading).close("h2").open("ul", &[("class", "tags"), ("data-cloud", "1")]);
        for tag in in_namespace {
            body.open("li", &[])
                .open("a", &[("href", &format!("/search?tag={}", urlencode(&tag.name)))])
                .text(&tag.name)
                .close("a")
                .close("li");
        }
        body.close("ul");
    }
    // The page above is the public, server-rendered vocabulary. The interactive panel below is the proposal/consent
    // surface: without its mount and script a reader can see ratified tags but has no way to change them.
    body.open("div", &[("id", "taxonomy-root"), ("aria-busy", "true")])
        .open("p", &[("class", "empty")]).text("Loading the vocabulary panel\u{2026}").close("p")
        .close("div");
    body.open("div", &[("id", "taxonomy-live"), ("aria-live", "polite")]).close("div");
    body.open("script", &[("src", &crate::shell::asset("/assets/js/taxonomy.js"))]).close("script");
    let bootstrap = serde_json::json!({
        "route": "/taxonomy",
        "namespaces": namespaces.iter().map(|namespace| serde_json::json!({
            "namespace": namespace,
            "tags": tags.iter().filter(|tag| &tag.namespace == namespace)
                .map(|tag| tag.name.clone()).collect::<Vec<_>>(),
        })).collect::<Vec<_>>(),
    });
    render(&headers, Page {
        title: "Taxonomy",
        path: "/taxonomy",
        description: "The ratified vocabulary the catalogue is described with.",
        bootstrap,
    }, body.finish())
}

/// `/scout` — the door to Scout: an idea, and what the catalogue already holds.
///
/// The verdict itself is computed by `concord-scout` and served at `/api/v1/scout`; this page is the form and the catalogue's honest
/// size. The form is a GET so an idea can be linked to — the same reason every other page here keeps its state in the URL — and it
/// submits to the API's route with `?idea=`, so a reader who submits gets the answer rather than a page that pretends to have one.
pub async fn scout(State(pool): State<PgPool>, headers: HeaderMap) -> Response {
    let catalogue = concord_db::scout_reads::catalog(&pool).await.unwrap_or_default();
    let mut body = Html::new();
    body.open("h1", &[]).text("Scout").close("h1")
        .open("p", &[])
        .text("Describe what you want to build. Scout checks it against what the catalogue already does, and tells you \
               whether to adopt, extend, inspire from, or avoid.")
        .close("p")
        .open("p", &[("class", "muted"), ("data-capabilities", &catalogue.len().to_string())])
        .text(&format!(
            "Scout compares against {} capability(ies) the catalogue has registered.",
            catalogue.len()
        ))
        .close("p");
    if catalogue.is_empty() {
        // An empty capability matrix is not "nothing to see": the question selector picks by information gain, so an empty matrix
        // makes the engine correctly decline to ask anything, and a reader deserves to know which of the two they are looking at.
        body.open("p", &[("class", "empty")])
            .text("No capabilities are registered, so Scout has nothing to compare an idea against.")
            .close("p");
    }
    // The static copy above stays for the page probe. The interactive report needs
    // its own shell; scout.js fills these slots (D-203).
    body.raw(r##"<div id="scout-root" aria-busy="true">
      <section id="scout-seed">
        <form id="scout-seed-form">
          <label for="scout-seed-text">Your idea</label>
          <textarea id="scout-seed-text" name="idea" rows="4"></textarea>
          <label for="scout-exclude">Exclude projects</label>
          <input id="scout-exclude" name="exclude" type="text" />
          <button type="submit">Scout it</button>
        </form>
      </section>
      <div id="scout-error" role="status" aria-live="polite" hidden></div>
      <section id="scout-report" hidden>
        <p id="scout-stats"></p>
        <p id="scout-decomp-summary"></p>
        <div id="scout-capabilities"></div>
        <div id="scout-verdicts"></div>
        <div id="scout-save">
          <label for="scout-save-slug">Save to project</label>
          <input id="scout-save-slug" type="text" />
          <button type="button">Save report</button>
          <p id="scout-save-error" hidden></p>
          <p id="scout-save-ok" hidden></p>
        </div>
        <button id="scout-again" type="button">Scout another idea</button>
      </section>
    </div>"##);
    body.open("script", &[("src", &crate::shell::asset("/assets/js/scout.js")), ("defer", "")]).close("script");
    let bootstrap = serde_json::json!({"route": "/scout", "capabilities": catalogue.len()});
    render(&headers, Page {
        title: "Scout",
        path: "/scout",
        description: "Check an idea against what the catalogue already does.",
        bootstrap,
    }, body.finish())
}

/// Percent-encode a query value for a link. Only what breaks a URL, which is enough for a link this page builds itself.
fn urlencode(value: &str) -> String {
    let mut out = String::with_capacity(value.len());
    for byte in value.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => out.push(byte as char),
            b' ' => out.push('+'),
            other => out.push_str(&format!("%{other:02X}")),
        }
    }
    out
}
