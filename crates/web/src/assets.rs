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

/// Where the assets are read from: the binary, or a directory when `CONCORD_ASSET_DIR` says so.
///
/// # Why an override exists for a thing that was deliberately embedded
///
/// The embed is the production answer and stays the default. But the mutation gate for these scripts (which proves the browser suites
/// actually assert the rules the scripts implement) rewrites one asset and needs the server to serve the rewrite. With the embed as
/// the only source, that means `cargo build --release` per mutant — a relink of a 21 MB binary that took **four to six minutes on a loaded
/// host**, nine times, and one of those builds was killed by the kernel under memory pressure and reported as a *surviving mutant*. The
/// override turns nine rebuilds into nine file writes. It is dev-only by construction: unset in production, unreadable from a route, and
/// the directory must be named by whoever starts the process.
///
/// The traversal guard is not decoration: the path comes from the URL.
fn read_asset(path: &str) -> Option<Vec<u8>> {
    let dir = std::env::var("CONCORD_ASSET_DIR").ok().filter(|dir| !dir.is_empty())?;
    if path.split('/').any(|segment| segment.is_empty() || segment == "." || segment == "..") {
        return None;
    }
    std::fs::read(std::path::Path::new(&dir).join(path)).ok()
}

/// `GET /assets/{*path}` — one file, or 404.
///
/// The content type is decided from the extension rather than stored: there are two kinds of file here, and a table of MIME types keyed
/// by extension is smaller than the table of files.
pub async fn serve(Path(path): Path<String>) -> Response {
    let path = path.trim_start_matches('/');
    let bytes: Vec<u8> = match read_asset(path) {
        Some(from_disk) => from_disk,
        None => match ASSETS.iter().find(|(name, _)| *name == path) {
            Some((_, bytes)) => bytes.to_vec(),
            None => return (StatusCode::NOT_FOUND, "no such asset").into_response(),
        },
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
