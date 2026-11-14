#!/usr/bin/env python3
"""Row 11d part 47 (B2c): §6.5's feature-scoped call, at the reference's path.

The port already answered this pair under its own path, `/features/{feature}/solution-call`, and the inventory called
`/features/{feature}/consensus` missing. So the work here was **not** new behaviour: it was (a) an alias so a caller written
against the Go service stops getting a 404 for a path that means the same thing, and (b) moving the readiness gate OUT of
the handler and into the store, beside the write, because the handler was re-deriving conditions that the readiness page
already computes — two answers to "may this be called?", and the second one is the one that goes stale.

The rules asserted:

1. readiness is **unauthenticated** — it is the public "why has no call opened yet" view, and a reader deciding whether to
   contribute needs it before they have an account;
2. opening on merit with **no leader** is refused, and with a leader but unmet conditions is refused **naming them**, and
   **nothing is opened** either way;
3. an early call requires a **stated reason** (400 without one) and a **collaborator** (403 for a stranger);
4. an early call is **flagged, not hidden**: `opened_early` and the reason are on the row, and the solution moves to
   `consensus`;
5. a second call on the same feature is refused.
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
        con.execute(f"delete from consensus_calls where feature_id in ({fmarks})", fparams)
        con.execute(f"delete from feature_complaints where feature_id in ({fmarks})", fparams)
        con.execute(f"delete from feature_deps where blocker_id in ({fmarks}) or blocked_id in ({fmarks})",
                    fparams + fparams)
        con.execute(f"delete from feature_evidence where feature_id in ({fmarks})", fparams)
        con.execute(f"delete from feature_tags where feature_id in ({fmarks})", fparams)
        con.execute(f"delete from work_item_claims where feature_id in ({fmarks})", fparams)
        con.execute(f"delete from solutions where feature_id in ({fmarks})", fparams)
        con.execute(f"delete from features where id in ({fmarks})", fparams)
    con.execute(f"delete from complaints where project_id in ({marks})", params)
    con.execute(f"delete from embeddings where project_id in ({marks})", params)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    maintainer_id, maintainer_token = register(f"scmaint{stamp}")
    stranger_id, stranger_token = register(f"scstranger{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-solcall-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Solution call probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (project, maintainer_id, "maintainer", now))
    feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, maintainer_id, "A feature with a solution", "", "ready", now, now),
    ).lastrowid
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    def calls():
        return con.execute("SELECT count(*) FROM consensus_calls WHERE feature_id = ?", (feature,)).fetchone()[0]

    base = f"/api/v1/projects/{slug}/features/{feature}/consensus"

    # ── readiness, before anything exists ────────────────────────────────────────────────────────────────────────
    status, readiness = call("GET", base)
    check("readiness is readable without a token", status, 200)
    readiness = readiness or {}
    check("it names the feature it is about", readiness.get("feature_id"), feature)
    check("with no solution there is no leader", readiness.get("leader_id"), 0)
    check("and the report says why, as an unmet condition",
          any("solution" in (condition.get("detail") or "")
              for condition in readiness.get("conditions") or []), True)

    # ── opening on merit, with nothing to call ──────────────────────────────────────────────────────────────────
    status, _ = call("POST", base, {})
    check("opening a call anonymously is 401", status, 401)
    # The merit path is gated by the CONDITIONS, not by a role: the reference's `handleOpenSolutionCall` resolves the actor
    # and the feature and hands the decision to the store, and only the EARLY path asks whether the actor is a
    # collaborator. Asserting 403 here would be asserting a rule the reference does not have.
    status, _ = call("POST", base, {}, token=stranger_token)
    check("a non-member is gated by the conditions, not by a role", status, 409)
    status, refused = call("POST", base, {}, token=maintainer_token)
    check("opening on merit with no leader is a conflict, not a bad request", status, 409)
    check("and it names what is missing", "solution" in json.dumps(refused), True)
    check("nothing was opened", calls(), 0)

    # ── an arena with a leader, and conditions that still do not hold ───────────────────────────────────────────
    solution = con.execute(
        "INSERT INTO solutions (feature_id, author_id, title, body, type, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (feature, maintainer_id, "The leading idea", "", "patch", "proposed", now, now),
    ).lastrowid
    arena = con.execute(
        "INSERT INTO arenas (type, project_id, feature_id, question, created_at) VALUES (?,?,?,?,?) RETURNING id",
        ("solution", project, feature, "Which solution?", now),
    ).lastrowid
    for entity, rating, baseline in ((solution, 1550.0, False), (0, 1500.0, True)):
        con.execute(
            "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games, is_baseline,"
            " updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
            (arena, "solution", entity, rating, 300.0, 0.06, 1, baseline, now),
        )
    con.commit()
    status, readiness = call("GET", base)
    readiness = readiness or {}
    check("with a solution the leader is named", readiness.get("leader_id"), solution)
    check("but a fresh arena is not ready", readiness.get("ready"), False)
    status, refused = call("POST", base, {}, token=maintainer_token)
    check("so opening on merit is still refused", status, 409)
    check("and the refusal is the unmet condition, not a bare code",
          len(refused.get("error") or refused) > 0 if isinstance(refused, dict) else bool(refused), True)
    check("still nothing was opened", calls(), 0)

    # §6.5's call must be ABOUT something: the store refuses a call on a feature with no linked complaint, because a
    # consensus window spent on a problem nobody filed is a window spent on nothing. The fixture needs the link, and
    # `complaints.public_id` has no default, so it is supplied rather than left to a sequence that does not exist.
    complaint = con.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, public_id, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, maintainer_id, "It is slow", "", f"c{stamp}", now, now),
    ).lastrowid
    con.execute("INSERT INTO feature_complaints (feature_id, complaint_id) VALUES (?,?)", (feature, complaint))
    con.commit()

    # ── §6.5's discretionary path ───────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", base, {"early": True}, token=maintainer_token)
    check("an early call with no reason is 400", status, 400)
    status, _ = call("POST", base, {"early": True, "reason": "we need this now"}, token=stranger_token)
    check("an early call is a collaborator's discretion", status, 403)
    status, opened = call("POST", base, {"early": True, "reason": "we need this now"},
                          token=maintainer_token)
    check("a collaborator may open one early", status, 201)
    opened = opened or {}
    call_row = (opened.get("call") or opened) or {}
    check("and it is flagged as early", call_row.get("opened_early"), 1)
    check("with the reason recorded", call_row.get("early_reason"), "we need this now")
    check("and it opened on the leader", call_row.get("solution_id"), solution)
    status_now = con.execute("SELECT status FROM solutions WHERE id = ?", (solution,)).fetchone()[0]
    check("the solution moves to consensus", status_now, "consensus")
    check("one call exists now", calls(), 1)
    status, _ = call("POST", base, {"early": True, "reason": "again"}, token=maintainer_token)
    check("a second call on the same feature is refused", status in (400, 409), True)
    check("and did not open a second row", calls(), 1)

    cleanup(con, f"{slug}%")
    con.commit()
    for user in (maintainer_id, stranger_id):
        con.execute("DELETE FROM api_tokens WHERE user_id = ?", (user,))
        con.execute("DELETE FROM users WHERE id = ?", (user,))
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
