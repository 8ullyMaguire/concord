#!/usr/bin/env python3
"""Mutation gate driver, shared by all three Concord gates.

    ./mutate.py <src-file> <test-package> <test-file> <mutants.json>

Two bugs it exists to prevent, both paid for in this session:

1.  `IFS='|' read -r name old new` on a `name|old|new` line. A Go pattern
    containing `||` splits into nine fields, so `old` and `new` are the wrong
    pieces and the gate prints a mutant NAME that is not the mutant it ran. A
    gate that reports findings about code it never mutated is worse than no
    gate. Here the mutants live in JSON, so the pipe problem cannot exist.

2.  `go test ... | grep -q FAIL` under `set -o pipefail`. grep exits at its first
    match, SIGPIPEs go test, and the pipeline reports 141 — so every mutant
    reads SURVIVED. The capabilities gate reported 0/8 on a suite that kills 6.
    Output is captured to a variable and searched in Python instead.

The third rule is reporting honesty: a pattern that is absent, or a substitution
that does not take, is reported NOT APPLIED. Never SURVIVED. The two are
indistinguishable from the output alone, and conflating them is what makes a
reader stop believing the gate.
"""
import atexit
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile

# `go test` prints "[build failed]" (and "# pkg" above it) when the mutation does not
# compile, which is a different verdict from a test going red.
BUILD_FAILED = re.compile(r"\[build failed\]|build failed|declared and not used|cannot use|undefined:")


def _restore(src_path, original):
    """Put the source back, and prove it landed.

    A restore that silently fails is indistinguishable from a restore that
    worked, and the next gate run then backs up the corruption as its baseline
    -- which is how a mutation harness starts reporting results about code that
    is not the code under test. The comparison is the point, not the write.
    """
    if open(src_path).read() != original:
        open(src_path, "w").write(original)
    return open(src_path).read() == original


def run_gate(src, test_pkg, test_file, mutants, label, repo=None):
    repo = repo or os.path.expanduser("~/code/projects/concord")
    src_path = os.path.join(repo, src)
    original = open(src_path).read()
    bak = tempfile.mktemp()
    shutil.copy(src_path, bak)

    # PREFLIGHT: does the file already carry one of these mutants?
    #
    # This harness writes each mutation into the source and restores it after
    # the test run. Nothing protected the window in between: a run killed by a
    # tool timeout, a Ctrl-C or SIGTERM -- i.e. exactly what happens when the
    # gate is run under a command timeout, which is the normal way to run it --
    # left the mutant in the tree. The symptom is not "the gate failed": it is
    # unrelated tests failing later, in a file the gate never names, because
    # `return 1.0` where `float64(cc.Consent) / float64(n)` belongs is a
    # plausible-looking line in a 2000-line file (it happened: governance.go's
    # SupportRatio() carried a survivor into the next test run and produced
    # three failures in ratio tests that had passed twenty minutes earlier).
    #
    # Worse than the leftover is what the next run does with it: `original` is
    # read from disk, so a stale mutant becomes the baseline to restore TO, and
    # every later verdict is a statement about the wrong source.
    for m in mutants:
        if m["new"] in original and m["old"] not in original:
            print(f"[{label}] REFUSING TO RUN: the source already carries the mutant "
                  f"{m['name']!r}. A previous run was interrupted between writing a "
                  f"mutation and restoring it. Restore {src} (git checkout or by hand) "
                  f"and run again -- results from this tree would describe code that is "
                  f"not the code under test.")
            return 1

    # RESTORE ON INTERRUPT: SIGTERM/SIGINT/SIGHUP and uncaught exceptions run
    # the same restore the normal path does. SIGKILL cannot be caught, which is
    # why the preflight above -- not this -- is the real guarantee.
    def _emergency(*_a):
        _restore(src_path, original)
        sys.exit(1)
    for _sig in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        try:
            signal.signal(_sig, _emergency)
        except (ValueError, OSError):
            pass
    atexit.register(_restore, src_path, original)

    # Derive the suite from the test file. A hand-written -run list is how the
    # first gate reported two mutants SURVIVED that its own suite could never
    # have caught: the pattern matched 5 of 12 tests.
    #
    # MULTIPLE test files, comma-separated. `test_file` used to be a single path
    # and that was a wiring trap: adding a store method in a new
    # `<domain>_count_test.go` and pointing the gate at `<domain>_test.go` armed
    # 24 tests, none of which called the new method, and every mutant for it
    # SURVIVED. That output is indistinguishable from missing coverage, so it
    # reads as a test problem and sends you to write a redundant test. It was a
    # harness problem. Gate on the files that hold the tests for the code.
    files = [f.strip() for f in test_file.split(",") if f.strip()]
    names, declared = [], 0
    for f in files:
        names.append(subprocess.run(
            f"grep -o '^func Test[A-Za-z0-9_]*' {f} | sed 's/func //'",
            shell=True, cwd=repo, capture_output=True, text=True).stdout)
        declared += int(subprocess.run(f"grep -c '^func Test' {f}",
                                       shell=True, cwd=repo,
                                       capture_output=True, text=True).stdout.strip() or 0)
    suite = "|".join(n for n in "".join(names).split() if n)
    if not suite:
        print(f"[{label}] NO TESTS DERIVED from {files}; the gate would report "
              f"every mutant SURVIVED without having run anything.")
        return 1

    v = subprocess.run(f"go test {test_pkg} -run '{suite}' -count=1 -v",
                       shell=True, cwd=repo, capture_output=True, text=True)
    armed = len(re.findall(r"^(?:    )?--- PASS", v.stdout, re.M))
    print(f"[{label}] armed {armed}/{declared} declared tests")
    if armed < declared:
        print(f"[{label}] FAIL: under-measuring — the gate would report survivors "
              f"it never ran. Fix the pattern derivation before reading any result.")
        return 1

    base = subprocess.run(f"go test {test_pkg} -run '{suite}' -count=1",
                          shell=True, cwd=repo, capture_output=True, text=True)
    if base.returncode != 0:
        print(f"[{label}] BASELINE FAILS — fix the suite before reading mutants")
        print(base.stdout[-1500:])
        return 1

    killed = survived = notapplied = 0
    for m in mutants:
        name, old, new = m["name"], m["old"], m["new"]
        if old not in original:
            print(f"  NOT APPLIED  {name} (pattern absent)")
            notapplied += 1
            continue
        text = original.replace(old, new, 1)
        open(src_path, "w").write(text)
        if new not in text:
            print(f"  NOT APPLIED  {name} (substitution did not take)")
            notapplied += 1
            open(src_path, "w").write(original)
            continue
        r = subprocess.run(f"go test {test_pkg} -run '{suite}' -count=1",
                           shell=True, cwd=repo, capture_output=True, text=True)
        out = r.stdout + r.stderr
        # A mutation that does not BUILD is not a kill. `go test` exits non-zero for a
        # compile error exactly as it does for a failing test, so "non-zero therefore
        # killed" reports a rule as proven when the mutation never reached it -- the
        # same way the UI gate reported seven kills it had not earned (a pytest that
        # could not import). Counted with NOT APPLIED, which fails the gate.
        build_error = BUILD_FAILED.search(out)
        if build_error:
            print(f"  UNUSABLE     {name} (the mutation does not build, so nothing "
                  f"was tested: {build_error.group(0).strip()})")
            notapplied += 1
            open(src_path, "w").write(original)
            continue
        if "FAIL" in out:
            print(f"  killed       {name}")
            killed += 1
        else:
            print(f"  SURVIVED     {name}")
            survived += 1
        open(src_path, "w").write(original)

    total = len(mutants)
    print(f"[{label}] ---- {killed}/{total} killed, {survived} survived, {notapplied} not applicable")
    if not _restore(src_path, original):
        print(f"[{label}] FAIL: the source could not be restored after the run. The tree "
              f"is now a mutant and every later reading of it is wrong.")
        return 1
    os.remove(bak)

    # A NOT APPLIED mutant fails the gate, deliberately. It is the one verdict
    # that is indistinguishable from a pass at a glance: before this change a
    # gate whose mutants were all stale patterns reported
    #
    #     ---- 0/1 killed, 0 survived, 1 not applicable
    #     GATE_EXIT=0
    #
    # which reads as "no survivors" and means "nothing ran". Verified by
    # execution, not by argument: a mutant naming a pattern absent from
    # capabilities.go exited 0 on a suite that kills 6 of its own mutants.
    # A refactor silently renaming the guarded expression is exactly the common
    # case, so this is the state a gate spends most of its life in after any
    # code motion -- and it was green.
    #
    # The failure is not cosmetic either: a refactor that renames the guarded
    # expression leaves the RULE unproven while the gate says it is proven, and
    # the next person to read the mutant list has no way to know.
    #
    # An UNUSABLE mutant (one that does not build) is counted here too, for the
    # same reason and one more: a compile error also exits non-zero, so counting
    # it as a kill claims a rule is enforced by a test that never ran.
    if notapplied:
        print(f"[{label}] FAIL: {notapplied} mutant(s) never applied or did not build. "
              f"The rules they guard are UNPROVEN and this run proves nothing about "
              f"them. Update the patterns in the mutants file to the current source "
              f"(or make the mutation compile).")
    return 0 if (survived == 0 and notapplied == 0) else 1


def main():
    if len(sys.argv) != 5:
        print(__doc__)
        return 2
    src, pkg, testfile, mutfile = sys.argv[1:5]
    mutants = json.load(open(mutfile))
    label = os.path.basename(src)
    return run_gate(src, pkg, testfile, mutants, label)


if __name__ == "__main__":
    sys.exit(main())