#!/usr/bin/env python3
"""Row 11: §6.2's themes at the GO's own paths — proposing applies nothing, and a theme EXPIRES.

The frontend reads `/api/v1/projects/{id}/strategy/themes` (settings.js). A port that answered only the curation
queue would leave that page blank with a 404 while every probe passed, so the gate is the Go's route shapes, not
the queue's.

Nine rules, each a way a consensus route becomes a hand-steering dial:

1. proposing applies NOTHING — the live theme list is unchanged and no weight moves;
2. a tag that does not exist is refused, naming the taxonomy route, rather than invented as a side effect;
3. a weight outside [-2, 3] is refused;
4. a rationale is required;
5. one pending proposal per (project, tag, namespace) — the second is a 409, not a queue;
6. the proposer cannot consent to their own proposal;
7. a member with no qualifying contribution is not in the eligible population;
8. a consent below the threshold is RECORDED and answered 202 — 200 would say the roadmap moved;
9. a theme expires, a RENEWAL moves the same row's expiry forward rather than adding a second, and a charter with
   `theme_lifetime_days = 0` refuses rather than writing a theme born expired.
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


def message(body):
    return ((body or {}).get("error") or {}).get("message", "") if isinstance(body, dict) else ""


def register(prefix, stamp):
    status, body = call(
        "POST", "/api/v1/auth/register",
        {"username": f"{prefix}{stamp}", "display_name": prefix, "password": "correct horse"},
    )
    if status != 201 or not isinstance(body, dict):
        print(f"register {prefix} failed: {status} {body}")
        sys.exit(2)
    return body["user"]["id"], body["token"]


def cleanup(con, like):
    """Remove anything a previous, interrupted run left behind — the ledger's rule on probe leftovers."""
    for (project,) in con.execute("select id from projects where slug like ?", (like,)).fetchall():
        con.execute("delete from strategic_theme_consents where proposal_id in "
                    "(select id from strategic_theme_proposals where project_id = ?)", (project,))
        con.execute("delete from strategic_theme_proposals where project_id = ?", (project,))
        con.execute("delete from strategic_themes where project_id = ?", (project,))
        con.execute("delete from strategic_weight_consents where proposal_id in "
                    "(select id from strategic_weight_proposals where project_id = ?)", (project,))
        con.execute("delete from strategic_weight_proposals where project_id = ?", (project,))
        con.execute("delete from merge_requests where project_id = ?", (project,))
        con.execute("delete from feature_tags where feature_id in "
                    "(select id from features where project_id = ?)", (project,))
        con.execute("delete from features where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from members where project_id = ?", (project,))
        con.execute("delete from charters where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.execute("delete from tags where name like ?", (like,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    tag = f"probe-theme-{stamp}"
    slug_like = f"probe-theme-%"

    proposer_id, proposer_token = register("thprop", stamp)
    first_id, first_token = register("thvote", stamp)
    second_id, second_token = register("thvote2", stamp)
    third_id, third_token = register("thvote3", stamp)
    idle_id, idle_token = register("thidle", stamp)

    con = harness.connect("unused")
    cleanup(con, slug_like)
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"probe-theme-{stamp}", "Theme probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, effort, origin,"
        " strategic_weight, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (project, proposer_id, "A feature", "", "discussion", "M", "api", 1.0, now, now),
    ).lastrowid
    # `tags` has no `kind` and no `created_at`: a tag is (project_id, name, namespace), and a global tag is the
    # one whose project_id is NULL. Read the schema, do not assume it.
    con.execute("INSERT INTO tags (project_id, name, namespace) VALUES (NULL,?,?)", (tag, "topic"))
    # Three eligible collaborators (a merged merge request each) plus one member who has only joined.
    for user_id in (proposer_id, first_id, second_id, third_id, idle_id):
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
            (project, user_id, "contributor", now),
        )
    for user_id in (proposer_id, first_id, second_id, third_id):
        con.execute(
            "INSERT INTO merge_requests (project_id, feature_id, author_id, title, status,"
            " opened_at, closed_at) VALUES (?,?,?,?,?,?,?)",
            (project, feature, user_id, "merged work", "merged", now - 86400, now - 3600),
        )
    con.commit()

    base = f"/api/v1/projects/{project}/strategy"
    results = []

    def check(name, got, want):
        results.append((name, got, want))

    def live_themes():
        status, body = call("GET", f"{base}/themes", token=first_token)
        assert status == 200, (status, body)
        return body

    def proposal_count(project_id):
        return con.execute(
            "select count(*) from strategic_theme_proposals where project_id = ?", (project_id,)
        ).fetchone()[0]

    # 1-4: the request's own rules.
    status, body = call("POST", base + "/themes",
                        {"theme_tag": f"nope-{stamp}", "weight": 1.0, "rationale": "x"},
                        token=proposer_token)
    check("an unknown tag is refused", status, 400)
    check("naming the taxonomy route", "taxonomy/proposals" in message(body), True)

    status, body = call("POST", base + "/themes",
                        {"theme_tag": tag, "weight": 3.5, "rationale": "x"}, token=proposer_token)
    check("a weight above the bound is refused", status, 400)
    check("naming the bound", "between -2 and 3" in message(body), True)

    status, body = call("POST", base + "/themes",
                        {"theme_tag": tag, "weight": 1.0, "rationale": "   "}, token=proposer_token)
    check("a missing rationale is refused", status, 400)
    check("and says a rationale is required", "rationale is required" in message(body), True)

    # 1 + 5: a proposal applies nothing, and a second pending one is a conflict.
    before_themes = live_themes()
    status, body = call("POST", base + "/themes",
                        {"theme_tag": tag, "weight": 1.5, "rationale": "more of this please"},
                        token=proposer_token)
    check("a valid proposal is created", status, 201)
    theme_proposal = (body or {}).get("id")
    check("the live theme list is unchanged", live_themes(), before_themes)
    check("no theme row was written", con.execute(
        "select count(*) from strategic_themes where project_id = ?", (project,)).fetchone()[0], 0)
    status, body = call("POST", base + "/themes",
                        {"theme_tag": tag, "weight": 2.0, "rationale": "even more"},
                        token=proposer_token)
    check("a second pending proposal for the tag is refused", status, 409)
    check("and says one is pending", "already pending" in message(body), True)

    status, body = call("GET", base + "/theme-proposals", token=first_token)
    check("the proposal list answers", status, 200)
    check("with the proposal pending", any(
        row.get("id") == theme_proposal and row.get("status") == "pending" for row in body), True)

    # 6-8: who may consent, and what a consent that ratifies nothing means.
    status, body = call("POST", f"{base}/themes/{theme_proposal}/consent", token=proposer_token)
    check("the proposer cannot consent to their own proposal", status, 403)
    status, body = call("POST", f"{base}/themes/{theme_proposal}/consent", token=idle_token)
    check("a member with no contribution cannot consent", status, 403)
    status, body = call("POST", f"{base}/themes/{theme_proposal}/consent", token=first_token)
    check("the first consent is 202, not 200", status, 202)
    check("and says recorded but not ratified", (body or {}).get("ratified"), False)
    # FOUR eligible, not three: the proposer is in the population too — they simply may not consent to their own
    # proposal. The population is the denominator for quorum, so excluding the proposer from it would make a
    # three-person project think it has two people and never reach the floor.
    check("and reports the tally", ((body or {}).get("consents"), (body or {}).get("eligible")), (1, 4))
    check("no theme yet", con.execute(
        "select count(*) from strategic_themes where project_id = ?", (project,)).fetchone()[0], 0)

    # A repeated click is one voice.
    call("POST", f"{base}/themes/{theme_proposal}/consent", token=first_token)
    check("a repeated consent is still one voice", con.execute(
        "select count(*) from strategic_theme_consents where proposal_id = ?",
        (theme_proposal,)).fetchone()[0], 1)

    call("POST", f"{base}/themes/{theme_proposal}/consent", token=second_token)
    status, body = call("POST", f"{base}/themes/{theme_proposal}/consent", token=third_token)
    check("the third consent ratifies with 200", status, 200)
    check("and says so", (body or {}).get("ratified"), True)
    in_force = live_themes()
    check("the theme is in force", len(in_force), 1)
    check("with the proposed weight", in_force[0]["weight"], 1.5)
    check("and an expiry from the charter's 90 days",
          round((in_force[0]["expires_at"] - now) / 86400), 90)

    # 9: renewal moves the SAME row's expiry forward; it does not add a second.
    first_expiry = in_force[0]["expires_at"]
    con.commit()
    status, body = call("POST", base + "/themes",
                        {"theme_tag": tag, "weight": 2.5, "rationale": "renewed and raised"},
                        token=proposer_token)
    check("a renewal is a new proposal", status, 201)
    renewal = (body or {}).get("id")
    for token in (first_token, second_token, third_token):
        status, body = call("POST", f"{base}/themes/{renewal}/consent", token=token)
    check("the renewal ratifies", status, 200)
    after = live_themes()
    check("there is still ONE theme row", len(after), 1)
    check("with the new weight", after[0]["weight"], 2.5)
    check("and an expiry moved forward", after[0]["expires_at"] > first_expiry, True)

    # 9b: a charter with no lifetime refuses rather than writing a theme born expired.
    third_proposal = con.execute(
        "INSERT INTO strategic_theme_proposals (project_id, theme_tag, theme_namespace, proposed_by,"
        " weight_before, weight_after, rationale, status, created_at) VALUES (?,?,?,?,?,?,?,?,?)"
        " RETURNING id",
        (project, tag, "topic", proposer_id, 2.5, -1.0, "negative weight", "pending", now),
    ).lastrowid
    # An UPDATE is not enough: a project with NO charter row gets the defaults, so the statement has to create
    # the row. The first run of this probe asserted 400 and got 200 because the UPDATE touched zero rows.
    con.execute(
        "INSERT INTO charters (project_id, theme_lifetime_days) VALUES (?, 0)"
        " ON CONFLICT (project_id) DO UPDATE SET theme_lifetime_days = 0", (project,))
    con.commit()
    call("POST", f"{base}/themes/{third_proposal}/consent", token=first_token)
    call("POST", f"{base}/themes/{third_proposal}/consent", token=second_token)
    status, body = call("POST", f"{base}/themes/{third_proposal}/consent", token=third_token)
    check("a zero lifetime refuses instead of applying", status, 400)
    check("naming the reason", "born expired" in message(body), True)
    check("and the live weight is unchanged", live_themes()[0]["weight"], 2.5)
    check("and the proposal stays pending", con.execute(
        "select status from strategic_theme_proposals where id = ?", (third_proposal,)).fetchone()[0],
        "pending")

    # 10: withdrawal.
    status, body = call("POST", f"{base}/themes/{third_proposal}/withdraw", token=second_token)
    check("a stranger cannot withdraw", status, 403)
    status, body = call("POST", f"{base}/themes/{third_proposal}/withdraw", token=proposer_token)
    check("the proposer may withdraw", status, 204)
    check("and it is withdrawn", con.execute(
        "select status from strategic_theme_proposals where id = ?", (third_proposal,)).fetchone()[0],
        "withdrawn")

    # 11: the weight-proposal routes, same two rules that make them a vote rather than a dial.
    status, body = call("POST", base + "/weight-proposals",
                        {"feature_id": feature, "weight": 2.0, "rationale": "worth more"},
                        token=proposer_token)
    check("a weight proposal is created", status, 201)
    weight_proposal = (body or {}).get("id")
    check("and applies nothing", con.execute(
        "select strategic_weight from features where id = ?", (feature,)).fetchone()[0], 1.0)
    status, body = call("POST", f"{base}/weight-proposals/{weight_proposal}/consent",
                        token=proposer_token)
    check("the weight proposer cannot consent either", status, 403)
    status, body = call("GET", base + "/weight-proposals", token=first_token)
    check("the weight proposal list answers", status, 200)

    cleanup(con, slug_like)
    con.execute("DELETE FROM api_tokens WHERE user_id IN (?,?,?,?,?)",
                (proposer_id, first_id, second_id, third_id, idle_id))
    con.execute("DELETE FROM users WHERE id IN (?,?,?,?,?)",
                (proposer_id, first_id, second_id, third_id, idle_id))
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
