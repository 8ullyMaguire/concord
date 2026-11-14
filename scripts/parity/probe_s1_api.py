#!/usr/bin/env python3
"""S1's API behaviour, probed directly: register, place a derived card, refuse past a WIP limit.

The e2e suites cannot prove the API half yet — they wait on `#kanban-board .kanban-card`, and the
page is P6's work (row 12). This probes the same behaviour over HTTP so the slice's claim is grounded
in a request and its response rather than in a green build.
"""
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "tests", "e2e"))

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

BASE = os.environ.get("CANDIDATE", "http://127.0.0.1:8107")


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw) if raw else None
        except ValueError:
            return e.code, raw.decode("utf-8", "replace")


def main():
    import harness  # the PgSeed adapter, which is what the suites seed with
    slug = "api-probe-" + str(int(time.time() * 1000) % 100000000)

    # Register first: `features.author_id` is a foreign key to `users`, and a fixture that inserted
    # features before an account existed failed with ForeignKeyViolation — which is the schema
    # saying the order is not a detail.
    status, body = call("POST", "/api/v1/auth/register", {
        "username": f"probe{int(time.time())}", "display_name": "Probe", "password": "correct horse",
    })
    token = body.get("token") if isinstance(body, dict) else None
    author = ((body or {}).get("user") or {}).get("id") if isinstance(body, dict) else None
    registered = ("register", status, 201, (token or "")[:4])
    if not token or not author:
        print(json.dumps([registered], indent=1))
        return 1

    con = harness.connect("unused")
    con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)",
        (slug, "API probe", "", "collective", "public", "complaint_first", time.time(), time.time()),
    )
    con.commit()
    pid = con.execute("SELECT id FROM projects WHERE slug = ?", (slug,)).fetchone()[0]
    for phase, wip, position in (("inbox", None, 0), ("ready", 1, 1)):
        con.execute(
            "INSERT INTO board_columns (project_id, phase, position, wip_limit) VALUES (?,?,?,?)",
            (pid, phase, position, wip),
        )
    for index in range(3):
        con.execute(
            "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?)",
            (pid, author, f"probe {index}", "", "draft", time.time(), time.time()),
        )
    con.commit()
    features = [r[0] for r in con.execute(
        "SELECT id FROM features WHERE project_id = ? ORDER BY id", (pid,)).fetchall()]
    con.close()

    results = [registered]

    # Creating a project: needs an account, validates the slug, seeds charter + nine columns +
    # membership + metrics, and refuses a slug already taken.
    status, body = call("POST", "/api/v1/projects",
                        {"slug": "no-token", "name": "No token"})
    results.append(("create without a token", status, 401, body))
    status, body = call("POST", "/api/v1/projects",
                        {"slug": "Not A Slug", "name": "Bad"}, token=token)
    results.append(("create with a bad slug", status, 400, body))
    status, body = call("POST", "/api/v1/projects",
                        {"slug": "created-probe", "name": "Created probe"}, token=token)
    results.append(("create", status, 201, (body or {}).get("slug")
                    if isinstance(body, dict) else body))
    status, body = call("POST", "/api/v1/projects",
                        {"slug": "created-probe", "name": "Created probe"}, token=token)
    results.append(("create a duplicate slug", status, 409, body))
    status, body = call("GET", "/api/v1/projects/created-probe/board")
    columns = len(((body or {}).get("columns") or [])) if isinstance(body, dict) else 0
    results.append((f"nine board columns ({columns})", columns, 9, body if columns != 9 else ""))

    # A derived card has no row; naming the feature is how it is placed, and the row is created.
    status, body = call("PUT", f"/api/v1/projects/{slug}/board/cards/0/move?to=inbox&feature_id="
                                f"{features[0]}", token=token)
    results.append(("place derived card", status, 200, body))

    # The same feature again, into the WIP=1 column: first placement fits, second must be refused.
    status, body = call("PUT", f"/api/v1/projects/{slug}/board/cards/0/move?to=ready&feature_id="
                                f"{features[0]}", token=token)
    results.append(("place into empty wip=1 column", status, 200, body))
    status, body = call("PUT", f"/api/v1/projects/{slug}/board/cards/0/move?to=ready&feature_id="
                                f"{features[1]}", token=token)
    results.append(("second into full wip=1 column", status, 409, body))

    # No token, no move.
    status, body = call("PUT", f"/api/v1/projects/{slug}/board/cards/0/move?to=inbox&feature_id="
                                f"{features[2]}")
    results.append(("move without a token", status, 401, body))

    bad = [r for r in results if r[1] != r[2]]
    for label, got, want, detail in results:
        print(f"{'ok ' if got == want else 'BAD'} {label}: {got} (want {want})"
              f"{'' if got == want else ' -> ' + json.dumps(detail)[:160]}")
    return 0 if not bad else 1


if __name__ == "__main__":
    sys.exit(main())
