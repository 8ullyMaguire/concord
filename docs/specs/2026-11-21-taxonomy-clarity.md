# Spec: taxonomy dedupe and hidden IDs

## Problem
The live `/taxonomy` page renders 68 links for 44 distinct names (`forum` x5, `rust` x5, `adult` x4), and the interactive panel's tooltips expose raw suffixed names such as `tag:topic:performance8103685`.

## Goals
- Group ratified tags by `(namespace, visible name)`, show one chip per group and its ratified count.
- Strip trailing `#`/digits from visible chip labels while keeping the exact server name in `data-tag` for actions.
- Keep search links useful and the axe gate green.

## Non-goals
- No database migration or deletion of tag rows; duplicates may be historically meaningful.
- No change to proposal/consent semantics.

## Constraints
- One targeted suite, one rebuild, one gate.
- Decisions recorded in the roadmap in the same commit.

## Success criteria
- Targeted candidate suite passes: unique visible labels, count badges, no raw ID suffix in visible text or tooltips.
- Live DOM probe on the public host shows equal link count and distinct labels, with no `tag:...<digits>` tooltips.
