#!/usr/bin/env python3
"""Walk every route the policy table declares, anonymously, and report what the instance answers.

This is the measurement step 1 of `docs/plans/refactor-roadmap.md` assumed and did not have. The table
(`crates/app/src/policy.rs`, `ROUTES`) says who may call each route; this asks the running instance what it
does with a caller who has no token, and compares the two:

  * a route declared `Session`/`Account`/`Operator` **and enforced** that answers 200 -> a hole: the gate
    claims to refuse strangers and did not;
  * a route declared `Session`/`Account`/`Operator` and **not enforced** that answers 200 -> the size of the
    remaining step-1b work, named route by route;
  * a route declared `Public`/`Optional` that answers 401/403 -> the table is wrong in the direction that
    breaks readers, which is the one direction a deploy must never ship.

Read-only on purpose: only GET routes are probed by default. `--writes` adds the mutating rows, and the
routes that create something for anybody who asks (`auth/register`, `auth/login`, `guest`, `consent`,
`finder/sessions`, `hooks/forgejo`, `auth/logout`) are never probed — a probe that changes production data
is not a test.

Usage:
    python3 scripts/parity/anonymous_walk.py                        # GET routes, live instance
    python3 scripts/parity/anonymous_walk.py --candidate http://127.0.0.1:8081
    python3 scripts/parity/anonymous_walk.py --json                 # machine-readable
"""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[2]
POLICY = ROOT / "crates/app/src/policy.rs"

ROW_RE = re.compile(
    r'\("(?P<method>GET|POST|PUT|PATCH|DELETE)",\s*"(?P<path>[^"]+)",\s*'
    r"Policy::(?P<policy>\w+),\s*(?P<enforced>true|false)\)"
)

# Placeholders resolved to a real row, because 404 says nothing about access.
SUBSTITUTES = {
    "project_id": "1",
    "slug": "suki",
    "feature": "1",
    "id": "1",
    "list": "1",
    "milestone": "1",
    "solution_id": "1",
    "call": "1",
    "arena_id": "1",
    "complaint": "1",
    "document": "1",
    "request_id": "1",
    "criterion": "1",
    "session": "probe",
    "thread_kind": "feature",
    "thread_id": "1",
    "kind": "tag",
    "hold_id": "1",
    "copy_id": "1",
    "suggestion_id": "1",
    "profile_id": "1",
    "block_id": "1",
    "alias_id": "1",
    "merge_id": "1",
    "invite": "probe",
    "path": "a",
    "name": "a",
}

# Routes that create something for anybody who asks. Declared Public on purpose, excluded from every walk.
NEVER_PROBED = {
    ("POST", "/api/v1/auth/register"),
    ("POST", "/api/v1/auth/login"),
    ("POST", "/api/v1/auth/logout"),
    ("POST", "/api/v1/guest"),
    ("POST", "/api/v1/consent"),
    ("POST", "/api/v1/finder/sessions"),
    ("POST", "/api/v1/hooks/forgejo"),
}

REFUSED = {401, 403}


def rows() -> list[dict]:
    text = POLICY.read_text()
    found = [match.groupdict() for match in ROW_RE.finditer(text)]
    if not found:
        raise SystemExit(f"anonymous_walk: no rows parsed out of {POLICY} — the table or the regex is wrong")
    for row in found:
        row["enforced"] = row["enforced"] == "true"
    return found


def concrete(path: str) -> str:
    return re.sub(r"\{(\w+)\}", lambda match: SUBSTITUTES.get(match.group(1), "1"), path)


def probe(candidate: str, method: str, path: str, timeout: float) -> int | None:
    url = candidate.rstrip("/") + concrete(path)
    request = urllib.request.Request(url, method=method)
    request.add_header("accept", "application/json")
    # A named agent, because the edge in front of the instance answers 403 to a default `Python-urllib` and
    # a 403 from the CDN would read as "the route refuses strangers" — the exact false positive this script
    # exists to distinguish from a real refusal.
    request.add_header("user-agent", "concord-anonymous-walk/1.0 (+https://concord.polarisocial.xyz)")
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.status
    except urllib.error.HTTPError as error:
        return error.code
    except (urllib.error.URLError, TimeoutError, ConnectionError):
        return None


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--candidate", default="https://concord.polarisocial.xyz")
    parser.add_argument("--writes", action="store_true", help="also probe mutating routes")
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--timeout", type=float, default=15.0)
    args = parser.parse_args()

    table = rows()
    holes: list[str] = []
    remaining: list[str] = []
    wrongly_refused: list[str] = []
    unreachable = 0
    checked = 0
    results = []

    for row in table:
        method, path = str(row["method"]), str(row["path"])
        if method != "GET" and not args.writes:
            continue
        if (method, path) in NEVER_PROBED:
            continue
        status = probe(args.candidate, method, path, args.timeout)
        checked += 1
        results.append({"method": method, "path": path, "policy": row["policy"],
                        "enforced": row["enforced"], "status": status})
        if status is None:
            unreachable += 1
            continue
        needs_identity = row["policy"] in ("Session", "Account", "Operator")
        if needs_identity and row["enforced"] and status == 200:
            holes.append(f"HOLE     {method} {path} is enforced as {row['policy']} and answered 200")
        elif needs_identity and not row["enforced"] and status == 200:
            remaining.append(f"{method} {path} ({row['policy']})")
        elif not needs_identity and status in REFUSED:
            wrongly_refused.append(f"REFUSED  {method} {path} is {row['policy']} and answered {status}")

    if args.json:
        print(json.dumps({"checked": checked, "unreachable": unreachable, "results": results,
                          "holes": holes, "remaining": remaining, "wrongly_refused": wrongly_refused}, indent=1))
    else:
        print(f"anonymous_walk: {checked} route(s) walked with no token against {args.candidate}")
        print(f"anonymous_walk: {unreachable} unreachable · {len(holes)} hole(s) · {len(remaining)} declared "
              f"but not enforced · {len(wrongly_refused)} wrongly refused")
        for line in holes + wrongly_refused:
            print("  " + line)
        if remaining:
            print(f"  step 1b owes enforcement on {len(remaining)} route(s), e.g. {remaining[:3]}")

    if holes or wrongly_refused:
        print("anonymous_walk: FAIL")
        return 1
    print("anonymous_walk: PASS — nothing declared as refused answers a stranger, nothing declared public refuses one")
    return 0


if __name__ == "__main__":
    sys.exit(main())
