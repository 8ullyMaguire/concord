#!/usr/bin/env python3
"""Row 11b part 32: one feature's solution standings.

Three rules, and the second is where two families of routes in the same service disagree:
1. a feature nobody has proposed for answers an EMPTY ARRAY, not 404 — a new feature's normal state;
2. a feature belonging to ANOTHER PROJECT answers 403 on this family (ErrPerm), while the complaint family answers 404
   for the same situation. The port reproduces each family's choice instead of harmonising them;
3. `limit` defaults to 100 and only a positive integer overrides it; a junk limit falls back to the default rather than
   erroring.
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
    slug = f"probe-featsol-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"fsauth{stamp}", "display_name": "Feature solutions", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-featsol-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Feature solutions probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    other = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-other", "Other project", "", "collective", "public", "both", now, now),
    ).lastrowid

    def feature(project_id, title):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?) RETURNING id",
            (project_id, author, title, "proposed", now, now),
        ).lastrowid

    designed = feature(project, "Designed")
    idle = feature(project, "Idle")
    foreign = feature(other, "Foreign")

    arena = con.execute(
        "INSERT INTO arenas (type, project_id, feature_id, question, use_case, created_at)"
        " VALUES (?,?,?,?,?,?) RETURNING id",
        ("solution", project, designed, "Which solves it?", "call", now),
    ).lastrowid

    def solution(index):
        solution_id = con.execute(
            "INSERT INTO solutions (feature_id, author_id, title, body, type, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?,?) RETURNING id",
            (designed, author, f"Proposal {index}", "body", "code", "proposed", now, now),
        ).lastrowid
        con.execute(
            "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games, is_baseline,"
            " updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
            (arena, "solution", solution_id, 1500.0 + index, 100.0, 0.06, 2, 0, now),
        )
        return solution_id

    for index in range(3):
        solution(index)
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{project}/features/{designed}/solutions")
    check("the standings answer", status, 200)
    body = body or {}
    check("the feature id is echoed", body.get("feature_id"), designed)
    check("all three proposals are listed", len(body.get("solutions") or []), 3)
    check("each carries its title", all(row.get("title", "").startswith("Proposal ")
                                        for row in body.get("solutions") or []), True)

    status, body = call("GET", f"/api/v1/projects/{project}/features/{idle}/solutions")
    check("a feature nobody proposed for answers 200", status, 200)
    check("with an empty ARRAY and not null", (body or {}).get("solutions"), [])

    status, body = call("GET", f"/api/v1/projects/{project}/features/{designed}/solutions?limit=1")
    check("limit=1 returns one", len((body or {}).get("solutions") or []), 1)
    status, body = call("GET", f"/api/v1/projects/{project}/features/{designed}/solutions?limit=junk")
    check("a junk limit falls back to the default rather than erroring", status, 200)
    check("and still lists everything", len((body or {}).get("solutions") or []), 3)
    status, body = call("GET", f"/api/v1/projects/{project}/features/{designed}/solutions?limit=0")
    check("limit=0 is not a limit of zero", len((body or {}).get("solutions") or []), 3)

    # The family disagreement, pinned: 403 here, 404 in the complaint routes.
    status, _ = call("GET", f"/api/v1/projects/{project}/features/{foreign}/solutions")
    check("another project's feature is 403 on this family", status, 403)

    status, _ = call("GET", f"/api/v1/projects/{project}/features/99999999/solutions")
    check("a missing feature is 404", status, 404)
    status, _ = call("GET", f"/api/v1/projects/{project}/features/not-a-number/solutions")
    check("a non-numeric feature id is 400", status, 400)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    status, _ = call("GET", f"/api/v1/projects/{project}/features/{designed}/solutions")
    check("a private project's standings are 404 without a token", status, 404)

    cleanup(con, "probe-featsol-%")
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
