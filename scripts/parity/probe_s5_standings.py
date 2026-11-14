#!/usr/bin/env python3
"""Row 11b part 31: project solution standings — grouped per feature, and the four features nobody proposed for.

Two rules that read as polish and are not:
1. standings are NOT merged across features: a solution's score is computed against the entries competing with it, so a
   project-wide ranking compares numbers that were never comparable;
2. a feature with no solutions is INCLUDED with an empty array and counted — dropping it makes one feature read as the
   whole project.

Plus the truncation rule: five shown, and `omitted` from an explicit count rather than from a seventh row.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, os.path.join(ROOT, "tests", "e2e"))

# ── consent ──────────────────────────────────────────────────────────────────────────────────────────────────────
# Every mutation on this instance must carry the accepted cookie notice: the server refuses with 403 `consent_required`
# otherwise, and that is the reference's behaviour, not a detail of this probe. Rather than thread the header through every call
# below, the probe carries it the way a browser does.
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


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {}
    if data is not None:
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            raw = response.read()
            return response.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, json.loads(raw) if raw else None
        except ValueError:
            return error.code, raw.decode("utf-8", "replace")


def cleanup(con, like):
    for (project,) in con.execute("select id from projects where slug like ?", (like,)).fetchall():
        con.execute("delete from pairwise_votes where project_id = ?", (project,))
        con.execute("delete from arena_entries where arena_id in"
                    " (select id from arenas where project_id = ?)", (project,))
        con.execute("delete from arenas where project_id = ?", (project,))
        con.execute("delete from solutions where feature_id in"
                    " (select id from features where project_id = ?)", (project,))
        con.execute("delete from features where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    slug = f"probe-standings-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"stdauth{stamp}", "display_name": "Standings", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-standings-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Standings probe", "", "collective", "public", "both", now, now),
    ).lastrowid

    def feature(title):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?) RETURNING id",
            (project, author, title, "proposed", now, now),
        ).lastrowid

    with_some = feature("Has proposals")
    with_none = feature("Nobody proposed")

    arena = con.execute(
        "INSERT INTO arenas (type, project_id, feature_id, question, use_case, created_at)"
        " VALUES (?,?,?,?,?,?) RETURNING id",
        ("solution", project, with_some, "Which solves it?", "call", now),
    ).lastrowid

    def solution(index):
        solution_id = con.execute(
            "INSERT INTO solutions (feature_id, author_id, title, body, type, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?,?) RETURNING id",
            (with_some, author, f"Proposal {index}", "body", "code", "proposed", now, now),
        ).lastrowid
        con.execute(
            "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games, is_baseline,"
            " updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
            (arena, "solution", solution_id, 1500.0 + index, 100.0, 0.06, 3, 0, now),
        )
        return solution_id

    created = [solution(index) for index in range(7)]
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{project}/solutions")
    check("the panel answers", status, 200)
    body = body or {}
    check("every feature is present, including the one nobody proposed for",
          sorted(row["feature_id"] for row in body.get("features") or []), sorted([with_some, with_none]))
    check("the count of features with solutions", body.get("features_with_solutions"), 1)
    check("and the count without", body.get("features_without"), 1)
    rows = {row["feature_id"]: row for row in body.get("features") or []}
    # `.get(..., {})` on every lookup: a control that removes a feature must report a FAIL line, not a traceback. A
    # probe that crashes on the failure it exists to detect reports nothing.
    busy = rows.get(with_some, {})
    idle = rows.get(with_none, {})
    check("five are shown for the busy feature", busy.get("shown"), 5)
    check("and two are omitted", busy.get("omitted"), 2)
    check("the array length follows 'shown'", len(busy.get("solutions") or []), 5)
    check("the idle feature answers an empty ARRAY and not null", idle.get("solutions"), [])
    check("with nothing omitted", idle.get("omitted"), 0)
    check("solutions are not merged into one project-wide list",
          all(row["feature_id"] == with_some or row["solutions"] == [] for row in body["features"]), True)
    titles = {row["solutions"][i]["title"] for row in body["features"] for i in range(len(row["solutions"]))}
    check("the standings carry proposal titles so the panel can say which is which",
          all(title.startswith("Proposal ") for title in titles), True)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    status, _ = call("GET", f"/api/v1/projects/{project}/solutions")
    check("a private project's standings are 404 without a token", status, 404)

    cleanup(con, "probe-standings-%")
    con.execute("DELETE FROM api_tokens WHERE user_id = ?", (author,))
    con.execute("DELETE FROM users WHERE id = ?", (author,))
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
