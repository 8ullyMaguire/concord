#!/usr/bin/env python3
"""Row 11b part 35: §17's accepted-answer badge — PUT and DELETE /requests/{id}/accept.

The badge is the author's statement about what answered their question, and four rules fall out of that sentence:

1. **author only** — a maintainer, an owner and a project member are all refused; the answer is theirs to praise, the
   badge is not theirs to set (403);
2. **the author check runs BEFORE the answer is looked up**, so a stranger naming an answer id that does not exist gets
   403 rather than 404. The other order leaks which answer ids exist to people who may not accept anything;
3. **the badge does not override the ranking** — accepting touches `accepted_answer_id` and `updated_at` and nothing
   else, so an accepted answer that lost its comparisons still sits where its rating puts it;
4. **unaccepting is not restricted by the answer's status** — an accepted answer that was later removed still leaves a
   badge pointing at it, and the author must be able to clear it.
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
    if projects:
        marks = ",".join("?" * len(projects))
        params = tuple(projects)
        con.execute(
            f"delete from request_answers where project_id in ({marks}) or request_id in"
            f" (select id from requests where project_id in ({marks}))",
            params + params,
        )
        con.execute(f"delete from requests where project_id in ({marks})", params)
        con.execute(f"delete from audit_log where project_id in ({marks})", params)
        con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    author_id, author_token = register(f"reqauthor{stamp}")
    stranger_id, stranger_token = register(f"reqstranger{stamp}")

    con = harness.connect("unused")
    cleanup(con, f"probe-accept-{stamp}%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"probe-accept-{stamp}", "Accept probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    rival = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"probe-accept-{stamp}-rival", "Rival", "", "collective", "public", "both", now, now),
    ).lastrowid
    # A third project: `request_answers` is UNIQUE (request_id, project_id) — one answer per project per request — so the
    # removed answer cannot share the first project's slot.
    third = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"probe-accept-{stamp}-third", "Third", "", "collective", "public", "both", now, now),
    ).lastrowid
    request_id = con.execute(
        "INSERT INTO requests (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, author_id, "Which is better?", "body", "open", now - 60, now - 60),
    ).lastrowid
    other_request = con.execute(
        "INSERT INTO requests (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, author_id, "Another question", "body", "open", now - 50, now - 50),
    ).lastrowid

    def answer(request, project_id, elo_r, status="active"):
        return con.execute(
            "INSERT INTO request_answers (request_id, project_id, body, status, elo_r, elo_rd, elo_vol,"
            " proposed_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
            (request, project_id, "answer", status, elo_r, 200.0, 0.06, author_id, now, now),
        ).lastrowid

    weak = answer(request_id, project, 1400.0)
    strong = answer(request_id, rival, 1700.0)
    removed = answer(request_id, third, 1300.0, status="removed")
    foreign = answer(other_request, project, 1500.0)
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("PUT", f"/api/v1/requests/{request_id}/accept", {"answer_id": weak})
    check("no token -> 401", status, 401)

    status, body = call("PUT", f"/api/v1/requests/{request_id}/accept", {"answer_id": weak},
                        token=stranger_token)
    check("a stranger -> 403", status, 403)

    status, _ = call("PUT", f"/api/v1/requests/{request_id}/accept", {"answer_id": 99999999},
                     token=stranger_token)
    check("a stranger naming a nonexistent answer still gets 403, not 404", status, 403)

    status, body = call("PUT", f"/api/v1/requests/{request_id}/accept", {"answer_id": foreign},
                        token=author_token)
    check("an answer from another request -> 404", status, 404)

    status, body = call("PUT", f"/api/v1/requests/{request_id}/accept", {"answer_id": removed},
                        token=author_token)
    check("a non-active answer -> 400", status, 400)

    status, body = call("PUT", f"/api/v1/requests/{request_id}/accept", {"answer_id": weak},
                        token=author_token)
    check("the author accepts the WEAKER answer -> 200", status, 200)
    check("and the badge names it", (body or {}).get("accepted_answer_id"), weak)

    status, body = call("GET", f"/api/v1/requests/{request_id}/answers")
    check("the ranking did NOT move: the stronger answer is still first",
          [row["id"] for row in body or []], [strong, weak, removed])
    check("and accepting changed no rating", [row["elo_r"] for row in body or []], [1700.0, 1400.0, 1300.0])

    status, body = call("GET", f"/api/v1/requests/{request_id}")
    check("the badge is visible on the request itself", (body or {}).get("accepted_answer_id"), weak)

    status, body = call("PUT", f"/api/v1/requests/{request_id}/accept", {"answer_id": strong},
                        token=author_token)
    check("accepting a second answer replaces the badge, not adds to it",
          (body or {}).get("accepted_answer_id"), strong)

    status, _ = call("DELETE", f"/api/v1/requests/{request_id}/accept", token=stranger_token)
    check("a stranger cannot unaccept -> 403", status, 403)

    con.execute("UPDATE request_answers SET status = 'removed' WHERE id = ?", (strong,))
    con.commit()
    status, body = call("DELETE", f"/api/v1/requests/{request_id}/accept", token=author_token)
    check("the author unaccepts even when the badged answer was removed -> 200", status, 200)
    check("and the key is gone, not zero", "accepted_answer_id" in (body or {}), False)

    status, _ = call("DELETE", f"/api/v1/requests/{request_id}/accept", token=author_token)
    check("unaccepting twice is harmless", status, 200)

    cleanup(con, f"probe-accept-{stamp}%")
    for user in (author_id, stranger_id):
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
