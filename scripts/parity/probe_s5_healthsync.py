#!/usr/bin/env python3
"""Row 11: `concord-cli healthsync` — the write half of the health sweep.

The score itself is proven against the Go implementation by `crates/ranking/tests/health_golden.rs` (1,344 rows,
8,087 values). What this probe proves is the WIRING, and the three rules that only exist in the write:

* **an unmeasured project writes NOTHING** — not zeros, not a fresh `computed_at` over stale numbers, because a
  row that says "computed two minutes ago" while every number in it is from last week hides that it is stale;
* **an unknown review time is `NULL`, never `-1`**, since the column is `REAL` and every other reader treats a
  number as a measurement;
* **a repository with no commit gets `NULL` for `last_commit_at`**, for the same reason.

The expected score is read out of `crates/ranking/tests/golden.tsv` — a number the GO implementation printed —
rather than recomputed here, so the probe cannot agree with the port by making the same mistake twice.
"""
import json
import os
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, os.path.join(ROOT, "tests", "e2e"))

BIN = os.environ.get("CONCORD_BIN", "/tmp/concord-cli")
SLUG = "probe-health-1"


def oracle_health(age, review, contributors, releases):
    """The Go total for these inputs, read from the oracle table the ranking tests use."""
    path = os.path.join(ROOT, "crates", "ranking", "tests", "golden.tsv")
    with open(path, encoding="utf-8") as handle:
        for line in handle:
            parts = line.rstrip("\n").split("\t")
            if parts[0] != "health":
                continue
            if (
                float(parts[1]) == float(age)
                and float(parts[2]) == float(review)
                and int(parts[3]) == contributors
                and int(parts[4]) == releases
            ):
                return float(parts[9]), parts[10] == "true"
    raise SystemExit(f"the oracle has no health row for {age}/{review}/{contributors}/{releases}")


def run(*arguments):
    environment = dict(os.environ)
    result = subprocess.run(
        [BIN, *arguments], capture_output=True, text=True, check=False, env=environment
    )
    if result.returncode != 0:
        print(f"FAIL healthsync {arguments} exited {result.returncode}\n{result.stdout}{result.stderr}")
        sys.exit(2)
    return result.stdout


def dump(entries):
    path = os.path.join(os.environ.get("TMPDIR", "/tmp"), f"healthsync-{os.getpid()}.json")
    with open(path, "w", encoding="utf-8") as handle:
        json.dump(entries, handle)
    return path


def main():
    import harness

    con = harness.connect("unused")
    # Clean up at START as well as at the end: an interrupted run must not poison the next gate.
    con.execute("delete from project_metrics where project_id in "
                "(select id from projects where slug = ?)", (SLUG,))
    # `board_columns` is created with the project and has a real foreign key to it, so the project cannot be
    # deleted while its board exists. A cleanup that cannot remove its own fixture breaks the NEXT run.
    con.execute("delete from board_cards where column_id in (select id from board_columns where project_id in (select id from projects where slug = ?))", (SLUG,))
    con.execute("delete from board_columns where project_id in (select id from projects where slug = ?)", (SLUG,))
    con.execute("delete from projects where slug = ?", (SLUG,))
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (SLUG, "Health probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    def stored():
        row = con.execute(
            "select health_score, median_review_hours,"
            " extract(epoch from last_commit_at)::double precision,"
            " extract(epoch from computed_at)::double precision, newcomer_response_hours, newcomer_sample"
            " from project_metrics where project_id = ?", (project,)
        ).fetchone()
        return row

    # 1. A measured project is written, with the score the GO implementation gives for the same inputs.
    #    review = -1 is the "nothing measured" sentinel, so the responsiveness component is excluded and the
    #    total is renormalised over the other three.
    want_total, want_known = oracle_health(30, -1, 2, 0)
    path = dump([{
        "slug": SLUG, "measured": True, "stars": 12, "forks": 3, "open_issues": 1,
        "commit_count": 400, "contributors": 2, "last_commit_age_days": 30,
        "median_review_hours": -1, "releases_90d": 0, "license": "MIT",
    }])
    run("healthsync", "--from", path)
    row = stored()
    check("a measured project is written", row is not None, True)
    check("the score is the Go implementation's", round(row[0], 12), round(want_total, 12))
    check("the unknown review time is NULL, not -1", row[1], None)
    check("the responsiveness flag agrees with the oracle", want_known, False)
    check("last_commit_at is 30 days back", round((now - row[2]) / 86400.0), 30)
    check("the licence is written to the project", con.execute(
        "select license from projects where id = ?", (project,)).fetchone()[0], "MIT")

    # 2. A project the forge did not answer for writes NOTHING — the old score, the old computed_at, no new row.
    before = stored()
    time.sleep(1.1)
    path = dump([{"slug": SLUG, "measured": False, "stars": 9999}])
    output = run("healthsync", "--from", path)
    after = stored()
    check("an unmeasured project writes nothing", before, after)
    check("and says so", "the forge did not answer; nothing written" in output, True)
    check("and counts it", "1 unmeasured" in output, True)

    # 3. A repository with no commit at all: NULL commit time, and no invented age.
    path = dump([{
        "slug": SLUG, "measured": True, "stars": 12, "forks": 3, "open_issues": 1,
        "commit_count": 0, "contributors": 2, "median_review_hours": 6, "releases_90d": 0,
    }])
    run("healthsync", "--from", path)
    row = stored()
    check("a repository with no commit gets NULL", row[2], None)
    check("and a measured review time is written", row[1], 6.0)

    # 4. The newcomer columns are one fact: hours and the sample, written together or not at all.
    run("healthsync", "--newcomer", f"{SLUG}=3.0,5")
    row = stored()
    check("the newcomer median is written", (row[4], row[5]), (3.0, 5))
    # Below the floor there is no number: NULL hours over an empty sample. `newcomer_sample` is NOT NULL in the
    # schema the Go tree created, so the "no number" state is (NULL, 0) and not (NULL, NULL).
    run("healthsync", "--newcomer", f"{SLUG}=none")
    row = stored()
    check("below the sample floor the hours are NULL and the sample is 0", (row[4], row[5]), (None, 0))

    # 5. A slug nobody owns is skipped and counted, not invented as a project.
    path = dump([{"slug": "nope-not-a-project", "measured": True, "stars": 1}])
    output = run("healthsync", "--from", path)
    check("an unknown slug is skipped", "no such project" in output, True)
    check("and no project is created", con.execute(
        "select count(*) from projects where slug = ?", ("nope-not-a-project",)).fetchone()[0], 0)

    os.unlink(path)
    con.execute("delete from project_metrics where project_id = ?", (project,))
    con.execute("delete from projects where id = ?", (project,))
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
