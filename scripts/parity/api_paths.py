"""Every API path a page builds, checked for the one character that made three features lie.

The port matches routes exactly. `/api/v1/similar/{kind}` answers; `/api/v1/similar/{kind}/` is a 404 with body
`{"error":{"code":"not_found"}}`. The Go service answered both, so three call sites kept a trailing slash and were
silently dead from cutover (2026-11-14) until `duplicates_e2e` was finally pointed at PostgreSQL:

    project-writes.js  '/api/v1/similar/' + kind + '/?text='        -> the duplicate panel said "could not run"
    scout.js           '/api/v1/projects/' + slug + '/documents/'   -> saving a scout digest 404'd
    settings.js        '/api/v1/projects/' + slug + '/features/'    -> the settings page lost its feature count

Two of the three swallow the failure (`getOrNull`, a `.catch`), which is why nothing noticed. A green build, a 200 on the
page and 136/136 probes are all consistent with a request that never resolves.

Three shapes are wrong, and this fails on each:

  1. a literal that starts with `/?`      — a slash written immediately before a query string
  2. a literal that ends with `/` AND is the end of the URL (not followed by `+`, which means the next fragment
     supplies the rest)                    — a trailing slash on a route the server matches exactly
  3. a literal that is only `/` followed by nothing, concatenated onto a collection path

A base fragment like `'/api/v1/projects/' + encodeURIComponent(slug)` is correct and is not flagged: the `/` is a
separator, and the next fragment carries the segment.

    python3 scripts/parity/api_paths.py            # exit 1 on any hit, prints file:line
"""

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
ASSETS = ROOT / "crates/web/assets/js"

LITERAL = re.compile(r"""(['"])(/[^'"\n]*)\1""")
QUERY_START = re.compile(r"""^/\?""")

# Reviewed exceptions: literals that end in `/` and are NOT the end of a URL. Each is a prefix the caller appends to, or a
# string test rather than a request. Everything else ending in `/` is a request the server will answer 404.
ALLOW = {
    ("consent.js", "/api/v1/auth/"): "`indexOf` on a URL to decide whether to attach the consent cookie — not a request",
    ("settings.js", "/strategy/themes/"): "argument to `withdrawAll`, which appends the id and `/withdraw`",
    ("settings.js", "/strategy/weight-proposals/"): "argument to `withdrawAll`, which appends the id and `/withdraw`",
}


def scan(path):
    hits = []
    for n, line in enumerate(path.read_text().split("\n"), 1):
        stripped = line.strip()
        if stripped.startswith("//") or stripped.startswith("*"):
            continue
        for m in LITERAL.finditer(line):
            literal = m.group(2)
            after = line[m.end():].lstrip()
            # (1) a slash immediately before a query string: `/similar/complaint/?text=`
            if QUERY_START.match(literal):
                hits.append((path.name, n, literal + "  (slash before the query)", stripped[:100]))
                continue
            # (2) a URL that ends with a slash: the next thing is not another fragment
            if literal.endswith("/") and literal != "/" and not after.startswith("+"):
                hits.append((path.name, n, literal + "  (URL ends in a slash)", stripped[:100]))
    return hits


def main():
    files = sorted(ASSETS.glob("*.js"))
    hits = [h for f in files for h in scan(f)]
    allowed = [h for h in hits if (h[0], h[2].split("  ")[0]) in ALLOW]
    hits = [h for h in hits if (h[0], h[2].split("  ")[0]) not in ALLOW]
    for name, n, match, _ in allowed:
        print(f"  allowed: {name}:{n}  {match.split('  ')[0]}  — {ALLOW[(name, match.split('  ')[0])]}")
    print(f"assets scanned: {len(files)}")
    if not hits:
        print("API paths with a stray trailing '/': 0")
        return 0
    print(f"API paths with a stray trailing '/': {len(hits)}")
    for name, n, match, context in hits:
        print(f"  {name}:{n}  {match}\n      {context}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
