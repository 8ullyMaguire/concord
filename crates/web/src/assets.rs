//! The page assets, compiled into the binary.
//!
//! # Why they are in the binary and not on disk
//!
//! The Go service served these from `internal/httpapi/assets` inside its own image. The port ships one file, and a page whose script is
//! missing is a page that renders its skeleton forever — so the assets are `include_bytes!`d and cannot be deployed separately from the
//! page that asks for them. `build.rs` generates the table; see its comments for why the table is generated rather than hand-written.
//!
//! # Provenance, because these are not this crate's code
//!
//! They are the reference implementation's page scripts and stylesheet, taken verbatim from the archived Go tree
//! (`internal/httpapi/assets`, commit `eb10320`). They are the **behaviour the browser suites assert**: the suites click `#project-detail`
//! after its `aria-busy` flips, count `.card-title a`, and read a rating bar whose width is set by the script. A rewrite in another
//! framework would be a rewrite of the thing under test, and the tests are the row's gate. Recorded in the ledger with the decision.

use axum::{
    extract::Path,
    http::{header, StatusCode},
    response::{IntoResponse, Response},
};

include!(concat!(env!("OUT_DIR"), "/asset_table.rs"));

/// `GET /assets/{*path}` — one file, or 404.
///
/// The content type is decided from the extension rather than stored: there are two kinds of file here, and a table of MIME types keyed
/// by extension is smaller than the table of files.
pub async fn serve(Path(path): Path<String>) -> Response {
    let path = path.trim_start_matches('/');
    let Some(bytes) = ASSETS.iter().find(|(name, _)| *name == path).map(|(_, bytes)| *bytes) else {
        return (StatusCode::NOT_FOUND, "no such asset").into_response();
    };
    let content_type = if path.ends_with(".js") {
        "application/javascript; charset=utf-8"
    } else if path.ends_with(".css") {
        "text/css; charset=utf-8"
    } else if path.ends_with(".svg") {
        "image/svg+xml"
    } else {
        "application/octet-stream"
    };
    // A page asset is versioned by the binary it ships with, and a cached script beside a fresh page is the classic "the fix did not
    // take" bug. `no-cache` makes the browser revalidate; the ETag is the binary's own build, so a redeploy is a new validator.
    (
        [
            (header::CONTENT_TYPE, content_type),
            (header::CACHE_CONTROL, "no-cache"),
        ],
        bytes,
    )
        .into_response()
}

/// The asset names, for the tests that assert a page's scripts are actually served.
#[must_use]
pub fn names() -> Vec<&'static str> {
    ASSETS.iter().map(|(name, _)| *name).collect()
}
