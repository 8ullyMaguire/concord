# Spec: settings field clarity

## Problem
The live charter form shows 22 machine names (`glicko_tau`, `lam`, `mu`, `pain_halflife_days`, ...) with no explanation, and its Save control sits below the long form with no visible state. A maintainer cannot tell what a value means or whether an edit is pending.

## Goals
- Humanize every charter field label without changing the wire key carried by `input[name]`.
- Add inline help for the four ranking fields named in the review.
- Keep Save and a new Reset control visible while the charter is scrolled, and show an honest dirty state.
- Prove all of it with a targeted browser suite and a live DOM probe.

## Non-goals
- No change to charter semantics, defaults or validation.
- No rename of API keys; `input[name]` stays exactly the server field.
- No reordering of the existing semantic groups.

## Constraints
- Existing `settings_e2e` assertions on `input[name=...]` must keep passing.
- One targeted suite, one rebuild, one gate.

## Success criteria
- Live and candidate DOM show zero underscore-containing visible labels on `#charter-form`, and four inline help paragraphs.
- Editing a field sets `data-dirty` text to "Unsaved changes"; Reset restores the original value and "All changes saved".
- Save/Reset container computes `position: sticky`.
