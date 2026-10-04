#!/usr/bin/env python3
"""Fail if an e2e test synchronises on a clock instead of on state.

The suites were written with `page.wait_for_timeout(800)` between an action and
the read that checks it. Twenty-two of them in finder_e2e.py alone. A sleep is not
a synchronisation: it passes when the machine is fast and fails when it is busy,
which is why the pattern presents as an intermittent flake that nobody can
reproduce on an idle box -- KNOWN-ISSUES.md carried `test_escape_goes_back` as
"fails ~1 run in 3" and it did not reproduce in nine consecutive runs.

They are all fixed (20 -> 1 in finder, and the survivors are documented). This
gate exists because a fix that is only a fix until the next person copies the
pattern is not a fix. It distinguishes three cases rather than banning the call:

  RACY    a sleep followed by a read that an assertion then depends on. This is
          the bug. A shorter sleep does not fix it; only waiting for state does.
  BOUNDED a sleep that is explicitly justified in a comment on the line above,
          which today means asserting the ABSENCE of something (no fetch arrived,
          no JS error raised) where there is no state to wait for.
  IDLE    a sleep in a path where nothing is asserted afterwards -- a debounce
          or a transition guard, not a synchronisation.

`wait_for_function` and `expect(...)` are the sanctioned waits. So is
`wait_for_load_state`. If a new one is genuinely needed, the honest move is to
add the justification comment, which makes the exception visible to a reviewer
rather than invisible in a diff.
"""
import os
import re
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
E2E = os.path.join(REPO, "tests", "e2e")

# A read that an assertion depends on: text_content / inner_text / get_attribute /
# count / all_inner_texts / first.click()
READ = re.compile(
    r"\.(inner_text|text_content|get_attribute|count|all_inner_texts|input_value)\s*\("
    r"|\.first\.click\s*\("
)
ASSERT_AFTER = re.compile(r"^\s*(assert|expect)\b")

failures = []
counts = {"racy": 0, "bounded": 0, "idle": 0}

for fn in sorted(os.listdir(E2E)):
    # Only the suites, and only real calls: harness.py holds the helpers and its
    # COMMENT deliberately quotes `page.wait_for_timeout(800)` while explaining why
    # the pattern is wrong. Scanning prose for the pattern it is warning about is
    # how a gate starts reporting its own documentation.
    if not fn.endswith("_e2e.py"):
        continue
    path = os.path.join(E2E, fn)
    lines = open(path).read().split("\n")
    for i, line in enumerate(lines):
        stripped = line.strip()
        if "wait_for_timeout" not in stripped or stripped.startswith("#"):
            continue
        # A justification immediately above (skipping blanks) marks it BOUNDED.
        j = i - 1
        while j >= 0 and not lines[j].strip():
            j -= 1
        # Walk up through the whole contiguous comment block, so a multi-line
        # justification is not silently truncated to its last line.
        block = []
        k2 = j
        while k2 >= 0 and (lines[k2].strip().startswith("#") or not lines[k2].strip()):
            block.append(lines[k2].lower())
            k2 -= 1
        rationale = "\n".join(block)
        justified = any(
            w in rationale
            for w in ("wait", "absence", "settle", "assert", "no state",
                      "debounce", "never arrive", "look identical until time")
        )

        # What follows the sleep decides the verdict.
        k = i + 1
        while k < len(lines) and not lines[k].strip():
            k += 1
        nxt = lines[k] if k < len(lines) else ""
        # Look a little further: an assignment may be followed by its assert.
        window = "\n".join(lines[i + 1 : i + 4])
        reads = bool(READ.search(window))
        asserts = bool(ASSERT_AFTER.match(lines[k])) if k < len(lines) else False

        if justified:
            counts["bounded"] += 1
            kind = "BOUNDED"
        elif reads and (asserts or "assert" in window or "expect" in window):
            counts["racy"] += 1
            kind = "RACY"
            failures.append(
                f"{fn}:{i + 1} sleeps, then reads, then asserts on the read:\n"
                f"    {line.strip()}\n"
                f"    then: {nxt.strip()[:80]}\n"
                f"    A sleep is not a synchronisation. Wait for the state instead:\n"
                f"    expect(locator).to_have_text(...) / to_have_count(...) /\n"
                f"    to_have_attribute(...), or page.wait_for_function(...)."
            )
        else:
            counts["idle"] += 1
            kind = "IDLE"

        print(f"  {kind:8} {fn}:{i + 1}  {line.strip()[:60]}")

print()
print(f"  bounded (justified): {counts['bounded']}   racy: {counts['racy']}   idle: {counts['idle']}")
if counts["bounded"] + counts["idle"] + counts["racy"] == 0:
    # Honest reporting: a gate that finds nothing must say so rather than look
    # like a gate that is guarding something.
    print("  NOTE  no wait_for_timeout calls remain in the e2e suites: this gate "
          "has nothing left to guard")

if failures:
    print(f"\nFAILED: {len(failures)} racy sleep(s)")
    for f in failures:
        print(f"  - {f}")
    sys.exit(1)
print("all checks passed")