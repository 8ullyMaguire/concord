# Spec: slimmer consent bar and larger header targets

## Problem
The public notice is a 237px wall of 516 characters with a 159×32 Accept button; header links are 34px high and the brand 36px, below a 44px touch target. Review suggestion 7 asks for a dismissible bar and 44×44 targets without making the header taller.

## Goals
- Shorten the notice to a two-line bar with Accept and a non-consenting "Not now" dismiss.
- Dismiss removes the bar for the current browser session without recording consent; interactions remain off.
- Raise brand and nav-link hit areas to at least 44px while keeping header height at or below the measured 82px.

## Non-goals
- No change to the consent rule itself: interactions stay blocked until the server records acceptance.
- No nav restructure or footer; that is suggestion 8.

## Constraints
- Axe gate and mobile no-overflow must stay green.
- One targeted suite, one rebuild, one gate.

## Success criteria
- Candidate suite proves small notice, working dismiss-without-consent, Accept-with-consent, ≥44px targets, unchanged header height and zero overflow at 390px.
- Live DOM probe records the measured public heights.
