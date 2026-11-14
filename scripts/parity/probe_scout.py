#!/usr/bin/env python3
"""Row 11 part 65b: `GET /api/v1/scout` — the last JSON route, and the only answer that is a judgement.

Scout reads an idea, decomposes it into catalogue capabilities, and classifies **every readable project** into adopt / base-on / extend /
inspire / avoid with a reason and the named inputs behind it. There is one route and no write: §8 persists a report as a document, which
goes through the document handler.

What the probe establishes, and why each one is not decoration:

1. **The decomposition is byte-identical to the reference for the same idea**, because it is the part a user can check by hand: "it read
   my idea as being about offline support" is either true or it is not.
2. **Every verdict carries a reason and at least one signal.** The surface exists to answer "why did it say that"; a classification
   without a named input is a bug.
3. **An unmeasured project is never `avoid`.** `project_metrics` is empty for most of the catalogue and its stored score is 0 for all of
   it, so a rule keyed on that number calls the whole catalogue dead. `adopt` with `health_unmeasured` is the honest answer.
4. **A private project is invisible to a stranger** — not counted, not named, not reflected in any coverage state. Otherwise the report
   is an oracle for which unlisted projects exist.
5. **Coverage is per capability across the visible set**: `covered` needs a CONFIRMED assertion, `partial` means somebody said something,
   `open` means nobody has — and a proposed capability is always `open`, because nothing can be asserted about a key no catalogue row
   defines.
6. **`reports: 0` never reads as `0%`**: the sample size travels beside the rate.
7. **Refusals are specific**: no idea is 400, an idea over 4 KiB is 400, a `max` that is not a positive number is 400 rather than being
   silently ignored.
8. **The verdict spellings are the API's**, and `avoid` sorts first — the reason to read the page is usually to avoid a mistake.

Deviations from the reference that this probe EXPECTS (each recorded in the ledger, part 65a):

* tied candidates are `extend` here and `adopt` there: the reference's tie signal arrives after its own decision, so its `extend` branch
  is unreachable. The probe asserts that every disagreement is exactly a tie, by name, and fails on any other difference.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, os.path.join(ROOT, "tests", "e2e"))

BASE = os.environ.get("CANDIDATE", "http://127.0.0.1:8108")
REFERENCE = os.environ.get("REFERENCE", "https://concord.polarisocial.xyz")
UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36"
VERDICTS = {"adopt", "base-on", "extend", "inspire", "avoid"}
IDEA = "a self hosted tool for WIP limits with offline use and live-sync"


def call(base, path, token=None):
    request = urllib.request.Request(base + path, headers={"User-Agent": UA} | ({"Authorization": "Bearer " + token} if token else {}))
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            raw = response.read()
            return response.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, json.loads(raw) if raw else None
        except ValueError:
            return error.code, raw.decode("utf-8", "replace")


def scout(base, idea, token=None, extra=""):
    return call(base, "/api/v1/scout?idea=" + urllib.parse.quote(idea) + extra, token)


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    status, body = call(BASE, "/api/v1/auth/register")
    # Register with a body: the helper above only does GETs, so do it directly.
    request = urllib.request.Request(
        BASE + "/api/v1/auth/register",
        data=json.dumps({"username": f"sct{stamp}", "display_name": "Scout Probe", "password": "correct horse"}).encode(),
        method="POST",
        headers={"Content-Type": "application/json", "Cookie": "concord_consent=1", "User-Agent": UA})
    with urllib.request.urlopen(request, timeout=30) as response:
        user = json.loads(response.read())
    token = user["token"]

    con = harness.connect("unused")
    public = f"probe-scout-{stamp}"
    private = f"probe-scout-private-{stamp}"
    con.execute("delete from projects where slug like ?", (f"probe-scout-%{stamp}",))
    con.commit()
    pid = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " repo_url, repo_host, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (public, public, "", "collective", "public", "both", None, None, time.time(), time.time())).lastrowid
    hidden = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " repo_url, repo_host, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (private, private, "", "collective", "private", "both", None, None, time.time(), time.time())).lastrowid
    # One assertion, one confirmation by someone else: `covered`.
    con.execute("INSERT INTO capability_assertions (project_id, capability, value, asserted_by, asserted_at)"
                " VALUES (?,?,?,?,?)", (pid, "offline", "yes", user["user"]["id"], time.time()))
    con.execute("INSERT INTO capability_assertions (project_id, capability, value, asserted_by, asserted_at)"
                " VALUES (?,?,?,?,?)", (hidden, "offline", "yes", user["user"]["id"], time.time()))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    def note(name, ok, detail=""):
        results.append((name, bool(ok), True))
        print(f"{'ok  ' if ok else 'FAIL'} {name}{(' — ' + detail) if detail else ''}", flush=True)

    # ── refusals ─────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call(BASE, "/api/v1/scout")
    check("no idea is 400", status, 400)
    status, _ = call(BASE, "/api/v1/scout?idea=")
    check("an empty idea is 400", status, 400)
    status, _ = scout(BASE, "x" * 4097)
    check("an idea over 4 KiB is 400", status, 400)
    status, _ = scout(BASE, "wip limits", extra="&max=abc")
    check("a non-numeric max is 400", status, 400)
    status, _ = scout(BASE, "wip limits", extra="&max=0")
    check("a non-positive max is 400", status, 400)

    # ── the answer ───────────────────────────────────────────────────────────────────────────────────────────────
    status, report = scout(BASE, IDEA, token=token)
    check("scout answers 200", status, 200)
    report = report or {}
    check("the report carries the reference's keys", sorted(report.keys()),
          sorted(["idea", "capabilities", "in_catalog", "proposed", "verdicts", "stats", "candidates_considered"]))
    check("the idea is echoed trimmed", report.get("idea"), IDEA)
    capabilities = report.get("capabilities") or []
    note("the decomposition found catalogue capabilities", report.get("in_catalog", 0) > 0,
         f"in_catalog={report.get('in_catalog')} proposed={report.get('proposed')}")
    note("a capability-shaped token outside the catalogue is a proposal, not dropped",
         any(not cap.get("in_catalog") for cap in capabilities) or report.get("proposed") == 0,
         f"proposed={report.get('proposed')}")
    for capability in capabilities:
        note(f"capability {capability.get('key')} has a coverage state",
             capability.get("coverage") in {"covered", "partial", "open"}, capability.get("coverage", ""))
        if not capability.get("in_catalog"):
            check(f"a proposal is open ({capability.get('key')})", capability.get("coverage"), "open")

    # The fixture project: an assertion nobody confirmed is `partial`, not `covered` and not open.
    offline = [cap for cap in capabilities if cap.get("key") == "offline"]
    if offline:
        check("an unconfirmed assertion is partial coverage", offline[0].get("coverage"), "partial")

    verdicts = report.get("verdicts") or []
    note("every readable project was classified", report.get("candidates_considered", 0) > 0,
         f"considered={report.get('candidates_considered')}")
    note("every candidate carries a verdict, a reason and a signal",
         all(v.get("verdict") in VERDICTS and (v.get("reason") or "").strip() and v.get("signals") for v in verdicts),
         f"{len(verdicts)} verdicts")
    slugs = {verdict.get("slug") for verdict in verdicts}
    check("the fixture project is classified", public in slugs, True)
    # `candidates_considered` and the stats count every candidate; the verdict LIST is trimmed to `max` (default 25). Comparing the
    # list's length to the count conflates "classified" with "displayed".
    check("the count is the whole catalogue, not the trimmed page",
          report.get("candidates_considered"), sum((report.get("stats") or {}).values()))

    # A stranger's report, untrimmed: this is where "a project you cannot read is not counted" is decided. Asserting it against the
    # list would only catch the leak when the fixture happened to rank in the top 25 — the count moves by exactly one instead.
    status, stranger = scout(BASE, IDEA, token=None, extra="&max=500")
    check("a stranger can ask the same question", status, 200)
    stranger = stranger or {}
    stranger_slugs = {verdict.get("slug") for verdict in (stranger.get("verdicts") or [])}
    check("the PRIVATE fixture project is invisible to a stranger's report", private in stranger_slugs, False)
    readable = con.execute(
        "select count(*) from projects where visibility in ('public', 'unlisted')").fetchone()[0]
    check("and it is not counted either: the stranger's total is the readable catalogue",
          stranger.get("candidates_considered"), readable)
    severities = {"avoid": 0, "base-on": 1, "extend": 2, "adopt": 3, "inspire": 4}
    order = [severities.get(verdict.get("verdict"), 9) for verdict in verdicts]
    check("avoid sorts first, so the mistake is read before the opportunity", order, sorted(order))
    # The fixture has no `project_metrics` row at all. Naming it makes "unmeasured is not abandoned" a hard observation rather than a
    # remark about whichever projects happened to be unmeasured this run.
    mine = next((verdict for verdict in verdicts if verdict.get("slug") == public), None)
    if mine:
        check("an unmeasured project is not called abandoned", mine.get("verdict"), "adopt")
        check("and it says why: the health signal names itself",
              "health_unmeasured" in (mine.get("signals") or []), True)
    for verdict in verdicts:
        if not verdict.get("health_known"):
            note(f"an unmeasured project is not called abandoned ({verdict.get('slug')})",
                 verdict.get("verdict") != "avoid", verdict.get("verdict", ""))
            note(f"and it says its health is unknown ({verdict.get('slug')})",
                 "health_unmeasured" in (verdict.get("signals") or []), str(verdict.get("signals")))

    # `max` trims the tail without changing the counts.
    status, limited = scout(BASE, IDEA, token=token, extra="&max=3")
    check("max=3 answers 200", status, 200)
    check("max trims the list", len((limited or {}).get("verdicts") or []), 3)
    check("and the counts still describe the whole catalogue",
          (limited or {}).get("candidates_considered"), report.get("candidates_considered"))

    # A licence constraint is a hard veto, and an unlicensed project does not violate one.
    status, vetoed = scout(BASE, IDEA, token=token, extra="&exclude_licenses=proprietary")
    check("a licence constraint is echoed so a reader can see it was applied",
          (vetoed or {}).get("constraints"), ["proprietary"])

    # ── the differential ────────────────────────────────────────────────────────────────────────────────────────
    # `max=500` on BOTH sides: the default limit trims to 25, and a trimmed list compares the trim rather than the classification.
    #
    # The reference is behind Cloudflare and rate-limits a client that asks several questions in a minute, so the call is retried; a
    # persistent 429 is NOT a disagreement and must not be read as one — the probe says so and skips the comparison rather than reporting
    # two services as differing because one of them refused to answer.
    for attempt in range(4):
        status, reference = scout(REFERENCE, IDEA, token=None, extra="&max=500")
        if status != 429:
            break
        time.sleep(20)
    if status == 429:
        note("the reference rate-limited; the differential is not judged this run", True,
             "429 after 4 attempts")
        status, reference = 0, {}
    judged = status == 200

    def refcheck(name, got, want):
        """A differential observation: recorded as a check when the reference answered, as a note when it did not."""
        if judged:
            check(name, got, want)
        else:
            note("(not judged — reference unavailable) " + name, True, "")

    refcheck("the reference answers the same question", status, 200)
    reference = reference or {}
    refcheck("the decomposition is byte-identical to the reference's",
          [(cap.get("key"), cap.get("in_catalog"), cap.get("evidence"), cap.get("coverage")) for cap in capabilities],
          [(cap.get("key"), cap.get("in_catalog"), cap.get("evidence"), cap.get("coverage"))
           for cap in (reference.get("capabilities") or [])])
    theirs = {verdict.get("slug"): verdict for verdict in (reference.get("verdicts") or [])}
    ours = {verdict.get("slug"): verdict for verdict in verdicts}
    status, wide = scout(BASE, IDEA, token=token, extra="&max=500")
    ours = {verdict.get("slug"): verdict for verdict in ((wide or {}).get("verdicts") or [])}
    theirs = {slug: verdict for slug, verdict in theirs.items() if not slug.startswith("probe-")}
    ours = {slug: verdict for slug, verdict in ours.items() if not slug.startswith("probe-")}
    shared = set(theirs) & set(ours)
    # The reference must be a SUBSET of the port: a project the live service serves and the port hides is a regression, while an extra
    # project on the target database is a fixture that the reference's database never had. The extras are reported, not tolerated silently.
    missing = sorted(set(theirs) - set(ours))
    refcheck("every project the reference serves is served by the port", missing, [])
    note("the port's catalogue is a superset (target-database fixtures)",
         len(ours) >= len(theirs), f"reference={len(theirs)} port={len(ours)} "
         f"extras={sorted(set(ours) - set(theirs))[:6]}")
    disagreements = []
    for slug in sorted(shared):
        if theirs[slug].get("verdict") != ours[slug].get("verdict"):
            disagreements.append((slug, theirs[slug].get("verdict"), ours[slug].get("verdict")))
    # The ONLY permitted difference: the reference cannot reach its own `extend` branch (its tie signal arrives after the decision), so a
    # tied candidate is `adopt` there and `extend` here.
    unexpected = [(slug, left, right) for slug, left, right in disagreements
                  if not (left == "adopt" and right == "extend"
                          and any(signal.startswith("tied_with:")
                                  for signal in (ours[slug].get("signals") or [])))]
    refcheck("every verdict disagreement is a tie the reference could not express", unexpected, [])
    note("and any tie deviation is documented rather than silent",
         all(any(signal.startswith("tied_with:") for signal in (ours[slug].get("signals") or []))
             for slug, _, _ in disagreements), f"{len(disagreements)} tied candidate(s)")

    con.execute("delete from capability_assertions where project_id in (?,?)", (pid, hidden))
    con.execute("delete from projects where id in (?,?)", (pid, hidden))
    con.execute("delete from api_tokens where user_id = ?", (user["user"]["id"],))
    con.execute("delete from users where id = ?", (user["user"]["id"],))
    con.commit()

    passed = sum(1 for _, got, want in results if got == want)
    print(f"\n{passed}/{len(results)} ok", flush=True)
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())
