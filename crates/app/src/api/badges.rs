//! Shields for READMEs (step 3, ADR 0014): three numbers rendered as SVG.
//!
//! The whole §3.4 badge section — handler, SVG builder and escape helper — moved
//! verbatim from `lib.rs`. `app_mutant` stays in the crate root and is borrowed.

use axum::{
    extract::{Path, State},
    http::StatusCode,
    Router,
};
use polaris_core::ApiError;

use crate::{app_mutant, internal, readable, AppState};

// ── §3.4's badges ──────────────────────────────────────────────────────────────────────────────────────────────

/// `GET /badge/{slug}.svg` and `GET /badge/instance.svg` — three numbers as an SVG, sized to its own text.
///
/// `instance` is a **reserved slug, handled before the project lookup**: a project actually named `instance` would otherwise shadow its
/// own badge, and the instance badge would silently display one project's numbers to every reader but that project's.
///
/// **An unmeasured value renders `—`, never `0.0`.** A zero is a claim that the instance measured nothing *and found it
/// instantaneous*, and the claim lands in a README where nobody can check it. Same for latency with n=0: nought observations is not
/// a median of zero.
///
/// **The 404 echoes nothing.** A badge is public and unauthenticated; a body carrying the slug would confirm that an unlisted project
/// exists, and the reader of a broken image tag cannot tell the two apart anyway.
async fn badge_svg(
    State(state): State<AppState>,
    Path(file): Path<String>,
) -> Result<axum::response::Response, ApiError> {
    let Some(slug) = file.strip_suffix(".svg") else {
        return Err(ApiError::not_found("not found"));
    };
    // Control: the body carries the slug, so a 404 confirms that an unlisted project exists.
    let missing = || {
        if app_mutant("badge-slug-in-404") {
            ApiError::not_found(format!("not found: project {slug:?}"))
        } else {
            ApiError::not_found("not found")
        }
    };
    // Characters a slug cannot contain are refused before any query: the segment is interpolated into an SVG text node below, and a
    // lookup that fails is a cheaper answer for every case but the one where it succeeds.
    if slug.is_empty() || !slug.chars().all(|ch| ch.is_ascii_alphanumeric() || ch == '-' || ch == '_') {
        return Err(missing());
    }
    let segments: Vec<(String, String)> = if slug == "instance" {
        let measured = concord_db::stats::measured(&state.pool).await.map_err(internal)?;
        let latency = concord_db::stats::delivery_latency(&state.pool)
            .await
            .map_err(internal)?;
        let since = now_seconds() - 30.0 * 86_400.0;
        let shipped = concord_db::stats::shipped_since(&state.pool, None, since)
            .await
            .map_err(internal)?;
        vec![
            ("health".to_string(), format!("{}/{}", measured.with_health, measured.projects)),
            ("latency".to_string(), latency_value(&latency)),
            ("shipped-30d".to_string(), shipped.to_string()),
        ]
    } else {
        let project = concord_db::projects::project_by_slug(&state.pool, slug)
            .await
            .map_err(internal)?
            .ok_or_else(missing)?;
        // No session: the route is unauthenticated by design, so a private project is simply not readable here.
        if !readable(&state, &project, None).await? {
            return Err(missing());
        }
        let health = concord_db::ops_reads::project_health(&state.pool, project.id)
            .await
            .map_err(internal)?;
        let latency = concord_db::stats::delivery_latency_for_project(&state.pool, project.id)
            .await
            .map_err(internal)?;
        let since = now_seconds() - 30.0 * 86_400.0;
        let shipped = concord_db::stats::shipped_since(&state.pool, Some(project.id), since)
            .await
            .map_err(internal)?;
        vec![
            (
                "health".to_string(),
                health.map_or_else(
                    || {
                        // Control: an unmeasured project publishes `0.00`, a claim that the instance measured nothing and found it
                        // instantaneous — in a README where nobody can check it.
                        if app_mutant("badge-zero-for-unmeasured") {
                            "0.00".to_string()
                        } else {
                            "—".to_string()
                        }
                    },
                    |value| format!("{value:.2}"),
                ),
            ),
            ("latency".to_string(), latency_value(&latency)),
            ("shipped-30d".to_string(), shipped.to_string()),
        ]
    };

    let body = badge_svg_text(&segments);
    let mut response = axum::response::Response::new(axum::body::Body::from(body));
    *response.status_mut() = StatusCode::OK;
    let headers = response.headers_mut();
    headers.insert(
        axum::http::header::CONTENT_TYPE,
        axum::http::HeaderValue::from_static("image/svg+xml"),
    );
    // An hour: the numbers move on the scale of days, and a README renders on every page view of someone else's site.
    headers.insert(
        axum::http::header::CACHE_CONTROL,
        axum::http::HeaderValue::from_static("max-age=3600"),
    );
    Ok(response)
}

/// The median for a badge, or an em dash when nothing has been measured.
fn latency_value(latency: &concord_db::stats::Latency) -> String {
    if latency.n == 0 {
        "—".to_string()
    } else {
        format!("{:.1}", latency.median_hours)
    }
}

/// Unix seconds now — the window's right edge. Written out rather than pulling a clock type into a handler.
fn now_seconds() -> f64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0.0, |elapsed| elapsed.as_secs_f64())
}

/// The SVG, computed from the text it will draw.
///
/// The **width is the sum of the segments**, not a constant: a hard-coded width truncates the third number, and truncation in an image
/// is invisible to the person reading it. `7` is a deliberately conservative advance for an 11px system-ui glyph — a wide glyph
/// overflows the box rather than clipping the text.
///
/// **Widths are counted in BYTES, not characters**, because that is what the reference does: Go's `len(string)` is the UTF-8 byte
/// count, so the em dash an unmeasured value renders occupies three advances rather than one. That is a defect in the reference's
/// geometry — the dash is drawn in 7px and allotted 21 — but it is the geometry every README already displays, and a badge that is
/// one glyph narrower than its neighbours is a diff in someone else's page. Measured against the live service, byte for byte.
fn badge_svg_text(segments: &[(String, String)]) -> String {
    const FONT: usize = 11;
    const WIDTH: usize = 7;
    const PAD: usize = 8;
    const GAP: usize = 12;
    let total: usize = if app_mutant("badge-fixed-width") {
        // Control: a constant width. Truncation in an image is invisible to the person reading it.
        segments.len() * 90
    } else {
        segments
            .iter()
            .map(|(label, value)| PAD + advance(label) * WIDTH + GAP + advance(value) * WIDTH + PAD)
            .sum()
    };
    let mut out = format!(
        r##"<svg xmlns="http://www.w3.org/2000/svg" width="{total}" height="20" role="img" aria-label="Concord badge">"##
    );
    let mut x = 0usize;
    for (label, value) in segments {
        let label_width = advance(label) * WIDTH;
        out.push_str(&format!(
            r##"<text x="{}" y="14" font-family="system-ui, -apple-system, sans-serif" font-size="{FONT}" fill="#555">{}</text>"##,
            x + PAD,
            escape_xml(label),
        ));
        out.push_str(&format!(
            r##"<text x="{}" y="14" font-family="ui-monospace, SFMono-Regular, monospace" font-size="{FONT}" font-weight="600" fill="#111">{}</text>"##,
            x + PAD + label_width + GAP,
            escape_xml(value),
        ));
        x += PAD + label_width + GAP + advance(value) * WIDTH + PAD;
    }
    out.push_str("</svg>");
    out
}

/// The advance count of a run of text: BYTES in the reference's geometry, CHARACTERS in the control that reverses it.
fn advance(text: &str) -> usize {
    if app_mutant("badge-chars-not-bytes") {
        text.chars().count()
    } else {
        text.len()
    }
}

/// XML text escaping. The values here are numbers and dashes, but the labels come from code that may one day take a project name, and
/// an unescaped `&` in an SVG is a broken image in someone else's README with no error anywhere.
fn escape_xml(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    for ch in text.chars() {
        match ch {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&quot;"),
            _ => out.push(ch),
        }
    }
    out
}



/// The one public, unauthenticated badge route, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new().route("/badge/{file}", axum::routing::get(badge_svg))
}
