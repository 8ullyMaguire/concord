#!/usr/bin/env python3
"""Remove duplicate complaints and features, keeping the lowest id of each group.

A duplicate is not harmless. A feature's pain score is the sum over its linked
complaints, so a complaint recorded twice doubles the pain of every feature
linked to it, and the ranking the site exists to produce is quietly wrong. The
seeder was fixed to be idempotent, but the duplicates an earlier run created are
still in the database and have to go.

Deleting a feature must also unlink it: feature_complaints has a foreign key to
features, and leaving orphan links behind would let a later "sum pain" query
count a complaint against a feature that no longer exists.

    ./dedupe.py            # report only (the default)
    ./dedupe.py --apply    # delete
"""

import argparse
import sqlite3
import sys

DB = "/home/alvaro/.local/share/concord/concord.db"


def duplicate_groups(cur, table, key_cols):
    """Return groups of ids sharing the same key, keeping the lowest id."""
    cols = ", ".join(key_cols)
    cur.execute(
        f"SELECT {cols}, COUNT(*) c, GROUP_CONCAT(id) FROM {table} "
        f"GROUP BY {cols} HAVING c > 1"
    )
    out = []
    for row in cur.fetchall():
        ids = sorted(int(x) for x in row[-1].split(","))
        out.append((row[:-2], ids))
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", default=DB)
    ap.add_argument(
        "--apply",
        action="store_true",
        help="actually delete; without it the script only reports",
    )
    args = ap.parse_args()

    con = sqlite3.connect(args.db)
    cur = con.cursor()

    removed = {"features": 0, "complaints": 0}

    # Features first: a feature holds links, so it must go before the complaint
    # it points at.
    for key, ids in duplicate_groups(cur, "features", ["project_id", "title"]):
        doomed = ids[1:]
        print(f"features (project {key[0]}) {key[1]!r}: keep {ids[0]}, drop {doomed}")
        if args.apply:
            for i in doomed:
                cur.execute("DELETE FROM feature_complaints WHERE feature_id = ?", (i,))
                # pairwise_votes references a feature from either side, so both
                # columns have to be cleared.
                cur.execute("DELETE FROM pairwise_votes WHERE feature_a = ? OR feature_b = ?", (i, i))
                cur.execute("DELETE FROM features WHERE id = ?", (i,))
            removed["features"] += len(doomed)

    for key, ids in duplicate_groups(cur, "complaints", ["project_id", "title"]):
        doomed = ids[1:]
        print(f"complaints (project {key[0]}) {key[1]!r}: keep {ids[0]}, drop {doomed}")
        if args.apply:
            for i in doomed:
                # A complaint that is still linked to a kept feature should be
                # relinked to the surviving twin rather than dropped, or the
                # feature silently loses the pain it was counting.
                cur.execute(
                    "SELECT feature_id FROM feature_complaints WHERE complaint_id = ?", (i,)
                )
                for (fid,) in cur.fetchall():
                    cur.execute(
                        "INSERT OR IGNORE INTO feature_complaints (feature_id, complaint_id) "
                        "VALUES (?, ?)", (fid, ids[0]),
                    )
                cur.execute("DELETE FROM feature_complaints WHERE complaint_id = ?", (i,))
                cur.execute("DELETE FROM complaint_impacts WHERE complaint_id = ?", (i,))
                cur.execute(
                    "DELETE FROM comments WHERE thread_id = ? AND thread_kind = 'complaint'", (i,)
                )
                cur.execute("DELETE FROM complaints WHERE id = ?", (i,))
            removed["complaints"] += len(doomed)

    if args.apply:
        con.commit()
        print(f"\ndeleted {removed['features']} feature(s), {removed['complaints']} complaint(s)")
    else:
        con.rollback()
        print("\nreport only; pass --apply to delete")
    con.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
