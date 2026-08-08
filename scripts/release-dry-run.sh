#!/usr/bin/env bash
#
# yc release dry run.
#
# Builds the exact artifacts a tagged release publishes, verifies their
# checksums, and smokes them. The whole run is credential-free: every YC_* and
# GOOGLE_* variable is unset, HOME and the XDG directories are redirected into a
# throwaway directory, and no step reads or writes a real credential file, a
# real quota ledger, or the network beyond the Go module cache and Docker.
#
# Exits non-zero on the first failure of any step.

set -euo pipefail

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)

usage() {
	cat <<'USAGE'
Usage: scripts/release-dry-run.sh [options]

Cross-build trimmed static yc binaries for the release targets, write and
verify SHA-256 checksums, build the Docker image, and smoke every artifact
without credentials.

Options:
  --out DIR        Output directory (default: dist/release)
  --version VER    Version stamped into the binary (default: git describe)
  --image NAME     Docker image tag (default: yc:local)
  --targets LIST   Space-separated GOOS/GOARCH list
                   (default: "linux/amd64 linux/arm64"; nothing else is allowed)
  --skip-docker    Skip the Docker image build and container smokes
  --keep           Keep the temporary runtime directory for inspection
  --help, -h       Show this help text

Environment overrides (read before the credential purge):
  YC_RELEASE_DIR         Same as --out
  YC_RELEASE_VERSION     Same as --version
  YC_RELEASE_IMAGE       Same as --image
  YC_RELEASE_TARGETS     Same as --targets
  YC_RELEASE_GO_VERSION  Go version passed to the Docker build
                         (default: the go.mod toolchain directive)

yc publishes linux/amd64 and linux/arm64 only. There is no darwin, windows,
snap, package-manager, signing, notarization, or registry step here.
USAGE
}

# --- configuration ----------------------------------------------------------
#
# Read every YC_RELEASE_* override first. The credential purge below unsets
# everything matching YC_*, which would otherwise take these with it.

out_dir=${YC_RELEASE_DIR:-"$repo_root/dist/release"}
version=${YC_RELEASE_VERSION:-}
image=${YC_RELEASE_IMAGE:-yc:local}
targets=${YC_RELEASE_TARGETS:-"linux/amd64 linux/arm64"}
go_version_override=${YC_RELEASE_GO_VERSION:-}
skip_docker=0
keep_runtime=0

while [ "$#" -gt 0 ]; do
	case "$1" in
	--help | -h)
		usage
		exit 0
		;;
	--out)
		[ "$#" -ge 2 ] || { echo "missing value for --out" >&2; exit 2; }
		out_dir=$2
		shift 2
		;;
	--version)
		[ "$#" -ge 2 ] || { echo "missing value for --version" >&2; exit 2; }
		version=$2
		shift 2
		;;
	--image)
		[ "$#" -ge 2 ] || { echo "missing value for --image" >&2; exit 2; }
		image=$2
		shift 2
		;;
	--targets)
		[ "$#" -ge 2 ] || { echo "missing value for --targets" >&2; exit 2; }
		targets=$2
		shift 2
		;;
	--skip-docker)
		skip_docker=1
		shift
		;;
	--keep)
		keep_runtime=1
		shift
		;;
	*)
		echo "unknown option: $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

# --- helpers ----------------------------------------------------------------

step() {
	printf '\n==> %s\n' "$*"
}

info() {
	printf '    %s\n' "$*"
}

die() {
	printf 'release dry-run failed: %s\n' "$*" >&2
	exit 1
}

on_error() {
	printf '\nrelease dry-run FAILED (line %s)\n' "${1:-?}" >&2
}
trap 'on_error "$LINENO"' ERR

sha256_of() {
	# Print the bare lowercase hex digest of a file.
	local file=$1
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum -- "$file" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 -- "$file" | awk '{print $1}'
	else
		die "sha256sum or shasum is required to checksum release artifacts"
	fi
}

# --- credential purge -------------------------------------------------------
#
# A release smoke that reads a developer's Google credentials proves nothing
# about a fresh machine, and a live call would spend a real daily quota.

purge_credentials() {
	local name
	for name in $(compgen -e || true); do
		case "$name" in
		YC_* | GOOGLE_*)
			unset "$name" || true
			;;
		esac
	done
}
purge_credentials

cd "$repo_root"

command -v go >/dev/null 2>&1 || die "go is required"
[ -f go.mod ] || die "go.mod not found in $repo_root"

runtime_dir=$(mktemp -d "${TMPDIR:-/tmp}/yc-release-runtime.XXXXXX")
cleanup() {
	if [ "$keep_runtime" -eq 1 ]; then
		printf '\nkept runtime directory: %s\n' "$runtime_dir"
		return 0
	fi
	rm -rf -- "$runtime_dir"
}
trap cleanup EXIT HUP INT TERM

smoke_home="$runtime_dir/home"
mkdir -p \
	"$smoke_home" \
	"$runtime_dir/config" \
	"$runtime_dir/cache" \
	"$runtime_dir/data" \
	"$runtime_dir/state"

# XDG redirection is safe to export globally: the Go toolchain keys its caches
# off GOCACHE/GOMODCACHE and HOME, not off XDG_CACHE_HOME. HOME is redirected
# only for the smokes, so builds keep using the real module cache.
export XDG_CONFIG_HOME="$runtime_dir/config"
export XDG_CACHE_HOME="$runtime_dir/cache"
export XDG_DATA_HOME="$runtime_dir/data"
export XDG_STATE_HOME="$runtime_dir/state"
export TERM=${TERM:-xterm-256color}
export GOTOOLCHAIN=${GOTOOLCHAIN:-auto}

smoke() {
	# Run a release artifact with an isolated HOME so no real config file,
	# credential file, or quota ledger is in reach.
	env HOME="$smoke_home" \
		XDG_CONFIG_HOME="$XDG_CONFIG_HOME" \
		XDG_CACHE_HOME="$XDG_CACHE_HOME" \
		XDG_DATA_HOME="$XDG_DATA_HOME" \
		XDG_STATE_HOME="$XDG_STATE_HOME" \
		TERM="$TERM" \
		"$@"
}

# --- version ----------------------------------------------------------------

if [ -z "$version" ]; then
	version=$(git -C "$repo_root" describe --tags --always --dirty 2>/dev/null || true)
	[ -n "$version" ] || version="0.0.0-dev"
fi
# A tag is written v0.1.0; the binary reports 0.1.0.
version=${version#v}

case "$version" in
*[[:space:]]* | "")
	die "invalid version string: '$version'"
	;;
esac

# The build identity lives in internal/cli, not package main.
version_symbol="github.com/worxbend/yc/internal/cli.Version"
ldflags="-s -w -X ${version_symbol}=${version}"

toolchain=$(awk '$1 == "toolchain" { print $2; exit }' go.mod)
[ -n "$toolchain" ] || die "go.mod must declare a toolchain directive for release builds"
go_version=${go_version_override:-"${toolchain#go}"}

step "release dry run"
info "repo root:   $repo_root"
info "version:     $version"
info "output:      $out_dir"
info "targets:     $targets"
info "go toolchain: $toolchain"
info "docker:      $([ "$skip_docker" -eq 1 ] && echo skipped || echo "$image")"

# --- build ------------------------------------------------------------------

mkdir -p -- "$out_dir"

native_goos=$(go env GOOS)
native_goarch=$(go env GOARCH)
native_bin=
built=()

for target in $targets; do
	case "$target" in
	linux/amd64 | linux/arm64) ;;
	*)
		die "unsupported release target '$target'; yc publishes linux/amd64 and linux/arm64 only"
		;;
	esac

	goos=${target%/*}
	goarch=${target#*/}
	name="yc_${goos}_${goarch}"
	bin="$out_dir/$name"

	step "building $target -> $name"
	rm -f -- "$bin" "$bin.sha256"
	env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
		go build -trimpath -ldflags "$ldflags" -o "$bin" ./cmd/yc

	[ -s "$bin" ] || die "$name was not produced"

	# CGO_ENABLED=0 must yield a static binary; a dynamic one would fail on a
	# machine without the matching libc.
	if command -v file >/dev/null 2>&1; then
		description=$(file -b -- "$bin")
		info "file: $description"
		case "$description" in
		*"dynamically linked"*)
			die "$name is dynamically linked; expected a static CGO_ENABLED=0 build"
			;;
		esac
	fi

	digest=$(sha256_of "$bin")
	printf '%s  %s\n' "$digest" "$name" >"$bin.sha256"

	# Verify by recomputing rather than trusting the file just written.
	recomputed=$(sha256_of "$bin")
	[ "$digest" = "$recomputed" ] || die "checksum for $name is not reproducible"
	(cd "$out_dir" && sha256sum -c --quiet "$name.sha256") ||
		die "sha256sum -c rejected $name.sha256"
	info "sha256: $digest"

	built+=("$name")
	if [ "$goos" = "$native_goos" ] && [ "$goarch" = "$native_goarch" ]; then
		native_bin=$bin
	fi
done

[ "${#built[@]}" -gt 0 ] || die "no targets were built"

# --- aggregate checksum file ------------------------------------------------

step "writing checksums.txt"
(
	cd "$out_dir"
	rm -f checksums.txt
	sha256sum -- "${built[@]}" >checksums.txt
	sha256sum -c --quiet checksums.txt
)
info "$out_dir/checksums.txt"
sed 's/^/    /' "$out_dir/checksums.txt"

# A corrupt artifact must be caught. Prove the verification step actually
# fails rather than passing vacuously.
step "negative check: a tampered artifact must fail verification"
tamper_dir="$runtime_dir/tamper"
mkdir -p "$tamper_dir"
cp -- "$out_dir/${built[0]}" "$tamper_dir/${built[0]}"
cp -- "$out_dir/${built[0]}.sha256" "$tamper_dir/${built[0]}.sha256"
printf 'tampered\n' >>"$tamper_dir/${built[0]}"
if (cd "$tamper_dir" && sha256sum -c --quiet "${built[0]}.sha256" >/dev/null 2>&1); then
	die "checksum verification passed on a tampered artifact"
fi
info "tampered artifact rejected as expected"

# --- native binary smokes ---------------------------------------------------

if [ -n "$native_bin" ]; then
	step "smoking native binary $(basename -- "$native_bin")"

	# The version ldflag is part of the release contract: a binary that reports
	# "dev" in a release is a broken release.
	reported=$(smoke "$native_bin" --version)
	info "--version -> $reported"
	[ "$reported" = "yc $version" ] ||
		die "expected 'yc $version' from --version, got '$reported'"

	smoke "$native_bin" --help >/dev/null
	info "--help ok"
	smoke "$native_bin" doctor >/dev/null
	info "doctor ok"
	smoke "$native_bin" config show >/dev/null
	info "config show ok"
	smoke "$native_bin" config path >/dev/null
	info "config path ok"
	smoke "$native_bin" quota >/dev/null
	info "quota ok"
	smoke "$native_bin" chat --mock >/dev/null
	info "chat --mock ok"

	# Live chat without credentials must refuse with the usage exit code
	# instead of opening a socket.
	# `|| live_status=$?` keeps this expected failure out of the ERR trap,
	# which fires on any non-zero command even with errexit disabled.
	live_status=0
	smoke "$native_bin" chat --video dQw4w9WgXcQ >/dev/null 2>&1 || live_status=$?
	[ "$live_status" -eq 2 ] ||
		die "credential-free live chat should exit 2, got $live_status"
	info "credential-free live chat refused with exit 2"
else
	info "native target $native_goos/$native_goarch is not in the target list; skipping native smokes"
fi

# --- docker -----------------------------------------------------------------

if [ "$skip_docker" -eq 0 ]; then
	command -v docker >/dev/null 2>&1 ||
		die "docker is required unless --skip-docker is passed"

	step "building Docker image $image (GO_VERSION=$go_version, VERSION=$version)"
	docker build --build-arg "GO_VERSION=$go_version" \
		--build-arg "VERSION=$version" -t "$image" "$repo_root"

	step "smoking Docker image $image"
	# docker run starts from an empty environment: nothing from this shell is
	# inherited, so no credential can reach the container.
	docker run --rm "$image" --help >/dev/null
	info "--help ok"
	# The image must report the version it was stamped with, not "dev". An
	# unstamped image makes a bug report from a container unattributable, and
	# the failure is silent unless it is asserted.
	docker_version=$(docker run --rm "$image" --version)
	[ "$docker_version" = "yc $version" ] ||
		die "docker image reports '$docker_version', want 'yc $version'"
	info "--version ok ($docker_version)"
	docker run --rm "$image" doctor >/dev/null
	info "doctor ok"
	docker run --rm "$image" config show >/dev/null
	info "config show ok"
	docker run --rm "$image" chat --mock >/dev/null
	info "chat --mock ok"
else
	step "skipping Docker build and container smokes"
fi

# --- summary ----------------------------------------------------------------

step "artifacts in $out_dir"
for name in "${built[@]}"; do
	printf '    %-20s %10s bytes\n' "$name" "$(wc -c <"$out_dir/$name")"
	printf '    %-20s %10s bytes\n' "$name.sha256" "$(wc -c <"$out_dir/$name.sha256")"
done
printf '    %-20s %10s bytes\n' "checksums.txt" "$(wc -c <"$out_dir/checksums.txt")"

printf '\nrelease dry run OK (yc %s)\n' "$version"
