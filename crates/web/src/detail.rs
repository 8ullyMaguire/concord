//! The pages for one row: a request, a list, a milestone, a feature, a complaint, the merge queue, and the settings.
//!
//! These are the pages a reader reaches by clicking a row rather than a tab, so their job is narrower than a project page's: put one
//! record on screen with the facts that make it checkable, and link back to the thing it belongs to.
//!
//! The shape rule they share with the rest of the shell: **an absent fact is printed as absent.** A request with no accepted answer
//! says so; a milestone with no target date prints an em dash; a complaint that is not merged does not show a merge pointer at zero.
//! And a record that does not belong to the project in the URL is a 404, not a 200 with someone else's row on it — the same
//! scoping rule the write routes already keep.

use axum::{
    extract::{Path, State},
    http::HeaderMap,
    response::Response,
};
use concord_db::projects::Project;
use sqlx::PgPool;

use crate::html::Html;
use crate::{render, Page};

/// The project and the row's id, or the 404.
async fn project_of(pool: &PgPool, slug: &str) -> Option<Project> {
    concord_db::projects::project_by_slug(pool, slug).await.ok().flatten()
}

/// A labelled fact, for the modules that build the same `<dl>` shape.
pub(crate) fn fact_public(body: &mut Html, label: &str, value: &str) {
    fact(body, label, value);
}

/// A labelled fact, the shape most of these pages are made of.
fn fact(body: &mut Html, label: &str, value: &str) {
    body.open("dt", &[]).text(label).close("dt")
        .open("dd", &[]).text(value).close("dd");
}

/// `/projects/{slug}/requests` — what the project has been asked for.
pub async fn requests(
    State(pool): State<PgPool>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Response {
    let Some(project) = project_of(&pool, &slug).await else { return crate::not_found(&headers) };
    let rows = match concord_db::requests::by_project(&pool, project.id).await {
        Ok(rows) => rows,
        Err(error) => return crate::error_page(&headers, "the requests could not be read", &error.to_string()),
    };
    let mut body = Html::new();
    if rows.is_empty() {
        body.open("p", &[("class", "empty")]).text("This project has no requests.").close("p");
    } else {
        body.open("ul", &[("class", "rows")]);
        for row in &rows {
            body.open("li", &[])
                .open("a", &[("href", &format!("/projects/{slug}/requests/{}", row.id))])
                .text(&row.title)
                .close("a")
                .open("span", &[("class", "badge")]).text(&row.status).close("span");
            if row.accepted_answer_id.is_some() {
                body.open("span", &[("class", "accepted")]).text("accepted fit").close("span");
            }
            body.close("li");
        }
        body.close("ul");
    }
    let bootstrap = serde_json::json!({
        "requests": rows.iter().map(|row| serde_json::json!({
            "id": row.id, "title": row.title, "status": row.status,
            "accepted_answer_id": row.accepted_answer_id,
        })).collect::<Vec<_>>(),
    });
    render(&headers, Page {
        title: &format!("{}: Requests", project.name),
        path: &format!("/projects/{slug}/requests"),
        description: &format!("Requests made of {}, on Concord.", project.name),
        bootstrap,
    }, body.finish())
}

/// `/projects/{slug}/requests/{id}` — one request and the answers to it.
pub async fn request(
    State(pool): State<PgPool>,
    Path((slug, id)): Path<(String, i64)>,
    headers: HeaderMap,
) -> Response {
    let Some(project) = project_of(&pool, &slug).await else { return crate::not_found(&headers) };
    let row = match concord_db::requests::get(&pool, id).await {
        // `project_id == 0` is an instance-wide request; `requests::by_project` does not list those, but a URL carried from the
        // instance board still has to answer rather than 404 behind a link somebody copied.
        Ok(Some(row)) if row.project_id == project.id || row.project_id == 0 => row,
        Ok(_) => return crate::not_found(&headers),
        Err(error) => return crate::error_page(&headers, "the request could not be read", &error.to_string()),
    };
    let answers = concord_db::requests::answers(&pool, id).await.unwrap_or_default();
    let mut body = Html::new();
    body.open("h3", &[]).text(&row.title).close("h3")
        .open("p", &[]).text(&row.body).close("p")
        .open("dl", &[("class", "facts")]);
    fact(&mut body, "Status", &row.status);
    // The accepted answer is a BADGE the author sets, and it does not override the ranking — so it is named as the author's choice.
    match row.accepted_answer_id {
        Some(answer) => fact(&mut body, "Accepted by the author", &format!("answer {answer}")),
        None => fact(&mut body, "Accepted by the author", "none"),
    }
    body.close("dl").open("h3", &[]).text(&format!("{} answer(s)", answers.len())).close("h3");
    if answers.is_empty() {
        body.open("p", &[("class", "empty")]).text("Nobody has answered yet.").close("p");
    } else {
        body.open("ul", &[("class", "answers")]);
        for answer in &answers {
            body.open("li", &[])
                .open("p", &[]).text(&answer.body).close("p")
                .open("p", &[("class", "muted")])
                .text(&format!("{} · rating {:.0} ± {:.0}", answer.status, answer.elo_r, answer.elo_rd))
                .close("p")
                .close("li");
        }
        body.close("ul");
    }
    let bootstrap = serde_json::json!({
        "request": {"id": row.id, "title": row.title, "status": row.status,
                    "accepted_answer_id": row.accepted_answer_id},
        "answers": answers.iter().map(|answer| serde_json::json!({
            "id": answer.id, "status": answer.status, "elo_r": answer.elo_r,
        })).collect::<Vec<_>>(),
    });
    render(&headers, Page {
        title: &format!("{}: {}", project.name, row.title),
        path: &format!("/projects/{slug}/requests/{id}"),
        description: &row.body.chars().take(160).collect::<String>(),
        bootstrap,
    }, body.finish())
}

/// `/projects/{slug}/lists` — the curated lists the project keeps.
pub async fn lists(
    State(pool): State<PgPool>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Response {
    let Some(project) = project_of(&pool, &slug).await else { return crate::not_found(&headers) };
    let rows = concord_db::criteria::lists(&pool, project.id).await.unwrap_or_default();
    let mut body = Html::new();
    if rows.is_empty() {
        body.open("p", &[("class", "empty")]).text("This project keeps no lists.").close("p");
    } else {
        body.open("ul", &[("class", "rows")]);
        for row in &rows {
            body.open("li", &[])
                .open("a", &[("href", &format!("/projects/{slug}/lists/{}", row.id))])
                .text(&row.title)
                .close("a")
                .open("span", &[("class", "badge")]).text(&row.status).close("span")
                .close("li");
        }
        body.close("ul");
    }
    let bootstrap = serde_json::json!({
        "lists": rows.iter().map(|row| serde_json::json!({
            "id": row.id, "name": row.title, "status": row.status,
        })).collect::<Vec<_>>(),
    });
    render(&headers, Page {
        title: &format!("{}: Lists", project.name),
        path: &format!("/projects/{slug}/lists"),
        description: &format!("Curated lists kept by {}, on Concord.", project.name),
        bootstrap,
    }, body.finish())
}

/// `/projects/{slug}/lists/{id}` — one list and its entries.
pub async fn list(
    State(pool): State<PgPool>,
    Path((slug, id)): Path<(String, i64)>,
    headers: HeaderMap,
) -> Response {
    let Some(project) = project_of(&pool, &slug).await else { return crate::not_found(&headers) };
    let row = match concord_db::criteria::list_by_id(&pool, id).await {
        // A list with `project_id IS NULL` is instance-wide, and `criteria::lists` deliberately includes those on every project's
        // page — so a 404 here would be a link the page itself just printed. Another PROJECT's list is still a 404.
        Ok(Some(row)) if row.project_id.is_none() || row.project_id == Some(project.id) => row,
        Ok(_) => return crate::not_found(&headers),
        Err(error) => return crate::error_page(&headers, "the list could not be read", &error.to_string()),
    };
    let entries = concord_db::criteria::entries(&pool, id).await.unwrap_or_default();
    let mut body = Html::new();
    body.open("h3", &[]).text(&row.title).close("h3");
    if !row.description.is_empty() {
        body.open("p", &[]).text(&row.description).close("p");
    }
    body.open("p", &[("class", "muted")])
        .text(&format!("{} · {} entries", row.status, entries.len()))
        .close("p");
    if entries.is_empty() {
        body.open("p", &[("class", "empty")]).text("This list has no entries yet.").close("p");
    } else {
        body.open("ul", &[("class", "entries")]);
        for entry in &entries {
            body.open("li", &[])
                .open("a", &[("href", &entry.url), ("rel", "noopener")]).text(&entry.title).close("a");
            // `category` is `Option` because "no category" is a fact: a chip with an empty label reads as a missing category the
            // page failed to load, so an unset one is simply not drawn.
            if let Some(category) = &entry.category {
                body.open("span", &[("class", "badge")]).text(category).close("span");
            }
            body.open("span", &[("class", "muted")])
                .text(&format!(" · {} · rating {:.0}", entry.status, entry.elo_r))
                .close("span")
                .close("li");
        }
        body.close("ul");
    }
    let bootstrap = serde_json::json!({
        "list": {"id": row.id, "name": row.title, "status": row.status},
        "entries": entries.iter().map(|entry| serde_json::json!({
            "url": entry.url, "title": entry.title, "category": entry.category,
        })).collect::<Vec<_>>(),
    });
    render(&headers, Page {
        title: &format!("{}: {}", project.name, row.title),
        path: &format!("/projects/{slug}/lists/{id}"),
        description: &format!("{} — a list kept by {}.", row.title, project.name),
        bootstrap,
    }, body.finish())
}

/// `/projects/{slug}/milestones/{id}` — one milestone, its counts and its features.
pub async fn milestone(
    State(pool): State<PgPool>,
    Path((slug, id)): Path<(String, i64)>,
    headers: HeaderMap,
) -> Response {
    let Some(project) = project_of(&pool, &slug).await else { return crate::not_found(&headers) };
    let row = match concord_db::milestones::milestone(&pool, project.id, id).await {
        Ok(Some(row)) => row,
        Ok(None) => return crate::not_found(&headers),
        Err(error) => return crate::error_page(&headers, "the milestone could not be read", &error.to_string()),
    };
    let features = concord_db::features::milestone_features(&pool, id).await.unwrap_or_default();
    let mut body = Html::new();
    body.open("h3", &[]).text(&row.title).close("h3");
    if !row.body.is_empty() {
        body.open("p", &[]).text(&row.body).close("p");
    }
    body.open("dl", &[("class", "facts")]);
    fact(&mut body, "Status", &row.status);
    match row.target_date {
        Some(epoch) => fact(&mut body, "Target", &crate::date(epoch)),
        None => fact(&mut body, "Target", "—"),
    }
    fact(&mut body, "Shipped", &format!("{} of {}", row.shipped, row.total));
    fact(&mut body, "Open", &row.open.to_string());
    if row.at_risk {
        fact(&mut body, "Risk", "at risk");
    }
    // Seeded and unshipped features are counts of features whose events are MISSING — the numbers a milestone's honesty depends on,
    // so they are shown rather than left to a JSON client that may never read them.
    if row.seeded > 0 {
        fact(&mut body, "Seeded without a creation event", &row.seeded.to_string());
    }
    if row.unshipped > 0 {
        fact(&mut body, "Shipped without a shipment event", &row.unshipped.to_string());
    }
    body.close("dl").open("h3", &[]).text(&format!("{} feature(s)", features.len())).close("h3");
    if features.is_empty() {
        body.open("p", &[("class", "empty")]).text("No feature is attached to this milestone.").close("p");
    } else {
        body.open("ul", &[("class", "rows")]);
        for feature in &features {
            body.open("li", &[])
                .open("a", &[("href", &format!("/projects/{slug}/features/{}", feature.id))])
                .text(&feature.title)
                .close("a")
                .open("span", &[("class", "badge")]).text(&feature.status).close("span")
                .close("li");
        }
        body.close("ul");
    }
    let bootstrap = serde_json::json!({
        "milestone": {"id": row.id, "title": row.title, "status": row.status,
                      "total": row.total, "shipped": row.shipped, "open": row.open,
                      "target_date": row.target_date, "at_risk": row.at_risk},
    });
    render(&headers, Page {
        title: &format!("{}: {}", project.name, row.title),
        path: &format!("/projects/{slug}/milestones/{id}"),
        description: &row.body.chars().take(160).collect::<String>(),
        bootstrap,
    }, body.finish())
}

/// `/projects/{slug}/features/{id}` — one feature, its ratings and the complaints it answers.
pub async fn feature(
    State(pool): State<PgPool>,
    Path((slug, id)): Path<(String, i64)>,
    headers: HeaderMap,
) -> Response {
    let Some(project) = project_of(&pool, &slug).await else { return crate::not_found(&headers) };
    let row = match concord_db::features::feature(&pool, id).await {
        Ok(Some(row)) if row.project_id == project.id => row,
        Ok(_) => return crate::not_found(&headers),
        Err(error) => return crate::error_page(&headers, "the feature could not be read", &error.to_string()),
    };
    let complaints = concord_db::complaints::feature_complaints(&pool, id).await.unwrap_or_default();
    let mut body = Html::new();
    body.open("h3", &[]).text(&row.title).close("h3");
    if !row.body.is_empty() {
        body.open("p", &[]).text(&row.body).close("p");
    }
    body.open("dl", &[("class", "facts")]);
    fact(&mut body, "Status", &row.status);
    fact(&mut body, "Effort", if row.effort.is_empty() { "—" } else { &row.effort });
    // An unmeasured ratio is absent, and its absence is printed: `impact_ratio` is `Option` because a ratio over no scale is not 0.
    match row.impact_ratio {
        Some(ratio) => fact(&mut body, "Impact / effort", &format!("{ratio:.2}")),
        None => fact(&mut body, "Impact / effort", "—"),
    }
    fact(&mut body, "Rating", &format!("{:.0} ± {:.0}", row.elo_r, row.elo_rd));
    body.close("dl").open("h3", &[]).text(&format!("{} linked complaint(s)", complaints.len())).close("h3");
    if complaints.is_empty() {
        body.open("p", &[("class", "empty")]).text("This feature answers no complaint.").close("p");
    } else {
        body.open("ul", &[("class", "rows")]);
        for complaint in &complaints {
            body.open("li", &[])
                .open("a", &[("href", &format!("/projects/{slug}/complaints/{}", complaint.id))])
                .text(&complaint.title)
                .close("a")
                .open("span", &[("class", "muted")])
                .text(&format!(" · severity {}", complaint.severity))
                .close("span")
                .close("li");
        }
        body.close("ul");
    }
    let bootstrap = serde_json::json!({
        "feature": {"id": row.id, "title": row.title, "status": row.status,
                    "elo_r": row.elo_r, "impact_ratio": row.impact_ratio},
        "complaints": complaints.iter().map(|c| serde_json::json!({"id": c.id, "title": c.title}))
            .collect::<Vec<_>>(),
    });
    render(&headers, Page {
        title: &format!("{}: {}", project.name, row.title),
        path: &format!("/projects/{slug}/features/{id}"),
        description: &row.body.chars().take(160).collect::<String>(),
        bootstrap,
    }, body.finish())
}

/// `/projects/{slug}/complaints/{id}` — one complaint.
pub async fn complaint(
    State(pool): State<PgPool>,
    Path((slug, id)): Path<(String, i64)>,
    headers: HeaderMap,
) -> Response {
    let Some(project) = project_of(&pool, &slug).await else { return crate::not_found(&headers) };
    let row = match concord_db::complaints::complaint(&pool, id).await {
        Ok(Some(row)) if row.project_id == project.id => row,
        Ok(_) => return crate::not_found(&headers),
        Err(error) => return crate::error_page(&headers, "the complaint could not be read", &error.to_string()),
    };
    let mut body = Html::new();
    body.open("h3", &[]).text(&row.title).close("h3")
        .open("p", &[]).text(&row.body).close("p")
        .open("dl", &[("class", "facts")]);
    fact(&mut body, "Status", &row.status);
    fact(&mut body, "Severity", &row.severity.to_string());
    fact(&mut body, "Frequency", &format!("{:.2}", row.frequency));
    fact(&mut body, "Strategic multiplier", &format!("{:.2}", row.strategic_multiplier));
    // `merged_into` is absent when the complaint is not merged — a pointer to complaint 0 would be a link to nothing.
    if let Some(target) = row.merged_into {
        fact(&mut body, "Merged into", &format!("complaint {target}"));
    }
    body.close("dl");
    let bootstrap = serde_json::json!({
        "complaint": {"id": row.id, "title": row.title, "status": row.status,
                      "severity": row.severity, "merged_into": row.merged_into},
    });
    render(&headers, Page {
        title: &format!("{}: {}", project.name, row.title),
        path: &format!("/projects/{slug}/complaints/{id}"),
        description: &row.body.chars().take(160).collect::<String>(),
        bootstrap,
    }, body.finish())
}

/// `/projects/{slug}/merge` — the merge queue, with the gate each request is at.
pub async fn merge(
    State(pool): State<PgPool>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Response {
    let Some(project) = project_of(&pool, &slug).await else { return crate::not_found(&headers) };
    let rows = match concord_db::merge_writes::list(&pool, project.id).await {
        Ok(rows) => rows,
        Err(error) => return crate::error_page(&headers, "the merge queue could not be read", &error.to_string()),
    };
    let mut body = Html::new();
    if rows.is_empty() {
        body.open("p", &[("class", "empty")]).text("No merge request has been opened for this project.").close("p");
    } else {
        body.open("table", &[("class", "table")])
            .open("thead", &[]).open("tr", &[]);
        for heading in ["Request", "Status", "External ref", "Opened", "Closed"] {
            body.open("th", &[("scope", "col")]).text(heading).close("th");
        }
        body.close("tr").close("thead").open("tbody", &[]);
        for row in &rows {
            body.open("tr", &[])
                .open("td", &[]).text(&row.title).close("td")
                .open("td", &[]).text(&row.status).close("td");
            // An absent external reference prints "—": a merge request that names no upstream is a fact, not an empty string.
            match &row.external_ref {
                Some(reference) => body.open("td", &[]).text(reference).close("td"),
                None => body.open("td", &[("class", "unmeasured")]).text("—").close("td"),
            };
            body.open("td", &[]).text(&crate::datetime(row.opened_at)).close("td");
            match row.closed_at {
                Some(epoch) => body.open("td", &[]).text(&crate::datetime(epoch)).close("td"),
                None => body.open("td", &[("class", "unmeasured")]).text("—").close("td"),
            };
            body.close("tr");
        }
        body.close("tbody").close("table");
    }
    let bootstrap = serde_json::json!({
        "merges": rows.iter().map(|row| serde_json::json!({
            "id": row.id, "title": row.title, "status": row.status, "external_ref": row.external_ref,
        })).collect::<Vec<_>>(),
    });
    render(&headers, Page {
        title: &format!("{}: Merge requests", project.name),
        path: &format!("/projects/{slug}/merge"),
        description: &format!("Merge requests for {}, on Concord.", project.name),
        bootstrap,
    }, body.finish())
}

/// `/projects/{slug}/settings` — the charter, and what the project has declared about itself.
pub async fn settings(
    State(pool): State<PgPool>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Response {
    let Some(project) = project_of(&pool, &slug).await else { return crate::not_found(&headers) };
    let charter = match concord_db::charter::charter(&pool, project.id).await {
        Ok(charter) => charter,
        Err(error) => return crate::error_page(&headers, "the charter could not be read", &error.to_string()),
    };
    let mut body = Html::new();
    body.open("dl", &[("class", "facts")]);
    fact(&mut body, "Governance model", &project.governance_model);
    fact(&mut body, "Visibility", &project.visibility);
    fact(&mut body, "Intake mode", &project.intake_mode);
    body.close("dl").open("h3", &[]).text("Charter").close("h3");
    // The charter's numbers, each with the name of the rule it sets. `accepted_fit_overrides_ranking` is deliberately ABSENT from the
    // JSON API's shape and from this page: §17 makes the author's accepted answer a badge, never an override, so a field implying the
    // opposite would be a rule the product does not have.
    body.open("dl", &[("class", "facts")]);
    fact(&mut body, "Quorum", &format!("{:.0}% of eligible, at least {}",
        charter.quorum_ratio * 100.0, charter.quorum_min));
    fact(&mut body, "Support over all positions", &format!("{:.0}%", charter.support_ratio_min * 100.0));
    fact(&mut body, "Consent over positions that took a side", &format!("{:.0}%", charter.consent_ratio * 100.0));
    fact(&mut body, "Override window", &format!("{:.0}%", charter.override_ratio * 100.0));
    fact(&mut body, "Vote window", &format!("{:.1} days", charter.vote_window_days));
    fact(&mut body, "Merge quorum", &if charter.merge_requires_quorum {
        format!("{:.0}%, at least {}", charter.merge_quorum_ratio * 100.0, charter.merge_quorum_min)
    } else {
        "not required".to_string()
    });
    fact(&mut body, "Reviewer approval", if charter.require_reviewer_approval { "required" } else { "not required" });
    fact(&mut body, "WIP limits", &format!("in progress {}, review {}",
        charter.wip_in_progress, charter.wip_review));
    body.close("dl");
    let bootstrap = serde_json::json!({
        "settings": {"governance_model": project.governance_model,
                     "visibility": project.visibility, "intake_mode": project.intake_mode,
                     "quorum_ratio": charter.quorum_ratio, "quorum_min": charter.quorum_min,
                     "vote_window_days": charter.vote_window_days,
                     "merge_requires_quorum": charter.merge_requires_quorum},
    });
    render(&headers, Page {
        title: &format!("{}: Settings", project.name),
        path: &format!("/projects/{slug}/settings"),
        description: &format!("How {} is governed, on Concord.", project.name),
        bootstrap,
    }, body.finish())
}
