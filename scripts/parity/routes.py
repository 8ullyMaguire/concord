#!/usr/bin/env python3
"""Row 11's exit gate: every route the GO server registers, against the routes the port answers.

Part 11 found `/strategy/themes` missing while every probe passed, because a probe only asks for the routes
somebody remembered. This compares the two routers mechanically:

  * the Go side is parsed from `internal/httpapi/` — `r.Get/Post/Put/Delete/Patch`, `r.Route(prefix, …)`,
    `r.Mount(prefix, …)` and the chi groups they nest in;
  * the port's side is parsed from `crates/app/src/lib.rs` and `crates/cli/src/lib.rs` — `.route("…", …)` and the
    method chain after it.

A route present in Go and absent in the port is a 404 at cutover. The comparison is by SHAPE, so `{slug}` and
`{project_id}` are the same path with a different parameter name: what matters is the segments and the method.

Usage:  python3 scripts/parity/routes.py [--go-only]
Exit:   0 when every Go route is answered, 1 when one is missing (and it prints the list).
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

# Where the REFERENCE implementation lives. The Go service still serves production until the cutover row, and this
# script parses its router to know what the port owes — but the Go tree now lives in the archive, not in this repo.
#
# Resolved in that order:
#   1. `CONCORD_GO_SRC` — set it when the reference is somewhere else (a second checkout, a build box);
#   2. `internal/httpapi` under this repo — true for any checkout that still carries the Go tree;
#   3. `~/code/archive/concord` — where it was archived on 2026-11-13 (`archived-go`, commit eb10320).
#
# A missing reference is a hard error rather than an empty route set: "the port answers every route" is a claim, and a
# claim made against a directory that is not there is the most comfortable kind of false green.
def go_source_root() -> str:
    """The directory holding the Go implementation this script parses."""
    configured = os.environ.get("CONCORD_GO_SRC")
    if configured:
        return configured
    if os.path.isdir(os.path.join(ROOT, "internal", "httpapi")):
        return ROOT
    return os.path.expanduser("~/code/archive/concord")


GO_ROOT = go_source_root()

# chi method helpers, in the order a chained call can appear.
METHODS = ("Get", "Post", "Put", "Delete", "Patch", "Head", "Options", "Method")


def go_routes():
    """Every (method, path) the Go server registers, with nested prefixes expanded."""
    return set(go_routes_with_handlers())


def go_routes_with_handlers():
    """Every (method, path) the Go server registers, mapped to the handler's name.

    The handler name is what makes the missing list ACTIONABLE: the Go registers HTML page routes (`/projects/{slug}
    /rank`, `/badge/{id}`, `/finder`) beside its JSON API, and the estate profile serves those pages from the
    frontend. `go_routes` alone reported all 123 as "routes the port does not answer", which invites porting pages as
    JSON — work that would then have to be deleted.
    """
    found = {}
    httpapi = os.path.join(GO_ROOT, "internal", "httpapi")
    if not os.path.isdir(httpapi):
        raise SystemExit(
            f"no Go reference found at {httpapi} — set CONCORD_GO_SRC to the archived Go tree "
            f"(default ~/code/archive/concord). Reporting an inventory without it would read as a finished port."
        )
    for name in sorted(os.listdir(httpapi)):
        if not name.endswith(".go") or name.endswith("_test.go"):
            continue
        with open(os.path.join(httpapi, name), encoding="utf-8") as handle:
            source = handle.read()
        # Every `r.Route("/prefix", func(r chi.Router) {` opens a block whose inner routes carry the prefix.
        # A small stack walk is enough: chi groups are declared with `r.Route` / `r.Group` and closed by `})`.
        stack = []  # list of prefixes at the current depth
        depth_by_prefix = {}
        depth = 0
        for line in source.splitlines():
            stripped = line.strip()
            # A route written INSIDE a comment is not a route. `handlers.go` explains the defect it fixed by quoting the
            # registration it moved — `// r.Post("/merge/{target_id}", …)` — and this script counted that quotation as a live
            # route under the comment's own prefix, so the port was reported as missing `POST /complaints/{}/merge/{}`, an
            # endpoint that has never existed on either side.
            if stripped.startswith("//"):
                continue
            # Count braces OUTSIDE string literals. A chi route prefix like `"/api/v1/projects/{project_id}/complaints"`
            # carries braces of its own, and counting them made one `r.Route(` line open TWO blocks — so the stack popped the
            # outer prefix early and every route in a doubly-nested block lost it. That is how
            # `POST /api/v1/projects/{project_id}/complaints/{id}/merge/{target_id}` was reported MISSING while the port has been
            # answering it for many turns: the Go side had recorded it as `/complaints/{}/merge/{}`.
            without_strings = re.sub(r'"(?:[^"\\]|\\.)*"', '""', stripped)
            opened = without_strings.count("{") - without_strings.count("}")
            # A prefix opens BEFORE the line's own route matches, so the block's own registrations see it.
            route_prefix = re.search(r"\br\.(?:Route|Group|Mount)\(\s*\"([^\"]+)\"", stripped)
            if route_prefix:
                stack.append((depth, route_prefix.group(1)))
                depth_by_prefix[depth] = route_prefix.group(1)
            for method in METHODS:
                match = re.search(
                    rf"\br\.{method}\(\s*\"([^\"]+)\"\s*,\s*(?:s\.)?(\w+)", stripped
                )
                if match:
                    path, handler = match.group(1), match.group(2)
                else:
                    # A registration whose handler is not a plain `s.name` (a closure, or a wrapped handler). The
                    # route still exists and must still be counted — losing it would hide work — so it is recorded
                    # with an empty handler and classified as JSON until somebody looks.
                    plain = re.search(rf"\br\.{method}\(\s*\"([^\"]+)\"", stripped)
                    if not plain:
                        continue
                    path, handler = plain.group(1), ""
                prefix = "".join(part for _, part in stack)
                full = prefix.rstrip("/") + "/" + path.lstrip("/")
                # A LIST of handlers, because `normalise` strips `/api/v1` and so an API route and a page route of the
                # same shape collide. Keeping both lets the classification say "JSON" when either answers JSON,
                # which is the question that matters: is there API work here?
                # The pair is (handler, registered-under-/api/), because `/api/v1` is stripped by `normalise` and the Go
                # registers a PAGE and its API at the same shape — `/projects/{}/board` and `/api/v1/projects/{}/board` —
                # so the flag is the only thing that still tells them apart after the prefix is gone.
                found.setdefault((method.upper(), normalise(full)), []).append((handler, "/api/" in full))
            depth += opened
            # Close blocks whose depth has gone past.
            while stack and depth <= stack[-1][0]:
                stack.pop()
    return found


def handler_body(name):
    """The source of one `func (s *Server) <name>(` body, or "" when it cannot be found."""
    httpapi = os.path.join(GO_ROOT, "internal", "httpapi")
    needle = f"func (s *Server) {name}("
    for filename in sorted(os.listdir(httpapi)):
        if not filename.endswith(".go") or filename.endswith("_test.go"):
            continue
        with open(os.path.join(httpapi, filename), encoding="utf-8") as handle:
            source = handle.read()
        start = source.find(needle)
        if start < 0:
            continue
        end = source.find("\nfunc ", start + 1)
        return source[start:end if end > 0 else len(source)]
    return ""


def answers_json(name):
    """Whether a handler answers JSON.

    A handler that calls writeJSON does. One that renders a template does not, and the estate profile serves that
    page from the frontend instead — so it is NOT a port TODO. A handler whose body cannot be found is reported as
    JSON, because guessing "page" would silently drop real API work from the list.
    """
    body = handler_body(name)
    if not body:
        return True
    if "writeJSON" in body:
        return True
    return not (
        "s.render(" in body or "executeTemplate" in body or "parseFiles" in body or "//go:embed" in body
    )


def rust_routes():
    """Every (method, path) the port registers.

    Parsed by finding each `.route(` and then walking to the matching close paren, because the calls are written
    across several lines and a lazy regex over them stops at the first nested `)` — which is how the first
    version of this script reported `/projects/{}/features` as missing when the port answers it.
    """
    # `found` maps a shape to whether it came from a PAGE source. The distinction is the port's own: `crates/web` is the page
    # renderer, `feeds.rs` and the three document routes are pages the app already answered, and everything else under
    # `crates/app` answers JSON. Without it the port's `/api/v1/scout` (JSON) would satisfy the Go's `/scout` (a page) after
    # `normalise` strips the prefix, which is the same collapse that hid 14 page routes on the Go side.
    PAGE_SOURCES = {"crates/web/src/lib.rs", "crates/app/src/feeds.rs"}
    # `/atom.xml` and `/badge/{slug}.svg` are documents the app already answers; `/healthz` answers JSON and is NOT one,
    # which is why it is absent here — putting it in made the port look as if it had dropped the Go's health endpoint.
    PAGE_PATHS = {"/atom.xml", "/badge/{}"}
    api_found = set()
    page_found = set()
    for relative in ("crates/app/src/lib.rs", "crates/app/src/feeds.rs", "crates/web/src/lib.rs"):
        path = os.path.join(ROOT, relative)
        if not os.path.exists(path):
            continue
        with open(path, encoding="utf-8") as handle:
            source = handle.read()
        # A comment between `.route(` and its path is legal Rust and it made the threads route invisible here: the
        # regex demanded the string immediately after the paren, so three routes the port answers were reported as
        # missing work. Skip whole `//` lines between the paren and the string.
        for match in re.finditer(r'\.route\(\s*(?://[^\n]*\n\s*)*"([^"]+)"', source):
            path_text = match.group(1)
            # Walk to the matching close paren of this `.route(` call.
            index = source.index("(", match.start())
            depth = 0
            while index < len(source):
                if source[index] == "(":
                    depth += 1
                elif source[index] == ")":
                    depth -= 1
                    if depth == 0:
                        break
                index += 1
            handlers = source[match.end():index]
            shape = normalise(path_text)
            # A feed is a document: `feed.xml`, `rss.xml`, `atom.xml`. They are registered in `lib.rs` beside the API but they
            # answer XML, and leaving them on the API side reported six routes the port has been answering as missing work.
            is_page = (
                relative in PAGE_SOURCES
                or shape in PAGE_PATHS
                or shape.endswith(("feed.xml", "rss.xml", "atom.xml"))
            )
            for method in ("get", "post", "put", "delete", "patch"):
                if re.search(rf"\b{method}\(", handlers) or re.search(rf"axum::routing::{method}\b", handlers):
                    # The key goes into the set for ITS kind only: a shape can be both (the port answers the page
                    # `/projects` from `crates/web` and the API `/projects` from `crates/app`), and one set holding a
                    # single boolean made whichever registration was read second erase the other.
                    (page_found if is_page else api_found).add((method.upper(), shape))
    return api_found, page_found


def normalise(path):
    """The SHAPE of a path: parameter names are not part of it, and neither is a trailing slash.

    The `/api/v1` prefix is stripped from both sides: the port nests its project routes under
    `.nest("/api/v1", …)`, so the strings in `crates/app/src/lib.rs` start at `/projects/…` while the Go's
    registrations carry the prefix. Comparing the prefixed forms would report every nested route as missing —
    which the first run of this script did, 182 of them.
    """
    parts = [part for part in path.split("/") if part]
    if parts[:2] == ["api", "v1"]:
        parts = parts[2:]
    shaped = ["{}" if part.startswith("{") or part.startswith(":") else part for part in parts]
    return "/" + "/".join(shaped)


def main():
    handlers = go_routes_with_handlers()
    go = set(handlers)
    rust_api_set, rust_page_set = rust_routes()
    rust = rust_api_set | rust_page_set
    # PAGE AND JSON ARE SEPARATE SETS ON BOTH SIDES, not one set split after the fact.
    #
    # `normalise` strips `/api/v1` so a port route and its Go registration compare equal. The Go registers BOTH a page at
    # `/projects/{slug}/board` and its API at `/api/v1/projects/{slug}/board`, and after stripping they are one key with two
    # handlers — so `any(answers_json(...))` classified the pair as JSON and the PAGE disappeared from the missing list. That is
    # how this script reported "missing: 10 page routes" while the Go registers 66 page GETs and the port served none of them.
    #
    # The split is now on the registered path, not on the handler's body: a Go route whose path is not under `/api/` is a page.
    # A path can be BOTH: the Go serves the page `/projects` and the API `/api/v1/projects`, which share a shape once the
    # prefix is stripped. Each registration contributes to its own kind's set, so neither can hide the other.
    go_api = {key for key in go if any(is_api for _, is_api in handlers[key])}
    go_pages = {key for key in go if any(not is_api for _, is_api in handlers[key])}
    rust_pages = rust_page_set
    rust_api = rust_api_set
    missing_api = sorted(go_api - rust_api)
    missing_pages = sorted(go_pages - rust_pages)
    missing = missing_api + missing_pages
    extra = sorted(set(rust) - go)
    api_missing = [(m, path) for m, path in missing_api
                   if any(answers_json(h) for h, _ in handlers.get((m, path), [("", True)]))]
    page_missing = missing_pages
    print(
        f"go routes: {len(go)} ({len(go_api)} json, {len(go_pages)} page)   "
        f"port answers: {len(go & set(rust))}   missing: {len(missing)} ({len(api_missing)} json, {len(page_missing)} page)"
    )
    if "--go-only" in sys.argv:
        for method, path in sorted(go):
            print(f"  {method:6} {path}")
        return 0
    if api_missing:
        print(f"\n{len(api_missing)} JSON route(s) the port does not answer (the row's work):")
        for method, path in api_missing:
            print(f"  {method:6} {path}")
    if page_missing:
        print(f"\n{len(page_missing)} PAGE route(s) the Go renders and the frontend serves (row 12, not port work):")
        for method, path in page_missing:
            print(f"  {method:6} {path}")
    if extra:
        print(f"\n{len(extra)} port route(s) the go tree does not have (extras are allowed; they are listed):")
        for method, path in extra:
            print(f"  {method:6} {path}")
    if not api_missing:
        print("\nevery go JSON route is answered")
    if not page_missing:
        print("every go page route is answered")
    return 1 if missing else 0


if __name__ == "__main__":
    sys.exit(main())
