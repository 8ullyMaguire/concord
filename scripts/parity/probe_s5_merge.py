#!/usr/bin/env python3
"""Row 11d part 53 (B6b): merge requests — §5.7's gate and the one destructive transition.

`execute` ships a feature and closes its complaints. The properties asserted here are the ones that keep that from happening
twice, from the wrong project, or without the confirmation the charter demands:

1. **the request must belong to the project in the URL** — otherwise any member of any project could land any request by id, and
   the answer to a foreign id is 404, never 403 (a 403 confirms the id is real somewhere else);
2. **`open` is the only state that can move, once** — a second execute used to re-run the whole landing, and a reject after a
   merge flipped a landed change to rejected;
3. **the gate's arithmetic is `governance::check_merge_gate`**, and `needed` depends on how many collaborators are eligible — so
   the count is computed, not guessed;
4. **an approval counts only if its author could still hold the gate open**: never the request's own author, and only an
   eligible collaborator. A row in the table is not a confirmation, and a page that counted rows would show a filled quorum the
   server refuses to honour;
5. **a shortfall names which condition failed** — "gate not met" leaves a reader unable to tell whether they need another
   confirmation or a reviewer.
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
    mrs = [row[0] for row in con.execute(
        f"select id from merge_requests where project_id in ({marks})", params).fetchall()]
    if mrs:
        mmarks = ",".join("?" * len(mrs))
        con.execute(f"delete from merge_approvals where mr_id in ({mmarks})", tuple(mrs))
    features = [row[0] for row in con.execute(
        f"select id from features where project_id in ({marks})", params).fetchall()]
    if features:
        fmarks = ",".join("?" * len(features))
        con.execute(f"delete from feature_complaints where feature_id in ({fmarks})", tuple(features))
    # Complaints are deleted only after the links that name them: `feature_complaints` has no cascade, and the FK is the
    # reason a probe that cleans up in the wrong order fails at the END of a green run.
    complaints = [row[0] for row in con.execute(
        f"select id from complaints where project_id in ({marks})", params).fetchall()]
    con.execute(f"delete from merge_requests where project_id in ({marks})", params)
    con.execute(f"delete from features where project_id in ({marks})", params)
    if complaints:
        cmarks = ",".join("?" * len(complaints))
        con.execute(f"delete from feature_complaints where complaint_id in ({cmarks})", tuple(complaints))
        con.execute(f"delete from complaints where id in ({cmarks})", tuple(complaints))
    con.execute(f"delete from reputation_events where project_id in ({marks})", params)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from charters where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    author_id, author_token = register(f"mauthor{stamp}")
    reviewer_id, reviewer_token = register(f"mreviewer{stamp}")
    plain_id, plain_token = register(f"mplain{stamp}")
    stranger_id, stranger_token = register(f"mstranger{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-merge-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Merge probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    other_project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-other", "Elsewhere", "", "collective", "public", "both", now, now),
    ).lastrowid
    for user, role in ((author_id, "contributor"), (reviewer_id, "reviewer"), (plain_id, "user")):
        con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                    (project, user, role, now))

    def make_feature(label):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?) RETURNING id",
            (project, author_id, label, "", "draft", now, now),
        ).lastrowid

    feature = make_feature("Land the thing")
    kept_feature = make_feature("Refused thing")
    complaint = con.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, plain_id, "It hurts", "", "open", now, now),
    ).lastrowid
    # `feature_complaints` is a pure link table: two columns, no timestamps of its own.
    con.execute("INSERT INTO feature_complaints (feature_id, complaint_id) VALUES (?,?)",
                (feature, complaint))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    path = f"/api/v1/projects/{slug}/merge"

    # ── listing and creating ───────────────────────────────────────────────────────────────────────────────────
    status, listed = call("GET", path)
    check("the list is an empty array, not null", (status, listed), (200, []))
    status, _ = call("POST", path, {"feature_id": feature})
    check("opening one anonymously is 401", status, 401)
    status, _ = call("POST", path, {"feature_id": feature}, token=stranger_token)
    check("a non-member is refused", status, 403)
    status, _ = call("POST", path, {}, token=author_token)
    check("a request with no feature is 400", status, 400)
    status, _ = call("POST", path, {"feature_id": 99999999}, token=author_token)
    check("a feature that does not exist is 404", status, 404)
    foreign_feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (other_project, author_id, "Not yours", "", "draft", now, now),
    ).lastrowid
    con.commit()
    status, _ = call("POST", path, {"feature_id": foreign_feature}, token=author_token)
    check("somebody else's feature is 404", status, 404)
    status, mr = call("POST", path, {"feature_id": feature}, token=author_token)
    check("a contributor may open one", status, 201)
    mr = mr or {}
    check("the title defaults to the feature's", mr.get("title"), "Land the thing")
    check("and it is open with no close time",
          (mr.get("status"), mr.get("closed_at")), ("open", None))

    status, listed = call("GET", path)
    check("the list now holds it, with its gate",
          (status, [row["request"]["id"] for row in (listed or [])], "gate" in ((listed or [{}])[0])),
          (200, [mr.get("id")], True))
    check("and an empty project's gate needs a reviewer before anything else",
          ((listed or [{}])[0].get("gate") or {}).get("reviewer_ok"),
          False)

    # ── the gate ───────────────────────────────────────────────────────────────────────────────────────────────
    status, body = call("PUT", f"{path}/{mr['id']}/execute", {}, token=author_token)
    check("executing an unconfirmed request is 409", status, 409)
    check("and the refusal names the failing condition",
          "reviewer approval is required" in json.dumps(body or {}), True)

    status, _ = call("PUT", f"{path}/{mr['id']}/approve", {}, token=author_token)
    check("the author cannot approve their own request", status, 403)
    status, _ = call("PUT", f"{path}/{mr['id']}/approve", {}, token=stranger_token)
    check("a non-member cannot approve", status, 403)
    status, _ = call("PUT", f"{path}/{mr['id']}/approve", {}, token=plain_token)
    check("a `user`-role member cannot approve", status, 403)

    status, body = call("PUT", f"{path}/{mr['id']}/approve", {}, token=reviewer_token)
    check("a reviewer may approve", status, 200)
    gate = (body or {}).get("gate") or {}
    check("their approval makes them eligible and counts",
          (gate.get("approvals"), gate.get("reviewer_ok")), (1, True))
    check("the quorum is one of one eligible collaborator",
          (gate.get("eligible"), gate.get("needed"), gate.get("quorum_ok")), (1, 1, True))
    check("so the gate passes", gate.get("passed"), True)

    # ── execute ────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("PUT", f"{path}/{mr['id']}/execute", {}, token=stranger_token)
    check("a non-member cannot execute", status, 403)
    # Two different refusals, both correct, and worth separating: a caller who is not a contributor of the project named in the
    # URL is refused on their *own* role (403, which says nothing about the request), while a genuine contributor of another
    # project reaching for this request by id gets 404 — the id is not confirmed to exist anywhere.
    status, _ = call("PUT", f"/api/v1/projects/{slug}-other/merge/{mr['id']}/execute", {},
                     token=author_token)
    check("a non-contributor of the URL's project is refused on their role", status, 403)
    other_id, other_token = register(f"mother{stamp}")
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (other_project, other_id, "maintainer", now))
    con.commit()
    status, _ = call("PUT", f"/api/v1/projects/{slug}-other/merge/{mr['id']}/execute", {},
                     token=other_token)
    check("but another project's contributor gets 404, not a confirmation the id is real", status, 404)
    status, body = call("PUT", f"{path}/{mr['id']}/execute", {}, token=author_token)
    check("a gated request executes", status, 200)
    row = con.execute("SELECT status, closed_at IS NOT NULL FROM merge_requests WHERE id = ?",
                      (mr["id"],)).fetchone()
    check("the request is merged and closed", (row[0], row[1]), ("merged", True))
    check("the feature shipped",
          con.execute("SELECT status FROM features WHERE id = ?", (feature,)).fetchone()[0], "shipped")
    check("and the complaint linked to it closed",
          con.execute("SELECT status FROM complaints WHERE id = ?", (complaint,)).fetchone()[0], "closed")
    check("the landing is logged as a quorum merge",
          con.execute("SELECT count(*) FROM audit_log WHERE project_id = ? AND entity_id = ?"
                      " AND action = 'merge' AND detail = 'merged'", (project, mr["id"])).fetchone()[0], 1)

    # The REASON matters, not only the status. Merging makes the request's author an eligible collaborator (a merged merge
    # request *is* a qualifying contribution), so the quorum rises from 1-of-1 to 1-of-2 — and a second execute would be refused
    # by the *gate* while reporting a shortfall that reads like "go and get another confirmation". It passed a status-only
    # assertion either way; the message is what distinguishes "you cannot do this" from "you may do this, later".
    status, body = call("PUT", f"{path}/{mr['id']}/execute", {}, token=author_token)
    check("executing twice is 409 because it is already merged, not because the gate moved",
          (status, "already merged" in json.dumps(body or {})), (409, True))
    status, body = call("PUT", f"{path}/{mr['id']}/reject", {}, token=author_token)
    check("and a landed change cannot be rejected", (status, "already merged" in json.dumps(body or {})),
          (409, True))

    # ── reject ─────────────────────────────────────────────────────────────────────────────────────────────────
    status, second = call("POST", path, {"feature_id": kept_feature, "title": "Second"}, token=author_token)
    second = second or {}
    status, _ = call("PUT", f"{path}/{second['id']}/reject", {}, token=author_token)
    check("an open request can be rejected", status, 200)
    check("the feature was NOT shipped",
          con.execute("SELECT status FROM features WHERE id = ?", (kept_feature,)).fetchone()[0], "draft")
    status, _ = call("PUT", f"{path}/{second['id']}/execute", {}, token=author_token)
    check("and a rejected request cannot then be executed", status, 409)

    # ── the counted approval is not the row ────────────────────────────────────────────────────────────────────
    # `plain` has role `user`: their row can exist (it cannot — approve refuses), which is the point. A *member* who is not
    # eligible gets the same 403, so the only way to have an uncounted row is a member whose eligibility lapsed afterwards.
    lapsed_feature = make_feature("Lapsed")
    con.commit()
    status, third = call("POST", path, {"feature_id": lapsed_feature}, token=author_token)
    third = third or {}
    status, _ = call("PUT", f"{path}/{third['id']}/approve", {}, token=reviewer_token)
    check("the reviewer approves the third request", status, 200)
    con.execute("UPDATE members SET role = 'user' WHERE project_id = ? AND user_id = ?",
                (project, reviewer_id))
    con.commit()
    status, _ = call("PUT", f"{path}/{third['id']}/execute", {}, token=author_token)
    check("an approval by somebody who has since dropped below contributor does not carry the merge",
          status, 409)
    gate = (call("GET", path)[1] or [])
    gate = [row for row in gate if row["request"]["id"] == third.get("id")]
    gate = (gate[0]["gate"] if gate else {})
    check("the row exists and is not counted",
          (con.execute("SELECT count(*) FROM merge_approvals WHERE mr_id = ?",
                       (third["id"],)).fetchone()[0], gate.get("approvals")), (1, 0))
    check("so the gate refuses the merge", gate.get("passed"), False)

    cleanup(con, f"{slug}%")
    con.commit()
    con.close()
    for user in (author_id, reviewer_id, plain_id, stranger_id, other_id):
        con2 = harness.connect("unused")
        con2.execute("DELETE FROM api_tokens WHERE user_id = ?", (user,))
        con2.execute("DELETE FROM users WHERE id = ?", (user,))
        con2.commit()
        con2.close()

    failures = sum(1 for _, got, want in results if got != want)
    print(f"\n{len(results) - failures}/{len(results)} ok")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
