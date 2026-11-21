# Spec: date and status presentation polish

## Problem
Review routes 11 and 24 found raw `snake_case` statuses on `/stats` and inconsistent date formats in `/projects/{slug}/documents`. A facet value like `in_progress` is a database key, not a label, and a locale date beside an ISO date reads as two different clocks.

## Goals
- Humanize status/role/kind facet values in the statistics cards (`in_progress` -> `In progress`).
- Render document metadata dates in one ISO day format (`YYYY-MM-DD`), matching the server's `date()` helper.
- Keep raw API values in the network contract; this is presentation only.

## Non-goals
- No API schema change and no normalisation of dates embedded in document markdown bodies.
- No changes to statistics math or document search.

## Constraints
- One targeted suite with intercepted API payloads, one rebuild, one gate.

## Success criteria
- Candidate suite proves `In progress` / `Stand aside` text, no raw underscores in rendered stats, and one ISO date format in document metadata.
- Live DOM probe records the public rendering.
