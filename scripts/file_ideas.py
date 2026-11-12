#!/usr/bin/env python3
"""File the depth catalog onto a live Concord instance.

    python3 scripts/file_ideas.py --base https://concord.polarisocial.xyz \
        --token-file ~/.secrets/concord-api-token.txt --project concord

For each theme in seed/concord_ideas.json:

1. find the theme's complaint by title, or create it and validate it (a feature must answer a
   VALIDATED complaint -- store.CreateFeature refuses an unvalidated one);
2. create each idea as a draft feature linked to that complaint, tagging the seeded body with the
   seed path so a row can always be traced back to the file that made it.

It is IDEMPOTENT by title: a title that already exists on the project (complaint or feature) is
skipped. Re-running after an edit adds only what is new, which is what makes the file safe to
keep editing.

It is paced, because the instance rate-limits at 100 requests/minute per client and a 400-request
burst would spend the run earning 429s.
"""
import argparse
import http.cookiejar
import json
import pathlib
import sys
import time
import urllib.error
import urllib.request

SEED = pathlib.Path(__file__).resolve().parent.parent / "seed" / "concord_ideas.json"


class Client:
    def __init__(self, base: str, token: str, pace: float):
        self.base = base.rstrip("/")
        self.token = token
        self.pace = pace
        self.calls = 0
        self.last = 0.0
        # The consent gate refuses every mutating request without its cookie, and the dish it
        # comes from is a POST to /api/v1/consent, so the client is an opener with a jar rather
        # than a bare urlopen. Consent permits; the bearer token still identifies.
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))

    def _wait(self):
        gap = self.pace - (time.monotonic() - self.last)
        if gap > 0:
            time.sleep(gap)
        self.last = time.monotonic()

    def call(self, method: str, path: str, body=None, retries: int = 5):
        url = self.base + path
        data = json.dumps(body).encode() if body is not None else None
        for attempt in range(retries):
            self._wait()
            req = urllib.request.Request(url, data=data, method=method)
            # Cloudflare rejects the default Python-urllib agent with a 403/1010 before the
            # request ever reaches the instance, so name the tool.
            req.add_header("User-Agent", "concord-file-ideas/1.0 (+https://concord.polarisocial.xyz)")
            req.add_header("Accept", "application/json")
            req.add_header("Authorization", "Bearer " + self.token)
            if data is not None:
                req.add_header("Content-Type", "application/json")
            try:
                with self.opener.open(req, timeout=60) as resp:
                    self.calls += 1
                    raw = resp.read()
                    return resp.status, (json.loads(raw) if raw else None)
            except urllib.error.HTTPError as e:
                self.calls += 1
                raw = e.read()
                if e.code == 429:
                    # The limiter is a per-minute bucket; waiting it out is cheaper than
                    # lowering the pace for every later call.
                    time.sleep(20 + attempt * 5)
                    continue
                try:
                    payload = json.loads(raw)
                except Exception:
                    payload = raw.decode(errors="replace")
                return e.code, payload
        return 429, "rate limited after retries"


def accept_consent(cli: "Client") -> None:
    st, resp = cli.call("POST", "/api/v1/consent", {})
    if st != 200:
        raise SystemExit(f"consent refused: {st} {resp}")
    print(f"consent accepted ({resp.get('consent')})")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="https://concord.polarisocial.xyz")
    ap.add_argument("--token-file", default=str(pathlib.Path.home() / ".secrets/concord-api-token.txt"))
    ap.add_argument("--project", default="concord", help="project slug")
    ap.add_argument("--limit", type=int, default=0, help="stop after N features (0 = all)")
    ap.add_argument("--only", default="", help="comma-separated theme keys")
    ap.add_argument("--pace", type=float, default=0.85, help="seconds between requests")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    token = pathlib.Path(args.token_file).read_text().strip()
    data = json.loads(SEED.read_text())
    cli = Client(args.base, token, args.pace)

    # No trailing slash here, and a trailing slash on EVERY collection/id route below: chi mounts
    # these as subrouters whose root is "/", so /features (no slash) is a 404 and /features/ works.
    accept_consent(cli)

    status, proj = cli.call("GET", f"/api/v1/projects/{args.project}")
    if status != 200:
        print(f"cannot read project {args.project}: {status} {proj}", file=sys.stderr)
        return 1
    pid = proj["id"]
    slug = proj["slug"]
    api = f"/api/v1/projects/{slug}"
    print(f"project {slug} = id {pid}")

    status, existing_features = cli.call("GET", f"{api}/features/")
    status2, existing_complaints = cli.call("GET", f"{api}/complaints/")
    if status != 200 or status2 != 200:
        print(f"cannot list existing rows: {status}/{status2}", file=sys.stderr)
        return 1
    have_features = {f["title"] for f in existing_features}
    complaints_by_title = {c["title"]: c for c in existing_complaints}
    print(f"existing: {len(have_features)} features, {len(complaints_by_title)} complaints")

    only = {s.strip() for s in args.only.split(",") if s.strip()}
    created = skipped = rejected = 0
    per_theme = {}
    for theme in data["themes"]:
        key = theme["key"]
        if only and key not in only:
            continue
        items = data["ideas"].get(key, [])
        if not items:
            continue

        complaint_id = None
        c = complaints_by_title.get(theme["complaint_title"])
        if c is not None:
            complaint_id = c["id"]
            if c.get("status") != "validated":
                if not args.dry_run:
                    st, resp = cli.call("POST", f"{api}/complaints/{complaint_id}/validate")
                    if st != 200:
                        print(f"  ! validate {theme['key']}: {st} {resp}", file=sys.stderr)
        else:
            if args.dry_run:
                print(f"[dry] complaint: {theme['complaint_title']}")
                complaint_id = -1
            else:
                st, resp = cli.call("POST", f"{api}/complaints/", {
                    "project_id": pid,
                    "title": theme["complaint_title"],
                    "body": theme["complaint_body"],
                    "severity": theme["severity"],
                    "frequency": 1.0,
                    "strategic_multiplier": 1.0,
                    "confirm_duplicate": True,
                })
                if st != 201:
                    print(f"  ! complaint {theme['key']}: {st} {resp}", file=sys.stderr)
                    continue
                complaint_id = resp["id"]
                st, resp = cli.call("POST", f"{api}/complaints/{complaint_id}/validate")
                if st != 200:
                    print(f"  ! validate new {theme['key']}: {st} {resp}", file=sys.stderr)
                    continue
                print(f"  + complaint #{complaint_id}: {theme['complaint_title']}")

        n = 0
        for idea in items:
            if args.limit and created >= args.limit:
                break
            if idea["title"] in have_features:
                skipped += 1
                continue
            body = idea["body"] + f"\n\n_Theme: {theme['label']}. Filed from `seed/concord_ideas.json`._"
            if args.dry_run:
                print(f"[dry] {key}: {idea['title']} ({idea['effort']})")
                created += 1
                n += 1
                continue
            payload = {
                "project_id": pid,
                "title": idea["title"],
                "body": body,
                "effort": idea["effort"],
                "linked_complaints": [complaint_id],
                "confirm_duplicate": True,
            }
            # The seed may carry a value out of ten and a cost in weeks. They are uploaded as the
            # feature's own impact and effort_score, which are the columns the page's
            # impact-over-effort order reads -- so the file that states the score and the row that
            # shows it cannot disagree.
            if "value" in idea:
                payload["impact"] = idea["value"]
            if "weeks" in idea:
                payload["effort_score"] = idea["weeks"]
            st, resp = cli.call("POST", f"{api}/features/", payload)
            if st != 201:
                print(f"  ! feature {idea['title']!r}: {st} {resp}", file=sys.stderr)
                continue
            # Below the stated bar the row is filed REJECTED rather than omitted: the verdict and
            # its reason live in the body, so the next session does not re-propose an idea that
            # already lost. The bar is 6, fixed before any score was written.
            if idea.get("value", 10) < 6 and isinstance(resp, dict) and "id" in resp:
                # PUT .../status carries only status and reason, and it is trust-gated
                # (handleSetFeatureStatus), which is why the filer needs the owner's token.
                st2, resp2 = cli.call("PUT", f"{api}/features/{resp['id']}/status",
                                      {"status": "rejected",
                                       "reason": "below the value bar (6/10)"})
                if st2 != 200:
                    print(f"  ! reject {idea['title']!r}: {st2} {resp2}", file=sys.stderr)
                else:
                    rejected += 1
            have_features.add(idea["title"])
            created += 1
            n += 1
        per_theme[key] = n
        print(f"  {key}: +{n}")

    print(f"\nfiled {created} new features ({rejected} as rejected, below the value bar), "
          f"skipped {skipped} that already existed, {cli.calls} requests")
    print("per theme: " + ", ".join(f"{k}={v}" for k, v in per_theme.items()))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
