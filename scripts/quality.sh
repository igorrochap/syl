#!/usr/bin/env bash

# Keep running after a failed gate so the summary can report every gate.
set -u

gates=(format vet style complexity coverage tests architecture)
selected_gates=()
results=()
notes=()
reports=()
failed_gates=()
golangci_lint_version=v2.13.1

format() {
  mapfile -t go_files < <(git ls-files '*.go')
  unformatted="$(gofmt -l "${go_files[@]}")"
  if [ -n "$unformatted" ]; then
    echo "The following Go files are not formatted:"
    echo "$unformatted"
    return 1
  fi
}

vet() {
  local failed=0

  shellcheck scripts/*.sh || failed=1
  go vet ./... || failed=1
  go mod tidy || failed=1
  git diff --exit-code -- go.mod go.sum || failed=1

  return "$failed"
}

quality_base_ref() {
  local requested_base_ref="${GITHUB_BASE_REF:-}"
  local base_ref
  local -a candidate_refs=()

  if [[ -n "$requested_base_ref" ]]; then
    candidate_refs+=("origin/$requested_base_ref" "$requested_base_ref")
  fi
  candidate_refs+=(main origin/main)

  for base_ref in "${candidate_refs[@]}"; do
    if git rev-parse --verify "${base_ref}^{commit}" >/dev/null 2>&1; then
      printf '%s\n' "$base_ref"
      return 0
    fi
  done

  return 1
}

style() {
  local installed_version
  local base_ref
  local changed_file
  local -a changed_go_files=()

  if ! command -v golangci-lint >/dev/null 2>&1; then
    echo "golangci-lint ${golangci_lint_version} is required for the style gate." >&2
    return 1
  fi

  if ! installed_version="$(golangci-lint version --short)"; then
    echo "Could not determine the golangci-lint version." >&2
    return 1
  fi
  if [[ "$installed_version" != "${golangci_lint_version#v}" ]]; then
    echo "golangci-lint ${golangci_lint_version} is required; found ${installed_version}." >&2
    return 1
  fi

  if ! base_ref="$(quality_base_ref)"; then
    echo "Could not determine the quality-gate base ref." >&2
    return 1
  fi

  while IFS= read -r changed_file; do
    if [[ -z "$changed_file" ]]; then
      continue
    fi
    case "$changed_file" in
      *.go) changed_go_files+=("$changed_file") ;;
    esac
  done < <(git diff --name-only --diff-filter=ACMR "$base_ref" -- '*.go')

  if (( ${#changed_go_files[@]} == 0 )); then
    printf '%s\n' "PASS  no changed Go files"
    return 0
  fi

  golangci-lint run --new-from-merge-base="$base_ref" ./...
}

complexity() {
  local complexity_limit=10
  local base_ref
  local changed_file
  local changed_ranges
  local over_limit_output
  local over_limit_status=0
  local all_functions_output
  local all_functions_status=0
  local violations
  local -a changed_files=()

  if ! base_ref="$(quality_base_ref)"; then
    echo "Could not determine the quality-gate base ref." >&2
    return 1
  fi

  while IFS= read -r changed_file; do
    case "$changed_file" in
      ""|*_test.go) ;;
      *.go) changed_files+=("$changed_file") ;;
    esac
  done < <(git diff --name-only --diff-filter=ACMR "$base_ref" -- '*.go')

  if (( ${#changed_files[@]} == 0 )); then
    printf '%s\n' "PASS  no changed non-test Go files"
    return 0
  fi

  if ! command -v gocyclo >/dev/null 2>&1; then
    echo "gocyclo is required for the complexity gate." >&2
    return 1
  fi

  changed_ranges="$(
    git diff --no-ext-diff --unified=0 "$base_ref" -- "${changed_files[@]}" |
      awk '
        /^\+\+\+ b\// {
          file = substr($0, 7)
          next
        }
        /^@@ / {
          range = $0
          sub(/^.*\+/, "", range)
          split(range, parts, " ")
          split(parts[1], bounds, ",")
          start = bounds[1] + 0
          count = bounds[2] == "" ? 1 : bounds[2] + 0
          if (count == 0) {
            count = 1
          }
          printf "%s\t%d\t%d\n", file, start, start + count - 1
        }
      '
  )"

  over_limit_output="$(gocyclo -over "$complexity_limit" "${changed_files[@]}" 2>&1)" || over_limit_status=$?
  if (( over_limit_status > 1 )); then
    printf '%s\n' "$over_limit_output" >&2
    return 1
  fi
  if [[ -z "$over_limit_output" ]]; then
    printf '%s\n' "PASS  changed functions are at or below complexity $complexity_limit"
    return 0
  fi

  all_functions_output="$(gocyclo "${changed_files[@]}" 2>&1)" || all_functions_status=$?
  if (( all_functions_status != 0 )); then
    printf '%s\n' "$all_functions_output" >&2
    return 1
  fi

  violations="$(
    {
      if [[ -n "$changed_ranges" ]]; then
        while IFS= read -r changed_range; do
          printf 'R\t%s\n' "$changed_range"
        done <<<"$changed_ranges"
      fi
      while IFS= read -r function; do
        printf 'A\t%s\n' "$function"
      done <<<"$all_functions_output"
      while IFS= read -r function; do
        printf 'O\t%s\n' "$function"
      done <<<"$over_limit_output"
    } | awk -F '\t' '
      function location_file(value) {
        sub(/:[0-9]+:[0-9]+$/, "", value)
        return value
      }
      function location_line(value) {
        sub(/:[0-9]+$/, "", value)
        sub(/^.*:/, "", value)
        return value + 0
      }
      function location_from_line(value, fields) {
        split(value, fields, /[[:space:]]+/)
        return fields[length(fields)]
      }
      $1 == "R" {
        range_file = $2
        range_index = ++range_count[range_file]
        range_start[range_file, range_index] = $3 + 0
        range_end[range_file, range_index] = $4 + 0
        next
      }
      $1 == "A" {
        line = substr($0, 3)
        location = location_from_line(line)
        if (location !~ /:[0-9]+:[0-9]+$/) {
          next
        }
        function_index++
        function_file[function_index] = location_file(location)
        function_start[function_index] = location_line(location)
        next
      }
      $1 == "O" {
        line = substr($0, 3)
        location = location_from_line(line)
        if (location !~ /:[0-9]+:[0-9]+$/) {
          next
        }
        file = location_file(location)
        start = location_line(location)
        end = 2147483647
        for (candidate = 1; candidate <= function_index; candidate++) {
          if (function_file[candidate] == file && function_start[candidate] > start && function_start[candidate] < end) {
            end = function_start[candidate] - 1
          }
        }
        for (candidate = 1; candidate <= range_count[file]; candidate++) {
          if (range_start[file, candidate] <= end && range_end[file, candidate] >= start) {
            print line
            break
          }
        }
      }
    '
  )"

  if [[ -z "$violations" ]]; then
    printf '%s\n' "PASS  no changed functions exceed complexity $complexity_limit"
    return 0
  fi

  printf 'Functions above complexity limit %d:\n' "$complexity_limit"
  printf '%s\n' "$violations"
  return 1
}

coverage() {
  # Put the coverage command for this project here.
  printf '%s\n' "SKIP  not configured"
}

tests() {
  local failed=0

  go test ./... || failed=1
  go test -race ./... || failed=1
  scripts/install_test.sh || failed=1

  return "$failed"
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

run_gate() {
  local gate=$1
  local report
  local exit_code
  local result
  local note

  if case "$gate" in
    format) report=$(format 2>&1) ;;
    vet) report=$(vet 2>&1) ;;
    style) report=$(style 2>&1) ;;
    complexity) report=$(complexity 2>&1) ;;
    coverage) report=$(coverage 2>&1) ;;
    tests) report=$(tests 2>&1) ;;
    architecture) report=$(architecture 2>&1) ;;
  esac
  then
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

  results+=("$result")
  notes+=("$note")
  reports+=("$report")
  if [[ "$result" == FAIL ]]; then
    failed_gates+=("$gate")
  fi
  return 0
}

build_summary() {
  local summary="QUALITY GATE"
  local index
  local gate
  local result
  local note
  local report
  local detail
  local line

  for (( index = 0; index < ${#selected_gates[@]}; index++ )); do
    gate=${selected_gates[$index]}
    result=${results[$index]}
    note=${notes[$index]}
    report=${reports[$index]}
    line=$(printf '  %-14s%s' "$gate" "$result")
    if [[ -n "$note" ]]; then
      line+="   $note"
    fi
    summary+=$'\n'"$line"
    if [[ "$result" == FAIL && -n "$report" ]]; then
      detail=${report#"$note"}
      detail=${detail#$'\n'}
      if [[ -n "$detail" ]]; then
        summary+=$'\n'"$detail"
      fi
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

for gate in "${selected_gates[@]}"; do
  run_gate "$gate"
done

summary=$(build_summary)
printf '%s\n' "$summary"

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  if ! printf '%s\n' "$summary" >>"$GITHUB_STEP_SUMMARY"; then
    printf 'Could not write GitHub step summary: %s\n' "$GITHUB_STEP_SUMMARY" >&2
  fi
fi

if (( ${#failed_gates[@]} > 0 )); then
  exit 1
fi

exit 0
