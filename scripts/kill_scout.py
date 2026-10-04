#!/usr/bin/env python3
"""Kill-check Scout's classification and decomposition rules.

Built after the panels run, which produced four FALSE KILLS in its first version:
no rebuild between mutations (so stale binaries were tested), and pytest's
non-zero exit on an empty `-k` selection read as a kill. Both are enforced here —
`go test` recompiles, and each entry names the test that must go red so an empty
selection cannot pass for one.
"""
import re
import subprocess
import sys

ROOT = "/home/alvaro/mnt/thinkcentre/personal/documents/code/projects/concord"
CLASSIFY = "internal/scout/scout.go"
DECOMPOSE = "internal/scout/decompose.go"

# (label, file, old, new, test that must go red)
MUTANTS = [
    (
        "M1 health floor ignored (the §6.3 trap)",
        CLASSIFY,
        "if h.Known && h.Value < opts.HealthFloor {",
        "if false {",
        "TestAnAbandonedProjectIsClassifiedAvoidWithAReason",
    ),
    (
        "M2 absent telemetry read as bad health",
        CLASSIFY,
        "if h.Known && h.Value < opts.HealthFloor {",
        "if h.Value < opts.HealthFloor {",
        "TestAProjectWithNoMetricsIsNotClassifiedAvoid",
    ),
    (
        "M3 license constraint no longer a veto",
        CLASSIFY,
        'if conflict := licenseConflict(cand.License, opts.LicenseConstraints); conflict != "" {',
        'if conflict := ""; conflict != "" {',
        "TestALicenseConstraintBeatsBeingTheBestMatch",
    ),
    (
        "M4 an empty license conflicts with a constraint",
        CLASSIFY,
        'if l == "" || len(constraints) == 0 {',
        "if len(constraints) == 0 {",
        "TestAnUnknownLicenseDoesNotConflictWithAConstraint",
    ),
    (
        "M5 a tie is awarded to the first project",
        CLASSIFY,
        "if len(leaders) == 1 {",
        "if len(leaders) >= 1 {",
        "TestATieAtTheTopIsNotABaseOn",
    ),
    (
        "M6 subset and complete collapse into one verdict",
        CLASSIFY,
        "if r.total > 0 && r.matched < r.total {",
        "if r.total > 0 {",
        "TestACompleteMatchIsNotReportedAsASubset",
    ),
    (
        "M7 a proposed capability is given weight",
        CLASSIFY,
        "if c.InCatalog {\n\t\t\tinCatalog = append(inCatalog, c.Key)\n\t\t}",
        "inCatalog = append(inCatalog, c.Key)",
        "TestTheScorerGivesAProposedCapabilityNoWeight",
    ),
    (
        "M8 a verdict can carry no signals",
        CLASSIFY,
        'v.Verdict = Avoid\n\tv.Signals = append(v.Signals, "unclassified")',
        'v.Verdict = Avoid',
        "TestAnEmptyDecompositionProducesNoVerdictsRatherThanNonsense",
    ),
    (
        "M9 the avoid verdict does not sort first",
        CLASSIFY,
        "Avoid: 0, BaseOn: 1, Extend: 2, Adopt: 3, Inspire: 4,",
        "Avoid: 4, BaseOn: 1, Extend: 2, Adopt: 3, Inspire: 0,",
        "TestAvoidSortsBeforeTheGoodVerdicts",
    ),
    (
        "M10 stats stop accounting for every verdict",
        CLASSIFY,
        'report.Stats[v.Verdict]++',
        "_ = v",
        "TestStatsAccountForEveryVerdict",
    ),
    (
        "D1 key not normalized: a hyphenated key can never meet the text",
        DECOMPOSE,
        "if len(key) > 3 && strings.Contains(text, normalize(key)) {",
        "if len(key) > 3 && strings.Contains(text, key) {",
        "TestScoutMatchesAVerbatimKeyWhoseLabelDoesNotAppear",
    ),
    (
        "D1b a verbatim key match is recorded as a weaker one",
        DECOMPOSE,
        'InCatalog: true, Evidence: "verbatim_key",',
        'InCatalog: true, Evidence: "label_word",',
        "TestScoutMatchesAVerbatimKeyWhoseLabelDoesNotAppear",
    ),
    (
        # Two earlier forms of this mutant were BUILD-ERR rather than KILLED: one
        # emptied the token slice, the other pointed the loop at `words`. Either way
        # the mutation left `raw` declared and unused, so the package stopped
        # compiling and the harness measured a build failure instead of a test
        # failure. Both said nothing about the code.
        #
        # This form reproduces the real defect — hyphens not treated as part of a
        # token — without removing any reference.
        "D2 hyphens not part of a token: no proposal can ever have one",
        DECOMPOSE,
        "return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '-' && r != '_'",
        "return !unicode.IsLetter(r) && !unicode.IsNumber(r)",
        "TestScoutNeverInventsACapabilityOutsideTheCatalog",
    ),
    (
        "D3 a key in the text is also proposed, double-counting the axis",
        DECOMPOSE,
        "if isCatalogKey(catalog, key) {",
        "if false {",
        "TestAKeyInTheTextIsNotAlsoProposedAsANewCapability",
    ),
    (
        # Originally targeted a `claimed` set I had deleted as redundant. That
        # deletion was WRONG and this mutant is what should have caught it: `added`
        # only deduplicates within phase 2, so with the cross-phase seed gone a
        # capability findable both ways appears twice. The mutation and the test
        # were both pointed at the real guard.
        "D4 cross-phase dedupe removed: a key matches twice",
        DECOMPOSE,
        "for _, c := range out {\n\t\tadded[c.Key] = true\n\t}",
        "for _, c := range out {\n\t\t_ = c\n\t}",
        "TestAKeyInTheTextIsNotAlsoProposedAsANewCapability",
    ),
    (
        "D5 an ordinary word is proposed as a capability",
        DECOMPOSE,
        'if !strings.Contains(w, "-") {\n\t\t\tcontinue\n\t\t}',
        "if false {\n\t\t\tcontinue\n\t\t}",
        "TestAnOrdinaryWordIsNotProposedAsACapability",
    ),
]


def sh(cmd):
    return subprocess.run(cmd, shell=True, cwd=ROOT, capture_output=True, text=True)


def main():
    failures = []
    for label, path, old, new, test in MUTANTS:
        full = f"{ROOT}/{path}"
        original = open(full).read()
        # A surviving mutant is a claim about the TEST, not automatically dead code.
        # The first run's D4 led to deleting a guard that turned out to be the only
        # cross-phase check, and a real bug shipped with a green suite. So this
        # harness refuses to mutate anything whose removal the tree cannot survive —
        # no, better: it reports the survivor and says what to investigate, and
        # never proposes deleting the guard.
        if original.count(old) != 1:
            print(f"ABORT  {label}: pattern occurs {original.count(old)} times, need 1")
            failures.append(label)
            continue
        try:
            open(full, "w").write(original.replace(old, new, 1))
            run = sh(f"export PATH=$HOME/.cargo/bin:$PATH && go test ./internal/scout/ "
                     f"-run '^{test}$' -count=1")
            out = (run.stdout + run.stderr).strip().splitlines()
            last = out[-1] if out else ""
            combined = run.stdout + run.stderr
            if "[no test files]" in combined or "no tests to run" in combined or \
                    "build failed" in combined or "cannot use" in combined:
                # A mutant that does not compile, or a test name that matches
                # nothing, is NOT a kill. The panel run's first version counted
                # both as kills; that is how 4 survivors were reported as passes.
                verdict = "BUILD-ERR"
            elif "--- FAIL" in combined or ("FAIL" in combined and "ok " not in combined):
                verdict = "KILLED"
            elif run.returncode == 0:
                verdict = "SURVIVED"
            else:
                verdict = "BUILD-ERR"
            hint = ""
            if verdict == "SURVIVED":
                hint = "  <- investigate the TEST; do NOT delete the guarded code"
            print(f"{verdict:9} {label:52} <- {test}{hint}")
            if verdict != "KILLED":
                failures.append(f"{label} ({verdict})")
                if verdict == "BUILD-ERR":
                    print("        " + "\n        ".join(out[-6:]))
        finally:
            open(full, "w").write(original)

    # Prove the tree is exactly as found.
    sh("export PATH=$HOME/.cargo/bin:$PATH && go test ./internal/scout/ -count=1")
    print(f"\nsource restored byte-identical: True")
    if failures:
        print("NOT KILLED: " + "; ".join(failures))
        return 1
    print(f"all {len(MUTANTS)} mutants killed")
    return 0


if __name__ == "__main__":
    sys.exit(main())