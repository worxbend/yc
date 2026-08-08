#!/usr/bin/env bash
#
# Static checks over every shell script in scripts/.
#
# scripts/install.sh is piped into a shell by people who have never read it, and
# scripts/release-dry-run.sh is what decides whether a release is fit to
# publish. Neither has a compiler. This file is the closest thing they get:
#
#   1. bash -n           every script parses
#   2. shellcheck        every script is clean at the severity CI uses
#   3. shebang           every script declares bash, and is executable
#   4. strict mode       every script sets -euo pipefail
#   5. hygiene           no CRLF, no trailing whitespace, ends with a newline
#   6. no self-execution no script pipes anything into a shell, and none of
#                        them uses eval
#
# Every finding is printed as file:line: message, so it can be jumped to.
#
# USAGE
#   scripts/shell_lint_test.sh                  check scripts/*.sh
#   scripts/shell_lint_test.sh --severity error only report errors
#   scripts/shell_lint_test.sh path/to/x.sh     check specific files
#
# The default severity is style, which is what .github/workflows/ci.yml runs
# (`shellcheck -x` with no -S flag); --severity warning is the looser bar the
# task brief asked for. shellcheck is optional: when it is missing the run says
# so and still performs every other check.
#
# Exit status is 0 only when nothing was reported.
#
# See also scripts/install_test.sh, which runs install.sh for real.

set -euo pipefail

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
readonly script_dir

severity=style
strict=0
findings=0
notes=0
checked=0

usage() {
	cat <<'USAGE'
Usage: scripts/shell_lint_test.sh [options] [file...]

Static checks over the repository's shell scripts: bash -n, shellcheck,
shebang and executable bit, strict mode, whitespace hygiene, and a ban on
piping anything into a shell.

Options:
  -s, --severity LEVEL  shellcheck severity: error, warning, info or style
                        (default: style, the level CI enforces)
      --strict          Treat notes as findings too
  -h, --help            Show this help text

With no file arguments every scripts/*.sh is checked.
USAGE
}

report() {
	printf '%s\n' "$*"
	findings=$((findings + 1))
}

# note is for a rule this repository has not adopted yet: it is printed on
# every run and is not fatal unless --strict is passed.
note() {
	printf 'note: %s\n' "$*"
	notes=$((notes + 1))
}

note_at() {
	local file=$1 line=$2
	shift 2
	note "$file:$line: $*"
}

# report_at prints a finding an editor can jump to.
report_at() {
	local file=$1 line=$2
	shift 2
	report "$file:$line: $*"
}

check_shebang() {
	local file=$1 first
	first=$(head -n 1 "$file")
	case "$first" in
	'#!/usr/bin/env bash') ;;
	'#!'*bash) ;;
	'#!'*)
		report_at "$file" 1 "shebang is '$first'; these scripts require bash"
		;;
	*)
		report_at "$file" 1 "no shebang"
		;;
	esac
	if [ ! -x "$file" ]; then
		report_at "$file" 1 "not executable (chmod +x)"
	fi
}

check_parses() {
	local file=$1 output line
	if output=$(bash -n "$file" 2>&1); then
		return 0
	fi
	# A here-string keeps the loop in this shell, so the counter survives.
	while IFS= read -r line; do
		report "$line"
	done <<<"$output"
	report_at "$file" 1 "bash -n failed"
}

check_strict_mode() {
	local file=$1
	grep -qE '^set -euo pipefail$' "$file" ||
		report_at "$file" 1 "does not 'set -euo pipefail'"
}

check_hygiene() {
	local file=$1 line n=0
	local -a lines=()
	mapfile -t lines <"$file"
	for line in ${lines[@]+"${lines[@]}"}; do
		n=$((n + 1))
		case "$line" in
		*$'\r') report_at "$file" "$n" "CRLF line ending" ;;
		esac
		case "$line" in
		*[[:space:]]) report_at "$file" "$n" "trailing whitespace" ;;
		esac
	done
	if [ -n "$(tail -c 1 "$file")" ]; then
		report_at "$file" "$n" "no newline at end of file"
	fi
}

# check_no_shell_execution is the point of this file: nothing in scripts/ may
# pipe fetched bytes into a shell, and nothing may eval. Comments are exempt,
# because install.sh documents the curl-pipe invocation its users type.
check_no_shell_execution() {
	local file=$1 line n=0 code
	local -a lines=()
	mapfile -t lines <"$file"
	for line in ${lines[@]+"${lines[@]}"}; do
		n=$((n + 1))
		code=${line#"${line%%[![:space:]]*}"}
		case "$code" in
		'#'*) continue ;;
		esac
		if [[ $code =~ \|[[:space:]]*(sudo[[:space:]]+)?(ba|z|k)?sh([[:space:]]|$) ]]; then
			report_at "$file" "$n" "pipes into a shell: $code"
		fi
		if [[ $code =~ (^|[^[:alnum:]_.])eval([[:space:]]|$) ]]; then
			report_at "$file" "$n" "uses eval: $code"
		fi
		# A double-quoted trap handler is an eval in disguise: the expansion is
		# baked into the trap string, so anything in the expanded value that
		# looks like shell is shell. Use a named cleanup function instead.
		if [[ $code =~ ^trap[[:space:]]+\" ]]; then
			note_at "$file" "$n" "trap handler is double-quoted, so its expansions become code at trap time; prefer 'trap cleanup ...' with a function: $code"
		fi
	done
}

run_shellcheck() {
	local -a files=("$@")
	if ! command -v shellcheck >/dev/null 2>&1; then
		printf 'shellcheck: not installed, skipping (CI installs it)\n'
		return 0
	fi
	printf 'shellcheck %s (severity: %s)\n' "$(shellcheck --version | awk '/^version:/ {print $2}')" "$severity"
	local output
	if ! output=$(shellcheck -x -S "$severity" -f gcc "${files[@]}" 2>&1); then
		printf '%s\n' "$output" | while IFS= read -r line; do
			printf '%s\n' "$line"
		done
		findings=$((findings + $(printf '%s\n' "$output" | grep -c . || true)))
	fi
}

main() {
	local -a files=()
	while [ "$#" -gt 0 ]; do
		case "$1" in
		-s | --severity)
			[ "$#" -ge 2 ] || {
				printf 'shell_lint_test: missing value for %s\n' "$1" >&2
				exit 2
			}
			severity=$2
			shift 2
			;;
		--severity=*)
			severity=${1#*=}
			shift
			;;
		--strict)
			strict=1
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		-*)
			printf 'shell_lint_test: unknown option: %s\n\n' "$1" >&2
			usage >&2
			exit 2
			;;
		*)
			files+=("$1")
			shift
			;;
		esac
	done

	case "$severity" in
	error | warning | info | style) ;;
	*)
		printf 'shell_lint_test: severity must be error, warning, info or style\n' >&2
		exit 2
		;;
	esac

	if [ "${#files[@]}" -eq 0 ]; then
		local candidate
		for candidate in "$script_dir"/*.sh; do
			[ -e "$candidate" ] || continue
			files+=("$candidate")
		done
	fi
	if [ "${#files[@]}" -eq 0 ]; then
		printf 'no shell scripts found in %s\n' "$script_dir" >&2
		exit 1
	fi

	local file
	for file in "${files[@]}"; do
		[ -f "$file" ] || {
			report "$file:0: not a file"
			continue
		}
		checked=$((checked + 1))
		check_shebang "$file"
		check_parses "$file"
		check_strict_mode "$file"
		check_hygiene "$file"
		check_no_shell_execution "$file"
	done

	run_shellcheck "${files[@]}"

	printf '\n'
	if [ "$strict" -eq 1 ]; then
		findings=$((findings + notes))
		notes=0
	fi
	if [ "$findings" -eq 0 ]; then
		printf 'PASS %s script(s) checked, no findings, %s note(s)\n' "$checked" "$notes"
		return 0
	fi
	printf 'FAIL %s finding(s), %s note(s) across %s script(s)\n' "$findings" "$notes" "$checked"
	return 1
}

main "$@"
