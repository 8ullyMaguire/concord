#!/usr/bin/env python3
"""Row 11b part 42 (B4a): project settings — tags, tag completeness, languages, intake mode.

The rules, each one a decision the reference made for a reason:

1. applying a tag that the **instance vocabulary has never seen** is refused with the route that can propose one — applied
   silently a typo becomes a permanent taxonomy entry nobody ratified;
2. tagging is **contributor** work (it is how a project becomes findable), languages likewise, the intake mode is
   **maintainer** work;
3. the percentage **sum** is bounded, not just each share: the discovery score treats them as shares, so a breakdown
   summing to 140 overstates the project everywhere it is ranked;
4. a save that changes nothing writes **no act**, because the settings panel posts the whole form;
5. `mode` and `intake_mode` are **both** accepted, because a caller echoing the response back is doing the obvious thing;
6. the completeness meter reports **both thresholds** so the page can explain the number, and pro-rates below them.
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
    con.execute(f"delete from project_tags where project_id in ({marks})", params)
    con.execute(f"delete from project_languages where project_id in ({marks})", params)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    maintainer_id, maintainer_token = register(f"setmaint{stamp}")
    contributor_id, contributor_token = register(f"setcontrib{stamp}")
    stranger_id, stranger_token = register(f"setstranger{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-set-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Settings probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    hidden = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-hidden", "Hidden", "", "collective", "private", "both", now, now),
    ).lastrowid
    for user, role in ((maintainer_id, "maintainer"), (contributor_id, "contributor")):
        con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                    (project, user, role, now))
    # A vocabulary to apply: one bare tag and two namespaced ones.
    names = [f"ledger{stamp}", f"topic:kanban{stamp}", f"kind:utility{stamp}"]
    for name in names:
        # A namespaced tag is stored as the BARE name plus a namespace column — `topic:kanban` is name `kanban` with
        # namespace `topic`, which is what `SplitTag` splits and what `ApplyExistingProjectTag` looks up. Seeding the
        # whole `topic:kanban` string as the name makes every namespaced apply miss.
        namespace, bare = (name.split(":", 1) if ":" in name else (None, name))
        # `suggested` is BOOLEAN in PostgreSQL (it was an integer flag in SQLite) — a bare 0 is an integer literal.
        con.execute("INSERT INTO tags (name, namespace, suggested) VALUES (?,?,false)", (bare, namespace))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    def audits(action):
        return con.execute(
            "SELECT count(*) FROM audit_log WHERE project_id = ? AND action = ?",
            (project, action)).fetchone()[0]

    # ── tags ────────────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("PUT", f"/api/v1/projects/{slug}/tags", {"tags": [names[0]]})
    check("applying anonymously is 401", status, 401)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/tags", {"tags": [names[0]]}, token=stranger_token)
    check("a non-member may not apply", status, 403)
    status, applied = call("PUT", f"/api/v1/projects/{slug}/tags", {"tags": [names[0]]}, token=contributor_token)
    check("a contributor may", status, 200)
    check("and the response is what is stored", (applied or {}).get("tags"), [names[0]])
    status, _ = call("PUT", f"/api/v1/projects/{slug}/tags", {"tags": [names[0]]}, token=contributor_token)
    links = con.execute("SELECT count(*) FROM project_tags WHERE project_id = ?", (project,)).fetchone()[0]
    check("applying the same tag twice leaves one link", links, 1)
    status, refused = call("PUT", f"/api/v1/projects/{slug}/tags",
                           {"tags": [f"nonesuch{stamp}"]}, token=contributor_token)
    check("an unknown tag is 400", status, 400)
    check("and the refusal names the proposing route",
          "taxonomy/proposals" in json.dumps(refused), True)
    status, _ = call("PUT", f"/api/v1/projects/{slug}-hidden/tags",
                     {"tags": [names[0]]}, token=stranger_token)
    check("a private project the caller cannot read is 404", status, 404)
    check("each accepted call audits apply_tags", audits("apply_tags"), 2)

    # ── completeness ───────────────────────────────────────────────────────────────────────────────────────────────
    status, meter = call("GET", f"/api/v1/projects/{slug}/tags/completeness")
    check("the meter is readable without a token", status, 200)
    meter = meter or {}
    check("it reports the thresholds, not just the score",
          (meter.get("want_tags"), meter.get("want_namespaced")), (5, 2))
    check("one of five tags scores 0.12 (pro-rated)", round(meter.get("score") or 0, 4), 0.12)
    check("and it is not complete", meter.get("complete"), False)

    call("PUT", f"/api/v1/projects/{slug}/tags", {"tags": names}, token=contributor_token)
    for index in range(2):
        name = f"extra{index}-{stamp}"
        con.execute("INSERT INTO tags (name, namespace, suggested) VALUES (?,?,false)", (name, None))
        con.commit()
        call("PUT", f"/api/v1/projects/{slug}/tags", {"tags": [name]}, token=contributor_token)
    status, meter = call("GET", f"/api/v1/projects/{slug}/tags/completeness")
    meter = meter or {}
    check("five tags with two namespaced is complete", (meter.get("tags"), meter.get("namespaced"),
                                                        meter.get("complete")), (5, 2, True))
    check("and scores exactly 1.0", round(meter.get("score") or 0, 4), 1.0)

    # ── languages ──────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("PUT", f"/api/v1/projects/{slug}/languages",
                     {"languages": [{"language": "Rust", "pct": 70}, {"language": "Svelte", "pct": 30}]},
                     token=contributor_token)
    check("a breakdown within 100% is accepted", status, 200)
    stored = con.execute(
        "SELECT language, pct FROM project_languages WHERE project_id = ? ORDER BY language",
        (project,)).fetchall()
    check("and replaces the previous set", [tuple(row) for row in stored], [("Rust", 70.0), ("Svelte", 30.0)])
    status, _ = call("PUT", f"/api/v1/projects/{slug}/languages",
                     {"languages": [{"language": "Rust", "pct": 50}, {"language": "Go", "pct": 70}]},
                     token=contributor_token)
    check("a sum above 100% is 400", status, 400)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/languages",
                     {"languages": [{"language": "Rust", "pct": 140}]}, token=contributor_token)
    check("a share above 100 is 400", status, 400)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/languages",
                     {"languages": [{"language": "Rust", "pct": -5}]}, token=contributor_token)
    check("a negative share is 400", status, 400)
    kept = con.execute("SELECT count(*) FROM project_languages WHERE project_id = ?", (project,)).fetchone()[0]
    check("a refused write left the stored set alone", kept, 2)
    check("and a change is audited", audits("set_languages"), 1)

    # ── intake mode ────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("PUT", f"/api/v1/projects/{slug}/intake-mode", {"mode": "idea_first"},
                     token=contributor_token)
    check("a contributor may not change the intake mode", status, 403)
    status, changed = call("PUT", f"/api/v1/projects/{slug}/intake-mode", {"mode": "idea_first"},
                           token=maintainer_token)
    check("a maintainer may", (status, (changed or {}).get("intake_mode")), (200, "idea_first"))
    detail = con.execute(
        "SELECT detail FROM audit_log WHERE project_id = ? AND action = 'intake_mode'",
        (project,)).fetchall()
    check("the act records the move", [row[0] for row in detail], ["both -> idea_first"])
    status, echo = call("PUT", f"/api/v1/projects/{slug}/intake-mode", {"intake_mode": "idea_first"},
                        token=maintainer_token)
    check("echoing the response field back is accepted", (status, (echo or {}).get("intake_mode")),
          (200, "idea_first"))
    check("and a no-op save writes NO second act", audits("intake_mode"), 1)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/intake-mode", {"mode": "banana"},
                     token=maintainer_token)
    check("an unknown mode is 400", status, 400)

    cleanup(con, f"{slug}%")
    con.execute("DELETE FROM tags WHERE name IN (?,?,?,?,?)",
                (names[0], names[1].split(":", 1)[1], names[2].split(":", 1)[1],
                 f"extra0-{stamp}", f"extra1-{stamp}"))
    con.commit()
    for user in (maintainer_id, contributor_id, stranger_id):
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


if __name__ == "__main__":
    sys.exit(main())
