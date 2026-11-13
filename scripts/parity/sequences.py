#!/usr/bin/env python3
"""Do the sequences agree with the rows they hand out ids to?

The mover inserts explicit ids (1..N from SQLite), which leaves every `id` sequence at its starting value. In
SQLite that costs nothing; in PostgreSQL the next insert that omits `id` asks for 1 and collides with the row
already there. On the migrated database the measured answer was **47 of 47 sequences behind**, and the first
write the port attempted failed with

    duplicate key value violates unique constraint "projects_pkey" DETAIL: Key (id)=(1) already exists

Usage: `python3 scripts/parity/sequences.py [--fix]`
"""
import os
import sys

import psycopg2


def main():
    fix = "--fix" in sys.argv
    dsn = os.environ.get("DATABASE_URL")
    if not dsn:
        print("DATABASE_URL is not set")
        return 2
    con = psycopg2.connect(dsn)
    cur = con.cursor()
    cur.execute("select sequencename, last_value from pg_sequences where schemaname = 'public'")
    sequences = cur.fetchall()
    behind = []
    for name, last in sequences:
        table = name[: -len("_id_seq")]
        cur.execute("select to_regclass(%s) is not null", (f"public.{table}",))
        if not cur.fetchone()[0]:
            continue
        cur.execute(f'select max(id), count(*) from "{table}"')
        top, rows = cur.fetchone()
        # `last_value` is NULL for a sequence that has never been called: never used is as behind as behind
        # gets, and reading it as "fine" is how the first insert after a migration fails.
        if top is not None and (last or 0) < top:
            behind.append((table, last or 0, top, rows))
    print(f"{len(behind)} of {len(sequences)} sequences are behind the rows they serve")
    for table, last, top, rows in behind[:8]:
        print(f"  {table}: sequence at {last}, rows reach {top} ({rows} rows)")
    if fix and behind:
        for table, _last, top, _rows in behind:
            cur.execute(
                "select setval(%s, %s, true)", (f"{table}_id_seq", top)
            )
        con.commit()
        print(f"setval applied to {len(behind)} sequences")
    con.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
