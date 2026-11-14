#!/usr/bin/env python3
"""Row 11b part 43 (B4b): criterion writes and the charter editor.

The rules, each one a decision the reference made for a reason:

* **a criterion is project-scoped twice** — the row is looked up with the project in the WHERE, and a vote re-checks that
  both features belong to that project. Without the second check a caller pools votes across projects by guessing ids;
* **one vote per (criterion, voter, pair)** — a re-vote is a double count and a silently doubled opinion is invisible in
  the audit trail;
* **`skip`, `both` and `neither` are recorded and move no rating** — abstention is data, and a neutral vote that left no
  trace would be indistinguishable from a lost one;
* **an absent direction is `higher_is_better` and a non-positive weight is `1.0`, silently** (leaving a field out is not a
  mistake) while an explicit bad direction is refused (that one changes what the ranking means);
* **a profile is replaced wholesale on re-save** — merging leaves stale weights for criteria the caller stopped
  mentioning, and a profile that silently keeps them is not the profile that was saved;
* **the charter's four bounds are refusals** — each guards a switch that can otherwise be set to a value that silently
  disables a mechanism for the whole project: the failure mode where nothing errors and nothing works;
* **authenticate before resolving the project**, so an anonymous caller cannot use the status code as a slug oracle.
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
    criteria = [row[0] for row in con.execute(
        f"select id from criteria where project_id in ({marks})", params).fetchall()]
    if criteria:
        cmarks = ",".join("?" * len(criteria))
        cparams = tuple(criteria)
        con.execute(f"delete from criterion_votes where criterion_id in ({cmarks})", cparams)
        con.execute(f"delete from criterion_ratings where criterion_id in ({cmarks})", cparams)
        profiles = [row[0] for row in con.execute(
            f"select id from criteria_profiles where project_id in ({marks})", params).fetchall()]
        if profiles:
            pmarks = ",".join("?" * len(profiles))
            con.execute(f"delete from criteria_profile_weights where profile_id in ({pmarks})", tuple(profiles))
        con.execute(f"delete from criteria_profiles where project_id in ({marks})", params)
        # `cmarks` are CRITERION ids — comparing them against `project_id` deletes nothing and leaves the projects
        # undeletable behind a foreign key.
        con.execute(f"delete from criteria where id in ({cmarks})", cparams)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from charters where project_id in ({marks})", params)
    con.execute(f"delete from features where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    maintainer_id, maintainer_token = register(f"crmaint{stamp}")
    contributor_id, contributor_token = register(f"crcontrib{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-crit-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Criteria probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    other = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-other", "Other", "", "collective", "public", "both", now, now),
    ).lastrowid
    for user, role in ((maintainer_id, "maintainer"), (contributor_id, "contributor")):
        con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                    (project, user, role, now))
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (other, maintainer_id, "maintainer", now))
    con.execute(
        "INSERT INTO charters (project_id, quorum_ratio, quorum_min, consent_ratio, override_ratio,"
        " vote_window_days, merge_requires_quorum, merge_quorum_min, merge_quorum_ratio,"
        " require_reviewer_approval, wip_in_progress, wip_review, lam, mu, pain_halflife_days,"
        " rep_halflife_days, glicko_tau, vote_weight_cap, support_ratio_min, theme_lifetime_days,"
        " solution_call_min_voters, solution_call_confidence, solution_stable_hours)"
        " VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
        (project, 0.5, 3, 0.66, 0.75, 14, False, 2, 0.6, False, 3, 5, 0.3, 0.7, 30, 90, 0.5, 1.0, 0.6, 30,
         3, 0.8, 72),
    )
    features = []
    for index in range(2):
        features.append(con.execute(
            "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?) RETURNING id",
            (project, maintainer_id, f"Criterion feature {index}", "", "proposed", now, now),
        ).lastrowid)
    foreign = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (other, maintainer_id, "Other project's feature", "", "proposed", now, now),
    ).lastrowid
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    def audits(action):
        return con.execute(
            "SELECT count(*) FROM audit_log WHERE project_id = ? AND action = ?",
            (project, action)).fetchone()[0]

    # ── charter ─────────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("PUT", f"/api/v1/projects/{slug}/charter", {"consent_ratio": 0.9})
    check("an anonymous charter edit is 401", status, 401)
    status, _ = call("PUT", f"/api/v1/projects/nonesuch-{stamp}/charter", {"consent_ratio": 0.9})
    check("and the same 401 for an unknown slug (no slug oracle)", status, 401)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/charter", {"consent_ratio": 0.9},
                     token=contributor_token)
    check("a contributor may not edit the charter", status, 403)
    status, updated = call("PUT", f"/api/v1/projects/{slug}/charter",
                           {"consent_ratio": 0.9, "quorum_min": 5}, token=maintainer_token)
    check("a maintainer may", (status, (updated or {}).get("status")), (200, "charter updated"))
    stored = con.execute("SELECT consent_ratio, quorum_min FROM charters WHERE project_id = ?",
                         (project,)).fetchone()
    check("and the values are stored", (stored[0], stored[1]), (0.9, 5))
    check("a charter edit is audited", audits("charter.update"), 1)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/charter",
                     {"consent_ratio": 0.9, "solution_call_confidence": 0.0}, token=maintainer_token)
    check("confidence 0 is refused", status, 400)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/charter",
                     {"consent_ratio": 0.9, "solution_call_confidence": 1.0}, token=maintainer_token)
    check("confidence 1.0 is refused (exclusive)", status, 400)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/charter",
                     {"consent_ratio": 0.9, "solution_call_min_voters": 0}, token=maintainer_token)
    check("min voters 0 is refused", status, 400)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/charter",
                     {"consent_ratio": 0.9, "solution_stable_hours": 0}, token=maintainer_token)
    check("stable hours 0 is refused", status, 400)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/charter",
                     {"consent_ratio": 0.9, "theme_lifetime_days": 0}, token=maintainer_token)
    check("theme lifetime 0 is refused", status, 400)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/charter",
                     {"consent_ratio": 0.9, "theme_lifetime_days": 40000}, token=maintainer_token)
    check("theme lifetime past a century is refused", status, 400)
    kept = con.execute("SELECT consent_ratio, theme_lifetime_days FROM charters WHERE project_id = ?",
                       (project,)).fetchone()
    check("a refused edit changed nothing", (kept[0], kept[1]), (0.9, 30))
    status, _ = call("PUT", f"/api/v1/projects/{slug}/charter", {"wip_review": 9}, token=maintainer_token)
    kept = con.execute("SELECT wip_review, consent_ratio FROM charters WHERE project_id = ?",
                       (project,)).fetchone()
    check("a PARTIAL body is filled from the current charter, not zeroed", (status, tuple(kept)), (200, (9, 0.9)))

    # ── criteria: create ───────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"/api/v1/projects/{slug}/criteria", {"slug": "speed", "name": "Speed"})
    check("creating a criterion anonymously is 401", status, 401)
    status, _ = call("POST", f"/api/v1/projects/{slug}/criteria", {"slug": "speed", "name": "Speed"},
                     token=contributor_token)
    check("a contributor may not", status, 403)
    status, criterion = call("POST", f"/api/v1/projects/{slug}/criteria",
                             {"slug": "Speed", "name": "Speed of delivery",
                              "description": "How fast it lands"}, token=maintainer_token)
    check("a maintainer may", status, 201)
    criterion = criterion or {}
    check("the slug is lowercased", criterion.get("slug"), "speed")
    check("an absent direction defaults to higher_is_better",
          criterion.get("direction"), "higher_is_better")
    check("and an absent weight defaults to 1.0", criterion.get("default_weight"), 1.0)
    check("a new criterion is active", criterion.get("active"), True)
    criterion_id = criterion.get("id")
    status, _ = call("POST", f"/api/v1/projects/{slug}/criteria",
                     {"slug": "bad", "name": "Bad", "direction": "sideways"}, token=maintainer_token)
    check("a bad direction is 400", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/criteria", {"slug": "", "name": "No slug"},
                     token=maintainer_token)
    check("an empty slug is 400", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/criteria", {"slug": "speed", "name": "Again"},
                     token=maintainer_token)
    check("a duplicate slug in the same project is 400", status, 400)

    # ── criteria: active ───────────────────────────────────────────────────────────────────────────────────────────
    status, off = call("PUT", f"/api/v1/projects/{slug}/criteria/{criterion_id}/active",
                       {"active": False}, token=maintainer_token)
    check("switching it off works", (status, (off or {}).get("active")), (200, False))
    status, _ = call("PUT", f"/api/v1/projects/{slug}/criteria/99999999/active",
                     {"active": True}, token=maintainer_token)
    check("an unknown criterion is 404", status, 404)
    call("PUT", f"/api/v1/projects/{slug}/criteria/{criterion_id}/active",
         {"active": True}, token=maintainer_token)

    # ── criteria: vote ─────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"/api/v1/projects/{slug}/criteria/{criterion_id}/vote",
                     {"feature_a": features[0], "feature_b": features[0], "outcome": "a"},
                     token=contributor_token)
    check("comparing a feature with itself is 400", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/criteria/{criterion_id}/vote",
                     {"feature_a": features[0], "feature_b": features[1], "outcome": "z"},
                     token=contributor_token)
    check("an unknown outcome is 400", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/criteria/{criterion_id}/vote",
                     {"feature_a": features[0], "feature_b": foreign, "outcome": "a"},
                     token=contributor_token)
    check("a feature from another project is refused", status, 400)
    votes = con.execute("SELECT count(*) FROM criterion_votes WHERE criterion_id = ?",
                        (criterion_id,)).fetchone()[0]
    check("and nothing is recorded for it", votes, 0)
    status, moved = call("POST", f"/api/v1/projects/{slug}/criteria/{criterion_id}/vote",
                         {"feature_a": features[0], "feature_b": features[1], "outcome": "a"},
                         token=contributor_token)
    check("a comparison is accepted", status, 200)
    moved = moved or {}
    check("and both ratings come back",
          (sorted(moved.keys()), (moved.get("feature_a") or {}).get("games")), (["feature_a", "feature_b"], 1))
    winner = (moved.get("feature_a") or {}).get("r") or 0
    checker = (moved.get("feature_b") or {}).get("r") or 0
    check("the winner's rating rose and the loser's fell", (winner > 1500, checker < 1500), (True, True))
    status, _ = call("POST", f"/api/v1/projects/{slug}/criteria/{criterion_id}/vote",
                     {"feature_a": features[0], "feature_b": features[1], "outcome": "a"},
                     token=contributor_token)
    check("voting the same comparison twice is 400", status, 400)
    status, neutral = call("POST", f"/api/v1/projects/{slug}/criteria/{criterion_id}/vote",
                           {"feature_a": features[1], "feature_b": features[0], "outcome": "skip"},
                           token=contributor_token)
    check("a skip is accepted", status, 200)
    check("and moves no rating", round(((neutral or {}).get("feature_a") or {}).get("r") or 0, 6), round(checker, 6))
    check("a neutral vote is audited", audits("criterion_vote_neutral"), 1)

    # ── criteria: profile ─────────────────────────────────────────────────────────────────────────────────────────
    body = {"slug": "balanced", "name": "Balanced", "weights": {str(criterion_id): 2.5}}
    status, _ = call("POST", f"/api/v1/projects/{slug}/criteria-profiles", body, token=contributor_token)
    check("a contributor may not save a profile", status, 403)
    status, profile = call("POST", f"/api/v1/projects/{slug}/criteria-profiles", body,
                           token=maintainer_token)
    check("a maintainer may", status, 201)
    check("and gets the id and slug back",
          (bool((profile or {}).get("id")), (profile or {}).get("slug")), (True, "balanced"))
    profile_id = (profile or {}).get("id")
    status, again = call("POST", f"/api/v1/projects/{slug}/criteria-profiles",
                         {"slug": "balanced", "name": "Balanced again", "weights": {}}, token=maintainer_token)
    check("re-saving the same slug is not an error", status, 201)
    check("and returns the SAME id", (again or {}).get("id"), profile_id)
    weights = con.execute("SELECT count(*) FROM criteria_profile_weights WHERE profile_id = ?",
                          (profile_id,)).fetchone()[0]
    check("re-saving REPLACES the weights wholesale", weights, 0)
    foreign_criterion = con.execute(
        "INSERT INTO criteria (project_id, slug, name, description, direction, default_weight, active,"
        " created_by, created_at, updated_at) VALUES (?,?,?,?,?,?,1,?,?,?) RETURNING id",
        (other, "foreign", "Foreign", "", "higher_is_better", 1.0, maintainer_id, now, now),
    ).lastrowid
    con.commit()
    status, _ = call("POST", f"/api/v1/projects/{slug}/criteria-profiles",
                     {"slug": "bad", "name": "Bad", "weights": {str(foreign_criterion): 1}},
                     token=maintainer_token)
    check("a weight for another project's criterion is refused", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/criteria-profiles",
                     {"slug": "bad", "name": "Bad", "weights": {"speed": 1}}, token=maintainer_token)
    check("a non-numeric weight key is 400", status, 400)

    cleanup(con, f"{slug}%")
    con.commit()
    for user in (maintainer_id, contributor_id):
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
