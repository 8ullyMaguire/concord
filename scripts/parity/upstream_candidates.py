#!/usr/bin/env python3
"""State a project's upstream repository from the checkout that already knows it.

68 of this catalogue's projects have no `repo_url` — the Go tree's seed file skipped them with "not stated (Q5)" — and that is what
keeps `mature`/`dormant` unreachable for them and the adoption pool empty. Stating a repository by guessing would put a stranger's
repository on a project's page, so this script does not guess: it reads the `origin` remote of a local checkout and accepts the match
only when

  * the remote's host is one the owner controls (`git.polarisocial.xyz`, the LAN Forgejo, `git.disroot.org/hirrolot19`,
    `github.com/8ullyMaguire`), and
  * the repository name equals the project slug after normalisation — lowercase, `.git` stripped, `_`→`-`, and one trailing
    `-rs`/`-go`/`-js`/`-py`/`-app`/`-site` removed from EITHER side.

A checkout whose name merely *starts with* the slug is not a match: `suki-gate`, `suki-m215` and `suki-wt-tmp` are worktrees of the
same repository, and matching on prefixes would say three projects have an upstream when one does. When several checkouts share a
repository, the one whose normalised name equals the slug wins; otherwise the shortest path wins and the rest are printed as ties.

    python3 scripts/parity/upstream_candidates.py                  # report only
    python3 scripts/parity/upstream_candidates.py --write          # write a complete seed/forge_repos.json
    python3 scripts/parity/upstream_candidates.py --write --out seed/forge_repos.proposed.json

The file it writes lists EVERY project, because `forgeseed` refuses a file that does not account for the catalogue: a stated project
gets its URL and host, and every other project gets `"skip": true` with the reason measured here rather than invented.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

CODE_ROOT = Path(os.environ.get("CONCORD_CODE_ROOT", "/personal/documents/code"))
# A host appears here only when `forge::KNOWN_HOSTS` also knows it: a URL this script states and the ingest
# refuses is worse than no URL, because the project then looks measured-from-a-repository and is not. The LAN
# Forgejo at `192.168.1.13:8002` is deliberately absent for that reason — it is not a public address, so a
# stranger reading the project's page cannot reach it and the ingest answers "not a known forge".
OWNED_HOSTS = {
    "git.polarisocial.xyz",
    "git.disroot.org",
    "github.com",
}
OWNED_OWNERS = {"hirrolot19", "8ullyMaguire", "8ullymaguire"}
SUFFIXES = ("-rs", "-go", "-js", "-py", "-app", "-site")
MAX_DEPTH = 3


def normalise(name: str) -> str:
    name = name.strip().lower()
    if name.endswith(".git"):
        name = name[:-4]
    name = name.replace("_", "-").replace(" ", "-")
    for suffix in SUFFIXES:
        if name.endswith(suffix) and len(name) > len(suffix):
            name = name[: -len(suffix)]
            break
    return re.sub(r"-+", "-", name).strip("-")


def git_remote(path: Path) -> str | None:
    try:
        out = subprocess.run(
            ["git", "-C", str(path), "remote", "get-url", "origin"],
            capture_output=True,
            text=True,
            timeout=10,
        )
    except (subprocess.SubprocessError, OSError):
        return None
    url = out.stdout.strip()
    return url or None


def classify(url: str) -> tuple[str, str, str] | None:
    """Return (host, owner, repo) when the remote is one the owner controls, else None."""
    if url.startswith("git@") and ":" in url:
        host_and_path = url[4:]
        host, path = host_and_path.split(":", 1)
    else:
        parsed = urllib.parse.urlparse(url)
        host = parsed.netloc.rsplit("@", 1)[-1]
        path = parsed.path
    parts = [p for p in path.strip("/").split("/") if p]
    if len(parts) < 2 or host not in OWNED_HOSTS:
        return None
    owner, repo = parts[-2], parts[-1]
    if owner.lower() not in OWNED_OWNERS:
        return None
    if repo.endswith(".git"):
        repo = repo[:-4]
    return host, owner, repo


def checkouts(root: Path) -> list[Path]:
    found: list[Path] = []
    root = root.resolve()
    base_depth = len(root.parts)
    for dirpath, dirnames, _ in os.walk(root):
        path = Path(dirpath)
        depth = len(path.parts) - base_depth
        if depth > MAX_DEPTH:
            dirnames[:] = []
            continue
        dirnames[:] = [d for d in dirnames if d != ".git" and not d.startswith("target")]
        if (path / ".git").exists():
            found.append(path)
            dirnames[:] = []  # a checkout's subdirectories are not checkouts of their own
    return found


def catalogue(dsn: str) -> list[tuple[str, str | None]]:
    """slug, repo_url for every project, straight from the database rather than from a document."""
    try:
        import psycopg  # type: ignore
    except ImportError:
        psycopg = None  # type: ignore
    if psycopg is not None:
        with psycopg.connect(dsn) as con, con.cursor() as cur:
            cur.execute("select slug, repo_url from projects order by slug")
            return [(row[0], row[1]) for row in cur.fetchall()]
    try:
        import psycopg2  # type: ignore
    except ImportError:
        sys.exit("neither psycopg nor psycopg2 is installed; ~/.venvs/concord-e2e has psycopg2-binary")
    con = psycopg2.connect(dsn)
    try:
        with con.cursor() as cur:
            cur.execute("select slug, repo_url from projects order by slug")
            return [(row[0], row[1]) for row in cur.fetchall()]
    finally:
        con.close()


def repository_exists(url: str) -> bool | None:
    """True when the forge answers for this repository, False when it says 404, None when it cannot be asked.

    "The checkout has an origin remote" is not the same claim as "the repository exists": three of this
    catalogue's own checkouts push to a repository the forge no longer has, and the ingest retried those every
    hour and failed every hour. Only a 404 is a negative answer; a timeout is not, because a forge that is down
    (as git.disroot.org was on 2026-11-15 after its storage server failed) must keep its URLs, since the URL is
    still true and only the vendor is unwell.
    """
    parsed = urllib.parse.urlparse(url)
    host = parsed.netloc
    path = parsed.path.strip("/")
    if path.endswith(".git"):
        path = path[:-4]
    parts = [part for part in path.split("/") if part]
    if len(parts) < 2:
        return None
    api = (
        f"https://api.github.com/repos/{parts[-2]}/{parts[-1]}"
        if host == "github.com"
        else f"https://{host}/api/v1/repos/{parts[-2]}/{parts[-1]}"
    )
    request = urllib.request.Request(
        api, headers={"User-Agent": "concord-upstream-candidates", "Accept": "application/json"}
    )
    try:
        with urllib.request.urlopen(request, timeout=20) as response:  # noqa: S310 - an https URL built above
            return response.status == 200
    except urllib.error.HTTPError as error:
        return False if error.code == 404 else None
    except (urllib.error.URLError, TimeoutError, OSError):
        return None


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--dsn", default=os.environ.get("CONCORD_E2E_DSN") or os.environ.get("DATABASE_URL"))
    ap.add_argument("--root", default=str(CODE_ROOT))
    ap.add_argument("--write", action="store_true")
    ap.add_argument("--out", default="seed/forge_repos.proposed.json")
    ap.add_argument("--verbose", action="store_true")
    ap.add_argument(
        "--verify",
        action="store_true",
        help="ask each forge whether the repository exists; a 404 becomes a skip with the measurement as its reason",
    )
    args = ap.parse_args()
    if not args.dsn:
        sys.exit("no DSN: set CONCORD_E2E_DSN or DATABASE_URL")

    rows = catalogue(args.dsn)
    stated = {slug for slug, url in rows if url}
    unstated = [slug for slug, url in rows if not url]

    by_repo: dict[str, list[Path]] = {}
    for path in checkouts(Path(args.root)):
        url = git_remote(path)
        if not url:
            continue
        hit = classify(url)
        if not hit:
            continue
        host, owner, repo = hit
        by_repo.setdefault(f"{host}/{owner}/{repo}", []).append(path)

    found: dict[str, dict] = {}
    for slug in unstated:
        want = normalise(slug)
        matches = []
        for key, paths in by_repo.items():
            repo_name = key.rsplit("/", 1)[-1]
            if normalise(repo_name) == want:
                matches.append((key, paths))
        if not matches:
            continue
        # One repository per slug: prefer a checkout whose own normalised name equals the slug, then the shortest path.
        matches.sort(key=lambda item: (normalise(item[1][0].name) != want, min(len(str(p)) for p in item[1])))
        key, paths = matches[0]
        host, owner, repo = key.split("/", 2)
        path = min(paths, key=lambda p: len(str(p)))
        found[slug] = {
            "slug": slug,
            "repo_url": f"https://{host}/{owner}/{repo}",
            "repo_host": host,
            "evidence": {
                "checkout": str(path),
                "remote": f"{key}",
                "ties": [str(p) for p in paths if p != path],
            },
        }

    print(f"catalogue: {len(rows)} projects · stated: {len(stated)} · unstated: {len(unstated)}")
    print(f"checkouts scanned under {args.root}: {sum(len(p) for p in by_repo.values())} owned remotes")
    print(f"stated by measurement: {len(found)} of {len(unstated)} unstated")
    for slug in sorted(found):
        item = found[slug]
        ties = f"  (also: {', '.join(item['evidence']['ties'])})" if item["evidence"]["ties"] else ""
        print(f"  {slug:<28} {item['repo_host']:<22} {item['evidence']['checkout']}{ties}")
    if args.verbose:
        print("\nunstated with no matching checkout:")
        for slug in unstated:
            if slug not in found:
                print(f"  {slug}")

    if not args.write:
        return 0

    out = Path(args.out)
    entries = []
    for slug, url in rows:
        if url:
            host = urllib.parse.urlparse(url).netloc.rsplit("@", 1)[-1]
            entries.append({"slug": slug, "repo_url": url, "repo_host": host})
        elif slug in found:
            entries.append({k: v for k, v in found[slug].items() if k != "evidence"})
        else:
            entries.append(
                {
                    "slug": slug,
                    "skip": True,
                    "reason": "no checkout under the code root has an origin remote this owner controls "
                    "(measured 2026-11-15 by scripts/parity/upstream_candidates.py); stating it is the owner's answer to give",
                }
            )
    if args.verify:
        verified = []
        for entry in entries:
            if entry.get("repo_url"):
                exists = repository_exists(entry["repo_url"])
                if exists is False:
                    print(f"  GONE  {entry['slug']:<28} {entry['repo_host']:<22} answers 404")
                    entry = {
                        "slug": entry["slug"],
                        "skip": True,
                        "reason": "the checkout's origin names a repository the forge answers 404 for and no other "
                        "host in the owner's set has it (measured by scripts/parity/upstream_candidates.py --verify); "
                        "a repository that does not exist is not an upstream",
                    }
                elif exists is None:
                    print(f"  UNASKED {entry['slug']:<27} {entry['repo_host']:<22} kept stated: the forge did not answer")
            verified.append(entry)
        entries = verified

    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(entries, indent=2) + "\n")
    stated_now = sum(1 for entry in entries if not entry.get("skip"))
    print(
        f"\nwrote {out}: {len(entries)} entries ({stated_now} stated, {len(entries) - stated_now} skipped)"
        + (f" · {len(entries) - len(stated) - len(found)} newly stated by measurement" if found else "")
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
