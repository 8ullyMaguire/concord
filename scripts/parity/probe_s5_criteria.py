#!/usr/bin/env python3
"""Row 11b part 13: the project page's panels — criteria, profiles, languages, lists.

Four orderings are rules, and a port that returns the right ROWS in the wrong ORDER is wrong in the only way a
page can see:

* criteria: `active DESC, slug` — a retired criterion is listed after the live ones, not dropped;
* profiles: by slug;
* languages: `pct DESC, language ASC` — the tie-break makes two equal shares stable between requests;
* lists: the project's own first, then the instance-wide ones, each newest first — and an instance-wide list has
  `project_id: null`, which is a SCOPE (§16) and not a zero.
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


def register(prefix, stamp):
    status, body = call(
        "POST", "/api/v1/auth/register",
        {"username": f"{prefix}{stamp}", "display_name": prefix, "password": "correct horse"},
    )
    if status != 201:
        print(f"register {prefix} failed: {status} {body}")
        sys.exit(2)
    return body["user"]["id"], body["token"]


def cleanup(con, like):
    for (project,) in con.execute("select id from projects where slug like ?", (like,)).fetchall():
        con.execute("delete from criteria where project_id = ?", (project,))
        con.execute("delete from criteria_profiles where project_id = ?", (project,))
        con.execute("delete from project_languages where project_id = ?", (project,))
        con.execute("delete from lists where project_id = ?", (project,))
        con.execute("delete from members where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.execute("delete from lists where slug like ?", (like,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    owner_id, owner_token = register("crpanel", stamp)
    like = f"probe-crit-{stamp}"

    con = harness.connect("unused")
    cleanup(con, f"probe-crit-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (like, "Criteria probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    con.execute(
        "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
        (project, owner_id, "owner", now),
    )
    # Two criteria, inserted in the order that would also look right if sorted by slug alone: the INACTIVE one
    # comes first alphabetically, so `active DESC` is the only thing that puts the live one on top.
    # `active` is a bigint in the PostgreSQL baseline, not a boolean: the Go stored 0/1 and the generator kept the
    # column as the SQLite affinity said (INTEGER -> bigint).
    for slug, name, active in (("aaa-retired", "Retired", 0), ("zzz-live", "Live", 1)):
        con.execute(
            "INSERT INTO criteria (project_id, slug, name, description, direction, default_weight, active)"
            " VALUES (?,?,?,?,?,?,?)",
            (project, slug, name, "why", "higher_is_better", 1.0, active),
        )
    for slug in ("zeta", "alpha"):
        con.execute(
            "INSERT INTO criteria_profiles (project_id, slug, name) VALUES (?,?,?)",
            (project, slug, slug.title()),
        )
    # Three languages, two at the same percentage so the name tie-break is the assertion.
    for language, pct in (("Zig", 30.0), ("Ada", 30.0), ("Rust", 40.0)):
        con.execute(
            "INSERT INTO project_languages (project_id, language, pct) VALUES (?,?,?)",
            (project, language, pct),
        )
    # One project list and one instance-wide list; the instance-wide one is NEWER, so it would sort first
    # under `updated_at DESC` alone — the scope ordering is what the probe checks.
    con.execute(
        "INSERT INTO lists (project_id, slug, title, description, status, created_by, created_at,"
        " updated_at) VALUES (?,?,?,?,?,?,?,?)",
        (project, f"{like}-own", "Own list", "", "open", owner_id, now - 100, now - 100),
    )
    con.execute(
        "INSERT INTO lists (project_id, slug, title, description, status, created_by, created_at,"
        " updated_at) VALUES (NULL,?,?,?,?,?,?,?)",
        (f"{like}-global", "Instance list", "", "open", owner_id, now, now),
    )
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{project}/criteria")
    check("criteria answer", status, 200)
    check("active first, then slug", [row["slug"] for row in body], ["zzz-live", "aaa-retired"])
    check("with the direction round-tripped", body[0]["direction"], "higher_is_better")
    check("and the weight", body[0]["default_weight"], 1.0)
    status, _ = call("GET", f"/api/v1/projects/{project}/criteria/")
    check("the trailing-slash form answers too", status, 200)

    status, body = call("GET", f"/api/v1/projects/{project}/criteria-profiles")
    check("profiles answer", status, 200)
    check("by slug", [row["slug"] for row in body], ["alpha", "zeta"])

    status, body = call("GET", f"/api/v1/projects/{like}/languages")
    check("languages answer", status, 200)
    check("highest percentage first, ties by name",
          [(row["language"], row["pct"]) for row in body],
          [("Rust", 40.0), ("Ada", 30.0), ("Zig", 30.0)])

    status, body = call("GET", f"/api/v1/projects/{project}/lists")
    check("lists answer", status, 200)
    check("the project's own list first even though it is older", body[0]["title"], "Own list")
    check("then the instance-wide one", body[1]["title"], "Instance list")
    check("with project_id null for the instance list", body[1]["project_id"], None)
    check("and the id for the project's own", body[0]["project_id"], project)

    # Visibility: the same 404 that confirms nothing.
    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    for path in ("criteria", "criteria-profiles", "lists"):
        status, _ = call("GET", f"/api/v1/projects/{project}/{path}")
        check(f"a private project's {path} are 404 without a token", status, 404)
    status, _ = call("GET", f"/api/v1/projects/{project}/criteria", token=owner_token)
    check("but answer for a member", status, 200)

    cleanup(con, "probe-crit-%")
    con.execute("DELETE FROM api_tokens WHERE user_id = ?", (owner_id,))
    con.execute("DELETE FROM users WHERE id = ?", (owner_id,))
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
