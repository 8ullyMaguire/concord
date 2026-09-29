#!/usr/bin/env python3
"""Seed Concord with the PolarSoci portfolio.

Walks the real domain chain for every project, in order:

    register/login -> create project -> file complaint -> validate complaint
    -> create feature linked to that complaint

Nothing is inserted by SQL. Each step goes through the HTTP API, so the seed
can only succeed if the API works — which is the point. An importer that wrote
rows directly would keep working after the endpoints broke, and this data is
the first thing a real user will look at.

Re-running is safe: projects and complaints are matched by slug and title, so a
second run updates rather than duplicates.
"""

import argparse
import json
import sys
import urllib.error
import time
import urllib.request
from typing import Any

DEFAULT_BASE = "http://127.0.0.1:8006/api/v1"


def as_items(payload) -> list:
    """List endpoints return a bare JSON array; tolerate both shapes."""
    if isinstance(payload, list):
        return payload
    if isinstance(payload, dict):
        for key in ("items", "complaints", "features", "projects"):
            if isinstance(payload.get(key), list):
                return payload[key]
    return []


class Client:
    """Minimal JSON client. One instance per run; holds the bearer token."""

    def __init__(self, base: str, token: str | None = None):
        self.base = base.rstrip("/")
        self.token = token

    def request(self, method: str, path: str, body=None) -> tuple[int, Any]:
        url = self.base + path
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(url, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        try:
            with urllib.request.urlopen(req, timeout=15) as res:
                raw = res.read()
                return res.status, (json.loads(raw) if raw else {})
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                return e.code, json.loads(raw)
            except json.JSONDecodeError:
                return e.code, {"error": raw.decode(errors="replace")[:200]}
            
        except urllib.error.URLError as e:
            raise SystemExit(f"cannot reach {url}: {e.reason}")

    def get(self, path):
        return self.request("GET", path)

    def post(self, path, body=None):
        return self.request("POST", path, body or {})

    def put(self, path, body=None):
        return self.request("PUT", path, body or {})

    def post_retrying(self, path, body=None, attempts=8):
        """POST, backing off when the rate limiter refuses.

        The limiter allows 100 requests a minute per client IP, and a full
        seed is roughly 60 requests. Running the seeder twice in a minute
        crosses that, so a 429 is expected rather than exceptional — it should
        be waited out, not reported as a failure.
        """
        for i in range(attempts):
            status, body_out = self.post(path, body)
            if status != 429:
                return status, body_out
            wait = 5.0 * (i + 1)
            print(f"  rate limited, waiting {wait:.0f}s before retrying...")
            time.sleep(wait)
        return 429, {"error": "rate limited after retries"}


def authenticate(base: str, username: str, password: str) -> Client:
    """Log in, creating the account if it does not exist yet."""
    c = Client(base)
    status, body = c.post("/auth/login", {"username": username, "password": password})
    if status == 200:
        return Client(base, str(body["token"]))

    status, body = c.post(
        "/auth/register",
        {"username": username, "password": password, "display_name": username},
    )
    if status != 201:
        raise SystemExit(f"could not authenticate as {username}: {status} {body}")
    return Client(base, body["token"])


def ensure_project(c: Client, p: dict) -> int:
    status, body = c.post_retrying(
        "/projects",
        {
            "slug": p["slug"],
            "name": p["name"],
            "description": p.get("description", ""),
            "governance_model": p.get("governance_model", "collective"),
            "license": p.get("license", ""),
        },
    )
    if status == 201:
        return int(body["id"])
    if status == 409:
        # Already seeded. Look up the id so complaints attach to the right project.
        status, body = c.get(f"/projects/{p['slug']}")
        if status != 200:
            raise SystemExit(f"project {p['slug']} exists but cannot be read: {body}")
        return body["id"]
    raise SystemExit(f"create project {p['slug']}: {status} {body}")


def ensure_complaint(c: Client, project_id: int, slug: str, comp: dict) -> int:
    """File a complaint and validate it, returning its id.

    A feature may only be linked to a *validated* complaint, so validation is
    not optional bookkeeping here: skip it and every feature for this project
    is rejected.
    """
    # Look up by title first. CreateComplaint has no unique constraint on
    # title, so it never returns 409: a re-run silently inserted a duplicate
    # complaint, and because a feature's pain is the sum over its linked
    # complaints, every duplicated complaint inflated the pain of every feature
    # linked to it. Re-seeding was not idempotent and quietly corrupted the
    # ranking it was meant to populate.
    lstatus, lbody = c.get(f"/projects/{slug}/complaints")
    if lstatus == 200:
        existing = as_items(lbody)
        match = next((x for x in existing if x.get("title") == comp["title"]), None)
        if match is not None:
            cid = int(match["id"])
            vstatus, vbody = c.post_retrying(f"/projects/{slug}/complaints/{cid}/validate")
            if vstatus not in (200, 400):
                raise SystemExit(f"validate complaint {cid}: {vstatus} {vbody}")
            return cid

    status, body = c.post_retrying(
        f"/projects/{slug}/complaints",
        {
            "project_id": project_id,
            "title": comp["title"],
            "body": comp.get("body", ""),
            "severity": comp.get("severity", 3),
            "frequency": comp.get("frequency", 0.5),
        },
    )
    if status == 201:
        cid = int(body["id"])
    elif status == 409:
        # Already present. Find it by title so a re-run is idempotent.
        status, body = c.get(f"/projects/{slug}/complaints")
        if status != 200:
            raise SystemExit(f"complaints unreadable for {slug}: {body}")
        items = as_items(body)
        match = next((x for x in items if x.get("title") == comp["title"]), None)
        if match is None:
            raise SystemExit(f"complaint {comp['title']!r} not found on re-run")
        cid = int(match["id"])
    else:
        raise SystemExit(f"create complaint for {slug}: {status} {body}")

    # Validate if not already. A second validation is a 400 ("only open
    # complaints can be validated"), which is fine.
    vstatus, vbody = c.post_retrying(f"/projects/{slug}/complaints/{cid}/validate")
    if vstatus not in (200, 400):
        raise SystemExit(f"validate complaint {cid}: {vstatus} {vbody}")
    return cid


def ensure_feature(c: Client, project_id: int, slug: str, f: dict, complaint_ids: list[int]) -> None:
    title = f["title"]

    # Check before creating. The API has no unique constraint on feature titles
    # and no conflict status, so a naive create inserts a duplicate on every
    # run; projects and complaints are matched by slug/title, and features must
    # be too or re-seeding corrupts the ranking.
    status, body = c.get(f"/projects/{slug}/features")
    if status == 200:
        items = as_items(body)
        match = next((x for x in items if x.get("title") == title), None)
        if match is not None:
            # Re-run: the feature exists, so do not create a second one — but
            # still reconcile the status. Skipping it made the seed
            # order-dependent: a run interrupted between create and set-status
            # left a feature stuck at draft, and every later run agreed it was
            # fine because the title matched.
            wanted = f.get("status")
            if wanted and wanted != match.get("status"):
                set_status(c, project_id, slug, int(match["id"]), wanted, "portfolio seed re-run")
            return

    status, body = c.post_retrying(
        f"/projects/{slug}/features",
        {
            "project_id": project_id,
            "title": title,
            "body": f.get("body", ""),
            "effort": f.get("effort", "M"),
            "linked_complaints": complaint_ids,
        },
    )
    if status == 201:
        # A feature is always created as `draft`; CreateFeature takes no status
        # and there is no other way to change one. The trust-gated route does,
        # so the seed states the real status immediately after creating it.
        wanted = f.get("status")
        if wanted and wanted != "draft":
            set_status(c, project_id, slug, body["id"], wanted, f.get("body", ""))
        return
    if status == 409:
        return
    raise SystemExit(f"create feature {title!r} on {slug}: {status} {body}")


def set_status(c: Client, project_id: int, slug: str, feature_id: int, new_status: str, reason: str) -> None:
    """Set a feature's status through the trust-gated route.

    Refuses to continue on failure rather than seeding a feature that silently
    stays `draft`: a portfolio where everything reads as an idea is worse than
    no portfolio, because it is wrong in a way nobody notices.
    """
    status, body = c.put(
        f"/projects/{slug}/features/{feature_id}/status",
        {"status": new_status, "reason": "portfolio seed"},
    )
    if status != 200:
        raise SystemExit(
            f"set status {new_status!r} on {slug} feature {feature_id}: {status} {body}"
        )


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--base", default=DEFAULT_BASE, help="API base URL")
    ap.add_argument("--seed", default=None, help="path to portfolio.json")
    ap.add_argument("--username", default="polaris", help="seed account")
    ap.add_argument(
        "--password",
        default=None,
        help="password for the seed account (required if the account exists)",
    )
    args = ap.parse_args()

    seed_path = args.seed
    if seed_path is None:
        here = __import__("pathlib").Path(__file__).resolve().parent
        seed_path = str(here / "portfolio.json")

    with open(seed_path) as f:
        data = json.load(f)

    if not args.password:
        raise SystemExit("--password is required; refusing to invent a credential")

    c = authenticate(args.base, args.username, args.password)
    print(f"authenticated as {args.username}")

    totals = {"projects": 0, "complaints": 0, "features": 0}
    for p in data["projects"]:
        pid = ensure_project(c, p)
        totals["projects"] += 1

        cids = []
        for comp in p.get("complaints", []):
            cids.append(ensure_complaint(c, pid, p["slug"], comp))
            totals["complaints"] += 1

        for f in p.get("features", []):
            # The seed refers to complaints by index within the project.
            linked = [cids[i] for i in f.get("complaints", []) if i < len(cids)]
            if not linked:
                raise SystemExit(
                    f"feature {f['title']!r} in {p['slug']} links to no complaint; "
                    "a feature must answer a validated complaint"
                )
            ensure_feature(c, pid, p["slug"], f, linked)
            totals["features"] += 1

        print(f"  {p['slug']}: {len(cids)} complaints, {len(p.get('features', []))} features")

    print(
        f"seeded {totals['projects']} projects, "
        f"{totals['complaints']} complaints, {totals['features']} features"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())