#!/usr/bin/env python3
"""Row 11b part 22: a list and its ranked entries.

Three rules that are each a plausible mistake:

1. §16: "entries are RANKED, not just listed" — `elo_r DESC`, and the fixture gives entries ratings in an order that
   is neither insertion order nor alphabetical, so a list ordering by title or id is visibly wrong;
2. removed entries are HIDDEN, not deleted: they are moderation history and the page must not count them;
3. the visibility decision belongs to the LIST's project, not the project in the URL: a list owned by a private
   project must not become readable through a public project's URL. An INSTANCE-WIDE list (`project_id` NULL) is
   readable from everywhere, which is the case a `project_id` int64 made unreachable at all.
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
        con.execute("delete from list_entries where list_id in (select id from lists where project_id = ?)",
                    (project,))
        con.execute("delete from lists where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.execute("delete from list_entries where list_id in (select id from lists where slug like ?)", (like,))
    con.execute("delete from lists where slug like ?", (like,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    slug = f"probe-list-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"listauth{stamp}", "display_name": "List", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-list-%")
    now = time.time()
    public = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Public probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    hidden = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug + "-priv", "Private probe", "", "collective", "private", "both", now, now),
    ).lastrowid

    def lst(project_id, title):
        return con.execute(
            "INSERT INTO lists (project_id, slug, title, description, status, created_by, created_at,"
            " updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
            (project_id, f"{slug}-{title}", title, "", "open", author, now, now),
        ).lastrowid

    listed = lst(public, "reading")
    secret = lst(hidden, "secret")
    global_list = lst(None, "everywhere")

    def entry(list_id, title, elo, state="active"):
        return con.execute(
            "INSERT INTO list_entries (list_id, url, title, description, category, status, elo_r, elo_rd,"
            " elo_vol, proposed_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id",
            (list_id, "https://example.invalid/" + title, title, "", None, state, elo, 350.0, 0.06,
             author, now, now),
        ).lastrowid

    # Insertion order = alphabetical order = the WRONG answer, so the rating order is distinguishable.
    alpha = entry(listed, "alpha", 1400.0)
    beta = entry(listed, "beta", 1700.0)
    gamma = entry(listed, "gamma", 1550.0)
    gone = entry(listed, "deleted-one", 1900.0, state="removed")
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{public}/lists/{listed}")
    check("the list answers", status, 200)
    # `name`, not `title`: the reference maps its `title` COLUMN to a JSON `name` for lists. This probe asserted `title` — the
    # port's own key — which is how a self-consistent suite hides a shape divergence.
    check("with its name", (body or {}).get("name"), "reading")
    check("and its project", (body or {}).get("project_id"), public)

    status, body = call("GET", f"/api/v1/projects/{public}/lists/{listed}/entries")
    check("entries answer", status, 200)
    check("ranked by rating, not by title or id",
          [e["id"] for e in (body or [])], [beta, gamma, alpha])
    check("the removed entry is hidden but its rating was the highest",
          [e["title"] for e in (body or [])], ["beta", "gamma", "alpha"])
    check("an entry carries its rating columns",
          ((body or [{}])[0].get("elo_r"), (body or [{}])[0].get("elo_rd")), (1700.0, 350.0))
    check("and a null category stays null", (body or [{}])[0].get("category"), None)

    # Instance-wide: project_id null in the JSON, readable with no token at all.
    status, body = call("GET", f"/api/v1/projects/{public}/lists/{global_list}")
    check("an instance-wide list is readable", status, 200)
    check("and says so with project_id null", (body or {}).get("project_id"), None)

    # A private project's list through a PUBLIC project's URL must not leak.
    status, _ = call("GET", f"/api/v1/projects/{public}/lists/{secret}")
    check("a private project's list is 404 through a public project's URL", status, 404)
    status, _ = call("GET", f"/api/v1/projects/{hidden}/lists/{secret}")
    check("and through its own, without a token", status, 404)

    status, _ = call("GET", f"/api/v1/projects/{public}/lists/{listed}/entries")
    check("entries stay readable when the parent is public", status, 200)
    status, _ = call("GET", f"/api/v1/projects/{public}/lists/{secret}/entries")
    check("but not for a list the caller cannot read", status, 404)

    status, _ = call("GET", f"/api/v1/projects/{public}/lists/not-a-number")
    check("a non-numeric list id is 400", status, 400)
    status, _ = call("GET", f"/api/v1/projects/{public}/lists/999999999")
    check("an id nobody created is 404", status, 404)

    cleanup(con, "probe-list-%")
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
