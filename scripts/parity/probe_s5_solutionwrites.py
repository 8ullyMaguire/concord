#!/usr/bin/env python3
"""Row 11d part 48 (B2c): filing a solution (§6.3).

Filing one solution is **three** writes that have to agree — the solution, the arena that ranks it, and its slot in that
arena — and the difference between "all three" and "some of them" is invisible from every read the product has. So most of
these assertions are about the *other two* writes, not the row.

The rules, each a refusal rather than a note on the row:

1. a **feature's author cannot author its solutions** (§5.2: nobody may judge their own entry);
2. `integrate-external` **needs a catalog link** — the type means "adopt the thing at this link";
3. a **complementary** solution is refused — it is stackable, so it does not compete, and comparing "do A, then B" against
   "do C" is not a question anybody can answer;
4. the first **do-nothing** becomes the arena's permanent baseline, and a second one **demotes** the first (its votes are
   history) rather than deleting it;
5. a **fork** inherits its parent's rating with **doubled RD**, unclamped — the reference doubles and leaves it.
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


def cleanup(con, like):
    projects = [row[0] for row in con.execute("select id from projects where slug like ?", (like,)).fetchall()]
    if not projects:
        return
    marks = ",".join("?" * len(projects))
    params = tuple(projects)
    features = [row[0] for row in con.execute(
        f"select id from features where project_id in ({marks})", params).fetchall()]
    if features:
        fmarks = ",".join("?" * len(features))
        fparams = tuple(features)
        arenas = [row[0] for row in con.execute(
            f"select id from arenas where feature_id in ({fmarks})", fparams).fetchall()]
        if arenas:
            amarks = ",".join("?" * len(arenas))
            aparams = tuple(arenas)
            con.execute(f"delete from arena_entries where arena_id in ({amarks})", aparams)
            con.execute(f"delete from pairwise_votes where arena_id in ({amarks})", aparams)
            con.execute(f"delete from arenas where id in ({amarks})", aparams)
        con.execute(f"delete from solutions where feature_id in ({fmarks})", fparams)
        con.execute(f"delete from consensus_calls where feature_id in ({fmarks})", fparams)
        con.execute(f"delete from features where id in ({fmarks})", fparams)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    author_id, author_token = register(f"sauthor{stamp}")
    filer_id, filer_token = register(f"sfiler{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-solutions-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Solution probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    for user in (author_id, filer_id):
        con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                    (project, user, "contributor", now))
    feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, author_id, "A feature", "", "ready", now, now),
    ).lastrowid
    con.commit()

    results = []

    def check(name, got, want):
        # Printed AS IT GOES: a probe that dies half-way through the happy path otherwise reports nothing at all, and the
        # first thing you need to know is which assertion was being made when it died.
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    base = f"/api/v1/projects/{slug}/features/{feature}/solutions"

    # ── refusals ─────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", base, {"title": "Anonymous"})
    check("filing anonymously is 401", status, 401)
    status, _ = call("POST", base, {"title": "   "}, token=filer_token)
    check("a solution needs a title", status, 400)
    status, _ = call("POST", base, {"title": "Mine"}, token=author_token)
    check("the feature's author may not author its solutions", status, 403)
    status, _ = call("POST", base, {"title": "Odd", "type": "wishful"}, token=filer_token)
    check("an unknown type is 400", status, 400)
    status, _ = call("POST", base, {"title": "Adopt it", "type": "integrate-external"}, token=filer_token)
    check("integrate-external without a link is 400", status, 400)
    status, _ = call("POST", base, {"title": "And also", "relationship": "complementary"},
                     token=filer_token)
    check("a complementary solution is 400", status, 400)
    arenas = con.execute("SELECT count(*) FROM arenas WHERE feature_id = ?", (feature,)).fetchone()[0]
    check("no refusal left an arena behind", arenas, 0)

    # ── the happy path, and the two writes beside it ─────────────────────────────────────────────────────────────
    status, filed = call("POST", base, {"title": "Cache it", "body": "the obvious thing"}, token=filer_token)
    check("a contributor may file", status, 201)
    filed = filed or {}
    solution = filed.get("solution") or {}
    solution_id = solution.get("id")
    check("it is filed as a draft", solution.get("status"), "draft")
    check("the type defaults to build-new", solution.get("type"), "build-new")
    check("and it is not the baseline", filed.get("is_baseline"), False)
    arena = con.execute("SELECT id FROM arenas WHERE feature_id = ? AND type = 'solution'",
                        (feature,)).fetchone()
    check("an arena now exists for the feature", arena is not None, True)
    entry = con.execute(
        "SELECT r, rd, is_baseline FROM arena_entries WHERE arena_id = ? AND entity_type = 'solution'"
        " AND entity_id = ?", (arena[0], solution_id)).fetchone()
    check("with a slot at the ranking defaults", entry, (1500.0, 350.0, False))

    status, adopted = call("POST", base, {"title": "Adopt Redis", "type": "integrate-external",
                                          "external_ref": "https://redis.io"}, token=filer_token)
    check("integrate-external with a link is accepted", status, 201)
    adopted = adopted or {}
    check("and the link is kept", (adopted.get("solution") or {}).get("external_ref"),
          "https://redis.io")
    arenas = con.execute("SELECT count(*) FROM arenas WHERE feature_id = ?", (feature,)).fetchone()[0]
    check("the second solution joins the SAME arena", arenas, 1)

    # ── the baseline ─────────────────────────────────────────────────────────────────────────────────────────────
    status, nothing = call("POST", base, {"title": "Do nothing", "type": "do-nothing"}, token=filer_token)
    check("do-nothing is the baseline", (status, (nothing or {}).get("is_baseline")), (201, True))
    first_baseline = (nothing or {}).get("solution") or {}
    status, nothing2 = call("POST", base, {"title": "Also do nothing", "type": "do-nothing"},
                            token=filer_token)
    flagged = con.execute(
        "SELECT entity_id FROM arena_entries WHERE arena_id = ? AND is_baseline", (arena[0],)).fetchall()
    check("a second do-nothing DEMOTES the first rather than adding one", [row[0] for row in flagged],
          [(nothing2 or {}).get("solution", {}).get("id")])
    check("and the demoted one is still in the arena (its votes are history)",
          con.execute("SELECT count(*) FROM arena_entries WHERE arena_id = ? AND entity_id = ?",
                      (arena[0], first_baseline.get("id"))).fetchone()[0], 1)

    # ── a fork ───────────────────────────────────────────────────────────────────────────────────────────────────
    con.execute("UPDATE arena_entries SET r = 1712.5 WHERE arena_id = ? AND entity_id = ?",
                (arena[0], solution_id))
    con.commit()
    status, forked = call("POST", base, {"title": "Cache it, but with a queue",
                                         "parent_solution_id": solution_id}, token=filer_token)
    check("a fork may be filed", status, 201)
    fork_id = ((forked or {}).get("solution") or {}).get("id")
    fork_entry = con.execute(
        "SELECT r, rd FROM arena_entries WHERE arena_id = ? AND entity_id = ?",
        (arena[0], fork_id)).fetchone()
    check("it inherits the parent's rating with DOUBLED, unclamped RD",
          fork_entry, (1712.5, 700.0))
    # The fork relation is recorded on `solutions.parent_solution_id`, not on `arena_entries.parent_entry_id` — this
    # schema's entries have a composite key and no surrogate id for that column to point at, and the foreign key on the
    # solution is the place that can enforce it.
    check("and the fork records its parent",
          con.execute("SELECT parent_solution_id FROM solutions WHERE id = ?", (fork_id,)).fetchone()[0],
          solution_id)
    status, _ = call("POST", base, {"title": "Fork of nothing", "parent_solution_id": 999999},
                     token=filer_token)
    check("a fork of a solution outside the arena is refused", status, 400)
    status, _ = call("POST", base, {"title": "Fork of itself"}, token=filer_token)
    check("filing after those refusals still works", status, 201)

    cleanup(con, f"{slug}%")
    con.commit()
    for user in (author_id, filer_id):
        con.execute("DELETE FROM api_tokens WHERE user_id = ?", (user,))
        con.execute("DELETE FROM users WHERE id = ?", (user,))
    con.commit()
    con.close()

    # The lines are already printed as they are checked; this is the count, not a second printing of the same failures.
    failures = sum(1 for _, got, want in results if got != want)
    print(f"\n{len(results) - failures}/{len(results)} ok")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
