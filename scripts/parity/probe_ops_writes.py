#!/usr/bin/env python3
"""Row 11 part 61: the two destructive ops writes — lifting a hold, folding one complaint into another.

`POST /api/v1/emergency-holds/{hold_id}/release` and `POST /api/v1/projects/{project_id}/complaints/{id}/merge/{target_id}`.

Both are authority actions on somebody else's content, and both are auditable *because* of that: a hold lifted with no written
reason leaves the ledger saying only that someone did it, and a merge is a decision about what a complaint means.

What the probe establishes:

1. **The hold's project comes from the hold, not the URL.** The release route sits outside the project router (a client should find
   a hold by its id), so a role check written against the path's `project_id` — a parameter that is not there — answers 404 for every
   caller and hides the second defect: a hold releasable by its id alone. Asserted by a maintainer of the WRONG project being
   refused.
2. **A written reason is required.** §9.3's ledger carries it, and "who did it" with no "why" is not accountability.
3. **Releasing twice is 400, not 200.** The store's UPDATE carries `released_at is null` in its WHERE, so a second release cannot
   overwrite the first reason — and the caller is told rather than silently served a stale success.
4. **Both write a ledger row AND an audit row.** These are the two records that make an emergency power checkable later.
5. **A merge cannot cross projects.** Loading both rows is not a guard on either: without the check, the source's impacts are
   inserted against a foreign complaint — an `on conflict do nothing` that no later reader can tell from a legitimate impact.
6. **A merge cannot be into itself** — that sets `merged_into` to its own id and every reader following the chain loops.
7. **A complaint reached through another project's URL is 404**, because the path's project and the row's project are two facts and
   the reference checks both. A maintainer of project A must not act on project B's complaint through A's URL.
8. **A merge moves the impacts** (`on conflict do nothing`: the same person may already be affected by both, and after the merge that
   is one opinion, not two) and leaves the source row in place, pointing at the target.
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


def refusal(body):
    if isinstance(body, dict):
        for key in ("error", "message", "detail"):
            if key in body:
                return json.dumps(body[key])
    return json.dumps(body)


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    maintainer_id, maintainer = register(f"mwkeep{stamp}")
    outsider_id, outsider = register(f"mwout{stamp}")
    reporter_id, _ = register(f"mwrep{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-ops-w-{stamp}"
    other = f"{slug}-other"
    con.execute("delete from projects where slug like ?", (f"{slug}%",))
    con.commit()

    def project(name):
        return con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
            " repo_url, repo_host, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
            (name, name, "", "collective", "public", "both", None, None, now, now),
        ).lastrowid

    pid = project(slug)
    other_pid = project(other)
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (pid, maintainer_id, "maintainer", now))
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (other_pid, outsider_id, "maintainer", now))
    con.commit()

    def complaint(project_id, title):
        return con.execute(
            "INSERT INTO complaints (project_id, author_id, title, body, severity, frequency,"
            " strategic_multiplier, status, created_at, updated_at, origin, public_id)"
            " VALUES (?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id",
            (project_id, reporter_id, title, "", 3, 1.0, 1.0, "open", now, now, "field", f"pc-{title}-{stamp}"),
        ).lastrowid

    source = complaint(pid, "slow")
    target = complaint(pid, "slowness")
    foreign = complaint(other_pid, "elsewhere")
    for who in (reporter_id, outsider_id):
        con.execute("INSERT INTO complaint_impacts (complaint_id, user_id, severity, created_at)"
                    " VALUES (?,?,?,?)", (source, who, 3, now))
    con.execute("INSERT INTO complaint_impacts (complaint_id, user_id, severity, created_at)"
                " VALUES (?,?,?,?)", (target, outsider_id, 2, now))
    # `emergency_holds.call_id` is a real FK onto `consensus_calls`, so the fixture needs the call it holds.
    def call_row(project_id, opened_by, public):
        return con.execute(
            "INSERT INTO consensus_calls (project_id, opened_by, opens_at, closes_at, public_id)"
            " VALUES (?,?,?,?,?) RETURNING id",
            (project_id, opened_by, now, now + 86400, public),
        ).lastrowid

    call_id = call_row(pid, maintainer_id, f"pc-hold-{stamp}")
    other_call = call_row(other_pid, outsider_id, f"pc-hold-other-{stamp}")
    hold_id = con.execute(
        "INSERT INTO emergency_holds (project_id, call_id, held_by, reason, grounds, created_at, expires_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (pid, call_id, maintainer_id, "security embargo", "§6.6", now, now + 86400),
    ).lastrowid
    other_hold = con.execute(
        "INSERT INTO emergency_holds (project_id, call_id, held_by, reason, grounds, created_at, expires_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (other_pid, other_call, outsider_id, "other project", "§6.6", now, now + 86400),
    ).lastrowid
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    release_path = f"/api/v1/emergency-holds/{hold_id}/release"

    # ── lifting a hold ───────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", release_path, {"reason": "resolved"})
    check("releasing a hold anonymously is 401", status, 401)
    status, _ = call("POST", "/api/v1/emergency-holds/999999999/release", {"reason": "resolved"}, token=maintainer)
    check("a hold that does not exist is 404", status, 404)
    status, body = call("POST", release_path, {"reason": "   "}, token=maintainer)
    check("a release with no reason is 400", status, 400)
    check("and the refusal says the reason is what the ledger records", "reason" in refusal(body), True)
    # The hold carries its own project; this caller maintains a DIFFERENT one.
    status, _ = call("POST", release_path, {"reason": "not mine"}, token=outsider)
    check("a maintainer of another project cannot lift this hold", status in (403, 404), True)
    check("and it is still unreleased",
          con.execute("SELECT released_at FROM emergency_holds WHERE id = ?", (hold_id,)).fetchone()[0], None)
    status, _ = call("POST", f"/api/v1/emergency-holds/{other_hold}/release", {"reason": "not mine"}, token=maintainer)
    check("nor can this project's maintainer lift the other project's hold", status in (403, 404), True)

    status, body = call("POST", release_path, {"reason": "vendor patched it"}, token=maintainer)
    body = body or {}
    check("the project's maintainer lifts it", status, 200)
    check("the answer carries the hold and a status", sorted(body.keys()), ["hold", "status"])
    check("with status released", body.get("status"), "released")
    check("and the hold shows when", isinstance((body.get("hold") or {}).get("released_at"), (int, float)), True)
    check("the reason is stored on the row",
          con.execute("SELECT release_reason FROM emergency_holds WHERE id = ?", (hold_id,)).fetchone()[0],
          "vendor patched it")
    check("the hold is no longer active", (body.get("hold") or {}).get("active"), False)
    status, body = call("POST", release_path, {"reason": "again"}, token=maintainer)
    check("releasing twice is 400", status, 400)
    check("and the first reason is not overwritten",
          con.execute("SELECT release_reason FROM emergency_holds WHERE id = ?", (hold_id,)).fetchone()[0],
          "vendor patched it")
    check("the ledger records the release, with its reason",
          con.execute("SELECT subject, justification FROM admin_ledger WHERE action = 'release_emergency_hold'"
                      " AND project_id = ? ORDER BY id DESC LIMIT 1", (pid,)).fetchone(),
          (f"consensus call {call_id}", "vendor patched it"))
    check("and the audit log records it too",
          con.execute("SELECT action, detail FROM audit_log WHERE project_id = ? AND entity_id = ?"
                      " AND action = 'release_emergency_hold'", (pid, call_id)).fetchone(),
          ("release_emergency_hold", "vendor patched it"))

    # ── merging two complaints ──────────────────────────────────────────────────────────────────────────────────
    merge_path = f"/api/v1/projects/{slug}/complaints/{source}/merge/{target}"
    status, _ = call("POST", merge_path)
    check("merging anonymously is 401", status, 401)
    status, _ = call("POST", merge_path, token=outsider)
    check("a non-member cannot merge", status in (403, 404), True)
    status, body = call("POST", f"/api/v1/projects/{slug}/complaints/{source}/merge/{foreign}", token=maintainer)
    check("a merge across projects is 400", status, 400)
    check("and the refusal says it cannot cross projects", "cross" in refusal(body).lower(), True)
    check("nothing was merged",
          con.execute("SELECT merged_into FROM complaints WHERE id = ?", (source,)).fetchone()[0], None)
    status, body = call("POST", f"/api/v1/projects/{slug}/complaints/{source}/merge/{source}", token=maintainer)
    check("merging a complaint into itself is 400", status, 400)
    check("and the refusal says so", "itself" in refusal(body), True)
    # The path's project and the row's project are two facts; only the first is checked by the path.
    status, _ = call("POST", f"/api/v1/projects/{other}/complaints/{source}/merge/{target}", token=outsider)
    check("a complaint reached through another project's URL is 404", status, 404)

    status, body = call("POST", merge_path, token=maintainer)
    check("the maintainer merges", status, 200)
    check("and is told so", (body or {}).get("status"), "merged")
    check("the source points at the target",
          con.execute("SELECT merged_into FROM complaints WHERE id = ?", (source,)).fetchone()[0], target)
    check("the source row is still there — a merge is a pointer, not a delete",
          con.execute("SELECT count(*) FROM complaints WHERE id = ?", (source,)).fetchone()[0], 1)
    check("the target's impacts now include the source's, without duplicating the shared one",
          sorted(row[0] for row in con.execute(
              "SELECT user_id FROM complaint_impacts WHERE complaint_id = ?", (target,)).fetchall()),
          sorted([reporter_id, outsider_id]))
    check("the merge is audited with the target",
          con.execute("SELECT detail FROM audit_log WHERE project_id = ? AND action = 'merge_complaints'"
                      " AND entity_id = ?", (pid, source)).fetchone()[0],
          f"merged into {target}")

    con.execute("delete from complaint_impacts where complaint_id in (?,?,?)", (source, target, foreign))
    con.execute("delete from complaints where project_id in (?,?)", (pid, other_pid))
    con.execute("delete from emergency_holds where project_id in (?,?)", (pid, other_pid))
    con.execute("delete from consensus_calls where project_id in (?,?)", (pid, other_pid))
    con.execute("delete from admin_ledger where project_id = ?", (pid,))
    con.execute("delete from audit_log where project_id in (?,?)", (pid, other_pid))
    con.execute("delete from members where project_id in (?,?)", (pid, other_pid))
    con.execute("delete from projects where id in (?,?)", (pid, other_pid))
    con.commit()

    passed = sum(1 for _, got, want in results if got == want)
    print(f"\n{passed}/{len(results)} ok", flush=True)
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())
