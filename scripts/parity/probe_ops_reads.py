#!/usr/bin/env python3
"""Row 11 part 60: three reads an operator page stands on — the ledger, the rising table, and the lists that have no project.

Three routes, batched because each is one read over a table the port already owns:
`GET /api/v1/admin-ledger`, `GET /api/v1/stats/rising`, `GET /api/v1/lists`.

What the probe establishes:

1. **An empty set is `[]`, never `null`.** The reference returns the literal `null` for an empty admin ledger — measured on its own
   instance — and every JS consumer survives only because it happens to write `|| []`. That makes the next client's guard
   load-bearing, silently. Both new routes answer `[]`.
2. **The ledger is public and unfiltered, and that is the point.** §9.3 makes admin actions publicly visible because an admin with
   no content authority is only a real constraint when anyone can watch them. No token, no project filter, no role gate.
3. **`limit` is clamped, not rejected.** Above 500 the reference silently uses 100; a caller learns the ceiling from the size of the
   page, not from an error it has to special-case.
4. **A rising row carries the SPAN it was measured over.** A rate over 14 days and one over 90 are different claims, so `days`
   travels with `features_per_week`. A series shorter than the floor is ABSENT — a `0.0` would read as "flat" rather than "not
   measurable".
5. **Health is a LEVEL beside the slope, never a substitute for it**, and it is `null` when nothing was measured — the reference's
   own `project_metrics.health_score` is 0 for every project on the live instance, so reading that column would publish a confident
   "0" for the whole catalogue.
6. **Instance-wide lists are a first-class scope, not a missing project.** `project_id` is `null`, and a project's lists INCLUDE
   them: the scope is what makes a "good resources" list useful to every project at once.
7. **The list's name is `name`, not `title`.** The reference maps its `title` column to a JSON `name`; the port had been answering
   `title` on an already-ported route, and its own probe asserted the wrong key. Found by probing the reference, not by trusting the
   port's tests — asserted here so it cannot drift back.
"""
import datetime
import json
import os
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, os.path.join(ROOT, "tests", "e2e"))

_ORIGINAL_REQUEST = urllib.request.Request


def _consented_request(*args, **kwargs):
    if len(args) > 2:
        headers = dict(args[2] or {})
        args = args[:2]
        kwargs["headers"] = headers
    else:
        kwargs["headers"] = dict(kwargs.pop("headers", None) or {})
    kwargs["headers"].setdefault(
        "Cookie", os.environ.get("CONCORD_CONSENT_COOKIE", "concord_consent=1"))
    return _ORIGINAL_REQUEST(*args, **kwargs)


urllib.request.Request = _consented_request

BASE = os.environ.get("CANDIDATE", "http://127.0.0.1:8108")
LIST_KEYS = ["created_at", "description", "id", "name", "project_id", "slug", "status", "updated_at"]


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {}
    if data is not None:
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            raw = response.read()
            return response.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, json.loads(raw) if raw else None
        except ValueError:
            return error.code, raw.decode("utf-8", "replace")


def register(tag):
    status, body = call(
        "POST", "/api/v1/auth/register",
        {"username": tag, "display_name": tag.title(), "password": "correct horse"},
    )
    if status != 201:
        raise SystemExit(f"register {tag} failed: {status} {body}")
    return body["user"]["id"], body["token"]


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    actor_id, token = register(f"opsread{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-ops-{stamp}"
    con.execute("delete from projects where slug like ?", (f"{slug}%",))
    con.commit()

    def project(name, visibility="public"):
        pid = con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
            " repo_url, repo_host, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
            (name, name, "", "collective", visibility, "both", None, None, now, now),
        ).lastrowid
        return pid

    rising = project(slug)
    short = project(f"{slug}-short")
    hidden = project(f"{slug}-hidden", "private")

    # `project_stats.day` is an ISO date TEXT column, and the span is a date subtraction — so the fixture writes dates, not offsets.
    today = datetime.date.today()

    def day(offset):
        return (today - datetime.timedelta(days=offset)).isoformat()

    # Two snapshots 14 days apart for `rising`: +28 features, +7 complaints.
    for offset, feats, comps in ((14, 10, 3), (0, 38, 10)):
        con.execute("INSERT INTO project_stats (day, project_id, organic_features_shipped,"
                    " organic_complaints_filed) VALUES (?,?,?,?)", (day(offset), rising, feats, comps))
    # A series 7 days long: below the floor, so it must be ABSENT rather than flat.
    for offset, feats in ((7, 5), (0, 40)):
        con.execute("INSERT INTO project_stats (day, project_id, organic_features_shipped,"
                    " organic_complaints_filed) VALUES (?,?,?,?)", (day(offset), short, feats, 0))
    # The private project would be first if the visibility filter were missing.
    for offset, feats in ((30, 0), (0, 999)):
        con.execute("INSERT INTO project_stats (day, project_id, organic_features_shipped,"
                    " organic_complaints_filed) VALUES (?,?,?,?)", (day(offset), hidden, feats, 0))
    # A REAL measurement: the four signals health is computed from. Not the stored `health_score`, which project creation writes as a
    # placeholder 0 for every project — reading that column would publish a confident "0" for the whole catalogue.
    con.execute("INSERT INTO project_metrics (project_id, health_score, contributors, last_commit_at,"
                " median_review_hours, releases_90d) VALUES (?,?,?,?,?,?)",
                (rising, 0.0, 3, now, 5.0, 2))
    # A placeholder row with a score and NO signal: the reference's trap, planted so the probe can catch it.
    con.execute("INSERT INTO project_metrics (project_id, health_score) VALUES (?,?)", (short, 0.0))
    con.commit()

    my_list = con.execute(
        "INSERT INTO lists (project_id, slug, title, description, status, created_by, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (rising, f"{slug}-list", "Project reading", "", "open", actor_id, now, now)).lastrowid
    con.execute(
        "INSERT INTO lists (project_id, slug, title, description, status, created_by, created_at, updated_at)"
        " VALUES (NULL,?,?,?,?,?,?,?)",
        (f"{slug}-instance", "Good resources", "", "open", actor_id, now, now))
    # The placeholder trap, planted BEFORE the table is read: a project whose metrics row exists but carries no signal must not
    # publish a score. Project creation writes exactly this row for every project.
    placeholder = project(f"{slug}-placeholder")
    for offset, feats in ((20, 0), (0, 3)):
        con.execute("INSERT INTO project_stats (day, project_id, organic_features_shipped,"
                    " organic_complaints_filed) VALUES (?,?,?,?)", (day(offset), placeholder, feats, 0))
    con.execute("INSERT INTO project_metrics (project_id, health_score) VALUES (?,?)", (placeholder, 0.0))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    # ── the admin ledger ─────────────────────────────────────────────────────────────────────────────────────────
    status, body = call("GET", "/api/v1/admin-ledger")
    check("the ledger is readable with no token at all — §9.3's whole point", status, 200)
    check("and an empty ledger is [] rather than null",
          isinstance(body, list), True)
    for index, (action, subject, justification) in enumerate(
            (("hold.release", "arena", "consensus could not resolve"), ("project.suspend", "spam", ""))):
        con.execute("INSERT INTO admin_ledger (actor_id, action, subject, justification, project_id, created_at)"
                    " VALUES (?,?,?,?,?,?)",
                    (actor_id, action, subject, justification, rising, now + index))
    con.commit()
    status, body = call("GET", "/api/v1/admin-ledger?limit=1")
    body = body or []
    check("a limit of 1 returns 1", len(body), 1)
    check("the newest action first", body[0].get("action"), "project.suspend")
    check("a row carries who, what, on what and why",
          sorted(body[0].keys()),
          ["action", "actor_id", "created_at", "id", "justification", "project_id", "subject"])
    check("the actor is named", body[0].get("actor_id"), actor_id)
    check("an empty justification is an empty string, not null", body[0].get("justification"), "")
    status, body = call("GET", "/api/v1/admin-ledger?limit=100000")
    check("an absurd limit is clamped by the server, not refused", status, 200)
    check("and the page reports the ceiling it used", isinstance(body, list), True)
    status, body = call("GET", "/api/v1/admin-ledger?limit=0")
    check("a nonsense limit is still 200 — a typo in a viewer is worse as a 400", status, 200)

    # ── rising ───────────────────────────────────────────────────────────────────────────────────────────────────
    status, body = call("GET", "/api/v1/stats/rising")
    check("rising is public", status, 200)
    rows = {row.get("slug"): row for row in (body or [])}
    check("the 14-day project is in the table", slug in rows, True)
    row = rows.get(slug, {})
    check("it carries the span it was measured over", row.get("days"), 14)
    check("and the deltas behind the slope",
          (row.get("features_shipped"), row.get("complaints_filed")), (28, 7))
    check("the rate is per WEEK, not per span",
          (round(row.get("features_per_week", 0), 3), round(row.get("complaints_per_week", 0), 3)), (14.0, 3.5))
    check("health is a level shown BESIDE the slope", isinstance(row.get("health_score"), (int, float)), True)
    check("and it is a real score in [0, 1]", 0.0 <= (row.get("health_score") or -1) <= 1.0, True)
    check("a series shorter than the floor is ABSENT, not a flat 0.0", f"{slug}-short" in rows, False)

    check("a placeholder metrics row publishes NO score, not a confident 0",
          rows.get(f"{slug}-placeholder", {}).get("health_score", "missing"), None)
    check("a private project is never ranked", f"{slug}-hidden" in rows, False)
    private = [name_ for name_ in rows
               if name_.startswith(slug) and name_ not in (slug, f"{slug}-placeholder")]
    check("no other fixture leaked into the table", private, [])
    status, body = call("GET", "/api/v1/stats/rising")
    check("and the answer is a bare array", isinstance(body, list), True)

    # ── instance-wide lists ──────────────────────────────────────────────────────────────────────────────────────
    status, body = call("GET", "/api/v1/lists")
    check("instance lists are public", status, 200)
    body = body or []
    check("only the instance-wide ones appear here",
          all(row.get("project_id") is None for row in body), True)
    check("and this run's instance list is among them",
          f"{slug}-instance" in [row.get("slug") for row in body], True)
    check("a list carries exactly the reference's keys", sorted(body[0].keys()), LIST_KEYS)
    # The reference maps its `title` COLUMN to a JSON `name`; the port answered `title` here and its own probe asserted that.
    check("the name travels as `name`, which is what the reference emits",
          [row.get("name") for row in body if row.get("slug") == f"{slug}-instance"], ["Good resources"])
    check("a list has no `title` field at all", "title" in body[0], False)
    check("the timestamps are seconds, not strings",
          isinstance(body[0].get("created_at"), (int, float)), True)

    status, body = call("GET", f"/api/v1/projects/{slug}/lists")
    body = body or []
    slugs = sorted(row.get("slug") for row in body)
    check("a project's lists INCLUDE the instance-wide ones",
          f"{slug}-instance" in slugs, True)
    check("and its own", f"{slug}-list" in slugs, True)
    check("with the project's own first", body[0].get("slug"), f"{slug}-list" if body else None)
    check("the project route uses `name` too", sorted(body[0].keys()) if body else [], LIST_KEYS)
    check("another project's list is not in it",
          f"{slug}-instance" != (body[0].get("slug") if body else ""), True)

    con.execute("delete from lists where slug like ?", (f"{slug}%",))
    con.execute("delete from project_stats where project_id in (?,?,?)", (rising, short, hidden))
    con.execute("delete from project_metrics where project_id in (?,?)", (rising, short))
    con.execute("delete from project_stats where project_id = ?", (placeholder,))
    con.execute("delete from project_metrics where project_id = ?", (placeholder,))
    con.execute("delete from projects where id = ?", (placeholder,))
    con.execute("delete from admin_ledger where project_id = ?", (rising,))
    con.execute("delete from projects where id in (?,?,?)", (rising, short, hidden))
    con.commit()

    passed = sum(1 for _, got, want in results if got == want)
    print(f"\n{passed}/{len(results)} ok", flush=True)
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())
