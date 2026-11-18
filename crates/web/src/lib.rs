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

/// The build's own stamp, from `CONCORD_VERSION` baked at compile time — one definition, so the page's asset query and
/// `/api/v1/version` cannot disagree about which build answered.
pub(crate) const fn version() -> &'static str {
    match option_env!("CONCORD_VERSION") {
        Some(stamp) => stamp,
        None => concat!("concord-rs ", env!("CARGO_PKG_VERSION"), " (unstamped build)"),
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

/// The Go landing page, ported from `internal/httpapi/templates/index.html` (D-75).
/// Every byte is a compile-time literal; no reader value reaches `Html::raw`.
const GO_LANDING: &str = r#"
<section class="hero"><div class="hero-inner">
  <h1 class="hero-title">The forge for <span class="gradient">decisions</span>, not just code.</h1>
  <p class="hero-sub">Every software project answers the same questions — what hurts, what to build, what to merge. Concord turns the messy, political, exhausting job of deciding into something structured, visible and fair.</p>
  <div class="hero-actions">
    <a href="/search" class="btn btn-primary btn-hero">Start searching</a>
    <a href="/projects" class="btn btn-outline">Explore projects</a>
  </div>
</div></section>
<section class="section"><div class="section-head">
  <h2 class="section-title">What Concord does</h2>
  <p class="section-sub">Code hosting is the substrate. Decisions are the product.</p>
</div><div class="grid-responsive-3 grid-2">
  <div class="card fade-in" data-icon="!"><h3 class="card-title">Complaints, not feature requests</h3><p class="card-text">Work starts as a structured problem report: who is hurt, how often, what the workaround costs. A feature must trace back to validated complaints.</p></div>
  <div class="card fade-in stagger-1" data-icon="⇄"><h3 class="card-title">Solutions compete</h3><p class="card-text">Proposed features are ranked by pairwise comparison (Glicko-2): &ldquo;which should we build first, A or B?&rdquo; Priority is computed, never hand-assigned.</p></div>
  <div class="card fade-in stagger-2" data-icon="✓"><h3 class="card-title">Consensus, not command</h3><p class="card-text">Features pass a formal consent round — consent, stand aside, abstain, or block with a stated principle and remedy — with quorum. Nobody steers alone.</p></div>
  <div class="card fade-in stagger-3" data-icon="▦"><h3 class="card-title">Kanban with teeth</h3><p class="card-text">Work moves through phases, and each advance is gated: nothing reaches Ready without consensus; nothing lands without a reviewer&rsquo;s technical approval and quorum.</p></div>
  <div class="card fade-in stagger-4" data-icon="⌕"><h3 class="card-title">Discovery is the point</h3><p class="card-text">Search is a query language, not a box. Filter by tag, language, license, governance, and a transparent maintenance-health score. API-first, facets on every result.</p></div>
  <div class="card fade-in stagger-5" data-icon="☰"><h3 class="card-title">Lists are objects, not files</h3><p class="card-text">Awesome lists become collaborative databases: anyone proposes entries, quorum admits them, pairwise ranking orders them, search finds entries across every list.</p></div>
</div></section>
<section class="section"><div class="stats-strip"><div class="stats-grid" id="stats-grid" aria-busy="true">
  <div><p class="stat-value" id="stat-projects">–</p><p class="stat-label">Projects</p></div>
  <div><p class="stat-value" id="stat-tags">–</p><p class="stat-label">Tags in use</p></div>
  <div><p class="stat-value" id="stat-languages">–</p><p class="stat-label">Languages in use</p></div>
</div></div></section>
<section class="section"><div class="section-head"><h2 class="section-title" style="font-size:1.25rem;">Explore</h2></div>
<div class="grid-4">
  <a href="/search" class="card fade-in"><div class="explore-title">Search</div><p class="card-text">Query across projects, features, complaints, and awesome lists.</p><div class="card-link-hint">Go to search &rarr;</div></a>
  <a href="/projects" class="card fade-in stagger-1"><div class="explore-title">Projects</div><p class="card-text">Browse every project on the forge with health scores and tags.</p><div class="card-link-hint">Browse projects &rarr;</div></a>
  <a href="/lists" class="card fade-in stagger-2"><div class="explore-title">Awesome Lists</div><p class="card-text">Collaborative databases of tools, libraries, and resources — inside each project.</p><div class="card-link-hint">Browse lists &rarr;</div></a>
  <a href="/projects" class="card fade-in stagger-3"><div class="explore-title">Features</div><p class="card-text">Proposed features ranked by the community, consented forward.</p><div class="card-link-hint">Browse features &rarr;</div></a>
</div></section>
<section class="flip-section">
  <h2>The flip</h2>
  <p>It is not a GitHub clone with extra buttons, and it does not replace git. It starts as <em>a decision layer</em> over an existing Forgejo or Gitea instance — OAuth login, webhooks, bot-mediated merges, and a record of why every decision was made.</p>
  <p class="small">It is not raw vote-based democracy either: it is consent-based, blocks require remedies, and silence never decides anything.</p>
  <p><a class="btn btn-primary" href="/search">Explore the forge</a></p>
</section>
"#;

/// The home page: the catalogue, as the API lists it.
async fn home(State(pool): State<PgPool>, headers: HeaderMap) -> Response {
    let projects = match concord_db::projects::list_projects(&pool).await {
        Ok(projects) => projects,
        Err(error) => return error_page(&headers, "the catalogue could not be read", &error.to_string()),
    };
    let mut body = Html::new();
    body.raw(GO_LANDING)
        .open("section", &[("class", "section")])
        .open("h2", &[("class", "section-title")]).text("Projects").close("h2")
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
        ai_badge(&mut body, project);
        body.close("li");
    }
    body.close("ul").close("section");
    for name in ["/assets/js/skeleton.js", "/assets/js/index.js"] {
        let src = crate::shell::asset(name);
        body.open("script", &[("src", src.as_str()), ("defer", "")]).close("script");
    }
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
/// The AI-involvement declaration, as a badge, or nothing at all.
///
/// Nothing for `None`, and that is the point of the feature rather than an omission: `None` is "not declared", not "no AI". A badge
/// rendered for an undeclared project would be this instance asserting something about somebody's code that nobody asserted, which is
/// worse than the silence the note complains about.
///
/// The word is the declaration itself (`assisted`), not a sentence about it: the badge sits beside the stage badge and has to read at a
/// glance, and the four values are a closed vocabulary the page can teach in one line.
fn ai_badge(body: &mut Html, project: &concord_db::projects::Project) {
    let Some(value) = project.ai_involvement.as_deref() else {
        return;
    };
    let class = match value {
        // Generated code reads as a caution, not a defect: the colour is the same amber the dormant stage uses for "read this before you
        // rely on it", and nothing here judges the choice.
        "generated" => "badge badge-amber",
        "audited" => "badge badge-green",
        _ => "badge badge-slate",
    };
    body.open("span", &[("class", class), ("title", &format!("AI involvement, declared by the maintainers: {value}"))])
        .text(&format!(" {value}"))
        .close("span");
}

fn stage_badge(body: &mut Html, project: &concord_db::projects::Project) {
    // `unknown` writes NOTHING and returns: see the comment above.
    if project.stage == "unknown" {
        return;
    }
    // `dormant` warns (it might still be alive and worth reviving); `archived` does not — it is a decision, and a decision is not a
    // caution. It keeps the slate colour and carries the maintainer's own sentence in the tooltip.
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
    body.open("div", &[("class", "page-head")])
        .open("div", &[("class", "page-head-main")])
        .open("span", &[("class", "page-eyebrow")]).text("Catalogue").close("span")
        .open("h1", &[("class", "page-title-lg")]).text("Projects").close("h1")
        .open("p", &[("class", "page-lede")])
        .text(&format!("{} projects in the catalogue", projects.len())).close("p")
        .close("div")
        .open("a", &[("class", "btn btn-primary"), ("href", "/projects/new")])
        .text("Start a project").close("a")
        .close("div");
    body.open("ul", &[("class", "project-list"), ("data-grid", "browse")]);
    for project in &projects {
        body.open("li", &[])
            .open("a", &[("class", "project-card-title"), ("href", &format!("/projects/{}", project.slug))])
            .text(&project.name)
            .close("a")
            .open("p", &[("class", "project-card-desc")])
            .text(&project.description)
            .close("p")
            .open("div", &[("class", "project-card-badges")]);
        stage_badge(&mut body, project);
        ai_badge(&mut body, project);
        body.close("div").close("li");
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

/// `/adopt` — the pool of projects the lifecycle has measured dormant, and the act of taking one on (roadmap step 5).
///
/// The page exists because a catalogue with no exit for a dead project is a catalogue that fills with dead projects. Everything on it is
/// server-rendered from one query; the only script is the claim button, because claiming is a write and the number that justifies it is
/// already in the document.
///
/// The empty state is the interesting one, and it is written to be TRUE rather than reassuring: on 2026-11-15 this instance had 73
/// projects, 39 of which state a repository and 23 of which have a MEASURED commit history (16 more state one that no longer answers,
/// and 16 are hosted on `git.disroot.org`, which was offline after its storage server failed). So "nothing to adopt" would be a claim it
/// cannot support — it says what was measured instead, and when nothing was measured it says that. Both numbers come from
/// `concord_db::adoption::pool`, never from this comment.
async fn adopt(State(db): State<PgPool>, headers: HeaderMap) -> Response {
    let pool = match concord_db::adoption::pool(&db).await {
        Ok(pool) => pool,
        Err(error) => return error_page(&headers, "the adoption pool could not be read", &error.to_string()),
    };
    let mut body = Html::new();
    body.open("h1", &[]).text("Adopt an abandoned project").close("h1")
        .open("p", &[]).text(concat!(
            "A project is in this pool when the forge measures ninety days or more since its last commit. ",
            "Taking one on makes you a maintainer of it, and the record says who did it and how long the silence was — ",
            "so the next reader can see that the project was abandoned rather than merely quiet.",
        )).close("p");

    if pool.candidates.is_empty() {
        body.open("p", &[("class", "muted")]);
        if pool.measured == 0 {
            body.text(&format!(
                "Nothing is listed, and that is not the same as nothing being abandoned: none of this instance's {} projects has a commit \
                 history it has read, so no project's silence has been measured at all.",
                pool.total,
            ));
        } else {
            body.text(&format!(
                "Nothing is dormant right now. {} of {} projects have a measured commit history, and the longest measured silence is \
                 {} days.",
                pool.measured,
                pool.total,
                pool.longest_silence.map(|days| format!("{days:.1}")).unwrap_or_else(|| "an unmeasured number of".to_string()),
            ));
        }
        body.close("p");
    } else {
        body.open("p", &[("class", "muted")]).text(&format!(
            "{} of {} projects are measured dormant, and {} of the catalogue have a commit history this instance has read.",
            pool.candidates.len(), pool.total, pool.measured,
        )).close("p");
        body.open("ul", &[("class", "adoption-list")]);
        for candidate in &pool.candidates {
            body.open("li", &[])
                .open("a", &[("href", &format!("/projects/{}", candidate.project.slug))])
                .text(&candidate.project.name)
                .close("a")
                .open("span", &[("class", "muted")])
                .text(&format!(" — {:.0} days since its last commit", candidate.days_silent))
                .close("span")
                .open("button", &[("class", "btn btn-secondary"), ("type", "button"), ("data-claim", &candidate.project.slug)])
                .text("Take it on")
                .close("button")
                .close("li");
        }
        body.close("ul");
    }

    // The claim button is the only script on the page; the list, the numbers and the reason all arrived in the document above.
    body.open("script", &[("src", &shell::asset("/assets/js/adopt.js")), ("defer", "")]).close("script");
    let bootstrap = serde_json::json!({ "route": "/adopt" });
    render(&headers, Page {
        title: "Adopt a project",
        path: "/adopt",
        description: "Projects the forge has measured dormant, and the act of taking one on.",
        bootstrap,
    }, body.finish())
}

/// One project's overview: a server-rendered HEADER, and a mount for the pills that need the API.
///
/// The port first rendered this whole page in the browser, and the comment here argued for it ("a server-rendered body would be a second
/// implementation"). That argument was right about the panels and wrong about the document: `/projects/{slug}` answered 5.5 KB of
/// `aria-busy` to anything that does not run JavaScript, so a crawler, a link preview and a reader on a bad connection saw an empty box
/// where the project was (review decision 2). Splitting is the answer the review asked for and it keeps the argument:
///
/// * **Here, on the server**: the `<h1>`, the slug, the stage and AI badges, the description, and the three actions. None of that needs a
///   second request, and all of it is what a document is for. It is the same `stage_badge`/`ai_badge` the lists use, so the two surfaces
///   cannot disagree about what a badge says.
/// * **In the mount**: the pills that are aggregations over other tables — complaints, features, health, the join button — which the page's
///   own script fetches. Those stay, because they are interactions with numbers, not the identity of the page.
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
    let reader_id = reader.as_ref().map(|user| user.id);
    match concord_db::projects::readable_for(&pool, &project, reader_id).await {
        Ok(true) => {}
        Ok(false) => return not_found(&headers),
        Err(error) => return error_page(&headers, "the project could not be read", &error.to_string()),
    }
    // Is this reader answerable for the project? The API refuses archive and claim with `role_for`, so the page asks the same question rather
    // than inferring it from a badge. An unknown role is "no": a control a reader cannot use is worse than no control.
    let maintainer = match reader {
        Some(user) => concord_db::feed_reads::role_for(&pool, project.id, user.id)
            .await
            .ok()
            .flatten()
            .map(|role| role == "owner" || role == "maintainer")
            .unwrap_or(false),
        None => false,
    };
    // The header, server-rendered. `stage_badge` and `ai_badge` are the SAME functions `/` and `/projects` use, so a badge cannot mean
    // one thing in the catalogue and another on the page it opens.
    let mut header = Html::new();
    header.open("header", &[("class", "detail-header")]);
    header.open("div", &[]);
    header.open("span", &[("class", "page-eyebrow")]).text("Project").close("span");
    header.open("h1", &[("class", "page-title")]).text(&project.name);
    header.open("span", &[("class", "badge badge-slate")])
        .text(&format!(" {}", project.slug))
        .close("span");
    stage_badge(&mut header, &project);
    ai_badge(&mut header, &project);
    header.close("h1");
    let subtitle = if project.description.trim().is_empty() { "No description yet." } else { project.description.as_str() };
    header.open("p", &[("class", "page-subtitle")]).text(subtitle).close("p");
    header.close("div");
    header.open("div", &[("class", "detail-actions")]);
    // The join control stays hidden until the page knows there is a session: a join is attributed to an account, and a button that can
    // only fail is worse than no button.
    header.open("button", &[("type", "button"), ("class", "btn"), ("id", "project-join"), ("hidden", "hidden")])
        .text("Join this project")
        .close("button");
    header.open("a", &[("class", "btn btn-primary"), ("href", &format!("/projects/{}/board", project.slug))])
        .text("Open board")
        .close("a");
    header.open("a", &[("class", "btn btn-secondary"), ("href", "/projects")]).text("All projects").close("a");
    // The archive act has had a route, a migration, a permission check and two unit tests since review item 8, and no way to reach it:
    // `scripts/parity/route_readers.py` counted it among the routes with no reader (ADR 0012). A capability the record keeps and the page
    // cannot reach is the defect this repository produces most often, so the control lives here — and only for a maintainer, because that is
    // the API's own rule and the page asks the same table rather than inferring permission from a badge.
    if maintainer {
        let archived = project.stage == "archived";
        header.open("button", &[("type", "button"), ("class", "btn"), ("id", "project-archive-toggle"),
                                ("data-archive-slug", &project.slug),
                                ("data-archived", if archived { "1" } else { "0" })])
            .text(if archived { "Restore this project" } else { "Archive this project" })
            .close("button");
        header.open("form", &[("id", "project-archive"), ("class", "archive-form"), ("hidden", "hidden")])
            .open("label", &[("for", "archive-reason")])
            .text(if archived { "Why are you restoring it? (optional)" } else { "Why is it archived? A sentence, not a word." })
            .close("label")
            .open("input", &[("id", "archive-reason"), ("name", "reason"), ("type", "text"), ("class", "input"),
                             ("autocomplete", "off")])
            .close("input")
            .open("button", &[("type", "submit"), ("class", "btn btn-primary")])
            .text(if archived { "Restore" } else { "Archive" })
            .close("button")
            .open("p", &[("class", "muted"), ("data-archive-status", ""), ("role", "status"), ("aria-live", "polite")])
            .close("p")
            .close("form");
    }
    header.close("div");
    header.close("header");

    // The control's script is emitted only when the control is: an anonymous reader was "seeing the control" in a probe because the script
    // that wires it was on every page, id and all. A page should not ship the JavaScript for a button it does not render.
    // Inline before the CSP: a `script-src 'self'` policy refuses an inline block, and the point of that policy is that a
    // missing script fails loudly here rather than silently in a browser. `defer` keeps it behind `api.js`.
    let archive_script = if maintainer {
        format!(
            r#"<script src="{}" defer></script>"#,
            shell::asset("/assets/js/project-archive.js")
        )
    } else {
        String::new()
    };
    let body = format!(
        r#"{}<div id="project-detail" aria-busy="true"></div>
<script src="{}"></script>
<script src="{}"></script>
<script src="{}"></script>
<script src="{}"></script>
<script src="{}"></script>
<script src="{}"></script>{}
""#,
        header.finish(),
        shell::asset("/assets/js/skeleton.js"),
        // The three split files register themselves on `window` before the page script runs — `project.js` calls
        // `window.ConcordArena.controls/wire`, `window.ConcordPanels.*` and binds the write helpers out of
        // `window.ConcordWrites` — and the order of these tags IS that dependency.
        shell::asset("/assets/js/project-arena.js"),
        shell::asset("/assets/js/project-panels.js"),
        shell::asset("/assets/js/project-writes.js"),
        // The node-only renderer registers `window.ConcordProjectRender` before the page script calls it.
        shell::asset("/assets/js/project-render.js"),
        shell::asset("/assets/js/project.js"),
        archive_script,
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
        .route("/adopt", get(adopt))
        // Static before dynamic: `/projects/new` must be the form, not a project whose slug is "new".
        .route("/projects/new", get(misc::new_project))
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
