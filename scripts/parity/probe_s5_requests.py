#!/usr/bin/env python3
"""Row 11b part 34: the request board, its answers and its tags.

Four rules, three of them bugs in the service being ported:

1. an INSTANCE-WIDE request (`project_id` NULL) is public by construction and readable with no token — the unported code
   called GetProjectByID(0) and answered 404, so the whole feature was dead for exactly the requests it exists to serve;
2. `project_id` is OMITTED from the JSON when the request is instance-wide, so a client can tell "instance-wide" from
   "project 0" without the key ever appearing;
3. an answer's ranking is a GLICKO RATING (`elo_r DESC, id`), not a vote score — the table has `proposed_by` and
   `elo_r/elo_rd/elo_vol`, and no `author_id` and no `score`;
4. a project-scoped request on a private project is 404 without a token, and a deleted project's request is 404 too.
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


def cleanup(con, like, instance_ids):
    """Remove every row this probe could have written, whatever order the FKs demand.

    The answers are deleted by BOTH routes they hang off — as a request's answers and as a project's answers — because
    `request_answers` has FKs to both tables, and a rival project answering another project's request is exactly the
    shape the fixture builds.
    """
    projects = [row[0] for row in con.execute("select id from projects where slug like ?", (like,)).fetchall()]
    if projects:
        marks = ",".join("?" * len(projects))
        params = tuple(projects)
        con.execute(
            f"delete from request_answers where project_id in ({marks}) or request_id in"
            f" (select id from requests where project_id in ({marks}))",
            params + params,
        )
        con.execute(
            f"delete from request_tags where request_id in"
            f" (select id from requests where project_id in ({marks}))",
            params,
        )
        # The tags this fixture wrote are project-scoped, and `tags` has an FK to `projects`: remove the join rows, then
        # the tags, before the project can go.
        con.execute(
            f"delete from request_tags where tag_id in (select id from tags where project_id in ({marks}))",
            params,
        )
        con.execute(f"delete from tags where project_id in ({marks})", params)
        con.execute(f"delete from requests where project_id in ({marks})", params)
        con.execute(f"delete from audit_log where project_id in ({marks})", params)
        con.execute(f"delete from projects where id in ({marks})", params)
    for request_id in instance_ids:
        con.execute("delete from request_answers where request_id = ?", (request_id,))
        con.execute("delete from request_tags where request_id = ?", (request_id,))
        con.execute("delete from requests where id = ?", (request_id,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    slug = f"probe-requests-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"rqauth{stamp}", "display_name": "Requests", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-requests-%", [])
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Requests probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    # `request_answers` carries UNIQUE (request_id, project_id): an answer is A PROJECT'S answer to a request, one per
    # project — which is exactly what makes the cross-project comparison the rating exists for. So the second answer
    # has to come from a second project, and no fixture can put two answers from one project on one request.
    rival = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-rival", "Rival", "", "collective", "public", "both", now, now),
    ).lastrowid
    empty_project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-empty", "Empty", "", "collective", "public", "both", now, now),
    ).lastrowid

    def request(project_id, title, created_at):
        return con.execute(
            "INSERT INTO requests (project_id, author_id, title, body, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?) RETURNING id",
            (project_id, author, title, "body", "open", created_at, created_at),
        ).lastrowid

    older = request(project, "Older request", now - 100)
    newer = request(project, "Newer request", now - 10)
    instance = request(None, "Instance-wide request", now - 5)
    con.commit()

    def answer(request_id, project_id, body, elo_r):
        return con.execute(
            "INSERT INTO request_answers (request_id, project_id, body, status, elo_r, elo_rd, elo_vol,"
            " proposed_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
            (request_id, project_id, body, "proposed", elo_r, 200.0, 0.06, author, now, now),
        ).lastrowid

    weak = answer(newer, project, "weaker answer", 1400.0)
    strong = answer(newer, rival, "stronger answer", 1700.0)
    tag_ids = []
    for name in ("zebra", "alpha"):
        tag_ids.append(con.execute(
            "INSERT INTO tags (project_id, name, namespace) VALUES (?,?,?) RETURNING id",
            (project, f"{name}{stamp}", "general"),
        ).lastrowid)
    for tag_id in tag_ids:
        con.execute("INSERT INTO request_tags (request_id, tag_id) VALUES (?,?)", (newer, tag_id))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{project}/requests")
    check("a project's requests answer", status, 200)
    body = body or []
    check("newest first", [row["title"] for row in body], ["Newer request", "Older request"])
    check("a project's list excludes the instance board", instance not in [row["id"] for row in body], True)

    status, body = call("GET", f"/api/v1/projects/{empty_project}/requests")
    check("a project with no requests answers an empty array", (status, body), (200, []))

    status, body = call("GET", "/api/v1/requests")
    check("the instance board answers", status, 200)
    ids = [row["id"] for row in body or []]
    check("with no token, and only instance-wide rows", ids.count(instance), 1)
    check("and no project in the path", instance in ids, True)
    board_row = next(row for row in body if row["id"] == instance)
    check("an instance-wide row OMITS project_id", "project_id" in board_row, False)

    status, body = call("GET", f"/api/v1/requests/{instance}")
    check("an instance-wide request is readable with NO token", status, 200)
    check("and says what it is", (body or {}).get("title"), "Instance-wide request")

    status, body = call("GET", f"/api/v1/requests/{newer}")
    check("a scoped request on a public project is readable", status, 200)
    check("and carries its project_id", (body or {}).get("project_id"), project)
    check("with no accepted answer, the key is absent", "accepted_answer_id" in (body or {}), False)

    status, body = call("GET", f"/api/v1/requests/{newer}/answers")
    check("answers answer", status, 200)
    check("ordered by RATING, strongest first", [row["id"] for row in body or []], [strong, weak])
    check("an answer names who proposed it", (body or [{}])[0].get("proposed_by"), author)

    status, body = call("GET", f"/api/v1/requests/{older}/answers")
    check("a request with no answers answers an empty array", (status, body), (200, []))

    status, body = call("GET", f"/api/v1/requests/{newer}/tags")
    check("tags answer", status, 200)
    check("alphabetically, by name", [name.rstrip("0123456789") for name in body or []], ["alpha", "zebra"])

    status, _ = call("GET", "/api/v1/requests/not-a-number")
    check("a non-numeric request id is 400", status, 400)
    status, _ = call("GET", "/api/v1/requests/99999999")
    check("a missing request is 404", status, 404)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    status, _ = call("GET", f"/api/v1/requests/{newer}")
    check("a request on a private project is 404 without a token", status, 404)
    status, _ = call("GET", f"/api/v1/requests/{instance}")
    check("but an instance-wide request stays readable", status, 200)

    cleanup(con, "probe-requests-%", [instance])
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
