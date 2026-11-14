#!/usr/bin/env python3
"""Remove a project and everything that hangs off it, without knowing the schema.

Probes create projects, and a project created through the API now carries a board, a charter, a ledger row, arenas and (if the
probe made any) features, complaints and consensus calls. A cleanup that lists the children by hand is a list that goes stale the
next time a table is added — and a cleanup that fails leaves a fixture behind, which is how a fixture becomes a *catalogue leak*
that a different probe fails on.

So the order is derived: every foreign key in `public` pointing at `projects` (transitively) gives a depth, children first, and
each table's rows are removed by the column that points at its parent. Depth-descending deletion is the topological order the
foreign keys demand.

Usage: `python3 clean_project.py <slug> [<slug> ...]` with `DATABASE_URL` set.
"""
import os
import sys

import psycopg2


def relation(pool, name):
    with pool.cursor() as cur:
        cur.execute("select to_regclass(%s)", (name,))
        return cur.fetchone()[0]


def main(slugs):
    pool = psycopg2.connect(os.environ["DATABASE_URL"])
    pool.autocommit = True
    with pool.cursor() as cur:
        cur.execute("select id from projects where slug = any(%s)", (slugs,))
        roots = [row[0] for row in cur.fetchall()]
        print(f"projects to remove: {len(roots)} ({slugs})")
        if not roots:
            return 0
        cur.execute(
            """
            select cl.relname::text, att.attname::text, pcl.relname::text, patt.attname::text
              from pg_constraint con
              join pg_class cl on cl.oid = con.conrelid
              join pg_class pcl on pcl.oid = con.confrelid
              join pg_attribute att
                on att.attrelid = con.conrelid and att.attnum = any(con.conkey)
              join pg_attribute patt
                on patt.attrelid = con.confrelid and patt.attnum = any(con.confkey)
             where con.contype = 'f'
               and cl.relnamespace = 'public'::regnamespace
               and pcl.relnamespace = 'public'::regnamespace
            """
        )
        edges = cur.fetchall()

    depth = {"projects": 0}
    changed = True
    while changed:
        changed = False
        for child, _, parent, _ in edges:
            if parent in depth and depth.get(child, -1) < depth[parent] + 1:
                depth[child] = depth[parent] + 1
                changed = True
    ordered = sorted((table for table in depth if table != "projects"),
                     key=lambda table: depth[table], reverse=True)

    ids = {"projects": set(roots)}
    removed = 0
    with pool.cursor() as cur:
        for table in ordered:
            for child, column, parent, _ in edges:
                if child != table or parent not in ids or not ids[parent]:
                    continue
                if relation(pool, table) is None:
                    continue
                placeholders = ",".join(str(int(value)) for value in ids[parent])
                # Record what this table holds before removing it, so the tables BELOW it can find their rows.
                try:
                    cur.execute(f'select id from "{table}" where "{column}" in ({placeholders})')
                    found = {row[0] for row in cur.fetchall()}
                except psycopg2.Error:
                    pool.rollback()
                    found = set()
                ids.setdefault(table, set()).update(found)
                try:
                    cur.execute(f'delete from "{table}" where "{column}" in ({placeholders})')
                    removed += cur.rowcount
                except psycopg2.Error as error:
                    # A row still referenced by something deeper is removed on a later pass, once that is gone.
                    pool.rollback()
                    print(f"  pending {table}.{column}: {str(error).splitlines()[0][:90]}")
    # Second pass: with the deepest rows gone, the rest can go. Two passes is enough for the shapes this schema has; a third is
    # cheap insurance and stops at stability.
    with pool.cursor() as cur:
        for _ in range(3):
            remaining = 0
            for table in ordered:
                for child, column, parent, _ in edges:
                    if child != table or parent not in ids or not ids[parent]:
                        continue
                    if relation(pool, table) is None:
                        continue
                    placeholders = ",".join(str(int(value)) for value in ids[parent])
                    try:
                        cur.execute(f'delete from "{table}" where "{column}" in ({placeholders})')
                        removed += cur.rowcount
                        remaining += cur.rowcount
                    except psycopg2.Error:
                        pool.rollback()
            if remaining == 0:
                break
        try:
            cur.execute("delete from projects where id = any(%s)", (roots,))
            removed += cur.rowcount
        except psycopg2.Error as error:
            print("projects remain:", str(error).splitlines()[0][:160])
            return 1
    print(f"rows removed: {removed}")
    with pool.cursor() as cur:
        cur.execute("select count(*) from projects")
        print("projects now:", cur.fetchone()[0])
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
