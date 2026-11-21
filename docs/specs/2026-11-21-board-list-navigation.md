# Spec: board and list navigation

## Problem
The review found a 362-idea board column with no filter or pagination, and a list detail with duplicate `Git Social` rows and over 100 entries rendered in one long wall. The current list caps at 100 but still no dedupe/filter/page controls.

## Goals
- Add a client-side title filter to the board and a "show 50 / show all" control for large columns.
- Deduplicate list entries by `(title, url)` keeping the highest-ranked first occurrence.
- Add a list filter and 25-entry pagination that preserves the API's ranking order and category grouping.

## Non-goals
- No server-side pagination or schema change; these are bounded client-side views over the existing reads.
- No change to vote/comparison semantics or card moves.

## Constraints
- Existing `card-move` and list vote wiring must keep working after re-render.
- One targeted suite, one rebuild, one gate.

## Success criteria
- Candidate suite proves board filter hides nonmatching cards, show-all reveals the rest, list dedupe removes duplicates, list filter works, and Next advances pages.
- Live DOM probe on the public host shows the controls and bounded row counts.
