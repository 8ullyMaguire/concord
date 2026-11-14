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
pub mod top;
pub mod detail;
pub mod misc;
pub mod shell;
pub mod assets;

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
pub(crate) fn script_json(value: &serde_json::Value) -> String {
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
            .close("span");
        stage_badge(&mut body, project);
        body.close("li");
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


/// The lifecycle stage as a badge, or nothing at all.
///
/// `None` for `unknown`, deliberately: that stage says this instance has not measured the project, which is our gap rather than a fact
/// about somebody's project, and a warning about our own gap teaches a reader to ignore the warning that matters. `dormant` takes the
/// warning colour because it is the one stage a reader should act on, and the reason travels as the `title` so the badge is not a label
/// to be taken on faith. Shared by both server-rendered lists so they cannot say different words.
fn stage_badge(body: &mut Html, project: &concord_db::projects::Project) {
    // `unknown` writes NOTHING and returns: see the comment above.
    if project.stage == "unknown" {
        return;
    }
    let class = if project.stage == "dormant" { "badge badge-amber" } else { "badge badge-slate" };
    // The tooltip is the REASON alone. Prefixing it with the stage said "dormant: measured: no commit for ninety days or more", which
    // repeats the word already on the badge and buries the sentence a reader wanted.
    body.open("span", &[("class", class), ("title", &project.stage_because)])
        .text(&format!(" {}", project.stage))
        .close("span");
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
            .close("span");
        stage_badge(&mut body, project);
        body.close("li");
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

/// One project's overview — the reference's mount point.
///
/// The reference renders this page **in the browser**: `project.js` fetches `/api/v1/projects/{slug}` (and the sub-resources it needs) and
/// builds the cards. The suite asserts what the script produced — `#project-detail` losing its `aria-busy`, every feature title being a
/// `<a href="/projects/{slug}/features/{id}">` — so the port renders the same mount and serves the same script. A server-rendered body here
/// would be a second implementation of the same page that the suite does not look at, and two implementations that disagree is the failure
/// mode this port exists to avoid.
async fn project_overview(
    State(pool): State<PgPool>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Response {
    // The 404 is decided on the SERVER: a mount point that renders "loading" and then a client-side "not found" is a page that answered
    // 200 for a project that does not exist, which is the one thing a 404 is for.
    let project = match concord_db::projects::project_by_slug(&pool, &slug).await {
        Ok(Some(project)) => project,
        Ok(None) => return not_found(&headers),
        Err(error) => return error_page(&headers, "the project could not be read", &error.to_string()),
    };
    // A private or protected project is a 404 PAGE to a stranger, not a 200 page whose panels fail to load: the second form leaks the
    // project's name, description and canonical URL, and tells the reader it exists. Same predicate the JSON API asks (the store's
    // `projects::readable_for`), asked here with the reader the `concord_nav` cookie names.
    let reader = nav_user(&pool, &headers).await;
    match concord_db::projects::readable_for(&pool, &project, reader.map(|user| user.id)).await {
        Ok(true) => {}
        Ok(false) => return not_found(&headers),
        Err(error) => return error_page(&headers, "the project could not be read", &error.to_string()),
    }
    let body = format!(
        r#"<div id="project-detail" aria-busy="true"></div>
<script src="{}"></script>
<script>(function(){{var b=document.getElementById("project-detail");if(b&&window.ConcordSkeleton){{b.innerHTML='<div class="card mb-6">'+window.ConcordSkeleton.title()+window.ConcordSkeleton.text(3)+'</div>'+window.ConcordSkeleton.rows(4);}}}})();</script>
<script src="{}"></script>
"#,
        shell::asset("/assets/js/skeleton.js"),
        shell::asset("/assets/js/project.js"),
    );
    let description = if project.description.is_empty() {
        format!("{} on Concord.", project.name)
    } else {
        project.description.clone()
    };
    render(&headers, Page {
        title: &project.name,
        path: &format!("/projects/{slug}"),
        description: &description,
        // No bootstrap payload: this page's data arrives from `/api/v1`, and a second copy in the document would be a second source of
        // truth for the same numbers.
        bootstrap: serde_json::json!({"route": format!("/projects/{slug}"), "mount": "project-detail"}),
    }, body)
}

/// Render a page as HTML, with the content type a document needs.
///
/// Goes through the shell, which is the reference's `base.html` contract: `data-consent` on the body, the notice rendered by the server
/// from the reader's own cookie, the nav hooks `session.js` needs, and the project tabs driven by the path.
pub(crate) fn render(headers: &HeaderMap, page: Page<'_>, body: String) -> Response {
    let host = host_of(headers);
    let accepted = shell::consent_accepted(headers);
    let version = std::env::var("CONCORD_VERSION").unwrap_or_else(|_| env!("CARGO_PKG_VERSION").to_string());
    let document = shell::document(
        &host,
        &shell::Shell {
            title: page.title,
            path: page.path,
            description: page.description,
            body: &body,
        },
        accepted,
        &version,
        Some(&page.bootstrap),
    );
    HtmlResponse(document).into_response()
}

/// A 404 that is a document, not a JSON error: a reader arrived here with a link.
/// The reader a browser NAVIGATION is, from the `concord_nav` cookie.
///
/// The reference's nav cookie carries the session token itself, and it is accepted only on a GET that is not under `/api/` — which is
/// exactly what a page render is. The web crate had no session at all, so `/ranking` could not answer the one question its page script
/// asks of the server: `data-can-moderate`, the attribute `ranking.js` reads to decide whether to emit a "Moderate" link. A token-only
/// client renders anonymous, and that is the safe direction — no control is offered that the API would refuse.
pub(crate) async fn nav_user(pool: &sqlx::PgPool, headers: &HeaderMap) -> Option<concord_db::auth::User> {
    let token = headers
        .get(axum::http::header::COOKIE)
        .and_then(|value| value.to_str().ok())
        .and_then(|cookies| {
            cookies.split(';').find_map(|cookie| {
                let mut parts = cookie.trim().splitn(2, '=');
                match (parts.next(), parts.next()) {
                    (Some("concord_nav"), Some(value)) if !value.is_empty() => Some(value.to_string()),
                    _ => None,
                }
            })
        })?;
    concord_db::auth::user_for_token(pool, &token).await.ok().flatten()
}

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
pub(crate) fn error_page(headers: &HeaderMap, headline: &str, detail: &str) -> Response {
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
        .route("/search", get(top::search))
        .route("/projects/{slug}/requests", get(detail::requests))
        .route("/projects/{slug}/requests/{id}", get(detail::request))
        .route("/projects/{slug}/lists", get(detail::lists))
        .route("/projects/{slug}/lists/{id}", get(detail::list))
        .route("/projects/{slug}/milestones/{id}", get(detail::milestone))
        .route("/projects/{slug}/features/{id}", get(detail::feature))
        .route("/projects/{slug}/complaints/{id}", get(detail::complaint))
        .route("/projects/{slug}/merge", get(detail::merge))
        .route("/projects/{slug}/settings", get(detail::settings))
        .route("/projects/{slug}/rank", get(misc::rank))
        .route("/projects/{slug}/ranking", get(misc::ranking))
        .route("/contribute", get(misc::contribute))
        .route("/curation", get(misc::curation))
        .route("/notifications", get(misc::notifications))
        .route("/login", get(misc::login))
        .route("/register", get(misc::register))
        .route("/finder", get(misc::finder))
        .route("/assets/{*path}", get(assets::serve))
        .route("/stats", get(top::stats))
        .route("/taxonomy", get(top::taxonomy))
        .route("/scout", get(top::scout))
}
