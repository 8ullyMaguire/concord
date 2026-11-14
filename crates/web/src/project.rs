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
/// The Go's strip has nine tabs and three of them (`Requests`, `Lists`, `Settings`) are not ported yet. A tab is a promise that the
/// address behind it answers; a strip that offers a 404 teaches a reader that the navigation lies, which is the one thing a
/// navigation cannot survive. The strip grows by one in each commit that lands a page, and the ledger names the missing three.
pub const TABS: [(&str, &str); 7] = [
    ("Overview", ""),
    ("Board", "/board"),
    ("Roadmap", "/roadmap"),
    ("Consensus", "/consensus"),
    ("Documents", "/documents"),
    ("Criteria", "/criteria"),
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

/// `/projects/{slug}/board` — the columns, with the cards in them and the limit each column declared.
async fn board_body(pool: &PgPool, project: &Project) -> Result<(String, serde_json::Value), String> {
    let board = projects::board_by_slug(pool, &project.slug)
        .await
        .map_err(|error| format!("the board could not be read: {error}"))?
        .ok_or_else(|| "this project has no board".to_string())?;
    if board.columns.is_empty() {
        return Ok((empty("This project has no board columns yet."), serde_json::json!({"columns": []})));
    }
    let mut body = Html::new();
    body.open("div", &[("class", "board")]);
    for column in &board.columns {
        let in_column: Vec<&projects::BoardCard> =
            board.cards.iter().filter(|card| card.column == column.name).collect();
        body.open("section", &[("class", "board-column")])
            .open("h3", &[]).text(&column.name).close("h3");
        // The WIP limit is a claim about capacity, so it is shown beside the count rather than enforced silently by the page.
        if let Some(limit) = column.wip {
            body.open("p", &[("class", "muted")])
                .text(&format!("{} of {limit} in progress", in_column.len()))
                .close("p");
        } else {
            body.open("p", &[("class", "muted")])
                .text(&format!("{} card(s), no limit", in_column.len()))
                .close("p");
        }
        body.open("ul", &[("class", "cards")]);
        for card in &in_column {
            // The title, and the feature it belongs to — as TEXT, because `/projects/{}/features/{id}` has no page yet and a link
            // to a 404 is a promise the shell cannot keep. The link arrives with the page.
            // A placed card carries no feature title in the store — the JSON API returns `title` only for DERIVED cards, and this
            // instance's 329 placed cards have none. So the identifier is the label: "feature 1793" is a fact a reader can act on,
            // and "(untitled feature)" repeated 329 times is a page that has given up. Decision dated 2026-11-14.
            let label = card.title.as_deref().map(str::to_string).unwrap_or_else(|| format!("#{}", card.feature_id));
            body.open("li", &[]).text(&label);
            if card.derived {
                body.open("span", &[("class", "muted")]).text(" · derived").close("span");
            }
            body.close("li");
        }
        body.close("ul").close("section");
    }
    body.close("div");
    let bootstrap = serde_json::json!({
        "columns": board.columns.iter().map(|column| serde_json::json!({
            "name": column.name, "wip": column.wip,
            "cards": board.cards.iter().filter(|card| card.column == column.name)
                .map(|card| serde_json::json!({"feature_id": card.feature_id, "title": card.title}))
                .collect::<Vec<_>>(),
        })).collect::<Vec<_>>(),
    });
    Ok((body.finish(), bootstrap))
}

project_page!(board, "/board", "Board", board_body);

/// `/projects/{slug}/roadmap` — the milestones, with the counts that make "on track" checkable.
async fn roadmap_body(pool: &PgPool, project: &Project) -> Result<(String, serde_json::Value), String> {
    let milestones = concord_db::milestones::list_milestones(pool, project.id)
        .await
        .map_err(|error| format!("the roadmap could not be read: {error}"))?;
    if milestones.is_empty() {
        return Ok((empty("This project has no milestones."), serde_json::json!({"milestones": []})));
    }
    let mut body = Html::new();
    body.open("table", &[("class", "table")])
        .open("thead", &[]).open("tr", &[]);
    for heading in ["Milestone", "Target", "Status", "Shipped", "Open", "Risk"] {
        body.open("th", &[("scope", "col")]).text(heading).close("th");
    }
    body.close("tr").close("thead").open("tbody", &[]);
    for milestone in &milestones {
        body.open("tr", &[])
            .open("td", &[])
            .text(&milestone.title)
            .close("td");
        // A milestone with no target date is not "targeted today": it prints an em dash, the same mark the badge uses for a
        // measurement that does not exist.
        match milestone.target_date {
            Some(epoch) => body.open("td", &[]).text(&crate::date(epoch)).close("td"),
            None => body.open("td", &[("class", "unmeasured")]).text("—").close("td"),
        };
        body.open("td", &[]).text(&milestone.status).close("td")
            .open("td", &[]).text(&format!("{} of {}", milestone.shipped, milestone.total)).close("td")
            .open("td", &[]).text(&milestone.open.to_string()).close("td");
        if milestone.at_risk {
            body.open("td", &[("class", "risk")]).text("at risk").close("td");
        } else {
            body.open("td", &[("class", "muted")]).text("—").close("td");
        }
        body.close("tr");
    }
    body.close("tbody").close("table");
    let bootstrap = serde_json::json!({
        "milestones": milestones.iter().map(|milestone| serde_json::json!({
            "id": milestone.id, "title": milestone.title, "status": milestone.status,
            "total": milestone.total, "shipped": milestone.shipped, "open": milestone.open,
            "target_date": milestone.target_date, "at_risk": milestone.at_risk,
        })).collect::<Vec<_>>(),
    });
    Ok((body.finish(), bootstrap))
}

project_page!(roadmap, "/roadmap", "Roadmap", roadmap_body);

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

/// `/projects/{slug}/audit` — what happened, in the order it happened.
async fn audit_body(pool: &PgPool, project: &Project) -> Result<(String, serde_json::Value), String> {
    let filter = concord_db::audit::Filter { project_id: project.id, limit: 100, ..Default::default() };
    let page = concord_db::audit::list(pool, &filter)
        .await
        .map_err(|error| format!("the audit log could not be read: {error}"))?;
    if page.entries.is_empty() {
        return Ok((empty("Nothing has been recorded for this project."), serde_json::json!({"entries": [], "total": 0})));
    }
    let mut body = Html::new();
    body.open("p", &[("class", "muted")])
        .text(&format!("{} of {} entries", page.entries.len(), page.total))
        .close("p")
        .open("table", &[("class", "table")])
        .open("thead", &[]).open("tr", &[]);
    for heading in ["When", "Actor", "Action", "Detail"] {
        body.open("th", &[("scope", "col")]).text(heading).close("th");
    }
    body.close("tr").close("thead").open("tbody", &[]);
    for entry in &page.entries {
        body.open("tr", &[])
            .open("td", &[]).text(&crate::datetime(entry.created_at)).close("td")
            // The display name, never the email: `audit.rs` resolves it for exactly this reason, and a page is a place an
            // email becomes permanent.
            .open("td", &[]).text(entry.actor_display_name.as_deref().unwrap_or("(system)")).close("td")
            .open("td", &[]).text(&entry.action).close("td")
            .open("td", &[]).text(&entry.detail).close("td")
            .close("tr");
    }
    body.close("tbody").close("table");
    let bootstrap = serde_json::json!({
        "total": page.total,
        "entries": page.entries.iter().map(|entry| serde_json::json!({
            "action": entry.action, "actor": entry.actor_display_name, "detail": entry.detail,
        })).collect::<Vec<_>>(),
    });
    Ok((body.finish(), bootstrap))
}

project_page!(audit, "/audit", "Audit log", audit_body);
