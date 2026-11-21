# Plan: header and consent

1. Write `tests/e2e/header_consent_e2e.py` on port 8466 with no consent cookie. Reproduce RED against the current candidate: notice height, missing dismiss, small targets.
2. In `crates/web/src/shell.rs`, shorten the notice copy and add `#cookie-dismiss` secondary button.
3. In `crates/web/assets/js/consent.js`, wire dismiss to hide the notice and remember dismissal in `sessionStorage` without POSTing or changing `data-consent`.
4. In `crates/web/assets/css/style.css`, make the notice a compact bar and grow `.site-nav a` / `.brand` hit areas to ≥44px while reducing header padding to hold its height.
5. Rebuild once; rerun the targeted suite; wire it into the gate; commit D-223; push; deploy; live DOM probe; rewrite notes.
