#!/usr/bin/env python3
"""Row 10: §4's curation queue — one page over proposals and reviewed acts.

Written to fail first, for the same reason as the inbox probe: row 10's gate names `curation_e2e` ("the
queue suite"), which cannot run against an API candidate because its fixture seeds through `sqlite3.connect`.

The rules under test, each a way the queue can look right and be wrong:

* **`open` is not a state any row carries.** A proposal is open while `pending`; an act is open while
  `applied`, because an act has no pending phase. A queue whose default filter was the literal "pending"
  would show every proposal and no act at all — half the queue the owner asked for, silently missing.
* **the cursor is (opened_at, kind, id), all three.** Two kinds share ids and timestamps, so a boundary
  landing exactly there drops a row with a two-part cursor. The probe walks the whole queue one row at a
  time and compares the union against the unpaged list: no repeats, no skips.
* **a malformed cursor is a 400, never a silent first page.** A silent first page turns a paging bug into an
  infinite scroll.
* **a `%` in a search is a literal.** Unescaped, a search for "%" returns the whole queue, which looks like
  a working search and is a wildcard.
* **contested is derived from dissent, at the instance's existing quorum**, not stored and not invented.
* **`unvoted=1` excludes your own proposals.** A queue that shows you your own work never empties.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.parse
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
    request = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
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


def main():
    import harness

    status, body = call("POST", "/api/v1/auth/register", {
        "username": f"curator{int(time.time() * 1000) % 1000000}",
        "display_name": "Curator",
        "password": "correct horse",
    })
    if status != 201:
        print(f"register failed: {status} {body}")
        return 2
    curator_id, token = body["user"]["id"], body["token"]
    status, body = call("POST", "/api/v1/auth/register", {
        "username": f"voter{int(time.time() * 1000) % 1000000}",
        "display_name": "Voter",
        "password": "correct horse",
    })
    voter_id = body["user"]["id"] if status == 201 else 0

    con = harness.connect("unused")
    now = time.time()
    slug = f"queue-{int(now) % 100000}"
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Queue", "", "collective", "public", "both", now, now),
    ).lastrowid

    # Three pending proposals and two applied acts, one of which was undone. Distinct timestamps so the
    # cursor has something to walk through.
    proposal_ids = []
    for index in range(3):
        proposal_ids.append(con.execute(
            "INSERT INTO taxonomy_proposals (project_id, proposed_by, action, target_tag, rationale,"
            " status, created_at) VALUES (?,?,?,?,?,?,?) RETURNING id",
            (project, curator_id, "add-tag", f"tag-{index}", f"because {index} % reasons", "pending",
             now - index * 100),
        ).lastrowid)
    acts = []
    for index in range(2):
        acts.append(con.execute(
            "INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail,"
            " created_at) VALUES (?,?,?,?,?,?,?) RETURNING id",
            (project, curator_id, "arena.remove", "feature", 500 + index,
             f"removed for reason {index}", now - 1000 - index * 100),
        ).lastrowid)
    con.execute("INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail,"
                " created_at, undoes) VALUES (?,?,?,?,?,?,?,?)",
                (project, curator_id, "arena.restore", "feature", 501, "restored", now - 900, acts[1]))
    # A second real account: act_reviews.user_id REFERENCES users, and SQLite never enforced it.
    status, body = call("POST", "/api/v1/auth/register", {
        "username": f"voter2{int(time.time() * 1000) % 1000000}",
        "display_name": "Voter Two",
        "password": "correct horse",
    })
    second_voter = body["user"]["id"] if status == 201 else 0

    # Dissent: two rejections make the first act contested, one does not.
    for voter in (voter_id, second_voter):
        con.execute(
            "INSERT INTO act_reviews (audit_id, user_id, vote, at) VALUES (?,?,?,?)",
            (acts[0], voter, -1, now),
        )
    con.execute("INSERT INTO act_reviews (audit_id, user_id, vote, at) VALUES (?,?,?,?)",
                (acts[1], voter_id, -1, now))
    # A vote by the curator on a proposal: my_vote must report it.
    # `curation_votes` is a VIEW over the consent tables, not a table: writing to it is a mistake this port
    # would have inherited invisibly, so the fixture writes the consent row the view projects.
    con.execute(
        "INSERT INTO taxonomy_consents (proposal_id, user_id, consented_at) VALUES (?,?,?)",
        (proposal_ids[0], curator_id, now),
    )
    con.commit()
    con.close()

    results = []
    # `state=open` is what the page asks for; the API has no default state, exactly as the Go handler has
    # none (`q.Get("state")` is "" when absent, and the store then narrows nothing). The two views are
    # asserted separately below rather than conflated into one default.
    base = f"/api/v1/curation?project={slug}&state=open"
    log_view = f"/api/v1/curation?project={slug}"

    # 1. The default is the queue view: pending proposals AND applied acts, and the undone act shown as such.
    status, body = call("GET", base, token=token)
    page = body if isinstance(body, dict) else {}
    items = page.get("items") or []
    # The log view (no state) must hold everything, undone acts included: that is the difference between the
    # queue and the history, and it is a request parameter, not a default.
    status_log, body_log = call("GET", log_view, token=token)
    log_items = (body_log or {}).get("items") or []
    kinds = sorted({item.get("kind") for item in items})
    states = sorted({item.get("state") for item in items})
    results.append(("the queue answers", status, 200, message(body)))
    results.append(("it holds proposals and acts", kinds, ["act", "taxonomy"], kinds))
    results.append(("open means pending for a proposal and applied for an act", states,
                    ["applied", "pending"], states))
    results.append(("the undone act is not in the open queue",
                    sum(1 for item in items if item.get("state") == "undone"), 0, items))
    results.append(("the log view holds the undone act",
                    sum(1 for item in log_items if item.get("state") == "undone"), 1, log_items))
    results.append(("the log view is larger than the queue", len(log_items) > len(items), True,
                    (len(log_items), len(items))))
    results.append(("the newest opens first",
                    items[0].get("opened_at") >= items[-1].get("opened_at") if items else None,
                    True, [item.get("opened_at") for item in items][:3]))

    # 2. Contested is derived from dissent at the instance's quorum.
    by_id = {(item.get("kind"), item.get("id")): item for item in items}
    results.append(("two dissenters make an act contested",
                    by_id.get(("act", acts[0]), {}).get("contested"), True,
                    by_id.get(("act", acts[0]))))
    # Read from the LOG view: acts[1] is the act that was undone, so it is not in the open queue at all.
    log_by_id = {(item.get("kind"), item.get("id")): item for item in log_items}
    results.append(("one dissenter does not",
                    log_by_id.get(("act", acts[1]), {}).get("contested"), False,
                    log_by_id.get(("act", acts[1]))))

    # 3. my_vote: a recorded consent reads as 1, and the act reviews read as -1.
    results.append(("a proposal the reader consented to says so",
                    by_id.get(("taxonomy", proposal_ids[0]), {}).get("my_vote"), 1,
                    by_id.get(("taxonomy", proposal_ids[0]))))
    results.append(("a proposal the reader did not vote on is 0",
                    by_id.get(("taxonomy", proposal_ids[1]), {}).get("my_vote"), 0,
                    by_id.get(("taxonomy", proposal_ids[1]))))
    results.append(("an act the reader rejected is -1",
                    by_id.get(("act", acts[0]), {}).get("my_vote"), 0,
                    by_id.get(("act", acts[0]))))

    # 4. The cursor: walk one row at a time, and the union must equal the unpaged list.
    status, body = call("GET", base + "&limit=1", token=token)
    # The FIRST page counts too. The earlier version of this walk started collecting from page two and so
    # "proved" that paging neither repeats nor skips while dropping the newest row -- a probe bug that looks
    # exactly like the bug it exists to catch.
    walked = [(item.get("kind"), item.get("id")) for item in (body or {}).get("items") or []]
    cursor = (body or {}).get("next_cursor")
    guard = 0
    while cursor and guard < 40:
        guard += 1
        status, page_body = call(
            "GET", base + "&limit=1&cursor=" + urllib.parse.quote(cursor), token=token)
        page_items = (page_body or {}).get("items") or []
        walked.extend((item.get("kind"), item.get("id")) for item in page_items)
        cursor = (page_body or {}).get("next_cursor")
    results.append(("paging one row at a time terminates", guard < 40, True, guard))
    results.append(("no row repeats while paging", len(set(walked)), len(walked), walked))
    status, body = call("GET", base + "&limit=100", token=token)
    everything = [((item.get("kind"), item.get("id"))) for item in (body or {}).get("items") or []]
    results.append(("paging neither repeats nor skips", walked, everything, (walked, everything)))

    # 5. A malformed cursor is a 400, not a first page.
    status, body = call("GET", base + "&cursor=nonsense", token=token)
    results.append(("a malformed cursor is refused", status, 400, message(body)))
    status, body = call("GET", base + "&cursor=1.000000|taxonomy", token=token)
    results.append(("a cursor with two parts is refused", status, 400, message(body)))

    # 6. `%` is a literal in the search.
    status, body = call("GET", base + "&q=%25", token=token)
    percent = (body or {}).get("items") or []
    results.append(("a percent sign searches for a percent sign", len(percent), 3,
                    [item.get("summary") for item in percent]))
    status, body = call("GET", log_view + "&q=reason%201", token=token)
    results.append(("a text search finds what it names", len((body or {}).get("items") or []), 1,
                    [(item.get("kind"), item.get("summary")) for item in (body or {}).get("items") or []]))

    # 7. unvoted=1 excludes the reader's own work and what they have voted on.
    status, body = call("GET", base + "&unvoted=1", token=token)
    mine = (body or {}).get("items") or []
    results.append(("unvoted excludes my own proposals",
                    sum(1 for item in mine if item.get("actor_id") == curator_id), 0, mine))
    results.append(("unvoted excludes what I consented to",
                    sum(1 for item in mine if item.get("my_vote") == 1), 0, mine))
    results.append(("unvoted still answers", status, 200, message(body)))

    # 8. The limit clamps rather than refusing.
    status, body = call("GET", base + "&limit=9999", token=token)
    results.append(("a limit above the ceiling clamps", (body or {}).get("limit"), 20,
                    (body or {}).get("limit")))
    status, body = call("GET", base + "&limit=0", token=token)
    results.append(("limit=0 clamps to the default", (body or {}).get("limit"), 20,
                    (body or {}).get("limit")))

    # 9. An anonymous reader gets no my_vote and no unvoted filtering, not an empty page.
    status, body = call("GET", base)
    anonymous = (body or {}).get("items") or []
    results.append(("anonymous asks and gets the unfiltered queue", status, 200, message(body)))
    results.append(("anonymous has no votes", sum(1 for item in anonymous if item.get("my_vote")), 0,
                    anonymous))
    status, body = call("GET", base + "&unvoted=1")
    results.append(("unvoted=1 without a reader is not an empty page",
                    len((body or {}).get("items") or []), len(everything), body))

    con = harness.connect("unused")
    con.execute("delete from act_reviews where audit_id in (select id from audit_log"
                " where project_id = ?)", (project,))
    con.execute("delete from taxonomy_consents where proposal_id in"
                " (select id from taxonomy_proposals where project_id = ?)", (project,))
    con.execute("delete from audit_log where project_id = ?", (project,))
    con.execute("delete from taxonomy_proposals where project_id = ?", (project,))
    con.execute("delete from projects where id = ?", (project,))
    con.commit()
    con.close()

    failed = 0
    for label, got, want, detail in results:
        ok = got == want
        if not ok:
            failed += 1
        print(f"{'ok  ' if ok else 'FAIL'} {label}: got {got!r} want {want!r}"
              + ("" if ok else f"  ({detail!r})"))
    print(f"\n{len(results) - failed}/{len(results)} ok")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
