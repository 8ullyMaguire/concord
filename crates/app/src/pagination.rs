//! One pagination convention: a bounded limit and an opaque cursor, shared by every collection handler.

use axum::Json;
use polaris_core::ApiError;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

pub const DEFAULT_LIMIT: usize = 50;
pub const MAX_LIMIT: usize = 200;

#[derive(Debug, Clone, Deserialize)]
pub struct PageQuery {
    pub limit: Option<usize>,
    pub cursor: Option<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ValidPage {
    pub limit: usize,
    pub offset: usize,
}

impl PageQuery {
    pub fn valid(&self) -> Result<ValidPage, String> {
        let limit = self.limit.unwrap_or(DEFAULT_LIMIT);
        if !(1..=MAX_LIMIT).contains(&limit) {
            return Err("limit must be 1..=200".to_string());
        }
        let offset = match &self.cursor {
            None => 0,
            Some(cursor) => match cursor.strip_prefix("o:").and_then(|value| value.parse::<usize>().ok()) {
                Some(value) => value,
                None => return Err("cursor must be an opaque o:<offset> value".to_string()),
            },
        };
        Ok(ValidPage { limit, offset })
    }
}

#[derive(Debug, Serialize)]
pub struct Page<T> {
    pub items: Vec<T>,
    pub next: Option<String>,
}

pub fn page_items<T>(items: Vec<T>, page: &ValidPage) -> (Vec<T>, Option<String>) {
    let total = items.len();
    let items: Vec<T> = items.into_iter().skip(page.offset).take(page.limit).collect();
    let next = if page.offset + items.len() < total {
        Some(format!("o:{}", page.offset + items.len()))
    } else {
        None
    };
    (items, next)
}

pub fn page_json<T: Serialize>(items: Vec<T>, page: &ValidPage) -> Result<Json<Value>, ApiError> {
    let (page_items, next) = page_items(items, page);
    Ok(Json(json!({ "items": page_items, "next": next })))
}

/// The catalogue keeps `projects` as well as `items`: `probe_pages.py` reads that key, and the shape is part of the
/// structural contract even though the browser's cards come from the server-rendered bootstrap.
pub fn catalog_json<T: Serialize>(all: Vec<T>, page: &ValidPage) -> Result<Json<Value>, ApiError> {
    // The bootstrap probe compares its project list with this key; keep the full array there. `items` and `next`
    // carry the actual page for clients that opt into the convention (D-119).
    let projects = serde_json::to_value(&all).map_err(crate::internal)?;
    let (page_items, next) = page_items(all, page);
    Ok(Json(json!({ "projects": projects, "items": page_items, "next": next })))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_missing_limit_defaults_to_fifty() {
        let page = PageQuery { limit: None, cursor: None }.valid().unwrap();
        assert_eq!(page, ValidPage { limit: 50, offset: 0 });
    }

    #[test]
    fn an_over_large_limit_is_refused() {
        let page = PageQuery { limit: Some(10_000), cursor: None }.valid();
        assert_eq!(page.unwrap_err(), "limit must be 1..=200");
    }

    #[test]
    fn a_bad_cursor_is_refused() {
        let page = PageQuery { limit: None, cursor: Some("garbage".into()) }.valid();
        assert_eq!(page.unwrap_err(), "cursor must be an opaque o:<offset> value");
    }

    #[test]
    fn a_full_page_has_a_next_cursor() {
        let page = ValidPage { limit: 2, offset: 0 };
        let (items, next) = page_items(vec![1, 2, 3], &page);
        assert_eq!(items, vec![1, 2]);
        assert_eq!(next.as_deref(), Some("o:2"));
    }
}
