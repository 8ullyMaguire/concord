"""Open every page a signed-out visitor can reach and report each request that did not answer 2xx.

`route_readers.py` answers "does every route have a caller". This answers the opposite and more damaging question: "does
every request a page makes reach a route" — which is how the trailing-slash class hid. A page whose script asks
`/api/v1/similar/complaint/?…`, gets a 404 and catches it renders a `200` with an empty panel; a probe that asserts status
codes sees nothing, and the feature is dead for every user while the page looks fine.

Three defects of exactly this shape were found by hand on 2026-11-15 (`/similar/{kind}/`, `PUT /documents/`,
`/features/` in settings). This crawls the signed-out surface and prints what is still broken.

    CANDIDATE=https://concord.polarisocial.xyz python3 scripts/parity/probe_requests.py
    # or against the local candidate: CONCORD_E2E_CANDIDATE=... PROBE_BASE=http://127.0.0.1:8499

Exit code 1 when any request answered 404, 405 or 5xx. A 401/403 is listed as *expected for a signed-out visitor* rather
than failed: those routes require an account, the client asks for them behind `getOrNull`, and what they prove is that the
route exists. Requests that never completed (network failed, aborted by navigation) are listed separately and do not fail
the probe: they are usually the page changing while the crawl moves on.
"""

import os
import sys
import urllib.parse

from playwright.sync_api import sync_playwright

BASE = os.environ.get("PROBE_BASE") or os.environ.get("CANDIDATE") or "http://127.0.0.1:8006"

# Every signed-out page that runs a script. Kept as a list rather than discovered, because a crawler that follows only the
# links it is given is honest about what it covered — and this list is checked against Shell's routes by hand when a page
# is added.
PAGES = [
    "/",
    "/projects",
    "/projects/suki",
    "/projects/suki/documents",
    "/projects/suki/board",
    "/projects/suki/roadmap",
    "/projects/suki/requests",
    "/projects/suki/ranking",
    "/projects/suki/settings",
    "/stats",
    "/adopt",
    "/find",
    "/map",
    "/search?q=arena",
    "/audit",
    "/curation",
    "/requests",
    "/merge",
    "/notifications",
    "/leaderboard",
]

INTERESTING = ("/api/", ".json", "/assets/js/")


def main():
    problems, unfinished, seen, expected = [], [], set(), []
    with sync_playwright() as p:
        browser = p.chromium.launch()
        context = browser.new_context()
        page = context.new_page()
        pending = []

        def on_response(response):
            url = response.url
            if not any(fragment in url for fragment in INTERESTING):
                return
            path = urllib.parse.urlparse(url).path
            key = (response.request.method, path, urllib.parse.urlparse(url).query)
            if key in seen:
                return
            seen.add(key)
            status = response.status
            if status in (401, 403):
                expected.append((response.request.method, path, status))
                return
            if status >= 400:
                problems.append((response.request.method, path, status, url[:120]))

        def on_failure(request):
            url = request.url
            if any(fragment in url for fragment in INTERESTING):
                unfinished.append((request.method, urllib.parse.urlparse(url).path, request.failure))

        page.on("response", on_response)
        page.on("requestfailed", on_failure)

        for path in PAGES:
            try:
                # `domcontentloaded`, not `load`: `load` waits for every subresource, and one hanging request (a font, an
                # image) then times the whole page out — the second run of this probe reported `/` as "navigation failed"
                # while `curl` fetched it in 0.35s. What this probe wants is the script's own requests, which start at
                # DOM ready; the fixed wait below is what gives them time to land.
                page.goto(BASE + path, wait_until="domcontentloaded", timeout=20000)
                page.wait_for_timeout(3000)
            except Exception as exc:  # noqa: BLE001 - a page that will not load is reported, not raised
                problems.append(("GET", path, "navigation failed", str(exc)[:100]))
        browser.close()

    print(f"base: {BASE}")
    print(f"pages crawled: {len(PAGES)}")
    print(f"distinct requests observed: {len(seen)}")
    if unfinished:
        print(f"requests that never completed: {len(unfinished)}")
        for method, path, why in unfinished[:10]:
            print(f"  {method} {path}  ({why})")
    if expected:
        print(f"routes that refuse an anonymous caller (401/403, expected): {len(expected)}")
        for method, path, status in sorted(set(expected)):
            print(f"  {method} {path}  -> {status}")
    if not problems:
        print("requests answered 404/405/5xx: 0")
        return 0
    print(f"requests answered 404/405/5xx: {len(problems)}")
    for method, path, status, url in problems:
        print(f"  {method} {path}  -> {status}\n      {url}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
