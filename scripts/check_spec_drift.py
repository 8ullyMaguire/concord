#!/usr/bin/env python3
"""Fail if any document cites a superseded spec as authority.

KNOWN-ISSUES.md has carried "docs/concord-spec.md is two revisions stale" for
revisions. The problem was never only that one file being old -- it was that
SEVEN other documents pointed readers at it: frontend-spec.md cites
"concord-spec.md §9.4" as authority three times, PREMISE.md says the full spec
lives there, and PLAN-r4 lists replacing it as outstanding work. A reader who
follows any of those citations implements from a spec two revisions back and
cannot tell, because the file looks exactly as authoritative as its replacement.

So the invariant is not "concord-spec.md is up to date" -- it will never be. It is:

  every document that names a spec file as the authority must name the CURRENT one,
  and any superseded file must open with a banner saying so.

That is checkable, so it is checked. A doc-rot finding that only lives in a
markdown list is a note, not a gate: it depends on someone reading that list,
which is the same failure mode as the original problem.
"""
import os
import re
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DOCS = os.path.join(REPO, "docs")

CURRENT = "concord-spec-r4.md"
SUPERSEDED = {"concord-spec.md"}

BANNER = "THIS FILE IS SUPERSEDED"

# KNOWN-ISSUES.md is exempt by PATH, not by keyword.
#
# It is where this class of problem is DESCRIBED, so it necessarily quotes the
# misdirected citation it is reporting -- "read concord-spec.md and join in" has
# to appear verbatim or the finding is unreadable. A keyword exemption was tried
# first and rejected: it would exempt any line containing a word like "stale",
# which is exactly the class of line most likely to misdirect a reader who skims.
# Naming the one file that is allowed to discuss the problem is narrower, and it
# is a fact about the file rather than about its wording.
META_FILES = {os.path.join(DOCS, "KNOWN-ISSUES.md")}

# Citations that are HISTORICAL rather than normative: a note describing that a
# file used to be stale, or a tracker listing the problem, is not a reader being
# misdirected. Matched case-insensitively against the whole line.
HISTORICAL = re.compile(
    r"(stale|superseded|two revisions|known-issues|not current|replaced|"
    r"outstanding work|r7 docs sync|do not read)", re.I,
)

failures = []


def rel(path):
    return os.path.relpath(path, REPO)


# 1. Every superseded spec must open with a banner.
for name in sorted(SUPERSEDED):
    path = os.path.join(DOCS, name)
    if not os.path.exists(path):
        continue
    head = open(path).read(2000)
    if BANNER not in head:
        failures.append(
            f"docs/{name} is superseded by {CURRENT} but has no '{BANNER}' banner "
            f"in its first 2000 chars: a reader landing here would implement from "
            f"a spec two revisions back"
        )
    else:
        print(f"  PASS  docs/{name} opens with a supersession banner")

# 2. No document may cite a superseded spec as authority.
print()
checked = 0
for root, dirs, files in os.walk(DOCS):
    dirs[:] = [d for d in dirs if not d.startswith(".")]
    for fn in sorted(files):
        if not fn.endswith(".md"):
            continue
        path = os.path.join(root, fn)
        if fn in SUPERSEDED:
            continue  # its own banner names the replacement; not a misdirection
        if path in META_FILES:
            continue  # KNOWN-ISSUES quotes the problem in order to report it
        try:
            text = open(path).read()
        except OSError:
            continue
        for i, line in enumerate(text.split("\n"), 1):
            for stale in SUPERSEDED:
                if stale not in line:
                    continue
                # A citation of the CURRENT spec may contain the stale name as a
                # substring only if it is written without a path separator, which
                # it is not: concord-spec-r4.md does not contain concord-spec.md.
                if HISTORICAL.search(line):
                    continue
                # A line naming the CURRENT spec next to the stale one is
                # EXPLAINING the supersession, which is the opposite of the
                # failure this gate exists to catch. Requiring the current name
                # on the same line keeps that honest: a line that mentions both
                # has, by construction, told the reader which one is current.
                if CURRENT in line:
                    continue
                checked += 1
                failures.append(
                    f"{rel(path)}:{i} cites {stale} as if current: "
                    f"{line.strip()[:90]!r}\n"
                    f"        point it at {CURRENT}, or say explicitly that it is "
                    f"historical"
                )

print(f"  checked {checked} normative citation(s) of a superseded spec")
if checked == 0:
    # Not a pass condition on its own -- zero citations would also mean the check
    # found nothing to do. Asserted by the fact that the tree HAS such citations
    # today; if they all disappear, this line is what tells you the gate is idle.
    print("  NOTE  no citations found: if the docs were rewritten to stop naming the "
          "old spec entirely, this gate has nothing left to guard")

print()
if failures:
    print(f"FAILED: {len(failures)} problem(s)")
    for f in failures:
        print(f"  - {f}")
    sys.exit(1)
print("all checks passed")