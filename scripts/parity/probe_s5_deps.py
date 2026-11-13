#!/usr/bin/env python3
"""Row 11b part 17: the dependency panel — three lists, two directions, one status predicate.

A port that swaps the two join columns is plausible on a feature nobody blocks and wrong everywhere else, so the
fixture is asymmetric: A is blocked by B (still in progress) and by C (shipped). Then

  blocked_by    = [B, C]      both, in id order
  blocks        = [A]         on B and on C, but NOT on A
  open_blockers = [B]         C shipped, so the page must not claim A is blocked by it

`open_blockers` is a STATUS PREDICATE and not a row property: deriving it in the client would ship the status
vocabulary there and report a shipped feature as a blocker the day a terminal status is added.
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
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
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
        con.execute("delete from feature_deps where blocked_id in"
                    " (select id from features where project_id = ?)", (project,))
        con.execute("delete from feature_deps where blocker_id in"
                    " (select id from features where project_id = ?)", (project,))
        con.execute("delete from features where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    slug = f"probe-deps-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"depsauth{stamp}", "display_name": "Deps", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-deps-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Deps probe", "", "collective", "public", "both", now, now),
    ).lastrowid

    def feature(title, state):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?) RETURNING id",
            (project, author, title, state, now, now),
        ).lastrowid

    blocked = feature("Blocked", "proposed")
    open_blocker = feature("Still building", "in_progress")
    shipped = feature("Already shipped", "shipped")
    con.execute("INSERT INTO feature_deps (blocker_id, blocked_id) VALUES (?,?)", (open_blocker, blocked))
    con.execute("INSERT INTO feature_deps (blocker_id, blocked_id) VALUES (?,?)", (shipped, blocked))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{slug}/features/{blocked}/deps")
    check("the panel answers", status, 200)
    body = body or {}
    check("blocked_by lists both blockers in id order",
          [r["id"] for r in body.get("blocked_by", [])], [open_blocker, shipped])
    check("open_blockers drops the shipped one",
          [r["id"] for r in body.get("open_blockers", [])], [open_blocker])
    check("blocks is empty for the blocked feature", body.get("blocks"), [])
    check("each ref carries a title and a status",
          body["blocked_by"][0]["title"], "Still building")

    status, body = call("GET", f"/api/v1/projects/{slug}/features/{open_blocker}/deps")
    check("the blocker's own panel answers", status, 200)
    check("nothing blocks it", (body or {}).get("blocked_by"), [])
    check("and it blocks exactly the one feature", [r["id"] for r in (body or {}).get("blocks", [])], [blocked])

    status, body = call("GET", f"/api/v1/projects/{slug}/features/{shipped}/deps")
    check("the shipped feature blocks but has no open blockers",
          ((body or {}).get("open_blockers"), [r["id"] for r in (body or {}).get("blocks", [])]),
          ([], [blocked]))

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    status, _ = call("GET", f"/api/v1/projects/{slug}/features/{blocked}/deps")
    check("a private project's deps are 404 without a token", status, 404)

    cleanup(con, "probe-deps-%")
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
