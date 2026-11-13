#!/usr/bin/env python3
"""Row 11b part 33: one solution, with its rating and its coverage claims.

Four rules:
1. a solution not yet in an arena is still a solution: LEFT JOIN, rating zeros, arena_id 0 — an inner join would 404 a
   proposal that exists;
2. `is_baseline` comes from the solution's TYPE (`do-nothing`), not from the arena entry's flag, so the two cannot
   disagree;
3. coverage claims are ordered by complaint id, and an unchallenged claim's `contested_by` / `contested_at` /
   `contest_reason` are NULL;
4. a solution belonging to ANOTHER FEATURE of the same project is 404, while a feature belonging to another project is
   403 — two sizes of "not yours" inside one route family, both reproduced.
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
        con.execute("delete from solution_coverage where complaint_id in"
                    " (select id from complaints where project_id = ?)", (project,))
        con.execute("delete from arena_entries where arena_id in"
                    " (select id from arenas where project_id = ?)", (project,))
        con.execute("delete from arenas where project_id = ?", (project,))
        con.execute("delete from solutions where feature_id in"
                    " (select id from features where project_id = ?)", (project,))
        con.execute("delete from complaints where project_id = ?", (project,))
        con.execute("delete from features where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    slug = f"probe-solution-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"solauth{stamp}", "display_name": "Solution", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-solution-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Solution probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    other = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-other", "Other", "", "collective", "public", "both", now, now),
    ).lastrowid

    def feature(project_id, title):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?) RETURNING id",
            (project_id, author, title, "proposed", now, now),
        ).lastrowid

    designed = feature(project, "Designed")
    sibling = feature(project, "Sibling feature")
    foreign = feature(other, "Foreign")

    complaint_high = con.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, author, "Second complaint", "", "open", now, now),
    ).lastrowid
    complaint_low = con.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, author, "First complaint", "", "open", now, now),
    ).lastrowid

    def solution(feature_id, title, kind="code", in_arena=True, baseline_flag=0):
        solution_id = con.execute(
            "INSERT INTO solutions (feature_id, author_id, title, body, type, relationship, status,"
            " expertise_tags, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
            (feature_id, author, title, "body", kind, "exclusive", "proposed", "", now, now),
        ).lastrowid
        if in_arena:
            con.execute(
                "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games,"
                " is_baseline, updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
                (arena, "solution", solution_id, 1620.0, 90.0, 0.06, 4, baseline_flag, now),
            )
        return solution_id

    arena = con.execute(
        "INSERT INTO arenas (type, project_id, feature_id, question, use_case, created_at)"
        " VALUES (?,?,?,?,?,?) RETURNING id",
        ("solution", project, designed, "Which solves it?", "call", now),
    ).lastrowid

    rated = solution(designed, "Rated proposal")
    unrated = solution(designed, "Not in an arena", in_arena=False)

    # Claims, inserted high-id first so ORDER BY complaint_id is a real constraint rather than insertion order.
    for complaint_id in (complaint_high, complaint_low):
        con.execute(
            "INSERT INTO solution_coverage (solution_id, complaint_id, claim, contested, created_at)"
            " VALUES (?,?,?,?,?)",
            (rated, complaint_id, f"fixes {complaint_id}", 0, now),
        )
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{project}/features/{designed}/solutions/{rated}")
    check("the proposal answers", status, 200)
    body = body or {}
    sol = body.get("solution") or {}
    check("its title travels", sol.get("title"), "Rated proposal")
    check("its type is under 'type'", sol.get("type"), "code")
    check("its rating is joined in", (sol.get("rating") or {}).get("r"), 1620.0)
    check("and the conservative score is derived", (sol.get("rating") or {}).get("conservative"), 1440.0)
    check("the rating names its entity type", (sol.get("rating") or {}).get("entity_type"), "solution")
    check("is_baseline is false for a code proposal", body.get("is_baseline"), False)
    check("both claims are listed", len(body.get("coverage") or []), 2)
    check("ordered by complaint id", [c["complaint_id"] for c in body.get("coverage") or []],
          sorted([complaint_high, complaint_low]))
    check("an unchallenged claim's contestant is NULL",
          (body.get("coverage") or [{}])[0].get("contested_by"), None)
    check("and its reason is NULL", (body.get("coverage") or [{}])[0].get("contest_reason"), None)

    status, body = call("GET", f"/api/v1/projects/{project}/features/{designed}/solutions/{unrated}")
    check("a proposal not in an arena still answers", status, 200)
    unrated_rating = ((body or {}).get("solution") or {}).get("rating") or {}
    check("with zeroed rating", (unrated_rating.get("r"), unrated_rating.get("rd")), (0.0, 0.0))
    check("and no arena id", unrated_rating.get("arena_id"), 0)
    check("and no claims", (body or {}).get("coverage"), [])

    # A do-nothing proposal is the baseline by TYPE, whatever the arena entry says.
    baseline_solution = solution(designed, "Do nothing", kind="do-nothing", baseline_flag=0)
    con.commit()
    status, body = call("GET", f"/api/v1/projects/{project}/features/{designed}/solutions/{baseline_solution}")
    check("a do-nothing proposal is the baseline by its type", (body or {}).get("is_baseline"), True)

    status, _ = call("GET", f"/api/v1/projects/{project}/features/{sibling}/solutions/{rated}")
    check("a solution belonging to another feature is 404", status, 404)
    status, _ = call("GET", f"/api/v1/projects/{project}/features/{foreign}/solutions/{rated}")
    check("a feature from another project is 403", status, 403)
    status, _ = call("GET", f"/api/v1/projects/{project}/features/{designed}/solutions/99999999")
    check("a missing solution is 404", status, 404)

    cleanup(con, "probe-solution-%")
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
