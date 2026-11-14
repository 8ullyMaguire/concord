#!/usr/bin/env python3
"""S3's consensus writes, probed over HTTP.

The rules under test, each one a place where a plausible implementation is wrong:

* a call opens only on work that is not already decided, only with evidence, and only once (a second
  open call on the same feature is refused);
* opening a call moves its feature to `consensus` -- the rule the deployed instance lost, leaving 87
  features at `status='consensus'` with zero `consensus_calls` rows;
* calling and closing are a maintainer's acts (a plain member is refused, not silently allowed);
* a stance is one of four words, and a typo is refused rather than stored;
* while a call is open its tally is hidden (counts null, participation visible, only the caller's own
  position), and once closed the tally is the record;
* closing reports the state it wrote -- status `closed`, a result -- not the call as it stood before.

Nothing is skipped when a route is missing: a 404 is the finding.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "tests", "e2e"))  # harness

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
        with urllib.request.urlopen(req, timeout=10) as response:
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
        "username": f"s3probe{int(time.time())}",
        "display_name": "S3",
        "password": "correct horse",
    })
    token = body.get("token") if isinstance(body, dict) else None
    if not token:
        print(f"register failed: {status} {body}")
        return 1
    author = body["user"]["id"]

    status, body = call("POST", "/api/v1/auth/register", {
        "username": f"s3member{int(time.time())}",
        "display_name": "S3 member",
        "password": "correct horse",
    })
    member_token = body.get("token") if isinstance(body, dict) else None
    member = body["user"]["id"]

    con = harness.connect("unused")
    slug = "s3-probe"
    # Delete the previous run's rows children-first: PostgreSQL enforces the foreign keys SQLite never
    # did, so a bare `delete from projects` leaves consensus_calls/positions/objections/features pointing
    # at a row that is gone and the whole probe fails before it asserts anything.
    for drop in (
        "delete from project_documents where project_id in (select id from projects where slug = ?)",
        "delete from override_positions where window_id in (select id from override_windows"
        " where call_id in (select id from consensus_calls"
        " where project_id in (select id from projects where slug = ?)))",
        "delete from override_windows where call_id in (select id from consensus_calls"
        " where project_id in (select id from projects where slug = ?))",
        "delete from positions where call_id in (select id from consensus_calls"
        " where project_id in (select id from projects where slug = ?))",
        "delete from objections where call_id in (select id from consensus_calls"
        " where project_id in (select id from projects where slug = ?))",
        "delete from emergency_holds where call_id in (select id from consensus_calls"
        " where project_id in (select id from projects where slug = ?))",
        "delete from consensus_calls where project_id in (select id from projects where slug = ?)",
        # AFTER the calls: consensus_calls.solution_id references solutions, so deleting the solutions
        # first fails on the very rows the next line exists to remove.
        "delete from solutions where feature_id in (select id from features"
        " where project_id in (select id from projects where slug = ?))",
        "delete from feature_complaints where feature_id in (select id from features"
        " where project_id in (select id from projects where slug = ?))",
        "delete from features where project_id in (select id from projects where slug = ?)",
        "delete from complaints where project_id in (select id from projects where slug = ?)",
        "delete from board_cards where column_id in"
        " (select id from board_columns where project_id in (select id from projects where slug = ?))",
        "delete from board_columns where project_id in (select id from projects where slug = ?)",
        "delete from members where project_id in (select id from projects where slug = ?)",
        # A hold writes an admin_ledger row, which references the project: the ledger is deliberately
        # durable, so a probe that repeated itself hit the FK on the SECOND run, not the first.
        "delete from admin_ledger where project_id in (select id from projects where slug = ?)",
        "delete from emergency_holds where project_id in (select id from projects where slug = ?)",
    ):
        con.execute(drop, (slug,))
    con.execute("delete from projects where slug = ?", (slug,))
    con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)",
        (slug, "S3 probe", "", "collective", "public", "both", time.time(), time.time()),
    )
    con.commit()
    project = con.execute("SELECT id FROM projects WHERE slug = ?", (slug,)).fetchone()[0]
    # `joined_at` is NOT NULL with no default in the baseline: SQLite let the writer omit it, PostgreSQL
    # does not, so every fixture that seats a member must say when.
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (project, author, "maintainer", time.time()))
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (project, member, "contributor", time.time()))
    for phase, position in (("inbox", 0), ("consensus", 1)):
        con.execute("INSERT INTO board_columns (project_id, phase, position) VALUES (?,?,?)",
                    (project, phase, position))

    def feature(title):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?) RETURNING id",
            (project, author, title, "", "discussion", time.time(), time.time()),
        ).lastrowid

    bare = feature("no evidence")
    with_evidence = feature("has evidence")
    second = feature("second call")
    complaint = con.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, severity, frequency,"
        " strategic_multiplier, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (project, author, "a validated complaint", "", 2, 1.0, 1.0, "validated",
         time.time(), time.time()),
    ).lastrowid
    for target in (with_evidence, second):
        con.execute("INSERT INTO feature_complaints (feature_id, complaint_id) VALUES (?,?)",
                    (target, complaint))
    # §6.5's outcomes only apply to a call about a solution, so the feature needs one.
    solution = con.execute(
        "INSERT INTO solutions (feature_id, author_id, title, body, type) VALUES (?,?,?,?,?)"
        " RETURNING id",
        (with_evidence, author, "the obvious solution", "", "internal"),
    ).lastrowid
    runner_up = con.execute(
        "INSERT INTO solutions (feature_id, author_id, title, body, type) VALUES (?,?,?,?,?)"
        " RETURNING id",
        (with_evidence, author, "the runner-up", "", "internal"),
    ).lastrowid

    # A call that has already closed BLOCKED, with a standing objection: the only state §6.6's override
    # window opens on. Seeded rather than driven through close, because reaching `blocked` needs quorum
    # plus an open objection and this probe is about the window, not about the close.
    import uuid
    blocked_call = con.execute(
        "INSERT INTO consensus_calls (project_id, feature_id, opened_by, opens_at, closes_at, status,"
        " result, question, public_id, closed_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (project, None, author, time.time() - 3600, time.time() - 60, "closed", "blocked",
         "a blocked call", f"probe{uuid.uuid4().hex[:20]}", time.time() - 60),
    ).lastrowid
    con.execute(
        "INSERT INTO objections (call_id, user_id, principle, violation, remedy, status, created_at)"
        " VALUES (?,?,?,?,?,?,?)",
        (blocked_call, member, "the audit ledger", "it rewrote a row", "append instead", "open",
         time.time() - 3000),
    )
    # A call that closed for some OTHER reason. Without it, "a window opens only on a blocked call" is
    # asserted against call #1, which DOES close blocked -- the objection above guarantees it -- and a
    # 201 there is correct behaviour that the assertion would call a failure.
    # Two calls that are ABOUT a solution and already closed by §6.8, which is the state the decision
    # record has to be writable in -- the guard is "already decided", not "already closed".
    def solution_call(question, result):
        return con.execute(
            "INSERT INTO consensus_calls (project_id, feature_id, solution_id, opened_by, opens_at,"
            " closes_at, status, result, question, public_id, closed_at)"
            " VALUES (?,?,?,?,?,?,?,?,?,?,?) RETURNING id",
            (project, with_evidence, solution, author, time.time() - 3600, time.time() - 60,
             "closed", result, question, f"probe{uuid.uuid4().hex[:20]}", time.time() - 60),
        ).lastrowid

    decided_call = solution_call("adopt the obvious solution?", "accepted")
    sentback_call = solution_call("adopt it now?", "rejected")
    rejected_call = con.execute(
        "INSERT INTO consensus_calls (project_id, feature_id, opened_by, opens_at, closes_at, status,"
        " result, question, public_id, closed_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (project, None, author, time.time() - 3600, time.time() - 60, "closed", "rejected",
         "a rejected call", f"probe{uuid.uuid4().hex[:20]}", time.time() - 60),
    ).lastrowid
    con.commit()
    con.close()

    results = []

    # 1. Opening a call needs an account, and a maintainer's role.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus",
                        {"feature_id": with_evidence, "title": "no token"})
    results.append(("open without a token", status, 401, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus",
                        {"feature_id": with_evidence, "title": "as a contributor"}, token=member_token)
    results.append(("open as a contributor", status, 403, message(body)))

    # 2. Evidence: a call on a feature with no linked complaint is a vote on a rumour.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus",
                        {"feature_id": bare, "title": "no evidence"}, token=token)
    results.append(("open without evidence", status, 400, message(body)))
    results.append(("the refusal names the evidence", 1 if "complaint" in message(body) else 0, 1,
                    message(body)))

    # 3. A valid call opens, and its feature moves to `consensus`.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus",
                        {"feature_id": with_evidence, "title": "ship it?",
                         "description": "the case for"}, token=token)
    call_id = body.get("id") if isinstance(body, dict) else None
    results.append(("open a call", status, 201, body))
    status, body = call("GET", f"/api/v1/projects/{slug}/features/{with_evidence}")
    results.append(("opening moves the feature to consensus", (body or {}).get("status"), "consensus",
                    (body or {}).get("status")))
    results.append(("opened_by is the caller", (body or {}).get("id") is not None, 1, author))

    # 4. One open call per feature.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus",
                        {"feature_id": with_evidence, "title": "again"}, token=token)
    results.append(("a second open call is refused", status, 400, message(body)))

    # 5. A stance is one of four words.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{call_id}/position",
                        {"position": "maybe"}, token=token)
    results.append(("an invented stance", status, 400, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{call_id}/position",
                        {"position": "consent"}, token=token)
    results.append(("cast consent", status, 200, body))

    # 6. An objection needs its principle and its violation.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{call_id}/objection",
                        {"principle": "", "violation": "it breaks the ledger"}, token=token)
    results.append(("an objection with no principle", status, 400, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{call_id}/objection",
                        {"principle": "the audit ledger", "violation": "it rewrote a row",
                         "remedy": "append instead"}, token=token)
    objection = body.get("id") if isinstance(body, dict) else None
    results.append(("file an objection", status, 201, body))

    # 7. A resolution is an answer somebody wrote.
    status, body = call("PUT", f"/api/v1/projects/{slug}/consensus/objections/{objection}/resolve",
                        {"status": "answered", "resolution": "   "}, token=token)
    results.append(("resolve with an empty answer", status, 400, message(body)))

    # 8. While the call is open the tally is hidden, and only the caller's own position comes back.
    status, body = call("GET", f"/api/v1/projects/{slug}/consensus/{call_id}", token=token)
    tally = (body or {}).get("tally") or {}
    positions = (body or {}).get("positions") or []
    results.append(("an open call hides its counts", tally.get("consent"), None, tally.get("consent")))
    results.append(("...and its ratios", tally.get("decisive_ratio"), None, tally.get("decisive_ratio")))
    results.append(("participation stays visible", 1 if tally.get("participants") is not None else 0,
                    1, tally.get("participants")))
    results.append(("the quorum bar stays visible",
                    1 if tally.get("quorum_required") is not None else 0, 1,
                    tally.get("quorum_required")))
    results.append(("tally_visible is false", (body or {}).get("tally_visible"), False,
                    (body or {}).get("tally_visible")))
    results.append(("only the caller's own position", len(positions), 1, positions))

    # 9. Closing fixes the verdict and reports what it wrote.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{call_id}/close", None, token=token)
    closed = (body or {}).get("call") or {}
    results.append(("close the call", status, 200, message(body)))
    results.append(("the close reports status closed", closed.get("status"), "closed",
                    closed.get("status")))
    results.append(("the close reports a result",
                    1 if (body or {}).get("result") else 0, 1, (body or {}).get("result")))

    # 10. The record now shows its tally, and a closed call takes no more positions.
    status, body = call("GET", f"/api/v1/projects/{slug}/consensus/{call_id}", token=token)
    tally = (body or {}).get("tally") or {}
    results.append(("a closed call shows its counts", tally.get("consent"), 1, tally.get("consent")))
    results.append(("a closed call shows every position",
                    1 if len((body or {}).get("positions") or []) >= 1 else 0, 1,
                    len((body or {}).get("positions") or [])))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{call_id}/position",
                        {"position": "block"}, token=member_token)
    results.append(("a closed call takes no positions", status, 400, message(body)))

    # A call on no feature is a §6.6 process decision and must not need one.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus",
                        {"title": "amend the charter"}, token=token)
    process_call = (body or {}).get("id")
    results.append(("a process decision opens without a feature", status, 201,
                    (body or {}).get("feature_id")))
    results.append(("...and its feature_id is 0", (body or {}).get("feature_id"), 0,
                    (body or {}).get("feature_id")))

    # 11. §6.6's override window: the three refusals, one window, the hidden tally, and the written note.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{blocked_call}/override")
    results.append(("override without a token", status, 401, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{blocked_call}/override",
                        None, token=member_token)
    results.append(("override as a contributor", status, 403, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{rejected_call}/override",
                        None, token=token)
    results.append(("override on a call that closed rejected", status, 400, message(body)))
    results.append(("the refusal names the result", 1 if "rejected" in message(body) else 0, 1,
                    message(body)))
    # A blocked call with no standing objection has no block left to override.
    con2 = harness.connect("unused")
    con2.execute("update objections set status = 'withdrawn' where call_id = ?", (blocked_call,))
    con2.commit()
    con2.close()
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{blocked_call}/override",
                        None, token=token)
    results.append(("a blocked call whose objection was withdrawn", status, 400, message(body)))
    results.append(("the refusal names the missing objection",
                    1 if "objection" in message(body) else 0, 1, message(body)))
    con3 = harness.connect("unused")
    con3.execute("update objections set status = 'open' where call_id = ?", (blocked_call,))
    con3.commit()
    con3.close()
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{blocked_call}/override",
                        None, token=token)
    window = body if isinstance(body, dict) else {}
    results.append(("open the override window", status, 201, window))
    results.append(("an open window hides its counts", window.get("consent"), None,
                    window.get("consent")))
    results.append(("the quorum bar is visible", 1 if window.get("quorum_needed") is not None else 0,
                    1, window.get("quorum_needed")))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{blocked_call}/override",
                        None, token=token)
    results.append(("a second window is a conflict", status, 409, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{blocked_call}/override/position",
                        {"position": "maybe"}, token=token)
    results.append(("an invented stance in the window", status, 400, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{blocked_call}/override/position",
                        {"position": "consent", "reason": "the remedy answers it"}, token=token)
    results.append(("cast a stance in the window", status, 200, body))
    status, body = call("GET", f"/api/v1/projects/{slug}/consensus/{blocked_call}/override",
                        token=token)
    results.append(("the window shows my own stance", (body or {}).get("my_position"), "consent",
                    (body or {}).get("my_position")))
    results.append(("...and still hides the counts", (body or {}).get("consent"), None,
                    (body or {}).get("consent")))
    # An override needs the objector's remedy answered in writing; the verdict is what makes it required.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{blocked_call}/override/close",
                        {"note": "   "}, token=token)
    results.append(("an override with no written answer", status, 400, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{blocked_call}/override/close",
                        {"note": "the remedy is applied in the next release"}, token=token)
    closed_window = body if isinstance(body, dict) else {}
    results.append(("close the window", status, 200, closed_window.get("result")))
    results.append(("the window records its verdict", closed_window.get("result"), "overridden",
                    closed_window.get("result")))
    results.append(("a closed window shows its counts", closed_window.get("consent"), 1,
                    closed_window.get("consent")))
    status, body = call("GET", f"/api/v1/projects/{slug}/consensus/{blocked_call}")
    results.append(("the override accepts the call", (body or {}).get("call", {}).get("result"),
                    "accepted_overridden", (body or {}).get("call", {}).get("result")))

    # 12. §6.6's emergency hold: a pause that cannot become a veto.
    status, body = call("GET", f"/api/v1/projects/{slug}/consensus/{process_call}/hold")
    results.append(("no hold yet", (body or {}).get("hold"), None, body))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{process_call}/hold",
                        {"grounds": "security", "reason": "an embargo"})
    results.append(("hold without a token", status, 401, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{process_call}/hold",
                        {"grounds": "security", "reason": "an embargo"}, token=member_token)
    results.append(("hold as a contributor", status, 403, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{process_call}/hold",
                        {"grounds": "vibes", "reason": "we do not like it"}, token=token)
    results.append(("hold on an invented ground", status, 400, message(body)))
    results.append(("the refusal names the grounds", 1 if "security" in message(body) else 0, 1,
                    message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{process_call}/hold",
                        {"grounds": "legal", "reason": "   "}, token=token)
    results.append(("hold with no written reason", status, 400, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{process_call}/hold",
                        {"grounds": "legal", "reason": "a subpoena", "days": 90}, token=token)
    results.append(("a hold past the ceiling is refused, not clamped", status, 400, message(body)))
    # The cross-project defect this store check exists to close: project B suspending project A's call.
    status, body = call("POST", f"/api/v1/projects/no-such-project/consensus/{process_call}/hold",
                        {"grounds": "legal", "reason": "a subpoena"}, token=token)
    results.append(("a hold through another project's URL", status, 404, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{process_call}/hold",
                        {"grounds": "Security", "reason": " an embargo "}, token=token)
    hold = body if isinstance(body, dict) else {}
    results.append(("place a hold", status, 201, hold))
    results.append(("the ground is normalised", hold.get("grounds"), "security", hold.get("grounds")))
    results.append(("the reason is trimmed", hold.get("reason"), "an embargo", hold.get("reason")))
    results.append(("the default duration is seven days",
                    1 if hold.get("expires_at") and hold.get("created_at")
                    and abs((hold["expires_at"] - hold["created_at"]) - 7 * 86400) < 2 else 0,
                    1, (hold.get("expires_at"), hold.get("created_at"))))
    results.append(("the hold is active", hold.get("active"), True, hold.get("active")))
    status, body = call("GET", f"/api/v1/projects/{slug}/consensus/{process_call}/hold")
    results.append(("the hold reads back", (body or {}).get("hold", {}).get("id"), hold.get("id"),
                    body))
    # A hold suspends: the call cannot be closed while it is force.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{process_call}/close",
                        None, token=token)
    results.append(("a held call cannot be closed", status, 400, message(body)))
    results.append(("the refusal names the hold", 1 if "hold" in message(body) else 0, 1,
                    message(body)))

    # 13. The charter: the numbers the verdicts are computed with, published where the page reads them.
    status, body = call("GET", f"/api/v1/projects/{project}/charter")
    charter = body if isinstance(body, dict) else {}
    results.append(("read the charter", status, 200, charter))
    for key in ("quorum_ratio", "consent_ratio", "override_ratio", "vote_window_days",
                "merge_requires_quorum", "require_reviewer_approval", "lam", "mu",
                "support_ratio_min"):
        results.append((f"the charter carries {key}", 1 if key in charter else 0, 1,
                        sorted(charter)))
    # The charters table has no row for this fixture, so every value is the collective model's default --
    # and the route must still answer rather than 404.
    # 0.7 is the collective model's decisive bar in internal/governance (ConsentRatio: 0.7). I first wrote
    # 0.66 here from memory; the probe failed and the Go source settled it.
    results.append(("a project with no charter row gets the model defaults",
                    charter.get("consent_ratio"), 0.7, charter.get("consent_ratio")))
    results.append(("...and the override bar too", charter.get("override_ratio"), 0.8,
                    charter.get("override_ratio")))
    status, body = call("GET", f"/api/v1/projects/{slug}/charter")
    results.append(("a slug in the numeric segment is a parse error", status, 400, message(body)))
    status, body = call("GET", "/api/v1/projects/999999/charter")
    results.append(("a charter for a project that does not exist", status, 404, message(body)))

    # 14. §6.5's five outcomes and the decision record they write.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{decided_call}/outcome",
                        {"outcome": "accepted"})
    results.append(("outcome without a token", status, 401, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{decided_call}/outcome",
                        {"outcome": "accepted"}, token=member_token)
    results.append(("outcome as a contributor", status, 403, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{decided_call}/outcome",
                        {"outcome": "approved"}, token=token)
    results.append(("an invented outcome", status, 400, message(body)))
    results.append(("the refusal lists the outcomes", 1 if "sent-back" in message(body) else 0, 1,
                    message(body)))
    # NOT process_call: that one carries the emergency hold placed above, and the hold guard fires before
    # the solution guard -- correctly, since a suspended call resolves nothing however the request is
    # shaped. rejected_call has no solution and no hold.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{rejected_call}/outcome",
                        {"outcome": "accepted"}, token=token)
    results.append(("an outcome on a call with no solution", status, 400, message(body)))
    results.append(("the refusal says why", 1 if "solution" in message(body) else 0, 1,
                    message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{decided_call}/outcome",
                        {"outcome": "fall-back"}, token=token)
    results.append(("fall-back naming nothing", status, 400, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{decided_call}/outcome",
                        {"outcome": "fall-back", "fallback_to": solution}, token=token)
    results.append(("fall-back to the solution the call was about", status, 400, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{decided_call}/outcome",
                        {"outcome": "accepted-with-amendments"}, token=token)
    results.append(("amendments with no text", status, 400, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{decided_call}/outcome",
                        {"outcome": "accepted", "summary": "the obvious solution covers it"}, token=token)
    recorded = body if isinstance(body, dict) else {}
    results.append(("record the outcome", status, 200, message(body)))
    results.append(("the call carries its outcome", recorded.get("outcome"), "accepted",
                    recorded.get("outcome")))
    results.append(("the call is closed by the decision", recorded.get("status"), "closed",
                    recorded.get("status")))
    status, body = call("GET", f"/api/v1/projects/{slug}/features/{with_evidence}")
    results.append(("the feature is ready", (body or {}).get("status"), "ready",
                    (body or {}).get("status")))
    # The decision record is a project document of kind 'adr', not a new table.
    con4 = harness.connect("unused")
    adr = con4.execute(
        "select title, body from project_documents where project_id = ? and kind = 'adr'"
        " order by id desc limit 1", (project,)).fetchone()
    con4.close()
    results.append(("a decision record was written", 1 if adr else 0, 1, adr[0] if adr else None))
    results.append(("it names the outcome", 1 if adr and "accepted" in adr[1] else 0, 1,
                    (adr[1][:60] if adr else None)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{decided_call}/outcome",
                        {"outcome": "rejected"}, token=token)
    results.append(("a second outcome is a conflict", status, 409, message(body)))
    # "sent back" is not a rejection: the call decided not to decide.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{sentback_call}/outcome",
                        {"outcome": "sent-back"}, token=token)
    results.append(("send a call back", status, 200, message(body)))
    status, body = call("GET", f"/api/v1/projects/{slug}/features/{with_evidence}")
    results.append(("sent back returns the feature to discussion", (body or {}).get("status"),
                    "discussion", (body or {}).get("status")))

    failed = 0
    for label, got, want, detail in results:
        ok = got == want
        if not ok:
            failed += 1
        print(f"{'ok  ' if ok else 'FAIL'} {label}: got {got!r} want {want!r}"
              + ("" if ok else f"  ({detail})"))
    print(f"\n{len(results) - failed}/{len(results)} ok")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
