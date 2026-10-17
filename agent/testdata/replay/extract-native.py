#!/usr/bin/env python3
"""Extract sizes only from native-tool NDJSON plus its debug log; no tool runs."""
import json
from pathlib import Path
import re
import sys

root = Path(sys.argv[1])
segments = []
for path in sorted(root.glob("seg-*.ndjson")):
    calls, seen = [], set()
    for line in path.read_text().splitlines():
        event = json.loads(line)
        if event["type"] not in ("item.completed", "error") or "item" not in event:
            continue
        item = event["item"]
        if item["id"] in seen:
            continue
        seen.add(item["id"])
        arguments = item.get("arguments", "")
        try:
            args = json.loads(arguments)
        except ValueError:
            args = {}
        output = item.get("output", item.get("error", ""))
        calls.append(dict(t=item.get("turn", 0), n=item["name"],
                          p=args.get("path", ""), a=len(arguments.encode()),
                          b=len(output.encode())))
    debug = path.with_suffix(".debug.log").read_text()
    prose = {k: int(v) for k, v in re.findall(
        r'model output received.*?"turn": (\d+), "output_len": (\d+)', debug)}
    tokens = [int(v) for v in re.findall(r'tokens_evaluated": (\d+)', debug)]
    segments.append(dict(seg=int(path.stem.split("-")[1]), tokens=tokens,
                         prose=prose, calls=calls))
print(json.dumps(dict(run=root.parent.name, segments=segments), separators=(",", ":")))
