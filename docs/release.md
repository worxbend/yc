# Releasing yc

How a `yc` release is built, verified, and published — what a machine does, and
what a human has to decide. For consuming a release, read
[install.md](install.md). For the container image, read [docker.md](docker.md).

## What A Release Is

A release is a `v*` Git tag, a GitHub Release, and seven assets:

| Asset | What it is |
| --- | --- |
| `yc_linux_amd64` | Static binary, `linux/amd64` |
| `yc_linux_amd64.sha256` | Its SHA-256 digest, in `sha256sum` format |
| `yc_linux_arm64` | Static binary, `linux/arm64` |
| `yc_linux_arm64.sha256` | Its SHA-256 digest |
| `checksums.txt` | Digests for both binaries **and** `install.sh` in one file |
| `install.sh` | The curl-pipe installer, byte-identical to `scripts/install.sh` |
| `install.sh.sha256` | Its digest, so the installer can be verified before it is run |

That is the whole matrix. There is no darwin build, no windows build, no 32-bit
build, no snap, no `.deb`/`.rpm`, no Homebrew tap, no registry image, and no
signature. Each of those is a support promise, and `yc` only makes promises it
tests. See [what is deliberately not done](#what-is-deliberately-not-done).

## Versioning

`yc` follows semantic versioning, and pre-1.0 means the CLI surface, config
keys, and on-disk formats may still change in a minor release. Breaking changes
are called out in [../CHANGELOG.md](../CHANGELOG.md).

The tag is written `v0.1.0`. The binary reports the version without the `v`:

```console
$ yc --version
yc 0.1.0
```

The version is compiled in with a linker flag, not read from a file at runtime:

```sh
go build -trimpath \
  -ldflags "-s -w -X github.com/worxbend/yc/internal/cli.Version=0.1.0" \
  ./cmd/yc
```

The symbol is `github.com/worxbend/yc/internal/cli.Version`, not `main.version`
— `cmd/yc/main.go` is a four-line shim and the CLI lives in `internal/cli`. A
build without that flag reports `yc dev`, which is exactly what a `go install`
or a plain `go build` should report.

Both `scripts/release-dry-run.sh` and the release workflow assert the built
binary prints `yc <version>` before anything is published. A release that
shipped a binary reporting `dev` would be a broken release, so it is a hard
failure rather than a cosmetic one.

## Build Flags

Every published binary is built identically:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=<arch> go build \
  -trimpath \
  -ldflags "-s -w -X github.com/worxbend/yc/internal/cli.Version=<version>" \
  -o yc_linux_<arch> ./cmd/yc
```

| Flag | Why |
| --- | --- |
| `CGO_ENABLED=0` | A static binary with no libc dependency, so one file runs on any glibc or musl distribution. The script fails the build if `file` reports a dynamically linked result. |
| `-trimpath` | Removes the maintainer's absolute build paths from the binary. A privacy and reproducibility measure. |
| `-s -w` | Drops the symbol table and DWARF data. Roughly a third smaller; stack traces still carry function names. |
| `-X ...Version=` | Stamps the release identity. |

`yc` has no build tags, no code generation step, and no vendored dependencies,
so the build is `go build` and nothing more.

## Cut A Release

Roughly ten minutes, most of it waiting for CI.

### 1. Decide the version and land the changelog

Human judgement, not automation. Update `CHANGELOG.md` with the version and
date, confirm `README.md` and the docs do not claim anything the release does
not do, and merge it to `main`. The release notes are generated from commits;
the changelog is the curated story, and only a person can write it.

### 2. Run the gate locally

```sh
go build ./... && go vet ./... && gofmt -l . && go test ./... && go test -race ./...
go tool govulncheck ./... && go tool staticcheck ./...
```

### 3. Run the release dry run

```sh
scripts/release-dry-run.sh --version v0.1.0
```

This builds exactly what the tag will publish. If it fails, the tag will too —
and an unpushed tag is far cheaper to fix than a published one.

### 4. Rehearse in CI (optional but cheap)

Actions → **Release dry run** → *Run workflow*. Same script, clean runner, no
release created.

### 5. Tag and push

```sh
git tag -a v0.1.0 -m "yc v0.1.0"
git push origin v0.1.0
```

The tag push is the trigger. Nothing before it publishes anything.

### 6. Watch the workflow

Actions → **Release**. It runs the full quality gate, builds both targets,
verifies the checksums, creates the GitHub Release with generated notes, and
uploads the seven assets.

### 7. Verify the published release like a user

Do not trust the workflow's word for it:

```sh
curl --proto '=https' --tlsv1.2 -fsSLO \
  https://github.com/worxbend/yc/releases/download/v0.1.0/checksums.txt
curl --proto '=https' --tlsv1.2 -fsSLO \
  https://github.com/worxbend/yc/releases/download/v0.1.0/yc_linux_amd64
sha256sum -c --ignore-missing checksums.txt

curl --proto '=https' --tlsv1.2 -sSf \
  https://github.com/worxbend/yc/releases/latest/download/install.sh | bash
yc --version
```

Then record what you actually ran in
[manual-validation.md](manual-validation.md).

## The Local Dry Run

`scripts/release-dry-run.sh` is the release pipeline minus the publishing. It is
credential-free and safe to run at any time.

```sh
scripts/release-dry-run.sh                          # everything, into dist/release
scripts/release-dry-run.sh --skip-docker            # no Docker on this machine
scripts/release-dry-run.sh --version v0.1.0         # stamp a specific version
scripts/release-dry-run.sh --out /tmp/yc-release    # somewhere other than dist/
```

| Option | Default | Meaning |
| --- | --- | --- |
| `--out DIR` | `dist/release` | Output directory. `dist/` is git-ignored. |
| `--version VER` | `git describe --tags --always --dirty`, else `0.0.0-dev` | Version stamped into the binary. A leading `v` is stripped. |
| `--image NAME` | `yc:local` | Docker image tag. |
| `--targets LIST` | `linux/amd64 linux/arm64` | Space-separated `GOOS/GOARCH`. Anything outside the two supported targets is refused, so the script cannot quietly become a wider support claim. |
| `--skip-docker` | off | Skip the image build and container smokes. |
| `--keep` | off | Keep the temporary runtime directory for inspection. |

`YC_RELEASE_DIR`, `YC_RELEASE_VERSION`, `YC_RELEASE_IMAGE`, `YC_RELEASE_TARGETS`,
and `YC_RELEASE_GO_VERSION` are environment equivalents. They are read *before*
the credential purge described below, which is why they survive it.

What it does, in order:

1. Unsets every `YC_*` and `GOOGLE_*` variable in its own environment.
2. Cross-builds each target with the flags above.
3. Checks each binary is non-empty and, when `file` is available, statically
   linked.
4. Writes `<name>.sha256`, then re-computes the digest and additionally runs
   `sha256sum -c` against it. A checksum is only useful if the checking path
   works, so it is exercised rather than assumed.
5. Writes `checksums.txt` covering every binary and verifies it.
6. **Negative check:** copies one artifact, appends a byte, and asserts that
   verification *fails*. A verification step that passes on a tampered file is
   worse than no verification at all.
7. Smokes the native binary with an isolated `HOME` and XDG directories:
   `--version` (asserted equal to `yc <version>`), `--help`, `doctor`,
   `config show`, `config path`, `quota`, and `chat --mock`.
8. Asserts `chat --video <id>` exits `2` without credentials — proof that the
   purge worked and that live chat refuses before opening a socket.
9. Builds the Docker image with `GO_VERSION` taken from the `go.mod` toolchain
   directive, and smokes the container with `--help`, `--version`, `doctor`,
   `config show`, and `chat --mock`.

Any failure exits non-zero. The temporary runtime directory is removed on exit,
including on interrupt.

### Isolation

The smokes run with `HOME` and all four XDG base directories pointed into a
throwaway `mktemp -d`, and with every `YC_*` and `GOOGLE_*` variable unset. So
the dry run cannot read your credential file, cannot read your config, cannot
write to your quota ledger, and cannot spend a unit of your daily allowance.
The build itself keeps the real `HOME` so it reuses the Go module cache instead
of re-downloading the world.

The container smokes pass no `-e` flags at all: `docker run` starts from an
empty environment, so nothing from the calling shell can reach the container.

## Automation

### `.github/workflows/release.yml`

Triggered by a `v*` tag push, and by manual dispatch for a build-only rehearsal.

- `permissions: contents: read` at the workflow level. Only the `release` job
  escalates to `contents: write`, and only because it creates the release and
  uploads assets. The gate job never gets write access.
- `concurrency: release-<ref>` with `cancel-in-progress: false` — cancelling
  mid-upload would leave a release with a partial asset set.
- Job `gate` re-runs the whole quality gate on the tagged tree: `go mod tidy`
  cleanliness, `gofmt`, `vet`, tests, race tests, `govulncheck`, `staticcheck`,
  and `shellcheck` plus `bash -n` on both release scripts. A tag can point at
  any commit, so the gate is re-run rather than assumed from a green `main`.
- Job `release` resolves the version from the tag, runs
  `scripts/release-dry-run.sh --skip-docker`, stages `install.sh` beside the
  binaries with its own digest, asserts all seven assets exist and are
  non-empty, uploads them as a workflow artifact, then publishes.
- Publishing uses `gh release create --verify-tag --generate-notes`, uploads
  with `--clobber` so a re-run replaces assets instead of failing, and then
  prepends install and verification instructions to the generated notes. The
  prepend step is idempotent: a re-run detects its own preamble and leaves the
  notes alone.

The container image is not a release asset, so the workflow passes
`--skip-docker`. CI builds and smokes the image separately.

### `.github/workflows/release-dry-run.yml`

Manual dispatch only, `permissions: contents: read`, publishes nothing. Inputs:
an optional `version` and a `skip_docker` toggle. It shellchecks both scripts,
runs the dry run, rehearses the installer's network-free paths (including
asserting that `--dry-run` writes nothing and that a malformed tag is rejected),
and uploads the artifacts with a 7-day retention.

Run it before tagging, or after any change to `scripts/`, the `Dockerfile`, or
`go.mod`.

## Automated Versus Manual

| Step | Who |
| --- | --- |
| Deciding the version number | **Human** |
| Writing `CHANGELOG.md` | **Human** |
| Confirming the docs do not overclaim | **Human** |
| Creating and pushing the tag | **Human** |
| Re-running the full quality gate on the tag | Automated |
| Building both Linux targets | Automated |
| Writing and verifying checksums | Automated |
| Asserting the version ldflag took effect | Automated |
| Smoking the binaries without credentials | Automated |
| Creating the GitHub Release | Automated |
| Generating release notes from commits | Automated |
| Uploading all seven assets | Automated |
| Verifying the published release as a user | **Human** |
| Recording that verification in `manual-validation.md` | **Human** |

## Secret Handling

Release artifacts must never carry a credential, and nothing in this pipeline
gives them the chance to:

- Both workflows pin every `YC_*` and `GOOGLE_*` variable empty, and
  `release-dry-run.sh` unsets them again in its own process.
- No release job uses a repository secret. The only token in play is the
  ephemeral `GITHUB_TOKEN`, scoped to `contents: write` in one job.
- Smokes run against isolated `HOME` and XDG directories, so no credential file
  or quota ledger is reachable even if one existed on the runner.
- The Docker build context is filtered by `.dockerignore` (`.env`,
  `config.toml`, `credentials.json`, the quota ledger, logs, agent state), and
  the build stage copies only `go.mod`, `go.sum`, `cmd/`, and `internal/`.
- Never add a `--build-arg`, an `ENV`, or a copied file that could carry a
  token, refresh token, client secret, API key, OAuth code, state, PKCE
  verifier, or authorization URL. That rule is not negotiable; see
  [code-style.md](code-style.md).

## Known Gap: The Container Reports `dev`

The `Dockerfile` builds with `-ldflags="-s -w"` and takes no version argument,
so `docker run yc:local --version` prints `yc dev` even when built from a
tagged tree. The published binaries are unaffected. Closing this means adding a
`VERSION` build argument to the `Dockerfile` and passing it from the release
script; until that happens, this document says so rather than implying the
container is version-stamped.

## What Is Deliberately Not Done

| Not done | Why |
| --- | --- |
| macOS and Windows binaries | Never built, never run, never tested. A published artifact is a support promise. |
| GPG or Sigstore signatures | Not set up. The checksums prove integrity against the release page, not authorship. |
| SBOM or SLSA provenance | Not generated. `go version -m yc` lists the module dependencies of any build. |
| Registry publishing | No image is pushed anywhere. Build locally; see [docker.md](docker.md). |
| Homebrew, AUR, `.deb`, `.rpm`, snap, Nix, Scoop | Each is a distribution channel with its own review, update, and support burden. None is maintained. |
| Reproducible-build attestation | `-trimpath` and `CGO_ENABLED=0` make builds highly reproducible in practice, but no bit-for-bit rebuild is verified in CI, so no claim is made. |

Adding any of these means adding the automation *and* the evidence. Do not
document one without the other.

## Evidence

Per the honesty contract in [index.md](index.md):

| Claim | Status |
| --- | --- |
| `scripts/release-dry-run.sh` builds, checksums, verifies, and smokes both targets | **Ready** — executed on a Linux amd64 host. Both binaries built, digests written and re-verified, a tampered artifact correctly rejected, and all native smokes passed. |
| The version ldflag reaches `yc --version` | **Ready** — a build stamped `0.1.0` reported `yc 0.1.0`; an unstamped build reports `yc dev`. |
| The Docker image builds and runs | **Ready** — `docker build` succeeded and `--help`, `--version`, `doctor`, `config show`, and `chat --mock` all passed in the container. |
| `scripts/install.sh` non-network paths | **Ready** — help, dry run, uninstall, non-Linux refusal, unsupported-architecture refusal, non-bash refusal, and malformed-tag rejection all executed. |
| `scripts/install.sh` download and verification | **Partial** — exercised end to end against a local server serving real release artifacts, including checksum mismatch, a non-digest checksum file, a missing checksum asset, an empty binary, an unwritable directory, and upgrade-in-place. Never run against a real GitHub Release. |
| `.github/workflows/release.yml` | **Planned** — never executed. The repository has no tags. Every shell block in it was extracted from the YAML and run locally against real artifacts, including the release-notes generation and its re-run guard, but GitHub has never run the workflow. |
| `.github/workflows/release-dry-run.yml` | **Planned** — never executed on GitHub; its script and installer rehearsal steps were run locally. |
| Published release assets | **Planned** — no release exists. |

Update this table when a release is actually cut, and record the manual
verification in [manual-validation.md](manual-validation.md).
