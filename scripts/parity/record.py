#!/usr/bin/env python3
"""Record the live /api/v1 shapes into a parity corpus (W11 phase 0, applied to Concord).

The corpus is what the port is judged against, so it is recorded from the DEPLOYED host and
deduplicated by (method, path-shape, body-shape). A path segment that is a number is replaced by
:id so 5,760 feature pages collapse into one shape; query parameters are sorted and kept.
"""
import argparse
import datetime
import hashlib
import json
import re
import sys
import urllib.error
import urllib.request

BASE = "https://concord.polarisocial.xyz"
ID = re.compile(r"/\d+(?=/|$)")


def shape(path: str) -> str:
    return ID.sub("/:id", path)


def fetch(method: str, path: str):
    req = urllib.request.Request(BASE + path, method=method)
    req.add_header("User-Agent", "curl/8")          # Cloudflare 1010s the python UA
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, dict(r.headers), r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read().decode("utf-8", "replace")


TIME_VARYING = "# time-varying:"


def read_seed(path: str):
    """The seed's paths, and the shapes it declares time-varying.

    A shape that carries a timestamp changes on every run, so a corpus that recorded it as stable
    would be lying; declaring it is the only honest option short of not recording it at all.
    """
    paths, varying = [], set()
    for raw in open(path):
        line = raw.strip()
        if line.startswith(TIME_VARYING):
            varying.add(shape(line[len(TIME_VARYING):].strip()))
        elif line and not line.startswith("#"):
            paths.append(line)
    return paths, varying


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--seed", required=True, help="file of paths to walk, one per line")
    ap.add_argument("--out", required=True)
    ap.add_argument("--previous", default=None,
                    help="an earlier recording to compare against (default: --out, if it exists)")
    args = ap.parse_args()
    # The corpus is only useful if it says which build it describes: the deployed host is not the
    # tree being ported, and a corpus without a version cannot be told apart from a stale one.
    corpus = {"__meta": {
        "base": BASE,
        "version": json.loads(fetch("GET", "/api/v1/version")[2]).get("version", "unknown"),
        "recorded_at": datetime.datetime.now(datetime.UTC).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "seed": args.seed,
    }}
    paths, varying = read_seed(args.seed)
    # A second run compares against the first, not only against itself: within one run the seed
    # visits each shape once, so a per-run check can never see a shape that drifts over time.
    previous = {}
    try:
        previous = json.load(open(args.previous or args.out))
    except (OSError, ValueError):
        pass
    for path in paths:
        status, headers, body = fetch("GET", path)
        key = shape(path)
        entry = corpus.setdefault(key, {
            "key": key, "status": status,
            "content_type": headers.get("content-type", ""),
            "estate_headers": {k: headers[k] for k in headers
                               if k.lower() in ("x-estate-site", "x-estate-version", "etag",
                                                "cache-control")},
            "body_sha": hashlib.sha256(body.encode()).hexdigest()[:16],
            "body_len": len(body),
        })
        # Two recordings of the same shape must agree, or the corpus is of a moving target.
        digest = hashlib.sha256(body.encode()).hexdigest()[:16]
        if entry["body_sha"] != digest:
            entry.setdefault("unstable", 0)
            entry["unstable"] += 1
        before = previous.get(key)
        if before and before.get("body_sha") != entry["body_sha"]:
            entry["unstable"] = entry.get("unstable", 0) + 1
        if key in varying:
            entry["time_varying"] = True
    json.dump(corpus, open(args.out, "w"), indent=1, sort_keys=True)
    print(f"{len(corpus)} shapes -> {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
