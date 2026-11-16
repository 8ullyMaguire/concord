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


def directives(policy: str) -> dict[str, set[str]]:
    """`script-src 'self' https:` -> {"script-src": {"'self'", "https:"}}."""
    out: dict[str, set[str]] = {}
    for part in policy.split(";"):
        words = part.split()
        if words:
            out[words[0].lower()] = set(words[1:])
    return out


def effective(policies: list[str], name: str) -> set[str]:
    """What the browser enforces for one directive when several policies are present.

    A browser applies **every** `Content-Security-Policy` header it receives, so a source is allowed only if every policy
    that mentions the directive allows it. An empty set means "no policy constrains it".
    """
    seen = [directives(p) for p in policies]
    mentioned = [d[name] for d in seen if name in d]
    if not mentioned:
        return set()
    out = set(mentioned[0])
    for sources in mentioned[1:]:
        out &= sources
    return out


def fetch(candidate: str, path: str) -> tuple[int, dict[str, str], bytes]:
    request = urllib.request.Request(candidate.rstrip("/") + path, method="GET")
    request.add_header("User-Agent", "concord-security-probe/1")
    def headers_of(message) -> dict[str, str]:  # noqa: ANN001 - http.client.HTTPMessage
        merged: dict[str, str] = {}
        for key, value in message.items():
            key = key.lower()
            # Several `Content-Security-Policy` headers arrive as one comma-joined value; keep them separate because a
            # browser enforces each one. Everything else is last-wins, which is what a browser does too.
            merged[key] = f"{merged[key]}\n{value}" if key in merged else value
        return merged

    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            return response.status, headers_of(response.headers), response.read()
    except urllib.error.HTTPError as error:  # a 404 still has headers, and it is the interesting one
        return error.code, headers_of(error.headers), error.read()


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
        raw = headers.get("content-security-policy", "")
        policies = [p for p in raw.split("\n") if p.strip()]
        script = effective(policies, "script-src")
        frame = effective(policies, "frame-ancestors")
        # The whole point: a policy that permits inline script permits exactly the injection it exists to stop, and a
        # browser enforces every header it is given, so the *intersection* is what protects the page.
        if not script:
            failures.append(f"{path} ({status}): no script-src in any policy")
        elif "'unsafe-inline'" in script:
            failures.append(
                f"{path} ({status}): effective script-src allows 'unsafe-inline' ({' '.join(sorted(script))})"
            )
        elif "self" not in {s.strip("'") for s in script}:
            failures.append(f"{path} ({status}): effective script-src is not 'self' ({' '.join(sorted(script))})")
        # `'none'` in any one policy blocks every frame on its own, so it is already the strictest outcome; an empty
        # intersection means the same thing (a source must be allowed by every policy, and one allows nothing).
        frame_none = any("none" in directives(p).get("frame-ancestors", set()) for p in policies)
        if not frame_none and frame:
            failures.append(
                f"{path} ({status}): frame-ancestors permits framing ({' '.join(sorted(frame))})"
            )
        note = ""
        if len(policies) > 1:
            weak = [p for p in policies if "'unsafe-inline'" in " ".join(directives(p).get("script-src", ()))]
            note = f"  [{len(policies)} policies; {len(weak)} permissive]"
        print(f"  {path:18} {status}  headers {'complete' if not missing else 'MISSING ' + ','.join(missing)}{note}")

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
