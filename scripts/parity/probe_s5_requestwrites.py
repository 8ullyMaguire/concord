#!/usr/bin/env python3
"""Row 11b part 39 (B1): request tags, answers, and the answer vote.

The rules, each one a defect in the service being ported:

1. tags are **author-only and not a role check** — a project owner who did not ask the question may not rewrite its
   deal-breakers, and the author themself must not be refused for having an ordinary role;
2. setting tags **replaces**; a merge cannot remove one, and whitespace-only names are dropped rather than stored as " ";
3. the tag vocabulary is **instance-wide**: the same name used on a second request reuses one `tags` row;
4. an answer is attributed to a **project**, and a second answer from the same project is refused (one answer per project
   per request — that is what the cross-project rating exists for);
5. an **instance-wide** request has no project to inherit: the answer must name one, else 400. The reference passed 0
   through and answered 500;
6. a vote names **two answers and an outcome**, never a direction, and both must belong to this request — a foreign answer
   is **403**, an unknown one **404**;
7. **you cannot vote on an answer you proposed** (§5.1), and that check runs before the write;
8. **skip records the vote and moves no rating**; `a` moves the winner up and the loser down through Glicko, not by a flat
   increment;
9. `weight` in the body is **ignored** — §8.3's "money cannot buy weight"; the weight is computed from reputation, role
   and account age.
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


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    author_id, author_token = register(f"reqauthor{stamp}")
    voter_id, voter_token = register(f"reqvoter{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-reqwrite-{stamp}"
    cleanup(con, f"{slug}%")
    ids = {}
    for name, visibility in (("a", "public"), ("b", "public"), ("c", "private")):
        ids[name] = con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
            " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
            (f"{slug}-{name}", name.upper(), "", "collective", visibility, "both", now, now),
        ).lastrowid
    con.commit()
    project_a, project_b, project_c = ids["a"], ids["b"], ids["c"]

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    title = f"Camera shutter sticks at 1/500 {stamp}"
    status, request = call("POST", f"/api/v1/projects/{project_a}/requests",
                           {"title": title, "body": "Only below 10C."}, token=author_token)
    check("a request to work with", status, 201)
    request_id = (request or {}).get("id")

    # ── tags ────────────────────────────────────────────────────────────────────────────────────────────────────────
    status, tagged = call("PUT", f"/api/v1/requests/{request_id}/tags",
                          {"tags": ["shutter", "mechanical"]}, token=author_token)
    check("the author may set tags", status, 200)
    check("and the answer is what was stored", (tagged or {}).get("tags"), ["mechanical", "shutter"])

    status, tagged = call("PUT", f"/api/v1/requests/{request_id}/tags",
                          {"tags": ["shutter", "   "]}, token=author_token)
    check("setting them again REPLACES the list", (tagged or {}).get("tags"), ["shutter"])
    check("and a whitespace-only name is not stored", "   " in ((tagged or {}).get("tags") or []), False)

    status, _ = call("PUT", f"/api/v1/requests/{request_id}/tags", {"tags": []}, token=author_token)
    check("an empty list clears them", status, 200)

    status, other = call("POST", "/api/v1/requests",
                         {"title": f"Battery drains overnight {stamp}", "body": ""}, token=author_token)
    other_id = (other or {}).get("id")
    call("PUT", f"/api/v1/requests/{request_id}/tags", {"tags": ["shutter"]}, token=author_token)
    call("PUT", f"/api/v1/requests/{other_id}/tags", {"tags": ["shutter"]}, token=author_token)
    same = con.execute("SELECT count(*) FROM tags WHERE name = ?", (f"shutter",)).fetchone()[0]
    check("a tag name used on two requests is ONE vocabulary row", same, 1)

    status, _ = call("PUT", f"/api/v1/requests/{request_id}/tags", {"tags": ["x"]}, token=voter_token)
    check("a stranger may not set them", status, 403)
    status, _ = call("PUT", f"/api/v1/requests/{request_id}/tags", {"tags": ["x"]})
    check("nor may an anonymous caller", status, 401)
    status, _ = call("PUT", "/api/v1/requests/99999999/tags", {"tags": ["x"]}, token=author_token)
    check("an unknown request is 404", status, 404)

    # ── answers ─────────────────────────────────────────────────────────────────────────────────────────────────────
    status, answer_a = call("POST", f"/api/v1/requests/{request_id}/answers",
                            {"body": "Replace the shutter unit."}, token=author_token)
    check("a project may answer", status, 201)
    answer_a = answer_a or {}
    check("the answer is active", answer_a.get("status"), "active")
    check("with a fresh rating", (answer_a.get("elo_r"), answer_a.get("elo_rd")), (1500.0, 350.0))
    check("attributed to the answering PROJECT", answer_a.get("project_id"), project_a)
    check("and the caller is recorded as the proposer", answer_a.get("proposed_by"), author_id)

    status, again = call("POST", f"/api/v1/requests/{request_id}/answers",
                         {"body": "Again."}, token=author_token)
    check("the same project answering twice is refused", status, 409)

    status, answer_b = call("POST", f"/api/v1/requests/{request_id}/answers",
                            {"body": "Send it in for service.", "project_id": project_b}, token=author_token)
    check("another project may answer", status, 201)
    answer_b = answer_b or {}
    check("attributed to that project", answer_b.get("project_id"), project_b)
    check("with an empty body allowed, as the reference allows", (answer_b.get("body") or ""), "Send it in for service.")

    status, listed = call("GET", f"/api/v1/requests/{request_id}/answers", token=author_token)
    check("both answers are listed", len(listed or []), 2)

    # ── the instance-wide case ──────────────────────────────────────────────────────────────────────────────────────
    status, instance = call("POST", "/api/v1/requests",
                            {"title": f"Firmware update bricked the lens {stamp}", "body": ""}, token=author_token)
    instance_id = (instance or {}).get("id")
    check("an instance-wide request", status, 201)
    status, _ = call("POST", f"/api/v1/requests/{instance_id}/answers", {"body": "?"}, token=author_token)
    check("answering it WITHOUT naming a project is 400, not 500", status, 400)
    status, inst_answer = call("POST", f"/api/v1/requests/{instance_id}/answers",
                               {"body": "Known bug.", "project_id": project_b}, token=author_token)
    check("naming a project works", status, 201)
    check("and is attributed to it", (inst_answer or {}).get("project_id"), project_b)

    # ── the vote ────────────────────────────────────────────────────────────────────────────────────────────────────
    before = {answer["id"]: (answer["elo_r"], answer["elo_rd"]) for answer in call(
        "GET", f"/api/v1/requests/{request_id}/answers", token=voter_token)[1]}

    status, voted = call("POST", f"/api/v1/answers/{request_id}/vote",
                         {"answer_a": answer_a["id"], "answer_b": answer_b["id"],
                          "outcome": "a", "weight": 999}, token=voter_token)
    check("a pairwise vote is accepted", status, 200)
    voted = voted or {}
    check("the response says voted", voted.get("status"), "voted")
    check("the 999 in the body is IGNORED", voted.get("weight") != 999, True)
    after = {answer["id"]: (answer["elo_r"], answer["elo_rd"]) for answer in voted.get("answers") or []}
    check("the winner moved up", after.get(answer_a["id"], (0,))[0] > before[answer_a["id"]][0], True)
    check("the loser moved down", after.get(answer_b["id"], (0,))[0] < before[answer_b["id"]][0], True)
    check("and its deviation narrowed", after.get(answer_a["id"], (0, 0))[1] < before[answer_a["id"]][1], True)

    # `unique (voter_id, answer_a, answer_b)`: one comparison of a given ORDERED pair per voter. Repeating the pair is
    # refused (the reference lets the constraint surface as a 500; the port names it). So the skip goes on the reversed
    # pair, which is a different comparison.
    status, _ = call("POST", f"/api/v1/answers/{request_id}/vote",
                     {"answer_a": answer_a["id"], "answer_b": answer_b["id"], "outcome": "b"},
                     token=voter_token)
    check("voting the same ordered pair twice is refused", status, 409)
    status, skipped = call("POST", f"/api/v1/answers/{request_id}/vote",
                           {"answer_a": answer_b["id"], "answer_b": answer_a["id"],
                            "outcome": "skip"}, token=voter_token)
    check("a skip is recorded on the reversed pair", status, 200)
    after_skip = {a["id"]: a["elo_r"] for a in (skipped or {}).get("answers") or []}
    check("and moves nothing", after_skip.get(answer_a["id"]), after.get(answer_a["id"], (None,))[0])

    status, _ = call("POST", f"/api/v1/answers/{request_id}/vote",
                     {"answer_a": answer_a["id"], "answer_b": answer_b["id"], "outcome": "b"},
                     token=author_token)
    check("you cannot vote on an answer you proposed", status, 403)
    status, _ = call("POST", f"/api/v1/answers/{request_id}/vote",
                     {"answer_a": answer_a["id"], "answer_b": answer_a["id"], "outcome": "a"},
                     token=voter_token)
    check("an answer cannot be compared with itself", status, 400)
    status, _ = call("POST", f"/api/v1/answers/{request_id}/vote",
                     {"answer_a": answer_a["id"], "answer_b": answer_b["id"], "outcome": "banana"},
                     token=voter_token)
    check("an unknown outcome is 400", status, 400)

    status, _ = call("POST", f"/api/v1/requests/{instance_id}/answers",
                     {"body": "Third.", "project_id": project_c}, token=author_token)
    check("answering from a project the caller cannot read is 404", status, 404)
    # Answer the OTHER request from B, then name that answer in a vote on this request: it exists and is readable, so the
    # refusal must be a permission one, not a missing row.
    status, other_answer = call("POST", f"/api/v1/requests/{other_id}/answers",
                                {"body": "Not this one.", "project_id": project_b}, token=author_token)
    check("an answer on another request exists", status, 201)
    status, _ = call("POST", f"/api/v1/answers/{request_id}/vote",
                     {"answer_a": answer_a["id"], "answer_b": (other_answer or {}).get("id"),
                      "outcome": "a"}, token=voter_token)
    check("an answer from another request is 403", status, 403)
    status, _ = call("POST", f"/api/v1/answers/{request_id}/vote",
                     {"answer_a": answer_a["id"], "answer_b": 99999999, "outcome": "a"}, token=voter_token)
    check("an unknown answer is 404", status, 404)
    status, _ = call("POST", f"/api/v1/answers/{request_id}/vote",
                     {"answer_a": answer_a["id"], "answer_b": answer_b["id"], "outcome": "a"})
    check("an anonymous vote is 401", status, 401)

    rows = con.execute(
        "SELECT outcome, weight FROM request_answer_votes WHERE request_id = ? ORDER BY id", (request_id,)
    ).fetchall()
    check("every accepted vote left a row, and the skip is one of them", [r[0] for r in rows], ["a", "skip"])
    check("the stored weight is the computed one, not 999", all(0 < r[1] < 999 for r in rows), True)

    # `cleanup` removes the projects, their requests, the answers, the votes and the filings' vectors. Two things it
    # cannot know about are the tag rows this probe created (they are instance-wide vocabulary, not project data) and any
    # leftover link rows from an earlier interrupted run of this same probe.
    cleanup(con, f"{slug}%")
    con.execute(
        "DELETE FROM request_tags WHERE tag_id IN (SELECT id FROM tags WHERE name IN ('shutter','mechanical','x'))"
    )
    con.execute("DELETE FROM tags WHERE name IN ('shutter','mechanical','x')")
    con.commit()
    for user in (author_id, voter_id):
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


def cleanup(con, like):
    projects = [row[0] for row in con.execute("select id from projects where slug like ?", (like,)).fetchall()]
    if not projects:
        return
    marks = ",".join("?" * len(projects))
    params = tuple(projects)
    requests = [row[0] for row in con.execute(
        f"select id from requests where project_id in ({marks})", params).fetchall()]
    # An answer can name a project that is not its request's project — that is the whole point of one-answer-per-project —
    # so a request reachable only through one of these projects' answers must be cleaned up too.
    requests += [row[0] for row in con.execute(
        f"select distinct request_id from request_answers where project_id in ({marks})", params).fetchall()]
    requests = sorted(set(requests))
    if requests:
        rmarks = ",".join("?" * len(requests))
        rparams = tuple(requests)
        # The filing path indexes every new request, so its vectors must go before the rows they point at.
        con.execute(f"delete from embeddings where kind = 'request' and entity_id in ({rmarks})", rparams)
        con.execute(f"delete from request_answer_votes where request_id in ({rmarks})", rparams)
        con.execute(f"delete from request_tags where request_id in ({rmarks})", rparams)
        con.execute(f"delete from request_answers where request_id in ({rmarks})", rparams)
        con.execute(f"delete from requests where id in ({rmarks})", rparams)
    con.execute(f"delete from request_answers where project_id in ({marks})", params)
    con.execute(f"delete from embeddings where project_id in ({marks})", params)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


if __name__ == "__main__":
    sys.exit(main())
