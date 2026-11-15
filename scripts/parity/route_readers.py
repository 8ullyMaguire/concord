#!/usr/bin/env python3
"""Which API routes does anything actually call?

The review's item 3 says the API was frozen rather than designed, and that the whole audit is a grep:
"every route with no reader is a route to remove". This is that grep, made repeatable.

A route is REACHED when some file outside the router names its path. The readers that count are the two
clients we have and the tests that stand in for them:

  crates/web/assets/js/*.js      the browser
  crates/cli/src/*.rs            the CLI
  tests/e2e/*.py                 the browser suites
  scripts/**/*.py                the probes and parity scripts
  scripts/**/*.sh                the gate scripts, which call routes with curl
  crates/web/src/*.rs            server-rendered pages that fetch their own API

Everything else is not a reader: the router itself, the docs, and this script.

Matching: a route literal with `{param}` segments becomes a pattern where each parameter matches one
path segment, and the search is done against the concatenated text of every reader file. Two routes that
differ only by a parameter name match the same readers, which is correct — they are one path.

Usage:
    scripts/parity/route_readers.py            # summary + the unread list
    scripts/parity/route_readers.py --all      # every route with its reader count
    scripts/parity/route_readers.py --unread   # only the unread ones
"""

import argparse
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
ROUTER = ROOT / "crates/app/src/lib.rs"

READER_GLOBS = [
    "crates/web/assets/js/*.js",
    "crates/cli/src/*.rs",
    "tests/e2e/*.py",
    "scripts/**/*.py",
    "scripts/**/*.sh",
    "crates/web/src/*.rs",
]


def routes() -> list[str]:
    text = ROUTER.read_text()
    found = re.findall(r'\.route\(\s*"([^"]+)"', text)
    # `nest` and `fallback` would be invisible to this, and that is stated rather than hidden: the count
    # printed below is the count of `.route()` literals, not of everything axum can answer.
    return sorted(set(found))


def reader_files() -> list[pathlib.Path]:
    files = []
    for pattern in READER_GLOBS:
        for path in ROOT.glob(pattern):
            if path.resolve() == pathlib.Path(__file__).resolve():
                continue
            files.append(path)
    return files


def reader_pattern(route: str) -> re.Pattern:
    """A path as a permissive regex: parameters match anything short, including a string concatenation.

    A reader does not have to contain the whole path in one string —
    `'/api/v1/projects/' + encodeURIComponent(slug) + '/claim'` is a reader of
    `/api/v1/projects/{slug}/claim`. Matching the path exactly found nothing there and reported a live
    route as unread, which is the "a zero grep is not a count" trap. A parameter is allowed up to 80
    characters of anything, which covers `{}`, `{slug}`, `' + encodeURIComponent(slug) + '` and
    `" + id + "`, and is tight enough that two unrelated paths do not match each other.
    """
    parts = []
    for part in route.split("/"):
        if part.startswith("{") and part.endswith("}"):
            parts.append(".{0,80}?")
        else:
            parts.append(re.escape(part))
    return re.compile("/".join(parts), re.DOTALL)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--all", action="store_true", help="list every route with its reader count")
    parser.add_argument("--unread", action="store_true", help="list only routes without readers")
    args = parser.parse_args()

    files = [(path, path.read_text(errors="ignore")) for path in reader_files()]
    rows = []
    indirect = set()
    weak: set[str] = set()
    for route in routes():
        pattern = reader_pattern(route)
        hits = sum(1 for _path, text in files if pattern.search(text))
        if hits == 0:
            # A reader can assemble the path from a variable that already holds the project base:
            # `var base = '/api/v1/projects/' + slug` and later `fetch(base + '/audit/actions')`. Neither
            # the whole path nor the base and the suffix sit next to each other, so a path grep sees
            # nothing. Second tier, deliberately weaker and counted separately: every static segment of
            # the route appears as a quoted `/segment` in the file, and the file also builds a project
            # base. It cannot prove the call, it can show the pieces are there.
            static = [seg for seg in route.strip("/").split("/")
                      if not (seg.startswith("{") and seg.endswith("}"))]
            if static and "/api/v1/projects/" in route:
                for _path, text in files:
                    if "api/v1/projects" in text and all(f"/{seg}" in text for seg in static):
                        weak.add(route)
                        break
        rows.append((route, hits))

    # A reader that assembles the path from a table is invisible to a path grep, and guessing at it is
    # how this script would start reporting its own opinions. Those are declared by hand, one per line,
    # with the file:line that proves the claim — `route_readers.allow` beside this script.
    declared = {}
    allow = pathlib.Path(__file__).with_suffix(".allow")
    if allow.exists():
        for line in allow.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            route, _, why = line.partition(" ")
            declared[route] = why.strip()

    # Three answers, not two. A 0 here is a QUESTION, not a verdict: this script can prove a reader
    # exists and cannot prove one is absent, because a path assembled through two variables leaves no
    # string to find. Reporting "unread" as a count would be the same false confidence as reporting a
    # green suite for a page nobody opened.
    unread = [r[0] for r in rows if r[1] == 0 and r[0] not in declared and r[0] not in weak]
    only_weak = [r[0] for r in rows if r[1] == 0 and r[0] not in declared and r[0] in weak]
    declared_here = [r[0] for r in rows if r[1] == 0 and r[0] in declared]
    print(f"routes registered: {len(rows)}")
    print(f"routes with an adjacent reader (proved by the script): {len(rows) - len(unread) - len(only_weak) - len(declared_here)}")
    print(f"routes declared by hand (route_readers.allow): {len(declared_here)}")
    print(f"routes with only weak evidence (every segment in one file, no path): {len(only_weak)}")
    for route in sorted(only_weak):
        print(f"  weak      {route}")
    print(f"routes with no evidence at all: {len(unread)}")

    if args.all:
        for route, hits in rows:
            print(f"  {hits:3d}  {route}")
    elif args.unread or not sys.argv[1:]:
        for route, _ in unread:
            print(f"       {route}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
