#!/usr/bin/env python3
"""Row 11b part 27: composite ranking — the weighting rules are the route.

Five behaviours, each of which a plausible port gets wrong, and the negative control for this probe is the one the Go
deliberately removed: unnamed criteria joining at their own default.

1. NO body = every ACTIVE criterion at its default (inactive excluded);
2. naming ONE weight EXCLUDES every criterion it did not name;
3. `include_inactive` brings the retired criterion back;
4. `require_min_rd` drops a criterion nobody has played (all ratings at the initial RD, zero games);
5. a non-numeric weight key is 400 with the key echoed, and `limit` truncates while `count` stays honest.
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
    # The Content-Type goes on ONLY when there is a body. `Content-Type: application/json` with an empty body makes
    # axum's Json extractor try to parse "" and answer 400, so a client that always sends the header cannot make the
    # "no body means every default" request at all — which is the request this route is built around.
    if data is not None:
        headers["Content-Type"] = "application/json"
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


def cleanup(con, like):
    for (project,) in con.execute("select id from projects where slug like ?", (like,)).fetchall():
        con.execute("delete from criterion_ratings where criterion_id in"
                    " (select id from criteria where project_id = ?)", (project,))
        con.execute("delete from criteria where project_id = ?", (project,))
        con.execute("delete from features where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    slug = f"probe-composite-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"cpauth{stamp}", "display_name": "Composite", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-composite-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Composite probe", "", "collective", "public", "both", now, now),
    ).lastrowid

    def feature(title):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?) RETURNING id",
            (project, author, title, "proposed", now, now),
        ).lastrowid

    def criterion(slug_name, direction, active=1, default_weight=1.0):
        return con.execute(
            "INSERT INTO criteria (project_id, slug, name, description, direction, default_weight, active)"
            " VALUES (?,?,?,?,?,?,?) RETURNING id",
            (project, slug_name, slug_name.title(), "", direction, default_weight, active),
        ).lastrowid

    good = feature("Better on both")
    middling = feature("Better on one")
    unrated = feature("Nobody played")

    speed = criterion("speed", "higher_is_better")
    cost = criterion("cost", "lower_is_better")
    retired = criterion("retired", "higher_is_better", active=0)
    unplayed = criterion("unplayed", "higher_is_better")

    def rate(criterion_id, feature_id, r, rd=100.0, games=3):
        con.execute(
            "INSERT INTO criterion_ratings (criterion_id, feature_id, r, rd, sigma, games, updated_at)"
            " VALUES (?,?,?,?,?,?,?)",
            (criterion_id, feature_id, r, rd, 0.06, games, now),
        )

    rate(speed, good, 1700.0)
    rate(speed, middling, 1500.0)
    rate(cost, good, 10.0)
    rate(cost, middling, 20.0)
    rate(retired, good, 1700.0)
    rate(retired, middling, 1500.0)
    # Unplayed: initial RD, zero games — the criterion require_min_rd must drop.
    rate(unplayed, good, 1500.0, rd=350.0, games=0)
    rate(unplayed, middling, 1500.0, rd=350.0, games=0)
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("POST", f"/api/v1/projects/{project}/rank/composite")
    check("no body ranks with the defaults", status, 200)
    body = body or {}
    check("inactive criterion is excluded, unplayed one is not", body.get("count"), 2)
    top = (body.get("results") or [{}])[0]
    check("the feature that is better on both ranks first", top.get("feature_id"), good)
    check("with a rank of 1", top.get("rank"), 1)
    # THREE, not two: an ACTIVE criterion nobody has played still contributes (each weight 1/3), which is exactly the
    # dilution `require_min_rd` exists to remove — and the assertion below measures that dilution in the confidence.
    check("and every active criterion explains it, played or not",
          len(top.get("contributions") or []), 3)
    check("the lower_is_better criterion reversed its scale",
          [round(c["normalised"], 6) for c in top["contributions"] if c["slug"] == "cost"], [1.0])
    check("confidence is reported beside the rank", isinstance(top.get("confidence"), (int, float)), True)
    check("and shows the unplayed criterion's dilution",
          round(top.get("confidence"), 3), 183.333)

    status, body = call("POST", f"/api/v1/projects/{project}/rank/composite",
                        {"weights": {str(speed): 1.0}})
    check("naming one weight makes it the whole query",
          len(((body or {}).get("results") or [{}])[0].get("contributions") or []), 1)
    check("and drops the criterion it did not name",
          [c["slug"] for c in (body or {})["results"][0]["contributions"]], ["speed"])

    status, body = call("POST", f"/api/v1/projects/{project}/rank/composite",
                        {"include_inactive": True})
    check("include_inactive brings the retired criterion back",
          len(((body or {}).get("results") or [{}])[0].get("contributions") or []), 4)

    status, body = call("POST", f"/api/v1/projects/{project}/rank/composite",
                        {"require_min_rd": True})
    check("require_min_rd drops the criterion nobody played",
          len(((body or {}).get("results") or [{}])[0].get("contributions") or []), 2)
    # The number the flag exists to change: dropping the unplayed criterion removes its 350 RD from the mean.
    check("and the confidence becomes the played criteria's mean",
          round(((body or {}).get("results") or [{}])[0].get("confidence"), 3), 100.0)

    status, body = call("POST", f"/api/v1/projects/{project}/rank/composite",
                        {"weights": {"not-a-number": 1.0}})
    check("a non-numeric weight key is 400", status, 400)
    check("and the key is echoed so the mistake is actionable",
          "not-a-number" in json.dumps(body or {}), True)

    status, body = call("POST", f"/api/v1/projects/{project}/rank/composite", {"limit": 1})
    check("limit truncates", len((body or {}).get("results") or []), 1)
    check("and count follows the truncation", (body or {}).get("count"), 1)

    status, body = call("POST", f"/api/v1/projects/{project}/rank/composite", {"profile": "nope"})
    check("an unknown profile is 404, not a silent default ranking", status, 404)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    status, _ = call("POST", f"/api/v1/projects/{project}/rank/composite")
    check("a private project's ranking is 404 without a token", status, 404)

    cleanup(con, "probe-composite-%")
    con.execute("DELETE FROM api_tokens WHERE user_id = ?", (author,))
    con.execute("DELETE FROM users WHERE id = ?", (author,))
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
