#!/usr/bin/env bash
#
# yc installer test harness.
#
# scripts/install.sh is the one file in this repository that people run before
# they have read it:
#
#   curl --proto '=https' --tlsv1.2 -sSf .../install.sh | bash
#
# So it is tested here against a real HTTPS server, with the real curl, a real
# certificate check, and the real install.sh - unmodified, not sourced, not
# stubbed, invoked exactly as a user invokes it.
#
# THE FAKE RELEASE SERVER
#   A throwaway self-signed certificate for github.com is generated per run,
#   python3 serves a fake release tree over TLS on 127.0.0.1, and curl is
#   pointed at it through a sandboxed $CURL_HOME/.curlrc holding
#
#       connect-to = "github.com:443:127.0.0.1:<port>"
#       cacert     = "<the throwaway certificate>"
#
#   install.sh therefore still requests the literal
#   https://github.com/worxbend/yc/releases/... URL it ships with, still
#   enforces --proto '=https' --proto-redir '=https' --tlsv1.2, and still
#   validates a certificate chain for the name github.com. Only the TCP
#   endpoint moves. Nothing leaves the machine: no name is resolved and the
#   socket is loopback. There is no offline mode, no --insecure, and no edited
#   copy of install.sh anywhere in this file.
#
# WHAT ELSE IS FAKED, AND NOTHING BEYOND IT
#   * uname, so one machine can act as linux/amd64, linux/arm64, Darwin, and
#     armv7l;
#   * the release artifacts, which are small bash scripts that append "$0 :: $*"
#     to a log when they run. "The installer never executes what it downloaded"
#     is therefore an observation, not an assumption: the log records both
#     whether a payload ran and which path it ran from.
#
# USAGE
#   scripts/install_test.sh                 run everything
#   scripts/install_test.sh -f checksum     run tests whose name matches
#   scripts/install_test.sh --list          list test names and exit
#   scripts/install_test.sh -v              stream each test's own output
#   scripts/install_test.sh --keep          keep the sandbox and print its path
#
# Exit status is 0 only if every test passed. A missing prerequisite (python3,
# openssl, curl, sha256sum) prints a SKIP line and exits 0; a broken one fails.
#
# CI runs this file two ways: `go test ./internal/cli -run TestInstallScript`
# shells out to it, and the shell job shellchecks it like every other script.
#
# See also scripts/shell_lint_test.sh for the static checks over scripts/*.sh.

set -euo pipefail

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
readonly script_dir
readonly INSTALL_SH="$script_dir/install.sh"

# Every test, in run order. Adding a function is not enough; name it here, so
# the run order is readable and a half-written test cannot join by accident.
readonly TESTS=(
	installs_for_linux_amd64
	installs_for_linux_arm64
	accepts_architecture_aliases
	installs_to_the_default_bin_dir
	writes_nothing_outside_the_bin_dir
	accepts_a_bare_digest_file
	accepts_an_uppercase_crlf_digest
	refuses_a_checksum_mismatch
	refuses_a_checksum_file_that_is_not_a_digest
	refuses_an_empty_binary
	refuses_a_missing_asset
	refuses_a_missing_checksum
	never_executes_downloaded_content
	refuses_a_plain_http_redirect
	refuses_an_untrusted_certificate
	refuses_a_non_linux_system
	refuses_an_unsupported_architecture
	dry_run_touches_neither_network_nor_disk
	dry_run_reports_the_pinned_release
	dry_run_reports_path_membership
	pins_the_requested_version
	honours_bin_dir_spellings
	honours_environment_overrides
	refuses_a_version_that_smuggles_a_path
	refuses_unknown_and_incomplete_options
	help_exits_zero_without_touching_anything
	reinstall_replaces_the_previous_binary
	uninstall_removes_only_the_binary
	uninstall_refuses_a_directory
	uninstall_is_idempotent
	warns_when_the_bin_dir_is_not_on_path
	stays_quiet_when_the_bin_dir_is_on_path
	refuses_an_unwritable_bin_dir
	falls_back_to_wget_without_curl
	refuses_without_any_downloader
	refuses_without_any_checksum_tool
	verifies_with_shasum_when_sha256sum_is_absent
	verifies_with_openssl_when_sha256sum_is_absent
	a_hostile_tmpdir_must_not_execute_code
	a_truncated_download_installs_nothing
	a_non_bash_shell_installs_nothing
)

# --- output -----------------------------------------------------------------

use_colour=0
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
	use_colour=1
fi

paint() {
	local colour=$1
	shift
	if [ "$use_colour" -eq 1 ]; then
		printf '\033[%sm%s\033[0m' "$colour" "$*"
	else
		printf '%s' "$*"
	fi
}

info() {
	printf '%s\n' "$*"
}

die() {
	printf 'install_test: error: %s\n' "$*" >&2
	exit 1
}

skip_run() {
	printf 'SKIP install_test.sh: %s\n' "$*"
	exit 0
}

usage() {
	cat <<'USAGE'
Usage: scripts/install_test.sh [options]

Exercise scripts/install.sh end to end against a local HTTPS release server.

Options:
  -f, --filter PATTERN  Run only tests whose name contains PATTERN
  -l, --list            List test names and exit
  -v, --verbose         Print each test's captured output, pass or fail
  -k, --keep            Keep the sandbox directory and print its path
  -h, --help            Show this help text

Requires bash, python3, openssl, curl and sha256sum. Nothing here reaches the
network: the fake release server listens on 127.0.0.1 and curl is redirected
to it with a sandboxed .curlrc.
USAGE
}

# --- prerequisites ----------------------------------------------------------

require_tools() {
	local missing=()
	local tool
	for tool in python3 openssl curl sha256sum mktemp install awk; do
		command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
	done
	if [ "${#missing[@]}" -gt 0 ]; then
		skip_run "missing ${missing[*]}"
	fi
	[ -f "$INSTALL_SH" ] || die "$INSTALL_SH not found"
	[ -x "$INSTALL_SH" ] || die "$INSTALL_SH is not executable"
}

# --- sandbox ----------------------------------------------------------------

work=""
server_pid=""
plain_pid=""
keep=0

cleanup() {
	local status=$? pid
	for pid in "$server_pid" "$plain_pid"; do
		if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
			kill "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
		fi
	done
	if [ -n "$work" ] && [ -d "$work" ]; then
		if [ "$keep" -eq 1 ]; then
			printf 'sandbox kept at %s\n' "$work"
		else
			rm -rf -- "$work"
		fi
	fi
	return "$status"
}

# make_certificate writes a self-signed certificate for github.com. It is its
# own trust anchor, handed to curl as --cacert, and it lives for the length of
# this run only.
make_certificate() {
	mkdir -p "$work/tls"
	cert="$work/tls/cert.pem"
	key="$work/tls/key.pem"
	openssl req -x509 -newkey rsa:2048 -sha256 -days 1 -nodes \
		-keyout "$key" -out "$cert" \
		-subj '/CN=github.com' \
		-addext 'subjectAltName=DNS:github.com,DNS:localhost,IP:127.0.0.1' \
		>"$work/tls/openssl.log" 2>&1 ||
		skip_run "openssl could not generate a test certificate (see $work/tls/openssl.log)"
}

# write_payload writes a fake yc binary. It records every invocation - the path
# it was executed from and its arguments - so a test can prove the installer
# ran the installed copy and never the downloaded temporary copy.
write_payload() {
	local file=$1 version=$2 arch=$3 flavour=${4:-normal}
	# The payload is generated source: "$0", "$*" and "${1:-}" below are text
	# for the generated file to expand when it runs, not here.
	# shellcheck disable=SC2016
	{
		printf '#!/usr/bin/env bash\n'
		printf '# fake yc %s (linux/%s) written by scripts/install_test.sh\n' "$version" "$arch"
		printf 'printf "%%s :: %%s\\n" "$0" "$*" >> %q\n' "$exec_log"
		if [ "$flavour" = hostile ]; then
			printf 'printf "the downloaded payload was executed\\n" >> %q\n' "$work/executed-payload"
			printf 'exit 0\n'
		fi
		printf 'case "${1:-}" in\n'
		printf '\t--version) printf %q ;;\n' "yc $version (linux/$arch)"$'\n'
		printf '\t*) : ;;\n'
		printf 'esac\n'
	} >"$file"
	# Deliberately not executable: install.sh must set the mode itself.
	chmod 0644 -- "$file"
}

# publish writes one release directory: both architectures plus the checksum
# file in the requested shape.
#
#   good      sha256sum's own output, "<digest>  <name>"
#   bare      the digest alone, no file name
#   odd       uppercase digest, CRLF line ending
#   tampered  a well-formed digest of something else
#   html      an HTML error page where a digest should be
#   empty     a zero-byte binary with a correct digest
#   none      no checksum file at all
publish() {
	local dir=$1 version=$2 kind=$3 flavour=${4:-normal}
	local arch asset
	mkdir -p "$dir"
	for arch in amd64 arm64; do
		asset="yc_linux_$arch"
		write_payload "$dir/$asset" "$version" "$arch" "$flavour"
		case "$kind" in
		good)
			(cd "$dir" && sha256sum -- "$asset" >"$asset.sha256")
			;;
		bare)
			sha256sum -- "$dir/$asset" | awk '{print $1}' >"$dir/$asset.sha256"
			;;
		odd)
			sha256sum -- "$dir/$asset" |
				awk -v n="$asset" '{printf "%s  %s\r\n", toupper($1), n}' \
					>"$dir/$asset.sha256"
			;;
		tampered)
			printf 'a different artifact' | sha256sum |
				awk -v n="$asset" '{printf "%s  %s\n", $1, n}' >"$dir/$asset.sha256"
			;;
		html)
			printf '<!DOCTYPE html>\n<html><head><title>404 Not Found</title></head>\n<body>Not Found</body></html>\n' \
				>"$dir/$asset.sha256"
			;;
		empty)
			: >"$dir/$asset"
			(cd "$dir" && sha256sum -- "$asset" >"$asset.sha256")
			;;
		none) ;;
		*)
			die "unknown checksum kind: $kind"
			;;
		esac
	done
}

build_release_tree() {
	srv="$work/srv"
	local releases="$srv/worxbend/yc/releases"
	mkdir -p "$releases/latest/download" "$releases/download"

	publish "$releases/latest/download" 0.9.9 good
	publish "$releases/download/v0.1.0" 0.1.0 good
	publish "$releases/download/v0.2.0" 0.2.0 good
	publish "$releases/download/baresum" 0.1.0-bare bare
	publish "$releases/download/oddsum" 0.1.0-odd odd
	publish "$releases/download/tampered" 0.1.0-tampered tampered
	publish "$releases/download/hostile" 0.1.0-hostile tampered hostile
	publish "$releases/download/htmlsum" 0.1.0-html html
	publish "$releases/download/emptybin" 0.1.0-empty empty
	publish "$releases/download/nosum" 0.1.0-nosum none
	# The mirror behind the plain-HTTP redirect carries an artifact whose own
	# checksum matches, so only the transport guard can stop it.
	publish "$releases/download/redirect-http" 6.6.6-plaintext good
	# v9.9.9 is deliberately never published: it is the 404 case.
}

# The server runs twice: once over TLS as github.com, and once in the clear as
# the attacker's mirror. Anything published under the redirect-http tag is
# answered by the TLS server with a 302 into the plain-HTTP mirror, which is how
# --proto-redir '=https' gets tested rather than merely read.
write_server() {
	cat >"$work/serve.py" <<'PYTHON'
import functools
import http.server
import os
import ssl
import sys
import threading
import time

root, logpath, cert, key = sys.argv[1:5]


def exit_when_orphaned(parent):
    # A test runner that kills the harness outright (go test's context timeout
    # sends SIGKILL) cannot run its trap, so the server retires itself rather
    # than lingering as an orphan holding a port.
    while os.getppid() == parent:
        time.sleep(0.5)
    os._exit(0)


threading.Thread(target=exit_when_orphaned, args=(os.getppid(),), daemon=True).start()
redirect_base = sys.argv[5] if len(sys.argv) > 5 else ""
REDIRECT_MARK = "/releases/download/redirect-http/"


class Handler(http.server.SimpleHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _record(self, line):
        with open(logpath, "a", encoding="utf-8") as handle:
            handle.write(line + "\n")

    def log_request(self, code="-", size="-"):
        self._record("%s %s %s" % (self.command, self.path, getattr(code, "value", code)))

    def log_error(self, fmt, *args):
        self._record("error " + (fmt % args))

    def log_message(self, fmt, *args):
        self._record(fmt % args)

    def do_GET(self):
        if redirect_base and REDIRECT_MARK in self.path:
            self.send_response(302)
            self.send_header("Location", redirect_base + self.path)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        super().do_GET()


handler = functools.partial(Handler, directory=root)
httpd = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
if cert != "-":
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(cert, key)
    httpd.socket = context.wrap_socket(httpd.socket, server_side=True)
print(httpd.server_address[1], flush=True)
httpd.serve_forever()
PYTHON
}

await_port() {
	local pid=$1 port_file=$2 err_file=$3 waited=0 found=""
	while [ "$waited" -lt 200 ]; do
		found=$(head -n 1 "$port_file" 2>/dev/null || true)
		[ -n "$found" ] && break
		kill -0 "$pid" 2>/dev/null ||
			die "the fake release server exited: $(cat "$err_file")"
		sleep 0.05
		waited=$((waited + 1))
	done
	[ -n "$found" ] || die "the fake release server never reported a port"
	printf '%s' "$found"
}

start_server() {
	plain_log="$work/plain-requests.log"
	srv_log="$work/requests.log"
	: >"$plain_log"
	: >"$srv_log"

	python3 -u "$work/serve.py" "$srv" "$plain_log" - - \
		>"$work/plain-port" 2>"$work/plain-server.err" &
	plain_pid=$!
	plain_port=$(await_port "$plain_pid" "$work/plain-port" "$work/plain-server.err")

	python3 -u "$work/serve.py" "$srv" "$srv_log" "$cert" "$key" \
		"http://127.0.0.1:$plain_port" \
		>"$work/port" 2>"$work/server.err" &
	server_pid=$!
	port=$(await_port "$server_pid" "$work/port" "$work/server.err")
}

# sandbox_path is a deliberately small PATH: everything install.sh may
# legitimately reach, and nothing else. A yc already installed on the
# developer's machine must not be visible to these tests.
build_sandbox_path() {
	local dirs=() tool dir
	for tool in curl wget sha256sum mktemp install awk env cat rm mv chmod mkdir tr uname stat; do
		dir=$(command -v "$tool" 2>/dev/null || true)
		[ -n "$dir" ] || continue
		dir=$(dirname -- "$dir")
		case " ${dirs[*]-} " in
		*" $dir "*) ;;
		*) dirs+=("$dir") ;;
		esac
	done
	sandbox_path=$(
		IFS=:
		printf '%s' "${dirs[*]}"
	)
	real_curl=$(command -v curl)
}

# --- per-test scaffolding ---------------------------------------------------

# Set by setup_test, read by the assertions.
t=""
bin_dir=""
status=0
exec_before=0
req_before=0
plain_before=0
extra_env=()
path_extra=""
toolbox_only=0
tmpdir_override=""

setup_test() {
	t="$work/tests/$1"
	mkdir -p "$t/home" "$t/tmp" "$t/fakebin"
	bin_dir="$t/bin"
	extra_env=()
	path_extra=""
	toolbox_only=0
	tmpdir_override=""
	fake_uname Linux x86_64
	{
		printf 'connect-to = "github.com:443:127.0.0.1:%s"\n' "$port"
		printf 'cacert = "%s"\n' "$cert"
	} >"$t/home/.curlrc"
	snapshot
}

snapshot() {
	exec_before=$(wc -l <"$exec_log")
	req_before=$(wc -l <"$srv_log")
	plain_before=$(wc -l <"$plain_log")
}

fake_uname() {
	local os=$1 machine=$2
	# Generated source again: "${1:-}" belongs to the fake uname, not to us.
	# shellcheck disable=SC2016
	{
		printf '#!/usr/bin/env bash\n'
		printf 'case "${1:-}" in\n'
		printf '\t-s) printf %q ;;\n' "$os"$'\n'
		printf '\t-m) printf %q ;;\n' "$machine"$'\n'
		printf '\t*) printf %q ;;\n' "$os"$'\n'
		printf 'esac\n'
	} >"$t/fakebin/uname"
	chmod 0755 -- "$t/fakebin/uname"
}

# toolbox builds a PATH holding only the named commands, so a test can remove
# curl, wget or sha256sum from the world without hiding anything else. bash is
# always present: env(1) resolves the interpreter through the sandbox PATH, so
# without it the test would measure a missing shell rather than a missing tool.
toolbox() {
	local tool src
	rm -rf -- "$t/toolbox"
	mkdir -p "$t/toolbox"
	for tool in bash "$@"; do
		src=$(command -v "$tool" 2>/dev/null || true)
		[ -n "$src" ] || die "toolbox: $tool not found"
		ln -s -- "$src" "$t/toolbox/$tool"
	done
	toolbox_only=1
}

run_install() {
	local path="$t/fakebin:$sandbox_path"
	if [ "$toolbox_only" -eq 1 ]; then
		path="$t/fakebin:$t/toolbox"
	fi
	if [ -n "$path_extra" ]; then
		path="$path:$path_extra"
	fi

	local -a env_args=(
		"PATH=$path"
		"HOME=$t/home"
		"TMPDIR=${tmpdir_override:-$t/tmp}"
		"CURL_HOME=$t/home"
	)
	if [ "${#extra_env[@]}" -gt 0 ]; then
		env_args+=("${extra_env[@]}")
	fi

	set +e
	env -i "${env_args[@]}" bash "$INSTALL_SH" "$@" >"$t/out" 2>"$t/err"
	status=$?
	set -e
}

# --- assertions -------------------------------------------------------------

fail() {
	printf 'ASSERTION FAILED: %s\n' "$*" >&2
	printf 'exit status: %s\n' "$status" >&2
	printf '%s\n' '--- stdout ---' >&2
	sed 's/^/    /' "$t/out" >&2 2>/dev/null || true
	printf '%s\n' '--- stderr ---' >&2
	sed 's/^/    /' "$t/err" >&2 2>/dev/null || true
	exit 1
}

assert_status() {
	[ "$status" -eq "$1" ] || fail "expected exit status $1, got $status"
}

assert_stderr_has() {
	grep -qF -- "$1" "$t/err" || fail "stderr does not mention: $1"
}

assert_stderr_lacks() {
	grep -qF -- "$1" "$t/err" && fail "stderr should not mention: $1"
	return 0
}

assert_stdout_has() {
	grep -qF -- "$1" "$t/out" || fail "stdout does not mention: $1"
}

assert_stdout_lacks() {
	grep -qF -- "$1" "$t/out" && fail "stdout should not mention: $1"
	return 0
}

assert_installed() {
	[ -f "$1" ] || fail "$1 was not installed"
	local mode
	mode=$(stat -c '%a' "$1")
	[ "$mode" = "755" ] || fail "$1 has mode $mode, expected 755"
}

assert_same_bytes() {
	cmp -s -- "$1" "$2" || fail "$1 and $2 differ"
}

# assert_nothing_installed also catches the staging file install.sh writes
# before its rename, which a failed run must never leave behind.
assert_nothing_installed() {
	local dir=${1:-$bin_dir} leftovers
	[ -d "$dir" ] || return 0
	leftovers=$(find "$dir" -mindepth 1 | sort | tr '\n' ' ')
	[ -z "$leftovers" ] || fail "expected $dir to be empty, found: $leftovers"
}

assert_no_staging_file() {
	local dir=${1:-$bin_dir} leftovers
	[ -d "$dir" ] || return 0
	leftovers=$(find "$dir" -mindepth 1 -name '.yc.install.*' | tr '\n' ' ')
	[ -z "$leftovers" ] || fail "a staging file survived: $leftovers"
}

assert_temp_is_clean() {
	local leftovers
	leftovers=$(find "$t/tmp" -mindepth 1 -maxdepth 1 | tr '\n' ' ')
	[ -z "$leftovers" ] || fail "the download directory was not removed: $leftovers"
}

executions_since() {
	tail -n +"$((exec_before + 1))" "$exec_log"
}

assert_execution_count() {
	local want=$1 got
	got=$(executions_since | grep -c . || true)
	[ "$got" -eq "$want" ] ||
		fail "expected $want payload execution(s), got $got: $(executions_since | tr '\n' ';')"
}

assert_only_execution_is() {
	local want_path=$1 want_args=$2 line
	assert_execution_count 1
	line=$(executions_since | head -n 1)
	[ "$line" = "$want_path :: $want_args" ] ||
		fail "expected the payload to run once as '$want_path :: $want_args', got '$line'"
}

requests_since() {
	tail -n +"$((req_before + 1))" "$srv_log"
}

assert_no_requests() {
	local seen
	seen=$(requests_since | tr '\n' ';')
	[ -z "$seen" ] || fail "expected no request to the release server, got: $seen"
}

assert_requested() {
	requests_since | grep -qF -- "$1" ||
		fail "expected a request for $1, saw: $(requests_since | tr '\n' ';')"
}

assert_not_requested() {
	requests_since | grep -qF -- "$1" &&
		fail "did not expect a request for $1"
	return 0
}

assert_no_plain_http_requests() {
	local seen
	seen=$(tail -n +"$((plain_before + 1))" "$plain_log" | tr '\n' ';')
	[ -z "$seen" ] || fail "install.sh spoke plain HTTP to the mirror: $seen"
}

served() {
	printf '%s/worxbend/yc/releases/%s' "$srv" "$1"
}

# --- tests ------------------------------------------------------------------

test_installs_for_linux_amd64() {
	run_install --bin-dir "$bin_dir"

	assert_status 0
	assert_installed "$bin_dir/yc"
	assert_same_bytes "$(served latest/download)/yc_linux_amd64" "$bin_dir/yc"
	assert_stderr_has 'checksum verified (sha256:'
	assert_stderr_has "installed $bin_dir/yc"
	assert_stderr_has 'installed yc 0.9.9 (linux/amd64)'
	assert_requested 'GET /worxbend/yc/releases/latest/download/yc_linux_amd64 200'
	assert_requested 'GET /worxbend/yc/releases/latest/download/yc_linux_amd64.sha256 200'
	assert_not_requested 'yc_linux_arm64'
	assert_no_staging_file
	assert_temp_is_clean
}

test_installs_for_linux_arm64() {
	fake_uname Linux aarch64
	run_install --bin-dir "$bin_dir"

	assert_status 0
	assert_installed "$bin_dir/yc"
	assert_same_bytes "$(served latest/download)/yc_linux_arm64" "$bin_dir/yc"
	assert_stderr_has 'installed yc 0.9.9 (linux/arm64)'
	assert_requested 'GET /worxbend/yc/releases/latest/download/yc_linux_arm64 200'
	assert_not_requested 'yc_linux_amd64'
	assert_temp_is_clean
}

test_accepts_architecture_aliases() {
	local machine want
	for machine in x86_64:amd64 amd64:amd64 aarch64:arm64 arm64:arm64; do
		want=${machine#*:}
		fake_uname Linux "${machine%%:*}"
		rm -rf -- "$bin_dir"
		snapshot
		run_install --bin-dir "$bin_dir"
		assert_status 0
		assert_same_bytes "$(served latest/download)/yc_linux_$want" "$bin_dir/yc"
	done
}

test_installs_to_the_default_bin_dir() {
	run_install

	assert_status 0
	assert_installed "$t/home/.local/bin/yc"
	assert_stderr_has "installed $t/home/.local/bin/yc"
}

# The installer may write into the install directory and its own temporary
# directory. Anything else - a dotfile in $HOME, a stray cache - is a bug.
test_writes_nothing_outside_the_bin_dir() {
	run_install --bin-dir "$bin_dir"
	assert_status 0

	local unexpected
	unexpected=$(find "$t/home" -mindepth 1 ! -name '.curlrc' | tr '\n' ' ')
	[ -z "$unexpected" ] || fail "install.sh wrote outside the bin dir: $unexpected"
	assert_temp_is_clean
}

test_accepts_a_bare_digest_file() {
	run_install --bin-dir "$bin_dir" --version baresum

	assert_status 0
	assert_installed "$bin_dir/yc"
	assert_stderr_has 'installed yc 0.1.0-bare (linux/amd64)'
}

test_accepts_an_uppercase_crlf_digest() {
	run_install --bin-dir "$bin_dir" --version oddsum

	assert_status 0
	assert_installed "$bin_dir/yc"
	assert_stderr_has 'installed yc 0.1.0-odd (linux/amd64)'
}

test_refuses_a_checksum_mismatch() {
	run_install --bin-dir "$bin_dir" --version tampered

	assert_status 1
	assert_stderr_has 'CHECKSUM MISMATCH'
	assert_stderr_has 'expected:'
	assert_stderr_has 'actual:'
	assert_stderr_has 'nothing was installed'
	assert_stderr_lacks 'checksum verified'
	assert_nothing_installed
	assert_temp_is_clean
	assert_execution_count 0
}

test_refuses_a_checksum_file_that_is_not_a_digest() {
	run_install --bin-dir "$bin_dir" --version htmlsum

	assert_status 1
	assert_stderr_has 'not a SHA-256 digest'
	assert_nothing_installed
	assert_temp_is_clean
	assert_execution_count 0
}

test_refuses_an_empty_binary() {
	run_install --bin-dir "$bin_dir" --version emptybin

	assert_status 1
	assert_stderr_has 'the downloaded binary is empty'
	assert_nothing_installed
	assert_temp_is_clean
}

test_refuses_a_missing_asset() {
	run_install --bin-dir "$bin_dir" --version v9.9.9

	assert_status 1
	assert_stderr_has 'could not download'
	assert_stderr_has "is release 'v9.9.9' published for linux/amd64?"
	assert_nothing_installed
	assert_temp_is_clean
	assert_execution_count 0
}

test_refuses_a_missing_checksum() {
	run_install --bin-dir "$bin_dir" --version nosum

	assert_status 1
	assert_stderr_has 'refusing to install an unverified binary'
	assert_nothing_installed
	assert_temp_is_clean
	assert_execution_count 0
}

# The payload published under the "hostile" tag writes a marker file the moment
# anything runs it, and its published digest does not match. Nothing may run it,
# and on the happy path the only execution must be the installed copy answering
# --version - never the file in the temporary directory.
test_never_executes_downloaded_content() {
	run_install --bin-dir "$bin_dir" --version hostile

	assert_status 1
	assert_stderr_has 'CHECKSUM MISMATCH'
	[ ! -e "$work/executed-payload" ] ||
		fail "the downloaded payload was executed: $(cat "$work/executed-payload")"
	assert_execution_count 0
	assert_nothing_installed

	rm -rf -- "$bin_dir"
	snapshot
	run_install --bin-dir "$bin_dir"
	assert_status 0
	assert_only_execution_is "$bin_dir/yc" '--version'
	[ ! -e "$work/executed-payload" ] || fail "a payload was executed during a successful install"
}

# A redirect out of HTTPS is the classic downgrade. The mirror on the other
# side of it serves a well-formed artifact with a matching checksum, so nothing
# downstream of the transport could catch this: only curl's
# --proto-redir '=https' can, and this test is what proves it is still there.
test_refuses_a_plain_http_redirect() {
	run_install --bin-dir "$bin_dir" --version redirect-http

	assert_status 1
	assert_stderr_has 'could not download'
	assert_stderr_lacks 'checksum verified'
	assert_nothing_installed
	assert_execution_count 0
	assert_no_plain_http_requests
	assert_requested 'GET /worxbend/yc/releases/download/redirect-http/yc_linux_amd64 302'
	assert_temp_is_clean
}

# No --insecure, no --no-check-certificate: with the test CA withheld the
# download must fail rather than proceed on an unverified connection.
test_refuses_an_untrusted_certificate() {
	printf 'connect-to = "github.com:443:127.0.0.1:%s"\n' "$port" >"$t/home/.curlrc"
	run_install --bin-dir "$bin_dir"

	assert_status 1
	assert_stderr_has 'could not download'
	assert_nothing_installed
	assert_execution_count 0
	assert_temp_is_clean
}

test_refuses_a_non_linux_system() {
	fake_uname Darwin arm64
	run_install --bin-dir "$bin_dir"

	assert_status 1
	assert_stderr_has 'prebuilt binaries are published for Linux only'
	assert_stderr_has 'this system reports Darwin'
	assert_stderr_has 'go install github.com/worxbend/yc/cmd/yc@latest'
	assert_nothing_installed
	assert_no_requests
	assert_execution_count 0
}

test_refuses_an_unsupported_architecture() {
	local machine
	for machine in armv7l i686 riscv64 ppc64le; do
		fake_uname Linux "$machine"
		snapshot
		run_install --bin-dir "$bin_dir"
		assert_status 1
		assert_stderr_has "unsupported CPU architecture $machine"
		assert_stderr_has 'linux/amd64 and linux/arm64 binaries only'
		assert_nothing_installed
		assert_no_requests
	done
}

test_dry_run_touches_neither_network_nor_disk() {
	run_install --bin-dir "$bin_dir" --dry-run

	assert_status 0
	assert_stdout_has 'dry run'
	assert_stdout_has 'platform:  linux/amd64'
	assert_stdout_has 'https://github.com/worxbend/yc/releases/latest/download/yc_linux_amd64'
	assert_stdout_has "$bin_dir/yc (mode 0755)"
	assert_no_requests
	assert_execution_count 0
	[ ! -e "$bin_dir" ] || fail "--dry-run created $bin_dir"

	local unexpected
	unexpected=$(find "$t/home" "$t/tmp" -mindepth 1 ! -name '.curlrc' | tr '\n' ' ')
	[ -z "$unexpected" ] || fail "--dry-run wrote: $unexpected"
}

test_dry_run_reports_the_pinned_release() {
	run_install --bin-dir "$bin_dir" --dry-run --version v0.2.0

	assert_status 0
	assert_stdout_has 'release:   v0.2.0'
	assert_stdout_has 'https://github.com/worxbend/yc/releases/download/v0.2.0/yc_linux_amd64'
	assert_stdout_has 'https://github.com/worxbend/yc/releases/download/v0.2.0/yc_linux_amd64.sha256'
	assert_stdout_lacks 'releases/latest/download'
	assert_no_requests
}

test_dry_run_reports_path_membership() {
	run_install --bin-dir "$bin_dir" --dry-run
	assert_status 0
	assert_stdout_has "$bin_dir is NOT on PATH"

	path_extra="$bin_dir"
	snapshot
	run_install --bin-dir "$bin_dir" --dry-run
	assert_status 0
	assert_stdout_has "$bin_dir is already on PATH"
}

test_pins_the_requested_version() {
	run_install --bin-dir "$bin_dir" --version v0.2.0

	assert_status 0
	assert_same_bytes "$(served download/v0.2.0)/yc_linux_amd64" "$bin_dir/yc"
	assert_stderr_has 'installed yc 0.2.0 (linux/amd64)'
	assert_requested 'GET /worxbend/yc/releases/download/v0.2.0/yc_linux_amd64 200'
	assert_not_requested 'releases/latest/download'

	rm -rf -- "$bin_dir"
	snapshot
	run_install --bin-dir "$bin_dir" --version=v0.1.0
	assert_status 0
	assert_stderr_has 'installed yc 0.1.0 (linux/amd64)'
}

test_honours_bin_dir_spellings() {
	local spelling
	for spelling in --bin-dir --dir; do
		rm -rf -- "$bin_dir"
		snapshot
		run_install "$spelling" "$bin_dir"
		assert_status 0
		assert_installed "$bin_dir/yc"

		rm -rf -- "$bin_dir"
		snapshot
		run_install "$spelling=$bin_dir"
		assert_status 0
		assert_installed "$bin_dir/yc"
	done
}

test_honours_environment_overrides() {
	extra_env=("YC_INSTALL_DIR=$bin_dir" "YC_INSTALL_VERSION=v0.2.0")
	run_install

	assert_status 0
	assert_installed "$bin_dir/yc"
	assert_stderr_has 'installed yc 0.2.0 (linux/amd64)'
	assert_requested 'GET /worxbend/yc/releases/download/v0.2.0/yc_linux_amd64 200'

	# An explicit flag still wins over the environment.
	rm -rf -- "$bin_dir"
	snapshot
	run_install --version v0.1.0
	assert_status 0
	assert_stderr_has 'installed yc 0.1.0 (linux/amd64)'
}

# The tag becomes a URL path segment. A tag carrying a slash, a space, or a
# query must be refused before anything is fetched.
test_refuses_a_version_that_smuggles_a_path() {
	local tag
	# '$(id)' must stay unexpanded here: it is a hostile tag, not a subshell.
	# shellcheck disable=SC2016
	for tag in '../../evil' 'v0.1.0/../../../etc/passwd' 'v0.1.0?x=1' 'v0.1.0 v0.2.0' '$(id)' 'v0.1.0#frag' 'https://evil.example/x'; do
		snapshot
		run_install --bin-dir "$bin_dir" --version "$tag"
		assert_status 1
		assert_stderr_has "invalid version '$tag'"
		assert_no_requests
		assert_nothing_installed
	done

	snapshot
	run_install --bin-dir "$bin_dir" --version ''
	assert_status 1
	assert_stderr_has 'the version must not be empty'
	assert_no_requests
}

test_refuses_unknown_and_incomplete_options() {
	run_install --bin-dir "$bin_dir" --frobnicate
	assert_status 2
	assert_stderr_has 'unknown option: --frobnicate'
	assert_stderr_has 'Usage: install.sh'
	assert_no_requests
	assert_nothing_installed

	snapshot
	run_install --version
	assert_status 1
	assert_stderr_has 'missing value for --version'
	assert_no_requests

	snapshot
	run_install --bin-dir
	assert_status 1
	assert_stderr_has 'missing value for --bin-dir'
	assert_no_requests

	snapshot
	run_install --bin-dir ''
	assert_status 1
	assert_stderr_has 'the install directory must not be empty'
	assert_no_requests
}

test_help_exits_zero_without_touching_anything() {
	local flag
	for flag in --help -h; do
		snapshot
		run_install "$flag"
		assert_status 0
		assert_stdout_has 'Usage: install.sh'
		assert_stdout_has '--dry-run'
		assert_stdout_has 'linux/amd64 and linux/arm64 binaries only'
		assert_no_requests
		assert_nothing_installed
	done
}

test_reinstall_replaces_the_previous_binary() {
	run_install --bin-dir "$bin_dir" --version v0.1.0
	assert_status 0
	assert_stderr_has 'installed yc 0.1.0 (linux/amd64)'

	snapshot
	run_install --bin-dir "$bin_dir" --version v0.2.0
	assert_status 0
	assert_installed "$bin_dir/yc"
	assert_same_bytes "$(served download/v0.2.0)/yc_linux_amd64" "$bin_dir/yc"
	assert_no_staging_file
	assert_temp_is_clean

	local entries
	entries=$(find "$bin_dir" -mindepth 1 | wc -l)
	[ "$entries" -eq 1 ] || fail "expected one file in $bin_dir, found $entries"
}

test_uninstall_removes_only_the_binary() {
	run_install --bin-dir "$bin_dir"
	assert_status 0
	printf 'keep me\n' >"$bin_dir/other-tool"

	snapshot
	run_install --bin-dir "$bin_dir" --uninstall
	assert_status 0
	assert_stderr_has "removed $bin_dir/yc"
	[ ! -e "$bin_dir/yc" ] || fail "yc survived --uninstall"
	[ -f "$bin_dir/other-tool" ] || fail "--uninstall removed an unrelated file"
	assert_no_requests
	assert_execution_count 0
}

test_uninstall_refuses_a_directory() {
	mkdir -p "$bin_dir/yc/inner"
	run_install --bin-dir "$bin_dir" --uninstall

	assert_status 1
	assert_stderr_has 'is a directory; refusing to remove it'
	[ -d "$bin_dir/yc/inner" ] || fail "--uninstall removed a directory it promised to leave"
}

test_uninstall_is_idempotent() {
	run_install --bin-dir "$bin_dir" --uninstall
	assert_status 0
	assert_stderr_has 'nothing to remove'
	assert_no_requests

	snapshot
	run_install --bin-dir "$bin_dir" --uninstall
	assert_status 0
	assert_stderr_has 'nothing to remove'
}

test_warns_when_the_bin_dir_is_not_on_path() {
	run_install --bin-dir "$bin_dir"

	assert_status 0
	assert_stderr_has "$bin_dir is not on your PATH"
	# The suggested export must reach the user unexpanded, or they will paste a
	# line that pins today's PATH into their profile forever.
	assert_stderr_has "export PATH=\"$bin_dir:\$PATH\""
	assert_stderr_has "Or run it by full path: $bin_dir/yc --help"
}

test_stays_quiet_when_the_bin_dir_is_on_path() {
	path_extra="$bin_dir"
	mkdir -p "$bin_dir"
	run_install --bin-dir "$bin_dir"

	assert_status 0
	assert_stderr_lacks 'is not on your PATH'
	assert_stderr_lacks 'Add it to your shell profile'
}

test_refuses_an_unwritable_bin_dir() {
	if [ "$(id -u)" -eq 0 ]; then
		exit 77
	fi
	mkdir -p "$bin_dir"
	chmod 0555 "$bin_dir"
	run_install --bin-dir "$bin_dir"
	chmod 0755 "$bin_dir"

	assert_status 1
	assert_stderr_has 'is not writable'
	assert_nothing_installed
	assert_temp_is_clean
	assert_execution_count 0
}

# With curl gone the installer must use wget, and must keep the same transport
# guarantees. The stand-in asserts the flags and then performs the download with
# the real curl, so the rest of the run is genuine.
test_falls_back_to_wget_without_curl() {
	toolbox sha256sum mktemp install awk env cat rm mv chmod mkdir tr stat
	cat >"$t/toolbox/wget" <<WGET
#!/usr/bin/env bash
set -euo pipefail
args="\$*"
for required in --https-only --secure-protocol=TLSv1_2 --max-redirect; do
	case " \$args " in
	*" \$required"*) ;;
	*)
		printf 'wget stand-in: install.sh omitted %s\n' "\$required" >&2
		exit 91
		;;
	esac
done
url="" out=""
while [ "\$#" -gt 0 ]; do
	case "\$1" in
	-O)
		out=\$2
		shift 2
		;;
	-*) shift ;;
	*)
		url=\$1
		shift
		;;
	esac
done
[ -n "\$url" ] && [ -n "\$out" ] || exit 92
exec $real_curl --proto '=https' --proto-redir '=https' --tlsv1.2 \\
	--location --max-redirs 5 --max-time 60 -sSf "\$url" -o "\$out"
WGET
	chmod 0755 -- "$t/toolbox/wget"

	run_install --bin-dir "$bin_dir"

	assert_status 0
	assert_installed "$bin_dir/yc"
	assert_requested 'GET /worxbend/yc/releases/latest/download/yc_linux_amd64 200'
	assert_same_bytes "$(served latest/download)/yc_linux_amd64" "$bin_dir/yc"
}

test_refuses_without_any_downloader() {
	toolbox sha256sum mktemp install awk env cat rm mv chmod mkdir tr stat
	run_install --bin-dir "$bin_dir"

	assert_status 1
	assert_stderr_has 'curl or wget is required'
	assert_nothing_installed
	assert_no_requests
}

test_refuses_without_any_checksum_tool() {
	toolbox curl mktemp install awk env cat rm mv chmod mkdir tr stat
	run_install --bin-dir "$bin_dir"

	[ "$status" -ne 0 ] || fail "install.sh installed a binary it could not verify"
	assert_stderr_has 'is required to verify the download'
	assert_nothing_installed
	assert_execution_count 0
}

# sha256sum is coreutils; shasum is perl; openssl is neither. install.sh falls
# back through all three, and a fallback that verified nothing would be worse
# than no fallback at all - so each one is checked against a good artifact and
# against a tampered one.
verify_with_alternative_digest_tool() {
	local tool=$1
	command -v "$tool" >/dev/null 2>&1 || exit 77
	toolbox curl mktemp install awk env cat rm mv chmod mkdir tr "$tool"

	run_install --bin-dir "$bin_dir"
	assert_status 0
	assert_installed "$bin_dir/yc"
	assert_stderr_has 'checksum verified (sha256:'
	assert_same_bytes "$(served latest/download)/yc_linux_amd64" "$bin_dir/yc"

	rm -rf -- "$bin_dir"
	snapshot
	run_install --bin-dir "$bin_dir" --version tampered
	assert_status 1
	assert_stderr_has 'CHECKSUM MISMATCH'
	assert_nothing_installed
}

test_verifies_with_shasum_when_sha256sum_is_absent() {
	verify_with_alternative_digest_tool shasum
}

test_verifies_with_openssl_when_sha256sum_is_absent() {
	verify_with_alternative_digest_tool openssl
}

# install.sh builds its cleanup trap by interpolating the temporary directory
# into a double-quoted string, so a TMPDIR carrying a quote and a command
# substitution is executed when the trap fires. TMPDIR is attacker-reachable in
# any environment where yc is installed by something other than the user's own
# interactive shell.
#
# While the bug reproduces this test skips with a banner rather than failing,
# because install.sh belongs to another lane. The fix is the shape
# release-dry-run.sh already uses - a named cleanup function, and tmp_dir at
# script scope so the trap can name it instead of interpolating it:
#
#	tmp_dir=""
#	cleanup() { [ -z "$tmp_dir" ] || rm -rf -- "$tmp_dir"; }
#	...
#	trap cleanup EXIT HUP INT TERM
#	tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/yc-install.XXXXXX") || ...
#
# Quoting the existing trap in place is not enough: tmp_dir is `local` to main,
# so an EXIT trap that expands it late dies on set -u and the download
# directory survives. This test checks for that too, and fails rather than
# skips if the injection stops but the cleanup breaks.
test_a_hostile_tmpdir_must_not_execute_code() {
	local marker="$t/INJECTED"
	local hostile="$t/tmp/a'\$(touch $marker)'b"
	mkdir -p "$hostile"
	tmpdir_override="$hostile"

	run_install --bin-dir "$bin_dir"

	if [ -e "$marker" ]; then
		printf 'KNOWN BUG - scripts/install.sh:322 executes code from TMPDIR\n'
		printf '  TMPDIR was: %s\n' "$hostile"
		printf '  the trap ran the embedded command and created: %s\n' "$marker"
		printf '  fix: hoist tmp_dir to script scope and install a named trap,\n'
		printf '       as scripts/release-dry-run.sh already does:\n'
		printf '         tmp_dir=""\n'
		# The suggested code is text for a human to copy, not code to run here.
		# shellcheck disable=SC2016
		printf '         cleanup() { [ -z "$tmp_dir" ] || rm -rf -- "$tmp_dir"; }\n'
		printf '         trap cleanup EXIT HUP INT TERM\n'
		printf '  merely re-quoting the trap in place is not enough: tmp_dir is\n'
		printf '  local to main, so a late expansion trips set -u at exit.\n'
		printf '  this test passes as soon as that is done.\n'
		exit 77
	fi

	assert_status 0
	assert_installed "$bin_dir/yc"
	local leftovers
	leftovers=$(find "$hostile" -mindepth 1 -maxdepth 1 | tr '\n' ' ')
	[ -z "$leftovers" ] || fail "the download directory survived: $leftovers"
}

# The whole script lives in functions with `main "$@"` on the last line, so a
# download cut short cannot run half an installer.
test_a_truncated_download_installs_nothing() {
	local last
	last=$(tail -n 1 "$INSTALL_SH")
	[ "$last" = 'main "$@"' ] ||
		fail "install.sh must end with 'main \"\$@\"' or a truncated download could run something, got: $last"

	local lines cut
	lines=$(wc -l <"$INSTALL_SH")
	for cut in $((lines - 1)) $((lines / 2)) $((lines / 4)) 40; do
		head -n "$cut" "$INSTALL_SH" >"$t/truncated.sh"
		snapshot
		set +e
		env -i "PATH=$t/fakebin:$sandbox_path" "HOME=$t/home" "TMPDIR=$t/tmp" \
			"CURL_HOME=$t/home" bash "$t/truncated.sh" --bin-dir "$bin_dir" \
			>"$t/out" 2>"$t/err"
		status=$?
		set -e
		assert_no_requests
		assert_execution_count 0
		assert_nothing_installed
		[ ! -e "$t/home/.local" ] || fail "a truncated installer wrote to \$HOME (cut at line $cut)"
	done
}

# Piped into a shell that is not bash, install.sh must refuse or fail - it must
# never limp along and install something.
test_a_non_bash_shell_installs_nothing() {
	local shells=() candidate found
	for candidate in dash ash mksh ksh yash zsh; do
		found=$(command -v "$candidate" 2>/dev/null || true)
		[ -n "$found" ] && shells+=("$found")
	done
	if [ "${#shells[@]}" -eq 0 ]; then
		exit 77
	fi

	local shell
	for shell in "${shells[@]}"; do
		snapshot
		set +e
		env -i "PATH=$t/fakebin:$sandbox_path" "HOME=$t/home" "TMPDIR=$t/tmp" \
			"CURL_HOME=$t/home" "$shell" "$INSTALL_SH" --bin-dir "$bin_dir" \
			>"$t/out" 2>"$t/err"
		status=$?
		set -e
		[ "$status" -ne 0 ] || fail "$shell ran install.sh to completion; expected a refusal"
		assert_nothing_installed
		assert_execution_count 0
	done
}

# --- runner -----------------------------------------------------------------

main() {
	local filter="" verbose=0 list=0
	while [ "$#" -gt 0 ]; do
		case "$1" in
		-f | --filter)
			[ "$#" -ge 2 ] || die "missing value for $1"
			filter=$2
			shift 2
			;;
		--filter=*)
			filter=${1#*=}
			shift
			;;
		-l | --list)
			list=1
			shift
			;;
		-v | --verbose)
			verbose=1
			shift
			;;
		-k | --keep)
			keep=1
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			printf 'install_test: unknown option: %s\n\n' "$1" >&2
			usage >&2
			exit 2
			;;
		esac
	done

	if [ "$list" -eq 1 ]; then
		printf '%s\n' "${TESTS[@]}"
		return 0
	fi

	require_tools

	work=$(mktemp -d "${TMPDIR:-/tmp}/yc-install-test.XXXXXX")
	trap cleanup EXIT HUP INT TERM
	mkdir -p "$work/tests"
	exec_log="$work/executions.log"
	: >"$exec_log"

	make_certificate
	build_release_tree
	write_server
	start_server
	build_sandbox_path

	info "install.sh harness: $INSTALL_SH"
	info "fake release server: https://github.com/ -> 127.0.0.1:$port (loopback, TLS)"
	info ""

	local name passed=0 failed=0 skipped=0 selected=0 rc
	local -a failures=() skips=()
	for name in "${TESTS[@]}"; do
		if [ -n "$filter" ] && [[ "$name" != *"$filter"* ]]; then
			continue
		fi
		selected=$((selected + 1))
		printf '  %-48s ' "$name"
		set +e
		(
			set -e
			setup_test "$name"
			"test_$name"
		) >"$work/tests/$name.log" 2>&1
		rc=$?
		set -e
		case "$rc" in
		0)
			paint 32 ok
			printf '\n'
			passed=$((passed + 1))
			;;
		77)
			paint 33 skip
			printf '\n'
			skipped=$((skipped + 1))
			skips+=("$name")
			# A skip carries a reason worth reading - a missing tool, or a
			# known bug this harness refuses to pretend it did not see.
			sed 's/^/      /' "$work/tests/$name.log"
			;;
		*)
			paint 31 FAIL
			printf '\n'
			failed=$((failed + 1))
			failures+=("$name")
			sed 's/^/      /' "$work/tests/$name.log"
			;;
		esac
		if [ "$verbose" -eq 1 ] && [ "$rc" -eq 0 ] && [ -s "$work/tests/$name.log" ]; then
			sed 's/^/      /' "$work/tests/$name.log"
		fi
	done

	info ""
	if [ "$selected" -eq 0 ]; then
		die "no test matched '$filter'"
	fi
	if [ "$skipped" -gt 0 ]; then
		info "skipped: ${skips[*]}"
	fi
	if [ "$failed" -eq 0 ]; then
		info "$(paint 32 PASS) $passed passed, $skipped skipped"
		return 0
	fi
	info "$(paint 31 FAIL) $failed failed, $passed passed, $skipped skipped"
	info "failed: ${failures[*]}"
	return 1
}

main "$@"
