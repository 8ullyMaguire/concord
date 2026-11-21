# Plan: date and status polish

1. Write `tests/e2e/date_status_polish_e2e.py` on port 8468 with intercepted stats and documents API payloads. Reproduce RED against the current candidate.
2. In `crates/web/assets/js/stats.js`, add a `humanize()` helper and use it for facet values.
3. In `crates/web/assets/js/documents.js`, make `fmtDate()` return `YYYY-MM-DD` in UTC.
4. Rebuild once; run the targeted suite; wire into the gate; commit D-225; push; deploy; live DOM probe; rewrite notes.
