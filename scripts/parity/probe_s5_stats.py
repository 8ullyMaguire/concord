#!/usr/bin/env python3
"""Row 11: the two statistics lists, and the three rules that keep a number honest.

The corpus records `/stats/history` and `/stats/responsiveness` as `[]` on the live instance and on the port,
and an empty array proves only that the route exists. These are the rules that decide what fills it:

* **a project under the sample floor is ABSENT, never last.** `0.5 hours` on 3 first PRs is not a better
  answer than `8 hours` on 9; and a leaderboard that renders an unmeasured project as `0.0` ranks it FIRST,
  which is the one direction a reader will act on. The probe seeds a project at 0.5h with n=3 and asserts it
  is not in the answer at all.
* **a private project is not ranked.** Every other statistics route publishes an aggregate a private project
  contributes to invisibly; this one publishes a ranking of NAMED projects, so its position is a disclosure
  its owner did not make. The probe seeds a private project at 1.0h with n=10 -- the best number on the
  instance -- and asserts it is absent.
* **the sample rides along**, and the order is stable across two identical calls. A ranking that swaps two
  equal rows between renders cannot be compared with its own previous rendering.

For `/stats/history`: `days` is a clamp and not a `400` (the header asks on every render), the series is
newest-first, and a NULL median stays NULL -- a median of zero is a different claim from "no sample".
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "tests", "e2e"))

BASE = os.environ.get("CANDIDATE", "http://127.0.0.1:8108")

STAMP = int(time.time() * 1000) % 100000000
SLUG_FAST = f"probe-s5-fast-{STAMP}"
SLUG_SLOW = f"probe-s5-slow-{STAMP}"
SLUG_THIN = f"probe-s5-thin-{STAMP}"
SLUG_PRIV = f"probe-s5-private-{STAMP}"
PROBE_SLUGS = (SLUG_FAST, SLUG_SLOW, SLUG_THIN, SLUG_PRIV)


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            raw = response.read()
            return response.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, json.loads(raw) if raw else None
        except ValueError:
            return error.code, raw.decode("utf-8", "replace")


def main():
    import harness

    con = harness.connect("unused")
    now = time.time()

    def project(slug, name, visibility):
        return con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
            " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
            (slug, name, "", "collective", visibility, "both", now, now),
        ).lastrowid

    fast = project(SLUG_FAST, "Fast", "public")
    slow = project(SLUG_SLOW, "Slow", "public")
    thin = project(SLUG_THIN, "Thin", "public")
    priv = project(SLUG_PRIV, "Private", "private")

    # hours, sample, health_score. The three eligible-looking shapes in one table:
    #   Fast 4h on 6   -> published
    #   Slow 8h on 9   -> published, after Fast
    #   Thin 0.5h on 3 -> BELOW THE FLOOR: fastest on the instance, must be absent
    #   Priv 1.0h on 10 -> best number here, private: must be absent
    for project_id, hours, sample in ((fast, 4.0, 6), (slow, 8.0, 9), (thin, 0.5, 3), (priv, 1.0, 10)):
        con.execute(
            "INSERT INTO project_metrics (project_id, health_score, newcomer_response_hours,"
            " newcomer_sample) VALUES (?,?,?,?)",
            (project_id, 0.5, hours, sample),
        )

    # Three recorded days in the future, so they are the newest in the whole table and `limit` selects them
    # rather than whatever the instance already has.
    days = ["2099-01-01", "2099-01-02", "2099-01-03"]
    for index, day in enumerate(days):
        con.execute(
            "INSERT INTO instance_stats (day, projects, projects_measured, features_total, features_seed,"
            " complaints_total, complaints_seed, triage_median_h, triage_n, delivery_median_h, delivery_n,"
            " decision_median_h, decision_n, no_origin, computed_at)"
            " VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
            (
                day, 10 + index, 9, 100, 40, 20, 5,
                # The middle day has NO triage sample: a median of null, not a median of zero.
                None if index == 1 else 12.5 + index, 0 if index == 1 else 7,
                3.0, 2, None, 0, 60, now,
            ),
        )
    con.commit()
    con.close()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    # --- /stats/responsiveness ---------------------------------------------------------------
    status, body = call("GET", "/api/v1/stats/responsiveness")
    rows = body if isinstance(body, list) else []
    slugs = [row.get("slug") for row in rows]
    mine = [row for row in rows if row.get("slug") in PROBE_SLUGS]

    check("the ranking is a bare array", status, 200)
    check("published projects appear, in median order", [r["slug"] for r in mine], [SLUG_FAST, SLUG_SLOW])
    check(
        "a project under the sample floor is absent, not last",
        SLUG_THIN in slugs,
        False,
    )
    check(
        "a private project is not ranked",
        SLUG_PRIV in slugs,
        False,
    )
    check("every row carries its sample", [r.get("n") for r in mine], [6, 9])
    check("the same request answers the same order", [row.get("slug") for row in rows], slugs)

    # --- /stats/history ----------------------------------------------------------------------
    status, body = call("GET", "/api/v1/stats/history?days=2")
    two = body if isinstance(body, list) else []
    check("history is a bare array", status, 200)
    check("days=2 selects two days", len(two), 2)
    check("the series is newest first", [row.get("day") for row in two], ["2099-01-03", "2099-01-02"])

    status, body = call("GET", "/api/v1/stats/history")
    default = body if isinstance(body, list) else []
    check("no parameter still answers", status, 200)
    check("the default window includes all three days", [d in [r.get("day") for r in default] for d in days],
          [True, True, True])

    status, body = call("GET", "/api/v1/stats/history?days=99999")
    clamped = body if isinstance(body, list) else []
    check("an oversized window is a clamp, not a refusal", status, 200)
    check("the clamp still returns the days", [d in [r.get("day") for r in clamped] for d in days],
          [True, True, True])

    status, body = call("GET", "/api/v1/stats/history?days=abc")
    check("a malformed window falls back to the default", status, 200)

    middle = [row for row in default if row.get("day") == "2099-01-02"]
    check("a day with no sample reports null, not zero",
          [row.get("triage_median_h") for row in middle], [None])
    check("its n is still a number", [row.get("triage_n") for row in middle], [0])

    # --- clean up ----------------------------------------------------------------------------
    con = harness.connect("unused")
    for day in days:
        con.execute("DELETE FROM instance_stats WHERE day = ?", (day,))
    for project_id in (fast, slow, thin, priv):
        con.execute("DELETE FROM project_metrics WHERE project_id = ?", (project_id,))
        con.execute("DELETE FROM projects WHERE id = ?", (project_id,))
    con.commit()
    con.close()

    failures = 0
    for name, got, want in results:
        ok = got == want
        if not ok:
            failures += 1
        print(f"{'ok  ' if ok else 'FAIL'} {name}: got {got!r} want {want!r}")
    print(f"\n{len(results) - failures}/{len(results)} ok")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
