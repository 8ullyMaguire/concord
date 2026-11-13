#!/usr/bin/env python3
"""Generate the PostgreSQL baseline DDL from the LIVE SQLite schema (row 4, M0.4).

Why a generator and not a hand-written file: the Go tree holds 39 migrations and 91 tables, and a
translation typed by hand drifts from the schema the moment a migration lands. The plan's warning is
kept, though — "every row is a decision the generator cannot make for you" — so every decision the
generator takes is either a rule from the plan's mapping table (cited below by rule number) or an
entry in OVERRIDES, which is where the judgement calls live, one line each with the reason.

Usage (on the host that holds the database):

    python3 scripts/parity/ddl.py --sqlite ~/.local/share/concord/concord.db \\
                                  --out crates/db/sql/0001_baseline.sql

The generated file is what `psql -f` applies; the generator is re-run when the schema moves, and the
diff between generations is the thing a reviewer reads.
"""
import argparse
import re
import sqlite3
import sys

# The plan's mapping table, as rules the code applies:
#   R1  INTEGER PRIMARY KEY            -> bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY
#   R2  INTEGER holding only 0/1       -> boolean   (verified per column, never assumed)
#   R3  REAL unix seconds              -> timestamptz (the mover converts with to_timestamp)
#   R4  REAL score/rating/weight       -> double precision
#   R5  TEXT + CHECK (x IN (...))      -> TEXT + CHECK, unchanged
#   R6  TEXT holding JSON              -> jsonb     (verified by parsing a sample per column)
#   R7  *_fts* + shadow tables         -> omitted; replaced by tsvector + GIN on the source table
#   R8  composite PRIMARY KEY          -> PRIMARY KEY (a, b)
#   R9  DEFAULT (strftime('%s','now')) -> DEFAULT now()
#   R10 created_at / updated_at        -> timestamptz NOT NULL

TIMESTAMP_SUFFIXES = ("_at", "_on", "_date", "_time", "_timestamp", "_deadline", "_due", "_since")
TIMESTAMP_NAMES = {"at", "created", "updated", "deleted", "expires", "expiry", "timestamp", "date",
                   "time", "deadline", "due", "since"}
JSON_NAMES = {"detail", "payload", "meta", "metadata", "data", "json", "config", "body_json",
              "settings", "context", "extra", "attrs", "props"}
# R2 is the rule that most looks mechanical and is not: a column holding only 0 and 1 is a count of
# 0 or 1 as often as it is a flag. `project_metrics.stars`, `users.trust_level` and
# `features.milestone_position` all pass the "never holds 2" test today and none of them is a
# boolean. So the name has to say boolean, and the data only has to agree.
BOOLEAN_TOKENS = {"is", "has", "needs", "need", "requires", "require", "can", "allow", "allows",
                  "enabled", "disabled", "active", "visible", "hidden", "deleted", "locked",
                  "pinned", "approved", "rejected", "merged", "resolved", "read", "starred",
                  "flagged", "muted", "verified", "guest", "moderator", "suggested", "baseline",
                  "quorum", "approval", "mentor", "optional", "required", "public", "private",
                  "draft", "pinned"}
SCORE_NAMES = {"score", "weight", "rating", "rank", "confidence", "affinity", "ratio", "value",
               "strength", "weight_sum", "sum", "avg", "mean", "median"}
# Tables that face the API get a public_id: the port's cursors and URLs use it, never the rowid.
API_TABLES = {"projects", "features", "complaints", "users", "project_documents", "milestones",
              "consensus_calls"}
# Decisions the generator must not take silently. table.column -> (sql type, why).
OVERRIDES = {
    # Filled in as the schema is reviewed; see the review table in docs/plans/port-to-profile.md.
}


def pg_ident(name: str) -> str:
    """Quote only what PostgreSQL requires quoting: reserved words and anything not [a-z0-9_]."""
    if name.replace("_", "").isalnum() and name == name.lower() and not name[0].isdigit():
        reserved = {"order", "user", "table", "column", "group", "check", "default", "references",
                    "primary", "select", "from", "where", "all", "limit", "as", "case", "end"}
        if name not in reserved:
            return name
    return '"' + name.replace('"', '""') + '"'


def fts_sources(cur) -> dict[str, list[str]]:
    """`projects` -> ['name', 'description', ...] read from the fts5 virtual table's own definition.

    Reading the definition rather than assuming the columns is the whole point: the port must index
    what the search actually indexed, and an fts table that gains a column must not be guessed at.
    """
    sources: dict[str, list[str]] = {}
    rows = cur.execute("select name, sql from sqlite_master where type='table' and name like '%_fts' "
                       "and sql like '%fts5%'").fetchall()
    for name, sql in rows:
        body = sql[sql.index("(") + 1:sql.rindex(")")]
        cols = []
        content = None
        for raw in body.split(","):
            token = raw.strip().split()[0] if raw.strip() else ""
            if not token:
                continue
            if token.startswith("content_rowid"):
                # `content_rowid='id'` starts with "content" too, and reading it as the source
                # table turned both fts tables into one whose source was called `id`.
                continue
            if token.startswith("content"):
                content = raw.split("=")[-1].strip().strip("'\"")
                continue
            if token.startswith("tokenize"):
                continue
            if raw.strip().upper().endswith("UNINDEXED"):
                continue
            cols.append(token)
        # A contentless fts5 table has no `content=` clause, so the source is read off the name:
        # `projects_fts` searches `projects`. Guessing there is safe because the name is the only
        # link the table has to its source.
        sources.setdefault(content or name[:-len("_fts")], []).extend(cols)
    return sources


def _is_boolean(cur, table: str, col) -> bool:
    _cid, name, decl, _nn, _dflt, pk = col
    if pk or "INT" not in (decl or "").upper():
        return False
    lo, hi, distinct = data_probe(cur, table, name)
    return (lo is not None and hi is not None and lo >= 0 and hi <= 1
            and (distinct or 0) <= 2 and bool(set(name.lower().split("_")) & BOOLEAN_TOKENS))


INDEX_SQL = re.compile(r"^\s*create\s+(unique\s+)?index\s+(\S+)\s+on\s+(\S+)\s*\((.*)\)"
                       r"(\s*where\s+.*)?$", re.IGNORECASE | re.DOTALL)


def translate_index(sql: str, bool_columns: set[str], notes: list[str]) -> str:
    """SQLite index DDL -> PostgreSQL, keeping the partial predicate and the expressions.

    Two textual fixes and no more: `ifnull(` is `coalesce(`, and a comparison against a column that
    became boolean (`is_baseline = 1`) becomes the column itself, because PostgreSQL has no
    boolean = integer.
    """
    match = INDEX_SQL.match(sql.strip().rstrip(";"))
    if not match:
        notes.append(f"index DDL not understood, omitted: {sql.strip()[:80]!r}")
        return "-- omitted: " + sql.strip().replace("\n", " ")
    unique, name, table, columns, where = match.groups()
    columns = columns.replace("\n", " ").strip()
    columns = re.sub(r"ifnull\s*\(", "coalesce(", columns, flags=re.IGNORECASE)
    pred = (where or "").replace("\n", " ").strip()
    # The regex captures the WHERE keyword with the predicate; prepending " where " without
    # stripping it produced "... arena_id) where WHERE is_baseline = true".
    pred = re.sub(r"^where\s+", "", pred, flags=re.IGNORECASE)
    pred = re.sub(r"ifnull\s*\(", "coalesce(", pred, flags=re.IGNORECASE)
    for column in bool_columns:
        pred = re.sub(rf"\b{re.escape(column)}\s*=\s*1\b", column, pred)
        pred = re.sub(rf"\b{re.escape(column)}\s*=\s*0\b", f"not {column}", pred)
    kind = "create unique index" if unique else "create index"
    body = f"{kind} {pg_ident(name)} on {pg_ident(table)} ({columns})"
    if pred:
        body += f" where {pred}"
    return body + ";"


def table_names(cur) -> list[str]:
    rows = cur.execute(
        "select name from sqlite_master where type='table' and name not like 'sqlite_%' "
        "and name not like '%_fts%' order by name").fetchall()
    return [r[0] for r in rows]


def data_probe(cur, table: str, column: str):
    """What the column actually holds: (min, max, distinct_count). Read-only, one pass."""
    q = (f"select min({pg_ident_sqlite(column)}), max({pg_ident_sqlite(column)}), "
         f"count(distinct {pg_ident_sqlite(column)}) from {pg_ident_sqlite(table)}")
    try:
        return cur.execute(q).fetchone()
    except sqlite3.Error as error:
        print(f"  ! probe failed on {table}.{column}: {error}", file=sys.stderr)
        return (None, None, None)


def pg_ident_sqlite(name: str) -> str:
    return '"' + name.replace('"', '""') + '"'


def is_json(cur, table: str, column: str, limit: int = 50) -> bool:
    rows = cur.execute(
        f"select {pg_ident_sqlite(column)} from {pg_ident_sqlite(table)} "
        f"where {pg_ident_sqlite(column)} is not null and {pg_ident_sqlite(column)} != '' "
        f"limit {limit}").fetchall()
    if not rows:
        return False
    import json as jsonlib
    for (value,) in rows:
        try:
            jsonlib.loads(value)
        except (ValueError, TypeError):
            return False
    return True


def column_sql(cur, table: str, name: str, decl: str, notnull: int, default, pk: int,
               composite_pk: bool, fk_columns: set[str] | None = None) -> str:
    fk_columns = fk_columns or set()
    decl = (decl or "").upper()

    if table + "." + name in OVERRIDES:
        sql_type, _why = OVERRIDES[table + "." + name]
        out = f"{pg_ident(name)} {sql_type}"
        return out

    if pk and not composite_pk and decl == "INTEGER":
        # R1a: ALWAYS only where the key is the table's surrogate, i.e. it is called `id`. Three
        # tables key on a column with another name (charters.project_id, and two others), and those
        # keys are supplied by the caller — `insert into charters (project_id, ...)` is how a project
        # gets its charter. ALWAYS refused it ("cannot insert a non-DEFAULT value"), so a natural key
        # gets BY DEFAULT: still generated when omitted, accepted when named.
        if name == "id":
            return f"{pg_ident(name)} bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY"
        return f"{pg_ident(name)} bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY"

    sql_type = "text"
    if "INT" in decl:
        lo, hi, distinct = data_probe(cur, table, name)
        # A 0/1 check is necessary and not sufficient: `asserted_by` holds 0 and 1 because the
        # table has two rows, and it is a user id. Anything that is a foreign key, or named like
        # one, stays an integer — an id of 1 is not `true`.
        looks_like_id = (name.endswith("_id") or name in ("by", "actor", "owner", "author",
                                                          "user", "creator", "assignee"))
        looks_like_flag = bool(set(name.lower().split("_")) & BOOLEAN_TOKENS)
        if (not pk and name not in fk_columns and not looks_like_id and looks_like_flag
                and lo is not None and hi is not None and lo >= 0 and hi <= 1
                and (distinct or 0) <= 2):
            sql_type = "boolean"                      # R2, verified rather than assumed
        else:
            sql_type = "bigint"
    elif "REAL" in decl or "FLOA" in decl or "DOUB" in decl:
        if name.endswith(TIMESTAMP_SUFFIXES) or name in TIMESTAMP_NAMES or "timestamp" in name:
            sql_type = "timestamptz"                  # R3
        else:
            sql_type = "double precision"             # R4
    elif "BLOB" in decl:
        sql_type = "bytea"
    elif "TEXT" in decl or "CLOB" in decl or decl == "":
        if name in JSON_NAMES or name.endswith("_json"):
            sql_type = "jsonb" if is_json(cur, table, name) else "text"   # R6, verified
        elif name in ("created_at", "updated_at", "deleted_at"):
            sql_type = "timestamptz"                  # R10
        else:
            sql_type = "text"
    else:
        sql_type = "text"

    out = f"{pg_ident(name)} {sql_type}"
    if default is not None:
        d = str(default).strip()
        if "strftime" in d:
            out += " NOT NULL DEFAULT now()"          # R9
            return out
        if d.upper() in ("NULL",):
            pass
        elif sql_type == "timestamptz":
            # A unix-seconds default in SQLite is a timestamp, not a number: keep the instant.
            try:
                float(d.strip("'"))
                out += f" DEFAULT to_timestamp({d.strip(chr(39))})"
                return out
            except ValueError:
                out += f" DEFAULT {d}"
        elif d in ("0", "1") and sql_type == "boolean":
            out += f" DEFAULT {'true' if d == '1' else 'false'}"
        else:
            out += f" DEFAULT {d}"
    elif sql_type == "timestamptz" and name in ("created_at", "updated_at"):
        out += " NOT NULL DEFAULT now()"
    if notnull and "NOT NULL" not in out:
        out += " NOT NULL"
    # A single-column primary key that is not an INTEGER id keeps its constraint: SQLite declares
    # `key TEXT PRIMARY KEY` and a translation that drops it turns a key into a loose column, which
    # is how a foreign key loses the thing it points at.
    if pk and not composite_pk and "PRIMARY KEY" not in out:
        out += " PRIMARY KEY"
    return out


def generate(db_path: str) -> tuple[str, list[str]]:
    con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    cur = con.cursor()
    tables = table_names(cur)
    fts_cols = fts_sources(cur)
    notes: list[str] = []
    out: list[str] = []
    fk_sql: list[str] = []
    out.append("-- Generated by scripts/parity/ddl.py from the live SQLite schema. Do not edit by hand:")
    out.append("-- re-run the generator and review the diff. Decisions live in OVERRIDES.")
    out.append("")
    out.append("begin;")
    out.append("")

    for table in tables:
        cols = cur.execute(f"pragma table_info({pg_ident_sqlite(table)})").fetchall()
        fk_columns = {fk[3] for fk in
                      cur.execute(f"pragma foreign_key_list({pg_ident_sqlite(table)})").fetchall()}
        if not cols:
            notes.append(f"{table} has no columns per PRAGMA table_info — skipped, check it")
            continue
        pk_cols = [c[1] for c in cols if c[5]]
        composite = len(pk_cols) > 1
        lines = []
        for _cid, name, decl, notnull, default, pk in cols:
            lines.append("    " + column_sql(cur, table, name, decl, notnull, default, pk,
                                             composite, fk_columns))
        if composite:
            lines.append("    PRIMARY KEY (" + ", ".join(pg_ident(c) for c in pk_cols) + ")")  # R8
        if table in API_TABLES:
            lines.append('    public_id text NOT NULL UNIQUE')                                  # R11
        real_columns = {c[1] for c in cols}
        indexed = [c for c in fts_cols.get(table, []) if c in real_columns]
        missing = [c for c in fts_cols.get(table, []) if c not in real_columns]
        if missing and table in fts_cols:
            notes.append(f"{table}: fts indexed {missing}, which are not columns of {table} — the "
                         f"fts row was assembled by the writer; the generated tsvector covers "
                         f"{indexed} only, and the slice that serves search must decide what "
                         f"replaces the rest")
        if indexed:                                              # R7
            # One generated column per table rather than one per indexed column: search queries
            # the union, and `simple` rather than `english` because FTS5's unicode61 tokeniser does
            # no stemming — swapping in stemming here would change which documents match, which is
            # a behaviour change hidden inside a schema move.
            joined = " || ' ' || ".join(f"coalesce({pg_ident(c)}, '')" for c in indexed)
            lines.append(f"    search_tsv tsvector GENERATED ALWAYS AS "
                         f"(to_tsvector('simple', {joined})) STORED")
        out.append(f"create table {pg_ident(table)} (")
        out.append(",\n".join(lines))
        out.append(");")

        # Foreign keys cannot be inline: alphabetical order puts a child before its parent, and
        # PostgreSQL (correctly) refuses a reference to a table that does not exist yet. They are
        # collected here and added by ALTER TABLE after every table exists.
        for fk in cur.execute(f"pragma foreign_key_list({pg_ident_sqlite(table)})").fetchall():
            _id, _seq, ref_table, from_col, to_col, _on_update, _on_delete, _match = fk
            if ref_table not in tables:
                notes.append(f"{table}.{from_col} references {ref_table}, which is not in the "
                             f"baseline (FTS shadow or dropped) — FK omitted")
                continue
            fk_sql.append(
                f"alter table {pg_ident(table)} add constraint "
                f"{pg_ident('fk_' + table + '_' + from_col)} "
                f"foreign key ({pg_ident(from_col)}) references {pg_ident(ref_table)}"
                f"({pg_ident(to_col or 'id')});")

        # Indexes: read from sqlite_master, not assembled from pragma index_info. A partial index
        # carries a WHERE clause that pragma does not return, and an expression index returns NULL
        # column names — the first turned `WHERE is_baseline = 1` into a full unique index, which
        # then refused rows the source accepts (duplicate key on idx_arena_entries_one_baseline).
        bool_columns = {c[1] for c in cols if _is_boolean(cur, table, c)}
        for idx in cur.execute(f"pragma index_list({pg_ident_sqlite(table)})").fetchall():
            _seq, idx_name, unique, origin, _partial = idx
            if origin == "u":
                # A UNIQUE constraint, inline (`slug TEXT NOT NULL UNIQUE`) or table-level
                # (`UNIQUE (project_id, user_id)`), appears here as an auto-created index with
                # origin 'u' and NO sql in sqlite_master — and skipping it dropped every uniqueness
                # rule in the schema, silently, because the `continue` came before the note. The
                # port then accepted a second project with the same slug, which the Go service
                # refuses. Rebuilt from pragma index_info, which is exact for these: they carry no
                # WHERE clause and no expressions.
                info = cur.execute(f"pragma index_info({pg_ident_sqlite(idx_name)})").fetchall()
                unique_columns = [row[2] for row in info if row[2]]
                if not unique_columns:
                    notes.append(f"{idx_name}: unique index with no named columns — omitted, check it")
                    continue
                rebuilt = "uniq_" + table + "_" + "_".join(unique_columns)
                out.append(
                    f"create unique index {pg_ident(rebuilt)} on {pg_ident(table)} "
                    f"({', '.join(pg_ident(c) for c in unique_columns)});")
                continue
            if origin != "c":
                continue
            sql_row = cur.execute("select sql from sqlite_master where type='index' and name=?",
                                  (idx_name,)).fetchone()
            if not sql_row or not sql_row[0]:
                notes.append(f"{idx_name}: no SQL in sqlite_master — index omitted, check it")
                continue
            out.append(translate_index(sql_row[0], bool_columns, notes))

        out.append("")

    out.append("-- Foreign keys last, so table order in this file cannot matter.")
    out.extend(fk_sql)
    out.append("")
    out.append("commit;")
    con.close()
    return "\n".join(out) + "\n", notes


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sqlite", required=True)
    ap.add_argument("--out", required=True)
    args = ap.parse_args()
    sql, notes = generate(args.sqlite)
    with open(args.out, "w") as fh:
        fh.write(sql)
    print(f"{sql.count('create table')} tables -> {args.out}")
    for note in notes:
        print("note:", note)
    return 0


if __name__ == "__main__":
    sys.exit(main())
