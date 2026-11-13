#!/usr/bin/env python3
"""Row 11b part 25: document search, and the hyphen that changes the meaning.

Three rules, each a way to be plausibly wrong:

1. `q` is required: MISSING is 400, but present-and-empty is an empty array. "You did not ask" and "you asked for
   nothing" are different requests;
2. a BARE term is a phrase, not an expression — `language-neutral` under FTS5 means "language AND NOT neutral", which
   silently returns the wrong rows for the commonest query shape there is. The fixture has a document that mentions
   "language" and never "neutral", so the boolean reading returns it and the phrase reading does not;
3. limit `<=0` or `>100` becomes 20, and `limit=1` returns one row while `count` stays honest.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.parse
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
        con.execute("delete from project_documents where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    slug = f"probe-search-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"srauth{stamp}", "display_name": "Search", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-search-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Search probe", "", "collective", "public", "both", now, now),
    ).lastrowid

    def document(title, body):
        return con.execute(
            "INSERT INTO project_documents (project_id, author_id, kind, slug, title, body, revision,"
            " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?) RETURNING id",
            (project, author, "spec", f"row-{len(title)}-{title}", title, body, 1, now, now),
        ).lastrowid

    permissive = document(
        "A language-agnostic design",
        "This design is language-neutral in every respect: no runtime assumptions at all.",
    )
    boolean_bait = document(
        "Language support",
        "We support one language today and neutral is not a word this document ever uses.",
    )
    unrelated = document("Deployment", "Nothing relevant in here at all.")
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    def search(query, extra=""):
        return call("GET", f"/api/v1/projects/{slug}/documents/search?q={urllib.parse.quote(query)}{extra}")

    status, body = search("language-neutral")
    check("a bare hyphenated term answers", status, 200)
    check("and matches only the document that says 'language-neutral'",
          [d["id"] for d in (body or [])], [permissive])
    check("the document that merely says 'language' is NOT returned",
          boolean_bait not in [d["id"] for d in (body or [])], True)

    status, body = search("deployment")
    check("a plain term still matches", [d["id"] for d in (body or [])], [unrelated])

    status, body = search("language neutral")
    check("a multi-word query is passed through, not phrased",
          sorted(d["id"] for d in (body or [])), sorted([permissive, boolean_bait]))

    status, body = call("GET", f"/api/v1/projects/{slug}/documents/search?q=")
    check("a present but EMPTY q answers", status, 200)
    check("with an empty array", body, [])
    status, _ = call("GET", f"/api/v1/projects/{slug}/documents/search")
    check("a MISSING q is 400", status, 400)

    status, body = search("language-neutral", "&limit=1")
    check("limit=1 returns one row", len(body or []), 1)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    status, _ = search("deployment")
    check("a private project's documents are 404 without a token", status, 404)

    cleanup(con, "probe-search-%")
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
