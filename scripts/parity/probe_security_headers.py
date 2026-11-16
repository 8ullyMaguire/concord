#!/usr/bin/env python3
"""The browser is told what to refuse: does this instance actually say it?

Measured on 2026-11-15, before this probe existed:

    curl -sI https://concord.polarisocial.xyz/ | grep -ic 'content-security-policy|x-frame-options'
    0

With the session token in `localStorage` and 137 `innerHTML` sites in the page scripts, one injected string was a full
account takeover and nothing in the response told the browser to refuse it.

Two questions, because a header and a document can disagree:

  1. **Does the response carry the policy?** `Content-Security-Policy`, `X-Frame-Options`, `X-Content-Type-Options`,
     `Strict-Transport-Security`, `Referrer-Policy` — on a page, on an API route, on an unknown path and on a refused
     request. A header that only appears on 200s is a header that is missing where it matters.
  2. **Does any page still need what the policy forbids?** `script-src 'self'` refuses inline scripts, so an inline
     `<script>` anywhere in the served HTML is a page that will not work once the policy is enforced — the failure the
     policy would cause silently in a browser, caught here by reading the bytes.

Exit 0 when both hold, 1 otherwise, with the offending URL and header named.

Usage:
    python3 scripts/parity/probe_security_headers.py --candidate https://concord.polarisocial.xyz
"""
from __future__ import annotations

import argparse
import re
import sys
import urllib.error
import urllib.request

REQUIRED = (
    "content-security-policy",
    "x-frame-options",
    "x-content-type-options",
    "strict-transport-security",
    "referrer-policy",
)

# A page, an API route, an unknown path (the 404 fallback) and a refused mutation (the gate). Four response paths, because
# a middleware can sit anywhere in the stack and the interesting failures are the ones it does not reach.
PATHS = ("/", "/projects", "/api/v1/version", "/no-such-page", "/v/nsfw")

# Pages that are HTML and therefore carry scripts. API responses are skipped for the inline question: JSON cannot be.
SCRIPT_TAG = re.compile(rb"<script\b([^>]*)>", re.I)


def fetch(candidate: str, path: str) -> tuple[int, dict[str, str], bytes]:
    request = urllib.request.Request(candidate.rstrip("/") + path, method="GET")
    request.add_header("User-Agent", "concord-security-probe/1")
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            return response.status, {k.lower(): v for k, v in response.headers.items()}, response.read()
    except urllib.error.HTTPError as error:  # a 404 still has headers, and it is the interesting one
        return error.code, {k.lower(): v for k, v in error.headers.items()}, error.read()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--candidate", required=True)
    args = parser.parse_args()

    failures: list[str] = []
    print(f"security headers on {args.candidate}")
    for path in PATHS:
        try:
            status, headers, body = fetch(args.candidate, path)
        except Exception as error:  # noqa: BLE001 - any transport failure is a failure of the probe's question
            failures.append(f"{path}: could not be fetched ({error})")
            continue
        missing = [name for name in REQUIRED if name not in headers]
        for name in missing:
            failures.append(f"{path} ({status}): missing {name}")
        policy = headers.get("content-security-policy", "")
        if "script-src 'self'" not in policy:
            failures.append(f"{path} ({status}): script-src is not 'self' ({policy or 'no policy'})")
        # The whole point: a policy that permits inline script permits exactly the injection it exists to stop.
        script_src = re.search(r"script-src([^;]*)", policy)
        if script_src and "'unsafe-inline'" in script_src.group(1):
            failures.append(f"{path} ({status}): script-src allows 'unsafe-inline' — the policy refuses nothing")
        if "frame-ancestors 'none'" not in policy:
            failures.append(f"{path} ({status}): frame-ancestors is not 'none' ({policy or 'no policy'})")
        print(f"  {path:18} {status}  headers {'complete' if not missing else 'MISSING ' + ','.join(missing)}")

        if "text/html" not in headers.get("content-type", ""):
            continue
        for match in SCRIPT_TAG.finditer(body):
            attrs = match.group(1).decode("utf-8", "replace")
            if "src=" in attrs:
                continue
            kind = re.search(r'type="([^"]+)"', attrs)
            if kind and not re.fullmatch(r"(?:text|application)/(?:javascript|ecmascript)", kind.group(1)):
                continue  # a data block (`application/json`), not an executable script
            line = body[: match.start()].count(b"\n") + 1
            failures.append(f"{path}: inline executable <script> at line {line} — script-src 'self' refuses it")

    if failures:
        for failure in failures:
            print(f"FAIL {failure}")
        print(f"probe_security_headers: FAIL ({len(failures)} problem(s))")
        return 1
    print("probe_security_headers: PASS — every response carries the policy and no page needs an inline script")
    return 0


if __name__ == "__main__":
    sys.exit(main())
