#!/usr/bin/env python3
"""Row 11b: the routes nothing was asking for — identity, tags and the members list.

`scripts/parity/routes.py` compares the Go router with the port's and reported **142 of 216 routes** the port does
not answer. That is the finding this probe starts from: every earlier probe asked for the routes whoever wrote it
remembered, so a 404 on `/auth/me` (which EVERY page needs) was invisible.

Four rules are asserted here, because they are the ones where a plausible port is wrong:

1. `/auth/me` answers the caller by id, with `role: "member"` — the Go's value, which is "you are signed in" and
   not this project's role;
2. an anonymous caller gets 401, not an empty object;
3. `/projects/{slug}/tags` resolves the project BEFORE listing tags, so a project the caller may not read is a
   404 and not a 200 with `[]` — a 200 that confirms the slug exists;
4. `/projects/{id}/members` orders by rank and then by id, so the invite list does not read as join order.
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


def register(prefix, stamp):
    status, body = call(
        "POST", "/api/v1/auth/register",
        {"username": f"{prefix}{stamp}", "display_name": prefix.title(), "password": "correct horse"},
    )
    if status != 201 or not isinstance(body, dict):
        print(f"register {prefix} failed: {status} {body}")
        sys.exit(2)
    return body["user"]["id"], body["token"]


def cleanup(con, like):
    for (project,) in con.execute("select id from projects where slug like ?", (like,)).fetchall():
        con.execute("delete from project_tags where project_id = ?", (project,))
        con.execute("delete from members where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.execute("delete from project_tags where tag_id in (select id from tags where name like ?)", (like,))
    con.execute("delete from tags where name like ?", (like,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    owner_id, owner_token = register("panowner", stamp)
    contributor_id, contributor_token = register("pancontrib", stamp)

    con = harness.connect("unused")
    cleanup(con, "probe-panel-%")
    now = time.time()
    public_project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"probe-panel-{stamp}", "Panel probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    private_project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"probe-panel-private-{stamp}", "Private probe", "", "collective", "private", "both", now, now),
    ).lastrowid
    # Two tags, inserted out of alphabetical order so the ORDER BY is the assertion.
    for name in (f"probe-panel-zulu-{stamp}", f"probe-panel-alpha-{stamp}"):
        tag_id = con.execute(
            "INSERT INTO tags (project_id, name, namespace) VALUES (NULL,?,?) RETURNING id", (name, "topic")
        ).lastrowid
        con.execute("INSERT INTO project_tags (project_id, tag_id) VALUES (?,?)", (public_project, tag_id))
    # Three members inserted in reverse rank order, so join order and rank order differ.
    for user_id, role in ((contributor_id, "contributor"), (owner_id, "owner")):
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
            (public_project, user_id, role, now),
        )
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    # 1. /auth/me
    status, body = call("GET", "/api/v1/auth/me", token=owner_token)
    check("auth/me answers", status, 200)
    check("with the caller's id", (body or {}).get("id"), owner_id)
    check("with the display name", (body or {}).get("display_name"), "Panowner")
    check("and the Go's role word", (body or {}).get("role"), "member")
    check("with a created_at", isinstance((body or {}).get("created_at"), (int, float)), True)

    # 2. anonymous
    status, body = call("GET", "/api/v1/auth/me")
    check("an anonymous call is 401", status, 401)

    # 3. project tags, alphabetically, and the visibility rule
    status, body = call("GET", f"/api/v1/projects/probe-panel-{stamp}/tags")
    check("project tags answer without a token (public project)", status, 200)
    check("in alphabetical order", body, [f"probe-panel-alpha-{stamp}", f"probe-panel-zulu-{stamp}"])
    status, body = call("GET", f"/api/v1/projects/probe-panel-private-{stamp}/tags")
    check("a private project's tags are 404, not an empty 200", status, 404)
    status, body = call("GET", f"/api/v1/projects/probe-panel-private-{stamp}/tags", token=owner_token)
    check("and 404 for a signed-in non-member too", status, 404)

    # 4. members: rank then id
    status, body = call("GET", f"/api/v1/projects/{public_project}/members")
    check("members answer", status, 200)
    check("the owner is first even though they joined last", [m["role"] for m in body],
          ["owner", "contributor"])
    check("each row carries the username", body[0]["username"], f"panowner{stamp}")
    check("and the project id", body[0]["project_id"], public_project)
    status, body = call("GET", f"/api/v1/projects/{private_project}/members")
    check("a private project's members are 404 without a token", status, 404)

    cleanup(con, "probe-panel-%")
    con.execute("DELETE FROM api_tokens WHERE user_id IN (?,?)", (owner_id, contributor_id))
    con.execute("DELETE FROM users WHERE id IN (?,?)", (owner_id, contributor_id))
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
