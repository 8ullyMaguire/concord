#!/usr/bin/env python3
"""Remove every probe fixture project and everything hanging off it.

WHY THIS EXISTS. `clean_project.py` derives the correct child-before-parent order, but issues one statement per root project per table —
thousands of round trips over 120 fixtures on this instance, and it did not finish. This does the same job for the whole set in a handful of
queries.

Two phases, both driven by the foreign-key graph read from `pg_constraint` once:

1. **Collect.** Starting from the fixture project ids, take each table's OWN ids (its `id` column) from the rows just identified. For every
   foreign key into that table, the child's ids are the rows whose column matches. Repeat until nothing new appears. Keying by the table's
   own primary key — not by the referencing column — is what makes the walk transitive: a table reached through `project_id` can be a parent
   itself, through `arena_id` or `complaint_id`, and its ids are what its children need.

2. **Delete.** For each table, delete `where id = any(own ids)`; for a table with no `id` column, `where <column> = any(parent ids)`. Order is
   found at runtime rather than computed: a delete blocked by a foreign key is rolled back to a savepoint and retried on the next round. That
   is a few extra round trips and it cannot be wrong about the order — which the computed versions repeatedly were.

Everything happens in ONE transaction, so a failure leaves the database exactly as it was.

Usage: `python3 purge_fixtures.py [sql-like-pattern]` with `DATABASE_URL` set. Default `%probe%`, which catches both `probe-…` slugs and the
one-off fixtures that were named `api-probe`, `arena-probe`, `created-probe`, `s2-probe`.
"""
import os
import sys

import psycopg2

DEFAULT_PATTERN = "%probe%"


def main():
    pattern = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_PATTERN
    pool = psycopg2.connect(os.environ["DATABASE_URL"])
    with pool.cursor() as cur:
        cur.execute("select id from projects where slug like %s", (pattern,))
        roots = [row[0] for row in cur.fetchall()]
        print(f"fixture projects: {len(roots)}", flush=True)
        if not roots:
            return 0

        cur.execute(
            """
            select con.conrelid::regclass::text,
                   (select att.attname::text from pg_attribute att
                     where att.attrelid = con.conrelid and att.attnum = con.conkey[1]),
                   con.confrelid::regclass::text,
                   (select att.attname::text from pg_attribute att
                     where att.attrelid = con.confrelid and att.attnum = con.confkey[1])
              from pg_constraint con
              join pg_namespace ns on ns.oid = con.connamespace
             where con.contype = 'f'
               and ns.nspname = 'public'
               and array_length(con.conkey, 1) = 1
            """
        )
        graph = cur.fetchall()
        cur.execute(
            "select table_name from information_schema.columns"
            " where table_schema = 'public' and column_name = 'id'"
        )
        has_id = {row[0] for row in cur.fetchall()}

        # ── Phase 1: collect every table's own ids, and every predicate a table without an id needs ────────────────────────────────
        own = {"projects": set(roots)}
        predicates = {}
        pending = {"projects"}
        rounds = 0
        while pending:
            rounds += 1
            if rounds > 12:
                raise SystemExit("collection did not converge")
            next_pending = set()
            for child, child_col, parent, parent_col in graph:
                if parent not in pending:
                    continue
                parents = own.get(parent)
                if not parents:
                    continue
                cur.execute(
                    f"select distinct {child_col} from {child} where {child_col} = any(%s)",
                    (list(parents),),
                )
                found = {row[0] for row in cur.fetchall()}
                if not found:
                    continue
                predicates.setdefault((child, child_col), set()).update(found)
                if child in has_id:
                    cur.execute(
                        f"select distinct id from {child} where {child_col} = any(%s)",
                        (list(found),),
                    )
                    ids = {row[0] for row in cur.fetchall()}
                    bucket = own.setdefault(child, set())
                    if ids - bucket:
                        bucket |= ids
                        next_pending.add(child)
            pending = next_pending

        print(f"collected {len(own)} tables", flush=True)

        # ── Phase 2: delete, retrying whatever a foreign key blocks ──────────────────────────────────────────────────────────────
        plan = {}
        for table, ids in own.items():
            if table in has_id:
                plan[(table, "id")] = set(ids)
        for (table, column), ids in predicates.items():
            if (table, "id") in plan and column != "id":
                continue
            plan.setdefault((table, column), set()).update(ids)

        total = 0
        stuck = 0
        while plan:
            progress = 0
            for (table, column), values in list(plan.items()):
                cur.execute("savepoint purge_row")
                try:
                    cur.execute(f"delete from {table} where {column} = any(%s)", (list(values),))
                except psycopg2.errors.ForeignKeyViolation:
                    cur.execute("rollback to savepoint purge_row")
                    continue
                rowcount = cur.rowcount
                cur.execute("release savepoint purge_row")
                plan.pop((table, column), None)
                total += rowcount
                progress += 1
            stuck = 0 if progress else stuck + 1
            if stuck >= 3:
                raise SystemExit(f"stuck with {len(plan)} sets: {sorted(plan)[:4]}")
        pool.commit()
        print(f"deleted {total} rows; fixtures removed", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
