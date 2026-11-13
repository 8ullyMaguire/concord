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

# chi method helpers, in the order a chained call can appear.
METHODS = ("Get", "Post", "Put", "Delete", "Patch", "Head", "Options", "Method")


def go_routes():
    """Every (method, path) the Go server registers, with nested prefixes expanded."""
    found = set()
    httpapi = os.path.join(ROOT, "internal", "httpapi")
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
            opened = stripped.count("{") - stripped.count("}")
            # A prefix opens BEFORE the line's own route matches, so the block's own registrations see it.
            route_prefix = re.search(r"\br\.(?:Route|Group|Mount)\(\s*\"([^\"]+)\"", stripped)
            if route_prefix:
                stack.append((depth, route_prefix.group(1)))
                depth_by_prefix[depth] = route_prefix.group(1)
            for method in METHODS:
                match = re.search(rf"\br\.{method}\(\s*\"([^\"]+)\"", stripped)
                if not match:
                    continue
                path = match.group(1)
                prefix = "".join(part for _, part in stack)
                full = prefix.rstrip("/") + "/" + path.lstrip("/")
                found.add((method.upper(), normalise(full)))
            depth += opened
            # Close blocks whose depth has gone past.
            while stack and depth <= stack[-1][0]:
                stack.pop()
    return found


def rust_routes():
    """Every (method, path) the port registers.

    Parsed by finding each `.route(` and then walking to the matching close paren, because the calls are written
    across several lines and a lazy regex over them stops at the first nested `)` — which is how the first
    version of this script reported `/projects/{}/features` as missing when the port answers it.
    """
    found = set()
    for relative in ("crates/app/src/lib.rs", "crates/app/src/feeds.rs"):
        path = os.path.join(ROOT, relative)
        if not os.path.exists(path):
            continue
        with open(path, encoding="utf-8") as handle:
            source = handle.read()
        for match in re.finditer(r'\.route\(\s*"([^"]+)"', source):
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
            for method in ("get", "post", "put", "delete", "patch"):
                if re.search(rf"\b{method}\(", handlers) or re.search(rf"axum::routing::{method}\b", handlers):
                    found.add((method.upper(), normalise(path_text)))
    return found


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
    go = go_routes()
    rust = rust_routes()
    missing = sorted(go - rust)
    extra = sorted(rust - go)
    print(f"go routes: {len(go)}   port routes: {len(rust)}")
    if "--go-only" in sys.argv:
        for method, path in sorted(go):
            print(f"  {method:6} {path}")
        return 0
    if missing:
        print(f"\n{len(missing)} go route(s) the port does not answer:")
        for method, path in missing:
            print(f"  {method:6} {path}")
    if extra:
        print(f"\n{len(extra)} port route(s) the go tree does not have (extras are allowed; they are listed):")
        for method, path in extra:
            print(f"  {method:6} {path}")
    if not missing:
        print("\nevery go route is answered")
    return 1 if missing else 0


if __name__ == "__main__":
    sys.exit(main())
