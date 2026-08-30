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
  local requested_base_ref="${QUALITY_BASE_REF:-}"
  local github_base_ref="${GITHUB_BASE_REF:-}"
  local base_ref
  local -a candidate_refs=()

  if [[ -n "$requested_base_ref" ]]; then
    if git rev-parse --verify "${requested_base_ref}^{commit}" >/dev/null 2>&1; then
      printf '%s\n' "$requested_base_ref"
      return 0
    fi
    printf "QUALITY_BASE_REF '%s' does not name an existing commit.\n" "$requested_base_ref" >&2
    return 1
  fi

  if [[ -n "$github_base_ref" ]]; then
    candidate_refs+=("origin/$github_base_ref" "$github_base_ref")
    for base_ref in "${candidate_refs[@]}"; do
      if git rev-parse --verify "${base_ref}^{commit}" >/dev/null 2>&1; then
        printf '%s\n' "$base_ref"
        return 0
      fi
    done
    printf "GITHUB_BASE_REF '%s' does not name an existing ref; tried origin/%s and %s.\n" \
      "$github_base_ref" "$github_base_ref" "$github_base_ref" >&2
    return 1
  fi

  candidate_refs+=(origin/main main)

  for base_ref in "${candidate_refs[@]}"; do
    if git rev-parse --verify "${base_ref}^{commit}" >/dev/null 2>&1; then
      printf '%s\n' "$base_ref"
      return 0
    fi
  done

  printf '%s\n' "No quality-gate base ref exists; tried origin/main and main." >&2
  return 1
}

quality_merge_base() {
  local base_ref=$1
  local merge_base

  if ! merge_base="$(git merge-base "$base_ref" HEAD 2>/dev/null)"; then
    printf "Could not determine the merge base for quality-gate base ref '%s'.\n" "$base_ref" >&2
    return 1
  fi
  if [[ -z "$merge_base" ]]; then
    printf "Could not determine the merge base for quality-gate base ref '%s'.\n" "$base_ref" >&2
    return 1
  fi

  printf '%s\n' "$merge_base"
}

style() {
  local installed_version
  local base_ref
  local merge_base
  local changed_file
  local changed_files
  local changed_file_count=0
  local base_note
  local lint_output
  local lint_status=0
  local -a changed_go_files=()

  if ! base_ref="$(quality_base_ref)"; then
    echo "Could not determine the quality-gate base ref." >&2
    return 1
  fi

  if ! merge_base="$(quality_merge_base "$base_ref")"; then
    return 1
  fi

  if ! changed_files="$(git diff --name-only --diff-filter=ACMR "$merge_base" --)"; then
    printf "Could not list files changed from quality-gate merge base '%s'.\n" "$merge_base" >&2
    return 1
  fi

  while IFS= read -r changed_file; do
    if [[ -z "$changed_file" ]]; then
      continue
    fi
    (( changed_file_count += 1 ))
    case "$changed_file" in
      *.go) changed_go_files+=("$changed_file") ;;
    esac
  done <<<"$changed_files"

  base_note=$(printf 'base %s, %d changed files' "$base_ref" "$changed_file_count")

  if (( ${#changed_go_files[@]} == 0 )); then
    printf '%s\n' "$base_note"
    return 0
  fi

  if ! command -v golangci-lint >/dev/null 2>&1; then
    printf '%s\n' "$base_note"
    echo "golangci-lint ${golangci_lint_version} is required for the style gate." >&2
    return 1
  fi

  if ! installed_version="$(golangci-lint version --short)"; then
    printf '%s\n' "$base_note"
    echo "Could not determine the golangci-lint version." >&2
    return 1
  fi
  if [[ "$installed_version" != "${golangci_lint_version#v}" ]]; then
    printf '%s\n' "$base_note"
    echo "golangci-lint ${golangci_lint_version} is required; found ${installed_version}." >&2
    return 1
  fi

  lint_output="$(golangci-lint run --new-from-merge-base="$merge_base" ./... 2>&1)" || lint_status=$?
  if [[ "$lint_output" == *"Can't process results by diff processor"* ]]; then
    printf '%s\n' "FAIL  golangci-lint lost the diff; reported faults may include files this branch does not touch."
    if [[ -n "$lint_output" ]]; then
      printf '%s\n' "$lint_output"
    fi
    printf '%s\n' "$base_note"
    return 1
  fi
  printf '%s\n' "$base_note"
  if (( lint_status != 0 )); then
    if [[ -n "$lint_output" ]]; then
      printf '%s\n' "$lint_output"
    fi
    return "$lint_status"
  fi
  if [[ -n "$lint_output" ]]; then
    printf '%s\n' "$lint_output"
  fi
}

complexity() {
  local complexity_limit=10
  local base_ref
  local merge_base
  local changed_file
  local all_changed_files
  local changed_file_count=0
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

  if ! merge_base="$(quality_merge_base "$base_ref")"; then
    return 1
  fi

  if ! all_changed_files="$(git diff --name-only --diff-filter=ACMR "$merge_base" --)"; then
    printf "Could not list files changed from quality-gate merge base '%s'.\n" "$merge_base" >&2
    return 1
  fi

  while IFS= read -r changed_file; do
    if [[ -z "$changed_file" ]]; then
      continue
    fi
    (( changed_file_count += 1 ))
    case "$changed_file" in
      *_test.go) ;;
      *.go) changed_files+=("$changed_file") ;;
    esac
  done <<<"$all_changed_files"

  printf 'base %s, %d changed files\n' "$base_ref" "$changed_file_count"

  if (( ${#changed_files[@]} == 0 )); then
    return 0
  fi

  if ! command -v gocyclo >/dev/null 2>&1; then
    echo "gocyclo is required for the complexity gate." >&2
    return 1
  fi

  changed_ranges="$(
    git diff --no-ext-diff --unified=0 "$merge_base" -- "${changed_files[@]}" |
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
  local coverage_limit=80
  local coverage_profile
  local test_output
  local test_status=0
  local base_ref
  local merge_base
  local all_changed_files
  local changed_file
  local changed_file_count=0
  local changed_go_file_count=0
  local coverage_diff
  local coverage_data
  local changed_go_line_count=0
  local changed_candidate_count=0
  local examined_line_count=0
  local covered_line_count=0
  local data_type
  local data_value
  local data_extra
  local uncovered_lines=""
  local coverage_percentage

  if ! coverage_profile="$(mktemp "${TMPDIR:-/tmp}/syl-coverage.XXXXXX")"; then
    echo "Could not create the coverage profile." >&2
    return 1
  fi

  test_output="$(go test ./... -coverprofile="$coverage_profile" 2>&1)" || test_status=$?
  if (( test_status != 0 )); then
    printf '%s\n' "Coverage test run failed:"
    printf '%s\n' "$test_output"
    rm -f "$coverage_profile"
    return 1
  fi

  if ! base_ref="$(quality_base_ref)"; then
    rm -f "$coverage_profile"
    echo "Could not determine the quality-gate base ref." >&2
    return 1
  fi

  if ! merge_base="$(quality_merge_base "$base_ref")"; then
    rm -f "$coverage_profile"
    return 1
  fi

  if ! all_changed_files="$(git diff --name-only --diff-filter=ACMR "$merge_base" --)"; then
    rm -f "$coverage_profile"
    printf "Could not list files changed from quality-gate merge base '%s'.\n" "$merge_base" >&2
    return 1
  fi

  while IFS= read -r changed_file; do
    if [[ -z "$changed_file" ]]; then
      continue
    fi
    (( changed_file_count += 1 ))
    case "$changed_file" in
      *.go) (( changed_go_file_count += 1 )) ;;
    esac
  done <<<"$all_changed_files"

  coverage_diff=""
  if (( changed_go_file_count > 0 )); then
    if ! coverage_diff="$(git diff --no-ext-diff --unified=0 "$merge_base" -- '*.go')"; then
      rm -f "$coverage_profile"
      printf "Could not collect changed lines from quality-gate merge base '%s'.\n" "$merge_base" >&2
      return 1
    fi
  fi

  if ! coverage_data="$(
    awk '
      function is_code_line(content) {
        if (content ~ /^[[:space:]]*$/ ||
            content ~ /^[[:space:]]*\/\// ||
            content ~ /^[[:space:]]*\/\*/ ||
            content ~ /^[[:space:]]*\*\// ||
            content ~ /^[[:space:]]*}[[:space:]]*$/) {
          return 0
        }
        return 1
      }

      FILENAME == ARGV[1] {
        if ($0 ~ /^\+\+\+ b\//) {
          file = substr($0, 7)
          next
        }
        if ($0 ~ /^@@ /) {
          range = $0
          sub(/^@@ -[^ ]+ /, "", range)
          sub(/^\+/, "", range)
          sub(/ .*/, "", range)
          split(range, bounds, ",")
          new_line = bounds[1] + 0
          remaining_lines = bounds[2] == "" ? 1 : bounds[2] + 0
          next
        }
        if (remaining_lines == 0) {
          next
        }
        if ($0 ~ /^\+/) {
          changed_line_count++
          content = substr($0, 2)
          if (is_code_line(content)) {
            key = file SUBSEP new_line
            if (!(key in changed)) {
              changed[key] = 1
              changed_file_name[key] = file
              changed_line_number[key] = new_line
              changed_line_order[++changed_candidate_count] = key
              changed_file_names[file] = 1
            }
          }
          new_line++
          remaining_lines--
          next
        }
        if ($0 ~ /^-/) {
          next
        }
        new_line++
        remaining_lines--
        next
      }

      $0 == "mode: set" || NF < 3 {
        next
      }

      {
        location = $1
        if (location !~ /:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/) {
          next
        }
        profile_file = location
        sub(/:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/, "", profile_file)
        coordinates = location
        sub(/^.*:/, "", coordinates)
        split(coordinates, bounds, /[,.]/)
        start = bounds[1] + 0
        end = bounds[3] + 0
        hits = $3 + 0

        for (candidate_file in changed_file_names) {
          if (profile_file != candidate_file &&
              (length(profile_file) <= length(candidate_file) ||
               substr(profile_file, length(profile_file) - length(candidate_file) + 1) != candidate_file ||
               substr(profile_file, length(profile_file) - length(candidate_file), 1) != "/")) {
            continue
          }
          for (line = start; line <= end; line++) {
            key = candidate_file SUBSEP line
            if (!(key in changed)) {
              continue
            }
            examined[key] = 1
            if (hits > 0) {
              covered[key] = 1
            }
          }
        }
      }

      END {
        printf "changed\t%d\n", changed_line_count
        printf "candidates\t%d\n", changed_candidate_count
        examined_count = 0
        covered_count = 0
        for (order = 1; order <= changed_candidate_count; order++) {
          key = changed_line_order[order]
          if (!(key in examined)) {
            continue
          }
          examined_count++
          if (key in covered) {
            covered_count++
            continue
          }
          printf "uncovered\t%s\t%d\n", changed_file_name[key], changed_line_number[key]
        }
        printf "examined\t%d\n", examined_count
        printf "covered\t%d\n", covered_count
      }
    ' <(printf '%s\n' "$coverage_diff") "$coverage_profile"
  )"; then
    rm -f "$coverage_profile"
    echo "Could not read the coverage profile." >&2
    return 1
  fi
  rm -f "$coverage_profile"

  while IFS=$'\t' read -r data_type data_value data_extra; do
    case "$data_type" in
      changed) changed_go_line_count=$data_value ;;
      candidates) changed_candidate_count=$data_value ;;
      examined) examined_line_count=$data_value ;;
      covered) covered_line_count=$data_value ;;
      uncovered)
        if [[ -n "$uncovered_lines" ]]; then
          uncovered_lines+=$'\n'
        fi
        uncovered_lines+="$data_value:$data_extra"
        ;;
    esac
  done <<<"$coverage_data"

  printf 'base %s, %d changed files, Changed lines examined: %d\n' \
    "$base_ref" "$changed_file_count" "$examined_line_count"

  if (( changed_go_file_count > 0 && changed_go_line_count == 0 )); then
    printf '%s\n' "FAIL  changed Go files produced no changed lines for coverage; examined 0 lines."
    return 1
  fi

  if (( changed_candidate_count > 0 && examined_line_count == 0 )); then
    printf '%s\n' "FAIL  changed code lines produced no coverage profile entries; examined 0 lines."
    return 1
  fi

  if (( examined_line_count == 0 )); then
    printf '%s\n' "PASS  no changed code lines"
    return 0
  fi

  coverage_percentage=$(( covered_line_count * 10000 / examined_line_count ))
  printf 'Coverage of changed lines: %d.%02d%% (%d/%d), required %d%%.\n' \
    "$(( coverage_percentage / 100 ))" "$(( coverage_percentage % 100 ))" \
    "$covered_line_count" "$examined_line_count" "$coverage_limit"

  if (( covered_line_count * 100 < coverage_limit * examined_line_count )); then
    if [[ -n "$uncovered_lines" ]]; then
      printf '%s\n' "Lines without tests:"
      printf '%s\n' "$uncovered_lines"
    fi
    return 1
  fi

  printf 'PASS  changed-line coverage meets %d%%\n' "$coverage_limit"
}

tests() {
  local failed=0

  # The coverage gate runs the non-race test suite while writing its profile.
  # Keep this gate's race run separate without running the same suite twice.
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
      base\ *)
        note="$report"
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
