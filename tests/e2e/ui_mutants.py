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

import atexit
import os
import shutil
import signal
import subprocess
import sys
import tempfile

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

# The interpreter that can import pytest. `sys.executable` is right when the gate is run
# by a pytest-capable python; E2E_PYTHON is for the Makefile, which invokes the gate with
# whatever `python3` is on PATH.
PYTHON = os.environ.get("E2E_PYTHON") or sys.executable

# THE CANDIDATE MODE, and why it is a mode rather than a second runner: the mutation gate is about the PAGE, so the two
# implementations that serve a page need the same eleven rules and only their build step differs. In the Go tree the asset
# is embedded by `go build -o bin/concord`; in the port it is embedded by `cargo build` through `include_bytes!`, and the
# binary the suites run is a COPY at `CONCORD_E2E_CANDIDATE`. A mutant that is written to the asset and not compiled into
# that copy is not a surviving mutant — it is a mutant nobody served, which would read as "killed" if the suite happened to
# fail for another reason and as "survived" if it passed. So the build is `cargo build --release -p concord-cli` and the
# install is a copy, both inside the loop.
CANDIDATE = os.environ.get("CONCORD_E2E_CANDIDATE", "")
# The binary `cargo build` produces is under the CHECKOUT's own `target/` unless CARGO_TARGET_DIR says otherwise — measured on the host
# the gate runs on, because the first version assumed this workstation's `~/.cache/cargo-target` and the gate died with
# `FileNotFoundError: .../concord-cli` on the very host that had just built it.
CANDIDATE_STAGE = os.environ.get(
    "CONCORD_E2E_CANDIDATE_BUILD",
    os.environ.get("CARGO_TARGET_DIR", os.path.join(REPO, "target")) + "/release/concord-cli",
)


def _build(js_rel):
    """Compile the mutated asset into whatever the suites will run.

    Returns `(ok, reason)`. The reason matters: a red build used to be recorded as a SURVIVED mutant, which is the opposite claim —
    a survivor says "the suite ran and did not notice", a red build says "nothing ran at all". One of them is a defended rule, the
    other is an outage, and folding the second into the first is how a flaky host turns into a false quality signal. Observed on
    thinkcentre: `cargo build` returned non-zero with EMPTY stderr (killed by the kernel under memory pressure from three other
    release builds), and the gate reported a survivor.
    """
    if os.environ.get("CONCORD_ASSET_DIR"):
        # The server reads the asset from disk (`crates/web/src/assets.rs`), so the mutation is already live and there is nothing to
        # compile. This is what makes the gate cheap enough to run: with the embed as the only source, nine mutants were nine relinks
        # of a 21 MB binary — four to six minutes each on a loaded host, one of them killed by the kernel and misfiled as a survivor.
        if not os.path.exists(CANDIDATE):
            return (False, f"no binary at {CANDIDATE}")
        return (True, "") 
    if not CANDIDATE:
        result = run(["go", "build", "-o", "bin/concord", "./cmd/concord"], 400)
        return (result.returncode == 0, f"go build exit {result.returncode}")
    result = run(["cargo", "build", "--release", "-p", "concord-cli"], 900)
    if result.returncode != 0:
        if result.stderr:
            sys.stderr.write(result.stderr[-2000:])
        return (False, f"cargo exit {result.returncode}" + ("" if result.returncode >= 0 else " (killed by a signal — not a code fault)"))
    # Rename, never copy over the target: `shutil.copy` onto a path whose previous inode is still mapped by a running server fails
    # with ETXTBSY, and the suite's server is killed asynchronously.
    staged = CANDIDATE + ".new"
    shutil.copy(CANDIDATE_STAGE, staged)
    os.chmod(staged, 0o755)
    os.replace(staged, CANDIDATE)
    return (True, "")


def run(cmd, timeout):
    return subprocess.run(cmd, cwd=REPO, capture_output=True, text=True, timeout=timeout)


def _pytest_ran(stdout: str, stderr: str) -> bool:
    """True when the run reported test results, whichever way it ended."""
    blob = stdout + stderr
    return "passed" in blob or "failed" in blob or "error" in blob.lower()


def say(*args):
    """Progress to a redirected log.

    `print` to a file is block-buffered, so a gate that takes fifteen minutes printed NOTHING until it exited — and the run that
    finally did exit had been killed by its outer timeout with an empty log, which reads as "it never started". Every line here is
    flushed.
    """
    print(*args, flush=True)


def run_mutants(js_rel, suite_rel, mutants, label):
    """Mutate `js_rel` once per entry, rebuild, run `suite_rel`, and require each corpse.

    Returns 0 only when every mutant was killed AND the source was restored
    byte-identically. A mutant that could not be applied is a failure: it means the
    pattern has moved and the gate is no longer guarding what its name says.
    """
    js = os.path.join(REPO, js_rel)
    original = open(js).read()

    # PREFLIGHT: does the page already carry one of these mutants?
    #
    # `original` is read from disk, so a mutation left behind by an interrupted run (or by
    # a hand-edit while debugging a survivor) becomes the state this harness restores TO,
    # and the byte-comparison at the end then reports "restored" for a corrupted file --
    # the check that exists to catch a bad restore ratifies it instead. Observed, not
    # imagined: the swap below was found in the tree and this gate kept reporting a clean
    # restore. Refuse to run against a source that already contains a mutant.
    for name, old, new in mutants:
        if new in original and old not in original:
            say(f"[{label}] REFUSING TO RUN: the page already carries the mutant "
                  f"{name!r}. Restore {js_rel} and run again -- a byte-compared restore "
                  f"would otherwise ratify it.")
            return 1

    def _emergency(*_a):
        open(js, "w").write(original)
        sys.exit(1)
    for _sig in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        try:
            signal.signal(_sig, _emergency)
        except (ValueError, OSError):
            pass
    atexit.register(lambda: open(js, "w").write(original))
    # A backup of the SOURCE, byte-compared on restore. A backup taken from a tree
    # already carrying a previous run's mutation is how a gate reports "restore
    # verified" against its own corruption.
    with tempfile.NamedTemporaryFile("w", suffix=".js", delete=False) as fh:
        fh.write(original)
        backup = fh.name

    killed, survived, not_applied, inconclusive, build_red = [], [], [], [], []
    try:
        for name, old, new in mutants:
            mutated = original.replace(old, new)
            if mutated == original:
                not_applied.append(name)
                say(f"  NOT APPLIED  {name}")
                continue
            open(js, "w").write(mutated)
            say(f"  running      {name}")
            try:
                built, why = _build(js_rel)
                if not built:
                    build_red.append(f"{name} ({why})")
                    say(f"  BUILD-RED    {name}   ({why}) — NOT a survivor: nothing ran")
                    continue
                # The binary embeds the JS, so a stale bin is not a flake, it is a false
                # PASS. Rebuilt every time above for exactly that reason.
                if CANDIDATE:
                    # `e2e_candidate.sh` recreates the scratch database, sets the candidate and its DSN, and runs
                    # the suite — the same wrapper a hand-run uses, so a mutant is judged by the same command.
                    # `-x`: stop at the FIRST failing test. Without it a killed mutant runs all fifteen tests, each waiting out its own
                    # fifteen-second expect, and one mutant costs four minutes — which is how this gate burned twenty-eight minutes and
                    # was killed by its outer timeout with an empty log. A surviving mutant still runs the whole suite.
                    res = run(["bash", "scripts/parity/e2e_candidate.sh", suite_rel, "-q", "-x"], 1200)
                else:
                    res = run([PYTHON, "-m", "pytest", "-p", "no:cacheprovider",
                               suite_rel, "-q", "--no-header"], 900)
                tail = [l for l in res.stdout.strip().split("\n") if l.strip()]
                last = tail[-1] if tail else ""
                if not _pytest_ran(res.stdout, res.stderr):
                    inconclusive.append(name)
                    say(f"  NOT RUN      {name}   (pytest produced no result: {last!r})")
                    continue
                if res.returncode == 0:
                    survived.append(name)
                    say(f"  SURVIVED     {name}")
                else:
                    killed.append(name)
                    say(f"  killed       {name}   ({last})")
            finally:
                open(js, "w").write(original)

        # The real binary from the real source, and a byte-compared restore.
        _build(js_rel)
        restored = open(js).read() == open(backup).read() == original
        say(f"\n  source restored byte-identical: {restored}")
        say(f"[{label}] ---- {len(killed)}/{len(mutants)} killed, {len(survived)} survived, "
              f"{len(not_applied)} not applicable, {len(inconclusive)} not run, "
              f"{len(build_red)} build-red")
        if survived or not_applied or inconclusive or build_red or not restored:
            say(f"[{label}] FAIL: the rules above are UNPROVEN. A surviving mutant is a "
                  f"missing fixture or a test that does not reach the rule; a mutant that "
                  f"was not applied or whose suite never ran proves nothing at all.")
            return 1
        return 0
    finally:
        open(js, "w").write(original)
        shutil.copy(backup, js)
        os.unlink(backup)
