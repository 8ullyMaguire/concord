//! The project's own pages.
//!
//! One shell, nine tabs, and every tab reads the same store functions the JSON API reads. The reason they live in one module is the
//! reason they are one shell: a page that invents its own navigation is a page a reader cannot leave, and nine separate
//! implementations of "which tab is current" is nine chances to disagree about the answer.
//!
//! Each page states what it cannot show rather than showing a zero. An empty board, an unmeasured milestone and a missing target date
//! are three different facts, and a table that renders all three as `0` is a table nobody can act on.

use axum::{
    extract::{Path, State},
    http::HeaderMap,
    response::Response,
};
use concord_db::projects::{self, Project};
use sqlx::PgPool;

use crate::html::Html;
use crate::{render, Page};

/// The tabs, in the order the Go renders them — but **only the ones whose page exists**.
///
/// The Go's strip has nine tabs; `Settings` is reachable from the overview rather than the strip (the Go puts it in the strip, and the
/// difference is recorded in the ledger), and `Requests`/`Lists` joined when their pages landed. A tab is a promise that the
/// address behind it answers; a strip that offers a 404 teaches a reader that the navigation lies, which is the one thing a
/// navigation cannot survive. The strip grows by one in each commit that lands a page, and the ledger names the missing three.
pub const TABS: [(&str, &str); 10] = [
    ("Overview", ""),
    ("Board", "/board"),
    ("Roadmap", "/roadmap"),
    ("Consensus", "/consensus"),
    ("Documents", "/documents"),
    ("Criteria", "/criteria"),
    ("Lists", "/lists"),
    ("Requests", "/requests"),
    ("Merge", "/merge"),
    ("Audit", "/audit"),
];

/// The project and its id, or the 404 document.
async fn resolve(pool: &PgPool, slug: &str) -> Result<Project, ()> {
    match projects::project_by_slug(pool, slug).await {
        Ok(Some(project)) => Ok(project),
        _ => Err(()),
    }
}

/// The tab strip. `current` is the suffix of the tab being rendered.
fn tabs(slug: &str, current: &str) -> String {
    let mut nav = Html::new();
    nav.open("nav", &[("class", "project-tabs"), ("aria-label", "Project sections")]);
    for (label, suffix) in TABS {
        let mut attributes = vec![("href", format!("/projects/{slug}{suffix}"))];
        if suffix == current {
            attributes.push(("aria-current", "page".to_string()));
        }
        let borrowed: Vec<(&str, &str)> = attributes.iter().map(|(k, v)| (*k, v.as_str())).collect();
        nav.open("a", &borrowed).text(label).close("a");
    }
    nav.close("nav");
    nav.finish()
}

/// A whole project page: the title, the tab strip, the section body.
fn shell(project: &Project, current: &str, section: &str, body: String) -> String {
    let mut page = Html::new();
    page.open("header", &[("class", "project-header")])
        .open("h1", &[]).text(&project.name).close("h1")
        .open("p", &[("class", "muted")])
        .text(&format!("{} · {}", project.governance_model, project.visibility))
        .close("p")
        .close("header")
        .raw(&tabs(&project.slug, current))
        .open("h2", &[]).text(section).close("h2")
        .raw(&body);
    page.finish()
}

/// A section that has nothing to show says so, in the section's own words.
fn empty(message: &str) -> String {
    let mut body = Html::new();
    body.open("p", &[("class", "empty")]).text(message).close("p");
    body.finish()
}

macro_rules! project_page {
    ($name:ident, $tab:literal, $section:literal, $body:path) => {
        pub async fn $name(
            State(pool): State<PgPool>,
            Path(slug): Path<String>,
            headers: HeaderMap,
        ) -> Response {
            let Ok(project) = resolve(&pool, &slug).await else {
                return crate::not_found(&headers);
            };
            let section = $section;
            let result: Result<(String, serde_json::Value), String> = $body(&pool, &project).await;
            let (body, bootstrap) = match result {
                Ok(pair) => pair,
                Err(headline) => {
                    let mut failure = Html::new();
                    failure.open("p", &[("class", "empty")]).text(&headline).close("p");
                    (failure.finish(), serde_json::json!({"error": headline}))
                }
            };
            let path = format!("/projects/{}{}", project.slug, $tab);
            let title = format!("{}: {}", project.name, section);
            let description = format!("{section} for {}, on Concord.", project.name);
            render(&headers, Page {
                title: &title,
                path: &path,
                description: &description,
                bootstrap,
            }, shell(&project, $tab, section, body))
        }
    };
}

/// `/projects/{slug}/consensus` — the calls, newest first, with the verdict where there is one.
async fn consensus_body(pool: &PgPool, project: &Project) -> Result<(String, serde_json::Value), String> {
    let calls = concord_db::consensus::list_calls(pool, project.id, 50)
        .await
        .map_err(|error| format!("the consensus calls could not be read: {error}"))?;
    if calls.is_empty() {
        return Ok((empty("No consensus call has been opened for this project."), serde_json::json!({"calls": []})));
    }
    let mut body = Html::new();
    body.open("ul", &[("class", "calls")]);
    for call in &calls {
        body.open("li", &[])
            .text(call.question.as_deref().unwrap_or("(untitled call)"))
            .open("span", &[("class", "badge")]).text(&call.status).close("span");
        // A call that closed without a verdict is not a call that decided "nothing": the absence is printed.
        match call.result.as_deref() {
            Some(result) => body.open("p", &[]).text(result).close("p"),
            None => body.open("p", &[("class", "muted")]).text("No verdict recorded.").close("p"),
        };
        body.close("li");
    }
    body.close("ul");
    let bootstrap = serde_json::json!({
        "calls": calls.iter().map(|call| serde_json::json!({
            "id": call.id, "question": call.question, "status": call.status, "result": call.result,
        })).collect::<Vec<_>>(),
    });
    Ok((body.finish(), bootstrap))
}

project_page!(consensus, "/consensus", "Consensus", consensus_body);

/// `/projects/{slug}/documents` — the charter and everything else the project keeps in prose.
async fn documents_body(pool: &PgPool, project: &Project) -> Result<(String, serde_json::Value), String> {
    let documents = projects::list_documents(pool, project.id)
        .await
        .map_err(|error| format!("the documents could not be read: {error}"))?;
    if documents.is_empty() {
        return Ok((empty("This project has no documents."), serde_json::json!({"documents": []})));
    }
    let mut body = Html::new();
    body.open("ul", &[("class", "documents")]);
    for document in &documents {
        body.open("li", &[])
            .text(&document.title)
            .open("span", &[("class", "muted")])
            .text(&format!("{} · revision {}", document.kind, document.revision))
            .close("span")
            .close("li");
    }
    body.close("ul");
    let bootstrap = serde_json::json!({
        "documents": documents.iter().map(|document| serde_json::json!({
            "kind": document.kind, "slug": document.slug, "title": document.title, "revision": document.revision,
        })).collect::<Vec<_>>(),
    });
    Ok((body.finish(), bootstrap))
}

project_page!(documents, "/documents", "Documents", documents_body);

/// `/projects/{slug}/criteria` — what the project is judged on, and in which direction.
async fn criteria_body(pool: &PgPool, project: &Project) -> Result<(String, serde_json::Value), String> {
    let criteria = concord_db::criteria::list(pool, project.id)
        .await
        .map_err(|error| format!("the criteria could not be read: {error}"))?;
    if criteria.is_empty() {
        return Ok((empty("This project has no criteria."), serde_json::json!({"criteria": []})));
    }
    let mut body = Html::new();
    body.open("table", &[("class", "table")])
        .open("thead", &[]).open("tr", &[]);
    for heading in ["Criterion", "Direction", "Weight", "Counts"] {
        body.open("th", &[("scope", "col")]).text(heading).close("th");
    }
    body.close("tr").close("thead").open("tbody", &[]);
    for criterion in &criteria {
        body.open("tr", &[])
            .open("td", &[]).text(&criterion.name).close("td")
            .open("td", &[]).text(&criterion.direction).close("td")
            .open("td", &[]).text(&format!("{:.2}", criterion.default_weight)).close("td")
            // An inactive criterion still appears: deleting it would lose the history of what was once judged.
            .open("td", &[]).text(if criterion.active { "yes" } else { "no" }).close("td")
            .close("tr");
    }
    body.close("tbody").close("table");
    let bootstrap = serde_json::json!({
        "criteria": criteria.iter().map(|criterion| serde_json::json!({
            "slug": criterion.slug, "name": criterion.name, "direction": criterion.direction,
            "default_weight": criterion.default_weight, "active": criterion.active,
        })).collect::<Vec<_>>(),
    });
    Ok((body.finish(), bootstrap))
}

project_page!(criteria, "/criteria", "Criteria", criteria_body);

/// `/projects/{slug}/audit` — the reference's audit mount.
///
/// `audit.js` fetches `/api/v1/projects/{slug}/audit` with the filters in the address and draws the table, the count and the empty state.
/// The suite asserts the CLIENT's answers — the count is the FILTERED total, a filter matching nothing says so, a `%` in the search box is
/// matched literally rather than as a wildcard, and a detail containing HTML is escaped — and a server-rendered table satisfies none of them.
pub async fn audit(State(pool): State<PgPool>, Path(slug): Path<String>, headers: HeaderMap) -> Response {
    let Ok(project) = resolve(&pool, &slug).await else { return crate::not_found(&headers) };
    if project.visibility != "public" {
        return crate::not_found(&headers);
    }
    let body = format!(
        r#"<div class="page-head">
  <div>
    <p class="breadcrumb"><a href="/projects">Projects</a> / <a href="/projects/{slug}">{slug}</a> / <span>Audit log</span></p>
    <h1>Audit log &middot; <span data-slug>{slug}</span></h1>
    <p class="lede">Everything privileged that happened here, in order, newest first. Reading it needs no account; the actions in it
      do.</p>
  </div>
</div>
<div id="audit-root" aria-busy="true">
<script src="{skeleton}"></script>
<script>(function(){{var b=document.getElementById("audit-root");if(b&&window.ConcordSkeleton){{b.innerHTML=window.ConcordSkeleton.rows(8);}}}})();</script>
  <div class="empty-state"><p class="empty-state-title">Loading the audit log&hellip;</p></div>
</div>
<script src="{audit_js}"></script>
"#,
        slug = project.slug,
        skeleton = crate::shell::asset("/assets/js/skeleton.js"),
        audit_js = crate::shell::asset("/assets/js/audit.js"),
    );
    let title = format!("{}: audit log", project.name);
    render(&headers, Page {
        title: &title,
        path: &format!("/projects/{}/audit", project.slug),
        description: &format!("The audit log of {}, on Concord.", project.name),
        bootstrap: serde_json::json!({"route": format!("/projects/{}/audit", project.slug), "mount": "audit-root"}),
    }, body)
}

// ── the two pages the reference renders in the browser ───────────────────────────────────────────────────────────────────────────────
//
// `board_body` and `roadmap_body` used to build these on the server, and the first suite run is what showed the cost: `probe_pages.py`
// asserted the columns and the milestones were on the page, the suite asserted `milestones.js` had drawn `.ms-counts`, and only the
// second one is what a reader sees. The bodies are the reference's mounts now; nothing on the server counts anything.

/// `/projects/{slug}/board` — the reference's kanban mount.
///
/// `board.js` fetches `/api/v1/projects/{slug}/board` and builds the columns; the suite asserts the cards it drew, that `?milestone=`
/// narrows them, and that a feature outside the milestone is ABSENT. A server-rendered board satisfies none of those.
pub async fn board(State(pool): State<PgPool>, Path(slug): Path<String>, headers: HeaderMap) -> Response {
    let Ok(project) = resolve(&pool, &slug).await else { return crate::not_found(&headers) };
    let body = format!(
        r#"<div class="page-header">
  <h1 class="page-title">Board <span class="muted" style="color:var(--slate-500);font-weight:400;">&middot; {slug}</span></h1>
  <p class="page-subtitle">Phase-gated kanban — each advance is consented.</p>
</div>
<div id="kanban-board" aria-busy="true"></div>
<script src="{skeleton}"></script>
<script>(function(){{var b=document.getElementById("kanban-board");if(b&&window.ConcordSkeleton){{b.innerHTML=window.ConcordSkeleton.board(4,2);}}}})();</script>
<script src="{board_js}"></script>
"#,
        slug = project.slug,
        skeleton = crate::shell::asset("/assets/js/skeleton.js"),
        board_js = crate::shell::asset("/assets/js/board.js"),
    );
    let title = format!("{}: Board", project.name);
    render(&headers, Page {
        title: &title,
        path: &format!("/projects/{}/board", project.slug),
        description: &format!("The board for {}, on Concord.", project.name),
        bootstrap: serde_json::json!({"route": format!("/projects/{}/board", project.slug), "mount": "kanban-board"}),
    }, body)
}

/// `/projects/{slug}/roadmap` — the reference's milestone timeline mount.
///
/// `milestones.js` builds it, and the suite asserts the COUNTS it prints (`.ms-counts`: "0/1 shipped", "1 open") rather than the shape of
/// the timeline, because a timeline nobody can count is a picture.
pub async fn roadmap(State(pool): State<PgPool>, Path(slug): Path<String>, headers: HeaderMap) -> Response {
    let Ok(project) = resolve(&pool, &slug).await else { return crate::not_found(&headers) };
    let body = format!(
        r#"<div class="page-header">
  <h1 class="page-title">Roadmap <span class="muted" style="color:var(--slate-500);font-weight:400;">&middot; {slug}</span></h1>
  <p class="page-subtitle">Milestones on a timeline &mdash; planned versus shipped, with the slip measured rather than asserted.</p>
</div>
<div id="roadmap" data-slug="{slug}" aria-busy="true"><p class="muted">Loading milestones&hellip;</p></div>
<script src="{milestones_js}"></script>
"#,
        slug = project.slug,
        milestones_js = crate::shell::asset("/assets/js/milestones.js"),
    );
    let title = format!("{}: Roadmap", project.name);
    render(&headers, Page {
        title: &title,
        path: &format!("/projects/{}/roadmap", project.slug),
        description: &format!("The roadmap for {}, on Concord.", project.name),
        bootstrap: serde_json::json!({"route": format!("/projects/{}/roadmap", project.slug), "mount": "roadmap"}),
    }, body)
}
