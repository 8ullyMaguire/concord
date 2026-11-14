//! The document, and the pages that fill it.
//!
//! ADR 0005 decision 2 and ADR 0011 §7(Q4): each page has a **server-rendered head** — title, description, canonical, `og:*`, and the
//! bootstrap JSON for the first paint — and the SvelteKit bundle hydrates into it. A pure static SPA answers every URL with the same empty
//! document and the title arrives only after JavaScript runs, which is why the head is a server concern here.
//!
//! Three properties this module exists to hold, each of them a rule rather than a preference:
//!
//! * **the canonical URL is absolute**, built from the request's own host rather than a configured base: a canonical that disagrees with
//!   the page's address is worse than none, because a crawler obeys it;
//! * **the bootstrap JSON is escaped for a `<script>` context** — `</script>` inside a project's description would otherwise end the
//!   element and put the rest of the JSON in the document as text;
//! * **every page renders its content from the API's own reads**, the same store functions the JSON routes call. A page that shows a number
//!   the API does not return is a lie, and the only way to keep them equal is for both to call one function.

pub mod html;
pub mod project;

use axum::{
    extract::{Path, State},
    http::HeaderMap,
    response::{Html as HtmlResponse, IntoResponse, Response},
    routing::get,
    Router,
};
use sqlx::PgPool;

use crate::html::Html;

/// The instance's one-sentence description, used by every page that does not have a better one.
pub const DESCRIPTION: &str =
    "Concord is a consensus forge: propose, argue, decide, and keep a public record of how a group reached its answer.";

/// The nav, in the order the Go renders it: the two entry points of the product ahead of the browse links.
pub const NAV: [(&str, &str); 7] = [
    ("/scout", "Scout"),
    ("/finder", "Finder"),
    ("/search", "Search"),
    ("/projects", "Projects"),
    ("/taxonomy", "Taxonomy"),
    ("/stats", "Statistics"),
    ("/curation", "Curation"),
];

/// What one page tells the document about itself.
pub struct Page<'a> {
    /// The page title, without the site suffix.
    pub title: &'a str,
    /// The canonical path, absolute-path form (`/projects/suki`).
    pub path: &'a str,
    /// The description, for `meta[name=description]` and `og:description`.
    pub description: &'a str,
    /// The route-specific bootstrap payload, serialised into the document for the first paint.
    pub bootstrap: serde_json::Value,
}

/// Render a whole document around `body`.
///
/// `host` comes from the request so the canonical URL matches the address the reader actually used — the Go instance behind
/// `concord.polarisocial.xyz` was also reachable at an IP during the migration, and a canonical pointing at the wrong one is a permanent
/// redirect nobody asked for.
#[must_use]
pub fn document(host: &str, page: &Page<'_>, body: &str) -> String {
    let canonical = format!("https://{host}{}", page.path);
    let image = format!("https://{host}/og/default.png");
    let mut head = Html::new();
    head.void("meta", &[("charset", "utf-8")])
        .void("meta", &[("name", "viewport"), ("content", "width=device-width, initial-scale=1")])
        .open("title", &[])
        .text(&format!("{} — Concord", page.title))
        .close("title")
        .void("meta", &[("name", "description"), ("content", page.description)])
        .void("link", &[("rel", "canonical"), ("href", &canonical)])
        .void("meta", &[("property", "og:type"), ("content", "website")])
        .void("meta", &[("property", "og:site_name"), ("content", "Concord")])
        .void("meta", &[("property", "og:title"), ("content", page.title)])
        .void("meta", &[("property", "og:description"), ("content", page.description)])
        .void("meta", &[("property", "og:url"), ("content", &canonical)])
        .void("meta", &[("property", "og:image"), ("content", &image)])
        .void("meta", &[("name", "twitter:card"), ("content", "summary_large_image")])
        .void("link", &[("rel", "alternate"), ("type", "application/atom+xml"),
                        ("title", "Concord projects"), ("href", "/atom.xml")]);

    // JSON-LD: the machine-readable claim about the page's subject. `serde_json` produces valid JSON, and the `<` escaping below keeps a
    // `</script>` inside a project's description from ending the element early.
    let structured = serde_json::json!({
        "@context": "https://schema.org",
        "@type": "WebSite",
        "name": "Concord",
        "url": format!("https://{host}/"),
        "description": DESCRIPTION,
    });
    head.open("script", &[("type", "application/ld+json")])
        .raw(&script_json(&structured))
        .close("script");

    let mut nav = Html::new();
    nav.open("div", &[("class", "container")])
        .open("a", &[("href", "/"), ("class", "brand"), ("aria-label", "Concord home")])
        .element("span", "C")
        .element("span", "Concord")
        .close("a")
        .open("nav", &[("class", "site-nav")]);
    for (href, label) in NAV {
        nav.open("a", &[("href", href)]).text(label).close("a");
    }
    nav.open("a", &[("href", "/login"), ("data-auth-link", "login")]).text("Sign in").close("a");
    nav.close("nav").close("div");

    let mut document = String::with_capacity(body.len() + 4096);
    document.push_str("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n");
    document.push_str(&head.finish());
    document.push_str("\n</head>\n<body>\n<header class=\"site-header\">");
    document.push_str(&nav.finish());
    document.push_str("</header>\n<main class=\"container\">");
    document.push_str(body);
    document.push_str("</main>\n<footer class=\"site-footer\"><div class=\"container\"><p>Concord — a forge for discussions.</p></div></footer>\n");
    // The mount point and the first paint's payload. `#app` is where the SvelteKit bundle hydrates; the server-rendered body above is what
    // a reader with JavaScript switched off still sees.
    document.push_str("<div id=\"app\"></div>\n<script type=\"application/json\" id=\"concord-bootstrap\">");
    document.push_str(&script_json(&page.bootstrap));
    document.push_str("</script>\n</body>\n</html>\n");
    document
}

/// Serialise a value for a `<script>` element.
///
/// The two escapes that matter: `<` (so `</script>` and `<!--` cannot appear) and U+2028/U+2029 (which are line terminators in JavaScript
/// and would break the literal in an older parser). Escaped as `\uXXXX`, which is the same string to every JSON reader.
fn script_json(value: &serde_json::Value) -> String {
    let text = serde_json::to_string(value).unwrap_or_else(|_| "null".to_string());
    text.replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}

/// A date, for a page. Unix seconds in, `YYYY-MM-DD` out — a reader needs the day, and a timestamp to the second in a table is noise
/// wearing precision.
#[must_use]
pub(crate) fn date(epoch: f64) -> String {
    match chrono::DateTime::from_timestamp(epoch as i64, 0) {
        Some(moment) => moment.format("%Y-%m-%d").to_string(),
        None => "—".to_string(),
    }
}

/// A date and time, for a log. An audit row's whole value is the order, so the time is part of it.
#[must_use]
pub(crate) fn datetime(epoch: f64) -> String {
    match chrono::DateTime::from_timestamp(epoch as i64, 0) {
        Some(moment) => moment.format("%Y-%m-%d %H:%M").to_string(),
        None => "—".to_string(),
    }
}

/// The host the reader used, from `Host`/`X-Forwarded-Host`, falling back to the public name.
pub fn host_of(headers: &HeaderMap) -> String {
    for name in ["x-forwarded-host", "host"] {
        if let Some(value) = headers.get(name).and_then(|value| value.to_str().ok()) {
            let host = value.split(',').next().unwrap_or(value).trim();
            if !host.is_empty() {
                return host.to_string();
            }
        }
    }
    "concord.polarisocial.xyz".to_string()
}

/// The home page: the catalogue, as the API lists it.
async fn home(State(pool): State<PgPool>, headers: HeaderMap) -> Response {
    let projects = match concord_db::projects::list_projects(&pool).await {
        Ok(projects) => projects,
        Err(error) => return error_page(&headers, "the catalogue could not be read", &error.to_string()),
    };
    let mut body = Html::new();
    body.open("h1", &[]).text("Concord").close("h1")
        .open("p", &[]).text(DESCRIPTION).close("p")
        .open("h2", &[]).text("Projects").close("h2")
        .open("ul", &[("class", "project-list")]);
    for project in &projects {
        body.open("li", &[])
            .open("a", &[("href", &format!("/projects/{}", project.slug))])
            .text(&project.name)
            .close("a")
            .open("span", &[("class", "muted")])
            .text(&format!(" {}", project.governance_model))
            .close("span")
            .close("li");
    }
    body.close("ul");
    let bootstrap = serde_json::json!({
        "route": "/",
        "projects": projects.iter().map(|project| {
            serde_json::json!({
                "slug": project.slug,
                "name": project.name,
                "health_score": project.health_score,
            })
        }).collect::<Vec<_>>(),
    });
    render(&headers, Page {
        title: "Projects",
        path: "/",
        description: DESCRIPTION,
        bootstrap,
    }, body.finish())
}

/// The project list, as a page of its own.
async fn project_index(State(pool): State<PgPool>, headers: HeaderMap) -> Response {
    let projects = match concord_db::projects::list_projects(&pool).await {
        Ok(projects) => projects,
        Err(error) => return error_page(&headers, "the catalogue could not be read", &error.to_string()),
    };
    let mut body = Html::new();
    body.open("h1", &[]).text("Projects").close("h1")
        .open("p", &[("class", "muted")])
        .text(&format!("{} project(s) in the catalogue", projects.len()))
        .close("p")
        .open("ul", &[("class", "project-list")]);
    for project in &projects {
        body.open("li", &[])
            .open("a", &[("href", &format!("/projects/{}", project.slug))])
            .text(&project.name)
            .close("a")
            .open("span", &[("class", "muted")])
            .text(&format!(" — {}", project.description))
            .close("span")
            .close("li");
    }
    body.close("ul");
    let bootstrap = serde_json::json!({
        "route": "/projects",
        "projects": projects.iter().map(|project| {
            serde_json::json!({
                "slug": project.slug,
                "name": project.name,
                "description": project.description,
                "governance_model": project.governance_model,
                "health_score": project.health_score,
            })
        }).collect::<Vec<_>>(),
    });
    render(&headers, Page {
        title: "Projects",
        path: "/projects",
        description: "Every project in the catalogue, with the model it is governed by.",
        bootstrap,
    }, body.finish())
}

/// One project's overview.
async fn project_overview(
    State(pool): State<PgPool>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Response {
    let project = match concord_db::projects::project_by_slug(&pool, &slug).await {
        Ok(Some(project)) => project,
        Ok(None) => return not_found(&headers),
        Err(error) => return error_page(&headers, "the project could not be read", &error.to_string()),
    };
    let mut body = Html::new();
    body.open("h1", &[]).text(&project.name).close("h1");
    if !project.description.is_empty() {
        body.open("p", &[]).text(&project.description).close("p");
    }
    body.open("dl", &[("class", "facts")])
        .open("dt", &[]).text("Governance").close("dt")
        .open("dd", &[]).text(&project.governance_model).close("dd")
        .open("dt", &[]).text("Visibility").close("dt")
        .open("dd", &[]).text(&project.visibility).close("dd");
    // Health is shown only when it was MEASURED: a `0.00` for a project nobody has measured is a number a reader will believe, and the
    // API answers `null` for exactly this reason.
    if let Some(score) = project.health_score {
        body.open("dt", &[]).text("Health").close("dt")
            .open("dd", &[]).text(&format!("{score:.2}")).close("dd");
    }
    body.close("dl");
    let bootstrap = serde_json::json!({
        "route": format!("/projects/{slug}"),
        "project": {
            "slug": project.slug,
            "name": project.name,
            "description": project.description,
            "governance_model": project.governance_model,
            "visibility": project.visibility,
            "health_score": project.health_score,
        },
    });
    let title = project.name.clone();
    let description = if project.description.is_empty() {
        format!("{} on Concord.", project.name)
    } else {
        project.description.clone()
    };
    render(&headers, Page {
        title: &title,
        path: &format!("/projects/{slug}"),
        description: &description,
        bootstrap,
    }, body.finish())
}

/// Render a page as HTML, with the content type a document needs.
pub(crate) fn render(headers: &HeaderMap, page: Page<'_>, body: String) -> Response {
    let host = host_of(headers);
    HtmlResponse(document(&host, &page, &body)).into_response()
}

/// A 404 that is a document, not a JSON error: a reader arrived here with a link.
pub(crate) fn not_found(headers: &HeaderMap) -> Response {
    let mut body = Html::new();
    body.open("h1", &[]).text("Not found").close("h1")
        .open("p", &[]).text("There is no project at this address.").close("p")
        .open("p", &[]).open("a", &[("href", "/projects")]).text("All projects").close("a").close("p");
    let mut response = render(headers, Page {
        title: "Not found",
        path: "/404",
        description: "There is no project at this address.",
        bootstrap: serde_json::json!({"route": "/404"}),
    }, body.finish());
    *response.status_mut() = axum::http::StatusCode::NOT_FOUND;
    response
}

/// A 500 that is a document: the reader gets a sentence and the operator gets the error in the log.
fn error_page(headers: &HeaderMap, headline: &str, detail: &str) -> Response {
    eprintln!("page error: {headline}: {detail}");
    let mut body = Html::new();
    body.open("h1", &[]).text(headline).close("h1")
        .open("p", &[]).text("The instance is reachable, but this page could not be built.").close("p");
    let mut response = render(headers, Page {
        title: "Something went wrong",
        path: "/error",
        description: headline,
        bootstrap: serde_json::json!({"route": "/error"}),
    }, body.finish());
    *response.status_mut() = axum::http::StatusCode::INTERNAL_SERVER_ERROR;
    response
}

/// The page routes this crate owns.
///
/// Mounted by the application's router. Kept as a function returning a `Router` so a page and its reader arrive in the same commit — the
/// estate's delete-twice rule applied to a route.
pub fn router<S>() -> Router<S>
where
    S: Clone + Send + Sync + 'static,
    PgPool: axum::extract::FromRef<S>,
{
    Router::new()
        .route("/", get(home))
        .route("/projects", get(project_index))
        .route("/projects/{slug}", get(project_overview))
        .route("/projects/{slug}/board", get(project::board))
        .route("/projects/{slug}/roadmap", get(project::roadmap))
        .route("/projects/{slug}/consensus", get(project::consensus))
        .route("/projects/{slug}/documents", get(project::documents))
        .route("/projects/{slug}/criteria", get(project::criteria))
        .route("/projects/{slug}/audit", get(project::audit))
}
