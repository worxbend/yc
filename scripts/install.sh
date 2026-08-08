#!/usr/bin/env bash
#
# yc installer.
#
#   curl --proto '=https' --tlsv1.2 -sSf \
#     https://github.com/worxbend/yc/releases/latest/download/install.sh | bash
#
# What it does, in order: refuse anything that is not Linux amd64/arm64,
# download the release binary and its SHA-256 checksum over HTTPS into a
# private temporary directory, verify the binary against that checksum, and
# only then move it into place with mode 0755.
#
# What it never does: pipe a downloaded byte into a shell, install an artifact
# whose checksum did not match, follow a plain-HTTP redirect, or write outside
# the install directory and its own temporary directory.
#
# This is the most security-sensitive file in the repository. Prefer reviewing
# it before running it:
#
#   curl --proto '=https' --tlsv1.2 -fsSLO \
#     https://github.com/worxbend/yc/releases/latest/download/install.sh
#   less install.sh
#   bash install.sh
#
# Everything below lives in a function, and main is called on the last line, so
# a truncated download cannot execute half an installer.

readonly YC_REPO="worxbend/yc"
readonly YC_BIN_NAME="yc"

# The bash check is POSIX-clean and comes before `set -o pipefail` on purpose.
# pipefail is not POSIX, so dash and ash abort on that line with "Illegal option
# -o pipefail" - a confusing failure instead of the instruction this header
# promises. Nothing above this point relies on bash.
if [ -z "${BASH_VERSION:-}" ]; then
	printf 'yc: error: this installer requires bash; pipe it into bash:\n' >&2
	printf "  curl --proto '=https' --tlsv1.2 -sSf https://github.com/%s/releases/latest/download/install.sh | bash\n" \
		"$YC_REPO" >&2
	exit 1
fi

set -euo pipefail

usage() {
	cat <<'USAGE'
Usage: install.sh [options]

Install the yc binary from a GitHub Release after verifying its SHA-256
checksum.

Options:
  --version TAG    Install a specific release tag (e.g. v0.1.0)
                   instead of the latest release
  --bin-dir DIR    Install directory (default: $HOME/.local/bin)
  --dry-run        Print exactly what would be downloaded and installed,
                   then exit. Touches neither the network nor the disk.
  --uninstall      Remove a previously installed yc from the install
                   directory and exit
  --help, -h       Show this help text

Environment overrides:
  YC_INSTALL_VERSION   Same as --version
  YC_INSTALL_DIR       Same as --bin-dir

yc publishes linux/amd64 and linux/arm64 binaries only. On any other platform
this script refuses to run; build from source instead (see docs/install.md).
USAGE
}

log() {
	printf 'yc: %s\n' "$*" >&2
}

warn() {
	printf 'yc: warning: %s\n' "$*" >&2
}

die() {
	printf 'yc: error: %s\n' "$*" >&2
	exit 1
}

# tmp_dir is the staging directory, empty until main creates one. cleanup is the
# EXIT trap and must tolerate being called before, or without, that happening.
tmp_dir=""

cleanup() {
	[ -z "$tmp_dir" ] || rm -rf -- "$tmp_dir"
}

require_cmd() {
	local cmd=$1 reason=$2
	command -v "$cmd" >/dev/null 2>&1 || die "$cmd is required $reason"
}

# detect_platform sets goarch, or refuses. yc has no darwin, windows, 386, or
# armv7 release artifact, and installing the wrong one is worse than refusing.
detect_platform() {
	local os arch
	os=$(uname -s 2>/dev/null || echo unknown)
	arch=$(uname -m 2>/dev/null || echo unknown)

	if [ "$os" != "Linux" ]; then
		printf 'yc: error: prebuilt binaries are published for Linux only (this system reports %s).\n' "$os" >&2
		printf '  Build from source instead:\n' >&2
		printf '    go install github.com/%s/cmd/%s@latest\n' "$YC_REPO" "$YC_BIN_NAME" >&2
		printf '  or run it in Docker; see https://github.com/%s/blob/main/docs/install.md\n' "$YC_REPO" >&2
		exit 1
	fi

	case "$arch" in
	x86_64 | amd64)
		goarch=amd64
		;;
	aarch64 | arm64)
		goarch=arm64
		;;
	*)
		printf 'yc: error: unsupported CPU architecture %s.\n' "$arch" >&2
		printf '  yc publishes linux/amd64 and linux/arm64 binaries only.\n' >&2
		printf '  Build from source instead:\n' >&2
		printf '    go install github.com/%s/cmd/%s@latest\n' "$YC_REPO" "$YC_BIN_NAME" >&2
		exit 1
		;;
	esac
}

# fetch downloads url into out. HTTPS only, on the initial request and on every
# redirect, so a hijacked redirect cannot downgrade the transport.
fetch() {
	local url=$1 out=$2
	if command -v curl >/dev/null 2>&1; then
		curl --proto '=https' --proto-redir '=https' --tlsv1.2 \
			--location --max-redirs 5 --max-time 300 --retry 2 \
			-sSf "$url" -o "$out"
	elif command -v wget >/dev/null 2>&1; then
		wget --https-only --secure-protocol=TLSv1_2 --max-redirect 5 \
			--timeout 300 --tries 3 -q "$url" -O "$out"
	else
		die "curl or wget is required to download a release"
	fi
}

# sha256_of prints the bare lowercase hex digest of a file.
sha256_of() {
	local file=$1 digest
	if command -v sha256sum >/dev/null 2>&1; then
		digest=$(sha256sum -- "$file" | awk '{print $1}')
	elif command -v shasum >/dev/null 2>&1; then
		digest=$(shasum -a 256 -- "$file" | awk '{print $1}')
	elif command -v openssl >/dev/null 2>&1; then
		digest=$(openssl dgst -sha256 -- "$file" | awk '{print $NF}')
	else
		die "sha256sum, shasum, or openssl is required to verify the download"
	fi
	printf '%s' "$digest" | tr 'A-F' 'a-f'
}

# verify_checksum compares the downloaded binary against the published digest.
# The digest is parsed and validated by hand instead of handing the file to
# `sha256sum -c`, whose exit status also depends on the file name recorded
# inside it. A mismatch is fatal and the binary is destroyed.
verify_checksum() {
	local bin_file=$1 sum_file=$2 expected actual

	[ -s "$sum_file" ] || die "the published checksum file is empty; refusing to install"

	expected=$(awk 'NR == 1 { print $1 }' "$sum_file" | tr -d '\r' | tr 'A-F' 'a-f')

	if [ "${#expected}" -ne 64 ] || [ -n "${expected//[0-9a-f]/}" ]; then
		rm -f -- "$bin_file"
		die "the published checksum file is not a SHA-256 digest; refusing to install"
	fi

	actual=$(sha256_of "$bin_file")

	if [ "$expected" != "$actual" ]; then
		rm -f -- "$bin_file"
		printf 'yc: error: CHECKSUM MISMATCH — the download does not match the published digest.\n' >&2
		printf '  expected: %s\n' "$expected" >&2
		printf '  actual:   %s\n' "$actual" >&2
		printf '  The downloaded file has been deleted and nothing was installed.\n' >&2
		printf '  Retry, and if it happens again report it: https://github.com/%s/security\n' "$YC_REPO" >&2
		exit 1
	fi

	printf '%s' "$actual"
}

warn_if_not_on_path() {
	local dir=$1
	case ":${PATH:-}:" in
	*":$dir:"*)
		return 0
		;;
	esac
	warn "$dir is not on your PATH; '$YC_BIN_NAME' will not be found by name yet"
	# The $PATH below is literal text for the user to paste into a profile, so
	# it must reach them unexpanded.
	# shellcheck disable=SC2016
	{
		printf '  Add it to your shell profile, then restart the shell:\n'
		printf '    echo '\''export PATH="%s:$PATH"'\'' >> ~/.bashrc\n' "$dir"
		printf '    echo '\''export PATH="%s:$PATH"'\'' >> ~/.zshrc\n' "$dir"
		printf '  Or run it by full path: %s/%s --help\n' "$dir" "$YC_BIN_NAME"
	} >&2
}

do_uninstall() {
	local bin_dir=$1 dest="$1/$YC_BIN_NAME" other

	if [ -e "$dest" ] || [ -L "$dest" ]; then
		[ ! -d "$dest" ] || die "$dest is a directory; refusing to remove it"
		rm -f -- "$dest"
		log "removed $dest"
	else
		log "nothing to remove: $dest does not exist"
	fi

	# A copy installed somewhere else stays put; say so instead of pretending
	# the machine is clean.
	other=$(command -v "$YC_BIN_NAME" 2>/dev/null || true)
	if [ -n "$other" ]; then
		warn "another $YC_BIN_NAME is still on your PATH at $other (left untouched)"
	fi

	# Literal text for the user to paste; nothing here should expand now.
	# shellcheck disable=SC2016
	{
		printf '  Config and credentials are left in place. Remove them yourself if you want:\n'
		printf '    rm -rf -- "${XDG_CONFIG_HOME:-$HOME/.config}/yc" "${XDG_CACHE_HOME:-$HOME/.cache}/yc"\n'
	} >&2
}

main() {

	local version="${YC_INSTALL_VERSION:-latest}"
	local bin_dir="${YC_INSTALL_DIR:-$HOME/.local/bin}"
	local dry_run=0 uninstall=0
	local goarch=""

	while [ "$#" -gt 0 ]; do
		case "$1" in
		--version)
			[ "$#" -ge 2 ] || die "missing value for --version"
			version=$2
			shift 2
			;;
		--version=*)
			version=${1#--version=}
			shift
			;;
		--bin-dir | --dir)
			[ "$#" -ge 2 ] || die "missing value for $1"
			bin_dir=$2
			shift 2
			;;
		--bin-dir=* | --dir=*)
			bin_dir=${1#*=}
			shift
			;;
		--dry-run)
			dry_run=1
			shift
			;;
		--uninstall)
			uninstall=1
			shift
			;;
		--help | -h)
			usage
			exit 0
			;;
		*)
			printf 'yc: error: unknown option: %s\n\n' "$1" >&2
			usage >&2
			exit 2
			;;
		esac
	done

	[ -n "$bin_dir" ] || die "the install directory must not be empty"
	[ -n "$version" ] || die "the version must not be empty"

	# The tag becomes part of a URL path. Keep it to the characters a release
	# tag actually uses so it cannot smuggle a path segment or a query.
	if [ "$version" != "latest" ] && [ -n "${version//[0-9A-Za-z._+-]/}" ]; then
		die "invalid version '$version'; expected a release tag such as v0.1.0"
	fi

	if [ "$uninstall" -eq 1 ]; then
		do_uninstall "$bin_dir"
		return 0
	fi

	detect_platform

	local asset="${YC_BIN_NAME}_linux_${goarch}"
	local base_url
	if [ "$version" = "latest" ]; then
		base_url="https://github.com/${YC_REPO}/releases/latest/download"
	else
		base_url="https://github.com/${YC_REPO}/releases/download/${version}"
	fi

	if [ "$dry_run" -eq 1 ]; then
		printf 'yc install dry run — nothing was downloaded, nothing was written.\n'
		printf '  platform:  linux/%s\n' "$goarch"
		printf '  release:   %s\n' "$version"
		printf '  binary:    %s/%s\n' "$base_url" "$asset"
		printf '  checksum:  %s/%s.sha256\n' "$base_url" "$asset"
		printf '  install:   %s/%s (mode 0755)\n' "$bin_dir" "$YC_BIN_NAME"
		case ":${PATH:-}:" in
		*":$bin_dir:"*) printf '  PATH:      %s is already on PATH\n' "$bin_dir" ;;
		*) printf '  PATH:      %s is NOT on PATH; you would need to add it\n' "$bin_dir" ;;
		esac
		return 0
	fi

	require_cmd mktemp "to stage the download"
	require_cmd install "to place the binary"

	umask 077
	# The trap names a function rather than interpolating the path into a
	# string. A TMPDIR containing a quote and a command substitution would
	# otherwise be executed by the shell when the trap fired, and TMPDIR is
	# attacker-reachable wherever yc is installed by something other than the
	# user's own interactive shell. tmp_dir is file-scope, not local, because a
	# late-expanding trap on a function-local name dies under `set -u` and
	# leaves the download directory behind.
	trap cleanup EXIT HUP INT TERM
	tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/yc-install.XXXXXX") ||
		die "could not create a temporary directory"

	log "downloading $asset ($version)"
	fetch "$base_url/$asset" "$tmp_dir/$asset" ||
		die "could not download $base_url/$asset (is release '$version' published for linux/$goarch?)"
	fetch "$base_url/$asset.sha256" "$tmp_dir/$asset.sha256" ||
		die "could not download the checksum for $asset; refusing to install an unverified binary"

	[ -s "$tmp_dir/$asset" ] || die "the downloaded binary is empty; refusing to install"

	local digest
	digest=$(verify_checksum "$tmp_dir/$asset" "$tmp_dir/$asset.sha256")
	log "checksum verified (sha256:$digest)"

	mkdir -p -- "$bin_dir" || die "could not create $bin_dir"
	[ -w "$bin_dir" ] || die "$bin_dir is not writable; pass --bin-dir DIR to install elsewhere"

	# Install to a staging name in the destination directory and rename, so an
	# interrupted run cannot leave a half-written yc on PATH, and so replacing
	# a running yc succeeds instead of failing with ETXTBSY.
	local staged="$bin_dir/.$YC_BIN_NAME.install.$$"
	install -m 0755 -- "$tmp_dir/$asset" "$staged" ||
		die "could not write to $bin_dir"
	mv -f -- "$staged" "$bin_dir/$YC_BIN_NAME" ||
		{ rm -f -- "$staged"; die "could not install to $bin_dir/$YC_BIN_NAME"; }
	chmod 0755 -- "$bin_dir/$YC_BIN_NAME"

	log "installed $bin_dir/$YC_BIN_NAME"

	local reported
	reported=$("$bin_dir/$YC_BIN_NAME" --version 2>/dev/null || true)
	[ -n "$reported" ] || die "the installed binary did not run; remove $bin_dir/$YC_BIN_NAME and report this"
	log "installed $reported"

	warn_if_not_on_path "$bin_dir"

	printf '\nNext: run mock chat — no Google account, no API key, no quota.\n' >&2
	printf '  %s chat --mock\n' "$YC_BIN_NAME" >&2
	printf 'Then: %s setup, and read https://github.com/%s/blob/main/docs/quickstart.md\n' \
		"$YC_BIN_NAME" "$YC_REPO" >&2
}

main "$@"
