#!/usr/bin/env bash

set -euo pipefail

REPO=${REPO:-$(git rev-parse --show-toplevel)}
TOKF_HOME=${TOKF_HOME:-/tmp/invokr-tokf}
RESULTS=${RESULTS:-$(mktemp -d /tmp/invokr-agent-results.XXXXXX)}
SANDBOX_WS=${SANDBOX_WS:-$(mktemp -d /tmp/invokr-agent-ws.XXXXXX)}
SANDBOX_HOME=${SANDBOX_HOME:-$(mktemp -d /tmp/invokr-agent-home.XXXXXX)}
BIN=$RESULTS/invokr-agent
LIVE_MEMORY_DB=${LIVE_MEMORY_DB:-$HOME/.invokr/memory.db}
TEST_SESSIONS=()

require_commands() {
  local command
  for command in "$@"; do
    command -v "$command" >/dev/null || {
      echo "missing required command: $command" >&2
      exit 2
    }
  done
}

file_digest() {
  local path=$1
  if [[ -f $path ]]; then
    sha256sum "$path" | cut -d' ' -f1
  else
    printf 'missing\n'
  fi
}

setup_sandbox() {
  require_commands tokf git sha256sum ss
  port_is_free 41371 || {
    echo "port 41371 must be free before agent tests" >&2
    exit 2
  }
  LIVE_MEMORY_BEFORE=$(file_digest "$LIVE_MEMORY_DB")
  export LIVE_MEMORY_BEFORE

  git -C "$SANDBOX_WS" init -q
  printf '# Sandbox\nA throwaway workspace.\n' >"$SANDBOX_WS/README.md"
  printf 'package main\n\nfunc greeting() string { return "hi" }\n' >"$SANDBOX_WS/hello.go"
  printf 'notes\nTODO: wire up the thing\n' >"$SANDBOX_WS/notes.txt"
  git -C "$SANDBOX_WS" add -A
  git -C "$SANDBOX_WS" -c user.email=test@example.invalid -c user.name=test commit -qm seed

  if [[ -f $HOME/.invokr/config.json ]]; then
    cp "$HOME/.invokr/config.json" "$SANDBOX_HOME/config.json"
  else
    printf '{}\n' >"$SANDBOX_HOME/config.json"
  fi
  mkdir -p "$SANDBOX_HOME/skills" "$SANDBOX_HOME/code_interpreter" "$SANDBOX_HOME/toolbox-venv"

  (
    cd "$REPO"
    env TOKF_HOME="$TOKF_HOME" GOCACHE="$RESULTS/go-cache" tokf run go build -o "$BIN" .
  )
}

invokr_env() {
  env \
    INVOKR_HOME="$SANDBOX_HOME" \
    INVOKR_MEMORY_DIR="$SANDBOX_HOME" \
    INVOKR_SKILLS_DIR="$SANDBOX_HOME/skills" \
    INVOKR_WORKSPACE_ROOT="$SANDBOX_WS" \
    INVOKR_CODE_INTERPRETER_SESSION_DIR="$SANDBOX_HOME/code_interpreter" \
    INVOKR_TOOLBOX_DIR="$REPO/toolbox" \
    INVOKR_TOOLBOX_VENV_DIR="$SANDBOX_HOME/toolbox-venv" \
    TOKF_HOME="$TOKF_HOME" \
    "$@"
}

run_invokr() {
  invokr_env "$BIN" --log-output "$SANDBOX_HOME/invokr.log" "$@"
}

port_is_free() {
  local port=${1:-41371}
  ! ss -H -ltn "sport = :$port" | grep -q .
}

register_session() {
  TEST_SESSIONS+=("$1")
}

cleanup_agent_test() {
  local status=$?
  set +e
  local session
  if (( ${#TEST_SESSIONS[@]} > 0 )); then
    for session in "${TEST_SESSIONS[@]}"; do
      tmux kill-session -t "$session" 2>/dev/null || true
    done
  fi

  local live_after
  live_after=$(file_digest "$LIVE_MEMORY_DB")
  if [[ ${LIVE_MEMORY_BEFORE:-missing} != "$live_after" ]]; then
    echo "ERROR: live memory database changed: $LIVE_MEMORY_DB" >&2
    status=1
  fi
  if ! port_is_free 41371; then
    echo "ERROR: memory llama port 41371 remains occupied" >&2
    status=1
  fi
  if [[ -f $SANDBOX_HOME/memory.db ]]; then
    cp "$SANDBOX_HOME/memory.db" "$RESULTS/memory.db"
  fi
  if [[ -f $SANDBOX_HOME/invokr.log ]]; then
    cp "$SANDBOX_HOME/invokr.log" "$RESULTS/invokr.log"
  fi
  if [[ ${KEEP_SANDBOX:-0} != 1 ]]; then
    rm -rf "$SANDBOX_WS" "$SANDBOX_HOME"
  else
    echo "sandbox workspace: $SANDBOX_WS"
    echo "sandbox home: $SANDBOX_HOME"
  fi
  echo "results: $RESULTS"
  # Clear the trap and exit with $status: a bare return from an EXIT-trap
  # function does not propagate the status, so a failed assertion above would
  # otherwise leave the script exiting 0.
  trap - EXIT
  exit "$status"
}

trap cleanup_agent_test EXIT
