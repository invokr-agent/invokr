#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
source "$SCRIPT_DIR/common.sh"

require_commands sqlite3 grep
setup_sandbox

printf '# User\n\nPrefers Go for backend work.\n' >"$SANDBOX_HOME/USER.md"
printf '# Project\n\nThrowaway memory test project.\n' >"$SANDBOX_HOME/PROJECT.md"

mexec() {
  local label=$1 prompt=$2
  INVOKR_MEMORY_ZERFOO_ENABLED=false run_invokr exec --json "$prompt" \
    >"$RESULTS/$label.jsonl" 2>"$RESULTS/$label.stderr"
}

dbq() {
  sqlite3 "$SANDBOX_HOME/memory.db" "$1"
}

tool_completed() {
  grep -q '"type":"item.completed"' "$RESULTS/$1.jsonl" &&
    grep -q "\"name\":\"$2\"" "$RESULTS/$1.jsonl"
}

require_tool() {
  local label=$1 tool=$2
  if ! tool_completed "$label" "$tool"; then
    echo "MODEL FAILURE: $tool was not completed for $label; inspect $RESULTS/$label.jsonl" >&2
    return 1
  fi
}

require_output() {
  local label=$1 pattern=$2
  if ! grep -qiE "$pattern" "$RESULTS/$label.jsonl"; then
    echo "MODEL FAILURE: response for $label missed /$pattern/; inspect $RESULTS/$label.jsonl" >&2
    return 1
  fi
}

mexec save 'Use memory to save a project fact with content: the sandbox project uses Go and SQLite.'
require_tool save memory
count=$(dbq "select count(*) from memory_records where status='active' and content like '%Go and SQLite%';")
[[ ${count:-0} -ge 1 ]]

before=$(dbq "select count(*) from memory_records where status='active';")
mexec dedup 'Use memory to save a project fact with content: the sandbox project uses Go and SQLite.'
require_tool dedup memory
after=$(dbq "select count(*) from memory_records where status='active';")
[[ "$before" == "$after" ]]

mexec recall 'Use recall with query "Go SQLite" to find the saved project fact.'
require_tool recall recall
require_output recall 'Go.*SQLite|SQLite.*Go'

mexec pinned 'What programming language do I prefer?'
require_output pinned 'Go'

mexec persist-save 'Use memory to save a project fact with content: persistence marker 2026-06-22.'
require_tool persist-save memory
mexec persist-recall 'Use recall to find the persistence marker.'
require_tool persist-recall recall
require_output persist-recall '2026-06-22'

# Opt-in vector check: with Zerfoo enabled, saving a fact embeds it in-process
# (no server, no port). Loads the embedding GGUF on first use, so it's gated.
if [[ ${RUN_ZERFOO_MEMORY:-0} == 1 ]]; then
  INVOKR_MEMORY_ZERFOO_ENABLED=true run_invokr exec --json 'Use memory to save a project fact with content: deployment region eu-west-1.' \
    >"$RESULTS/vector-save.jsonl" 2>"$RESULTS/vector-save.stderr"
  vectors_count=$(dbq 'select count(*) from memory_vectors;')
  [[ ${vectors_count:-0} -ge 1 ]]
fi

echo 'Memory smoke suite passed'
