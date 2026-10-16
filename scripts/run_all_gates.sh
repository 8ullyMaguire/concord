#!/bin/bash
# Run EVERY mutation gate and report each verdict, without stopping at the first failure.
#
# `make gates` stops at the first non-zero exit, so a run that fails on the fourth gate
# says nothing about the eight after it -- and a gate that has never run is worse than one
# that fails: it is indistinguishable from a gate whose rules are all proven. This pass
# found nine mutants across three gates that had been reported as kills for four passes
# while never compiling, which is only visible if every gate runs on every pass.
#
# The gate list is EXTRACTED FROM THE MAKEFILE, not repeated here. Two lists would drift,
# and the drifted one is the one that runs.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

mapfile -t gates < <(grep -oE 'python3 internal/mutate\.py [^#]*' Makefile | sed 's/ *$//')
if [ "${#gates[@]}" -eq 0 ]; then
  echo "no gates found in the Makefile; the extraction pattern is stale"
  exit 1
fi

fail=0
for g in "${gates[@]}"; do
  out="$($g 2>&1)"
  summary="$(printf '%s\n' "$out" | grep -E '^\[.*\] ----' | tail -1)"
  printf '%s\n' "${summary:-$g: NO SUMMARY LINE}"
  bad="$(printf '%s\n' "$out" | grep -cE 'SURVIVED|UNUSABLE|NOT APPLIED')"
  if [ -z "$summary" ] || [ "$bad" != "0" ]; then
    fail=1
    printf '%s\n' "$out" | grep -E 'SURVIVED|UNUSABLE|NOT APPLIED|FAIL:' | head -20
  fi
done
[ "$fail" = 0 ] && echo "ALL GATES CLEAN" || echo "SOME GATES FAILED"
exit "$fail"
