#!/usr/bin/env python3
"""Prove seed/dedupe.py --apply still works against the append-only vote log.

Migration 0024 made pairwise_votes append-only and opened exactly one escape
hatch for maintenance. The obvious way to ship that is to break dedupe.py: a
trigger that refuses a DELETE the repair script needs turns a maintenance script
into a crash. So this runs the REAL script -- not a re-implementation of what it
does -- against a database holding a real duplicate, and checks three things:

  1. report-only mode still reports and changes nothing
  2. --apply deletes the duplicate AND its votes, rather than aborting
  3. the maintenance signal is disarmed afterwards, so the hatch does not stay open

The live database is locked by the running service, so it is copied rather than
opened. The script's DB path is overridden by patching the module constant, so
the code under test is the file that ships.
"""
import importlib.util
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEDUPE = os.path.join(REPO, "seed", "dedupe.py")

failures = []


def check(label, cond, detail=""):
    print(f"  {'PASS' if cond else 'FAIL'}  {label}" + (f"  [{detail}]" if detail else ""))
    if not cond:
        failures.append(label)


def build_db(path, migrate_to=None):
    """A migrated database with one duplicated feature and its votes."""
    out = subprocess.run(
        ["go", "run", "./scripts/mkdb", path],
        cwd=REPO, capture_output=True, text=True,
    )
    return out


def scenario_apply(tmp):
    db = os.path.join(tmp, "apply.db")
    con = sqlite3.connect(db)
    con.executescript(open(os.path.join(REPO, "internal/db/migrations/0001_init.sql")).read())
    # Apply the rest of the migrations in order, the way the app does.
    mig_dir = os.path.join(REPO, "internal/db/migrations")
    for f in sorted(os.listdir(mig_dir)):
        if not f.endswith(".sql") or f == "0001_init.sql":
            continue
        try:
            con.executescript(open(os.path.join(mig_dir, f)).read())
        except sqlite3.Error as e:
            print(f"    (migration {f} failed to apply standalone: {e})")
            break
    con.commit()

    con.executescript("""
        INSERT INTO users (username, display_name, created_at) VALUES ('u1','U One',1);
        INSERT INTO projects (slug, name, governance_model, created_at, updated_at)
            VALUES ('p1','P One','collective',1,1);
        INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)
            VALUES ((SELECT id FROM projects WHERE slug='p1'), (SELECT id FROM users WHERE username='u1'),
                    'Duplicated Feature','body','draft',1,1);
    """)
    fid = con.execute("SELECT id FROM features WHERE title='Duplicated Feature'").fetchone()[0]
    con.executescript("""
        INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)
            VALUES ((SELECT id FROM projects WHERE slug='p1'), (SELECT id FROM users WHERE username='u1'),
                    'Duplicated Feature','body','draft',1,1);
    """)
    fid2 = con.execute(
        "SELECT id FROM features WHERE title='Duplicated Feature' AND id > ?", (fid,)).fetchone()[0]

    # A vote on the duplicate, which is what the trigger would refuse to delete.
    aid = con.execute("SELECT id FROM arenas WHERE project_id=(SELECT id FROM projects WHERE slug='p1') LIMIT 1")
    if aid.fetchone() is None:
        con.execute("INSERT INTO arenas (type, project_id, created_at) VALUES ('feature-priority',"
                    " (SELECT id FROM projects WHERE slug='p1'), 1)")
    aid = con.execute("SELECT id FROM arenas WHERE project_id=(SELECT id FROM projects WHERE slug='p1') LIMIT 1").fetchone()[0]
    other = con.execute("SELECT id FROM features WHERE id != ? AND id != ? LIMIT 1", (fid, fid2)).fetchone()
    if other is None:
        con.execute("INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
                    " VALUES ((SELECT id FROM projects WHERE slug='p1'), (SELECT id FROM users WHERE username='u1'),"
                    " 'Other','body','draft',1,1)")
        other = con.execute("SELECT id FROM features WHERE title='Other'").fetchone()
    oid = other[0]
    con.execute("""INSERT INTO pairwise_votes (project_id, arena_id, voter_id, feature_a, feature_b,
                     outcome, weight, created_at)
                  VALUES ((SELECT id FROM projects WHERE slug='p1'), ?,
                          (SELECT id FROM users WHERE username='u1'), ?, ?, 'a', 1.0, 1)""",
                (aid, fid2, oid))
    con.commit()
    votes = con.execute("SELECT count(*) FROM pairwise_votes").fetchone()[0]
    con.close()
    return db, votes


def run_dedupe(db, apply):
    """Run the REAL script with its DB path overridden."""
    code = (
        "import runpy, sys, importlib.util\n"
        f"spec = importlib.util.spec_from_file_location('dedupe', {DEDUPE!r})\n"
        "mod = importlib.util.module_from_spec(spec)\n"
        "spec.loader.exec_module(mod)\n"
        f"mod.DB = {db!r}\n"
        f"sys.argv = ['dedupe.py'] + (['--apply'] if {apply} else [])\n"
        "sys.exit(mod.main())\n"
    )
    return subprocess.run([sys.executable, "-c", code], cwd=REPO,
                          capture_output=True, text=True, timeout=300)


def main():
    with tempfile.TemporaryDirectory() as tmp:
        print("scenario: --apply against a real duplicate with votes")
        db, votes_before = scenario_apply(tmp)
        check("fixture has a vote to delete", votes_before > 0, f"{votes_before} vote(s)")

        print("\nscenario: report-only must not touch anything")
        r = run_dedupe(db, apply=False)
        con = sqlite3.connect(db)
        n_after_report = con.execute("SELECT count(*) FROM pairwise_votes").fetchone()[0]
        con.close()
        check("report mode exits 0", r.returncode == 0, r.stderr.strip()[:120])
        check("report mode deleted no votes", n_after_report == votes_before, f"{n_after_report} votes")

        print("\nscenario: --apply")
        r = run_dedupe(db, apply=True)
        out = r.stdout + r.stderr
        check("apply exits 0", r.returncode == 0, out.strip().splitlines()[-1][:140] if out.strip() else "")
        check("apply did not abort on the append-only trigger",
              "append-only" not in out, "trigger refused the delete" if "append-only" in out else "")
        con = sqlite3.connect(db)
        votes_after = con.execute("SELECT count(*) FROM pairwise_votes").fetchone()[0]
        feats = con.execute("SELECT count(*) FROM features WHERE title='Duplicated Feature'").fetchone()[0]
        try:
            armed = con.execute(
                "SELECT enabled FROM maintenance_signals WHERE name='seed_dedupe'").fetchone()
        except sqlite3.Error:
            armed = None
        con.close()
        check("the duplicate's votes were removed", votes_after < votes_before,
              f"{votes_before} -> {votes_after}")
        check("the duplicate feature was removed", feats == 1, f"{feats} remaining")
        check("the maintenance signal is disarmed afterwards",
              armed is None or armed[0] == 0, f"enabled={armed}")

        # And with the hatch closed, a further delete is refused.
        #
        # A VOTE MUST BE SEEDED FIRST. This check was vacuous as written: --apply
        # left the table empty, and a BEFORE DELETE trigger fires per row, so
        # `DELETE FROM pairwise_votes` over zero rows touches no row, fires no
        # trigger, and succeeds -- which the check read as "the table is not
        # protected" when it only means "there was nothing to protect". Deleting
        # nothing successfully is not a hole, but a test that cannot tell those
        # two apart is decoration, so the row comes first.
        print("\nscenario: after the run, the table is protected again")
        con = sqlite3.connect(db)
        cur_a = con.execute("SELECT id FROM arenas LIMIT 1").fetchone()[0]
        pid = con.execute("SELECT id FROM projects WHERE slug='p1'").fetchone()[0]
        uid = con.execute("SELECT id FROM users WHERE username='u1'").fetchone()[0]
        fids = [r[0] for r in con.execute(
            "SELECT id FROM features WHERE project_id=? ORDER BY id", (pid,)).fetchall()]
        if len(fids) < 2:
            con.execute("INSERT INTO features (project_id, author_id, title, body, status,"
                        " created_at, updated_at) VALUES (?,?,'Second','b','draft',1,1)",
                        (pid, uid))
            fids = [r[0] for r in con.execute(
                "SELECT id FROM features WHERE project_id=? ORDER BY id", (pid,)).fetchall()]
        con.execute("""INSERT INTO pairwise_votes (project_id, arena_id, voter_id, feature_a,
                         feature_b, outcome, weight, created_at)
                      VALUES (?,?,?,?,?, 'a', 1.0, 1)""",
                    (pid, cur_a, uid, fids[0], fids[1]))
        con.commit()
        before = con.execute("SELECT count(*) FROM pairwise_votes").fetchone()[0]
        check("a vote exists to protect", before > 0, f"{before} vote(s)")
        try:
            con.execute("DELETE FROM pairwise_votes")
            protected = False
        except sqlite3.Error as e:
            protected = True
            check("the refusal names the append-only rule",
                  "append-only" in str(e), str(e)[:80])
        con.close()
        check("a post-run DELETE is refused", protected)

    print()
    if failures:
        print(f"FAILED: {len(failures)} check(s): {failures}")
        return 1
    print("all checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())