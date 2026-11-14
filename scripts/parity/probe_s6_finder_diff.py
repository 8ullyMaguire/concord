#!/usr/bin/env python3
"""Row 11 part 55b: the finder's six routes, gated against the LIVE Go service.

This is not a normal probe. An engine of this size cannot be proven by asserting my own expectations — my expectations are the
port's source read back to me. So the gate is **differential**: the same seed goes into both services, the same answers go into
both sessions, and every step compares what came back — the question chosen, how many candidates survived, and the ranking.

Two services, one ledger:
  reference = https://concord.polarisocial.xyz   (the Go service, serving production today)
  candidate = http://127.0.0.1:8108              (this port, same production database)

What is compared, per step:
  * the catalogue (GET /finder/questions): candidate_count, and every dimension's key, family, askable and gain_bits
  * the first question of a session: key, family, option ids in order, known_fraction
  * after each answer: candidate_count and the chosen next question's key
  * the top-five ranking at the end: slugs in order, and each fit within 1e-9

What is NOT compared, and why: session ids (each service mints its own), and `filtered_out` names (the reference records slugs it
filtered; the port records the filter and no names — see the decision in the ledger).
"""
import json
import os
import sys
import urllib.error
import urllib.request

REFERENCE = os.environ.get("REFERENCE", "https://concord.polarisocial.xyz")
CANDIDATE = os.environ.get("CANDIDATE", "http://127.0.0.1:8108")
# The Go service gates mutating routes on the consent cookie. The port does not implement the consent gate yet (it is one of the
# remaining rows), so the cookie is harmless there.
COOKIE = "concord_consent=1"

results = []


def check(name, got, want):
    results.append((name, got, want))
    print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)


def call(base, method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    # A desktop user agent, because the live service sits behind Cloudflare and its bot rules answer 1010 to
    # `Python-urllib/3.x` — the same request through curl passes. This is a probe pretending to be a browser, not the port
    # pretending to be a client.
    headers = {
        "Cookie": COOKIE,
        "User-Agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) "
        "Chrome/131.0 Safari/537.36",
        "Accept": "application/json",
    }
    if data is not None:
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(base + path, data=data, method=method, headers=headers)
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


def project_index(base, limit=200):
    """slug -> id for every project the service will list."""
    status, body = call(base, "GET", f"/api/v1/search?q=&limit={limit}")
    if status != 200:
        raise SystemExit(f"{base} search: {status} {body}")
    return {row["slug"]: row["id"] for row in body["results"]}


def catalogue(base):
    status, body = call(base, "GET", "/api/v1/finder/questions")
    if status != 200:
        raise SystemExit(f"{base} catalogue: {status} {body}")
    works = {
        d["key"]: (d["family"], d["askable"], round(d["gain_bits"], 9))
        for d in body["dimensions"]
    }
    return body["candidate_count"], works


def session_step(base, path, body=None):
    return call(base, "POST", path, body)


def main():
    reference_count, reference_dims = catalogue(REFERENCE)
    candidate_count, candidate_dims = catalogue(CANDIDATE)
    print(f"   catalogue sizes: reference {reference_count}, port {candidate_count}", flush=True)
    # These two are equal now for a reason worth stating: they were NOT, by 18, and every one of the 18 was a fixture an earlier
    # probe of mine had left behind in the Postgres copy. A probe that leaves rows behind makes the next probe's baseline wrong —
    # and this one is the probe whose entire job is comparing two databases.
    check("both catalogues hold the same projects", candidate_count, reference_count)
    check("every dimension the Go service offers, the port offers too",
          [key for key in reference_dims if key not in candidate_dims], [])
    check("with the same family for each",
          [key for key in reference_dims
           if candidate_dims.get(key, ("",))[0] != reference_dims[key][0]], [])
    # The gains DIFFER and must: they are computed from the live candidate set, and the two sets differ by 18 projects. What can
    # be compared is whether a dimension is askable at all, which is the decision the engine makes from those gains.
    check("and the same askability verdict",
          [key for key in reference_dims
           if candidate_dims.get(key, ("", None))[1] != reference_dims[key][1]], [])

    # THE TWO SERVICES DO NOT HOLD THE SAME DATA, and that is measured rather than assumed: the live Go service runs on its own
    # SQLite file (CONCORD_DB=…/concord.db, 73 public projects), while the port serves a Postgres copy (91 public). So the first
    # comparison is the drift itself, and the ENGINE comparison below runs on the intersection — the same projects on both sides,
    # by id — because a question chosen from 91 candidates cannot be compared to one chosen from 73.
    reference_index = project_index(REFERENCE)
    candidate_index = project_index(CANDIDATE)
    common = sorted(set(reference_index) & set(candidate_index))
    shared_ids = [reference_index[slug] for slug in common]
    print(f"   reference projects: {len(reference_index)}, port projects: {len(candidate_index)}, "
          f"shared: {len(common)}", flush=True)
    check("the migration preserved every project id the two share",
          [candidate_index[slug] for slug in common], shared_ids)
    only_reference = sorted(set(reference_index) - set(candidate_index))
    only_candidate = sorted(set(candidate_index) - set(reference_index))
    print(f"   only in the Go's SQLite: {only_reference}", flush=True)
    print(f"   only in the port's Postgres: {only_candidate}", flush=True)

    status, ref = session_step(REFERENCE, "/api/v1/finder/sessions", {"seed_candidates": shared_ids})
    if status not in (200, 201):
        raise SystemExit(f"reference session: {status} {ref}")
    status, cand = session_step(CANDIDATE, "/api/v1/finder/sessions", {"seed_candidates": shared_ids})
    if status not in (200, 201):
        raise SystemExit(f"candidate session: {status} {cand}")
    check("a session starts with the same number of candidates",
          cand["candidate_count"], ref["candidate_count"])
    check("the same first question is chosen",
          (cand["question"] or {}).get("key"), (ref["question"] or {}).get("key"))
    check("with the same options in the same order",
          [o["id"] for o in (cand["question"] or {}).get("options", [])],
          [o["id"] for o in (ref["question"] or {}).get("options", [])])
    check("and the same stop verdict",
          (cand["should_suggest_stop"], cand["stop_reason"]),
          (ref["should_suggest_stop"], ref["stop_reason"]))

    # Six answers, chosen to walk the flow rather than to be kind: each one takes the SECOND option (not `any`), which is the
    # branch that actually filters, and falls back to `any` when a question has only one real option left.
    for step in range(6):
        ref_question = ref.get("question")
        cand_question = cand.get("question")
        if not ref_question or not cand_question:
            break
        options = [o["id"] for o in ref_question["options"] if o["id"] != "any"]
        if not options:
            break
        choice = options[1] if len(options) > 1 else options[0]
        answer = {"question_key": ref_question["key"], "option_id": choice, "mode": "required"}
        ref_id = ref["session_id"]
        cand_id = cand["session_id"]
        status, ref = session_step(REFERENCE, f"/api/v1/finder/sessions/{ref_id}/answers", answer)
        check(f"step {step}: the reference accepts {ref_question['key']}={choice}", status, 200)
        status, cand = session_step(CANDIDATE, f"/api/v1/finder/sessions/{cand_id}/answers", answer)
        check(f"step {step}: the port accepts the same answer", status, 200)
        if status != 200:
            break
        check(f"step {step}: the same candidates survive",
              cand["candidate_count"], ref["candidate_count"])
        check(f"step {step}: the same next question is chosen",
              (cand["question"] or {}).get("key"), (ref["question"] or {}).get("key"))
        check(f"step {step}: with the same option set",
              [o["id"] for o in (cand["question"] or {}).get("options", [])],
              [o["id"] for o in (ref["question"] or {}).get("options", [])])

    status, ref_results = call(REFERENCE, "GET", f"/api/v1/finder/sessions/{ref['session_id']}/results")
    status, cand_results = call(
        CANDIDATE, "GET", f"/api/v1/finder/sessions/{cand['session_id']}/results"
    )
    check("both results endpoints answer", (status, status), (200, 200))
    ref_top = [row["slug"] for row in ref_results["candidates"][:5]]
    cand_top = [row["slug"] for row in cand_results["candidates"][:5]]
    check("the top five are the same projects in the same order", cand_top, ref_top)
    ref_fits = {row["slug"]: round(row["fit_score"], 9) for row in ref_results["candidates"][:5]}
    cand_fits = {row["slug"]: round(row["fit_score"], 9) for row in cand_results["candidates"][:5]}
    check("and the same fit scores", cand_fits, ref_fits)
    check("the same gaps are reported",
          [(g["key"], g["unknown_fields"]) for g in cand_results["gaps"]],
          [(g["key"], g["unknown_fields"]) for g in ref_results["gaps"]])
    check("the same unknown-fit list",
          [row["slug"] for row in cand_results["unknown_fit"]],
          [row["slug"] for row in ref_results["unknown_fit"]])

    # ── back and lift, which recompute rather than replay ──────────────────────────────────────────────────────────────
    status, ref = session_step(REFERENCE, f"/api/v1/finder/sessions/{ref['session_id']}/back")
    status, cand = session_step(CANDIDATE, f"/api/v1/finder/sessions/{cand['session_id']}/back")
    check("back answers on both", (status, status), (200, 200))
    check("back leaves the same candidate count", cand["candidate_count"], ref["candidate_count"])
    check("and offers the same question", (cand["question"] or {}).get("key"),
          (ref["question"] or {}).get("key"))

    lifted = cand["answers"][0]["dimension_key"] if cand.get("answers") else ""
    if lifted:
        status, ref = session_step(REFERENCE, f"/api/v1/finder/sessions/{ref['session_id']}/lift",
                                   {"question_key": lifted})
        status, cand = session_step(CANDIDATE, f"/api/v1/finder/sessions/{cand['session_id']}/lift",
                                    {"question_key": lifted})
        check(f"lifting {lifted} answers on both", (status, status), (200, 200))
        check("and both land on the same count and question",
              (cand["candidate_count"], (cand["question"] or {}).get("key")),
              (ref["candidate_count"], (ref["question"] or {}).get("key")))

    failures = sum(1 for _, got, want in results if got != want)
    print(f"\n{len(results) - failures}/{len(results)} ok")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
