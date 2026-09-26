#!/usr/bin/env python3
"""Report the seeded portfolio: project, complaint and feature counts, and any
duplicate titles. Duplicates are the signal that a seed run was not idempotent,
so this is worth being able to check after every re-run."""

import json
import sys
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:8006/api/v1"


def get(path, attempts=8):
    """GET with backoff. The limiter allows 100 requests a minute per IP, and
    this script makes one per project per collection, so a re-run soon after
    another will be refused."""
    for i in range(attempts):
        try:
            with urllib.request.urlopen(BASE + path, timeout=15) as res:
                return json.loads(res.read() or b"null")
        except urllib.error.HTTPError as e:
            if e.code != 429 or i == attempts - 1:
                raise
            wait = 5.0 * (i + 1)
            print(f"  rate limited on {path}, waiting {wait:.0f}s", file=sys.stderr)
            time.sleep(wait)
    raise AssertionError("unreachable")


def as_items(payload):
    """List endpoints return a bare array; tolerate both shapes."""
    if isinstance(payload, list):
        return payload
    if isinstance(payload, dict):
        for key in ("items", "projects", "complaints", "features"):
            if isinstance(payload.get(key), list):
                return payload[key]
    return []


def main() -> int:
    projects = as_items(get("/projects"))
    print(f"projects: {len(projects)}\n")

    problems = 0
    for p in projects:
        slug = p["slug"]
        complaints = as_items(get(f"/projects/{slug}/complaints"))
        features = as_items(get(f"/projects/{slug}/features"))

        ctitles = [c["title"] for c in complaints]
        ftitles = [f["title"] for f in features]
        cdupes = {t: ctitles.count(t) for t in ctitles if ctitles.count(t) > 1}
        fdupes = {t: ftitles.count(t) for t in ftitles if ftitles.count(t) > 1}
        if cdupes or fdupes:
            problems += 1

        flag = ""
        if cdupes or fdupes:
            flag = f"  DUPLICATES: complaints={cdupes} features={fdupes}"
        print(
            f"{slug:22} {len(complaints):2} complaints  {len(ftitles):2} features"
            f"   (project id {p['id']}){flag}"
        )

    print()
    if problems:
        print(f"{problems} project(s) have duplicate titles: a re-run was not idempotent")
        return 1
    print("no duplicate titles")
    return 0


if __name__ == "__main__":
    sys.exit(main())
