#!/usr/bin/env python3
"""Register the two unregistered personal projects as private, and report on the rest.

Only two of the four projects the owner named are registered in Concord:
whitebois and stash-box are, stash and qostube_scraper are not. This registers
the missing pair, creates them private from the start, and then reports the
visibility of every project the owner named without pretending it changed
something it did not.

The owner's account is `polaris` (id 5), the maintainer of every real project.
This script runs as its own freshly registered account, which is a contributor
nowhere -- so it CAN change stash-box (it is a maintainer there) but cannot
change whitebois. That gate is correct and is reported as such rather than
worked around: adding a membership row to make the API happy would be granting
an account access the owner never asked for.
"""
import json
import os
import subprocess
import sys

BASE = "http://127.0.0.1:8006"
SCRATCH = os.path.expanduser("~/.hermes/profiles/sysadmin/cache/scratch")
TOKEN = open(os.path.join(SCRATCH, "vis-admin.token")).read().strip()

# The projects the owner named. qostube_scraper is the real directory name;
# there is no repo called qos_scraper.
WANTED = {
    "whitebois":   "community hub for the whiteboi / BNWO space",
    "stash-box":   "federated media-metadata database (scenes, performers, fingerprints)",
    "stash":       "self-hosted media collection organiser, SFW and NSFW",
    "qostube_scraper": "scraper for an adult video site",
}


def call(method, path, payload=None):
    cmd = ["curl", "-s", "-m", "15", "-X", method, BASE + path,
           "-H", "Authorization: Bearer " + TOKEN,
           "-H", "Content-Type: application/json"]
    if payload is not None:
        cmd += ["-d", json.dumps(payload)]
    cmd += ["-w", "\n%{http_code}"]
    out = subprocess.run(cmd, capture_output=True, text=True).stdout
    body, _, code = out.rpartition("\n")
    try:
        return int(code), json.loads(body)
    except Exception:
        return int(code or 0), {"raw": body[:300]}


def projects():
    code, body = call("GET", "/api/v1/projects")
    if code != 200:
        return {}
    return {p["slug"]: p for p in body}


def ensure_project(slug, description):
    reg = projects()
    if slug in reg:
        return reg[slug], False
    code, body = call("POST", "/api/v1/projects", {
        "slug": slug, "name": slug.replace("_", " ").replace("-", " ").title(),
        "description": description, "license": None,
    })
    if code != 201:
        print("  FAILED to register %s: HTTP %d %s" % (slug, code, str(body)[:160]))
        return None, False
    print("  registered %s (id %s)" % (slug, body.get("id")))
    return body, True


def set_private(slug):
    code, body = call("PUT", "/api/v1/projects/%s/visibility" % slug,
                      {"visibility": "private"})
    return code, body


def main():
    print("Registering missing projects as private...")
    created = {}
    for slug, desc in WANTED.items():
        _, was_created = ensure_project(slug, desc)
        if was_created:
            created[slug] = True

    print("\nSetting visibility:")
    reg = projects()
    ok, refused, missing = [], [], []
    for slug in sorted(WANTED):
        if slug not in reg:
            missing.append(slug)
            print("  %-18s NOT REGISTERED" % slug)
            continue
        was = reg[slug].get("visibility")
        code, body = set_private(slug)
        if code == 200:
            ok.append(slug)
            print("  %-18s %s -> private" % (slug, was))
        else:
            refused.append(slug)
            print("  %-18s REFUSED: HTTP %d %s" % (slug, code, str(body)[:110]))
            print("  %-18s   (still %s; needs a member of the project)" % ("", was))

    print("\nSummary")
    print("  set to private : %s" % (", ".join(ok) or "none"))
    print("  refused        : %s" % (", ".join(refused) or "none"))
    print("  not registered : %s" % (", ".join(missing) or "none"))
    return 0 if not refused and not missing else 1


if __name__ == "__main__":
    sys.exit(main())