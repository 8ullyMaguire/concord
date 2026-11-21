# Spec: grouped navigation and a real footer

## Problem
The header presents nine unlabelled top-level links and the footer is a single version line with no navigation. Review suggestion 8 asks for consolidation and a real footer.

## Goals
- Group header links into Explore, Community and Account with accessible group labels, keeping every existing link and session hook.
- Add a footer with the same destinations in Explore/Community/Project groups plus the version line.
- Keep 44px header targets, zero overflow and the axe gate green.

## Non-goals
- No new routes, no dropdown menus, and no change to authentication or notification behaviour.
- No date/status formatting work; that is suggestion 9.

## Constraints
- `data-notifications-link`, `data-auth-link` and `data-signout` hooks stay on the header only; footer links are plain anchors.
- One targeted suite, one rebuild, one gate.

## Success criteria
- Candidate suite proves three nav groups, all nine header links, grouped footer links, and no 390px overflow.
- Live DOM probe records the measured public structure.
