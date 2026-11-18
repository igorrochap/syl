#!/usr/bin/env bash

# Keep running after a failed gate so the summary can report every gate.
set -u

gates=(format vet style complexity coverage tests architecture)
# List mutually independent Gates here to run them concurrently.
# Leave the list empty to run every Gate one after another.
# Unlisted Gates after the first listed Gate wait for the whole batch to finish.
# Concurrent Gates receive no stdin, so keep interactive Gates out of this list.
concurrent_gates=()
selected_gates=()
results=()
notes=()
concurrent_result_directory=""
concurrent_gate_pids=()

format() {
  # Put the format command for this project here.
  printf '%s\n' "SKIP  not configured"
}

vet() {
  # Put the vet command for this project here.
  printf '%s\n' "SKIP  not configured"
}

style() {
  # Put the style command for this project here.
  printf '%s\n' "SKIP  not configured"
}

complexity() {
  # Put the complexity command for this project here.
  printf '%s\n' "SKIP  not configured"
}

coverage() {
  # Put the coverage command for this project here.
  printf '%s\n' "SKIP  not configured"
}

tests() {
  # Put the tests command for this project here.
  printf '%s\n' "SKIP  not configured"
}

architecture() {
  # Put the architecture command for this project here.
  printf '%s\n' "SKIP  not configured"
}

is_known_gate() {
  local requested=$1
  local gate

  for gate in "${gates[@]}"; do
    if [[ "$gate" == "$requested" ]]; then
      return 0
    fi
  done
  return 1
}

show_known_gates() {
  printf 'Known gates:\n'
  printf '  %s\n' "${gates[@]}"
}

is_concurrent_gate() {
  local requested=$1
  local gate

  for gate in "${concurrent_gates[@]-}"; do
    if [[ "$gate" == "$requested" ]]; then
      return 0
    fi
  done
  return 1
}

validate_concurrent_gates() {
  local gate

  for gate in "${concurrent_gates[@]-}"; do
    if [[ -z "$gate" ]]; then
      continue
    fi
    if ! is_known_gate "$gate"; then
      printf 'Unknown concurrent gate: %s\n' "$gate"
      show_known_gates
      return 1
    fi
  done
}

first_line() {
  local value=$1

  printf '%s\n' "${value%%$'\n'*}"
}

remove_status_prefix() {
  local value=$1

  case "$value" in
    SKIP*) value=${value#SKIP} ;;
    PASS*) value=${value#PASS} ;;
  esac
  value=${value# }
  value=${value# }
  printf '%s\n' "$value"
}

run_gate_command() {
  local gate=$1

  case "$gate" in
    format) format ;;
    vet) vet ;;
    style) style ;;
    complexity) complexity ;;
    coverage) coverage ;;
    tests) tests ;;
    architecture) architecture ;;
  esac
}

evaluate_gate() {
  local gate=$1
  local report
  local exit_code
  local result
  local note

  if report=$(run_gate_command "$gate" 2>&1); then
    exit_code=0
  else
    exit_code=1
  fi
  if (( exit_code != 0 )); then
    result=FAIL
    note=$(first_line "$report")
    if [[ -z "$note" ]]; then
      note="command failed"
    fi
  else
    report=$(first_line "$report")
    result=PASS
    note=""
    case "$report" in
      SKIP*)
        result=SKIP
        note=$(remove_status_prefix "$report")
        ;;
      PASS*)
        note=$(remove_status_prefix "$report")
        ;;
    esac
  fi

  gate_result=$result
  gate_note=$note
  return 0
}

run_concurrent_gates() {
  local result_directory
  local gate
  local index
  local worker_index
  local result_file
  local start_file
  local worker_pid
  local -a worker_indexes=()

  if ! result_directory=$(mktemp -d "${TMPDIR:-/tmp}/quality-gates.XXXXXX"); then
    for (( index = 0; index < ${#selected_gates[@]}; index++ )); do
      gate=${selected_gates[$index]}
      if is_concurrent_gate "$gate"; then
        results[index]=FAIL
        notes[index]="could not create a result directory"
      fi
    done
    return 0
  fi
  concurrent_result_directory=$result_directory

  set -m
  for (( index = 0; index < ${#selected_gates[@]}; index++ )); do
    gate=${selected_gates[$index]}
    if ! is_concurrent_gate "$gate"; then
      continue
    fi

    result_file="$result_directory/$index"
    start_file="$result_directory/start.$index"
    # Keep the worker idle until its PID is tracked; removing the directory aborts an interrupted launch.
    (
      trap - EXIT INT TERM
      while [[ ! -e "$start_file" ]]; do
        if [[ ! -d "$result_directory" ]]; then
          exit 1
        fi
        sleep 0.01
      done
      evaluate_gate "$gate"
      printf '%s\n%s\n' "$gate_result" "$gate_note" >"$result_file"
    ) </dev/null &
    worker_pid=$!
    concurrent_gate_pids+=("$worker_pid")
    worker_indexes+=("$index")
    printf '%s\n' "$worker_pid" >"$result_directory/pid.$index"
    : >"$start_file"
  done
  set +m

  for (( worker_index = 0; worker_index < ${#concurrent_gate_pids[@]}; worker_index++ )); do
    if ! wait "${concurrent_gate_pids[$worker_index]}"; then
      index=${worker_indexes[$worker_index]}
      printf '%s\n%s\n' FAIL "could not collect the gate result" >"$result_directory/$index"
    fi
    concurrent_gate_pids[worker_index]=""
  done

  for index in "${worker_indexes[@]}"; do
    result_file="$result_directory/$index"
    gate_result=FAIL
    gate_note="could not collect the gate result"
    if [[ -r "$result_file" ]]; then
      {
        IFS= read -r gate_result || gate_result=FAIL
        IFS= read -r gate_note || gate_note=""
      } <"$result_file"
    fi
    results[index]=$gate_result
    notes[index]=$gate_note
  done

  if rm -rf "$result_directory"; then
    concurrent_result_directory=""
  fi
  concurrent_gate_pids=()
}

# These cleanup handlers are reached through the traps below.
# shellcheck disable=SC2329
quality_cleanup_concurrent_gates() {
  local pid

  trap - EXIT INT TERM
  for pid in "${concurrent_gate_pids[@]-}"; do
    if [[ -z "$pid" ]]; then
      continue
    fi
    if ! kill -TERM -- "-$pid" 2>/dev/null; then
      kill -TERM "$pid" 2>/dev/null || true
    fi
  done
  for pid in "${concurrent_gate_pids[@]-}"; do
    if [[ -n "$pid" ]]; then
      wait "$pid" 2>/dev/null || true
    fi
  done
  if [[ -n "$concurrent_result_directory" ]]; then
    rm -rf "$concurrent_result_directory"
  fi
  concurrent_gate_pids=()
  concurrent_result_directory=""
}

# shellcheck disable=SC2329
quality_stop_concurrent_gates() {
  local exit_status=$1

  quality_cleanup_concurrent_gates
  exit "$exit_status"
}

trap 'quality_cleanup_concurrent_gates' EXIT
trap 'quality_stop_concurrent_gates 130' INT
trap 'quality_stop_concurrent_gates 143' TERM

build_summary() {
  local summary="QUALITY GATE"
  local index
  local gate
  local result
  local note
  local line
  local -a failed_gates=()

  for (( index = 0; index < ${#selected_gates[@]}; index++ )); do
    gate=${selected_gates[$index]}
    result=${results[$index]}
    note=${notes[$index]}
    line=$(printf '  %-14s%s' "$gate" "$result")
    if [[ -n "$note" ]]; then
      line+="   $note"
    fi
    summary+=$'\n'"$line"
    if [[ "$result" == FAIL ]]; then
      failed_gates+=("$gate")
    fi
  done

  if (( ${#failed_gates[@]} > 0 )); then
    summary+=$'\n\nFAILED:'
    for gate in "${failed_gates[@]}"; do
      summary+=" $gate"
    done
  fi
  printf '%s\n' "$summary"
}

if (( $# > 1 )); then
  printf 'Usage: %s [gate|--list]\n' "$0"
  show_known_gates
  exit 2
fi

if (( $# == 0 )); then
  selected_gates=("${gates[@]}")
elif [[ "$1" == "--list" ]]; then
  if (( $# != 1 )); then
    printf 'Usage: %s [gate|--list]\n' "$0"
    show_known_gates
    exit 2
  fi
  printf '%s\n' "${gates[@]}"
  exit 0
else
  if ! is_known_gate "$1"; then
    printf 'Unknown gate: %s\n' "$1"
    show_known_gates
    exit 2
  fi
  selected_gates=("$1")
fi

if ! validate_concurrent_gates; then
  exit 2
fi

for gate in "${selected_gates[@]}"; do
  results+=("")
  notes+=("")
done

concurrent_gates_started=0
for (( index = 0; index < ${#selected_gates[@]}; index++ )); do
  gate=${selected_gates[$index]}
  if is_concurrent_gate "$gate"; then
    if (( concurrent_gates_started == 0 )); then
      run_concurrent_gates
      concurrent_gates_started=1
    fi
    continue
  fi

  evaluate_gate "$gate"
  results[index]=$gate_result
  notes[index]=$gate_note
done

summary=$(build_summary)
printf '%s\n' "$summary"

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  if ! printf '%s\n' "$summary" >>"$GITHUB_STEP_SUMMARY"; then
    printf 'Could not write GitHub step summary: %s\n' "$GITHUB_STEP_SUMMARY" >&2
  fi
fi

for result in "${results[@]}"; do
  if [[ "$result" == FAIL ]]; then
    exit 1
  fi
done

exit 0
