#!/usr/bin/env python3
"""Make the PostgreSQL database say what the live service says, before the cutover publishes it.

# The problem this solves

The Go service has been the one serving `concord.polarisocial.xyz` while the port was proved against it, so the SQLite database it runs on
is the production truth and the PostgreSQL database the port reads is a *copy*, refreshed at a moment that is no longer now. Measured on
2026-11-14, immediately before the cutover:

    PostgreSQL   80 projects   638 users   5760 features   2406 complaints
    SQLite       73 projects    88 users   5760 features   2402 complaints

The difference is not drift to reconcile in both directions. Nobody signed up on the Go side after the copy was taken — **every SQLite user
already exists in PostgreSQL** (`comm -13` over the sorted usernames is empty) — and every extra row is probe litter: seven `probe-…`
projects and five hundred and fifty accounts named like `stdauth98278092`, created by the eight months of probes that ran against the
production database because that is where the interesting data was.

# What it does

1. Reads both databases and reports the drift per table, so the run has a before and an after rather than a claim.
2. Deletes the rows that are ONLY in PostgreSQL — projects whose slug the SQLite database does not have, users whose username it does not
   have — by walking the foreign-key graph from those roots, which is `purge_fixtures.purge` rather than a second deletion routine.
3. Reports what is still different. A leftover difference is printed and exits 1; it is never "close enough".

# What it refuses to do

It never writes to SQLite, and it never touches a row that exists in both. A row that is only in SQLite is a row the copy MISSED — that is
listed as drift and is not something this script can invent: publishing it needs the Go's own data, which means a re-copy, which is a
different job and a decision someone has to make.

# Usage

    DATABASE_URL=... python3 scripts/parity/reconcile_production.py \\
        --sqlite /home/alvaro/.local/share/concord/concord.db [--dry-run]
"""

import argparse
import os
import sys

import psycopg2

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from purge_fixtures import load_graph, purge  # noqa: E402 — sibling script, same directory

# The tables whose counts the two databases should agree on once the litter is gone. Not every table: the port owns tables the Go never
# had (`capability_confirmations`, the arena slot tables, `forge_events`), and comparing those would fail forever for a reason that is not a
# defect. These are the rows a reader can see.
COMPARED = ["projects", "users", "features", "complaints", "milestones", "comments", "votes"]


def sqlite_rows(path):
    import sqlite3

    connection = sqlite3.connect(path)
    try:
        return connection
    finally:
        connection.close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sqlite", required=True, help="the live service's database, read-only")
    parser.add_argument("--dry-run", action="store_true", help="report the rows it would delete, delete nothing")
    parser.add_argument("--allow-project-drift", action="store_true",
                        help="do not fail when SQLite has a project PostgreSQL lacks (a re-copy is a separate decision)")
    args = parser.parse_args()

    import sqlite3

    live = sqlite3.connect(f"file:{args.sqlite}?mode=ro", uri=True)
    pool = psycopg2.connect(os.environ["DATABASE_URL"])

    def counts(cur, sqlite_cursor):
        """Per table, the two counts — and `None` where a table does not exist, which is a fact rather than a zero.

        The Go's schema does not have every table the port does and does not call every table the same thing (`votes` is `feature_votes`
        there), so a hard-coded list would either crash on the live side or compare a table against nothing and call the difference drift.
        A missing table prints as `-` and is skipped by the drift check.
        """
        out = {}
        for table in COMPARED:
            try:
                sqlite_cursor.execute(f"select count(*) from {table}")
                sqlite_count = sqlite_cursor.fetchone()[0]
            except sqlite3.OperationalError:
                sqlite_count = None
            try:
                cur.execute(f"select count(*) from {table}")
                pg = cur.fetchone()[0]
            except psycopg2.errors.UndefinedTable:
                pool.rollback()
                pg = None
            out[table] = (pg, sqlite_count)
        return out

    def show(counts_table):
        print(f"{'table':<14}{'postgres':>10}{'sqlite':>10}{'delta':>8}")
        for table, (pg, sq) in counts_table.items():
            delta = "-" if pg is None or sq is None else f"{pg - sq}"
            print(f"{table:<14}{'-' if pg is None else pg:>10}{'-' if sq is None else sq:>10}{delta:>8}")

    with pool.cursor() as cur:
        live_cursor = live.cursor()
        before = counts(cur, live_cursor)
        show(before)

        # ── what is only in PostgreSQL ───────────────────────────────────────────────────────────────────────────────────────────
        live_cursor.execute("select slug from projects")
        live_slugs = {row[0] for row in live_cursor.fetchall()}
        cur.execute("select id, slug from projects")
        extra_projects = [(row[0], row[1]) for row in cur.fetchall() if row[1] not in live_slugs]

        live_cursor.execute("select username from users")
        live_users = {row[0] for row in live_cursor.fetchall()}
        cur.execute("select id, username from users")
        extra_users = [(row[0], row[1]) for row in cur.fetchall() if row[1] not in live_users]

        print(f"\nprojects only in postgres: {len(extra_projects)}")
        for _, slug in sorted(extra_projects)[:12]:
            print(f"  {slug}")
        print(f"users only in postgres: {len(extra_users)}")

        # ── what is only in the live database: NOT ours to invent ────────────────────────────────────────────────────────────────
        cur.execute("select slug from projects")
        pg_slugs = {row[0] for row in cur.fetchall()}
        cur.execute("select username from users")
        pg_users = {row[0] for row in cur.fetchall()}
        missing_projects = sorted(live_slugs - pg_slugs)
        missing_users = sorted(live_users - pg_users)
        print(f"projects only in sqlite (NOT deletable, NOT inventable): {len(missing_projects)}")
        for slug in missing_projects[:12]:
            print(f"  {slug}")
        print(f"users only in sqlite: {len(missing_users)}")

        if extra_projects or extra_users:
            graph, has_id = load_graph(cur)
            roots = {
                "projects": [row[0] for row in extra_projects],
                "users": [row[0] for row in extra_users],
            }
            counts_deleted = purge(cur, roots, graph, has_id, dry_run=args.dry_run)
            label = "would delete" if args.dry_run else "deleted"
            print(f"\n{label} {sum(counts_deleted.values())} rows across {len(counts_deleted)} tables")
            for table, rowcount in sorted(counts_deleted.items()):
                print(f"  {table:<28}{rowcount:>7}")
            if not args.dry_run:
                pool.commit()
        else:
            print("\nnothing to delete: postgres has no row the live service does not")

    if args.dry_run:
        return 0

    with pool.cursor() as cur:
        live_cursor = live.cursor()
        after = counts(cur, live_cursor)
        print()
        show(after)
        drift = sum(1 for pg, sq in after.values() if pg is not None and sq is not None and pg != sq)
        live.close()
        if missing_projects and not args.allow_project_drift:
            print(f"\n{len(missing_projects)} project(s) exist only in the live database: a re-copy is needed before cutover.")
            return 1
        if drift:
            print(f"\n{drift} table(s) still disagree. That is not 'close enough' — see the numbers above.")
            return 1
        print("\nthe two databases agree on every compared table")
    return 0


if __name__ == "__main__":
    sys.exit(main())
