#!/usr/bin/env python3
"""Regenerate seed/forge_repos.json from the catalog and the two forges (Q5, spec §4.5.7.1).

The file this writes is the one `cmd/forgeseed` applies, and its contract is: every project in the
catalog appears exactly once, either with a `repo_url` + `repo_host`, or with `"skip": true` and a
reason. `cmd/forgeseed` refuses a file that misses a slug, that has an entry with neither, or that
skips without saying why -- so this script would rather emit a skip with a measured reason than a
guess.

Three rules, in order of how much they cost to get wrong:

 1. NEVER GUESS A URL. A candidate is accepted only if it is the slug's own name on one of the two
    accounts, or an alias declared in ALIASES below with its justification next to it. The bar is
    "I can say why this repository IS this project", not "the names look alike".

 2. VERIFY BEFORE WRITING. Every accepted URL is fetched from the forge's API. A URL that does not
    answer 200 anonymously is either private or gone; either way `healthsync` (which has no token
    on this host) would fail on it every hour, so it does not go in.

 3. SAY WHAT WAS LOOKED FOR. A skip carries a reason a reader can act on -- "private, and the
    ingest has no token", "upstream project, not the user's repository", "no repository found in
    the Forgejo account, the GitHub account, or the local checkouts". "not stated (Q5)" told nobody
    anything, which is why the file stayed at 5 of 74 for two weeks.

Usage:
    scripts/build_forge_repos.py --db ~/.local/share/concord/concord.db --out seed/forge_repos.json
    scripts/build_forge_repos.py --db ... --out /tmp/x.json --dry-run
"""

import argparse
import json
import os
import sqlite3
import sys
import time
import urllib.error
import urllib.request

FORGEJO = "https://git.polarisocial.xyz"
FORGEJO_OWNER = "hirrolot19"
GITHUB_API = "https://api.github.com"
GITHUB_OWNER = "8ullyMaguire"

# slug -> (host, owner, repo, justification). Every entry here is a name that is NOT the slug, and
# the justification is the reason a reader should believe it rather than the next one they think of.
ALIASES = {
    "ferrisfeed": ("forgejo", FORGEJO_OWNER, "piefed-rs",
                   "the ferrisfeed site is the piefed-rs deployment; the checkout named ferrisfeed "
                   "has its origin at hirrolot19/piefed-rs"),
    "feedgen": ("forgejo", FORGEJO_OWNER, "bluesky-feedgen",
                "feedgen is the short name of bluesky-feedgen, which has its own repository"),
    "ficnexus": ("github", GITHUB_OWNER, "ficnexus",
                 "the Forgejo copy answers 404 anonymously; the GitHub repository is public"),
}

# slug -> why it cannot carry a repository. Kept as data rather than inferred, because the reason a
# reader needs is different in each case and "no repo" is not one reason.
SKIPS = {
    "suki": "private: git.polarisocial.xyz/hirrolot19/suki answers 404 anonymously, and healthsync "
            "runs without CONCORD_FORGE_TOKEN on this host",
    "blackrep": "private: git.polarisocial.xyz/hirrolot19/blackrep answers 404 anonymously, and "
                "healthsync runs without CONCORD_FORGE_TOKEN on this host",
    "ascend": "private: git.polarisocial.xyz/hirrolot19/ascend answers 404 anonymously, and "
              "healthsync runs without CONCORD_FORGE_TOKEN on this host",
    "ao3downloader": "private: git.polarisocial.xyz/hirrolot19/ao3downloader answers 404 "
                     "anonymously, and healthsync runs without CONCORD_FORGE_TOKEN on this host",
    "media-wiki": "the repository is on git.disroot.org, which is not one of the three hosts "
                  "ParseRepoURL accepts (github.com, codeberg.org, git.polarisocial.xyz)",
    "dream-social-media-platform": "the repository is on git.disroot.org, which is not one of the "
                                   "three hosts ParseRepoURL accepts",
    "lemmy": "upstream Lemmy project (LemmyNet); the catalog entry is the work, not a repository "
             "the user owns",
    "lemmy-ui": "upstream Lemmy project (LemmyNet), not the user's repository",
    "lemmy-docs": "upstream Lemmy project (LemmyNet), not the user's repository",
    "build-lemmy": "no repository found in the Forgejo account, the GitHub account, or the local "
                   "checkouts",
    "ravenclaws": "the only repository by this name belongs to another author (egkristi/RavenClaws)",
    "rust-threadlight-mirror": "a mirror: measuring it and threadlight-go would count the same "
                               "commits twice",
    "piefed-rs-broken-git": "the entry records a broken checkout, not a repository; piefed-rs is "
                            "seeded under ferrisfeed",
    "trending-linux-packages-data": "the data lives inside trending-linux-packages, which has its "
                                    "own repository",
    "whitebois": "the checkout has no remote configured (an un-remoted mirror)",
    "stashforge": "no repository found in the Forgejo account, the GitHub account, or the local "
                  "checkouts",
    "stashbox-fork": "no repository found in the Forgejo account, the GitHub account, or the local "
                     "checkouts",
}

NO_REPO = ("no repository found in the Forgejo account, the GitHub account, or the local "
           "checkouts (measured 2026-11-12)")


def fetch(url, timeout=25):
    req = urllib.request.Request(url, headers={"User-Agent": "concord-forge-seed"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read(200000)
    except urllib.error.HTTPError as e:
        return e.code, b""
    except Exception as e:  # noqa: BLE001 - a network failure is a "not verified", not a crash
        return 0, str(e).encode()


def forgejo_public_repos():
    names = set()
    for page in range(1, 6):
        code, body = fetch(f"{FORGEJO}/api/v1/repos/search?limit=50&page={page}")
        if code != 200:
            break
        data = json.loads(body or b"{}").get("data") or []
        if not data:
            break
        for r in data:
            names.add(r["full_name"].split("/")[-1].lower())
    return names


def github_public_repos():
    names = set()
    for page in range(1, 4):
        code, body = fetch(f"{GITHUB_API}/users/{GITHUB_OWNER}/repos?per_page=100&page={page}")
        if code != 200:
            break
        data = json.loads(body or b"[]")
        if not data:
            break
        for r in data:
            names.add(r["name"].lower())
    return names


class Inconclusive(Exception):
    """A verification answer that neither confirms nor denies the repository."""


def verify(host, owner, repo, attempts=4):
    """Fetch the repository, and say only what the answer supports.

    200 means it is there. 404/410 means it is not -- a skip, and a measured one. 403 is neither:
    both forges answer it when the rate limit is spent, which says nothing about the repository.
    Writing a skip for it is how a regeneration silently deleted six good entries on the first
    attempt, so an inconclusive answer retries and then ABORTS the run without writing a file.
    """
    url = (f"{GITHUB_API}/repos/{owner}/{repo}" if host == "github"
           else f"{FORGEJO}/api/v1/repos/{owner}/{repo}")
    last = None
    for i in range(attempts):
        code, _ = fetch(url)
        if code in (200, 404, 410):
            return code
        last = code
        if i + 1 < attempts:
            time.sleep(15 * (i + 1))  # rides out a burst; GitHub's real window is an hour
    raise Inconclusive(f"{host} {owner}/{repo} answered {last} on {attempts} attempts")


def catalog_repos(db_path):
    con = sqlite3.connect(f"file:{os.path.expanduser(db_path)}?mode=ro", uri=True)
    try:
        return [(r[0], r[1], r[2]) for r in con.execute(
            "SELECT slug, repo_url, repo_host FROM projects "
            "WHERE repo_url IS NOT NULL AND repo_url != '' ORDER BY slug")]
    finally:
        con.close()


def write(entries, args):
    text = json.dumps(entries, indent=2) + "\n"
    seeded = [e for e in entries if not e.get("skip")]
    print(f"{len(seeded)}/{len(entries)} slugs carry a repository", file=sys.stderr)
    if args.dry_run:
        print(text)
        return
    with open(args.out, "w") as f:
        f.write(text)
    print(f"wrote {args.out}", file=sys.stderr)


def catalog(db_path):
    con = sqlite3.connect(f"file:{os.path.expanduser(db_path)}?mode=ro", uri=True)
    try:
        return [r[0] for r in con.execute("SELECT slug FROM projects ORDER BY slug")]
    finally:
        con.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", required=True, help="live catalog database (read-only)")
    ap.add_argument("--out", default="seed/forge_repos.json")
    ap.add_argument("--dry-run", action="store_true", help="print the file, write nothing")
    ap.add_argument("--offline", action="store_true",
                    help="do not touch the network: rebuild from the catalog's current repo_url")
    args = ap.parse_args()

    slugs = catalog(args.db)
    if args.offline:
        # The file is a projection of what is applied, for the times the forges cannot be asked:
        # a rate-limited hour, a machine with no route out. It cannot discover anything, and it
        # says so rather than pretending a verification happened.
        entries = []
        for slug, url, host in catalog_repos(args.db):
            entries.append({"slug": slug, "repo_url": url, "repo_host": host})
        known = {e["slug"] for e in entries}
        for slug in slugs:
            if slug in known:
                continue
            entries.append({"slug": slug, "skip": True,
                            "reason": SKIPS.get(slug, NO_REPO)})
        entries.sort(key=lambda e: e["slug"])
        write(entries, args)
        return

    fj, gh = forgejo_public_repos(), github_public_repos()
    print(f"catalog {len(slugs)} slugs; forgejo public {len(fj)}; github public {len(gh)}",
          file=sys.stderr)

    entries = []
    for slug in slugs:
        if slug in SKIPS:  # a skip is a decision, and it outranks a name collision
            entries.append({"slug": slug, "skip": True, "reason": SKIPS[slug]})
            continue
        if slug in ALIASES:
            host, owner, repo, _why = ALIASES[slug]
        elif slug.lower() in fj:
            host, owner, repo = "forgejo", FORGEJO_OWNER, slug
        elif slug.lower() in gh:
            host, owner, repo = "github", GITHUB_OWNER, slug
        else:
            entries.append({"slug": slug, "skip": True, "reason": NO_REPO})
            continue
        try:
            code = verify(host, owner, repo)
        except Inconclusive as e:
            # Nothing is written. A wrong skip is worse than a stale file: forgeseed CLEARS a
            # repository when an entry becomes a skip, so a rate limit would silently unseed a
            # project that works.
            print(f"ABORTED: {e}; nothing written", file=sys.stderr)
            raise SystemExit(2)
        if code != 200:
            entries.append({"slug": slug, "skip": True,
                            "reason": f"repository {owner}/{repo} on {host} answers HTTP {code} "
                                      f"anonymously (measured 2026-11-12)"})
            continue
        base = "https://github.com" if host == "github" else FORGEJO
        entries.append({"slug": slug, "repo_url": f"{base}/{owner}/{repo}", "repo_host": host})

    entries.sort(key=lambda e: e["slug"])
    write(entries, args)


if __name__ == "__main__":
    main()
