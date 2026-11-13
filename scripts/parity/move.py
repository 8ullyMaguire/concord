#!/usr/bin/env python3
"""Move the live SQLite rows into the PostgreSQL baseline, one transaction per table.

Two rules, both learned the hard way in this repo:
  * the column list is read from the DESTINATION (`information_schema.columns`), never from the
    source: a source column that the baseline dropped (every `*_fts*` shadow) must not be silently
    re-inserted, and a destination column the source lacks must get its DEFAULT.
  * a table whose row count disagrees after the move ABORTS the whole run, because a partial load
    that reports success is the failure this script exists to prevent.

Three things the plan's sketch got wrong and this version does not:

  * `column_default is null` as the column filter drops every timestamp: `created_at` has
    `DEFAULT now()`, and excluding defaulted columns would move the rows without their dates. The
    filter here is `is_generated = 'NEVER'` plus the intersection with the source's own columns.
  * the table order is derived from PostgreSQL's own foreign keys (depth-first), not typed out.
    A hand-written ORDER goes stale the first time a migration adds a table.
  * psycopg3 is not installed on this host (psycopg2 2.9.13 is), so the driver is psycopg2.

Usage:
    CONCORD_PG_URL=postgres://... python3 scripts/parity/move.py [--dry-run]
"""
import argparse
import datetime
import hashlib
import os
import sqlite3
import sys

import psycopg2
import psycopg2.extras

SQLITE = os.path.expanduser("~/.local/share/concord/concord.db")

B32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"


def ulid_for(table: str, row_id) -> str:
    """A deterministic, ULID-shaped public id for (table, id).

    Deterministic rather than random so a re-run produces the same ids and the corpus stays
    comparable across rebuilds; ULID-shaped (26 Crockford base32 chars, leading 0 for a timestamp
    the baseline does not need to carry) so the column matches what the slices will generate.
    """
    digest = hashlib.sha256(f"{table}:{row_id}".encode()).digest()[:16]
    number = int.from_bytes(digest, "big")
    out = []
    for _ in range(26):
        out.append(B32[number % 32])
        number //= 32
    return "0" + "".join(reversed(out))[:25]


def destination_columns(cur, table: str) -> list[tuple[str, str, bool]]:
    """(name, type, is_identity) for the columns a load must fill, in ordinal order."""
    cur.execute(
        "select column_name, data_type, (is_identity = 'YES') from information_schema.columns "
        "where table_schema = 'public' and table_name = %s and is_generated = 'NEVER' "
        "order by ordinal_position", (table,))
    return cur.fetchall()


def source_columns(cur, table: str) -> set[str]:
    try:
        return {row[1] for row in cur.execute(f'pragma table_info("{table}")').fetchall()}
    except sqlite3.Error:
        return set()


def topological_order(pg) -> list[str]:
    """Every baseline table, parents before children, read off PostgreSQL's own constraints."""
    with pg.cursor() as cur:
        cur.execute("select table_name from information_schema.tables "
                    "where table_schema = 'public' and table_type = 'BASE TABLE'")
        tables = [r[0] for r in cur.fetchall()]
        cur.execute("""
            select c.conrelid::regclass::text as child, c.confrelid::regclass::text as parent
            from pg_constraint c
            join pg_class cl on cl.oid = c.conrelid
            join pg_namespace n on n.oid = cl.relnamespace
            where c.contype = 'f' and n.nspname = 'public'
        """)
        edges = [(r[0].strip('"'), r[1].strip('"')) for r in cur.fetchall()]
    children = {t: set() for t in tables}
    for child, parent in edges:
        if child in children and parent in children and child != parent:
            children[child].add(parent)
    order, seen = [], set()

    def visit(table, stack):
        if table in seen:
            return
        if table in stack:
            return                       # a cycle: both tables are already handled by the caller
        stack.add(table)
        for parent in sorted(children.get(table, ())):
            visit(parent, stack)
        stack.discard(table)
        seen.add(table)
        order.append(table)

    for table in sorted(tables):
        visit(table, set())
    return order


def convert(value, data_type: str, column: str, table: str):
    if value is None:
        return None
    if data_type == "boolean":
        return bool(value)
    if data_type == "jsonb":
        return psycopg2.extras.Json(value) if isinstance(value, str) else value
    if data_type == "timestamp with time zone":
        # unix seconds in SQLite, an instant in PostgreSQL. Converted here rather than in SQL
        # because psycopg2 sends a float as numeric, and PostgreSQL refuses numeric -> timestamptz
        # ("column created_at is of type timestamp with time zone but expression is of type
        # numeric"). A real datetime also keeps the instant exact.
        if isinstance(value, (int, float)):
            return datetime.datetime.fromtimestamp(float(value), datetime.UTC)
        return value
    return value


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sqlite", default=SQLITE)
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()
    url = os.environ.get("CONCORD_PG_URL") or os.environ.get("DATABASE_URL")
    if not url:
        print("CONCORD_PG_URL is not set", file=sys.stderr)
        return 2

    src = sqlite3.connect(f"file:{args.sqlite}?mode=ro", uri=True)
    src.row_factory = sqlite3.Row
    src_cur = src.cursor()
    pg = psycopg2.connect(url)
    pg.autocommit = False

    order = topological_order(pg)
    moved, skipped = 0, []
    failures = 0
    for table in order:
        dest = destination_columns(pg.cursor(), table)
        have = source_columns(src_cur, table)
        if not have:
            skipped.append(f"{table} (not in SQLite)")
            continue
        cols = [(name, typ, ident) for name, typ, ident in dest if name in have]
        public_id = any(name == "public_id" for name, _, _ in dest)
        names = [c[0] for c in cols]

        rows = src_cur.execute(f'select {",".join(chr(34) + c + chr(34) for c in names)} '
                               f'from "{table}"').fetchall()
        # Computed before the loop: a table with zero rows still needs a column list for the
        # truncate-and-count path, and a name defined inside the loop is unbound exactly then.
        names_here = names + ["public_id"] if public_id else names
        values = []
        for row in rows:
            out = [convert(row[name], typ, name, table) for name, typ, _ in cols]
            if public_id:
                out.append(ulid_for(table, row["id"]))
            values.append(tuple(out))

        if args.dry_run:
            print(f"{table}: would move {len(rows)} rows into {len(names_here)} columns")
            moved += len(rows)
            continue

        ident = [name for name, _, is_identity in cols if is_identity]
        # `overriding system value` goes between the column list and VALUES; after them it is a
        # syntax error, which is how the first run failed on every table with an identity column.
        insert = f'insert into "{table}" ({",".join(chr(34) + n + chr(34) for n in names_here)}) '
        if ident:
            insert += "overriding system value "
        insert += "values %s"
        try:
            with pg:
                with pg.cursor() as cur:
                    cur.execute(f'truncate table "{table}" cascade')
                    if values:
                        psycopg2.extras.execute_values(cur, insert, values, page_size=500)
                    cur.execute(f'select count(*) from "{table}"')
                    got = cur.fetchone()[0]
            if got != len(rows):
                print(f"{table}: MOVED {got} of {len(rows)} — aborting", file=sys.stderr)
                return 3
            moved += got
            print(f"{table}: {got}")
        except psycopg2.Error as error:
            failures += 1
            print(f"{table}: FAILED {error.__class__.__name__}: "
                  f"{str(error).strip().splitlines()[0]}", file=sys.stderr)
            pg.rollback()

    # THE MOVE IS NOT DONE WHEN THE ROWS ARE IN. Every insert above names `id`, which leaves each sequence at
    # its starting value; the next insert that omits `id` asks for 1 and collides with the row already there.
    # Measured on the migrated database: 19 of 47 sequences behind, and the probe's first write failed with
    #   duplicate key value violates unique constraint "projects_pkey" DETAIL: Key (id)=(1) already exists
    with pg:
        with pg.cursor() as cur:
            cur.execute("select sequencename from pg_sequences where schemaname = 'public'")
            behind = 0
            for (name,) in cur.fetchall():
                table = name[: -len("_id_seq")]
                cur.execute("select to_regclass(%s) is not null", (f"public.{table}",))
                if not cur.fetchone()[0]:
                    continue
                cur.execute(f'select max(id) from "{table}"')
                top = cur.fetchone()[0]
                if top is None:
                    continue
                cur.execute(
                    "select last_value from pg_sequences where schemaname = 'public' and sequencename = %s",
                    (name,),
                )
                last = cur.fetchone()[0] or 0
                if last < top:
                    cur.execute("select setval(%s, %s, true)", (name, top))
                    behind += 1
    print(f"sequences: {behind} advanced to the rows they serve")

    for note in skipped:
        print("skipped:", note)
    print(f"{moved} rows, {failures} tables failed")
    return 0 if failures == 0 else 4


if __name__ == "__main__":
    sys.exit(main())
