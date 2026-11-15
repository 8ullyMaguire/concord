#!/usr/bin/env bash
# Push to both hosts, then ask GitHub to run the gate.
#
# Why this exists: `git push` to this repository does NOT create a workflow run, and three things that could explain that were
# each measured and each ruled out on 2026-11-15:
#
#   * the trigger's branch filter — it was `branches: ["**"]`, it is now `branches: [port-rust, master, main]`; push `f9b4a07`
#     created no run either way.
#   * the default branch — `master` was the pre-port Go tree, so `port-rust` became the default branch via
#     `gh api -X PATCH … -f default_branch=port-rust`; push `309f69e` created no run either way.
#   * Actions being off, or the workflow being disabled — `{"enabled":true,"allowed_actions":"all"}` and the workflow is
#     `active`, while `repos/:owner/:repo/events` lists PushEvents on `port-rust` for 2026-11-13, 14 and 15.
#
# What does work is `workflow_dispatch`: runs 43728560320 and 43732462823 both ran on demand, the second green. So the push path
# dispatches instead of waiting for a trigger nobody can see the reason for. If the trigger ever starts working again this script
# is still correct — it pushes, then dispatches, and a duplicate run is a duplicate run.
set -euo pipefail

branch="${1:-$(git rev-parse --abbrev-ref HEAD)}"
remote_github="${GITHUB_REMOTE:-github}"
remote_forgejo="${FORGEJO_REMOTE:-forgejo}"

echo "push: $branch -> $remote_forgejo, $remote_github"
git push "$remote_forgejo" "$branch"
git push "$remote_github" "$branch"

if ! command -v gh >/dev/null 2>&1; then
  echo "push: gh not installed — GitHub's gate was NOT dispatched" >&2
  exit 1
fi

echo "push: dispatching GitHub's gate on $branch"
# `api.github.com` is intermittently unresolvable from this host ("error connecting to api.github.com", and the same failure
# appeared as `Could not resolve host: index.crates.io` while building). A dispatch that does not happen because DNS blinked is
# the same defect as a push that creates no run: the gate silently did not run. Three attempts, six seconds apart, and it fails
# loudly rather than pretending.
attempt=0
until gh workflow run gate.yml --ref "$branch"; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 3 ]; then
    echo "push: GitHub's gate was NOT dispatched after $attempt attempts — the gate did not run" >&2
    exit 1
  fi
  echo "push: dispatch failed (attempt $attempt), retrying in 6s" >&2
  sleep 6
done
echo "push: dispatched — \`gh run list --limit 1\` to watch it"
