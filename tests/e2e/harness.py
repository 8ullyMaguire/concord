"""Shared server harness for the Playwright suites.

Two things this exists to fix, both measured rather than guessed:

1. **`pkill -f bin/concord` kills the caller.** The pattern matches any process
   whose cmdline *contains* that string -- including the shell executing the
   `make e2e` recipe, which has the binary path in its own argv. `make e2e`
   therefore SIGTERM'd itself on the recipe's first line and has never once
   reached pytest. The suites had the same pattern in their fixtures, where it
   was harmless only because pytest's own cmdline does not contain the path.

   So: no substring pkill anywhere. Ownership is decided by port, and a port
   held by something we did not start is an error with a name, not a kill.

2. **A leaked server made the next run fail with a confusing message.** The old
   fixture killed by pattern to be forgiving; instead this writes a pidfile
   next to the database, reaps only that pid, and reports precisely who holds
   the port if it is not ours.
"""

import json
import os
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
BIN = os.path.join(REPO, "bin", "concord")

# The port's two knobs on this harness, both unset in the Go world so every existing run behaves
# exactly as before.
#
#   CONCORD_E2E_CANDIDATE — a binary that speaks `serve --addr HOST:PORT`, i.e. the Rust port.
#   CONCORD_E2E_DSN       — the PostgreSQL URL that binary reads. When set, `connect()` seeds
#                           PostgreSQL instead of SQLite, because the candidate does not read
#                           `concord.db` and a suite that seeded it would assert on an empty service.
#
# The suites seed through the database rather than the API on purpose (a browser test whose data
# arrives through the API under test cannot fail for the reason it exists), so seeding is the one
# thing a candidate has to satisfy — and it is the one thing this harness can translate.
CANDIDATE = os.environ.get("CONCORD_E2E_CANDIDATE")
SEED_DSN = os.environ.get("CONCORD_E2E_DSN")

# Whose process a leaked pidfile is allowed to reap. The candidate is a different path, and a reap
# that only knows `bin/concord` refuses to clean up after the port — which is exactly the state the
# first candidate run was left in: a server holding the port that the harness would not touch.
SERVED_BY = CANDIDATE or BIN


def connect(path):
    """A connection for seeding: SQLite, or PostgreSQL when a candidate is under test.

    Returns something the suites already know how to use: `execute(sql, params)`, `commit()`,
    `close()`, and `lastrowid` after an INSERT. PostgreSQL has no `lastrowid`, so the adapter asks
    for `RETURNING id` instead; that is the only API difference the suites exercise.
    """
    if not SEED_DSN:
        import sqlite3
        return sqlite3.connect(path)
    return PgSeed(SEED_DSN)


class PgSeed:
    """The slice of the sqlite3 API the suites use, over psycopg2.

    Three translations, each because the Go service and the port disagree about a type rather than
    about a behaviour:

      * `?` placeholders become `%s` (every suite was written against SQLite, and rewriting 86 call
        sites to prove a point is not the work — translating one string is);
      * a numeric parameter bound to a `timestamptz` column becomes a datetime, because the suites
        seed `time.time()` into what is a REAL in SQLite and a timestamp in the baseline. The
        column's type is read from `information_schema`, not guessed from the value: a 1500 elo and
        a 1794563868 unix second are both numbers, and only one of them is a time;
      * an `INSERT` without `RETURNING` gets `RETURNING id` appended, because `lastrowid` is how a
        suite carries a freshly inserted id into the next statement.
    """

    def __init__(self, dsn):
        import psycopg2
        self._con = psycopg2.connect(dsn)
        self._cursor = None
        self._types = {}
        self.lastrowid = None

    def _column_types(self, table):
        if table not in self._types:
            cur = self._con.cursor()
            cur.execute(
                "select column_name, data_type from information_schema.columns "
                "where table_schema = 'public' and table_name = %s", (table,))
            self._types[table] = dict(cur.fetchall())
        return self._types[table]

    def _coerce(self, column_type, value):
        """One parameter as its destination column wants it."""
        import datetime
        if isinstance(value, (int, float)) and not isinstance(value, bool):
            if column_type == "timestamp with time zone":
                return datetime.datetime.fromtimestamp(float(value), datetime.UTC)
            if column_type == "boolean":
                return bool(value)
        return value

    def _convert(self, table, columns, params, values_expr):
        """Parameters as the destination's types want them, matched by PLACEHOLDER position.

        Position matters and by-index is wrong: the suites write `VALUES (?, 'feature', ?, ?, ?)`,
        so the fourth parameter belongs to the fifth column and `entered_at` would receive a
        column id. Each `?` is matched to the column at the same position in the list, and a literal
        consumes a position without consuming a parameter.
        """
        tokens, depth, current = [], 0, ""
        for char in values_expr:
            if char == "(":
                depth += 1
            elif char == ")":
                depth -= 1
            if char == "," and depth == 0:
                tokens.append(current)
                current = ""
            else:
                current += char
        tokens.append(current)

        types = self._column_types(table)
        out, next_param = [], 0
        for position, token in enumerate(tokens):
            # `?` or `%s`: `_convert` runs after the placeholder translation, so the tokens it
            # sees are already PostgreSQL's. Checking only for `?` converted nothing at all and the
            # symptom was the original numeric-into-timestamptz error.
            if token.strip() not in ("?", "%s"):
                continue
            if next_param >= len(params):
                break
            column = columns[position] if position < len(columns) else None
            out.append(self._coerce(types.get(column), params[next_param]))
            next_param += 1
        # Anything the walk did not account for is passed through: losing a parameter silently is
        # how a fixture inserts the wrong row and the suite still passes.
        out.extend(params[next_param:])
        return out

    def execute(self, sql, params=None):
        import re
        import uuid
        cur = self._con.cursor()
        translated = sql.replace("?", "%s")
        params = list(params or ())
        match = re.match(r"\s*insert\s+into\s+([\w\"]+)\s*\(([^)]*)\)", translated,
                         re.IGNORECASE)
        if match:
            table = match.group(1).strip('"')
            columns = [c.strip().strip('"') for c in match.group(2).split(",")]
            values = re.search(r"values\s*\((.*)\)\s*$", translated,
                               re.IGNORECASE | re.DOTALL)
            # The baseline gives every API-facing table a `public_id` the Go schema did not have.
            # It is added before the parameters are converted, so a single pass sees the final
            # column list; doing it after produced a placeholder with no argument.
            if "public_id" in self._column_types(table) and "public_id" not in columns:
                columns = columns + ["public_id"]
                params = params + [uuid.uuid4().hex[:26].upper()]
                column_sql = match.group(0).rstrip().rstrip(")") + ", public_id)"
                translated = translated.replace(match.group(0), column_sql, 1)
                values = re.search(r"values\s*\((.*)\)\s*$", translated,
                                   re.IGNORECASE | re.DOTALL)
                if values:
                    translated = (translated[:values.start(1)] + values.group(1).rstrip()
                                  + ", %s" + translated[values.end(1):])
            params = self._convert(table, columns, params,
                                   values.group(1) if values else "")
        if translated.lstrip().lower().startswith("insert") and "returning" not in translated.lower():
            translated = translated.rstrip().rstrip(";") + " RETURNING id"
            row = None
        cur.execute(translated, tuple(params))
        if translated.rstrip().upper().endswith("RETURNING ID"):
            row = cur.fetchone()
            self.lastrowid = row[0] if row else None
        self._cursor = cur
        return self

    def executemany(self, sql, rows):
        cur = self._con.cursor()
        cur.executemany(sql.replace("?", "%s"), rows)
        return self

    def executescript(self, script):
        cur = self._con.cursor()
        cur.execute(script.replace("?", "%s"))
        return self

    def fetchone(self):
        return self._cursor.fetchone() if self._cursor else None

    def fetchall(self):
        return self._cursor.fetchall() if self._cursor else []

    def commit(self):
        self._con.commit()

    def rollback(self):
        self._con.rollback()

    def close(self):
        self._con.close()

    def cursor(self):
        return self


def port_is_free(port):
    with socket.socket() as s:
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            s.bind(("127.0.0.1", port))
            return True
        except OSError:
            return False


def port_holder(port):
    """The pid listening on `port`, or None. Read from /proc, no new tools."""
    hexport = f"{port:04X}"
    inodes = set()
    with open("/proc/net/tcp", "r") as f:
        next(f)
        for line in f:
            parts = line.split()
            if len(parts) < 10 or parts[1].split(":")[1] != hexport:
                continue
            if parts[3] != "0A":  # 0A = LISTEN
                continue
            inodes.add(parts[9])
    if not inodes:
        return None
    for pid in os.listdir("/proc"):
        if not pid.isdigit():
            continue
        fd_dir = f"/proc/{pid}/fd"
        try:
            for fd in os.listdir(fd_dir):
                try:
                    target = os.readlink(f"{fd_dir}/{fd}")
                except OSError:
                    continue
                if target.startswith("socket:[") and target[8:-1] in inodes:
                    with open(f"/proc/{pid}/cmdline", "rb") as c:
                        cmd = c.read().replace(b"\0", b" ").decode(errors="replace")
                    return int(pid), cmd.strip()
        except OSError:
            continue
    return None


def reap(port, pidfile):
    """Kill a server WE started in an earlier run. Never a port holder we did
    not start -- see the module docstring.

    The pid is verified against its own /proc cmdline before any signal: a
    pidfile can outlive its process and the pid can be recycled by something
    unrelated, and SIGTERMing an innocent process because a temp file said so
    is worse than the leaked server this exists to clean up.
    """
    if not os.path.exists(pidfile):
        return
    try:
        with open(pidfile) as f:
            pid = int(f.read().strip())
    except (ValueError, OSError):
        os.unlink(pidfile)
        return
    os.unlink(pidfile)
    try:
        with open(f"/proc/{pid}/cmdline", "rb") as c:
            cmd = c.read().replace(b"\0", b" ").decode(errors="replace")
    except OSError:
        return  # gone already
    # The basename allowance exists only for a candidate, whose path is wherever it was built. In
    # the Go world the match is the exact binary path, and the selftest that guards this ("refuses
    # to kill a pid that is not concord") fails the moment the rule loosens: a temp path like
    # /tmp/concord-e2e-8421.pid contains the word, and an innocent python process was killed.
    if SERVED_BY not in cmd and not (CANDIDATE and os.path.basename(CANDIDATE) in cmd):
        print(f"harness: pid {pid} is no longer concord ({cmd.strip()[:80]}); "
              f"not touching it")
        return
    try:
        os.kill(pid, 15)
    except ProcessLookupError:
        return
    for _ in range(30):
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return
        time.sleep(0.1)
    try:
        os.kill(pid, 9)
    except ProcessLookupError:
        pass


def pidfile_for(port):
    """A STABLE path, so a server leaked by a crashed run is found by the next
    run. It lived inside the per-run tempdir before, which made every leak
    permanent and every subsequent run fail with "port is held by pid N" --
    a failure whose cause was three runs in the past."""
    return os.path.join(tempfile.gettempdir(), f"concord-e2e-{port}.pid")


def start_server(port, dbdir, logdir, extra_env=None):
    """Start a real concord on `port` with a throwaway DB, and reap it after.

    Returns (proc, db_path, base_url).

    `extra_env` is for suites that need the server to be configured a particular
    way -- the guest suite runs a second instance with CONCORD_GUEST_MAU_LIMIT=0 to
    show what a visitor is told once the door is shut.
    """
    db = os.path.join(dbdir, "concord.db")
    pidfile = pidfile_for(port)
    reap(port, pidfile)

    holder = port_holder(port)
    if holder:
        pid, cmd = holder
        raise RuntimeError(
            f"port {port} is held by pid {pid}: {cmd}\n"
            f"That is not a server this suite started (no pidfile in {dbdir}).\n"
            f"Stop it yourself, or change PORT in the suite -- do NOT let a "
            f"pattern-kill reach it.")

    if CANDIDATE:
        # The candidate reads PostgreSQL, not `concord.db`, and takes its address as an argument.
        env = dict(os.environ, CONCORD_DB_URL=SEED_DSN or "",
                   RUST_LOG=os.environ.get("RUST_LOG", "info,sqlx=warn"))
        if extra_env:
            env.update(extra_env)
        argv = [CANDIDATE, "serve", "--addr", f"127.0.0.1:{port}"]
    else:
        env = dict(os.environ, CONCORD_DB=db, CONCORD_LISTEN=f"127.0.0.1:{port}")
        if extra_env:
            env.update(extra_env)
        argv = [BIN]
    logpath = os.path.join(logdir, f"concord-{port}.log")
    log = open(logpath, "w")
    proc = subprocess.Popen(argv, env=env, stdout=log, stderr=subprocess.STDOUT)

    base = f"http://127.0.0.1:{port}"
    for _ in range(100):
        if proc.poll() is not None:
            raise RuntimeError(
                f"concord exited immediately (code {proc.returncode}); "
                f"see {logpath}\n--- log ---\n{open(logpath).read()}")
        try:
            with urllib.request.urlopen(f"{base}/api/v1/healthz", timeout=1) as r:
                if b"ok" in r.read():
                    break
        except Exception:
            time.sleep(0.2)
    else:
        proc.kill()
        raise RuntimeError(
            f"concord never became healthy on :{port}; see {logpath}\n"
            f"--- log ---\n{open(logpath).read()}")

    with open(pidfile, "w") as f:
        f.write(str(proc.pid))
    return proc, db, base


def stop_server(proc):
    proc.terminate()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=5)


def api_raw(base, method, path, body=None, token=None, cookie=None):
    """One HTTP call, with exactly the Cookie header the caller asks for.

    Separate from api() because some tests are ABOUT the cookie notice: api()
    always sends the acceptance (every other test needs it to be able to write at
    all), and a test that must see the refusal has to be able to send nothing.
    """
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    if cookie:
        headers["Cookie"] = cookie
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers=headers)
    with urllib.request.urlopen(req, timeout=5) as resp:
        raw = resp.read()
        return json.loads(raw) if raw else None


def api(base, method, path, body=None, token=None):
    # The cookie notice is accepted, because every mutating request needs it and
    # this is the one place the suite's direct HTTP calls are built. A visitor who
    # has accepted the notice sends it on every request, so the harness does too;
    # the tests that are about the refusal use api_raw with no cookie.
    return api_raw(base, method, path, body=body, token=token, cookie="concord_consent=1")


# ---------------------------------------------------------------- synchronising
#
# WHY THESE EXIST. The suites were written with `page.wait_for_timeout(800)`
# between an action and the read that checks it. A sleep is not a
# synchronisation: it passes when the machine is fast and fails when it is busy,
# which is how a test becomes a flake that nobody can reproduce on an idle box.
#
# So the correct pattern gets a NAME here, and the call sites below read as
# "wait for the results" rather than as "hope 800ms was enough". Two rules make it
# correct rather than merely different:
#
#   1. `settled` waits for an observable STATE (an element visible, a count
#      reached), not for a duration. Playwright's expect() already retries, so
#      the actual wait is in the assertion.
#   2. `settled_text` takes the text to wait FOR, so a caller cannot accidentally
#      assert against a value it read before the action -- which is the specific
#      way the old sleep-based version passed vacuously.

DEFAULT_SYNC_TIMEOUT = 10000


def settled(expect, locator, timeout=DEFAULT_SYNC_TIMEOUT):
    """Wait for `locator` to satisfy `expect`, e.g. settled(expect, x).to_be_visible()."""
    return expect(locator).to_be_visible(timeout=timeout)


def settled_count(expect, locator, n, timeout=DEFAULT_SYNC_TIMEOUT):
    """Wait for `locator` to hold exactly `n` elements."""
    return expect(locator).to_have_count(n, timeout=timeout)


def settled_text(expect, locator, pattern, timeout=DEFAULT_SYNC_TIMEOUT):
    """Wait until `locator`'s text matches `pattern` (a str, or a compiled regex)."""
    return expect(locator).to_have_text(pattern, timeout=timeout)


def settled_attr(expect, locator, name, pattern, timeout=DEFAULT_SYNC_TIMEOUT):
    """Wait until `locator`'s attribute `name` matches `pattern`."""
    return expect(locator).to_have_attribute(name, pattern, timeout=timeout)
