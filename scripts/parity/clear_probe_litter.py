
"""Remove every project whose slug starts with `probe-`, and everything that hangs off it.

Why this exists: a crashed probe leaves its fixture behind, and the parity corpus counts live rows — `/api/v1/curation`
reported 4 items where the Go build reports 2 purely because 24 half-torn probes were still in the database. The gate
then reads as a product regression when it is a housekeeping failure, and the next hour goes into the wrong file.

Why the deletion order is computed and not written down: a hand-written order was wrong twice (milestones before
features, then `request_answers`, which reaches `projects` only through `requests`). The order is `hops from projects`,
descending — a number that cannot go stale against the schema.

Usage: `CONCORD_E2E_DSN=... DATABASE_URL=... python3 scripts/parity/clear_probe_litter.py`
"""
import os, psycopg2
from collections import defaultdict, deque

con = psycopg2.connect(os.environ["DATABASE_URL"]); con.autocommit = False
cur = con.cursor()
cur.execute("select id from projects where slug like 'probe-%%'")
projects = [r[0] for r in cur.fetchall()]
print("projects:", len(projects))
if not projects:
    raise SystemExit(0)
m = ",".join(["%s"]*len(projects)); p = tuple(projects)

# Build the FK graph from the catalogue rather than from a hand-written order: the hand-written order was wrong twice
# (milestones before features, then request_answers, which reaches projects only through requests).
cur.execute("""
    select conrelid::regclass::text as child, confrelid::regclass::text as parent,
           a.attname as column_name
      from pg_constraint c
      join pg_attribute a on a.attrelid = c.conrelid and a.attnum = c.conkey[1]
     where c.contype = 'f' and array_length(c.conkey, 1) = 1
""")
children = defaultdict(list)   # parent -> [(child, column)]
for child, parent, column in cur.fetchall():
    # Bare names on BOTH sides: `regclass` prints `public.requests` for one register and `requests` for another, and a
    # mismatch here silently produced the wrong deletion order (children of `requests` were never visited first).
    children[parent.split(".")[-1]].append((child, column))

# Restrict to public tables (regclass prints schema-qualified names when the search_path is unusual).
def bare(name):
    return name.split(".")[-1]

# Every table that reaches `projects`, with the chain of (child, column, parent) back to it.
chain = {}
queue = deque([("projects", None)])
while queue:
    parent, _ = queue.popleft()
    for child, column in children[parent]:
        child_bare = bare(child)
        if child_bare in chain or child_bare == "projects":
            continue
        chain[child_bare] = (column, bare(parent))
        queue.append((child_bare, None))

def predicate(table, depth=0):
    """A WHERE clause selecting rows of `table` that belong to these projects, one hop at a time."""
    if table == "projects":
        return f"id in ({m})"
    column, parent = chain[table]
    if parent == "projects":
        return f"{column} in (select id from projects where id in ({m}))"
    return f"{column} in (select id from {parent} where {predicate(parent, depth + 1)})"

# Delete by DEPTH, deepest first. A DFS post-order was the obvious thing and produced
# ['features', 'requests', 'request_answers', 'projects'] — requests before its own child — so the order is now a number
# that cannot be got wrong: hops from `projects`, descending.
def depth(table):
    hops = 0
    while table != "projects":
        table = chain[table][1]
        hops += 1
    return hops

order = sorted(chain, key=depth, reverse=True) + ["projects"]
removed = 0
for table in order:
    if table == "projects":
        continue
    cur.execute(f"delete from {table} where {predicate(table)}", p)
    removed += cur.rowcount
cur.execute(f"delete from projects where id in ({m})", p); removed += cur.rowcount
con.commit()
print("rows deleted:", removed)
cur.execute("select count(*) from projects where slug like 'probe-%%'"); print("left:", cur.fetchone()[0])
