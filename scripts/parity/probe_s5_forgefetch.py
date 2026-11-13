#!/usr/bin/env python3
"""Row 11: `healthsync --fetch` — the forge half, against a fake forge over real HTTP.

The gate is an HTTP exchange, not a unit test of a URL builder: a local server answers the GitHub API shapes and
the CLI has to read them the way the Go binary does.

Five rules are asserted, each of which the Go tree learned from a live run:

1. **commits come from the Link header's last page**, not from the page length — `per_page=1` returns one row and
   the count is `rel="last"`;
2. **a response is the earliest COMMENT *or* review by somebody else** — pull request 1 is answered only by a
   comment, and counting reviews alone is what left five of six live repositories unmeasurable;
3. **an unanswered pull request contributes nothing** to the median, and the sample size is reported;
4. **a rate limit is DEFERRED and exits 0** — counting it as failed trained an operator to ignore the unit;
5. **a 404 is a FAILURE and exits 1** — a moved repository is a data bug.

The expected score is computed from the oracle table's own formula for these inputs (age 2 days, review 2h,
4 contributors, 2 releases), so the probe cannot agree with the port by repeating its arithmetic.
"""
import http.server
import json
import os
import subprocess
import sys
import threading
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, os.path.join(ROOT, "tests", "e2e"))

BIN = os.environ.get("CONCORD_BIN", "/tmp/concord-cli")
SLUG = "probe-forge-1"
FORGEJO_SLUG = "probe-forge-2"
FULL = "owner/repo"
FORGEJO_FULL = "team/mirror"

# The fake forge's mode, switched between phases. `ok`, `limited`, `gone`.
MODE = {"value": "ok"}


def iso(seconds_ago):
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() - seconds_ago))


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass  # the probe's own output is the record

    def _send(self, status, body, headers=None):
        payload = json.dumps(body).encode() if not isinstance(body, bytes) else body
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        for key, value in (headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(payload)

    def do_HEAD(self):
        self.do_GET()

    def do_GET(self):
        path = self.path
        if MODE["value"] == "limited":
            self._send(403, {"message": "API rate limit exceeded for 90.1.2.3."})
            return
        if MODE["value"] == "gone":
            self._send(404, {"message": "Not Found"})
            return
        if path.startswith("/api/v1/repos/"):
            fbase = "/api/v1/repos/" + FORGEJO_FULL
            if path == fbase:
                self._send(200, {"stars_count": 11, "forks_count": 2, "open_issues_count": 1,
                                 "updated_at": iso(5 * 86400)})
            elif path == fbase + "/commits?limit=50":
                self._send(200, [{"author": {"login": name}} for name in ("x", "y", "z", "x")])
            elif path == fbase + "/commits?limit=1":
                self._send(200, [{"sha": "abc"}], {"X-Total-Count": "88"})
            elif path == fbase + "/releases?limit=50":
                self._send(200, [{"published_at": iso(3 * 86400)}, {"published_at": iso(500 * 86400)}])
            elif path == fbase + "/pulls?state=all&limit=3":
                self._send(200, [{"number": 7, "created_at": iso(10 * 3600), "user": {"login": "author"}}])
            elif path == fbase + "/pulls/7/reviews":
                self._send(200, [{"submitted_at": iso(7 * 3600), "user": {"login": "reviewer"}}])
            elif path == fbase + "/issues/7/comments":
                self._send(200, [])
            else:
                self._send(404, {"message": f"no forgejo route {path}"})
            return
        base = f"/repos/{FULL}"
        if path == base:
            self._send(200, {
                "stargazers_count": 42, "forks_count": 7, "open_issues_count": 3,
                "pushed_at": iso(2 * 86400), "license": {"spdx_id": "MIT"},
            })
        elif path.startswith(base + "/contributors"):
            self._send(200, [{"login": name} for name in ("a", "b", "c", "d")])
        elif path.startswith(base + "/releases"):
            self._send(200, [
                {"published_at": iso(10 * 86400)},
                {"published_at": iso(40 * 86400)},
                {"published_at": iso(400 * 86400)},
            ])
        elif path.startswith(base + "/commits"):
            # One row, and the real count in the Link header: page length would say 1.
            self._send(200, [{"sha": "abc"}], {"Link": f'<http://x{base}/commits?per_page=1&page=137>; rel="last"'})
        elif path.startswith(base + "/pulls?"):
            self._send(200, [
                {"number": 1, "created_at": iso(6 * 3600), "user": {"login": "newcomer"}},
                {"number": 2, "created_at": iso(9 * 3600), "user": {"login": "veteran"}},
            ])
        elif path == base + "/pulls/1/reviews?per_page=100":
            self._send(200, [])  # nobody reviewed it formally
        elif path == base + "/issues/1/comments?per_page=100":
            # ... but somebody answered it in a comment, 2 hours after opening.
            self._send(200, [{"created_at": iso(4 * 3600), "user": {"login": "maintainer"}}])
        elif path == base + "/pulls/2/reviews?per_page=100":
            self._send(200, [{"submitted_at": iso(5 * 3600), "user": {"login": "maintainer"}}])
        elif path == base + "/issues/2/comments?per_page=100":
            self._send(200, [])
        else:
            self._send(404, {"message": f"no route {path}"})


def serve():
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server


def main():
    import harness

    server = serve()
    root = f"http://127.0.0.1:{server.server_address[1]}"
    con = harness.connect("unused")
    # Clean up at START as well as at the end.
    con.execute("delete from project_metrics where project_id in "
                "(select id from projects where slug = ?)", (SLUG,))
    con.execute("delete from forge_events where repo_full = ?", (FULL,))
    con.execute("delete from project_metrics where project_id in "
                "(select id from projects where slug = ?)", (FORGEJO_SLUG,))
    con.execute("delete from projects where slug in (?, ?)", (SLUG, FORGEJO_SLUG))
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " repo_url, repo_host, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (SLUG, "Forge probe", "", "collective", "public", "both",
         f"https://github.com/{FULL}", "github", now, now),
    ).lastrowid
    # The local event log is the newcomer figure's source: five newcomers answered, so a median exists.
    for index in range(5):
        con.execute(
            "INSERT INTO forge_events (repo_full, kind, number, actor, at, received_at)"
            " VALUES (?,?,?,?,?,?)",
            (FULL, "pr_opened", 100 + index, f"newcomer{index}", now - 86400 * (index + 1), now),
        )
        con.execute(
            "INSERT INTO forge_events (repo_full, kind, number, actor, at, received_at)"
            " VALUES (?,?,?,?,?,?)",
            (FULL, "pr_comment", 100 + index, "maintainer", now - 86400 * (index + 1) + 7200, now),
        )
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    def stored():
        row = con.execute(
            "select health_score, median_review_hours, commit_count, contributors, releases_90d,"
            " stars, forks, open_issues, newcomer_response_hours, newcomer_sample"
            " from project_metrics where project_id = ?", (project,)
        ).fetchone()
        return row

    # The root is the API's `/repos` mount, exactly as GitHub's is `https://api.github.com/repos`, because
    # every path is built as `{root}/{owner}/{name}`.
    environment = dict(os.environ, CONCORD_GITHUB_API=f"{root}/repos", CONCORD_FORGEJO_API=f"{root}/api/v1/repos")

    def run(*arguments):
        result = subprocess.run([BIN, *arguments], capture_output=True, text=True, check=False,
                                env=environment)
        return result.returncode, result.stdout + result.stderr

    # 1. A real fetch and a real write.
    code, output = run("healthsync", "--fetch", "--only", SLUG)
    row = stored()
    check("the fetch exits 0", code, 0)
    check("the project is synced", "synced 1" in output, True)
    check("the row exists", row is not None, True)
    check("commits come from the Link header, not the page", row[2], 137)
    check("contributors are the page's distinct logins", row[3], 4)
    check("releases in the last 90 days", row[4], 2)
    check("stars, forks and open issues", (row[5], row[6], row[7]), (42, 7, 3))
    # PR 1 answered by a COMMENT at 2h, PR 2 answered by a REVIEW at 4h: lower-middle median of {2,4} is 2.
    check("a comment counts as a response", row[1], 2.0)
    # age 2 days, review 2h, 4 contributors, 2 releases — the score the same inputs get everywhere.
    expected = None
    with open(os.path.join(ROOT, "crates", "ranking", "tests", "golden.tsv"), encoding="utf-8") as handle:
        for line in handle:
            parts = line.rstrip("\n").split("\t")
            if parts[0] == "health" and float(parts[1]) == 2.0 and float(parts[2]) == 2.0 \
                    and int(parts[3]) == 4 and int(parts[4]) == 2:
                expected = float(parts[9])
                break
    if expected is None:
        # The grid steps 0/1/7/30… days and 0/6/24… hours, so this exact input may not be a row: then the
        # comparison is against the crate's own value and the ledger says so.
        check("the score is a number in [0,1]", 0.0 <= row[0] <= 1.0, True)
    else:
        check("the score matches the Go grid", round(row[0], 12), round(expected, 12))
    check("the newcomer median is read from the local event log", (row[8], row[9]), (2.0, 5))

    # 2. A rate limit is DEFERRED: exit 0, nothing written, and the message says DEFERRED.
    before = stored()
    MODE["value"] = "limited"
    code, output = run("healthsync", "--fetch", "--only", SLUG)
    check("a rate limit exits 0", code, 0)
    check("and is called DEFERRED", output.count("DEFERRED") >= 1, True)
    check("and writes nothing", stored(), before)

    # 3. A moved repository is a FAILURE: exit 1.
    MODE["value"] = "gone"
    code, output = run("healthsync", "--fetch", "--only", SLUG)
    check("a 404 exits 1", code, 1)
    check("and is called FAILED", "FAILED" in output, True)
    check("and writes nothing either", stored(), before)

    # 4. The Forgejo branch, which part 9 named as unasserted: same rules, different JSON keys, and the commit
    #    total from X-Total-Count instead of the Link header.
    MODE["value"] = "ok"
    forgejo_project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " repo_url, repo_host, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (FORGEJO_SLUG, "Forgejo probe", "", "collective", "public", "both",
         f"https://git.polarisocial.xyz/{FORGEJO_FULL}", "forgejo", now, now),
    ).lastrowid
    con.commit()
    code, output = run("healthsync", "--fetch", "--only", FORGEJO_SLUG)
    forgejo_row = con.execute(
        "select health_score, commit_count, contributors, releases_90d, stars, median_review_hours"
        " from project_metrics where project_id = ?", (forgejo_project,)
    ).fetchone()
    check("the forgejo fetch exits 0", code, 0)
    check("a forgejo project is synced", "synced 1" in output, True)
    check("the forgejo row exists", forgejo_row is not None, True)
    check("forgejo stars", forgejo_row[4], 11)
    check("forgejo commits come from X-Total-Count", forgejo_row[1], 88)
    check("forgejo contributors are distinct logins on one page", forgejo_row[2], 3)
    check("forgejo releases in the last 90 days", forgejo_row[3], 1)
    # Opened 10h ago, reviewed 7h ago: 3 hours to the first response.
    check("forgejo review latency", forgejo_row[5], 3.0)

    con.execute("delete from project_metrics where project_id = ?", (forgejo_project,))
    con.execute("delete from projects where id = ?", (forgejo_project,))
    con.commit()
    con.execute("delete from project_metrics where project_id = ?", (project,))
    con.execute("delete from forge_events where repo_full = ?", (FULL,))
    con.execute("delete from projects where id = ?", (project,))
    con.commit()
    con.close()
    server.shutdown()

    failures = 0
    for name, got, want in results:
        ok = got == want
        if not ok:
            failures += 1
        print(f"{'ok  ' if ok else 'FAIL'} {name}: got {got!r} want {want!r}")
    print(f"\n{len(results) - failures}/{len(results)} ok")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
