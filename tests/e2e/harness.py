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
    if BIN not in cmd:
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


def start_server(port, dbdir, logdir):
    """Start a real concord on `port` with a throwaway DB, and reap it after.

    Returns (proc, db_path, base_url).
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

    env = dict(os.environ, CONCORD_DB=db, CONCORD_LISTEN=f"127.0.0.1:{port}")
    logpath = os.path.join(logdir, f"concord-{port}.log")
    log = open(logpath, "w")
    proc = subprocess.Popen([BIN], env=env, stdout=log, stderr=subprocess.STDOUT)

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


def api(base, method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers=headers)
    with urllib.request.urlopen(req, timeout=5) as resp:
        raw = resp.read()
        return json.loads(raw) if raw else None