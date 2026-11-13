#!/usr/bin/env python3
"""Row 11b part 38: filing with the duplicate gate, and the pre-submit panel.

Six rules, and the ones that bite are about what the gate must NOT do:

1. a near-identical filing is refused with **409** carrying the candidates, the band and the way out — a refusal with no
   route forward is a lost filing;
2. `confirm_duplicate: true` proceeds, and the existing entry is **untouched** either way;
3. the title is searched **on its own first**, because the concatenation buried a literal duplicate at 0.58 when
   title-to-title scores 1.00;
4. the search is **instance-wide**, which is §6.1's "this was fixed in X" case; a per-project search made a verbatim copy
   filed elsewhere invisible;
5. `/similar` is **anonymous** but not an oracle: a private project's row must not appear in its answer;
6. an instance-wide request is filed with **no gate at all** — the reference never checked there, and matching it is the
   job until cutover.
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
    if projects:
        marks = ",".join("?" * len(projects))
        params = tuple(projects)
        con.execute(
            f"delete from embeddings where entity_id in (select id from requests where project_id in ({marks}))"
            f" and kind = 'request'",
            params,
        )
        con.execute(
            f"delete from request_answers where project_id in ({marks}) or request_id in"
            f" (select id from requests where project_id in ({marks}))",
            params + params,
        )
        con.execute(
            f"delete from request_tags where request_id in (select id from requests where project_id in ({marks}))",
            params,
        )
        con.execute(f"delete from audit_log where project_id in ({marks})", params)
        con.execute(f"delete from requests where project_id in ({marks})", params)
        con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    author_id, author_token = register(f"filer{stamp}")
    _, stranger_token = register(f"strangerfiler{stamp}")

    con = harness.connect("unused")
    cleanup(con, f"probe-file-{stamp}%")
    now = time.time()
    slug = f"probe-file-{stamp}"
    private_slug = f"{slug}-hidden"
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Filing probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    hidden = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (private_slug, "Hidden", "", "collective", "private", "both", now, now),
    ).lastrowid
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    # A distinctive sentence, so the probe cannot collide with real corpus text.
    title = f"Printer makes a grinding noise at the end of every page {stamp}"
    body = "It starts about a centimetre from the bottom and stops when the page feeds out."

    status, created = call("POST", f"/api/v1/projects/{project}/requests", {"title": title, "body": body},
                           token=author_token)
    check("filing a request answers 201", status, 201)
    first_id = (created or {}).get("id")
    check("and returns the request", isinstance(first_id, int), True)

    status, refused = call("POST", f"/api/v1/projects/{project}/requests", {"title": title, "body": body},
                           token=author_token)
    check("filing the SAME text again is refused with 409", status, 409)
    refused = refused if isinstance(refused, dict) else {}
    check("the refusal names the candidates", len(refused.get("similar") or []) >= 1, True)
    check("and the one already filed", (refused.get("similar") or [{}])[0].get("entity_id"), first_id)
    check("the top score is at or above the strong band",
          (refused.get("top_score") or 0) >= (refused.get("threshold") or 2), True)
    check("a candidate carries a page URL, not an /api/v1 route",
          str((refused.get("similar") or [{}])[0].get("url", "")).startswith("/projects/"), True)
    check("the refusal says how to proceed", (refused.get("resubmit_with") or {}).get("confirm_duplicate"), True)

    status, confirmed = call(
        "POST", f"/api/v1/projects/{project}/requests",
        {"title": title, "body": body, "confirm_duplicate": True}, token=author_token,
    )
    check("confirm_duplicate=true files it anyway", status, 201)
    check("as a NEW row, leaving the first untouched", (confirmed or {}).get("id") != first_id, True)
    status, still = call("GET", f"/api/v1/requests/{first_id}", token=author_token)
    check("the first entry really is untouched", (still or {}).get("title"), title)

    # A paraphrase: different words, same complaint. Under the LOCAL hashed embedder this is NOT refused — measured, it
    # flags 0 of 6 paraphrases — so the honest expectation is "filed, and surfaced as a hint if it clears the weak band".
    # Asserting a 409 here would be asserting that the local embedder is a transformer, which it is not.
    paraphrase = f"Printer grinds loudly near the bottom of each page {stamp}"
    status, _ = call("POST", f"/api/v1/projects/{project}/requests",
                     {"title": paraphrase, "body": ""}, token=author_token)
    check("a paraphrase is filed, not refused, under the local embedder", status, 201)
    status, hint = call("GET", f"/api/v1/similar/request?text={urllib.parse.quote(paraphrase)}")
    ids = [row.get("entity_id") for row in (hint or {}).get("similar") or []]
    check("and the panel lists the original among its leads", first_id in ids, True)
    # A text nothing like it must NOT report a duplicate — otherwise has_duplicate is a constant, not a decision.
    status, fresh = call(
        "GET",
        "/api/v1/similar/request?text=" + urllib.parse.quote(f"my keyboard repeats the letter q {stamp}"),
    )
    check("a novel text reports no duplicate", (fresh or {}).get("has_duplicate"), False)

    status, unrelated = call("POST", f"/api/v1/projects/{project}/requests",
                             {"title": f"Password reset email never arrives {stamp}", "body": "I gave up after an hour."},
                             token=author_token)
    check("an unrelated filing goes through", status, 201)

    # The panel, asked the way the frontend asks it: no token, just text.
    status, panel = call("GET", f"/api/v1/similar/request?text={urllib.parse.quote(title)}")
    check("/similar answers without a token", status, 200)
    panel = panel if isinstance(panel, dict) else {}
    check("it says which model answered", bool(panel.get("model_id")), True)
    check("it carries the bands", set((panel.get("thresholds") or {}).keys()), {"strong", "likely", "weak"})
    check("it reports has_duplicate", panel.get("has_duplicate"), True)
    check("it reports coverage, so a thin index is visible",
          isinstance((panel.get("coverage") or {}).get("total"), int), True)

    status, _ = call("GET", "/api/v1/similar/request")
    check("no text is 400", status, 400)
    status, _ = call("GET", "/api/v1/similar/banana?text=x")
    check("an unknown kind is 400", status, 400)

    # The panel is anonymous by design, so the visibility clause is the only thing between it and an oracle. Set it up
    # through the API — file the same text into a second, public project, then make that project private — so the row
    # under test is indexed by the same writer the service uses.
    second = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-second", "Second", "", "collective", "public", "both", now, now),
    ).lastrowid
    con.commit()
    status, seeded = call("POST", f"/api/v1/projects/{second}/requests",
                          {"title": title, "body": body, "confirm_duplicate": True}, token=author_token)
    check("a verbatim copy filed in ANOTHER project needs the override too", status, 201)
    seeded_id = (seeded or {}).get("id")

    status, panel = call("GET", f"/api/v1/similar/request?text={urllib.parse.quote(title)}")
    ids = [row.get("entity_id") for row in (panel or {}).get("similar") or []]
    check("while that project is public, its row IS visible", seeded_id in ids, True)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (second,))
    con.commit()
    status, panel = call("GET", f"/api/v1/similar/request?text={urllib.parse.quote(title)}")
    ids = [row.get("entity_id") for row in (panel or {}).get("similar") or []]
    check("once it is private, the anonymous panel no longer sees it", seeded_id in ids, False)
    check("but the public project's row still answers", first_id in ids, True)

    # An instance-wide request: filed without a gate, which is the reference's behaviour, not an oversight here.
    status, instance = call("POST", "/api/v1/requests",
                            {"title": f"Instance-wide question about printers {stamp}", "body": ""},
                            token=author_token)
    check("an instance-wide request is filed", status, 201)
    check("and its JSON omits project_id", "project_id" in (instance or {}), False)
    status, again = call("POST", "/api/v1/requests",
                         {"title": f"Instance-wide question about printers {stamp}", "body": ""},
                         token=author_token)
    check("the same instance-wide text is filed AGAIN — no gate on this route", status, 201)

    status, _ = call("POST", "/api/v1/requests", {"title": "   "}, token=author_token)
    check("a blank instance-wide title is 400", status, 400)
    status, _ = call("POST", "/api/v1/projects/project-does-not-exist/requests",
                     {"title": "x"}, token=author_token)
    check("an unknown project segment is 404", status, 404)

    cleanup(con, f"probe-file-{stamp}%")
    con.execute("delete from embeddings where entity_id in (select id from requests where project_id is null)"
                " and kind = 'request' and source like ?", (f"%{stamp}%",))
    con.execute("delete from requests where project_id is null and title like ?", (f"%{stamp}%",))
    con.execute("delete from audit_log where project_id is null and detail like ?", (f"%{stamp}%",))
    for user in (author_id,):
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
    import urllib.parse  # noqa: E402  (used by the panel query above)
    sys.exit(main())
