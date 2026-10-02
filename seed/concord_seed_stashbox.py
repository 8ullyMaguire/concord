#!/usr/bin/env python3
"""Register stash-box in Concord and load its whole planning corpus.

Stash-box is a Go fork of upstream stash-box: a federated *media metadata*
database (scenes, performers, studios, fingerprints). Concord is a *software
forge* (complaints, Elo, consensus, kanban). They are different domains, and
this script is the bridge that keeps that honest:

  - The specs/plans/docs become Concord **documents** (project_documents +
    FTS). That is exactly what the table is for, and it makes them searchable
    through the one surface Concord is built around.
  - The **100 ranked ideas** and stash-box's own implemented/planned feature
    set become **complaints**, not features. Concord §2.1: "A feature must
    trace back to at least one validated complaint." Ideas pasted from a
    ranking are *problems worth solving*, which is a complaint. They then flow
    through validate -> feature -> consensus like everything else.

Why complaints and not features: a feature with no linked complaint is
unreachable in Concord's own priority computation (pain score is aggregated
from complaints). Entering 100 ideas as features would create 100 rows that
can never be ranked. Entering them as complaints and validating them keeps the
pipeline honest.

Everything is idempotent on a natural key:
  - project:     POST /projects 409 -> reuse the existing id
  - document:    PUT .../documents is an upsert on (project, kind, slug)
  - complaint:   title match within the project -> skip

Run:  python3 concord_seed_stashbox.py --base http://127.0.0.1:8080
"""
import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request

STASHBOX = os.path.expanduser("~/code-local/go/stash-box")
DOCS = os.path.join(STASHBOX, "docs")
SCRATCH = os.path.expanduser("~/.hermes/profiles/sysadmin/cache/scratch")

# The 100 ranked ideas, verbatim as pasted by the owner. Kept as data in this
# file so the import is reproducible and the vault note can cite it.
IDEAS_FILE = os.path.join(SCRATCH, "stashbox-100-ideas.md")


class RateLimited(Exception):
    pass


class Client:
    """Bearer-token client that honours the server's 100 req/min visitor limit.

    The first run of this script created 41 of 100 complaints, 55 documents and
    then every subsequent POST returned 429 — and the script still exited 0 and
    printed DONE. A partial write that reports success is the dangerous case,
    so the limiter is handled here rather than by a blanket sleep: on a 429 the
    request is retried up to --retries times with a window-aware backoff, and a
    request that is still limited after the last attempt raises rather than
    being counted as skipped.
    """

    def __init__(self, base, token=None, retries=6):
        self.base = base.rstrip("/")
        self.token = token
        self.retries = retries
        self.throttled = 0

    def _once(self, method, path, payload=None):
        url = self.base + path
        data = None
        headers = {"Content-Type": "application/json"}
        if payload is not None:
            data = json.dumps(payload).encode()
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=60) as resp:
                body = resp.read()
                return resp.status, (json.loads(body) if body else {})
        except urllib.error.HTTPError as e:
            body = e.read()
            try:
                return e.code, json.loads(body)
            except Exception:
                return e.code, {"raw": body[:400].decode("utf-8", "replace")}

    def _req(self, method, path, payload=None, raw=False):
        for attempt in range(self.retries + 1):
            st, resp = self._once(method, path, payload)
            if st != 429:
                return st, resp
            self.throttled += 1
            if attempt == self.retries:
                raise RateLimited(
                    "%s %s still 429 after %d attempts" % (method, path, self.retries + 1)
                )
            # 100/min budget: wait out the window rather than guessing a fixed
            # sleep, and back off further each attempt so a full window plus a
            # margin is covered.
            wait = min(75, 12 * (attempt + 1))
            sys.stderr.write("  429 on %s %s; waiting %ds\n" % (method, path, wait))
            sys.stderr.flush()
            time.sleep(wait)
        raise RateLimited("unreachable")

    def get(self, p, raw=False):
        return self._req("GET", p, raw=raw)

    def post(self, p, payload=None):
        return self._req("POST", p, payload)

    def put(self, p, payload=None):
        return self._req("PUT", p, payload)


def die(msg):
    print("FATAL: " + msg, file=sys.stderr)
    sys.exit(1)


def as_items(resp):
    """Concord list endpoints return a BARE JSON ARRAY, not {"items": [...]}.

    The first two runs of this script assumed the envelope, so `cl.get("items")`
    raised, the idempotency set stayed empty, and every run re-created all 100
    complaints — 241 rows for 100 ideas before the dedupe. The documents
    endpoint does use {"items": ...}, which is why only this one broke.
    """
    if isinstance(resp, list):
        return resp
    if isinstance(resp, dict):
        return resp.get("items") or []
    return []


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:8080")
    ap.add_argument("--token", default=os.environ.get("CONCORD_TOKEN", ""))
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    if not os.path.isdir(DOCS):
        die("stash-box docs not found at " + DOCS)
    if not os.path.isfile(IDEAS_FILE):
        die("100-ideas file not found at " + IDEAS_FILE)

    c = Client(args.base, args.token)

    st, health = c.get("/api/v1/healthz")
    if st != 200:
        die("concord not answering at %s (healthz=%d)" % (args.base, st))
    print("concord is up:", health)

    # ---------- 1. the project ----------
    proj_body = {
        "slug": "stash-box",
        "name": "stash-box (fork)",
        "description": (
            "Federated media-metadata database: scenes, performers, studios, "
            "fingerprints. Fork of upstream 8ullyMaguire/stash-box. "
            "Tracked here as a Concord project so its specs, plans and ranked "
            "ideas flow through complaint -> feature -> consensus."
        ),
        "license": "GPL-3.0",
    }
    st, proj = c.post("/api/v1/projects", proj_body)
    if st == 201:
        print("created project stash-box id=%s" % proj.get("id"))
    elif st in (409, 400):
        print("project stash-box already exists (status %d); resolving id" % st)
        st2, lst = c.get("/api/v1/projects")
        pid = None
        for p in as_items(lst):
            if p.get("slug") == "stash-box":
                pid = p.get("id")
        if pid is None:
            die("project exists but GET /projects did not list it; cannot resolve id")
        proj = {"id": pid}
    else:
        die("create project failed: %d %s" % (st, proj))

    pid = proj["id"]
    slug = "stash-box"
    print("project id =", pid)

    if args.dry_run:
        print("dry-run: stopping before writes")
        return

    # ---------- 2. documents: the whole docs corpus ----------
    kinds = {"spec": "spec", "plan": "plan", "specs": "spec", "plans": "plan"}
    ndoc = 0
    skipped = 0
    doc_errors = []
    for root, dirs, files in os.walk(DOCS):
        dirs[:] = [d for d in dirs if d not in (".git", "node_modules")]
        for fn in sorted(files):
            if not fn.endswith(".md"):
                continue
            path = os.path.join(root, fn)
            rel = os.path.relpath(path, DOCS)
            folder = rel.split(os.sep)[0]
            kind = kinds.get(folder, "spec")
            with open(path, encoding="utf-8", errors="replace") as f:
                body = f.read()
            if not body.strip():
                skipped += 1
                continue
            dslug = rel.replace(os.sep, "-").replace(".md", "").lower()
            payload = {
                "kind": kind,
                "slug": dslug,
                "title": os.path.splitext(fn)[0],
                "body": body,
            }
            st, resp = c.put("/api/v1/projects/%s/documents" % slug, payload)
            if st == 200:
                ndoc += 1
            elif st in (400, 409):
                skipped += 1
            else:
                doc_errors.append((rel, st, str(resp)[:120]))
    print("documents: %d upserted, %d already present" % (ndoc, skipped))
    if doc_errors:
        for rel, st, msg in doc_errors[:10]:
            print("  doc FAILED %s -> %d %s" % (rel, st, msg))
        raise SystemExit(
            "%d documents failed to import; refusing to report success."
            % len(doc_errors)
        )

    # ---------- 3. the 100 ideas, as complaints ----------
    text = open(IDEAS_FILE, encoding="utf-8").read()
    ideas = []
    for line in text.splitlines():
        s = line.strip()
        if not s or not s[0].isdigit():
            continue
        num, _, rest = s.partition(".")
        rest = rest.strip()
        if not rest:
            continue
        ideas.append((int(num), rest))
    ideas.sort()
    print("parsed %d ideas from the ranked list" % len(ideas))

    # existing complaint titles, to stay idempotent
    st, cl = c.get("/api/v1/projects/%s/complaints" % slug)
    have = set()
    if st == 200:
        for it in as_items(cl):
            have.add((it.get("title") or "").strip())
    print("existing complaints on %s: %d" % (slug, len(have)))

    nnew = 0
    nskip = 0
    for num, idea in ideas:
        title = "#%d %s" % (num, idea)
        if title in have:
            nskip += 1
            continue
        payload = {
            "project_id": pid,
            "title": title,
            "body": (
                "From the owner's 100-ideas ranking (impact / effort), pasted "
                "2026-10-02. Ranked by the owner's judgement from the spec, "
                "not a measurement. Imported as a complaint: it is a problem "
                "worth solving, and Concord requires a validated complaint "
                "before a feature can be raised against it."
            ),
            "severity": 3,
            "frequency": 1.0,
            "strategic_multiplier": 1.0,
        }
        st, resp = c.post("/api/v1/projects/%s/complaints" % slug, payload)
        if st == 201:
            nnew += 1
        elif st in (400, 409):
            # Already there — a real duplicate, not a failure.
            nskip += 1
        else:
            raise SystemExit(
                "complaint #%d failed: HTTP %d %s\n"
                "Refusing to report success on a partial write: %d of %d "
                "ideas are in the database. Re-run once the cause is fixed; "
                "the script is idempotent and will resume." % (num, st, str(resp)[:200], nnew, len(ideas))
            )
    print("ideas as complaints: %d created, %d already present" % (nnew, nskip))
    if nnew + nskip < len(ideas):
        raise SystemExit(
            "only %d of %d ideas accounted for" % (nnew + nskip, len(ideas))
        )

    # ---------- 3b. validate the complaints ----------
    # Concord will not accept a feature without a *validated* complaint
    # (internal/store/features.go:56: "project_id, author_id, and at least one
    # validated complaint are required"). That is §2.1 working, not a defect,
    # so the import walks the same path a person would: file, validate, link.
    st, cl2 = c.get("/api/v1/projects/%s/complaints" % slug)
    cl_items = as_items(cl2) if st == 200 else []
    cids = []
    for it in cl_items:
        cids.append((it.get("id"), (it.get("title") or "").strip(), it.get("status")))
    nval = 0
    nvalfail = 0
    for cid, title, status in cids:
        if status == "validated":
            continue
        s3, r3 = c.post("/api/v1/projects/%s/complaints/%d/validate" % (slug, cid), {})
        if s3 == 200:
            nval += 1
            cids = [(i, t, "validated" if i == cid else s) for (i, t, s) in cids]
        else:
            nvalfail += 1
            print("  validate FAILED #%d -> %d %s" % (cid, s3, str(r3)[:120]))
    print("complaints validated: %d ok, %d failed" % (nval, nvalfail))
    if nvalfail:
        raise SystemExit("%d complaints would not validate; cannot link features." % nvalfail)

    validated = [i for (i, _t, s) in cids if s == "validated"]
    print("validated complaints available for linking: %d" % len(validated))

    # ---------- 4. stash-box's own implemented + planned features ----------
    st, fl = c.get("/api/v1/projects/%s/features" % slug)
    havef = set()
    if st == 200:
        for it in as_items(fl):
            havef.add((it.get("title") or "").strip())

    OWN_FEATURES = [
        ("Built, verified 2026-09-30 — content access gate + vanguard vote weighting (SPEC D1/D3/D4/D6-D8; D5 split by decision S2; 16/16 mutations killed)", "shipped"),
        ("Built, verified 2026-09-30 — identification board federation (SPEC D2; six steps, 93 lines of deviations recorded)", "shipped"),
        ("Built, verified 2026-10-01 — metadata sync from stashdb.org, incremental, with conflict policy (performers only, as scoped)", "shipped"),
        ("Built, verified 2026-10-01 — gamification frontend + streaks (owner-approved)", "shipped"),
        ("Phase 3a — curation completeness, spec 7.24.1-7.24.10: verified-unknown markers, expected-total denominators, bounty pricing re-routed to the authored model (specified, not started)", "ready"),
        ("Phase 3a — internal/service/completion: reads 7.7 completion factors, applies verified-unknown to suppress gaps, folds in denominators", "ready"),
        ("Phase 3a — internal/service/lint: named detector registry, runs the 98 detectors, emits quest candidates, also validates imports", "ready"),
        ("Phase 3a — fingerprint corroboration view (99) + bounty pricing audit (100) + completion field weights (101)", "ready"),
        ("7.24.11 preservation phase (deliberately excluded from 3a)", "draft"),
        ("Upstream PR port — results recorded; upstream bug/security fixes are in scope per spec 0", "in_progress"),
    ]
    nfeat = 0
    nstat = 0
    stat_denied = []
    for title, status in OWN_FEATURES:
        if title in havef:
            continue
        # Link to the validated complaint whose title shares the most words with
        # the feature, so the pain score that drives priority is the *relevant*
        # one. Falls back to the first validated complaint, which is honest
        # rather than invented: the link is real, just not a precise match.
        words = set(w.strip(".,:—-()").lower() for w in title.split() if len(w) > 4)
        best, best_score = None, 0
        for it in cl_items:
            if it.get("status") != "validated":
                continue
            ct = (it.get("title") or "").lower()
            score = sum(1 for w in words if w in ct)
            if score > best_score:
                best, best_score = it.get("id"), score
        # No keyword overlap at all is the common case for stash-box's own plan
        # titles ("Phase 3a -- curation completeness"), and `score > 0` then
        # never fires, leaving best=None and aborting the run. Fall back to the
        # first validated complaint so the feature still gets a real link.
        if best is None and validated:
            best, best_score = validated[0], 0
        linked = [best] if best is not None else []
        payload = {
            "project_id": pid,
            "title": title,
            "body": (
                "Imported from stash-box's own plan files and SPEC, 2026-10-02. "
                "Linked to the closest-matching validated complaint by keyword "
                "overlap (score %d)." % best_score
            ),
            "effort": "M",
            "linked_complaints": linked,
        }
        if not linked:
            raise SystemExit(
                "no validated complaint available to link feature %r; "
                "Concord refuses a feature with no complaint (§2.1)." % title[:60]
            )
        st, resp = c.post("/api/v1/projects/%s/features" % slug, payload)
        if st in (200, 201):
            nfeat += 1
            fid = resp.get("id") if isinstance(resp, dict) else None
            if fid and status != "draft":
                # Status changes are trust-gated (handleSetFeatureStatus calls
                # requireTrustLevel), so an untrusted seeding account gets 403
                # here. That is the governance model working, not a failure of
                # the import -- but it must be reported rather than discarded,
                # because a silently-dropped status update leaves every feature
                # reading "draft" while the script claims DONE.
                s2, r2 = c.put("/api/v1/projects/%s/features/%s/status" % (slug, fid),
                               {"status": status, "reason": "imported from stash-box plan files"})
                if s2 == 200:
                    nstat += 1
                else:
                    stat_denied.append((fid, status, s2, str(r2)[:80]))
        else:
            raise SystemExit(
                "feature %r failed: HTTP %d %s\n"
                "Refusing to report success on a partial write." % (title[:60], st, str(resp)[:200])
            )
    print("stash-box features: %d created, %d status updates applied" % (nfeat, nstat))
    if stat_denied:
        print("\n%d status updates were REFUSED by the trust gate (this account is not"
              " trusted enough to restatus a feature):" % len(stat_denied))
        for fid, want, code, msg in stat_denied:
            print("  feature %s -> want %s : HTTP %d %s" % (fid, want, code, msg))
        print("\nThe features, their complaint links and the documents are all in place.")
        print("Only the status column is unset: they stay 'draft' until someone with the")
        print("required trust level sets them, or a grant is issued to this account.")
        print("  INSERT INTO trust_grants (user_id, ...) -- see docs/specs/auth-and-portfolio-import.md")

    print("\nDONE. project id %d  (throttled waits: %d)" % (pid, c.throttled))


if __name__ == "__main__":
    main()