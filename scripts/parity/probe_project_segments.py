"""Every project-scoped route the PORT registers, asked with a SLUG.

# Why this exists

Four routes were "ported" and answered nothing a page could use, and the route inventory could not see any of them: it compares
PATTERNS, and a pattern with a project segment is the same pattern whether the handler resolves an id, a slug, or both.

    /api/v1/projects/suki/threads/project/1         400 project id must be an integer
    /api/v1/projects/suki/features/2128/complaints  404 no such project
    /api/v1/projects/feat-demo/complaints/2         404 no such project
    /api/v1/projects/suki/charter                   400 project id must be an integer

Each was found by a browser suite, one at a time, each after rediscovering the same fact: the reference's own page scripts build every one
of these URLs from the SLUG (`comments.js`, `feature.js`, `settings.js`), and the reference answers them. This probe states the fact once,
for every route at once.

# Where the list comes from, and why not from `routes.py`

The inventory's `go_routes()` returns paths with `/api/v1` stripped and every placeholder normalised to `{}` — correct for comparing two
implementations, useless for making a request. The first version of this probe used it and reported fourteen routes as broken that were
merely requested at the wrong address, which is a gate that cries wolf. So the list is parsed from the PORT's own router source: the
`.route("...")` literals under `crates/app` and `crates/web`, which carry their real prefixes and their real segment names.

# What it asserts

For every GET route whose path names a project, `suki` substituted for that segment must answer with something that is not a 400 parse
failure and not a 404. A route may legitimately refuse an anonymous reader (401) or a caller without standing (403); this probe is about
the SEGMENT. A 5xx is a failure: the segment reached the store and broke something.

A route registered as `/api/v1/projects/{id}` is asked with the slug too — that is the entire point, and a route that only resolves an id
is exactly the defect this file guards.

# Usage

    CANDIDATE=http://127.0.0.1:8108 python3 scripts/parity/probe_project_segments.py
"""

import json
import os
import re
import sys
import urllib.error
import urllib.request

BASE = os.environ.get("CANDIDATE", "http://127.0.0.1:8108")
SLUG = os.environ.get("PROBE_PROJECT", "suki")
UA = "Mozilla/5.0 (X11; Linux x86_64) concord-port-probe"

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SOURCES = ["crates/app/src/lib.rs", "crates/app/src/feeds.rs", "crates/web/src/lib.rs"]


def routes():
    """GET route literals the port registers that name a project, with the project placeholder marked."""
    found = set()
    for relative in SOURCES:
        path = os.path.join(REPO, relative)
        if not os.path.exists(path):
            continue
        source = open(path).read()
        # `.route("PATH", get(handler))` — the verb is the first combinator after the literal, so a route whose only verb is `post(` is
        # skipped rather than asked as a GET.
        for match in re.finditer(r'\.route\(\s*"([^"]+)"\s*,\s*([a-z_]+)\(', source):
            route, verb = match.group(1), match.group(2)
            if verb not in ("get", "any", "on"):
                continue
            if "/projects/{" not in route:
                continue
            # The FIRST project segment is the project; later ones are features, arenas and calls, and are asked with `1`.
            route = re.sub(r"\{[a-z_]+\}", "{PROJECT}", route, count=1)
            found.add(route)
    return sorted(found)


def get(path):
    request = urllib.request.Request(BASE + path, headers={"User-Agent": UA})
    try:
        with urllib.request.urlopen(request) as response:
            return response.status, response.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode("utf-8", "replace")
    except urllib.error.URLError as error:
        return 0, str(error)


PROJECT_FAILURES = (
    # The route's path extractor typed the project segment as an integer: Axum rejects the request before any handler runs.
    "Invalid URL",
    "must be an integer",
    # A handler looked the segment up and failed. `no such project` and `project suki` are the two spellings the port uses.
    "no such project",
    "project suki",
)


def classify(status, body):
    """Is this answer a failure to RESOLVE THE PROJECT, or something else that is fine?

    The distinction is the whole gate, and the first version of this probe got it wrong by treating every 404 as a failure: fourteen
    routes were reported broken because the CHILD id the probe invented (`/features/1`) does not exist in the project — the project
    resolved perfectly and the answer said so. So the test is the message, not the status.
    """
    if status == 0 or status >= 500:
        return "the segment reached the store and broke it"
    try:
        message = json.loads(body).get("error")
    except Exception:  # noqa: BLE001 — an extractor rejection is plain text, which is itself a finding
        message = body
    message = message if isinstance(message, str) else str(message)
    for marker in PROJECT_FAILURES:
        if marker in message:
            return f"refused the segment: {message[:90]!r}"
    return None


def main():
    failures = 0
    paths = routes()
    print(f"{len(paths)} project-scoped GET route(s) registered by the port\n")
    for pattern in paths:
        path = re.sub(r"\{[a-z_]+\}", "1", pattern.replace("{PROJECT}", SLUG))
        status, body = get(path)
        complaint = classify(status, body)
        if complaint:
            print(f"FAIL {path} -> {status} {complaint}")
            failures += 1
        else:
            print(f"ok   {path} -> {status}")
    print(f"\n{len(paths) - failures}/{len(paths)} answered")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
