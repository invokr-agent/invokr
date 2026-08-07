#!/usr/bin/env bash
# Extract a compact replay fixture from a recorded marathon run.
# usage: extract.sh <run-dir>   (prints JSON on stdout)
set -euo pipefail
dir="$1"
{
  echo -n '{"run":"'"$(basename "$(dirname "$dir")")"'","segments":['
  first_seg=1
  for nd in "$dir"/seg-*.ndjson; do
    n="${nd##*/seg-}"; n="${n%%.ndjson}"
    dbg="$dir/seg-$n.debug.log"
    [ "$first_seg" = 1 ] || echo -n ','
    first_seg=0
    prose=$(grep -o 'model output received.*' "$dbg" 2>/dev/null \
      | sed 's/.*"turn": \([0-9]*\), "output_len": \([0-9]*\)}/\1 \2/' | jq -Rn '[inputs|split(" ")|{key:.[0],value:(.[1]|tonumber)}]|from_entries')
    calls=$(jq -c --argjson prose "${prose:-{\}}" '
      select(.type=="item.completed" or .type=="error")
      | .item
      | {t:(.turn//0),
         n:.name,
         p:((.arguments|capture("\"path\"\\s*:\\s*\"(?<v>(\\\\.|[^\"\\\\])*)\"")?|.v)//""),
         a:(.arguments|length),
         b:(if .error then (.error|length) else ((.output|length)//0) end)}' "$nd" | jq -s '.')
    tokens=$(grep -o 'tokens_evaluated": [0-9]*' "$dbg" 2>/dev/null | sed 's/.*: //' | jq -Rn '[inputs|tonumber]')
    echo -n '{"seg":'"$n"',"tokens":'"${tokens:-[]}"',"prose":'"${prose:-{\}}"',"calls":'"$calls"'}'
  done
  echo -n ']}'
} | jq -c .
