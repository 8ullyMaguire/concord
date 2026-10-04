#!/usr/bin/env python3
"""Kill-check Scout's PAGE with browser mutations.

Built on the two lessons from kill_scout.py, both of which cost real time:

  - **Rebuild after EVERY mutation.** Go embeds assets at compile time, so
    mutating scout.js and running the prebuilt binary tests the UNMUTATED page and
    reports a fake kill. `make build` runs before each pytest here for that reason.
  - **Order the verdict checks: build-failure, then test-failure, then survived.**
    A mutant that does not compile is neither. pytest exiting non-zero on an
    empty `-k` selection reads as a pass, so each mutant names the test that must
    go red and the summary line is checked for it.

The page mutations here are the ones that matter for Scout specifically: the
honesty rules. Each of the three is a rule the API already enforces and a page
could quietly undo by rendering a tidier number.
"""
import re
import subprocess
import sys

ROOT = "/home/alvaro/mnt/thinkcentre/personal/documents/code/projects/concord"
JS = f"{ROOT}/internal/httpapi/assets/js/scout.js"
TPL = f"{ROOT}/internal/httpapi/templates/scout.html"
SUITE = "tests/e2e/scout_e2e.py"

# (label, path, old, new, test that must go red)
MUTANTS = [
    (
        "P1 an unmeasured health renders as a number",
        JS,
        "bits.push(v.health_known ? 'health ' + v.health.toFixed(2) : 'health not measured');",
        "bits.push('health ' + v.health.toFixed(2));",
        "testAnUnmeasuredProjectIsNotRenderedAsAZeroHealth",
    ),
    (
        # The first form of this mutant dropped the sample size from the rate and
        # the test SURVIVED: a project with reports == 0 never reaches that branch,
        # so the mutation changed nothing observable. The defect worth killing is
        # the one where an UNMEASURED rate is printed at all.
        "P2 an unmeasured outcome rate prints a rate anyway",
        JS,
        "} else if (v.reports === 0) {\n      bits.push('no field reports yet');\n    }",
        "} else {\n      bits.push('field reports ' + pct(v.outcome_rate));\n    }",
        "testNoFieldReportsIsNotRenderedAsZeroPercent",
    ),
    (
        # Anchored to the ON-SCREEN renderer. The stored-markdown one has the same
        # guard now that reports can be saved, so the bare pattern occurs twice and
        # the harness would ABORT rather than mutate. P14 covers the markdown side.
        "P3 signals are not rendered on screen",
        JS,
        "if (v.signals && v.signals.length) {\n      var sig = document.createElement('ul');",
        "if (false) {\n      var sig = document.createElement('ul');",
        "testATiedVerdictSaysWhoItTiesWith",
    ),
    (
        "P4 a proposed capability is not marked",
        JS,
        "tag.textContent = 'proposed';",
        "tag.textContent = '';",
        "testAProposedCapabilityIsMarkedAsProposed",
    ),
    (
        "P5 the decomposition is never rendered",
        JS,
        "renderCapabilities(data.capabilities || []);",
        "renderCapabilities([]);",
        "testTheDecompositionIsShownBeforeTheVerdicts",
    ),
    (
        "P6 evidence coverage is dropped from the facts line",
        JS,
        "bits.push(v.matched + ' of ' + v.total + ' capabilities (' +\n        pct(v.fit) + ', evidence ' + pct(v.evidence_coverage) + ')');",
        "bits.push(v.matched + ' of ' + v.total + ' capabilities (' + pct(v.fit) + ')');",
        "testAProjectWithNoAssertionsShowsNoEvidenceCoverage",
    ),
    (
        "P7 the license constraint stops reaching the API",
        TPL,
        'id="scout-exclude"',
        'id="scout-exclude-disabled"',
        "testALicenseConstraintExcludesAndThePageSaysSo",
    ),
    (
        # This one took THREE forms, and each failure was instructive:
        #
        #   1. Removing the `if (!idea)` guard. SURVIVED, and correctly: with the
        #      guard gone the page fetches an empty idea, the server answers 400,
        #      and `fail()` shows an error either way. The test was asserting the
        #      wrong thing -- "an empty idea is refused" holds both ways, because
        #      the SERVER refuses it too.
        #   2. Putting `q = 'idea='` before the fetch. ALSO SURVIVED, and for a
        #      dumber reason: the guard returns earlier, so that line is DEAD CODE
        #      in the unmutated file too. A mutant inside unreachable code cannot
        #      change any observable. Always check that the mutation is on a path
        #      the test actually executes.
        #   3. This form: neuter the guard itself, so the `q` assignment and the
        #      fetch after it DO become reachable.
        #
        # The claim under test is "no pointless request", which is the thing the
        # client guard actually buys, so this is the form that can kill it.
        "P8 an empty idea is fetched anyway",
        JS,
        "if (!idea) {\n      fail('Say what you want to build first.');\n      return;\n    }",
        "if (false) {\n      fail('Say what you want to build first.');\n      return;\n    }",
        "testAnEmptyIdeaMakesNoRequest",
    ),
    (
        "P9 an unmatched idea claims it matched something",
        JS,
        "if (!parts.length) {",
        "if (false) {",
        "testAnUnmatchedIdeaExplainsItselfRatherThanInventingVerdicts",
    ),
    (
        "P10 Scout renders as a bare heading (the /scout 404 shape)",
        TPL,
        "<h1>\U0001F52D Scout</h1>",
        "<h1>Not found</h1>",
        "testTheScoutPageLoadsAndIsNotA404",
    ),
    (
        "P11 the save sends the wrong kind",
        JS,
        "kind: 'scout',",
        "kind: 'plan',",
        "testAReportSavesAsADocumentOfKindScout",
    ),
    (
        # Targeted at the two-ideas test, not the repeat test. A constant slug
        # REPLACES just as cleanly when the same idea is saved twice, so the repeat
        # test cannot see this defect -- it passed with the mutation, and only
        # "two different ideas are two different documents" distinguishes them.
        "P12 the save slug is not derived from the idea, so a second report overwrites the first",
        JS,
        "return s ? ('scout-' + s) : 'scout-report';",
        "return 'scout-report';",
        "testTwoDifferentIdeasAreTwoDifferentDocuments",
    ),
    (
        "P13 a failed save leaves a stale success message",
        JS,
        ".catch(function (err) {\n      saveFail(err.message || 'Could not save the report.');",
        ".catch(function (err) {\n      saveFail('');",
        "testAFailedSaveDoesNotLeaveAStaleSuccessMessage",
    ),
    (
        "P14 the stored document drops the signals",
        JS,
        "out.push('Signals: ' + v.signals.map(function (s) { return '`' + s + '`'; }).join(', '));",
        "out.push('Signals: (omitted)');",
        "testASavedReportCarriesTheReasoning",
    ),
    (
        # This is the bug the browser found and no Go test could: scout.js had no
        # token()/headers() because its GET needs no auth, and the save path called
        # headers() the moment it was written. The page threw on click and every
        # read-only test still passed.
        "P15 the save posts without a bearer token",
        JS,
        "var t = token();\n    if (t) h.Authorization = 'Bearer ' + t;",
        "var t = null;\n    if (t) h.Authorization = 'Bearer ' + t;",
        "testAReportSavesAsADocumentOfKindScout",
    ),
    (
        # Going "back" without dropping lastReport would let a save file the
        # PREVIOUS idea under whatever project is typed next.
        "P16 going back keeps the old report so it can be saved by mistake",
        JS,
        "lastReport = null;\n      show(report, false);",
        "show(report, false);",
        "testGoingBackClearsTheReportSoItCannotBeSavedByMistake",
    ),
]


def sh(cmd, timeout=600):
    return subprocess.run(cmd, shell=True, cwd=ROOT, capture_output=True,
                          text=True, timeout=timeout)


def classify(run, test):
    """KILLED / SURVIVED / BUILD-ERR / NO-TESTS. In that order, never optimistically.

    The bug this replaces: the first version fell through to `"error" in
    combined.lower()` and scored every CLEAN pytest summary as BUILD-ERR. pytest -q
    on a passing selection ends in a line like `1 passed, 15 deselected in 4.57s`,
    and the word "deselected" contains no "error" -- but the harness was also
    matching the bare word "error" anywhere in the output, which appears in
    Playwright's own teardown chatter and in any warning text. Nine of ten mutants
    were reported BUILD-ERR when nine of ten were in fact fine.

    The lesson, now written down in the concord-platform skill: match on STRUCTURE
    (`N failed` / `N passed` / the test's own FAILED line), never on a loose
    substring. A keyword search over log output is a guess.
    """
    combined = (run.stdout or "") + (run.stderr or "")

    # An empty selection is not a red test. pytest exits non-zero here, so this has
    # to be checked before anything else or it reads as a kill.
    if ("no tests ran" in combined or "no tests to run" in combined
            or "deselected" in combined and "passed" not in combined
            and "failed" not in combined):
        return "NO-TESTS", combined
    if "ERROR: file or directory not found" in combined:
        return "NO-TESTS", combined

    # The test itself reported: that is the authoritative signal.
    if re.search(rf"FAILED .*{re.escape(test)}", combined):
        return "KILLED", combined
    if re.search(rf"ERROR .*{re.escape(test)}", combined):
        return "KILLED", combined  # an error in the named test is still a red test

    # Fall back to the summary COUNTS, which are unambiguous.
    m = re.search(r"(\d+) failed", combined)
    if m and int(m.group(1)) > 0:
        return "KILLED", combined
    m = re.search(r"(\d+) passed", combined)
    if m and int(m.group(1)) > 0:
        return "SURVIVED", combined

    # A compile failure in the suite, or the binary refusing to start.
    if ("build failed" in combined or "cannot use" in combined
            or "never became healthy" in combined
            or "make: *** " in combined):
        return "BUILD-ERR", combined
    return "UNKNOWN", combined


def main():
    failures = []
    for label, path, old, new, test in MUTANTS:
        original = open(path).read()
        n = original.count(old)
        if n != 1:
            print(f"ABORT   {label}: pattern occurs {n} times, need 1")
            failures.append(label)
            continue
        try:
            open(path, "w").write(original.replace(old, new, 1))
            # Rebuild: Go embeds these assets, so without it the binary under test
            # is the unmutated one and every kill below would be a lie.
            build = sh("export PATH=$HOME/.cargo/bin:$PATH && make build", timeout=900)
            if build.returncode != 0:
                print(f"BUILD-ERR {label}: make build failed")
                print("         " + "\n         ".join(
                    (build.stdout + build.stderr).strip().splitlines()[-6:]))
                failures.append(f"{label} (BUILD-ERR)")
                continue
            run = sh(f"export PATH=$HOME/.cargo/bin:$PATH && "
                     f"python3 -m pytest -p no:cacheprovider {SUITE} "
                     f"-k {test} -q", timeout=600)
            verdict, combined = classify(run, test)
            hint = ""
            if verdict == "SURVIVED":
                hint = "  <- investigate the TEST, do not delete the guarded code"
            print(f"{verdict:9} {label:52} <- {test}{hint}")
            if verdict != "KILLED":
                failures.append(f"{label} ({verdict})")
                tail = [l for l in combined.strip().splitlines() if l.strip()][-4:]
                for l in tail:
                    print("         " + l[:150])
        finally:
            open(path, "w").write(original)

    # Restore and prove it.
    rb = sh("export PATH=$HOME/.cargo/bin:$PATH && make build", timeout=900)
    rr = sh("export PATH=$HOME/.cargo/bin:$PATH && "
            f"python3 -m pytest -p no:cacheprovider {SUITE} -q", timeout=600)
    print(f"\nrestored tree rebuilds: {rb.returncode == 0}")
    print(f"restored suite: {' '.join(rr.stdout.strip().splitlines()[-1:])}")
    if failures:
        print("NOT KILLED: " + "; ".join(failures))
        return 1
    print(f"all {len(MUTANTS)} page mutants killed")
    return 0


if __name__ == "__main__":
    sys.exit(main())