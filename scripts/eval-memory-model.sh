#!/usr/bin/env bash
#
# Evaluate whether a model is viable for Invokr's memory embedding and/or
# summarization subsystems, by running the isolated zerfoo integration tests
# against it. Reports whether the model loads and produces output.
#
# Usage:
#   scripts/eval-memory-model.sh embed     <model>
#   scripts/eval-memory-model.sh summarize <model>
#   scripts/eval-memory-model.sh both      <embed-model> <summarize-model>
#
# <model> is either a local GGUF path (starts with / or ./) or a HuggingFace
# model ID (e.g. HuggingFaceTB/SmolLM2-360M-Instruct-GGUF), which zerfoo will
# download to its cache on first use. HF IDs use the Q4_K_M quant by default;
# append a quant to override (e.g. some/repo-GGUF/Q8_0).
#
# Examples:
#   scripts/eval-memory-model.sh embed ~/models/smollm2-360m-q8_0.gguf
#   scripts/eval-memory-model.sh summarize HuggingFaceTB/SmolLM2-360M-Instruct-GGUF
#   scripts/eval-memory-model.sh both /path/embed.gguf /path/chat.gguf
#
# Env:
#   TEST_TIMEOUT  go test timeout (default 20m)
set -euo pipefail

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TIMEOUT=${TEST_TIMEOUT:-20m}

usage() { sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 2; }

# set_model VAR_PREFIX MODEL — export the *_PATH var for a local file, else the
# *_MODEL var for a HuggingFace ID.
set_model() {
  local prefix=$1 model=$2
  if [[ $model == /* || $model == ./* || $model == ../* ]]; then
    [[ -f $model ]] || { echo "no such file: $model" >&2; exit 1; }
    export "INVOKR_MEMORY_ZERFOO_${prefix}_PATH=$model"
  else
    export "INVOKR_MEMORY_ZERFOO_${prefix}_MODEL=$model"
  fi
}

run() {
  local label=$1 pattern=$2
  echo "=== evaluating $label ==="
  if INVOKR_ZERFOO_INTEGRATION=1 go test -tags=integration -race -count=1 \
      -run "$pattern" -v -timeout "$TIMEOUT" "$REPO/internal/memory/zerfoo/..." \
      2>&1 | grep -vE 'INFO (mmap|loaded GGUF|detected)'; then
    echo "RESULT: $label VIABLE"
  else
    echo "RESULT: $label NOT VIABLE (see errors above)"
    return 1
  fi
}

mode=${1:-}
case "$mode" in
  embed)
    [[ $# -eq 2 ]] || usage
    set_model EMBEDDING "$2"
    run "embedding ($2)" 'TestIntegrationEmbeddingLoading|TestIntegrationConcurrentEmbeddingLoadsOnce'
    ;;
  summarize)
    [[ $# -eq 2 ]] || usage
    set_model SUMMARIZATION "$2"
    run "summarization ($2)" 'TestIntegrationSummarizationLoading'
    ;;
  both)
    [[ $# -eq 3 ]] || usage
    set_model EMBEDDING "$2"
    set_model SUMMARIZATION "$3"
    rc=0
    run "embedding ($2)" 'TestIntegrationEmbeddingLoading|TestIntegrationConcurrentEmbeddingLoadsOnce' || rc=1
    run "summarization ($3)" 'TestIntegrationSummarizationLoading' || rc=1
    exit $rc
    ;;
  *) usage ;;
esac
