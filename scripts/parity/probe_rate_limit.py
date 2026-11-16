#!/usr/bin/env python3
"""Does a login loop get refused, and does the refusal say when to come back?

Measured on 2026-11-16, before this probe existed: `grep -rn "rate" crates/app/src/lib.rs` found nothing, and the
vendored `polaris_ratelimit` crate — 592 lines with deterministic bucket arithmetic — was a dependency of nothing
(`cargo tree -i polaris-ratelimit` answered "nothing depends on it"). A loop of forty logins was answered forty times.

Three questions, because a rate limit is easy to fake and easy to make harmful:

  1. **Does the budget hold?** The first N requests in a class are answered normally; the next one is a 429. N comes from
     `--budget`, which must match the server's `CONCORD_RATE_LOGIN` — the instrument is told what it is testing instead of
     guessing, and the refused response's `X-RateLimit-Limit` is checked against it.
  2. **Can the client act on the refusal?** `Retry-After` must be a positive integer and `X-RateLimit-Remaining` must be
     0. A 429 without either is a wall, not a limit.
  3. **Is the budget per client?** A second client identity still gets answered — otherwise one loop silences the site —
     and a *different class* (an ordinary API GET) is untouched, because charging page loads to the login bucket would
     refuse readers for reading.

Usage:
    python3 scripts/parity/probe_rate_limit.py --candidate http://127.0.0.1:8097 --budget 3
    python3 scripts/parity/probe_rate_limit.py --candidate https://concord.polarisocial.xyz --budget 10
"""
from __future__ import annotations

import argparse
import json
import sys
import urllib.error
import urllib.request

LOGIN = "/api/v1/auth/login"


def call(candidate: str, path: str, body: dict | None, client: str) -> tuple[int, dict[str, str], bytes]:
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(candidate.rstrip("/") + path, data=data, method="POST" if data else "GET")
    request.add_header("User-Agent", "concord-rate-probe/1")
    # `client_key` reads this only from a loopback peer, which is what a local candidate is; against a deployment it is
    # Cloudflare's own header and cannot be chosen. Either way a distinct value is a distinct bucket, which is the point.
    request.add_header("CF-Connecting-IP", client)
    if data:
        request.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            return response.status, {k.lower(): v for k, v in response.headers.items()}, response.read()
    except urllib.error.HTTPError as error:
        return error.code, {k.lower(): v for k, v in error.headers.items()}, error.read()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--budget", type=int, default=3, help="the server's CONCORD_RATE_LOGIN")
    parser.add_argument("--attempts", type=int, default=0, help="defaults to budget + 3")
    parser.add_argument("--client", default="203.0.113.7")
    args = parser.parse_args()
    attempts = args.attempts or args.budget + 3

    failures: list[str] = []
    print(f"rate limit on {args.candidate} (login budget {args.budget}/min)")
    bad_login = {"email": "nobody@example.invalid", "password": "not-the-password"}

    statuses: list[int] = []
    for attempt in range(attempts):
        status, headers, _ = call(args.candidate, LOGIN, bad_login, args.client)
        statuses.append(status)
        if status == 429:
            wait = headers.get("retry-after", "")
            if not wait.isdigit() or int(wait) <= 0:
                failures.append(f"429 without a usable Retry-After ({wait!r})")
            limit = headers.get("x-ratelimit-limit", "")
            if limit != str(args.budget):
                failures.append(f"429 says X-RateLimit-Limit {limit!r}, the server was told {args.budget}")
            if headers.get("x-ratelimit-remaining") != "0":
                failures.append("429 without X-RateLimit-Remaining: 0")
            print(f"  login #{attempt + 1:2}  429  retry-after={wait}s limit={limit}")
            break
        print(f"  login #{attempt + 1:2}  {status}")

    refused = [i for i, status in enumerate(statuses) if status == 429]
    if not refused:
        failures.append(
            f"no 429 in {attempts} logins from one client — the route has no budget "
            f"(statuses: {sorted(set(statuses))})"
        )
    elif refused[0] < args.budget:
        failures.append(f"refused at attempt {refused[0] + 1}, before the {args.budget}/min budget was used")
    elif refused[0] > args.budget:
        failures.append(f"refused at attempt {refused[0] + 1}, after a {args.budget}/min budget")

    # A different client must still be answered: a key, not a global switch.
    status, _, _ = call(args.candidate, LOGIN, bad_login, "198.51.100.42")
    if status == 429:
        failures.append("a second client was refused too — the budget is global, not per client")
    else:
        print(f"  second client {status}  (own bucket)")

    # A different class must be untouched: page loads and API reads are not logins.
    status, headers, _ = call(args.candidate, "/api/v1/projects", None, args.client)
    if status == 429:
        failures.append("an ordinary API GET was refused after a login burst — the classes share a bucket")
    else:
        print(f"  api read      {status}  (separate budget)")

    if failures:
        for failure in failures:
            print(f"FAIL {failure}")
        print(f"probe_rate_limit: FAIL ({len(failures)} problem(s))")
        return 1
    print("probe_rate_limit: PASS — the budget holds, the refusal says when to retry, and it is per client and per class")
    return 0


if __name__ == "__main__":
    sys.exit(main())
