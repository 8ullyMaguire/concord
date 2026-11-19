# Step 6 — one pagination convention, enforced

Date: 2026-11-19
Status: spec accepted; plan in `.hermes/plans/2026-11-19_031500-step6-pagination.md`.

## Problem

Measured at `fef7bcd`:
- `scripts/parity/collection_pages.py` does not exist; the roadmap's named gate is absent.
- `GET /api/v1/projects?limit=10000` returns `200` and a body beginning `[{"id":1,"slug":"lorebook",…`.
- `list_projects` in `crates/app/src/api/catalog.rs` takes no query and returns every project.
- `list_documents` in `crates/app/src/api/documents.rs` accepts only `fields`, so one project's 11.7 MB of prose is
  assembled before the browser asks for `?fields=meta`.

There is no shared answer to "how much may one response contain", so each collection handler invents its own.

## Goals

1. One `PageQuery` extractor: `limit` defaults to 50, must be 1..=200, optional opaque `cursor`.
2. A `Page<T>` response with `items`, `next`; `next` is null when there is no further page.
3. Every collection route accepts the extractor. The structural gate fails when a handler returning a collection
   does not take `PageQuery`, unless it has a named allow entry with a reason recorded here.
4. Live refusal: `?limit=10000` → `400` with `limit must be 1..=200`; `?limit=0` → same refusal.

## Non-goals

- Changing the order or content of existing rows. Pagination slices the same measured list.
- Changing the `?fields=meta` document response in the first slice.
- Rewriting the frontend fetch layer in this slice. Existing callers that omit `limit` get the first 50 rows, which
  is a visible behaviour change for `/projects`; D-116 records why that is acceptable and how the next-page link is
  exposed.
- Adding new database indexes. The large lists are bounded in the app before this step; index work is a measurement
  away.

## Decisions

- D-115: `PageQuery` lives in `crates/app/src/pagination.rs` and is shared by handlers, not reimplemented per route.
- D-116: the body carries `items` and `next`; `/api/v1/projects` also carries `projects` with the same array because
  `probe_pages.py:146` reads that key. The frontend's project cards come from the server-rendered bootstrap, not this array.
- D-117: cursor is the opaque string `o:<offset>`; an invalid cursor is a 400, never a silent first page. No new
  base64 dependency is needed for a value that is already opaque to callers.
- D-118: allow entries are only for routes that are bounded by a parent id and already small (board columns, charter);
  each allow entry must name the route and the measured max. The first gate ships with an empty allow list.

## Success criteria

1. `python3 scripts/parity/collection_pages.py` → `PASS — N collection routes, 0 without PageQuery`.
2. `cargo test -p concord-app --lib pagination` → at least 3 passed; 0 failed, including default, over-limit and bad
   cursor cases.
3. `gate: PASS` with `pages: 132/132 ok`.
4. Live `?limit=10000` → `400` with the named message; `?limit=50&cursor=<valid>` → next page; `?cursor=garbage` → 400.
5. Deploy stamped with the new sha; anonymous walk 0 holes; CI green.

## Order

1. Add row 6 and D-115..D-118 to the roadmap; commit spec and plan.
2. Add `pagination.rs`, unit tests, and `collection_pages.py` scanner. Run both RED before wiring.
3. Apply `PageQuery` to `/api/v1/projects` and the document list; prove the live refusals and next cursor.
4. Extend to the remaining collection routes until the scanner reports 0 without a page parameter.
5. Gate, deploy, live probe, docs/vault/handoff.
