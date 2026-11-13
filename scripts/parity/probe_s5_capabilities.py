#!/usr/bin/env python3
"""Row 11b part 14: §4.10's capabilities panel.

The panel is a JOIN of two decisions and both are easy to get wrong in a way a page shows:

1. **the whole catalogue is rendered** — an unasserted capability is the contribution queue, so hiding it removes
   the reason the panel exists;
2. **`asserted: false` is not "no"** — the value and state are ABSENT, not "unknown", and the counts say how many
   of each there are;
3. **a dispute outranks a confirmation at the same quorum** — two confirms and two disputes is `disputed`;
4. **a disputed claim keeps its original value** — substituting "no" turns "people disagree" into "this is false".
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


def register(prefix, stamp):
    status, body = call(
        "POST", "/api/v1/auth/register",
        {"username": f"{prefix}{stamp}", "display_name": prefix, "password": "correct horse"},
    )
    if status != 201:
        print(f"register {prefix} failed: {status} {body}")
        sys.exit(2)
    return body["user"]["id"], body["token"]


def cleanup(con, like, keys_like):
    # The project slug and the capability keys carry different prefixes — deleting by one and expecting the other
    # to go is how a probe poisons its own next run (the confirmations outlive the assertion and the users cannot
    # be removed).
    for (project,) in con.execute("select id from projects where slug like ?", (like,)).fetchall():
        con.execute("delete from capability_confirmations where assertion_id in "
                    "(select id from capability_assertions where project_id = ?)", (project,))
        con.execute("delete from capability_assertions where project_id = ?", (project,))
        con.execute("delete from members where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    # The pattern is a PARAMETER, not a literal: the harness formats its SQL, and a literal `%` in a string is a
    # format specifier there ("tuple index out of range") rather than a wildcard.
    con.execute(
        "delete from capability_confirmations where user_id in"
        " (select id from users where username like ?)",
        ("cap%",),
    )
    con.execute("delete from capabilities where key like ?", (keys_like,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    author_id, _ = register("capauth", stamp)
    confirmer_a, _ = register("capconf", stamp)
    confirmer_b, _ = register("capconf2", stamp)
    disputer_a, _ = register("capdisp", stamp)
    disputer_b, _ = register("capdisp2", stamp)
    like = f"cap-{stamp}"
    corrupt_slug = f"probe-cap-{stamp}"

    con = harness.connect("unused")
    cleanup(con, f"probe-cap-{stamp}", f"cap-{stamp}%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (corrupt_slug, "Capability probe", "", "collective", "public", "both", now, now),
    ).lastrowid

    # Three capabilities: one asserted and confirmed, one asserted and disputed, one nobody touched.
    keys = {}
    for suffix, label in (("one", "Capability one"), ("two", "Capability two"), ("three", "Capability three")):
        key = f"{like}-{suffix}"
        keys[suffix] = key
        con.execute(
            "INSERT INTO capabilities (key, label, category, kind, values) VALUES (?,?,?,?,?)",
            (key, label, "probe", "boolean", '["yes","no"]'),
        )

    def assertion(key, value, who):
        return con.execute(
            "INSERT INTO capability_assertions (capability, project_id, value, evidence, asserted_by,"
            " asserted_at) VALUES (?,?,?,?,?,?) RETURNING id",
            (key, project, value, "because", who, now),
        ).lastrowid

    confirmed = assertion(keys["one"], "yes", author_id)
    for who in (confirmer_a, confirmer_b):
        con.execute(
            "INSERT INTO capability_confirmations (assertion_id, user_id, confirmed, at) VALUES (?,?,?,?)",
            (confirmed, who, 1, now),
        )
    disputed = assertion(keys["two"], "yes", author_id)
    for who in (confirmer_a, confirmer_b):
        con.execute(
            "INSERT INTO capability_confirmations (assertion_id, user_id, confirmed, at) VALUES (?,?,?,?)",
            (disputed, who, 1, now),
        )
    for who in (disputer_a, disputer_b):
        con.execute(
            "INSERT INTO capability_confirmations (assertion_id, user_id, confirmed, at) VALUES (?,?,?,?)",
            (disputed, who, 0, now),
        )
    # The asserter's own confirmation must NOT count: the query excludes `user_id = asserted_by`.
    con.execute(
        "INSERT INTO capability_confirmations (assertion_id, user_id, confirmed, at) VALUES (?,?,?,?)",
        (confirmed, author_id, 1, now),
    )
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{project}/capabilities")
    check("the panel answers", status, 200)
    check("with the project id", (body or {}).get("project_id"), project)
    rows = {row["key"]: row for row in (body or {}).get("capabilities", [])}
    # The catalogue is the INSTANCE's, not the probe's: the panel must render every catalogued capability, so the
    # assertion is that the probe's three are among them and that the counts partition the whole catalogue —
    # `unknown = total - asserted`. Asserting a total of 3 would only pass on an empty instance.
    check("the whole catalogue is rendered", set(keys.values()) <= set(rows), True)
    catalogue_size = len(rows)
    check("asserted and unasserted partition the catalogue",
          ((body or {}).get("asserted_count"), (body or {}).get("unknown_count")), (2, catalogue_size - 2))

    check("two confirmations read as confirmed", rows[keys["one"]]["state"], "confirmed")
    check("and the asserter's own confirmation does not count", rows[keys["one"]]["confirms"], 2)
    check("two confirmations and two disputes read as disputed", rows[keys["two"]]["state"], "disputed")
    check("and a disputed claim keeps its original value", rows[keys["two"]]["value"], "yes")

    untouched = rows[keys["three"]]
    check("an unasserted capability says asserted: false", untouched["asserted"], False)
    check("with no value at all, not the word unknown", "value" in untouched, False)
    check("and no state", "state" in untouched, False)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    status, _ = call("GET", f"/api/v1/projects/{project}/capabilities")
    check("a private project's panel is 404 without a token", status, 404)

    cleanup(con, f"probe-cap-{stamp}", f"cap-{stamp}%")
    con.execute("DELETE FROM api_tokens WHERE user_id IN (?,?,?,?,?)",
                (author_id, confirmer_a, confirmer_b, disputer_a, disputer_b))
    con.execute("DELETE FROM users WHERE id IN (?,?,?,?,?)",
                (author_id, confirmer_a, confirmer_b, disputer_a, disputer_b))
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
