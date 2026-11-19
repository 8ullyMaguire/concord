#!/usr/bin/env python3
"""A vendored crate with no importer is a copy nobody can tell from a dependency."""
import glob
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]
zero = []
found = []
for d in sorted((ROOT / "vendor").iterdir()):
    if not d.is_dir() or not (d / "Cargo.toml").exists():
        continue
    needle = d.name.replace("-", "_")
    count = 0
    for f in glob.glob(str(ROOT / "crates/**/*.rs"), recursive=True):
        if needle in Path(f).read_text():
            count += 1
    found.append((d.name, count))
    if count == 0:
        zero.append(d.name)

for name, count in found:
    print(f"{count} {name}")
if zero:
    print(f"corpse check: FAIL — {', '.join(zero)}")
    sys.exit(1)
print(f"corpse check: PASS — {len(found)} vendored crates, 0 with zero importers")
