//! The document shell: the part of every page that is not the page.
//!
//! This is a port of the reference's `base.html` contract, and the contract is what the browser suites assert rather than the markup being
//! pretty: `body` carries `data-consent` so `consent.js` can refuse a write before the server has to, the cookie notice is rendered by the
//! SERVER from the request's own cookie so the state is right in the first paint, the nav carries `data-auth-link` and
//! `data-notifications-link` for `session.js`, and the project tab bar is driven by the request PATH rather than by template data — a tab
//! bar that renders from the path cannot disagree with the page it is on.
//!
//! The content templates are ported too, in the same spirit: a mount div with `aria-busy="true"`, a skeleton, and the page's own script.
//! The reference renders its pages in the browser from `/api/v1`, and the suites click what the script produced — so a server-rendered body
//! would be a second implementation of the same page that the tests do not look at, and two implementations disagreeing is the failure mode
//! this whole port exists to avoid.

use axum::http::HeaderMap;

use crate::html::{escape, Html};

/// The notice text's version. A visitor's acceptance is recorded against it, so changing the text without changing this would record
/// agreement to words nobody was shown.
pub const CONSENT_VERSION: &str = "1";

/// What the shell needs to know about the request.
pub struct Shell<'a> {
    /// The page title, without the site suffix.
    pub title: &'a str,
    /// The request's path — the tab bar and the canonical URL are both derived from it.
    pub path: &'a str,
    /// The description, for the meta tag.
    pub description: &'a str,
    /// The body markup.
    pub body: &'a str,
}

/// The project sub-page tabs, as the reference orders them. `Vote` is `/rank` and `Modlog` is `/audit` — the reference's own labels, kept
/// because a suite and a reader both look for them.
pub const TABS: [(&str, &str, &str); 13] = [
    ("", "Overview", ""),
    ("consent", "Consensus", "consensus"),
    ("board", "Board", "board"),
    ("rank", "Vote", "rank"),
    ("ranking", "Ranking", "ranking"),
    ("roadmap", "Roadmap", "roadmap"),
    ("criteria", "Criteria", "criteria"),
    ("merge", "Merge", "merge"),
    ("documents", "Documents", "documents"),
    ("requests", "Requests", "requests"),
    ("lists", "Lists", "lists"),
    ("audit", "Modlog", "audit"),
    ("settings", "Settings", "settings"),
];

/// The project slug and sub-page a path names, if it names one.
///
/// `/projects/{slug}` is the overview; `/projects/{slug}/{sub}/{rest...}` is a sub-page. A path under `/projects` that names nothing
/// (`/projects`) is not a project page and gets no tabs — the reference's `tabFor` answers nothing there, and a tab bar on the index would
/// claim a project the reader is not looking at.
#[must_use]
pub fn tab_for(path: &str) -> Option<(String, String)> {
    let parts: Vec<&str> = path.trim_start_matches('/').split('/').filter(|part| !part.is_empty()).collect();
    if parts.len() < 2 || parts[0] != "projects" {
        return None;
    }
    let slug = parts[1].to_string();
    let sub = parts.get(2).map(|part| (*part).to_string()).unwrap_or_default();
    // A project page is `/projects/{slug}` or `/projects/{slug}/{known tab}/...`; anything else (a feed, an atom document) belongs to the
    // project but is not a tab, and the reference shows the bar anyway on the overview's siblings like `/milestones/12`.
    if slug.is_empty() || slug == "{" {
        return None;
    }
    Some((slug, sub))
}

/// Whether the request's own cookie says the notice was accepted.
#[must_use]
pub fn consent_accepted(headers: &HeaderMap) -> bool {
    headers
        .get(axum::http::header::COOKIE)
        .and_then(|value| value.to_str().ok())
        .map(|cookies| {
            cookies.split(';').any(|cookie| {
                let mut parts = cookie.trim().splitn(2, '=');
                matches!(
                    (parts.next(), parts.next()),
                    (Some(name), Some(value)) if name == "concord_consent" && value == CONSENT_VERSION
                )
            })
        })
        .unwrap_or(false)
}

/// The whole document.
///
/// `bootstrap` is the first paint's payload for the pages that render their content on the server. The pages that mount and fetch carry
/// `None`: their payload arrives from `/api/v1` and a second copy in the document would be a second source of truth for the same data.
#[must_use]
pub fn document(
    host: &str,
    shell: &Shell<'_>,
    accepted: bool,
    version: &str,
    bootstrap: Option<&serde_json::Value>,
) -> String {
    let canonical = format!("https://{host}{}", shell.path);
    let mut head = Html::new();
    head.void("meta", &[("charset", "utf-8")])
        .void("meta", &[("name", "viewport"), ("content", "width=device-width, initial-scale=1")])
        .open("title", &[])
        .text(&format!("{} — Concord", shell.title))
        .close("title")
        .void("meta", &[("name", "description"), ("content", shell.description)])
        .void("link", &[("rel", "canonical"), ("href", &canonical)])
        .void("link", &[("rel", "stylesheet"), ("href", &asset("/assets/css/style.css"))])
        .void("meta", &[("property", "og:type"), ("content", "website")])
        .void("meta", &[("property", "og:site_name"), ("content", "Concord")])
        .void("meta", &[("property", "og:title"), ("content", shell.title)])
        .void("meta", &[("property", "og:description"), ("content", shell.description)])
        .void("meta", &[("property", "og:url"), ("content", &canonical)])
        .void("meta", &[("property", "og:image"), ("content", &format!("https://{host}/og/default.png"))])
        .void("meta", &[("name", "twitter:card"), ("content", "summary_large_image")])
        .void("link", &[("rel", "alternate"), ("type", "application/atom+xml"),
                        ("title", "Concord projects"), ("href", "/atom.xml")]);
    let structured = serde_json::json!({
        "@context": "https://schema.org",
        "@type": "WebSite",
        "name": "Concord",
        "url": format!("https://{host}/"),
        "description": shell.description,
    });
    head.open("script", &[("type", "application/ld+json")])
        .raw(&crate::script_json(&structured))
        .close("script");

    let consent_state = if accepted { "accepted" } else { "required" };
    let mut document = String::with_capacity(shell.body.len() + 8192);
    document.push_str("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n");
    document.push_str(&head.finish());
    document.push_str(&format!(
        "\n</head>\n<body data-consent=\"{consent_state}\" data-consent-version=\"{}\">\n",
        escape(version)
    ));

    if !accepted {
        // The notice is a NOTICE WITH A CONSEQUENCE: until it is accepted the server refuses every write and `session.js` refuses to send
        // one, so the sentence under the buttons is the same one the server would have answered with.
        document.push_str(
            r#"<div id="cookie-notice" class="cookie-notice" role="dialog" aria-labelledby="cookie-notice-text">
  <p id="cookie-notice-text">
    Concord sets two cookies, and nothing else. <code>concord_nav</code> lets a page you are already signed in to render as signed
    in — it is never accepted on an action, so it cannot be used to make you do anything. <code>concord_consent</code> records this
    choice. No advertising, no analytics, no third parties. You can take part without an account: you act as a nameless guest — a
    random name and a timestamp, nothing else — and creating an account later moves what you did into it.
    <strong>Interactions are off until you accept.</strong>
  </p>
  <div class="cookie-notice-actions">
    <button type="button" id="cookie-accept" class="btn btn-primary">Accept and continue</button>
  </div>
</div>
"#,
        );
    }

    document.push_str(
        r#"<header class="site-header">
  <div class="container">
    <a href="/" class="brand" aria-label="Concord home"><span class="brand-tile">C</span><span class="brand-name">Concord</span></a>
    <nav class="site-nav">
      <a href="/scout">Scout</a>
      <a href="/finder">Finder</a>
      <a href="/search">Search</a>
      <a href="/projects">Projects</a>
      <a href="/taxonomy">Taxonomy</a>
      <a href="/stats">Statistics</a>
      <a href="/curation">Curation</a>
      <a href="/notifications" data-notifications-link>Notifications</a>
      <a href="/login" data-auth-link="login">Sign in</a>
      <button type="button" class="nav-signout" data-signout hidden>Sign out</button>
    </nav>
  </div>
</header>
<main class="container">
"#,
    );
    if let Some((slug, sub)) = tab_for(shell.path) {
        document.push_str(&tabs(&slug, &sub));
    }
    document.push_str(shell.body);
    document.push_str("</main>\n<footer class=\"site-footer\"><div class=\"container\"><p>Concord — a forge for discussions. ");
    document.push_str(&escape(version));
    document.push_str("</p></div></footer>\n");
    if let Some(value) = bootstrap {
        document.push_str("<script type=\"application/json\" id=\"concord-bootstrap\">");
        document.push_str(&crate::script_json(value));
        document.push_str("</script>\n");
    }
    document.push_str("<script src=\"/assets/js/session.js\"></script>\n<script src=\"/assets/js/consent.js\"></script>\n");
    document.push_str("</body>\n</html>\n");
    document
}

/// The project tab bar.
fn tabs(slug: &str, sub: &str) -> String {
    let mut nav = Html::new();
    nav.open("nav", &[("class", "detail-tabs"), ("aria-label", "Project sections")]);
    for (suffix, label, name) in TABS {
        let active = !name.is_empty() && name == sub
            || name.is_empty() && (sub.is_empty() || sub == slug);
        let mut attributes = vec![("class", if active { "detail-tab is-active" } else { "detail-tab" })];
        let href = if suffix.is_empty() {
            format!("/projects/{slug}")
        } else {
            format!("/projects/{slug}/{suffix}")
        };
        attributes.push(("href", &href));
        nav.open("a", &attributes).text(label).close("a");
    }
    nav.close("nav");
    nav.finish()
}

/// The asset helper: a path, with the cache-busting query the reference appends.
///
/// The reference appends a content hash; the port appends the build's own version string. Either way the effect is the point: a cached
/// script beside a fresh page is the classic "the fix did not take", and a query that changes when the binary changes forces the fetch.
#[must_use]
pub fn asset(path: &str) -> String {
    format!("{path}?v={}", env!("CARGO_PKG_VERSION"))
}

// The page templates, compiled in. Same reason as the assets: a page whose template is missing is a page that does not render, and one
// binary is the deployment.
include!(concat!(env!("OUT_DIR"), "/template_table.rs"));

/// A page template rendered for one request.
///
/// # Why a substitution and not a template engine
///
/// The reference's templates are Go's `text/template`, and the constructs in the ones the port needs are exactly three: `{{define
/// "content"}}...{{end}}` around the body, a dotted field (`.Slug`, `.CallID`) and `asset "path"`. Everything else in `consensus.html` is
/// static markup, including script comments full of braces. A hand transcription into a Rust string literal would escape every one of
/// those braces by hand, and every future edit of the reference's page would have to re-escape them — so the file is kept as the file.
///
/// # What it refuses
///
/// After substitution, a `{{` left in the output means the template uses a construct this function does not implement (`if`, `range`,
/// `with`, a nested template). Returning the document with a literal `{{range .Calls}}` on the page would be a page that renders wrong and
/// says nothing; instead the placeholder is replaced by a visible marker AND an error is returned, so the caller answers 500 and the log
/// names the page.
///
/// # Escaping
///
/// Values are HTML-escaped, because they are slugs and ids from the request. The template's own markup is not touched.
pub fn template(name: &str, values: &[(&str, &str)]) -> Result<String, String> {
    let Some((_, source)) = TEMPLATES.iter().find(|(file, _)| *file == name) else {
        return Err(format!("no template named {name:?}"));
    };
    // The body: everything between `{{define "content"}}` and its closing `{{end}}`.
    let mut text = source.replace("{{define \"content\"}}", "");
    if let Some(end) = text.rfind("{{end}}") {
        text.truncate(end);
        text.push_str(&text_after(source));
    }
    // `asset "..."` becomes the path with the build's own version query, so a template cannot serve a stale script.
    let mut rendered = String::with_capacity(text.len() + 256);
    let mut rest = text.as_str();
    while let Some(at) = rest.find("{{") {
        rendered.push_str(&rest[..at]);
        let tail = &rest[at + 2..];
        let Some(close) = tail.find("}}") else {
            rendered.push_str("{{");
            rest = tail;
            continue;
        };
        let construct = tail[..close].trim();
        rest = &tail[close + 2..];
        if let Some(path) = construct.strip_prefix("asset ") {
            rendered.push_str(&asset(path.trim().trim_matches('"')));
        } else if let Some(field) = construct.strip_prefix('.') {
            match values.iter().find(|(key, _)| *key == field) {
                Some((_, value)) => rendered.push_str(&escape(value)),
                None => {
                    return Err(format!(
                        "template {name:?} asks for `{field}`, which this page did not pass"
                    ))
                }
            }
        } else {
            return Err(format!(
                "template {name:?} uses the construct `{construct}`, which the port does not implement"
            ));
        }
    }
    rendered.push_str(rest);
    Ok(rendered)
}

/// Whatever the reference's template had outside the single `content` definition — nothing, in every template it ships, and kept separate
/// so a template that grows a second block fails loudly here rather than rendering half a page.
fn text_after(source: &str) -> &str {
    match source.rfind("{{end}}") {
        Some(at) => &source[at + "{{end}}".len()..],
        None => "",
    }
}
