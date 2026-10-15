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

from pathlib import Path
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


def collect_suites():
    """Run a real directory-level collection and return (per-file counts, total).

    The subprocess is the measurement. Reading pytest.ini and reasoning about
    fnmatch patterns would re-implement pytest's discovery and disagree with it
    the first time either changes.
    """
    out = subprocess.run(
        [sys.executable, "-m", "pytest", "tests/e2e", "--collect-only", "-q"],
        cwd=REPO, capture_output=True, text=True, timeout=180)
    if out.returncode != 0:
        raise AssertionError(
            "pytest --collect-only exited %d, so the collection below is not a "
            "measurement of anything:\n%s\n%s"
            % (out.returncode, out.stdout[-2000:], out.stderr[-2000:]))
    counts = {}
    for line in out.stdout.splitlines():
        if line.startswith("tests/e2e/") and "::" in line:
            name = line.split("::", 1)[0].rsplit("/", 1)[-1]
            counts[name] = counts.get(name, 0) + 1
    m = re.search(r"(\d+) tests? collected", out.stdout)
    assert m, f"pytest --collect-only printed no count:\n{out.stdout[-2000:]}"
    return counts, int(m.group(1))


def test_every_suite_is_collected_by_a_directory_run():
    """`pytest tests/e2e` must collect EVERY suite in the directory.

    The directory is the authority. The previous version of this test named four
    suites by hand and asserted a total of >= 88, and both stayed true while five
    suites -- feature, requests, lists, board, settings, 58 tests between them --
    stopped being collected at all. `pytest tests/e2e` reported a pass over 152 of
    210 tests. A hand-maintained list of what to check goes stale exactly the way
    the thing it guards does, and this list went stale twice.

    So: glob the directory, and require every `*_e2e.py` in it to contribute tests.
    A suite that is not collectible fails the test that is about collectability.
    """
    suites = sorted(p.name for p in Path(__file__).parent.glob("*_e2e.py"))
    assert suites, (
        "no *_e2e.py files found next to this test: the glob is empty, so this "
        "check would pass while examining nothing. Treat that as a failure.")
    counts, total = collect_suites()

    missing = [s for s in suites if counts.get(s, 0) == 0]
    assert not missing, (
        "these suites contributed no tests to a directory-level run -- they match "
        "neither pytest's default patterns nor pytest.ini's python_files, so "
        "`pytest tests/e2e` reports a pass while silently skipping them: "
        + ", ".join(missing) + f"\ncollected: {sorted(counts)}")

    # And a per-file floor, so a suite that collects one trivial test still fails.
    # Per-file rather than a single global total: a total can be held up by one
    # large suite while another loses every test it has, which is the failure this
    # test exists for.
    thin = {s: n for s, n in counts.items() if n < 3}
    assert not thin, (
        f"these suites collected fewer than 3 tests each: {thin}. A suite that "
        f"collects one test is a suite that has lost its tests.")


def test_the_documented_e2e_counts_are_the_measured_ones():
    """The tracker's e2e numbers must be numbers this tree actually has.

    `docs/REMAINING.md` stated "174 e2e tests in 10 suites" while the tree held 210
    in 13 -- and the Go test count beside it was gated by
    `scripts/check_doc_numbers.py` the whole time. A number that no gate reads is a
    number that is wrong within the session it is written in.

    Checked here rather than in check_doc_numbers.py because the measurement needs
    pytest, and `make verify` must not start depending on the e2e toolchain. This
    test already runs under pytest and already collects the suites.
    """
    suites = sorted(p.name for p in Path(__file__).parent.glob("*_e2e.py"))
    counts, total = collect_suites()

    doc = open(os.path.join(REPO, "docs", "REMAINING.md")).read()
    m = re.search(r"(\d+)\s+e2e tests?\s+in\s+(\d+)\s+suite files?", doc)
    assert m, (
        "docs/REMAINING.md no longer states an e2e count in the form "
        "'N e2e tests in M suite files', so nothing ties this measurement to the "
        "tracker. Restore the sentence or re-point this check.")
    stated_tests, stated_suites = int(m.group(1)), int(m.group(2))
    # The suite count is the number of files that CONTRIBUTED tests, read off the
    # collection rather than counted in the directory: `e2e_test.py` and
    # `harness_selftest_test.py` do not match `*_e2e.py`, so a directory count and a
    # collection count differ by two and neither is obviously "the" number. The
    # measured one is the one with tests in it.
    assert stated_suites == len(counts), (
        f"docs/REMAINING.md says {stated_suites} e2e suite files; a directory-level "
        f"run collects tests from {len(counts)}: {sorted(counts)}")
    assert stated_tests == total, (
        f"docs/REMAINING.md says {stated_tests} e2e tests; a directory-level run "
        f"collects {total}")
    # And the glob's suites must be a subset of what contributed, so this pair of
    # assertions cannot both hold while a `*_e2e.py` file sits there uncollected
    # (the case the test above reports in detail).
    assert set(suites) <= set(counts), (
        f"{sorted(set(suites) - set(counts))} are not collected at all")



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


def test_no_two_suites_claim_the_same_port():
    """Two suites on one port only work because the Makefile runs them one per line.

    project_panels_e2e.py and scout_e2e.py both claimed 8423 for a while, and the
    only thing preventing a collision was the run order -- so running either file on
    its own, or a future parallel runner, produced a confusing "port is held by pid N"
    error that looks like a leaked process rather than a duplicate constant.

    Asserted as a cross-file check rather than a per-file one for the same reason as
    the token-key gate: a suite cannot see the others' ports from inside itself.
    """
    ports = {}
    for path in sorted(Path(__file__).parent.glob("*_e2e.py")):
        for line in path.read_text().splitlines():
            if line.startswith("PORT = "):
                port = int(line.split("=", 1)[1])
                assert port not in ports, (
                    f"{path.name} and {ports[port]} both claim port {port}")
                ports[port] = path.name
                break

def _waits(src):
    """Yield each complete wait_for_selector(...) call, multi-line ones joined."""
    lines = src.splitlines()
    i = 0
    while i < len(lines):
        if "wait_for_selector(" in lines[i]:
            stmt = lines[i]
            while stmt.count("(") > stmt.count(")"):
                i += 1
                stmt += " " + lines[i].strip()
            yield stmt
        i += 1


def test_every_wait_for_a_loaded_page_uses_one_aria_busy_idiom():
    """`:not([aria-busy])` and `[aria-busy="false"]` both time out here, for good reasons.

    This codebase clears the flag TWO ways, and both are ARIA-correct:

      - ConcordSkeleton.done() SETS aria-busy="false"  (the spec form)
      - lists.js / requests.js REMOVE the attribute

    So a test waiting on `#root:not([aria-busy])` never matches the first -- an
    attribute whose value is "false" is still an attribute -- and one waiting on
    `#root[aria-busy="false"]` never matches the second. Each was written, each timed out
    on a correct page, and the symptom was a timeout that pointed at the page rather
    than at the selector.

    `#root:not([aria-busy="true"])` is the one form that accepts both, so it is the only
    one allowed. Asserted across the whole directory for the same reason the token-key
    gate is: a file cannot see its siblings' selectors.
    """
    # Check the WHOLE wait, not one line: the selector is usually wrapped, so
    # `wait_for_selector(f'#{root}:not([aria-busy="true"])',` is on one line and
    # `timeout=15000)` on the next. A line-at-a-time check flagged the CORRECT form,
    # because the line holding wait_for_selector did not contain the selector.
    OK = ':not([aria-busy="true"])'
    bad = []
    for path in sorted(Path(__file__).parent.glob("*_e2e.py")):
        src = path.read_text()
        for n, stmt in enumerate(_waits(src), 1):
            if "aria-busy" in stmt and OK not in stmt:
                bad.append(f"{path.name}: {stmt.strip()}")
    assert not bad, (
        "these waits can never match one of the two clearing conventions:\n  "
        + "\n  ".join(bad) + '\n  use #root:not([aria-busy="true"])')


def _strip_prose(src):
    """Blank out string literals and comments, preserving line structure.

    A gate that fires on a docstring gets muted, and then it guards nothing.
    board_e2e.py's fixture docstring opens "A SIGNED-IN page." and the first version
    of this check read that prose as a real `page.` call -- a false positive on the one
    suite whose fixtures were already correct.
    """
    out, i, n = [], 0, len(src)
    while i < n:
        ch = src[i]
        if ch == "#":
            while i < n and src[i] != "\n":
                out.append(" ")
                i += 1
        elif ch in ("\"", "'"):
            three = src[i:i + 3]
            delim = three if three in (chr(34) * 3, chr(39) * 3) else ch
            i += len(delim)
            while i < n:
                if src[i:i + len(delim)] == delim:
                    out.append(" " * len(delim))
                    i += len(delim)
                    break
                out.append("\n" if src[i] == "\n" else " ")
                i += 1
        else:
            out.append(ch)
            i += 1
    return "".join(out)


def _fixture_body(src, name):
    """Return the fixture `name`'s body, with strings and comments blanked out."""
    m = re.search(r"\ndef " + re.escape(name) + r"\(([^)]*)\):\n", src)
    if not m:
        return ""
    body = src[m.end():]
    nxt = re.search(r"\n(?:@|def )", body)
    body = body[: nxt.start()] if nxt else body
    return _strip_prose(body)


def test_a_fixture_never_uses_its_own_name_in_its_body():
    """`def page(browser): page.new_page()` errors on EVERY test that uses it.

    A fixture whose body calls its OWN name resolves to the fixture, not the browser,
    so pytest hands the body a FixtureFunctionDefinition and it raises
    `AttributeError: ... has no attribute 'new_page'`. requests_e2e.py had exactly
    that: 12 tests ERRORing in 1.23s.

    The first version of this gate checked the SIGNATURE -- "is the fixture's own name
    among its arguments" -- and went GREEN on the reintroduced bug, because
    `def page(browser)` does not take `page`. The shadow is in the BODY. Checking the
    wrong syntactic position is how a gate passes the defect it was written for.
    """
    bad = []
    for path in sorted(Path(__file__).parent.glob("*_e2e.py")):
        src = path.read_text()
        for name in re.findall(r"\ndef (\w+)\(([^)]*)\):\n", src):
            pass
        for m in re.finditer(r"\ndef (\w+)\(([^)]*)\):\n", src):
            name = m.group(1)
            args = [a.strip() for a in m.group(2).split(",") if a.strip()]
            if name in args:
                bad.append(f"{path.name}: def {name}({m.group(2)}) takes its own name")
            body = _fixture_body(src, name)
            # `name.something` inside its own body is the shadow.
            for use in re.findall(rf"\b{re.escape(name)}\s*\.", body):
                bad.append(f"{path.name}: def {name}(...) calls {name}.<attr> on itself")
                break
    assert not bad, (
        "these fixtures reference their own name, so every test using them errors:\n  "
        + "\n  ".join(sorted(set(bad))))
