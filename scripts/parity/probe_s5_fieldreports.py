#!/usr/bin/env python3
"""Row 11b part 15: §4.10's field reports panel — "worked for 83% of REPORTERS on arm64".

Four facts this panel must keep apart, each invisible if merged into one number:

1. **no reports** -> `outcome_rate` ABSENT and `sample_size: 0`. A zero would claim reporters tried it and it
   failed, which is a claim about a project nobody has tried;
2. **one report** -> a rate with `sample_size: 1`, which is established by nobody; the denominator is what stops
   that being presented as settled;
3. **reputation weights the rate** (`1 + min(rep,100)/100`), and `worked-with-caveats` counts HALF — a report that
   says "it worked, with these caveats" is neither;
4. **removed reports are hidden, not deleted**, and never counted in the rate — serving one would undo the quorum
   decision that removed it.
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
    return body["user"]["id"]


def cleanup(con, like):
    for (project,) in con.execute("select id from projects where slug like ?", (like,)).fetchall():
        con.execute("delete from field_report_responses where report_id in"
                    " (select id from field_reports where project_id = ?)", (project,))
        con.execute("delete from field_reports where project_id = ?", (project,))
        con.execute("delete from reputation_events where project_id = ?", (project,))
        con.execute("delete from members where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    veteran, newcomer, failing, hider = (register(f"fr{i}", stamp) for i in range(4))
    slug = f"probe-fr-{stamp}"

    con = harness.connect("unused")
    cleanup(con, "probe-fr-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Field report probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    # Reputation: the veteran is at the cap (weight 2.0), everyone else at 0 (weight 1.0).
    con.execute(
        "INSERT INTO reputation_events (project_id, user_id, kind, points, created_at) VALUES (?,?,?,?,?)",
        (project, veteran, "probe", 500, now),
    )

    def report(user, outcome, environment, removed=0):
        return con.execute(
            "INSERT INTO field_reports (project_id, user_id, version, use_case, environment, scale, duration,"
            " outcome, migrated_to, caveats, workaround, advice, removed, created_at)"
            " VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id",
            (project, user, "1.0", "probe", environment, "small", "1h", outcome, "", "", "", "",
             removed, now),
        ).lastrowid

    worked = report(veteran, "worked", "arm64")
    caveats = report(newcomer, "worked-with-caveats", "arm64")
    failed = report(failing, "abandoned", "x86_64")
    hidden = report(hider, "worked", "arm64", removed=1)
    con.execute(
        "INSERT INTO field_report_responses (report_id, user_id, body, is_owner, created_at)"
        " VALUES (?,?,?,?,?)",
        (failed, veteran, "thank you, tracking", 1, now + 1),
    )
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{project}/field-reports")
    check("the panel answers", status, 200)
    body = body or {}
    # work: veteran worked = 2.0*1.0 = 2.0; caveats = 1.0*0.5 = 0.5; abandoned = 1.0*0.0 = 0.0.
    # denominator 2.0 + 1.0 + 1.0 = 4.0 -> 2.5/4 = 0.625
    check("the rate is reputation-weighted and caveats count half",
          round(body.get("outcome_rate", -1), 6), 0.625)
    check("the denominator counts every live report", body.get("sample_size"), 3)
    check("the removed report is hidden", body.get("shown"), 3)
    check("and is not in the list", sorted(r["id"] for r in body["reports"]), sorted([worked, caveats, failed]))
    check("newest first", [r["id"] for r in body["reports"]][0], failed)
    # arm64: 2.0*1 + 1.0*0.5 = 2.5 over 3.0 = 0.8333… — the spec's "83% of reporters on arm64".
    check("arm64 is the spec's 83%", round(body["by_environment"]["arm64"], 4), 0.8333)
    check("and x86_64 is a real zero, not an absence", body["by_environment"]["x86_64"], 0.0)
    owner_reply = [r for r in body["reports"] if r["id"] == failed][0]["responses"]
    check("the owner's reply rides with its report", [(x["body"], x["is_owner"]) for x in owner_reply],
          [("thank you, tracking", True)])
    check("and it names the report it answers", owner_reply[0]["report_id"], failed)

    # No data at all: a second, empty project.
    empty = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug + "-empty", "Empty", "", "collective", "public", "both", now, now),
    ).lastrowid
    con.commit()
    status, body = call("GET", f"/api/v1/projects/{empty}/field-reports")
    check("an unreported project answers", status, 200)
    check("with NO outcome_rate at all", "outcome_rate" in (body or {}), False)
    check("and a sample size of zero", (body or {}).get("sample_size"), 0)
    check("and no environment split", "by_environment" in (body or {}), False)

    cleanup(con, "probe-fr-%")
    con.execute("DELETE FROM reputation_events WHERE user_id IN (?,?,?,?)", (veteran, newcomer, failing, hider))
    con.execute("DELETE FROM api_tokens WHERE user_id IN (?,?,?,?)", (veteran, newcomer, failing, hider))
    con.execute("DELETE FROM users WHERE id IN (?,?,?,?)", (veteran, newcomer, failing, hider))
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
