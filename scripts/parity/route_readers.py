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
    for route in routes():
        pattern = reader_pattern(route)
        hits = sum(1 for _path, text in files if pattern.search(text))
        rows.append((route, hits))

    unread = [r for r in rows if r[1] == 0]
    print(f"routes registered: {len(rows)}")
    print(f"routes with a reader: {len(rows) - len(unread)}")
    print(f"routes with no reader: {len(unread)}")

    if args.all:
        for route, hits in rows:
            print(f"  {hits:3d}  {route}")
    elif args.unread or not sys.argv[1:]:
        for route, _ in unread:
            print(f"       {route}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
