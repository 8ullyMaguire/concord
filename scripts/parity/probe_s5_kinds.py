#!/usr/bin/env python3
"""Row 11b part 16: two small reads — the document kinds and a feature's tags.

Both look like one-line lookups, and both have a rule that decides whether the answer is honest:

1. `documents/kinds` is the SCHEMA's vocabulary, not `select distinct kind`. A distinct query lets one typo in one
   row invent a kind, and the writer then validates against the typo.
2. the tags route resolves the FEATURE inside the project before listing, so a feature id from another project is
   a 404 and not a list of somebody else's tags.
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
        con.execute("delete from feature_tags where feature_id in"
                    " (select id from features where project_id = ?)", (project,))
        con.execute("delete from features where project_id = ?", (project,))
        con.execute("delete from project_tags where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.execute("delete from tags where name like ?", (like,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"kindauth{stamp}", "display_name": "Kinds", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]
    prefix = f"probe-kinds-{stamp}"
    con = harness.connect("unused")
    cleanup(con, f"probe-kinds-{stamp}%")
    now = time.time()
    first = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (prefix, "Kinds probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    second = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (prefix + "-other", "Other", "", "collective", "public", "both", now, now),
    ).lastrowid
    # A feature in each project, so the cross-project lookup has something to refuse.
    mine = con.execute(
        "INSERT INTO features (project_id, author_id, title, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?)"
        " RETURNING id",
        (first, author, "Mine", "proposed", now, now),
    ).lastrowid
    theirs = con.execute(
        "INSERT INTO features (project_id, author_id, title, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?)"
        " RETURNING id",
        (second, author, "Theirs", "proposed", now, now),
    ).lastrowid
    # Three tags attached out of alphabetical order.
    for name in (prefix + "-zulu", prefix + "-alpha", prefix + "-mike"):
        tag = con.execute(
            "INSERT INTO tags (project_id, name, namespace) VALUES (NULL,?,?) RETURNING id", (name, "topic")
        ).lastrowid
        con.execute("INSERT INTO feature_tags (feature_id, tag_id) VALUES (?,?)", (mine, tag))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{prefix}/documents/kinds")
    check("kinds answer", status, 200)
    check("as the schema's vocabulary, in its order", (body or {}).get("kinds"),
          ["readme", "spec", "plan", "wiki", "adr", "changelog", "scout"])

    status, body = call("GET", f"/api/v1/projects/{prefix}/features/{mine}/tags")
    check("feature tags answer", status, 200)
    check("keyed by the feature", (body or {}).get("feature_id"), mine)
    check("alphabetically", (body or {}).get("tags"),
          [prefix + "-alpha", prefix + "-mike", prefix + "-zulu"])

    status, _ = call("GET", f"/api/v1/projects/{prefix}/features/{theirs}/tags")
    check("another project's feature is a 404, not its tags", status, 404)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (first,))
    con.commit()
    status, _ = call("GET", f"/api/v1/projects/{prefix}/documents/kinds")
    check("kinds are 404 for a project the caller cannot read", status, 404)

    cleanup(con, f"probe-kinds-{stamp}%")
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
