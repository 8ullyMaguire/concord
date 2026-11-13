//! §12's syndication: thirteen `.xml` routes, two serialisations, five documents.
//!
//! A feed is the one part of this product that is **copied and kept by somebody else**. That changes what has to
//! be true here:
//!
//! * **a tally never appears.** The consensus page shows counts; a feed item carries the call's STATE and
//!   nothing else. §6.6's hidden tally is a rule about who may see a number, and a third-party copy is the one
//!   place it cannot be taken back — so `TallyConsensus` is not called from this module at all.
//! * **the visible list is the public list.** The projects feed calls `list_projects` (every project) and then
//!   keeps only `public` ones, because a feed is fetched by anonymous aggregators and a member's unlisted
//!   project must not leak into a document indexed by the whole internet.
//! * **an empty feed is STABLE.** `<updated>` is the newest item, never the server's clock: a feed whose
//!   timestamp changes on every fetch makes every reader re-download everything, forever.
//! * **two date formats, on purpose.** RSS takes RFC 822 and Atom takes RFC 3339. A single format in both is a
//!   common and reader-visible bug: an RSS reader that cannot parse RFC 3339 silently drops the item rather
//!   than showing it undated.
//! * **markup is stripped, and length is capped.** A complaint body is markdown; raw markdown in a summary is
//!   noise in every reader. Cut on a rune boundary — a byte slice through UTF-8 produces an invalid document.
//!
//! The base URL comes from `CONCORD_BASE_URL`. Unset, every feed answers 503 with the instruction to set it:
//! an absolute URL is what makes an item's `id` stable, and a relative one would be resolved by each reader
//! against its own guess.

use axum::{
    extract::{OriginalUri, Path, Query, State},
    http::{header, StatusCode},
    response::{IntoResponse, Response},
};
use polaris_core::ApiError;
use serde::Deserialize;

use crate::{internal, AppState};

/// How many items one document carries. A feed with ten thousand items is a scrape target, not a reader.
const ITEM_LIMIT: usize = 50;

/// One syndication document, independent of its serialisation.
pub struct Feed {
    /// The document's title.
    pub title: String,
    /// Absolute URL of this document.
    pub self_url: String,
    /// Absolute URL of the page a human should read instead.
    pub alt_url: String,
    /// One line of prose.
    pub description: String,
    /// The newest item's time; see [`newest`].
    pub updated: Option<f64>,
    /// The entries, newest first as the store returned them.
    pub items: Vec<FeedItem>,
}

/// One entry.
pub struct FeedItem {
    /// Absolute, stable, never reused.
    pub id: String,
    /// The title.
    pub title: String,
    /// Absolute link.
    pub link: String,
    /// Unix seconds, or zero for "no date", which renders as an empty date rather than as 1970.
    pub updated: f64,
    /// The summary, already stripped to text.
    pub summary: String,
    /// A display name, never an id.
    pub author: String,
    /// Category terms.
    pub categories: Vec<String>,
}

/// The newest item time, or `None` for a feed with no items.
///
/// `None` is what makes an empty feed stable: rendering the clock instead would report a change on every fetch
/// of a document that has not changed.
fn newest(items: &[FeedItem]) -> Option<f64> {
    items
        .iter()
        .map(|item| item.updated)
        .filter(|at| *at > 0.0)
        .fold(None, |best: Option<f64>, at| match best {
            Some(current) if current >= at => Some(current),
            _ => Some(at),
        })
}

/// RFC 822, RSS's date format (`Mon, 02 Jan 2006 15:04:05 -0700`). Zero renders as an empty string.
fn rfc822(sec: f64) -> String {
    if sec <= 0.0 {
        return String::new();
    }
    let when = chrono::DateTime::from_timestamp(sec as i64, 0)
        .unwrap_or_else(|| chrono::DateTime::from_timestamp(0, 0).expect("the epoch is a valid instant"));
    when.format("%a, %d %b %Y %H:%M:%S +0000").to_string()
}

/// RFC 3339, Atom's date format.
fn rfc3339(sec: f64) -> String {
    if sec <= 0.0 {
        return String::new();
    }
    let when = chrono::DateTime::from_timestamp(sec as i64, 0)
        .unwrap_or_else(|| chrono::DateTime::from_timestamp(0, 0).expect("the epoch is a valid instant"));
    when.format("%Y-%m-%dT%H:%M:%SZ").to_string()
}

/// Reduce user-supplied markup to something a reader renders as prose, and cap the length.
///
/// Escaping is not done here: every value goes through [`escape`] on the way into the document, so a title
/// containing `<script>` cannot break out. This is only the readability half.
pub fn plain_text(text: &str, max: usize) -> String {
    let mut stripped = text.replace("```", " ");
    for token in ["**", "__", "*", "_", "`", "#"] {
        stripped = stripped.replace(token, "");
    }
    let collapsed = stripped.split_whitespace().collect::<Vec<_>>().join(" ");
    let runes: Vec<char> = collapsed.chars().collect();
    if runes.len() > max {
        let mut cut: String = runes[..max].iter().collect();
        cut.push('\u{2026}');
        return cut;
    }
    collapsed
}

/// XML text escaping, including the two characters that make a document unparseable rather than merely wrong.
fn escape(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    for ch in text.chars() {
        match ch {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&quot;"),
            '\'' => out.push_str("&apos;"),
            // A control character is not representable in XML 1.0 at all: carrying one through produces a
            // document that every parser rejects. Dropped, because a feed that will not parse is worse than a
            // feed missing a byte of somebody's title.
            control if (control as u32) < 0x20 && control != '\n' && control != '\t' => {}
            other => out.push(other),
        }
    }
    out
}

/// The RSS 2.0 document.
pub fn rss(feed: &Feed) -> String {
    let mut doc = String::from("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<rss version=\"2.0\" \
        xmlns:atom=\"http://www.w3.org/2005/Atom\">\n<channel>\n");
    doc.push_str(&format!("<title>{}</title>\n", escape(&feed.title)));
    doc.push_str(&format!("<link>{}</link>\n", escape(&feed.alt_url)));
    doc.push_str(&format!(
        "<description>{}</description>\n",
        escape(&feed.description)
    ));
    doc.push_str(&format!(
        "<pubDate>{}</pubDate>\n",
        rfc822(feed.updated.unwrap_or_default())
    ));
    doc.push_str(&format!(
        "<atom:link rel=\"self\" href=\"{}\" title=\"{}\"/>\n",
        escape(&feed.self_url),
        escape(&feed.title)
    ));
    for item in &feed.items {
        doc.push_str("<item>\n");
        doc.push_str(&format!("<title>{}</title>\n", escape(&item.title)));
        doc.push_str(&format!("<link>{}</link>\n", escape(&item.link)));
        // `isPermaLink="false"` because the link is absolute and already the identity: letting a reader treat
        // the guid as a fetchable permalink invites a duplicate request for the same page.
        doc.push_str(&format!(
            "<guid isPermaLink=\"false\">{}</guid>\n",
            escape(&item.id)
        ));
        doc.push_str(&format!("<pubDate>{}</pubDate>\n", rfc822(item.updated)));
        doc.push_str(&format!(
            "<description>{}</description>\n",
            escape(&item.summary)
        ));
        if !item.author.is_empty() {
            doc.push_str(&format!("<author>{}</author>\n", escape(&item.author)));
        }
        for category in &item.categories {
            doc.push_str(&format!("<category>{}</category>\n", escape(category)));
        }
        doc.push_str("</item>\n");
    }
    doc.push_str("</channel>\n</rss>\n");
    doc
}

/// The Atom 1.0 document.
pub fn atom(feed: &Feed) -> String {
    let mut doc = String::from("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n\
        <feed xmlns=\"http://www.w3.org/2005/Atom\">\n");
    doc.push_str(&format!("<title>{}</title>\n", escape(&feed.title)));
    doc.push_str(&format!("<id>{}</id>\n", escape(&feed.self_url)));
    doc.push_str(&format!(
        "<updated>{}</updated>\n",
        rfc3339(feed.updated.unwrap_or_default())
    ));
    if !feed.description.is_empty() {
        doc.push_str(&format!(
            "<subtitle>{}</subtitle>\n",
            escape(&feed.description)
        ));
    }
    doc.push_str(&format!(
        "<link rel=\"self\" href=\"{}\" type=\"application/atom+xml; charset=utf-8\"/>\n",
        escape(&feed.self_url)
    ));
    doc.push_str(&format!(
        "<link rel=\"alternate\" href=\"{}\" type=\"text/html\"/>\n",
        escape(&feed.alt_url)
    ));
    for item in &feed.items {
        doc.push_str("<entry>\n");
        doc.push_str(&format!("<title>{}</title>\n", escape(&item.title)));
        doc.push_str(&format!("<id>{}</id>\n", escape(&item.id)));
        doc.push_str(&format!("<updated>{}</updated>\n", rfc3339(item.updated)));
        doc.push_str(&format!(
            "<link rel=\"alternate\" href=\"{}\" type=\"text/html\"/>\n",
            escape(&item.link)
        ));
        doc.push_str(&format!("<summary>{}</summary>\n", escape(&item.summary)));
        // An `<author>` with an empty `<name>` is worse than no author element: readers render the name they
        // find, and "" looks like a bug.
        if !item.author.is_empty() {
            doc.push_str(&format!(
                "<author><name>{}</name></author>\n",
                escape(&item.author)
            ));
        }
        for category in &item.categories {
            // Atom's category carries a term ATTRIBUTE; a text node is not the same element.
            doc.push_str(&format!("<category term=\"{}\"/>\n", escape(category)));
        }
        doc.push_str("</entry>\n");
    }
    doc.push_str("</feed>\n");
    doc
}

/// The serialisation for this request.
///
/// An explicit `?format=` wins over the path suffix. The other order is wrong and a Go test caught it:
/// `/feed.xml?format=atom` answered RSS because the suffix was consulted first. The suffix is a default, the
/// parameter is a choice, and silently ignoring a choice is worse than ignoring a default.
///
/// An unknown format is refused rather than defaulted: a reader that asked for `?format=pdf` and was handed RSS
/// has no way to tell what happened.
fn resolve_format(path: &str, format: Option<&str>) -> Result<&'static str, ApiError> {
    if let Some(requested) = format.filter(|value| !value.is_empty()) {
        return match requested {
            "rss" => Ok("rss"),
            "atom" => Ok("atom"),
            _ => Err(ApiError::invalid_request("unsupported format")),
        };
    }
    if path.ends_with("/atom.xml") {
        return Ok("atom");
    }
    Ok("rss")
}

/// The site's public origin, or the 503 that tells the operator to set it.
fn base_url() -> Result<String, Response> {
    let base = std::env::var("CONCORD_BASE_URL").unwrap_or_default();
    let trimmed = base.trim().trim_end_matches('/').to_string();
    if trimmed.is_empty() {
        return Err((
            StatusCode::SERVICE_UNAVAILABLE,
            [(header::CONTENT_TYPE, "text/plain; charset=utf-8")],
            "Feeds are disabled. Set CONCORD_BASE_URL to this site's public origin, \
             for example https://concord.example.org\n",
        )
            .into_response());
    }
    Ok(trimmed)
}

/// Serialise and answer.
fn write(feed: Feed, format: &str) -> Response {
    let body = match format {
        "atom" => atom(&feed),
        _ => rss(&feed),
    };
    let content_type = match format {
        "atom" => "application/atom+xml; charset=utf-8",
        _ => "application/rss+xml; charset=utf-8",
    };
    ([(header::CONTENT_TYPE, content_type)], body).into_response()
}

/// The query parameters feeds read.
#[derive(Debug, Deserialize, Default)]
pub struct FeedParams {
    /// `rss` or `atom`; wins over the path suffix.
    #[serde(default)]
    format: Option<String>,
    /// `stats` on the projects feed selects the numbers instead of the projects.
    #[serde(default)]
    kind: Option<String>,
}

/// A project a feed may publish, or the 404 that makes a private one indistinguishable from a missing one.
async fn readable_project(
    state: &AppState,
    slug: &str,
) -> Result<concord_db::projects::Project, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("not found"))?;
    if project.visibility == "public" {
        return Ok(project);
    }
    // Anonymous readers only: a feed is fetched by aggregators, which carry no session. The 404 body never
    // names the slug, so an unlisted project and a nonexistent one answer identically.
    Err(ApiError::not_found("not found"))
}

/// `GET /feed.xml`, `/rss.xml`, `/atom.xml` — every public project, or (`?kind=stats`) the numbers.
pub async fn projects_feed(
    State(state): State<AppState>,
    Query(params): Query<FeedParams>,
    uri: OriginalUri,
) -> Response {
    let format = match resolve_format(uri.path(), params.format.as_deref()) {
        Ok(format) => format,
        Err(error) => return error.into_response(),
    };
    let base = match base_url() {
        Ok(base) => base,
        Err(response) => return response,
    };
    if params.kind.as_deref() == Some("stats") {
        return stats_feed(&state, format, &base).await;
    }
    let mut projects = match concord_db::projects::list_projects(&state.pool).await {
        Ok(projects) => projects,
        Err(error) => return internal(error).into_response(),
    };
    projects.retain(|project| project.visibility == "public");
    projects.truncate(ITEM_LIMIT);
    let mut items = Vec::with_capacity(projects.len());
    for project in &projects {
        let url = format!("{base}/projects/{}", project.slug);
        items.push(FeedItem {
            id: url.clone(),
            title: project.name.clone(),
            link: url,
            updated: project.updated_at,
            summary: plain_text(&project.description, 400),
            author: String::new(),
            categories: Vec::new(),
        });
    }
    let feed = Feed {
        title: "Concord projects".to_string(),
        self_url: format!("{base}/feed.xml"),
        alt_url: format!("{base}/projects"),
        description: "Public projects on this Concord instance.".to_string(),
        updated: newest(&items),
        items,
    };
    write(feed, format)
}

/// `GET /feed.xml?kind=stats` — the numbers, as one item.
///
/// Built from the same payload `GET /api/v1/stats` answers, so a subscriber and a page reader cannot see
/// different figures. The summary is prose, and each median is quoted with its `n`: a median without one is not
/// a number.
async fn stats_feed(state: &AppState, format: &str, base: &str) -> Response {
    let payload = match concord_db::stats::payload(&state.pool).await {
        Ok(payload) => payload,
        Err(error) => return internal(error).into_response(),
    };
    let latency = |name: &str, value: &concord_db::stats::Latency| {
        if value.n == 0 {
            format!("{name} not yet measured")
        } else {
            format!(
                "{name} {:.1}h (n={}, unfinished={})",
                value.median_hours, value.n, value.unfinished
            )
        }
    };
    let summary = format!(
        "{} projects; {} features with a recorded origin; {}; {}; {}.",
        payload.measured.projects,
        payload.measured.with_origin,
        latency("triage", &payload.latency.triage),
        latency("delivery", &payload.latency.delivery),
        latency("decision", &payload.latency.decision),
    );
    let now = payload.generated_at;
    let url = format!("{base}/stats");
    let items = vec![FeedItem {
        id: url.clone(),
        title: format!("Instance statistics, {}", rfc3339(now).split('T').next().unwrap_or("")),
        link: url,
        updated: now,
        summary,
        author: String::new(),
        categories: Vec::new(),
    }];
    let feed = Feed {
        title: "Concord instance statistics".to_string(),
        self_url: format!("{base}/feed.xml?kind=stats"),
        alt_url: url_from(base, "/stats"),
        description: "The instance census and its response times.".to_string(),
        updated: newest(&items),
        items,
    };
    write(feed, format)
}

fn url_from(base: &str, path: &str) -> String {
    format!("{base}{path}")
}

/// `GET /projects/{slug}/feed.xml` — the project's complaints.
pub async fn project_feed(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Query(params): Query<FeedParams>,
    uri: OriginalUri,
) -> Response {
    // The PATH, not an empty string: `/atom.xml` is an alias for `?format=atom`, and reading the suffix from
    // nothing made every project-level `.atom.xml` answer RSS. Measured before the fix:
    //   FAIL /projects/…/atom.xml is atom: got 'rss' want 'atom'
    let format = match resolve_format(uri.path(), params.format.as_deref()) {
        Ok(format) => format,
        Err(error) => return error.into_response(),
    };
    let base = match base_url() {
        Ok(base) => base,
        Err(response) => return response,
    };
    let project = match readable_project(&state, &slug).await {
        Ok(project) => project,
        Err(error) => return error.into_response(),
    };
    let complaints = match concord_db::complaints::list_complaints(&state.pool, project.id, "").await {
        Ok(complaints) => complaints,
        Err(error) => return internal(error).into_response(),
    };
    let authors: Vec<i64> = complaints.iter().map(|c| c.author_id).collect();
    let names = match concord_db::auth::display_names(&state.pool, &authors).await {
        Ok(names) => names,
        Err(error) => return internal(error).into_response(),
    };
    let mut items = Vec::with_capacity(complaints.len().min(ITEM_LIMIT));
    for complaint in complaints.iter().take(ITEM_LIMIT) {
        let url = format!("{base}/projects/{slug}/complaints/{}", complaint.id);
        items.push(FeedItem {
            id: url.clone(),
            title: complaint.title.clone(),
            link: url,
            updated: complaint.updated_at,
            summary: plain_text(&complaint.body, 400),
            author: names.get(&complaint.author_id).cloned().unwrap_or_default(),
            categories: vec![complaint.status.clone()],
        });
    }
    let feed = Feed {
        title: format!("{slug}: complaints"),
        self_url: format!("{base}/projects/{slug}/feed.xml"),
        alt_url: format!("{base}/projects/{slug}"),
        description: format!("Complaints filed against {slug}."),
        updated: newest(&items),
        items,
    };
    write(feed, format)
}

/// `GET /projects/{slug}/board/feed.xml` — cards, most recently moved first.
pub async fn board_feed(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Query(params): Query<FeedParams>,
    uri: OriginalUri,
) -> Response {
    // The PATH, not an empty string: `/atom.xml` is an alias for `?format=atom`, and reading the suffix from
    // nothing made every project-level `.atom.xml` answer RSS. Measured before the fix:
    //   FAIL /projects/…/atom.xml is atom: got 'rss' want 'atom'
    let format = match resolve_format(uri.path(), params.format.as_deref()) {
        Ok(format) => format,
        Err(error) => return error.into_response(),
    };
    let base = match base_url() {
        Ok(base) => base,
        Err(response) => return response,
    };
    let project = match readable_project(&state, &slug).await {
        Ok(project) => project,
        Err(error) => return error.into_response(),
    };
    // The same call the board page makes, so the feed and the page cannot disagree about what is on the board.
    let board = match concord_db::projects::board_by_slug(&state.pool, &slug).await {
        Ok(Some(board)) => board,
        Ok(None) => return ApiError::not_found("not found").into_response(),
        Err(error) => return internal(error).into_response(),
    };
    let _ = project;
    // `board.cards` carries the column NAME beside each card, which is why the feed reads the flat list
    // rather than the columns: the same card must not be published twice from two shapes of one answer.
    let mut items = Vec::with_capacity(board.cards.len().min(ITEM_LIMIT));
    for card in &board.cards {
        items.push(FeedItem {
            id: format!("{base}/projects/{slug}/board#{}", card.feature_id),
            title: card.title.clone().unwrap_or_else(|| card.column.clone()),
            link: format!("{base}/projects/{slug}/board"),
            updated: card.entered_at,
            summary: String::new(),
            author: String::new(),
            categories: vec![card.column.clone()],
        });
    }
    items.sort_by(|left, right| {
        right
            .updated
            .partial_cmp(&left.updated)
            .unwrap_or(std::cmp::Ordering::Equal)
    });
    items.truncate(ITEM_LIMIT);
    let feed = Feed {
        title: format!("{slug}: board"),
        self_url: format!("{base}/projects/{slug}/board/feed.xml"),
        alt_url: format!("{base}/projects/{slug}/board"),
        description: format!("Board cards on {slug}, most recently moved first."),
        updated: newest(&items),
        items,
    };
    write(feed, format)
}

/// `GET /projects/{slug}/milestones/feed.xml` — opens and closes.
pub async fn milestones_feed(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Query(params): Query<FeedParams>,
    uri: OriginalUri,
) -> Response {
    // The PATH, not an empty string: `/atom.xml` is an alias for `?format=atom`, and reading the suffix from
    // nothing made every project-level `.atom.xml` answer RSS. Measured before the fix:
    //   FAIL /projects/…/atom.xml is atom: got 'rss' want 'atom'
    let format = match resolve_format(uri.path(), params.format.as_deref()) {
        Ok(format) => format,
        Err(error) => return error.into_response(),
    };
    let base = match base_url() {
        Ok(base) => base,
        Err(response) => return response,
    };
    let project = match readable_project(&state, &slug).await {
        Ok(project) => project,
        Err(error) => return error.into_response(),
    };
    let events = match concord_db::milestones::events(&state.pool, project.id).await {
        Ok(events) => events,
        Err(error) => return internal(error).into_response(),
    };
    let mut items = Vec::with_capacity(events.len().min(ITEM_LIMIT));
    for event in events.iter().take(ITEM_LIMIT) {
        // A milestone whose status never changed is reported as opened: the alternative is a feed item
        // claiming a change that the audit log does not contain.
        let (when, title) = if event.status_changed_at > 0.0 {
            (
                event.status_changed_at,
                format!("{} is {}", event.title, event.status),
            )
        } else {
            (event.created_at, format!("{} opened", event.title))
        };
        let url = format!("{base}/projects/{slug}/milestones/{}", event.milestone_id);
        items.push(FeedItem {
            id: url.clone(),
            title,
            link: url,
            updated: when,
            summary: String::new(),
            author: String::new(),
            categories: vec![event.status.clone()],
        });
    }
    let feed = Feed {
        title: format!("{slug}: milestones"),
        self_url: format!("{base}/projects/{slug}/milestones/feed.xml"),
        alt_url: format!("{base}/projects/{slug}/roadmap"),
        description: format!(
            "Milestones on {slug}: opened, moved and closed, most recently changed first."
        ),
        updated: newest(&items),
        items,
    };
    write(feed, format)
}

/// `GET /projects/{slug}/consensus/feed.xml` — calls, with their state and never with a tally.
pub async fn consensus_feed(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Query(params): Query<FeedParams>,
    uri: OriginalUri,
) -> Response {
    // The PATH, not an empty string: `/atom.xml` is an alias for `?format=atom`, and reading the suffix from
    // nothing made every project-level `.atom.xml` answer RSS. Measured before the fix:
    //   FAIL /projects/…/atom.xml is atom: got 'rss' want 'atom'
    let format = match resolve_format(uri.path(), params.format.as_deref()) {
        Ok(format) => format,
        Err(error) => return error.into_response(),
    };
    let base = match base_url() {
        Ok(base) => base,
        Err(response) => return response,
    };
    let project = match readable_project(&state, &slug).await {
        Ok(project) => project,
        Err(error) => return error.into_response(),
    };
    let calls =
        match concord_db::consensus::list_calls(&state.pool, project.id, ITEM_LIMIT as i64).await {
            Ok(calls) => calls,
            Err(error) => return internal(error).into_response(),
        };
    let authors: Vec<i64> = calls.iter().map(|call| call.opened_by).collect();
    let names = match concord_db::auth::display_names(&state.pool, &authors).await {
        Ok(names) => names,
        Err(error) => return internal(error).into_response(),
    };
    let mut items = Vec::with_capacity(calls.len());
    for call in &calls {
        let url = format!("{base}/projects/{slug}/consensus?call={}", call.id);
        items.push(FeedItem {
            id: url.clone(),
            title: call
                .question
                .clone()
                .filter(|question| !question.is_empty())
                .unwrap_or_else(|| "Consensus call".to_string()),
            link: url,
            updated: call.opens_at,
            summary: call
                .description
                .as_deref()
                .map(|text| plain_text(text, 400))
                .unwrap_or_default(),
            author: names.get(&call.opened_by).cloned().unwrap_or_default(),
            categories: vec![call.status.clone()],
        });
    }
    let feed = Feed {
        title: format!("{slug}: consensus calls"),
        self_url: format!("{base}/projects/{slug}/consensus/feed.xml"),
        alt_url: format!("{base}/projects/{slug}/consensus"),
        description: format!("Consensus calls opened on {slug}."),
        updated: newest(&items),
        items,
    };
    write(feed, format)
}

/// `GET /projects/{slug}/documents/feed.xml` — every document kind.
///
/// `project_documents.kind` is one of `readme`/`spec`/`plan`/`wiki`/`adr`/`changelog`/`scout` and the table has
/// no draft/published column at all, so a document is published the moment it exists. Filtering on a publish
/// flag would have refused every reader with a 400 for a rule the schema does not have.
pub async fn documents_feed(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Query(params): Query<FeedParams>,
    uri: OriginalUri,
) -> Response {
    // The PATH, not an empty string: `/atom.xml` is an alias for `?format=atom`, and reading the suffix from
    // nothing made every project-level `.atom.xml` answer RSS. Measured before the fix:
    //   FAIL /projects/…/atom.xml is atom: got 'rss' want 'atom'
    let format = match resolve_format(uri.path(), params.format.as_deref()) {
        Ok(format) => format,
        Err(error) => return error.into_response(),
    };
    let base = match base_url() {
        Ok(base) => base,
        Err(response) => return response,
    };
    let project = match readable_project(&state, &slug).await {
        Ok(project) => project,
        Err(error) => return error.into_response(),
    };
    let documents = match concord_db::projects::list_documents(&state.pool, project.id).await {
        Ok(documents) => documents,
        Err(error) => return internal(error).into_response(),
    };
    let authors: Vec<i64> = documents.iter().filter_map(|doc| doc.author_id).collect();
    let names = match concord_db::auth::display_names(&state.pool, &authors).await {
        Ok(names) => names,
        Err(error) => return internal(error).into_response(),
    };
    let mut items = Vec::with_capacity(documents.len().min(ITEM_LIMIT));
    for document in documents.iter().take(ITEM_LIMIT) {
        let url = format!("{base}/projects/{slug}/documents/{}", document.slug);
        items.push(FeedItem {
            id: url.clone(),
            title: document.title.clone(),
            link: url,
            updated: document.updated_at,
            summary: plain_text(&document.body, 400),
            author: document
                .author_id
                .and_then(|id| names.get(&id).cloned())
                .unwrap_or_default(),
            categories: Vec::new(),
        });
    }
    let feed = Feed {
        title: format!("{slug}: documents"),
        self_url: format!("{base}/projects/{slug}/documents/feed.xml"),
        alt_url: format!("{base}/projects/{slug}/documents"),
        description: format!("Published documents on {slug}."),
        updated: newest(&items),
        items,
    };
    write(feed, format)
}
