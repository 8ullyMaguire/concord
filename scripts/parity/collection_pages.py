#!/usr/bin/env python3
"""Every handler returning a collection must take the shared page query."""
import glob, re, sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FOUND = []
MISSING = []
for path in sorted(glob.glob(str(ROOT / "crates/app/src/**/*.rs"), recursive=True)):
    text = Path(path).read_text()
    for match in re.finditer(r"(?ms)^async fn .*?^\}\n", text):
        block = match.group(0)
        if "crate::pagination::page_json" not in block and "crate::pagination::catalog_json" not in block:
            continue
        name = re.search(r"async fn (\w+)", block).group(1)
        rel = Path(path).relative_to(ROOT)
        entry = f"{rel}:{name}"
        FOUND.append(entry)
        if "Query<PageQuery>" not in block or "page_query.valid()" not in block:
            MISSING.append(entry)

print(f"collection routes: {len(FOUND)}")
for entry in MISSING:
    print(f"  no page: {entry}")
if MISSING:
    print(f"collection_pages: FAIL — {len(FOUND)} collection routes, {len(MISSING)} without PageQuery")
    sys.exit(1)
print(f"collection_pages: PASS — {len(FOUND)} collection routes, 0 without PageQuery")
