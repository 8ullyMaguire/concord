#!/usr/bin/env python3
"""Row 10: moderation acts — take an idea out of an arena, and put it back.

Three things make this the most dangerous surface in the product, and the probe checks all three because each
one is a hole the database would happily allow:

1. **An act states its reason.** An act that removes somebody's standing and explains nothing is
   indistinguishable from a grudge, and the log is read by the person on the other end of it.
2. **The prior state is written INTO the log, and the restore replays it from there** — never from the
   request. A restore that took the values from the caller would be a second way to write a rating.
3. **Refusals the schema permits**: a shipped feature (not competing any more; "rejecting" it would rewrite
   the record of something that happened), a baseline entry (the thing everything else is scored against),
   and a second reversal of the same act (the log is append-only).
"""
import json
import os
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
    request = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            raw = response.read()
            if not raw:
                return response.status, None
            try:
                return response.status, json.loads(raw)
            except ValueError:
                # Not every 200 is JSON: an extractor rejection would be axum's plain text, and a probe that
                # crashes on it learns less than one that prints it.
                return response.status, raw.decode("utf-8", "replace")
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, json.loads(raw) if raw else None
        except ValueError:
            return error.code, raw.decode("utf-8", "replace")


def message(body):
    if not isinstance(body, dict):
        return ""
    return (body.get("error") or {}).get("message", "") if isinstance(body.get("error"), dict) else ""


def register(prefix):
    status, body = call("POST", "/api/v1/auth/register", {
        "username": f"{prefix}{int(time.time() * 1000) % 1000000}",
        "display_name": prefix,
        "password": "correct horse",
    })
    if status != 201:
        print(f"register {prefix} failed: {status} {body}")
        sys.exit(2)
    return body["user"]["id"], body["token"]


def main():
    import harness

    reviewer_id, reviewer_token = register("reviewer")
    member_id, member_token = register("member")
    author_id, author_token = register("author")

    con = harness.connect("unused")
    now = time.time()
    slug = f"mod-{int(now) % 100000}"
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "mod", "", "collective", "public", "both", now, now),
    ).lastrowid
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (project, reviewer_id, "reviewer", now))
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (project, member_id, "contributor", now))
    arena = con.execute(
        "INSERT INTO arenas (type, project_id, question, created_at) VALUES (?,?,?,?) RETURNING id",
        ("alternatives", project, "which one?", now),
    ).lastrowid
    feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, author_id, "an idea", "", "discussion", now, now),
    ).lastrowid
    shipped = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, author_id, "already done", "", "shipped", now, now),
    ).lastrowid
    baseline_feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, author_id, "the baseline", "", "discussion", now, now),
    ).lastrowid
    con.execute(
        "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games, is_baseline,"
        " updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
        (arena, "feature", feature, 1611.5, 92.25, 0.0601, 7, False, now),
    )
    con.execute(
        "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games, is_baseline,"
        " updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
        (arena, "feature", shipped, 1500.0, 350.0, 0.06, 0, False, now),
    )
    con.execute(
        "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games, is_baseline,"
        " updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
        (arena, "feature", baseline_feature, 1500.0, 350.0, 0.06, 0, True, now),
    )
    con.commit()
    con.close()

    base = f"/api/v1/projects/{slug}/moderation/arena"
    results = []

    # 0. Permission: two doors, and a contributor is under both.
    status, body = call("POST", f"{base}/remove",
                        {"feature_id": feature, "reason": "x", "confirm": True}, token=member_token)
    results.append(("a contributor cannot moderate", status, 403, message(body)))
    results.append(("the refusal says what it wanted",
                    1 if "reviewer" in message(body) else 0, 1, message(body)))
    status, body = call("POST", f"{base}/remove",
                        {"feature_id": feature, "reason": "x", "confirm": True})
    results.append(("no token is a 401", status, 401, message(body)))

    # 1. Confirmation and the reason are both required.
    status, body = call("POST", f"{base}/remove", {"feature_id": feature, "reason": "x"},
                        token=reviewer_token)
    results.append(("an unconfirmed act is refused", status, 400, message(body)))
    results.append(("it says so in the page's words", message(body), "this act needs confirmation", body))
    status, body = call("POST", f"{base}/remove", {"reason": "x", "confirm": True}, token=reviewer_token)
    results.append(("feature_id is required", status, 400, message(body)))
    status, body = call("POST", f"{base}/remove", {"feature_id": feature, "confirm": True},
                        token=reviewer_token)
    results.append(("an act with no reason is refused", status, 400, message(body)))
    results.append(("in the crate's own sentence", message(body),
                    "this act requires a reason, and none was given", body))
    status, body = call("POST", f"{base}/remove",
                        {"feature_id": feature, "reason": "   ", "confirm": True}, token=reviewer_token)
    results.append(("a reason of spaces is no reason", status, 400, message(body)))

    # A REFUSAL MUST NOT HAVE DONE THE THING ANYWAY. The first version of this port checked the reason at the
    # end, inside the act's insert, so a refused act had already deleted the entry and rejected the feature:
    # a silent removal, no log row, 400 to the moderator. This is the assertion that catches that order.
    con = harness.connect("unused")
    still_there = con.execute(
        "select count(*) from arena_entries where arena_id = ? and entity_id = ?",
        (arena, feature)).fetchone()[0]
    still_status = con.execute("select status from features where id = ?", (feature,)).fetchone()[0]
    acts_so_far = con.execute("select count(*) from audit_log where action = 'arena.remove'").fetchone()[0]
    con.close()
    results.append(("a refused act left the entry alone", still_there, 1, still_there))
    results.append(("a refused act left the status alone", still_status, "discussion", still_status))
    results.append(("a refused act wrote no log row", acts_so_far, 0, acts_so_far))

    # 2. The refusals the schema permits.
    status, body = call("POST", f"{base}/remove",
                        {"feature_id": shipped, "reason": "spam", "confirm": True}, token=reviewer_token)
    results.append(("a shipped feature cannot be removed", status, 400, message(body)))
    results.append(("and it says why", 1 if "shipped" in message(body) else 0, 1, message(body)))
    status, body = call("POST", f"{base}/remove",
                        {"feature_id": baseline_feature, "reason": "spam", "confirm": True},
                        token=reviewer_token)
    results.append(("the baseline entry cannot be removed", status, 400, message(body)))
    status, body = call("POST", f"{base}/remove",
                        {"feature_id": feature, "arena_id": arena + 500, "reason": "spam",
                         "confirm": True}, token=reviewer_token)
    results.append(("an arena the idea is not in is a 404", status, 404, message(body)))

    # 3. The act itself.
    status, body = call("POST", f"{base}/remove",
                        {"feature_id": feature, "reason": "  spam  ", "confirm": True},
                        token=reviewer_token)
    act = (body or {}).get("act") or {}
    results.append(("the act is taken", status, 201, message(body)))
    results.append(("it is named arena.remove", act.get("action"), "arena.remove", act))
    results.append(("the page is told how to undo it", (body or {}).get("undo", {}).get("action"),
                    "arena.restore", body))
    results.append(("the undo target is the act", (body or {}).get("undo", {}).get("target"),
                    act.get("id"), body))

    con = harness.connect("unused")
    entry = con.execute("select count(*) from arena_entries where arena_id = ? and entity_id = ?",
                        (arena, feature)).fetchone()
    status_now = con.execute("select status from features where id = ?", (feature,)).fetchone()
    notice = con.execute(
        "select kind, reason from notifications where subject_id = ? and kind = 'feature_removed'",
        (feature,)).fetchone()
    detail = con.execute("select detail from audit_log where id = ?", (act.get("id"),)).fetchone()
    con.close()
    results.append(("the entry is gone", entry[0], 0, entry))
    results.append(("the feature is rejected", status_now[0] if status_now else None, "rejected", status_now))
    results.append(("the author is told, in the same act", notice[0] if notice else None,
                    "feature_removed", notice))
    results.append(("the reason is trimmed in the notice", notice[1] if notice else None, "spam", notice))
    prior = json.loads(detail[0]).get("prev") if detail else {}
    results.append(("the log carries the prior rating", prior.get("elo_r"), 1611.5, prior))
    results.append(("and the prior status", prior.get("status"), "discussion", prior))
    results.append(("and the arena", prior.get("arena_id"), arena, prior))
    results.append(("the reason is in the log", json.loads(detail[0]).get("reason") if detail else None,
                    "spam", detail))

    # 4. The restore replays the log, not the caller.
    status, body = call("POST", f"{base}/restore",
                        {"reason": "not spam", "confirm": True}, token=reviewer_token)
    results.append(("undoes is required", status, 400, message(body)))
    status, body = call("POST", f"{base}/restore",
                        {"undoes": act.get("id"), "confirm": True}, token=reviewer_token)
    results.append(("a restore with no reason is refused", status, 400, message(body)))
    status, body = call("POST", f"{base}/restore", {
        "undoes": act.get("id"), "reason": "not spam", "confirm": True,
        # The caller tries to write a rating. It must be ignored: the log decides.
        "r": 9999.0, "rd": 1.0, "sigma": 1.0, "games": 99, "status": "shipped",
    }, token=reviewer_token)
    if not isinstance(body, dict):
        print(f"restore answered {status} with a non-JSON body: {body!r}")
    restore = (body if isinstance(body, dict) else {}).get("act") or {}
    results.append(("the act is reversed", status, 200, message(body)))
    results.append(("it is named arena.restore", restore.get("action"), "arena.restore", restore))

    con = harness.connect("unused")
    back = con.execute("select r, rd, sigma, games from arena_entries where arena_id = ? and entity_id = ?",
                       (arena, feature)).fetchone()
    status_now = con.execute("select status from features where id = ?", (feature,)).fetchone()
    undo_row = con.execute("select undoes from audit_log where id = ?", (restore.get("id"),)).fetchone()
    con.close()
    results.append(("the rating is the prior rating, not the caller's", back, (1611.5, 92.25, 0.0601, 7),
                    back))
    results.append(("the status is the prior status, not the caller's",
                    status_now[0] if status_now else None, "discussion", status_now))
    results.append(("the restore names the act it undid", undo_row[0] if undo_row else None,
                    act.get("id"), undo_row))

    # 5. Append-only: no second reversal, and nothing else is restorable.
    status, body = call("POST", f"{base}/restore",
                        {"undoes": act.get("id"), "reason": "again", "confirm": True},
                        token=reviewer_token)
    results.append(("an act cannot be reversed twice", status, 400, message(body)))
    results.append(("and it says so", 1 if "already been reversed" in message(body) else 0, 1,
                    message(body)))
    status, body = call("POST", f"{base}/restore",
                        {"undoes": restore.get("id"), "reason": "again", "confirm": True},
                        token=reviewer_token)
    results.append(("a restore is not itself restorable", status, 400, message(body)))
    status, body = call("POST", f"{base}/restore",
                        {"undoes": 987654321, "reason": "nothing", "confirm": True}, token=reviewer_token)
    results.append(("an act that does not exist is a 404", status, 404, message(body)))

    con = harness.connect("unused")
    con.execute("delete from audit_log where project_id = ?", (project,))
    con.execute("delete from notifications where project_id = ?", (project,))
    con.execute("delete from arena_entries where arena_id = ?", (arena,))
    con.execute("delete from arenas where id = ?", (arena,))
    con.execute("delete from features where project_id = ?", (project,))
    con.execute("delete from members where project_id = ?", (project,))
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
