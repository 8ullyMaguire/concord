"""Meta-tests for the e2e harness itself.

These do not drive a browser. They exist because a green e2e run is a claim
about *which* tests ran, and two separate defects made that claim false while
every individual suite was genuinely green:

  1. `make e2e` SIGTERM'd itself on the first recipe line (`pkill -f bin/concord`
     matched the shell running the recipe). It had never reached pytest.
  2. Once that was fixed, `pytest tests/e2e` collected 35 tests and passed,
     silently skipping the Finder suite's 38 -- `finder_e2e.py` matches neither
     default pattern.

Both are silent. Neither throws. The tests below assert the *harness's*
preconditions, so a future edit that reintroduces either fails here rather than
being discovered as a suspiciously round number.
"""

import ast
import os
import re
import socket
import subprocess
import sys
import tempfile

import pytest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
E2E = os.path.join(REPO, "tests", "e2e")


def read(name):
    with open(os.path.join(E2E, name)) as f:
        return f.read()


def test_every_suite_is_collected_by_a_directory_run():
    """`pytest tests/e2e` must collect BOTH browser suites, not just the one
    whose filename happens to match.

    Asserted by running pytest --collect-only and reading which files appear,
    because "the run was green" is not the question; "did the Finder tests run"
    is. It used to collect 35 and pass, silently skipping 38.
    """
    out = subprocess.run(
        [sys.executable, "-m", "pytest", "tests/e2e", "--collect-only", "-q"],
        cwd=REPO, capture_output=True, text=True, timeout=120).stdout
    for suite in ("e2e_test.py", "finder_e2e.py", "project_panels_e2e.py",
                    "harness_selftest_test.py"):
        assert suite in out, (
            f"{suite} contributed no tests to a directory-level run -- it matches "
            f"neither of pytest's default patterns and is absent from "
            f"pytest.ini's python_files. Its tests would be silently skipped.")
    # And a real count, so a suite that collects one trivial test still fails.
    m = re.search(r"(\d+) tests? collected", out)
    assert m, f"pytest --collect-only printed no count:\n{out[-2000:]}"
    total = int(m.group(1))
    assert total >= 88, (
        f"only {total} tests collected across all suites; expected >= 88 "
        f"(35 site + 38 finder + 10 project panels + 5 harness). A drop means a "
        f"suite lost tests.")


def test_the_two_browser_suites_cannot_share_one_pytest_process():
    """Why `make e2e` runs three processes instead of one.

    Each browser suite opens a session-scoped sync_playwright() context, and
    both fixtures are alive at the same time -- so the contexts are NESTED, not
    sequential. Playwright's sync API refuses a second one there ("It looks like
    you are using Playwright Sync API inside the asyncio loop"). If a future edit
    "simplifies" the target back to a single `pytest tests/e2e`, the Finder suite
    errors at fixture setup with a message about asyncio, which points nowhere
    near the real cause.

    Verified by actually attempting it, so the assertion cannot rot into a
    comment about a library version that no longer behaves this way.

    The first draft of this probe opened and CLOSED the first context before
    opening the second, and printed SECOND_OK -- because that shape does work.
    Two sequential contexts are fine; it is the nesting that breaks. That
    distinction is the whole content of this test, so it is probed with the
    nested shape a pytest session fixture actually produces.
    """
    probe = (
        "from playwright.sync_api import sync_playwright\n"
        "try:\n"
        "    with sync_playwright() as p:\n"
        "        p.chromium.launch(headless=True).close()\n"
        "        with sync_playwright() as q:   # nested: both suites alive\n"
        "            print('SECOND_OK')\n"
        "except Exception as e:\n"
        "    print('SECOND_FAILED:' + type(e).__name__)\n"
    )
    out = subprocess.run([sys.executable, "-c", probe], cwd=REPO,
                         capture_output=True, text=True, timeout=180).stdout
    assert "SECOND_OK" not in out, (
        "nested sync_playwright() contexts now work, so the three separate "
        "invocations in the Makefile could be merged into one -- but the port "
        "collision has to be resolved first, since two servers cannot bind 8421.")
    assert "SECOND_FAILED" in out, (
        f"the nested-context probe produced no verdict:\n{out}")


def test_no_pattern_kill_anywhere_in_the_harness_or_suites():
    """No process may be killed by matching its command line.

    `pkill -f <substring>` is the banned shape. The failure it causes is silent
    and total: the shell running the recipe contains the pattern in its own argv,
    so it kills itself and make reports a failure at the line after, pointing at
    nothing. `make e2e` did exactly that, on every invocation, and had never
    reached pytest.

    Checked on the PARSE TREE, not the text. The first two drafts were wrong in
    two different ways, and a mutation run found both:
      - a plain substring grep trips on harness.py's own docstring, which quotes
        the banned string to explain why it is banned;
      - stripping comments and docstrings before grepping removes *every* string
        literal -- and the pattern in `pkill -f 'bin/concord'` IS a string
        literal. That version passed against a harness with pkill re-added,
        which is the one thing it existed to prevent.

    So: walk the AST, find every subprocess/os call that spawns or signals, and
    assert none of its arguments is a kill pattern. A docstring is not a call.
    """
    for name in ("e2e_test.py", "finder_e2e.py", "harness.py"):
        tree = ast.parse(read(name), filename=name)
        for node in ast.walk(tree):
            if not isinstance(node, ast.Call):
                continue
            func = ast.unparse(node.func)
            if not re.search(r"(subprocess\.(run|Popen|call|check_output)"
                             r"|os\.(system|popen|exec\w*)|signal)", func):
                continue
            args = " ".join(ast.unparse(a) for a in node.args)
            args += " " + " ".join(
                f"{kw.arg}={ast.unparse(kw.value)}" for kw in node.keywords)
            for banned in ("pkill", "killall", "fuser -k"):
                if banned in args:
                    raise AssertionError(
                        f"{name}:{node.lineno} spawns a process with {func}() and "
                        f"a {banned} pattern in its arguments: {args[:120]}. "
                        f"It matches any process whose cmdline contains the "
                        f"pattern -- including the caller. Use the pidfile.")
            # A bare `kill -9 -f`-equivalent: killpg/kill with a signal and a
            # negative pid, or kill with a shell.
            if re.search(r"\bsystem\s*\(", args) or "killpg" in args:
                raise AssertionError(
                    f"{name}:{node.lineno} uses {func}() with {args[:120]}, which "
                    f"can signal processes this suite does not own")


def test_the_banned_pattern_check_would_catch_a_reintroduced_pkill():
    """Prove-guard: the check above is applied to a snippet that DOES pkill.

    A guard that has never been seen to fail is a guard of unknown power. This
    runs the same AST walk over a synthetic source and requires it to complain,
    so 'the check passes' can never mean 'the check does nothing'.
    """
    bad = (
        "import subprocess\n"
        "subprocess.run(['pkill', '-f', 'bin/concord'], check=False)\n"
    )
    with pytest.raises(AssertionError, match="pkill"):
        for name, src in [("synthetic.py", bad)]:
            tree = ast.parse(src, filename=name)
            for node in ast.walk(tree):
                if not isinstance(node, ast.Call):
                    continue
                func = ast.unparse(node.func)
                if not re.search(r"(subprocess\.(run|Popen|call|check_output)"
                                 r"|os\.(system|popen|exec\w*)|signal)", func):
                    continue
                args = " ".join(ast.unparse(a) for a in node.args)
                for banned in ("pkill", "killall", "fuser -k"):
                    if banned in args:
                        raise AssertionError(f"{name}: {banned} pattern")


def test_the_two_suites_use_different_ports():
    """They must be able to run in one invocation, which is what `make e2e` does."""
    ports = {}
    for name in ("e2e_test.py", "finder_e2e.py"):
        m = re.search(r"^PORT\s*=\s*(\d+)", read(name), re.M)
        assert m, f"{name} has no PORT constant"
        ports[name] = int(m.group(1))
    assert ports["e2e_test.py"] != ports["finder_e2e.py"], (
        f"both suites bind {ports['e2e_test.py']}; the second server cannot start "
        f"and every test in it errors")


def test_the_pidfile_is_outside_the_per_run_tempdir():
    """A pidfile in a fresh mkdtemp is never found again, so a leaked server
    outlives the run that started it and every later run fails with
    'port is held by pid N' -- a failure whose cause is in the past."""
    harness = read("harness.py")
    assert "def pidfile_for" in harness
    assert "tempfile.gettempdir()" in harness, (
        "the pidfile must be at a stable path, not inside the per-run tempdir")
    # Prove it is stable: two calls with the same port must agree.
    sys.path.insert(0, E2E)
    try:
        import harness as h
        assert h.pidfile_for(8421) == h.pidfile_for(8421)
        assert h.pidfile_for(8421) != h.pidfile_for(8422)
    finally:
        sys.path.remove(E2E)
        sys.modules.pop("harness", None)


def test_the_harness_refuses_to_kill_a_pid_that_is_not_concord():
    """A pidfile can outlive its process and the pid can be recycled.

    SIGTERMing an innocent process because a temp file said so is a worse bug
    than the leaked server this harness prevents, so the cmdline is checked
    first.

    The reap runs in a CHILD process, and that is the load-bearing detail. This
    test's first draft pointed the harness at `os.getpid()` -- the pytest
    process -- and asserted it survived. With the cmdline check removed, the
    harness SIGTERM'd the test runner: pytest died, printed nothing, and the
    mutation run recorded that as a PASS. A test that verifies survival by being
    alive cannot report the failure that kills it.

    So: a child writes the pidfile, calls reap(), and reports. The parent then
    checks the child exited cleanly -- which it can only do while still alive.
    """
    script = f'''
import os, sys
sys.path.insert(0, {E2E!r})
import harness
pf = {os.path.join(tempfile.gettempdir(), "concord-harness-selftest.pid")!r}
with open(pf, "w") as f:
    f.write(str(os.getpid()))
harness.reap(8421, pf)
print("REAP_RETURNED")
'''
    out = subprocess.run([sys.executable, "-c", script], cwd=REPO,
                         capture_output=True, text=True, timeout=60)
    assert "REAP_RETURNED" in out.stdout, (
        "harness.reap did not return -- it killed the process that called it. "
        f"stdout={out.stdout!r} stderr={out.stderr[-400:]!r}")
    assert out.returncode == 0, (
        f"the reap child exited {out.returncode}; it must check the pid's own "
        f"cmdline before signalling anything.")


def test_the_harness_names_the_port_holder_instead_of_killing_it():
    """When the port is held by a stranger, the error must say who holds it.

    `port_holder` is what turns 'concord exited immediately' -- which blames the
    wrong thing entirely -- into a message naming a pid and a command line.
    """
    sys.path.insert(0, E2E)
    try:
        import harness as h
        assert h.port_holder(8421) is None or isinstance(h.port_holder(8421), tuple)
        # Round-trip: a socket we bind must be found by our own reader.
        s = socket.socket()
        s.bind(("127.0.0.1", 0))
        s.listen(1)
        port = s.getsockname()[1]
        found = h.port_holder(port)
        assert found and found[0] == os.getpid(), (
            f"port_holder did not find a listener we just bound on {port}: {found}")
        s.close()
    finally:
        sys.path.remove(E2E)
        sys.modules.pop("harness", None)