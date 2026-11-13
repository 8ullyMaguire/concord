#!/usr/bin/env python3
"""Row 11: the two timer jobs, and the rules that make their output worth reading at 3am.

The jobs are `concord-cli statsnapshot` and `concord-cli proposalsweep` — the Go `cmd/statsnapshot` and
`cmd/proposalsweep` folded into the one binary, as the estate's shape asks. This probe runs them as the timer
would and checks what they wrote:

* **a dry run writes nothing.** `--dry-run` and the real pass must print the same set; only the second may leave
  a row. Measured by counting before and after.
* **the day's row agrees with the live page.** The snapshot's medians come from the same code `GET /api/v1/stats`
  answers from, so the two must agree — a snapshot that recomputed them would be a second answer to one question.
* **a sweep is idempotent.** A second pass finds nothing, because the UPDATE is guarded by `status = 'pending'`.
* **the expiry is attributed to NOBODY.** `audit_log.actor_id` is NULL: recording a person there would blame
  them for an automatic expiry.
* **one line on success**, and a TTL of zero disables the sweep rather than failing.
"""
import json
import os
import subprocess
import sys
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "tests", "e2e"))

BIN = os.environ.get("CONCORD_BIN", "/tmp/concord-cli")
CANDIDATE = os.environ.get("CANDIDATE", "http://127.0.0.1:8108")


def run(*args):
    result = subprocess.run([BIN, *args], capture_output=True, text=True, timeout=120)
    return result.returncode, result.stdout.strip(), result.stderr.strip()


def main():
    import harness

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    con = harness.connect("unused")

    # --- statsnapshot -------------------------------------------------------------------------
    code, before_out, err = run("statsnapshot", "--dry-run")
    check("the dry run exits 0", code, 0)
    check("the dry run prints one summary line", before_out.splitlines()[0].startswith("day="), True)
    check("and the project row count", before_out.splitlines()[-1].startswith("project rows="), True)
    count_dry = con.execute("select count(*) from instance_stats where day = to_char(current_date, 'YYYY-MM-DD')").fetchone()[0]
    check("the dry run wrote no row", count_dry, 0)

    code, real_out, err = run("statsnapshot")
    check("the real pass exits 0", code, 0)
    check("it reports what it recorded", real_out.startswith("recorded "), True)
    count_real = con.execute("select count(*) from instance_stats where day = to_char(current_date, 'YYYY-MM-DD')").fetchone()[0]
    check("the real pass wrote one row", count_real, 1)
    project_rows = con.execute(
        "select count(*) from project_stats where day = to_char(current_date, 'YYYY-MM-DD')"
    ).fetchone()[0]
    check("and the per-project series", project_rows > 0, True)

    # --- the snapshot agrees with the page ----------------------------------------------------
    with urllib.request.urlopen(f"{CANDIDATE}/api/v1/stats", timeout=20) as response:
        payload = json.load(response)
    row = con.execute(
        "select projects, projects_measured, features_total, complaints_total, triage_median_h,"
        " triage_n, delivery_median_h, delivery_n, decision_median_h, decision_n"
        " from instance_stats where day = to_char(current_date, 'YYYY-MM-DD')"
    ).fetchone()
    check("projects agrees with the live payload", row[0], payload["measured"]["projects"])
    check("features agrees", row[2], sum(f["total"] for f in payload["census"]["features_by_status"]))
    check("triage n agrees", row[5], payload["latency"]["triage"]["n"])
    check("delivery n agrees", row[7], payload["latency"]["delivery"]["n"])
    check("decision n agrees", row[9], payload["latency"]["decision"]["n"])

    # A second pass corrects the day rather than failing on a duplicate key.
    code, again, err = run("statsnapshot")
    check("a second pass is idempotent", code, 0)
    check("and there is still one row", con.execute(
        "select count(*) from instance_stats where day = to_char(current_date, 'YYYY-MM-DD')").fetchone()[0], 1)

    # --- proposalsweep ------------------------------------------------------------------------
    now = time.time()
    stamp = int(now * 1000) % 100000000
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"probe-s5-sweep-{stamp}", "Sweep probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    proposer = con.execute(
        "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?) RETURNING id",
        (f"sweepprobe{stamp}", "Sweep Probe", now),
    ).lastrowid
    old = now - 400 * 86400
    stale_id = con.execute(
        "INSERT INTO taxonomy_proposals (project_id, proposed_by, action, target_tag, value,"
        " rationale, status, created_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (project, proposer, "add", f"tag-{stamp}", "", "probe", "pending", old),
    ).lastrowid
    fresh_id = con.execute(
        "INSERT INTO taxonomy_proposals (project_id, proposed_by, action, target_tag, value,"
        " rationale, status, created_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        # A DIFFERENT tag, because `idx_taxonomy_one_pending` allows one pending proposal per
        # (project, action, target_tag, value) and two identical ones are refused by the schema.
        (project, proposer, "add", f"tag-fresh-{stamp}", "", "probe", "pending", now),
    ).lastrowid
    con.commit()

    audited_before = con.execute("select count(*) from audit_log").fetchone()[0]
    code, dry_out, err = run("proposalsweep", "--ttl-days", "30", "--dry-run")
    check("the sweep dry run exits 0", code, 0)
    check("it names the row it would mark", dry_out.splitlines()[0].startswith("taxonomy\tstale\t"), True)
    check("a dry run writes no act", con.execute("select count(*) from audit_log").fetchone()[0], audited_before)
    check("and leaves the proposal pending", con.execute(
        "select status from taxonomy_proposals where id = ?", (stale_id,)).fetchone()[0], "pending")

    code, sweep_out, err = run("proposalsweep", "--ttl-days", "30")
    check("the sweep exits 0", code, 0)
    check("with one line naming the count", sweep_out.endswith("1 proposal(s) older than 30 days as stale"), True)
    check("the old proposal is stale", con.execute(
        "select status from taxonomy_proposals where id = ?", (stale_id,)).fetchone()[0], "stale")
    check("the fresh one is not", con.execute(
        "select status from taxonomy_proposals where id = ?", (fresh_id,)).fetchone()[0], "pending")
    actor, action = con.execute(
        "select actor_id, action from audit_log where action = 'stale_taxonomy_proposal'"
        " order by id desc limit 1").fetchone()
    check("the expiry is attributed to nobody", actor, None)
    check("the act names the action", action, "stale_taxonomy_proposal")

    code, second, err = run("proposalsweep", "--ttl-days", "30")
    check("a second sweep is idempotent", code, 0)
    check("and finds nothing", second.endswith("0 proposal(s) older than 30 days as stale"), True)
    check("without a second act", con.execute(
        "select count(*) from audit_log where action = 'stale_taxonomy_proposal'").fetchone()[0], 1)

    code, disabled, err = run("proposalsweep", "--ttl-days", "0")
    check("a ttl of zero disables rather than fails", code, 0)
    check("and says so", "disabled" in disabled, True)

    # --- clean up -----------------------------------------------------------------------------
    con.execute("DELETE FROM audit_log WHERE action = 'stale_taxonomy_proposal'")
    con.execute("DELETE FROM taxonomy_proposals WHERE project_id = ?", (project,))
    con.execute("DELETE FROM projects WHERE id = ?", (project,))
    con.execute("DELETE FROM users WHERE id = ?", (proposer,))
    con.execute("DELETE FROM project_stats WHERE day = to_char(current_date, 'YYYY-MM-DD')")
    con.execute("DELETE FROM instance_stats WHERE day = to_char(current_date, 'YYYY-MM-DD')")
    con.commit()
    con.close()

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
