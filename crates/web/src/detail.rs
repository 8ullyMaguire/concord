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

/// The project the reader may see, or the 404.
///
/// **Visibility is checked here and not in the API layer.** The API answers a private project with a 404 for a reader without access, and
/// the pages had no equivalent check: `/projects/{private}` rendered its mount and answered `200`, which told an anonymous reader that the
/// slug exists. The suite asserts the opposite in the strongest form it has — the 404 body must not contain the slug at all — so the
/// projection is "public, or nothing". A reader WITH a session is a case the web crate cannot see (it does not read tokens), and the reason
/// that is acceptable is that a private project's page leaks nothing a public one does not: the client's own `/api/v1` calls are refused,
/// so the mount fills with the same "no such project" the API gave it.
async fn project_of(pool: &PgPool, slug: &str) -> Option<Project> {
    let project = concord_db::projects::project_by_slug(pool, slug).await.ok().flatten()?;
    if project.visibility != "public" {
        return None;
    }
    Some(project)
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

/// `/projects/{slug}/features/{id}` — the reference's mount point, moderation panel and comment thread.
///
/// The reference builds this page in the browser from `/api/v1/projects/{slug}/features/{id}` and its neighbours; the suite asserts the
/// DOM that yields (`#feature-root h1`, a rating bar whose width differs between a settled and an uncertain feature, "not yet compared"
/// for a feature nobody has voted on). So the port renders the same mount, the same panel ids and the same scripts.
pub async fn feature(
    State(pool): State<PgPool>,
    Path((slug, id)): Path<(String, i64)>,
    headers: HeaderMap,
) -> Response {
    let Some(project) = project_of(&pool, &slug).await else { return crate::not_found(&headers) };
    // The page does NOT look the feature up, and that is the reference's contract rather than an omission: its handler renders the mount
    // for any id and lets `feature.js` report "No such feature", which is the only place the suite can see the difference between a
    // skeleton that never clears and a page that says why. A server-side 404 here would be a *different product behaviour* — and the probe
    // for it lives in the page script, not in this function.
    let body = format!(
        r#"<div class="page-head">
  <div>
    <p class="breadcrumb"><a href="/projects">Projects</a> / <a href="/projects/{slug}">{slug}</a> / <span>Feature</span></p>
    <h1>Feature &middot; <span data-slug>{slug}</span></h1>
    <p class="lede">The unit the ranking sorts on. Impact against effort, with the uncertainty shown rather than hidden &mdash; a new
      feature with no votes yet is <em>unsure</em>, not <em>low</em>.</p>
  </div>
</div>
<div id="feature-root" aria-busy="true">
<script src="{skeleton}"></script>
<script>(function(){{var b=document.getElementById("feature-root");if(b&&window.ConcordSkeleton){{b.innerHTML=window.ConcordSkeleton.rows(4);}}}})();</script>
  <div class="empty-state"><p class="empty-state-title">Loading the feature&hellip;</p></div>
</div>
<section id="moderation-panel" class="moderation-panel" data-project="{slug}" data-feature="{id}" hidden>
  <h2>Moderation</h2>
  <form id="moderation-form">
    <label for="moderation-reason">Reason</label>
    <textarea id="moderation-reason" name="reason" rows="2" maxlength="500"></textarea>
    <p class="note">A reason is required, and it is published in the modlog beside the act.</p>
    <div id="moderation-confirm" hidden>
      <p class="note">Remove this idea from the arena? It stops competing and its status becomes rejected &mdash; the idea itself is
        kept, and the act can be undone.</p>
      <button type="button" class="btn btn-secondary" id="moderation-cancel">Cancel</button>
    </div>
    <button type="submit" class="btn btn-secondary" id="moderation-remove">Remove from the arena</button>
  </form>
  <div id="moderation-undo" hidden><button type="button" class="btn btn-secondary" id="moderation-undo-btn">Undo</button></div>
  <p class="note" id="moderation-status" hidden></p>
</section>
<script src="{moderation}"></script>
<section id="comment-thread" class="comment-thread" data-thread-kind="feature" data-thread-id="{id}" aria-live="polite">
  <h2>Discussion</h2>
  <p class="note">Loading comments&hellip;</p>
</section>
<script src="{comments}"></script>
<script src="{feature_js}"></script>
"#,
        slug = project.slug,
        id = id,
        skeleton = crate::shell::asset("/assets/js/skeleton.js"),
        moderation = crate::shell::asset("/assets/js/moderation.js"),
        comments = crate::shell::asset("/assets/js/comments.js"),
        feature_js = crate::shell::asset("/assets/js/feature.js"),
    );
    render(&headers, Page {
        title: &format!("{}: feature {}", project.name, id),
        path: &format!("/projects/{slug}/features/{id}"),
        description: &format!("A feature of {}, on Concord.", project.name),
        bootstrap: serde_json::json!({"route": format!("/projects/{slug}/features/{id}"), "mount": "feature-root"}),
    }, body)
}

/// `/projects/{slug}/complaints/{id}` — the reference's mount, its comment thread and `complaint.js`.
///
/// The complaint page's requirements are "impact meter, linked features, status", and each has a way of being wrong that looks right: the
/// meter must show its ARITHMETIC (severity and frequency are not on one scale), the linked features come from an endpoint of their own
/// (`/complaints/{id}` does not carry them), and an unvalidated complaint must SAY it is not validated because §6.2 makes validation
/// load-bearing. All three are the script's job, so the port renders the mount and serves the script.
pub async fn complaint(
    State(pool): State<PgPool>,
    Path((slug, id)): Path<(String, i64)>,
    headers: HeaderMap,
) -> Response {
    let Some(project) = project_of(&pool, &slug).await else { return crate::not_found(&headers) };
    let body = format!(
        r#"<div class="page-head">
  <div>
    <p class="breadcrumb"><a href="/projects">Projects</a> / <a href="/projects/{slug}">{slug}</a> / <span>Complaint</span></p>
    <h1>Complaint &middot; <span data-slug>{slug}</span></h1>
    <p class="lede">A validated complaint is the only thing a feature may be built from. This is that evidence &mdash; the reported pain,
      how much of it there is, and which work answers it.</p>
  </div>
</div>
<div id="complaint-root" aria-busy="true">
<script src="{skeleton}"></script>
<script>(function(){{var b=document.getElementById("complaint-root");if(b&&window.ConcordSkeleton){{b.innerHTML=window.ConcordSkeleton.rows(4);}}}})();</script>
  <div class="empty-state"><p class="empty-state-title">Loading the complaint&hellip;</p></div>
</div>
<section id="comment-thread" class="comment-thread" data-thread-kind="complaint" data-thread-id="{id}" aria-live="polite">
  <h2>Discussion</h2>
  <p class="note">Loading comments&hellip;</p>
</section>
<script src="{comments}"></script>
<script src="{complaint_js}"></script>
"#,
        slug = project.slug,
        id = id,
        skeleton = crate::shell::asset("/assets/js/skeleton.js"),
        comments = crate::shell::asset("/assets/js/comments.js"),
        complaint_js = crate::shell::asset("/assets/js/complaint.js"),
    );
    render(&headers, Page {
        title: &format!("{}: complaint {}", project.name, id),
        path: &format!("/projects/{slug}/complaints/{id}"),
        description: &format!("A complaint against {}, on Concord.", project.name),
        bootstrap: serde_json::json!({"route": format!("/projects/{slug}/complaints/{id}"), "mount": "complaint-root"}),
    }, body)
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
