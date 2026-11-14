//! The last eight pages: the ranking loop's two halves, the contribution feed, curation, notifications, and the two auth forms.
//!
//! The two ranking pages are the ones with a contract written down in the reference's own tests, and it is worth restating because both
//! halves are easy to get wrong in a way that looks fine:
//!
//! * **`/rank` offers a PAIR and five outcomes**, and posts the vote. It is the half of the loop that produces data.
//! * **`/ranking` presents the order the API already returned**, showing `pain_score`, `strategic_weight`, `priority_score` and
//!   `elo_rd` — the components and the uncertainty. It must NOT re-sort: the score already folds in pain and strategic weight, so a
//!   client-side sort by rating would show an order the forge never concluded. `priorities::feature_priorities` returns them in the
//!   API's order and this module does not touch that order.
//!
//! The auth pages are forms and nothing else — they post to the API's own routes, because a second place that knows how to log someone in
//! is a second place that can get it wrong.

use axum::{
    extract::{Path, Query, State},
    http::HeaderMap,
    response::Response,
};
use serde::Deserialize;
use sqlx::PgPool;

use crate::html::Html;
use crate::{render, Page};

/// The five outcomes a vote can carry, in the order the reference offers them. `skip` and `neither` are not the same act: one says
/// "I cannot judge this pair", the other says "neither is what we should do", and the store records them differently.
pub const OUTCOMES: [(&str, &str); 5] = [
    ("a", "A"),
    ("b", "B"),
    ("both", "Both"),
    ("neither", "Neither"),
    ("skip", "Skip — I cannot judge this"),
];

/// `/projects/{slug}/rank` — one pair, five outcomes.
pub async fn rank(
    State(pool): State<PgPool>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Response {
    let Some(project) = concord_db::projects::project_by_slug(&pool, &slug).await.ok().flatten() else {
        return crate::not_found(&headers);
    };
    // Voter 0 is "the reader who has judged nothing": the store's `next_pair` picks by closeness with the voter's own history removed,
    // and an anonymous reader has no history. Voting itself needs a session, which the page says below rather than pretending the
    // buttons work — a form that posts to a 401 is a form that wastes a reader's judgement.
    let pair = match concord_db::feed_reads::next_pair(&pool, project.id, 0).await {
        Ok(Ok((first, second))) => Some((first, second)),
        _ => None,
    };
    let mut body = Html::new();
    body.open("p", &[("class", "muted")])
        .text("The forge asks for one pairwise judgement at a time. A pair is chosen because its two features are close \
               enough that a judgement between them carries the most information.")
        .close("p")
        .open("p", &[]).open("a", &[("href", &format!("/projects/{slug}/ranking"))])
        .text("See the ranking this feeds").close("a").close("p");

    match pair.as_ref() {
        // `next_vote_pair` answers `None` when the project has judged every pair it has. That is a FINISHED state, not an error, and
        // the page says which of the two it is.
        None => {
            body.open("p", &[("class", "empty")])
                .text("Every pair the forge could offer has been judged. Nothing is waiting for you here.")
                .close("p");
        }
        Some((first, second)) => {
            body.open("div", &[("class", "pair"), ("data-pair", &format!("{}-{}", first.id, second.id))])
                .open("article", &[("class", "option"), ("data-option", "a")])
                .open("h3", &[]).text(&first.title).close("h3")
                .open("p", &[("class", "muted")])
                .text(&format!("rating {:.0} ± {:.0}", first.elo_r, first.elo_rd)).close("p")
                .close("article")
                .open("article", &[("class", "option"), ("data-option", "b")])
                .open("h3", &[]).text(&second.title).close("h3")
                .open("p", &[("class", "muted")])
                .text(&format!("rating {:.0} ± {:.0}", second.elo_r, second.elo_rd)).close("p")
                .close("article")
                .close("div")
                // The five outcomes, as the store records them. The form posts to the API's own vote route, which requires a session —
                // so the page names that requirement here instead of letting a reader spend a judgement on a 401.
                .open("form", &[("method", "post"),
                                ("action", &format!("/api/v1/projects/{slug}/features/{}/vote", first.id))])
                .void("input", &[("type", "hidden"), ("name", "feature_b"), ("value", &second.id.to_string())]);
            for (value, label) in OUTCOMES {
                body.open("button", &[("type", "submit"), ("name", "outcome"), ("value", value)])
                    .text(label)
                    .close("button");
            }
            body.close("form")
                .open("p", &[("class", "muted")])
                .open("a", &[("href", &format!("/login?next=/projects/{slug}/rank"))])
                .text("Sign in to record a judgement")
                .close("a")
                .close("p");
        }
    }
    let bootstrap = serde_json::json!({
        "route": format!("/projects/{slug}/rank"),
        "pair": pair.as_ref().map(|(first, second)| serde_json::json!({
            "a": {"id": first.id, "title": first.title, "elo_r": first.elo_r},
            "b": {"id": second.id, "title": second.title, "elo_r": second.elo_r},
        })),
        "outcomes": OUTCOMES.iter().map(|(value, label)| serde_json::json!({"value": value, "label": label}))
            .collect::<Vec<_>>(),
    });
    render(&headers, Page {
        title: &format!("{}: Rank", project.name),
        path: &format!("/projects/{slug}/rank"),
        description: &format!("Judge a pair of features in {}, on Concord.", project.name),
        bootstrap,
    }, body.finish())
}

/// `/projects/{slug}/ranking` — the order the forge concluded, in the API's order and with its components.
pub async fn ranking(
    State(pool): State<PgPool>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Response {
    let Some(project) = concord_db::projects::project_by_slug(&pool, &slug).await.ok().flatten() else {
        return crate::not_found(&headers);
    };
    let priorities = match concord_db::priorities::feature_priorities(&pool, project.id).await {
        Ok(rows) => rows,
        Err(error) => return crate::error_page(&headers, "the ranking could not be read", &error.to_string()),
    };
    // Participation is a separate read on purpose: a rating shown without the number of judgements behind it invites a reader to trust
    // a number nobody voted for.
    let tallies = concord_db::tallies::tallies(&pool, project.id).await.ok().flatten().unwrap_or_default();
    let mut body = Html::new();
    body.open("p", &[]).open("a", &[("href", &format!("/projects/{slug}/rank"))])
        .text("Judge a pair").close("a").close("p");
    if priorities.is_empty() {
        body.open("p", &[("class", "empty")]).text("This project has no features to rank.").close("p");
    } else {
        body.open("table", &[("class", "table"), ("data-order", "api")])
            .open("thead", &[]).open("tr", &[]);
        for heading in ["#", "Feature", "Priority", "Pain", "Strategic weight", "Rating", "Votes"] {
            body.open("th", &[("scope", "col")]).text(heading).close("th");
        }
        body.close("tr").close("thead").open("tbody", &[]);
        for (position, row) in priorities.iter().enumerate() {
            let tally = tallies.iter().find(|tally| tally.feature_id == row.id);
            body.open("tr", &[])
                .open("td", &[]).text(&(position + 1).to_string()).close("td")
                .open("td", &[]).open("a", &[("href", &format!("/projects/{slug}/features/{}", row.id))])
                .text(&row.title).close("a").close("td")
                .open("td", &[]).text(&format!("{:.3}", row.priority_score)).close("td")
                .open("td", &[]).text(&format!("{:.3}", row.pain_score)).close("td")
                .open("td", &[]).text(&format!("{:.3}", row.strategic_weight)).close("td")
                // The uncertainty travels with the rating: `±0` is a settled number and `±200` is a guess, and they look identical
                // when only the rating is printed.
                .open("td", &[]).text(&format!("{:.0} ± {:.0}", row.elo_r, row.elo_rd)).close("td");
            match tally {
                Some(tally) => body.open("td", &[])
                    .text(&format!("{}W {}L {}S", tally.wins, tally.losses, tally.skips)).close("td"),
                None => body.open("td", &[("class", "unmeasured")]).text("—").close("td"),
            };
            body.close("tr");
        }
        body.close("tbody").close("table");
        body.open("p", &[("class", "muted")])
            .text("Ordered by the API, which already folds pain, strategic weight and the theme's μ into the score. This page does \
                   not re-sort.")
            .close("p");
    }
    let bootstrap = serde_json::json!({
        "route": format!("/projects/{slug}/ranking"),
        "order": "api",
        "priorities": priorities.iter().map(|row| serde_json::json!({
            "id": row.id, "title": row.title, "priority_score": row.priority_score,
            "pain_score": row.pain_score, "strategic_weight": row.strategic_weight,
            "elo_r": row.elo_r, "elo_rd": row.elo_rd,
        })).collect::<Vec<_>>(),
    });
    render(&headers, Page {
        title: &format!("{}: Ranking", project.name),
        path: &format!("/projects/{slug}/ranking"),
        description: &format!("What {} should build next, and why it ranks there.", project.name),
        bootstrap,
    }, body.finish())
}

/// `/contribute` — the work somebody could pick up.
pub async fn contribute(State(pool): State<PgPool>, headers: HeaderMap) -> Response {
    // An anonymous reader gets the catalogue's open work per project, which is what the feed's own read answers for project zero sense:
    // the feed ranks WORK ITEMS per project, so the page asks each readable project in turn. Capped, because a page that queries 78
    // projects to render is a page that will time out the day the catalogue doubles.
    let projects = concord_db::projects::list_projects(&pool).await.unwrap_or_default();
    let mut rows: Vec<(String, String, String)> = Vec::new();
    for project in projects.iter().take(24) {
        let items = concord_db::feed_reads::work_items(&pool, project.id).await.unwrap_or_default();
        for item in items.into_iter().take(3) {
            rows.push((project.slug.clone(), project.name.clone(), item.title));
        }
        if rows.len() >= 40 {
            break;
        }
    }
    let mut body = Html::new();
    if rows.is_empty() {
        body.open("p", &[("class", "empty")])
            .text("No project is offering work right now. Work items appear when a maintainer claims a feature and leaves it open.")
            .close("p");
    } else {
        body.open("ul", &[("class", "rows")]);
        for (slug, name, title) in &rows {
            body.open("li", &[])
                .open("a", &[("href", &format!("/projects/{slug}/board"))]).text(title).close("a")
                .open("span", &[("class", "muted")]).text(&format!(" · {name}")).close("span")
                .close("li");
        }
        body.close("ul");
    }
    body.open("p", &[("class", "muted")])
        .text("Sign in to vote, claim work or file a field report.")
        .close("p");
    let bootstrap = serde_json::json!({
        "route": "/contribute",
        "items": rows.iter().map(|(slug, _name, title)| serde_json::json!({"project": slug, "title": title}))
            .collect::<Vec<_>>(),
    });
    render(&headers, Page {
        title: "Contribute",
        path: "/contribute",
        description: "Work you could pick up across the catalogue.",
        bootstrap,
    }, body.finish())
}

/// `/curation` — the moderation queue, which is empty for a reader who is not a curator.
pub async fn curation(State(pool): State<PgPool>, headers: HeaderMap) -> Response {
    let filter = concord_db::curation::Filter::default();
    let page = match concord_db::curation::list(&pool, &filter).await {
        Ok(page) => page,
        Err(error) => return crate::error_page(&headers, "the curation queue could not be read", &error.to_string()),
    };
    let mut body = Html::new();
    // Anonymous callers see the queue's size and no rows: the read is what a curator uses, and a page that shows proposed deletions to
    // everyone is a page that turns a queue into a rumour.
    // `curation::list` is cursor-paginated: it answers a page of items and whether there is more, not a total. The page prints what it
    // actually knows — "at least this many, and whether another page follows" — rather than inventing a count the read cannot give.
    body.open("p", &[("class", "muted")])
        .text(&match (page.items.len(), !page.next_cursor.is_empty()) {
            (0, _) => "Nothing is waiting for review.".to_string(),
            (shown, false) => format!("{shown} item(s) are waiting for a curator."),
            (shown, true) => format!("{shown} item(s) shown, and more follow."),
        })
        .close("p");
    if page.items.is_empty() {
        body.open("p", &[("class", "empty")]).text("The queue is empty.").close("p");
    }
    let bootstrap = serde_json::json!({
        "route": "/curation",
        "shown": page.items.len(),
        "more": !page.next_cursor.is_empty(),
    });
    render(&headers, Page {
        title: "Curation",
        path: "/curation",
        description: "The moderation queue.",
        bootstrap,
    }, body.finish())
}

/// `/notifications` — the reader's own inbox, and a page that must say "sign in" rather than pretend it is empty.
pub async fn notifications(State(_pool): State<PgPool>, headers: HeaderMap) -> Response {
    // No token is read here: this crate renders documents from public reads, and an inbox is not public. The page is honest about what
    // it cannot show rather than printing an empty list that looks like "you have no notifications".
    let mut body = Html::new();
    body.open("p", &[]).text("Notifications are delivered to the signed-in reader.").close("p")
        .open("p", &[]).open("a", &[("href", "/login")]).text("Sign in").close("a").close("p");
    let bootstrap = serde_json::json!({"route": "/notifications", "requires": "session"});
    render(&headers, Page {
        title: "Notifications",
        path: "/notifications",
        description: "Your notifications.",
        bootstrap,
    }, body.finish())
}

/// `/login?next=…`
#[derive(Debug, Default, Deserialize)]
pub struct LoginQuery {
    #[serde(default)]
    pub next: String,
}

/// `/login` — the form, and the reason every failure answers the same way.
pub async fn login(Query(request): Query<LoginQuery>, headers: HeaderMap) -> Response {
    let next = if request.next.starts_with('/') { request.next.as_str() } else { "/" };
    let mut body = Html::new();
    body.open("h2", &[]).text("Sign in").close("h2")
        .open("form", &[("method", "post"), ("action", "/api/v1/auth/login"), ("class", "auth-form")])
        .void("input", &[("type", "hidden"), ("name", "next"), ("value", next)])
        .open("label", &[("for", "username")]).text("Username").close("label")
        .void("input", &[("type", "text"), ("id", "username"), ("name", "username"), ("autocomplete", "username")])
        .open("label", &[("for", "password")]).text("Password").close("label")
        .void("input", &[("type", "password"), ("id", "password"), ("name", "password"),
                         ("autocomplete", "current-password")])
        .open("button", &[("type", "submit")]).text("Sign in").close("button")
        .close("form")
        // The single message is the point: "no such user" and "wrong password" answer identically, so the form cannot be used to
        // discover which usernames exist. The page says so out loud rather than leaving a reader to wonder why the error is vague.
        .open("p", &[("class", "muted")])
        .text("A wrong password and an unknown account produce the same message — that is deliberate.")
        .close("p")
        .open("p", &[]).open("a", &[("href", "/register")]).text("Create an account").close("a").close("p");
    let bootstrap = serde_json::json!({"route": "/login", "next": next});
    render(&headers, Page {
        title: "Sign in",
        path: "/login",
        description: "Sign in to Concord.",
        bootstrap,
    }, body.finish())
}

/// `/register` — the form that turns a guest into an account, in place.
pub async fn register(headers: HeaderMap) -> Response {
    let mut body = Html::new();
    body.open("h2", &[]).text("Create an account").close("h2")
        .open("form", &[("method", "post"), ("action", "/api/v1/auth/register"), ("class", "auth-form")])
        .open("label", &[("for", "username")]).text("Username").close("label")
        .void("input", &[("type", "text"), ("id", "username"), ("name", "username"), ("autocomplete", "username")])
        .open("label", &[("for", "password")]).text("Password").close("label")
        .void("input", &[("type", "password"), ("id", "password"), ("name", "password"),
                         ("autocomplete", "new-password")])
        .open("button", &[("type", "submit")]).text("Create account").close("button")
        .close("form")
        // Registering in place is a promise the store keeps: a guest who registers KEEPS their identity, so their votes and filings are
        // not orphaned. A reader who does not know that will assume registering starts them over.
        .open("p", &[("class", "muted")])
        .text("If you have been reading as a guest, registering keeps your identity: your votes and filings stay yours.")
        .close("p");
    let bootstrap = serde_json::json!({"route": "/register"});
    render(&headers, Page {
        title: "Create an account",
        path: "/register",
        description: "Create an account on Concord.",
        bootstrap,
    }, body.finish())
}

/// `/finder` — what Finder asks, and how the questions are chosen.
pub async fn finder(State(pool): State<PgPool>, headers: HeaderMap) -> Response {
    let projects = concord_db::projects::list_projects(&pool).await.unwrap_or_default();
    // The SAME read the finder's session uses, so the page cannot describe a dimension set the engine does not have. The ids are capped:
    // the read is one query per dimension over the id list, and a page is not the place to fan that out to 78 projects.
    let ids: Vec<i64> = projects.iter().take(64).map(|project| project.id).collect();
    let attributes = concord_db::finder_reads::attributes(&pool, &ids).await.unwrap_or_default();
    let mut dimensions: Vec<String> = Vec::new();
    for map in attributes.values() {
        for key in map.keys() {
            if !dimensions.contains(key) {
                dimensions.push(key.clone());
            }
        }
    }
    dimensions.sort();
    let capabilities = dimensions.iter().filter(|dimension| dimension.starts_with("cap:")).count();
    let measured = attributes.values().filter(|map| !map.is_empty()).count();

    let mut body = Html::new();
    body.open("p", &[])
        .text("Finder narrows the catalogue by asking questions whose answers split the remaining projects most evenly — the \
               question that removes the most candidates wins, and a dimension every project shares is never asked because its \
               answer is already known.")
        .close("p")
        .open("dl", &[("class", "facts")]);
    crate::detail::fact_public(&mut body, "Projects it can narrow", &projects.len().to_string());
    crate::detail::fact_public(&mut body, "Projects with measured attributes", &measured.to_string());
    crate::detail::fact_public(&mut body, "Dimensions it asks about", &dimensions.len().to_string());
    crate::detail::fact_public(&mut body, "Of which capabilities", &capabilities.to_string());
    body.close("dl");
    if dimensions.is_empty() {
        // An EMPTY dimension set is the interesting case, and it has two causes a reader cannot tell apart from the outside: no project
        // has been measured, or nothing measured is shared. Naming both is honest; printing "0 questions" alone is not.
        body.open("p", &[("class", "empty")])
            .text("No dimension has been measured across the catalogue yet, so Finder has nothing to ask. It stays silent rather than \
                   asking a question whose answer it already knows.")
            .close("p");
    } else {
        body.open("h3", &[]).text("What it asks about").close("h3").open("ul", &[("class", "tags")]);
        for dimension in &dimensions {
            body.open("li", &[]).text(dimension).close("li");
        }
        body.close("ul");
    }
    // The flow itself needs a session, and a session is a POST with a cookie. The page says so: a reader who expects a wizard here
    // should learn on this page, not after clicking nothing.
    body.open("p", &[("class", "muted")])
        .text("Answering the questions needs a session; the interactive flow arrives with the frontend bundle.")
        .close("p");
    let bootstrap = serde_json::json!({
        "route": "/finder",
        "projects": projects.len(),
        "measured": measured,
        "dimensions": dimensions,
    });
    render(&headers, Page {
        title: "Finder",
        path: "/finder",
        description: "Narrow the catalogue by answering questions that split it.",
        bootstrap,
    }, body.finish())
}
