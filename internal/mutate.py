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
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile


def run_gate(src, test_pkg, test_file, mutants, label, repo=None):
    repo = repo or os.path.expanduser("~/code/projects/concord")
    src_path = os.path.join(repo, src)
    original = open(src_path).read()
    bak = tempfile.mktemp()
    shutil.copy(src_path, bak)

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
        if "FAIL" in out:
            print(f"  killed       {name}")
            killed += 1
        else:
            print(f"  SURVIVED     {name}")
            survived += 1
        open(src_path, "w").write(original)

    total = len(mutants)
    print(f"[{label}] ---- {killed}/{total} killed, {survived} survived, {notapplied} not applicable")
    shutil.copy(bak, src_path)
    os.remove(bak)
    return 0 if survived == 0 else 1


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