"""Shared runner for the UI mutation gates.

`internal/mutate.py` rewrites Go source and runs `go test`. The UI gates here rewrite a
page's JAVASCRIPT, rebuild the binary that embeds it, and run the browser suite -- which
is the only thing that can kill them, because every one of these mutants is a client-side
defect and the Go side serves the same 200 either way.

WHY THIS FILE EXISTS. The first version of the audit gate called `python3 -m pytest`
from `subprocess`, and in an environment where that interpreter has no pytest it got a
non-zero exit code and reported the mutant "killed". Seven false kills in a row, printed
with an EMPTY tail -- the tail was empty because pytest never printed anything, and the
empty string in parentheses was the only visible hint. Two rules follow, and they are the
reason this is one shared runner rather than a copy per page:

  1. run pytest with the interpreter that is running this script (`sys.executable`), or
     the one named by `E2E_PYTHON` when the harness sets it;
  2. require EVIDENCE THAT PYTEST RAN. A kill is only a kill if the run reported test
     results; anything else is INCONCLUSIVE and fails the gate. "The command exited
     non-zero" is how a broken toolchain impersonates a killed mutant.
"""

import os
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

# The interpreter that can import pytest. `sys.executable` is right when the gate is run
# by a pytest-capable python; E2E_PYTHON is for the Makefile, which invokes the gate with
# whatever `python3` is on PATH.
PYTHON = os.environ.get("E2E_PYTHON") or sys.executable


def run(cmd, timeout):
    return subprocess.run(cmd, cwd=REPO, capture_output=True, text=True, timeout=timeout)


def _pytest_ran(stdout: str, stderr: str) -> bool:
    """True when the run reported test results, whichever way it ended."""
    blob = stdout + stderr
    return "passed" in blob or "failed" in blob or "error" in blob.lower()


def run_mutants(js_rel, suite_rel, mutants, label):
    """Mutate `js_rel` once per entry, rebuild, run `suite_rel`, and require each corpse.

    Returns 0 only when every mutant was killed AND the source was restored
    byte-identically. A mutant that could not be applied is a failure: it means the
    pattern has moved and the gate is no longer guarding what its name says.
    """
    js = os.path.join(REPO, js_rel)
    original = open(js).read()
    # A backup of the SOURCE, byte-compared on restore. A backup taken from a tree
    # already carrying a previous run's mutation is how a gate reports "restore
    # verified" against its own corruption.
    with tempfile.NamedTemporaryFile("w", suffix=".js", delete=False) as fh:
        fh.write(original)
        backup = fh.name

    killed, survived, not_applied, inconclusive = [], [], [], []
    try:
        for name, old, new in mutants:
            mutated = original.replace(old, new)
            if mutated == original:
                not_applied.append(name)
                print(f"  NOT APPLIED  {name}")
                continue
            open(js, "w").write(mutated)
            try:
                build = run(["go", "build", "-o", "bin/concord", "./cmd/concord"], 400)
                if build.returncode != 0:
                    survived.append(name)
                    print(f"  BUILD-RED    {name}")
                    continue
                # The binary embeds the JS, so a stale bin is not a flake, it is a false
                # PASS. Rebuilt every time above for exactly that reason.
                res = run([PYTHON, "-m", "pytest", "-p", "no:cacheprovider",
                           suite_rel, "-q", "--no-header"], 900)
                tail = [l for l in res.stdout.strip().split("\n") if l.strip()]
                last = tail[-1] if tail else ""
                if not _pytest_ran(res.stdout, res.stderr):
                    inconclusive.append(name)
                    print(f"  NOT RUN      {name}   (pytest produced no result: {last!r})")
                    continue
                if res.returncode == 0:
                    survived.append(name)
                    print(f"  SURVIVED     {name}")
                else:
                    killed.append(name)
                    print(f"  killed       {name}   ({last})")
            finally:
                open(js, "w").write(original)

        # The real binary from the real source, and a byte-compared restore.
        run(["go", "build", "-o", "bin/concord", "./cmd/concord"], 400)
        restored = open(js).read() == open(backup).read() == original
        print(f"\n  source restored byte-identical: {restored}")
        print(f"[{label}] ---- {len(killed)}/{len(mutants)} killed, {len(survived)} survived, "
              f"{len(not_applied)} not applicable, {len(inconclusive)} not run")
        if survived or not_applied or inconclusive or not restored:
            print(f"[{label}] FAIL: the rules above are UNPROVEN. A surviving mutant is a "
                  f"missing fixture or a test that does not reach the rule; a mutant that "
                  f"was not applied or whose suite never ran proves nothing at all.")
            return 1
        return 0
    finally:
        open(js, "w").write(original)
        shutil.copy(backup, js)
        os.unlink(backup)
