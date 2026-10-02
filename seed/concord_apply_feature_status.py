#!/usr/bin/env python3
"""Backfill feature statuses from the provenance recorded at import time.

The importer records in each complaint body whether the row came from a ledger
("from ledger X") or from git history ("from git log (verifiable)"). A ledger
row whose status column read as a done-word is shipped; a git-log row is
shipped by definition, because the commit exists.

This exists as a separate pass rather than a flag on the importer: the importer
only sets status for features it creates in the same pass, so re-running it over
already-imported data creates nothing and therefore updates nothing. That is the
reason the first backfill attempt silently did nothing.

Status changes need trust level >= config.TrustLevelMin, which no account had
until 2026-10-02; the refusals are counted and reported rather than swallowed.
"""
import json
import os
import re
import sys
import urllib.error
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from concord_import_portfolio import (Client, as_items, TOKEN_FILE)  # noqa: E402

BASE = "http://127.0.0.1:8006"

# A feature is shipped when its own complaint body says so. The ledger status
# vocabulary is deliberately not collapsed into a single scale across projects:
# "shipped" in one ledger is not the same claim as "tested" in another, and
# both were written down as done-words by classify().
SHIPPED_MARKERS = ("shipped", "from git log")


def main():
    token = open(TOKEN_FILE).read().strip()
    c = Client(BASE, token)

    st, projects = c.get("/api/v1/projects")
    if st != 200:
        raise SystemExit("cannot list projects: HTTP %d" % st)

    applied = unchanged = refused = unknown = 0
    per_project = []

    for p in sorted(as_items(projects), key=lambda x: x.get("slug", "")):
        slug = p["slug"]
        st, feats = c.get("/api/v1/projects/%s/features" % slug)
        if st != 200:
            continue
        items = as_items(feats)
        if not items:
            continue

        # Pull this project's complaints once and index them by title: the
        # feature body names its source, and the complaint body carries the
        # derived status.
        stc, comps = c.get("/api/v1/projects/%s/complaints" % slug)
        by_title = {i.get("title", "").strip(): i.get("body", "") for i in
                    as_items(comps)} if stc == 200 else {}

        n = 0
        for f in items:
            if f.get("status") == "shipped":
                unchanged += 1
                continue
            body = (f.get("body") or "") + " " + by_title.get(f.get("title", "").strip(), "")
            want = "shipped" if any(m in body.lower() for m in SHIPPED_MARKERS) else None
            if want is None:
                # A ledger row that parsed as planned stays draft. That is the
                # honest state: nothing in the repo says it was built.
                unknown += 1
                continue
            code, resp = c.put("/api/v1/projects/%s/features/%d/status" % (slug, f["id"]),
                               {"status": want,
                                "reason": "backfilled from the provenance recorded at import"})
            if code == 200:
                applied += 1
                n += 1
            elif code in (401, 403):
                refused += 1
            else:
                print("  %s/%s -> %d %s" % (slug, f["id"], code, str(resp)[:90]))
        if n:
            per_project.append((slug, n))

    print("applied to shipped : %d" % applied)
    print("already shipped    : %d" % unchanged)
    print("left as draft      : %d" % unknown)
    print("REFUSED (403)      : %d" % refused)
    print("api calls          : %d (%d throttled)" % (c.calls, c.throttled))
    if per_project:
        print("\nby project (top 15):")
        for slug, n in sorted(per_project, key=lambda x: -x[1])[:15]:
            print("   %4d  %s" % (n, slug))


if __name__ == "__main__":
    main()
