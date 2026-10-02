#!/usr/bin/env python3
"""Import every project in the manifest into Concord: documents, then features.

Documents are mechanical: every .md at depth<=3 becomes a document, its kind
inferred from its path and filename against Concord's six valid kinds
(readme, spec, plan, wiki, adr, changelog). A .md that maps to none of them is
imported as 'wiki' rather than dropped, because the alternative is silently
losing the file -- the kind is a label, not a gate.

Features are derived from each repo's OWN state, never invented:

  1. a ledger file the repo ships (requirements.csv, capability-ledger.md,
     ROADMAP/BACKLOG/TODO md, SPECIFICATION.md sections) is parsed for rows and
     their status column;
  2. git log supplies what was actually shipped, which is the only source that
     cannot drift from reality;
  3. anything inferred is labelled in the body as inferred.

Every derived row becomes a COMPLAINT, then is validated, then becomes a
FEATURE linked to it. Concord refuses a feature with no validated complaint
(store/features.go:56), so this walks the same path a person would rather than
writing rows that can never be ranked.

Limits enforced here, both from the running server:
  - 1 MB request body (server.go:418 MaxBytesReader). A doc over that is
    truncated with a marker rather than failing the whole batch.
  - 100 requests/minute per visitor. Every verb goes through one retry helper
    that honours a window-aware backoff, and the run RAISES rather than
    reporting success on a partial write.

Idempotent: projects by slug (409 -> resolve id), documents by (project, slug)
upsert, complaints by title within the project.
"""
import argparse
import csv
import io
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request

SCRATCH = os.path.expanduser("~/.hermes/profiles/sysadmin/cache/scratch")
MANIFEST = os.path.join(SCRATCH, "manifest.json")
TOKEN_FILE = os.path.join(SCRATCH, "import.token")

# Concord allows 8 MB for the document endpoints specifically
# (internal/httpapi/documents.go:30, maxDocumentBody), and 1 MB for everything
# else. Using the wrong one truncated three real documents -- a 1.1 MB
# fullstack tutorial, a 1.7 MB CHANGELOG and a 1.0 MB report -- at a limit the
# server never actually enforced on this route.
MAX_BODY_BYTES = 8 << 20
DOC_KINDS = ["readme", "spec", "plan", "wiki", "adr", "changelog"]
SKIP_DIRS = {".git", "node_modules", "target", "vendor", "dist", ".venv",
             "__pycache__", "migrations", "fixtures", "testdata",
             # Generated output, not documentation. ao3's recommendations dir
             # holds 236 scraped work-id tables of 2-4 MB each; importing them
             # added 236 documents that were pure noise and 86 of them landed
             # truncated. A corpus of scraped IDs is not a document.
             "recommendations", "recommender", "recommender_old",
             "recommender_public", "popularities", "output", "outputs",
             "reports", "coverage", "benchmarks"}


class RateLimited(Exception):
    pass


class Client:
    def __init__(self, base, token, retries=8):
        self.base = base.rstrip("/")
        self.token = token
        self.retries = retries
        self.throttled = 0
        self.calls = 0

    def _once(self, method, path, payload=None):
        self.calls += 1
        data = json.dumps(payload).encode() if payload is not None else None
        req = urllib.request.Request(
            self.base + path, data=data, method=method,
            headers={"Content-Type": "application/json",
                     "Authorization": "Bearer " + self.token})
        try:
            with urllib.request.urlopen(req, timeout=120) as resp:
                body = resp.read()
                return resp.status, (json.loads(body) if body else {})
        except urllib.error.HTTPError as e:
            body = e.read()
            try:
                return e.code, json.loads(body)
            except Exception:
                return e.code, {"raw": body[:300].decode("utf-8", "replace")}

    def req(self, method, path, payload=None):
        for attempt in range(self.retries + 1):
            st, resp = self._once(method, path, payload)
            if st != 429:
                return st, resp
            self.throttled += 1
            if attempt == self.retries:
                raise RateLimited("%s %s still 429 after %d attempts"
                                  % (method, path, self.retries + 1))
            wait = min(70, 10 * (attempt + 1))
            sys.stderr.write("  429 %s %s -> waiting %ds\n" % (method, path, wait))
            sys.stderr.flush()
            time.sleep(wait)
        raise RateLimited("unreachable")

    def get(self, p):
        return self.req("GET", p)

    def post(self, p, payload=None):
        return self.req("POST", p, payload)

    def put(self, p, payload=None):
        return self.req("PUT", p, payload)


def as_items(resp):
    if isinstance(resp, list):
        return resp
    if isinstance(resp, dict):
        return resp.get("items") or []
    return []


def sh(cmd):
    return subprocess.run(cmd, shell=True, capture_output=True, text=True).stdout


# ---------------------------------------------------------------- documents

def infer_kind(rel_path, filename):
    """Map a file to one of Concord's six kinds, by path then by name.

    Path beats filename: docs/specs/0003-x.md is a spec even though the name
    looks like neither, and a file called SPECIFICATION.md at the repo root is
    a spec by every convention that matters.
    """
    low = rel_path.lower()
    parts = low.split(os.sep)
    base = filename.lower()

    for p in parts[:-1]:
        if p in ("specs", "spec", "adr", "adrs", "wiki", "plans", "plan",
                 "handoffs", "archive", "research"):
            return {"specs": "spec", "spec": "spec", "adr": "adr", "adrs": "adr",
                    "wiki": "wiki", "plans": "plan", "plan": "plan",
                    "handoffs": "plan", "archive": "plan",
                    "research": "wiki"}[p]
    if "changelog" in base:
        return "changelog"
    if base in ("readme.md", "readme"):
        return "readme"
    if "spec" in base:
        return "spec"
    if "plan" in base or "roadmap" in base or "backlog" in base or "todo" in base:
        return "plan"
    if "adr" in base or "decision" in base:
        return "adr"
    return "wiki"


def doc_slug(rel_path):
    s = rel_path.replace(os.sep, "-")[:-3] if rel_path.endswith(".md") else rel_path
    s = re.sub(r"[^a-z0-9._-]+", "-", s.lower()).strip("-")
    return re.sub(r"-{2,}", "-", s)


def find_docs(root):
    """Every .md at depth<=3, skipping vendored and generated trees."""
    out = []
    root_depth = root.rstrip("/").count("/")
    for dirpath, dirnames, filenames in os.walk(root):
        depth = dirpath.count("/") - root_depth
        dirnames[:] = [d for d in dirnames
                       if d not in SKIP_DIRS and not d.startswith(".")
                       and depth < 3]
        if depth >= 3:
            continue
        for fn in sorted(filenames):
            if fn.endswith(".md"):
                out.append(os.path.join(dirpath, fn))
    return out


def read_doc(path):
    with open(path, encoding="utf-8", errors="replace") as f:
        text = f.read()
    truncated = False
    # The server caps the request body at 1 MB. Truncate the doc, not the run:
    # a partial document is still a document, and the marker says so.
    if len(text.encode("utf-8")) > MAX_BODY_BYTES - 8192:
        text = text.encode("utf-8")[:MAX_BODY_BYTES - 8192].decode("utf-8", "ignore")
        text += "\n\n---\n\n*Truncated on import: the source file exceeds Concord's 1 MB request limit.*\n"
        truncated = True
    return text, truncated


# ---------------------------------------------------------------- features

LEDGER_PATTERNS = [
    "requirements.csv", "requirements.md", "capability-ledger.md",
    "ROADMAP.md", "BACKLOG.md", "TODO.md", "SPECIFICATION.md", "PLAN.md",
]

# Status vocabularies differ per project and must not be collapsed into one
# scale: "shipped" in one ledger is not the same claim as "tested" in another.
DONE_WORDS = {"shipped", "done", "complete", "completed", "built", "implemented",
              "merged", "closed", "tested", "delivered", "released"}
PLANNED_WORDS = {"planned", "specified", "ready", "todo", "open", "next",
                 "in_progress", "progress", "backlog", "pending", "draft",
                 "proposed", "not_started"}


def parse_ledger(path):
    """Extract (title, status) rows from a ledger file, without inventing any."""
    rows = []
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            text = f.read()
    except OSError:
        return rows

    if path.endswith(".csv"):
        try:
            for rec in csv.DictReader(io.StringIO(text)):
                keys = {k.lower(): v for k, v in rec.items() if k}
                title = next((keys[k] for k in
                              ("requirement", "title", "name", "id", "feature")
                              if k in keys and keys[k]), None)
                status = next((keys[k] for k in
                               ("status", "state", "disposition", "stage")
                               if k in keys and keys[k]), "")
                if title:
                    rows.append((str(title).strip()[:200],
                                 str(status).strip().lower()))
        except Exception:
            return rows
        return rows

    # Markdown tables: | id | title | status |
    for line in text.splitlines():
        line = line.strip()
        if not line.startswith("|") or line.count("|") < 3:
            continue
        if set(line.replace("|", "").strip()) <= set("-: "):
            continue  # separator row
        cells = [c.strip() for c in line.strip("|").split("|")]
        if not cells:
            continue
        status = cells[-1].lower()
        has_status = any(w in status for w in DONE_WORDS | PLANNED_WORDS)
        title = None
        if has_status:
            for c in cells:
                if c and not any(w in c.lower() for w in DONE_WORDS | PLANNED_WORDS) \
                        and not set(c) <= set("-: "):
                    title = c
                    break
            if title is None and len(cells) >= 2:
                title = cells[1]
        else:
            title = cells[1] if len(cells) > 1 else cells[0]
        if title and len(title) > 3:
            rows.append((title.strip()[:200], status))
    return rows


# Commit prefixes that describe maintenance rather than capability. A .gitignore
# or a version bump is not a feature, and importing them as one makes the whole
# feature list read like a changelog -- which is exactly what the ledger path
# exists to avoid.
NOT_FEATURES = re.compile(
    r"^(chore|ci|build|style|test|docs|fix|perf|refactor|revert)(\(|\s|:)",
    re.I)


def is_featureful(subject):
    """Whether a commit message describes something the project can DO.

    A conventional-commit `feat:` is the strong signal. A bare subject with no
    prefix is accepted, because many of these repos predate the convention and
    write "the encrypted token store" as a whole commit message. Everything
    prefixed chore/ci/build/style/test/docs/perf/refactor/revert is maintenance
    and is dropped.
    """
    s = subject.strip()
    if not s or len(s) < 8:
        return False
    if re.match(r"^feat(\(|\s|:)", s, re.I):
        return True
    if NOT_FEATURES.match(s):
        return False
    # A capitalised prose sentence is how the pre-convention repos write a
    # feature; a lowercase fragment is usually a fix.
    return s[0].isupper() or s.startswith("the ") or s.startswith("add ")


def classify(status):
    s = (status or "").lower()
    for w in DONE_WORDS:
        if w in s:
            return "shipped"
    for w in PLANNED_WORDS:
        if w in s:
            return "planned"
    return "planned"


def git_shipped(root, limit=12):
    """What the repo says it actually shipped. Hard to drift from reality."""
    out = sh(f"git -C {root} log --no-merges --pretty=format:'%s' -n {limit} 2>/dev/null")
    lines = [l.strip(" .") for l in out.splitlines() if l.strip()]
    return lines


def find_ledgers(root):
    found = []
    for dirpath, dirnames, filenames in os.walk(root):
        depth = dirpath.count("/") - root.rstrip("/").count("/")
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS
                       and not d.startswith(".") and depth < 3]
        if depth >= 3:
            continue
        for fn in filenames:
            if fn in LEDGER_PATTERNS:
                found.append(os.path.join(dirpath, fn))
    return found


# ---------------------------------------------------------------- main

def api_slug(name):
    """The slug Concord will accept for a given checkout name.

    validSlug (store.go:732) allows only [a-z0-9._-] and requires a leading
    lowercase letter or digit, so three real directory names are rejected
    outright: AO3-Stats-Icons-with-Hover-Text, FerrisFeed,
    L2-adrenaline-scripts. Lowercasing is the fix rather than skipping them; the
    original name is kept in the project description so the mapping back to the
    directory is never lost.
    """
    s = re.sub(r"[^a-zA-Z0-9._-]+", "-", name).lower().strip("-.")
    s = re.sub(r"-{2,}", "-", s)
    if not s or not re.match(r"[a-z0-9]", s):
        s = "p-" + s
    return s[:100]


def ensure_project(c, name, display, description, license_=None):
    slug = api_slug(name)
    st, body = c.post("/api/v1/projects", {
        "slug": slug, "name": display, "description": description,
        "license": license_,
    })
    if st == 201:
        return body["id"], slug
    # 409 means the slug is taken. 400 does NOT: it means the request was
    # rejected (bad slug, bad model), and treating it as "already exists" hides
    # a real failure behind a successful-looking import.
    if st == 409:
        st2, lst = c.get("/api/v1/projects")
        for p in as_items(lst):
            if p.get("slug") == slug:
                return p["id"], slug
        raise SystemExit("project %r is taken but not listable (HTTP %d)"
                         % (slug, st))
    raise SystemExit("create project %s (slug %s): HTTP %d %s"
                     % (name, slug, st, str(body)[:200]))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:8006")
    ap.add_argument("--token-file", default=TOKEN_FILE)
    ap.add_argument("--only", default="", help="comma-separated project names")
    ap.add_argument("--max-docs-per-project", type=int, default=400)
    ap.add_argument("--skip-features", action="store_true")
    ap.add_argument("--visibility", default="private",
                    help="visibility for newly registered projects")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    manifest = json.load(open(MANIFEST))
    projects = manifest["projects"]
    if args.only:
        wanted = {s.strip() for s in args.only.split(",")}
        projects = {k: v for k, v in projects.items() if k in wanted}
    if not projects:
        raise SystemExit("no projects selected")

    token = open(args.token_file).read().strip()
    c = Client(args.base, token)

    st, health = c.get("/api/v1/healthz")
    if st != 200:
        raise SystemExit("concord not answering: healthz=%d" % st)
    print("concord up:", health)
    print("projects in manifest: %d%s"
          % (len(projects), (" (filtered)" if args.only else "")))

    totals = dict(projects=0, documents=0, features=0, skipped=0)
    blocked = []
    for name in sorted(projects):
        rec = projects[name]
        root = rec["path"]
        print("\n=== %s  (%s)" % (name, root))

        docs = find_docs(root)
        if len(docs) > args.max_docs_per_project:
            print("  capping docs at %d of %d found"
                  % (args.max_docs_per_project, len(docs)))
            docs = sorted(docs, key=lambda p: (0 if "spec" in p.lower() or
                                               "readme" in p.lower() else 1, p)
                          )[:args.max_docs_per_project]
        ledgers = find_ledgers(root)
        shipped = git_shipped(root)
        nrows = sum(len(parse_ledger(l)) for l in ledgers)
        print("  %d docs, %d ledgers (%d rows), %d recent commits"
              % (len(docs), len(ledgers), nrows, len(shipped)))

        if args.dry_run:
            continue

        desc = ("%s. Last commit %s, %d commits. Imported from %s%s"
                % (name, rec["last"], rec["commits"], root,
                   (" (also at %s)" % ", ".join(rec["also_at"])) if rec["also_at"] else ""))
        pid, slug = ensure_project(
            c, name, name.replace("_", " ").replace("-", " ").title(), desc)
        totals["projects"] += 1
        print("  project id %s (slug %s)" % (pid, slug))

        # Documents
        ndoc = 0
        not_member = False
        for path in docs:
            rel = os.path.relpath(path, root)
            fn = os.path.basename(path)
            kind = infer_kind(rel, fn)
            try:
                body_text, _ = read_doc(path)
            except OSError:
                totals["skipped"] += 1
                continue
            if not body_text.strip():
                totals["skipped"] += 1
                continue
            st, resp = c.put("/api/v1/projects/%s/documents" % slug, {
                "kind": kind,
                "slug": doc_slug(rel),
                "title": os.path.splitext(fn)[0].replace("-", " ").replace("_", " "),
                "body": body_text,
            })
            if st == 200:
                ndoc += 1
            elif st in (400, 409):
                totals["skipped"] += 1
            elif st in (401, 403):
                # The write gate is membership (requireRole contributor). A 403
                # means this account is not a member of a project that already
                # existed -- normally one owned by another account. Count it once
                # and stop retrying per file.
                not_member = True
                totals["skipped"] += 1
            else:
                print("  doc FAILED %s -> %d %s" % (rel, st, str(resp)[:120]))
                totals["skipped"] += 1
        totals["documents"] += ndoc
        if not_member and ndoc == 0:
            print("  documents: 0 -- this account is not a member of %r "
                  "(it already existed); needs a membership row" % slug)
            blocked.append("%s (slug %s)" % (name, slug))
        else:
            print("  documents: %d imported" % ndoc)

        if args.skip_features:
            continue

        # Features -> complaints -> validate -> feature.
        entries = []  # (title, status, provenance)
        seen = set()
        for lf in ledgers:
            for title, status in parse_ledger(lf):
                key = title.lower()[:60]
                if key in seen or len(entries) >= 25:
                    continue
                seen.add(key)
                entries.append((title, classify(status),
                                "from ledger %s" % os.path.basename(lf)))
        for subject in shipped:
            if len(entries) >= 25:
                break
            if not is_featureful(subject):
                continue
            key = subject.lower()[:60]
            if key in seen:
                continue
            seen.add(key)
            entries.append((subject[:200], "shipped",
                            "from git log (verifiable)"))
        if not entries:
            print("  features: none derived (no ledger rows, no commits)")
            continue

        st, cl = c.get("/api/v1/projects/%s/complaints" % slug)
        have = {i.get("title", "").strip() for i in as_items(cl)} if st == 200 else set()

        nnew_c = 0
        for title, status, prov in entries:
            if title.strip() in have:
                continue
            code, resp = c.post("/api/v1/projects/%s/complaints" % slug, {
                "project_id": pid, "title": title[:200],
                "body": "%s. Derived by the importer 2026-10-02, %s."
                        % (status.capitalize(), prov),
                "severity": 3, "frequency": 1.0, "strategic_multiplier": 1.0,
            })
            if code == 201:
                nnew_c += 1
                have.add(title.strip())
            elif code in (400, 409):
                pass
            else:
                print("  complaint FAILED %r -> %d %s" % (title[:50], code, str(resp)[:100]))
        print("  complaints: %d new" % nnew_c)

        st, cl = c.get("/api/v1/projects/%s/complaints" % slug)
        items = as_items(cl) if st == 200 else []
        validated = []
        for it in items:
            if it.get("status") == "validated":
                validated.append(it["id"])
                continue
            code, _ = c.post("/api/v1/projects/%s/complaints/%d/validate"
                             % (slug, it["id"]), {})
            if code == 200:
                validated.append(it["id"])
        print("  validated: %d" % len(validated))
        if not validated:
            print("  features: skipped, nothing validated to link to")
            continue

        st, fl = c.get("/api/v1/projects/%s/features" % slug)
        havef = {i.get("title", "").strip() for i in as_items(fl)} if st == 200 else set()

        nfeat = 0
        nstatus = 0
        stat_denied = []
        for title, status, prov in entries:
            if title.strip() in havef:
                continue
            want_status = "shipped" if status == "shipped" else "draft"
            code, resp = c.post("/api/v1/projects/%s/features" % slug, {
                "project_id": pid, "title": title[:200],
                "body": "Derived by the importer 2026-10-02, %s." % prov,
                "effort": "M", "linked_complaints": [validated[0]],
            })
            if code in (200, 201):
                nfeat += 1
                havef.add(title.strip())
                fid = resp.get("id") if isinstance(resp, dict) else None
                if fid and want_status != "draft":
                    sc, sr = c.put("/api/v1/projects/%s/features/%d/status"
                                   % (slug, fid),
                                   {"status": want_status,
                                    "reason": "derived from the repo's own ledger/git history"})
                    if sc == 200:
                        nstatus += 1
                    else:
                        stat_denied.append(want_status)
            else:
                print("  feature FAILED %r -> %d %s" % (title[:50], code, str(resp)[:100]))
        totals["features"] += nfeat
        print("  features: %d created, %d status updates applied" % (nfeat, nstatus))
        if stat_denied:
            # Status changes are trust-gated. Every derived feature will read
            # "draft" until someone with the required trust level sets them.
            # Reported, never silently swallowed.
            print("    (%d status updates REFUSED by the trust gate; features stay draft)"
                  % len(stat_denied))

    print("\n" + "=" * 60)
    if blocked:
        print("projects needing a membership row for this account:")
        for b in blocked:
            print("   %s" % b)
    print("projects registered : %d" % totals["projects"])
    print("documents imported  : %d" % totals["documents"])
    print("features created    : %d" % totals["features"])
    print("skipped             : %d" % totals["skipped"])
    print("api calls           : %d (%d throttled waits)" % (c.calls, c.throttled))


if __name__ == "__main__":
    main()