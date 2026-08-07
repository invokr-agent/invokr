#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
source "$SCRIPT_DIR/common.sh"

require_commands tmux grep
setup_sandbox

mkdir -p "$SANDBOX_HOME/skills/sample-skill"
printf '%s\n' '---' 'name: sample-skill' 'description: Test skill.' '---' '' 'Reply with SAMPLE SKILL LOADED.' \
  >"$SANDBOX_HOME/skills/sample-skill/SKILL.md"

run_invokr index --allow-large >"$RESULTS/index.txt"

pane() {
  tmux capture-pane -t "$1" -p 2>/dev/null || echo ""
}

start_ui() {
  local session=$1
  local launch
  printf -v launch 'cd %q && exec env INVOKR_HOME=%q INVOKR_MEMORY_DIR=%q INVOKR_SKILLS_DIR=%q INVOKR_WORKSPACE_ROOT=%q INVOKR_CODE_INTERPRETER_SESSION_DIR=%q INVOKR_TOOLBOX_DIR=%q INVOKR_TOOLBOX_VENV_DIR=%q TOKF_HOME=%q %q --log-output %q agent --log-view --log-level=warn' \
    "$REPO" "$SANDBOX_HOME" "$SANDBOX_HOME" "$SANDBOX_HOME/skills" "$SANDBOX_WS" \
    "$SANDBOX_HOME/code_interpreter" "$REPO/toolbox" "$SANDBOX_HOME/toolbox-venv" "$TOKF_HOME" \
    "$BIN" "$SANDBOX_HOME/invokr.log"
  tmux new-session -d -s "$session" -x 220 -y 50 "$launch"
  register_session "$session"
  wait_for "$session" 'INVOKR AGENT' 30
}

wait_for() {
  local session=$1 pattern=$2 timeout=${3:-90} elapsed=0
  until pane "$session" | grep -qE "$pattern"; do
    sleep 0.25
    elapsed=$((elapsed + 1))
    if (( elapsed >= timeout * 4 )); then
      pane "$session" >"$RESULTS/timeout-$session.txt"
      echo "timeout waiting for $pattern in $session" >&2
      return 1
    fi
  done
}

send_prompt() {
  tmux send-keys -t "$1" -l "$2"
  tmux send-keys -t "$1" Enter
}

stop_ui() {
  local session=$1 elapsed=0
  tmux send-keys -t "$session" -l exit 2>/dev/null || true
  tmux send-keys -t "$session" Enter 2>/dev/null || true
  while tmux has-session -t "$session" 2>/dev/null; do
    sleep 0.25
    elapsed=$((elapsed + 1))
    if (( elapsed >= 40 )); then
      tmux kill-session -t "$session" 2>/dev/null || true
      echo "UI session did not exit cleanly: $session" >&2
      return 1
    fi
  done
  port_is_free 41371 || {
    echo "memory llama leaked after $session" >&2
    return 1
  }
}

ask_once() {
  local name=$1 prompt=$2 tool_pattern=$3 result_pattern=${4:-} timeout=${5:-90}
  local session="invokr-$name-$$"
  start_ui "$session"
  send_prompt "$session" "$prompt"
  wait_for "$session" "$tool_pattern" "$timeout"
  wait_for_response_count "$session" 1 "$timeout"
  pane "$session" >"$RESULTS/$name.txt"
  grep -qE "$tool_pattern" "$RESULTS/$name.txt"
  if [[ -n $result_pattern ]]; then
    grep -qEi "$result_pattern" "$RESULTS/$name.txt"
  fi
  stop_ui "$session"
}

approval_case() {
  local name=$1 prompt=$2 decision=$3 expected=$4 result_pattern=${5:-}
  local session="invokr-$name-$$"
  start_ui "$session"
  send_prompt "$session" "$prompt"
  wait_for "$session" 'approval required' 90
  pane "$session" >"$RESULTS/$name-approval.txt"
  tmux send-keys -t "$session" "$decision"
  wait_for "$session" "$expected" 90
  wait_for_response_count "$session" 1 90
  pane "$session" >"$RESULTS/$name.txt"
  if [[ -n $result_pattern ]]; then
    grep -qEi "$result_pattern" "$RESULTS/$name.txt"
  fi
  stop_ui "$session"
}

response_count() {
  pane "$1" | grep -c '─ response ─' || true
}

wait_for_response_count() {
  local session=$1 wanted=$2 timeout=${3:-90} elapsed=0
  until (( $(response_count "$session") >= wanted )); do
    sleep 0.25
    elapsed=$((elapsed + 1))
    if (( elapsed >= timeout * 4 )); then
      echo "timeout waiting for response $wanted in $session" >&2
      return 1
    fi
  done
}

ask_once read-file 'Use the read_file tool to show notes.txt.' 'tool read_file completed' 'TODO: wire up the thing'
ask_once file-search 'Use file_search to find TODO in the workspace.' 'tool file_search completed' 'notes.txt'
ask_once shell-readonly 'Use shell with command git and args ["status"].' 'tool shell completed' 'working tree'
approval_case shell-approve 'Use shell with command git and args ["add","-A"].' y 'tool shell completed'
approval_case shell-deny 'Use shell with command go and args ["build","./..."].' n 'approval denied: shell'
grep -q 'Authoritative tool outcome' "$RESULTS/shell-deny.txt"
approval_case apply-patch 'Use apply_patch to create greeting.txt containing Hello.' y 'tool apply_patch completed'
[[ -f $SANDBOX_WS/greeting.txt ]] || { echo 'apply_patch did not create greeting.txt' >&2; exit 1; }
ask_once tool-search 'Use tool_search to list tools related to files.' 'tool tool_search completed'
ask_once skill 'Load sample-skill and follow it.' 'tool load_skill completed' 'SAMPLE SKILL LOADED'
approval_case code-interpreter 'Use code_interpreter to compute sum(range(10)).' y 'tool code_interpreter (completed|failed)' '45'
approval_case delegate 'Use delegate_task to summarize hello.go.' y 'tool delegate_task (completed|failed)'
ask_once retrieval 'Use retrieval to find where greeting is defined.' 'tool retrieval completed' 'hello.go'

memory_session="invokr-memory-$$"
start_ui "$memory_session"
send_prompt "$memory_session" 'Use memory to save that my favorite color is teal.'
wait_for "$memory_session" 'tool memory completed' 90
wait_for_response_count "$memory_session" 1 90
send_prompt "$memory_session" 'Use recall to find my favorite color.'
wait_for "$memory_session" 'tool recall completed' 90
wait_for_response_count "$memory_session" 2 90
pane "$memory_session" >"$RESULTS/memory.txt"
grep -qi teal "$RESULTS/memory.txt"
stop_ui "$memory_session"

echo 'TUI smoke suite passed'
