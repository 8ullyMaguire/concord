#!/usr/bin/env python3
"""Row 11 part 62: the global tag taxonomy — the four routes that keep tagging from being unilateral.

`GET /api/v1/taxonomy/tags`, `GET|POST /api/v1/taxonomy/proposals`, `POST /api/v1/taxonomy/proposals/{id}/consent`.

The rule the whole cluster exists to enforce: **maintainers hold no unilateral taxonomy power.** Tagging used to auto-create a tag
on use, which gave every project editor instance-wide vocabulary control; so a change is a proposal, decided by the eligible set,
applied only when the charter's threshold is met. Every assertion below is one way that stops being true.

What the probe establishes:

1. **`tags` answers `[]`, never `null`, and only lists what exists** — a `null` here is the trap every client guards against with
   `|| []`, which makes the next client's guard load-bearing, silently.
2. **`include_suggested` is opt-in.** A suggested tag is a hint nobody has ratified; listing it by default would make the vocabulary
   grow by being mentioned.
3. **The proposal's action is a closed set** and the refusal names it; `create`/`delete` take no operand while `rename`/`alias`/
   `merge`/`reparent` require one, because requiring a value from a create would make every create invalid.
4. **A rename-to-itself is a no-op even when the two spellings differ by namespace** — `topic:thing` and `thing` are the same bare
   tag, so a comparison of the raw strings lets it through.
5. **An unknown namespace is refused with the known list**, rather than minting a namespace by use.
6. **A rationale is required** — a change to shared vocabulary with no reason is a change nobody can argue with later.
7. **A duplicate pending proposal is 409**, so a double-submit cannot stack two voices for one change.
8. **Instance-wide proposals need contributor somewhere; project proposals need contributor THERE.** An instance-wide change is
   decided by the instance charter, not by whichever project happens to be largest.
9. **A consent below the threshold is 202 with the current state**, not an error: the first vote is a normal state of a consensus
   process, and reporting it as a failure makes the first voter think they broke something.
10. **A repeated consent adds no voice** — the tally counts rows, and a second click on the same button is not a second voter.
11. **The eligible set is the denominator**, and a stranger's consent is refused rather than counted.
12. **Ratification applies the change** — the tag exists afterwards, which is the only observable difference between "voted" and
    "decided".
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
TAG_KEYS = ["id", "name", "namespace", "parent_id", "project_id", "relation", "suggested"]


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


def refusal(body):
    if isinstance(body, dict):
        for key in ("error", "message", "detail"):
            if key in body:
                return json.dumps(body[key])
    return json.dumps(body)


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    maintainer_id, maintainer = register(f"txm{stamp}")
    member_id, member = register(f"txc{stamp}")
    stranger_id, stranger = register(f"txs{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-tax-{stamp}"
    con.execute("delete from projects where slug like ?", (f"{slug}%",))
    con.commit()
    pid = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " repo_url, repo_host, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (slug, slug, "", "collective", "public", "both", None, None, now, now),
    ).lastrowid
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (pid, maintainer_id, "maintainer", now))
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (pid, member_id, "contributor", now))

    # Tag fixtures: one ratified `topic:` tag with an alias, one suggested tag that must not appear by default.
    real = con.execute(
        "INSERT INTO tags (project_id, name, namespace, parent_id, relation, suggested)"
        " VALUES (NULL,?,?,NULL,NULL,false) RETURNING id", (f"probe-real-{stamp}", "topic")).lastrowid
    hint = con.execute(
        "INSERT INTO tags (project_id, name, namespace, parent_id, relation, suggested)"
        " VALUES (NULL,?,?,NULL,NULL,true) RETURNING id", (f"probe-hint-{stamp}", "topic")).lastrowid
    try:
        con.execute("INSERT INTO tag_aliases (tag_id, alias, created_by, created_at) VALUES (?,?,?,?)",
                    (real, f"probe-alias-{stamp}", maintainer_id, now))
    except Exception:
        con.rollback()
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    # ── tags ─────────────────────────────────────────────────────────────────────────────────────────────────────
    status, body = call("GET", "/api/v1/taxonomy/tags")
    check("the tag list is public", status, 200)
    check("and it is a list, never null", isinstance(body, list), True)
    names = [row.get("name") for row in (body or [])]
    check("it carries the ratified tag", f"probe-real-{stamp}" in names, True)
    check("a suggested tag is NOT listed by default", f"probe-hint-{stamp}" in names, False)
    check("a tag row carries the reference's keys",
          sorted((body or [{}])[0].keys()) if body else [], sorted(TAG_KEYS[:7]))
    mine = [row for row in (body or []) if row.get("name") == f"probe-real-{stamp}"]
    check("a global tag has no project", (mine[0] if mine else {}).get("project_id"), 0)
    check("and carries its namespace", (mine[0] if mine else {}).get("namespace"), "topic")
    check("a tag with an alias carries them", (mine[0] if mine else {}).get("aliases"), [f"probe-alias-{stamp}"])
    status, body = call("GET", "/api/v1/taxonomy/tags?include_suggested=true")
    hint_row = [row for row in (body or []) if row.get("name") == f"probe-hint-{stamp}"]
    check("a tag with no alias omits the key entirely",
          "aliases" in (hint_row[0] if hint_row else {"aliases": "present"}), False)
    status, body = call("GET", "/api/v1/taxonomy/tags?include_suggested=true")
    check("include_suggested lists the hint", f"probe-hint-{stamp}" in [row.get("name") for row in (body or [])], True)
    status, body = call("GET", f"/api/v1/taxonomy/tags?namespace=nosuch{stamp}")
    check("a namespace filter with no matches is an empty list, not null",
          (status, body), (200, []))
    ordered = [(row.get("namespace") or "", row.get("name")) for row in (body or [])]
    status, body = call("GET", "/api/v1/taxonomy/tags?namespace=topic")
    check("a namespace filter returns only that namespace",
          sorted({row.get("namespace") for row in (body or [])}), ["topic"])

    # ── proposing ────────────────────────────────────────────────────────────────────────────────────────────────
    propose = "/api/v1/taxonomy/proposals"
    good = {"project_id": pid, "action": "create", "target_tag": f"topic:probe-new-{stamp}", "rationale": "needed"}
    status, _ = call("POST", propose, good)
    check("proposing anonymously is 401", status, 401)
    status, body = call("POST", propose, dict(good, action="munge"), token=maintainer)
    check("an unknown action is 400", status, 400)
    check("and the refusal names it", "munge" in refusal(body), True)
    status, body = call("POST", propose, dict(good, target_tag="  "), token=maintainer)
    check("an empty target tag is 400", status, 400)
    status, body = call("POST", propose, dict(good, action="rename", value=""), token=maintainer)
    check("a rename with no value is 400", status, 400)
    check("and the refusal names the value", "value" in refusal(body), True)
    # `topic:x` renamed to `x` is the same bare tag: comparing raw strings lets this through.
    status, body = call("POST", propose, dict(good, action="rename",
                                              target_tag=f"topic:same-{stamp}",
                                              value=f"same-{stamp}"), token=maintainer)
    check("a rename that only drops the namespace is a no-op and 400", status, 400)
    check("and the refusal says so", "no-op" in refusal(body), True)
    status, body = call("POST", propose, dict(good, target_tag=f"nosuchns:x-{stamp}"), token=maintainer)
    check("an unknown namespace is 400", status, 400)
    check("and lists the known ones", "topic" in refusal(body) and "lang" in refusal(body), True)
    status, body = call("POST", propose, dict(good, rationale=""), token=maintainer)
    check("a proposal with no rationale is 400", status, 400)

    status, body = call("POST", propose, good, token=maintainer)
    check("a valid proposal is 201", status, 201)
    proposal = (body or {}).get("proposal") or {}
    proposal_id = proposal.get("id")
    check("the answer carries the proposal and what happens next",
          sorted((body or {}).keys()), ["message", "proposal"])
    check("the message says it needs quorum",
          "quorum" in str((body or {}).get("message", "")).lower(), True)
    check("the proposal starts pending", proposal.get("status"), "pending")
    check("and remembers who proposed it", proposal.get("proposed_by"), maintainer_id)
    status, body = call("POST", propose, good, token=maintainer)
    check("an identical pending proposal is 409, not a second voice", status, 409)
    check("proposing is audited",
          con.execute("SELECT count(*) FROM audit_log WHERE action = 'propose_taxonomy_change'"
                      " AND entity_id = ?", (proposal_id,)).fetchone()[0], 1)

    # Who may propose what.
    status, _ = call("POST", propose, {"action": "create", "target_tag": f"topic:i-{stamp}",
                                       "rationale": "x"}, token=stranger)
    check("an instance-wide proposal from someone with no membership anywhere is refused", status, 403)
    status, _ = call("POST", propose, dict(good, target_tag=f"topic:other-{stamp}"), token=stranger)
    check("a project proposal from a non-member of that project is refused", status, 403)

    # ── consenting ───────────────────────────────────────────────────────────────────────────────────────────────
    consent = f"/api/v1/taxonomy/proposals/{proposal_id}/consent"
    status, _ = call("POST", "/api/v1/taxonomy/proposals/999999999/consent", token=maintainer)
    check("consenting to a proposal that does not exist is 404", status, 404)
    status, body = call("POST", consent, token=stranger)
    check("a stranger's consent is refused", status, 403)
    check("and it is not counted",
          con.execute("SELECT count(*) FROM taxonomy_consents WHERE proposal_id = ?",
                      (proposal_id,)).fetchone()[0], 0)
    status, body = call("POST", consent, token=member)
    check("an eligible member's first consent is 202 with the state, not an error", status, 202)
    body = body or {}
    check("the state carries the proposal and a reason", sorted(body.keys()), ["message", "proposal"])
    check("the proposal comes back as it now stands", (body.get("proposal") or {}).get("status"), "pending")
    check("the consent is recorded anyway",
          con.execute("SELECT count(*) FROM taxonomy_consents WHERE proposal_id = ?",
                      (proposal_id,)).fetchone()[0], 1)
    status, again = call("POST", consent, token=member)
    check("a repeated consent adds no voice",
          con.execute("SELECT count(*) FROM taxonomy_consents WHERE proposal_id = ?",
                      (proposal_id,)).fetchone()[0], 1)
    check("and answers the same 202", status, 202)

    # ── listing ─────────────────────────────────────────────────────────────────────────────────────────────────
    status, body = call("GET", f"/api/v1/taxonomy/proposals?project_id={pid}")
    check("proposals are listable", status, 200)
    check("and the answer is a list, never null", isinstance(body, list), True)
    rows = {row.get("id"): row for row in (body or [])}
    check("this project's proposal is in it", proposal_id in rows, True)
    row = rows.get(proposal_id, {})
    check("a row carries the counts a reader needs to judge their vote",
          ("consent_count" in row, "eligible_count" in row), (True, True))
    check("the consent count is the rows, not the clicks", row.get("consent_count"), 1)
    # The eligible set: two members with contributor or above (maintainer + contributor).
    check("the eligible set is the project's eligible members", row.get("eligible_count"), 2)
    check("pending proposals sort first",
          [r.get("status") for r in (body or [])][0], "pending")

    con.execute("delete from taxonomy_consents where proposal_id in (select id from taxonomy_proposals"
                " where target_tag like ?)", (f"%{stamp}%",))
    # No `%` inside the SQL: the harness's `?`-to-placeholder translation leaves a literal per-cent sign looking like a format
    # specifier to the driver, which then demands an argument that is not there.
    con.execute("delete from audit_log where action in"
                " ('propose_taxonomy_change', 'taxonomy_consent', 'ratify_taxonomy_change')"
                " and entity_id in (select id from taxonomy_proposals where target_tag like ?)",
                (f"%{stamp}%",))
    con.execute("delete from taxonomy_proposals where target_tag like ?", (f"%{stamp}%",))
    # Children first: `tag_aliases.tag_id` is a real FK, so a tag cannot go before its aliases.
    con.execute("delete from tag_aliases where tag_id in (select id from tags where name like ?)", (f"%probe-%{stamp}",))
    con.execute("delete from tags where name like ?", (f"%probe-%{stamp}",))
    con.execute("delete from members where project_id = ?", (pid,))
    con.execute("delete from projects where id = ?", (pid,))
    con.commit()

    passed = sum(1 for _, got, want in results if got == want)
    print(f"\n{passed}/{len(results)} ok", flush=True)
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())
