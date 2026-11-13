#!/usr/bin/env python3
"""Row 10: the inbox — and the rules that make a moderation notice safe to read.

Row 10's gate names `notifications_e2e`, which cannot run against an API candidate (its fixture seeds
through `sqlite3.connect`, like the five before it), so this probe is the API-level evidence the row can
actually produce. It is written to fail first.

The rules under test, each a way the endpoint can look right and be wrong:

* **the inbox is the reader's own.** Two accounts, two rows each; neither may see the other's. This is the
  rule with a privacy consequence, so it is checked in both directions rather than one.
* **marking somebody else's notification read is a 404, not a 403.** A distinct answer confirms the id is
  real, which is a fact about another person. The probe asserts the same status for "not yours" and "does
  not exist", because the point is that they are indistinguishable.
* **marking twice is not an error.** `coalesce(read_at, now())` keeps the first timestamp and the row still
  matches, so a control clicked twice answers 200 both times.
* **the badge counts the whole inbox, not the page.** A reader with 60 unread rows who asks for `limit=2`
  must still see 60, or the numeral lies in exactly the case that matters.
* **a bad limit is a clamp, not a 400.** The header asks for this on every page render.
* **the shape matches Go's `omitempty`.** `actor` and `project_slug` are absent, not empty, when they have
  nothing to say — a page that branches on their presence would behave differently between the two
  services if this drifted.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "tests", "e2e"))

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


def register(prefix):
    status, body = call("POST", "/api/v1/auth/register", {
        "username": f"{prefix}{int(time.time() * 1000) % 1000000}",
        "display_name": prefix,
        "password": "correct horse",
    })
    if status != 201 or not isinstance(body, dict):
        print(f"register {prefix} failed: {status} {body}")
        sys.exit(2)
    return body["user"]["id"], body["token"]


def main():
    import harness

    reader_id, reader_token = register("reader")
    other_id, other_token = register("other")

    con = harness.connect("unused")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"inbox-{int(now) % 100000}", "Inbox", "", "collective", "public", "both", now, now),
    ).lastrowid

    # Six notices for the reader, two for somebody else: enough to have a page smaller than the inbox.
    mined = []
    for index in range(6):
        mined.append(con.execute(
            "INSERT INTO notifications (user_id, kind, project_id, subject_id, title, reason, actor_id,"
            " created_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
            (reader_id, "feature_removed", project, 100 + index, f"feature {index} was removed",
             "duplicate of a shipped feature", other_id, now - index * 60),
        ).lastrowid)
    theirs = con.execute(
        "INSERT INTO notifications (user_id, kind, project_id, subject_id, title, reason, actor_id,"
        " created_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (other_id, "complaint_rejected", project, 900, "your complaint was rejected",
         "not reproducible", reader_id, now),
    ).lastrowid
    # A notice with no actor: the page renders "a moderator", and the JSON key must be ABSENT.
    #
    # The obvious companion case -- a notice whose project row is gone -- is UNREACHABLE here:
    # `fk_notifications_project_id` forbids an orphan, so `coalesce(p.slug, '')` is defensive code with no
    # route into it. Asserting it would have meant inventing a state the schema refuses.
    con.execute("INSERT INTO notifications (user_id, kind, project_id, subject_id, title, reason, actor_id,"
                " created_at) VALUES (?,?,?,?,?,?,?,?)",
                (reader_id, "feature_removed", project, 901, "gone actor", "see above", None, now - 900))
    con.commit()
    con.close()

    results = []

    # 1. Auth.
    status, body = call("GET", "/api/v1/notifications")
    results.append(("the inbox without a token", status, 401, message(body)))

    # 2. The reader's own inbox, newest first.
    status, body = call("GET", "/api/v1/notifications", token=reader_token)
    inbox = body if isinstance(body, dict) else {}
    rows = inbox.get("notifications") or []
    results.append(("the inbox answers", status, 200, inbox.get("unread")))
    results.append(("it holds the reader's rows, newest first", len(rows), 7,
                    [row.get("title") for row in rows]))
    results.append(("the newest is first", (rows[0] or {}).get("title") if rows else None,
                    "feature 0 was removed", (rows[0] or {}).get("title") if rows else None))
    results.append(("the badge counts the whole inbox", inbox.get("unread"), 7, inbox.get("unread")))
    results.append(("no other reader's row is in it",
                    sum(1 for row in rows if row.get("title") == "your complaint was rejected"), 0, rows))

    # 3. omitempty: a missing actor and a missing project are ABSENT keys, not empty strings.
    gone = next((row for row in rows if row.get("subject_id") == 901), {})
    results.append(("an absent actor's key is absent", "actor" in gone, False, gone.get("actor")))
    # Not the counterpart of the line above: the schema refuses the state that would empty this key.
    results.append(("a live project's slug is a non-empty string",
                    isinstance(gone.get("project_slug"), str) and gone.get("project_slug") != "",
                    True, gone.get("project_slug")))
    present = next((row for row in rows if row.get("subject_id") == 100), {})
    # The username carries a timestamp suffix so repeated runs do not collide, so this asserts the prefix
    # rather than an exact string: the point is that the actor is NAMED, not which name the fixture chose.
    results.append(("a live actor is named",
                    1 if str(present.get("actor", "")).startswith("other") else 0, 1,
                    present.get("actor")))
    results.append(("a live project is named",
                    isinstance(present.get("project_slug"), str) and present.get("project_slug") != "",
                    True, present.get("project_slug")))
    results.append(("read starts false", present.get("read"), False, present.get("read")))

    # 4. The page is smaller than the inbox, and the badge does not follow the page.
    status, body = call("GET", "/api/v1/notifications?limit=2", token=reader_token)
    page = body if isinstance(body, dict) else {}
    results.append(("a page honours the limit", len(page.get("notifications") or []), 2,
                    page.get("notifications")))
    results.append(("the badge still counts the whole inbox", page.get("unread"), 7, page.get("unread")))
    status, body = call("GET", "/api/v1/notifications?limit=0", token=reader_token)
    results.append(("limit=0 clamps rather than refusing", status, 200, message(body)))
    status, body = call("GET", "/api/v1/notifications?limit=9999", token=reader_token)
    results.append(("limit above the ceiling clamps", status, 200, message(body)))

    # 5. Marking read: own, idempotent, and never somebody else's.
    status, body = call("POST", f"/api/v1/notifications/{mined[0]}/read", token=reader_token)
    results.append(("mark one read", status, 200, message(body)))
    status, body = call("POST", f"/api/v1/notifications/{mined[0]}/read", token=reader_token)
    results.append(("marking it again is not an error", status, 200, message(body)))
    status, body = call("GET", "/api/v1/notifications", token=reader_token)
    results.append(("the badge drops by one", (body or {}).get("unread"), 6, (body or {}).get("unread")))
    first = next((row for row in (body or {}).get("notifications") or []
                  if row.get("id") == mined[0]), {})
    results.append(("the row reads as read", first.get("read"), True, first.get("read")))

    status, body = call("POST", f"/api/v1/notifications/{theirs}/read", token=reader_token)
    results.append(("marking somebody else's is a 404", status, 404, message(body)))
    status, body = call("POST", "/api/v1/notifications/987654321/read", token=reader_token)
    results.append(("a row that does not exist answers the same", status, 404, message(body)))
    status, body = call("GET", "/api/v1/notifications", token=other_token)
    results.append(("their row is still unread", (body or {}).get("unread"), 1,
                    (body or {}).get("unread")))

    # 6. Read-all clears only the reader's own badge.
    status, body = call("POST", "/api/v1/notifications/read-all", token=reader_token)
    results.append(("read-all answers how many it cleared", (body or {}).get("read"), 6,
                    (body or {}).get("read")))
    status, body = call("GET", "/api/v1/notifications", token=reader_token)
    results.append(("the reader's badge is clear", (body or {}).get("unread"), 0,
                    (body or {}).get("unread")))
    status, body = call("POST", "/api/v1/notifications/read-all", token=reader_token)
    results.append(("read-all on an empty badge clears nothing", (body or {}).get("read"), 0,
                    (body or {}).get("read")))
    status, body = call("GET", "/api/v1/notifications", token=other_token)
    results.append(("the other reader's badge is untouched", (body or {}).get("unread"), 1,
                    (body or {}).get("unread")))

    # Cleanup, deepest first: every probe that owns rows deletes them, and the order is the FK graph.
    con = harness.connect("unused")
    con.execute("delete from notifications where project_id = ?", (project,))
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
