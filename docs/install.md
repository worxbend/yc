# Installing yc

Every way to get `yc` onto a machine, with the exact commands. For how those
artifacts are produced and published, read [release.md](release.md). For the
first run afterwards, read [quickstart.md](quickstart.md).

## Support Boundary

| Platform | Prebuilt binary | Build from source |
| --- | --- | --- |
| Linux amd64 (`x86_64`) | Yes | Yes |
| Linux arm64 (`aarch64`) | Yes | Yes |
| macOS (Intel or Apple silicon) | **No** | Yes, but saved credentials work only on Go `unix` builds and no macOS build has ever been run |
| Windows | **No** | Unsupported: saved credentials are disabled and nothing is tested |
| Docker (amd64/arm64 host) | Image built locally | Yes |

Linux amd64 and arm64 are the only published artifacts. That is a deliberate
narrow claim, not an oversight: they are the only targets built, checksummed,
and smoked by the release pipeline. There is no Homebrew formula, no AUR
package, no `.deb`, no `.rpm`, no snap, no Nix derivation, no Scoop manifest,
and no container image on any registry.

`yc` needs a terminal. Any install path gives you the same binary; the
difference is only how it arrives.

## 1. Curl Pipe (Linux amd64/arm64)

The fast path. It downloads the binary for your architecture, verifies its
SHA-256 checksum against the digest published alongside it, and installs it to
`~/.local/bin/yc` with mode `0755`.

```sh
curl --proto '=https' --tlsv1.2 -sSf \
  https://github.com/worxbend/yc/releases/latest/download/install.sh | bash
```

`bash`, not `sh`. On Debian and Ubuntu `/bin/sh` is `dash`, and the script uses
`local`, `[[ ]]`, and `set -o pipefail`. Piped into a non-bash shell it prints
this line and installs nothing, rather than failing halfway through.

Pin a release instead of taking the latest:

```sh
curl --proto '=https' --tlsv1.2 -sSf \
  https://github.com/worxbend/yc/releases/download/v0.1.0/install.sh | bash -s -- --version v0.1.0
```

Install somewhere else:

```sh
curl --proto '=https' --tlsv1.2 -sSf \
  https://github.com/worxbend/yc/releases/latest/download/install.sh | bash -s -- --bin-dir /usr/local/bin
```

Everything after `-s --` is passed to the script:

| Flag | Effect |
| --- | --- |
| `--version TAG` | Install a specific release tag (for example `v0.1.0`) instead of the latest. |
| `--bin-dir DIR` | Install directory. Default `$HOME/.local/bin`. |
| `--dry-run` | Print exactly what would be downloaded and where it would land, then exit. Touches neither the network nor the disk. |
| `--uninstall` | Remove `yc` from the install directory and exit. |
| `--help` | Show the same table. |

`YC_INSTALL_VERSION` and `YC_INSTALL_DIR` are environment equivalents of
`--version` and `--bin-dir`.

The installer requires `bash`. It refuses with a clear message if piped into a
shell that is not bash, because it relies on `pipefail` and on `local`.

### Read It Before You Run It

Piping a script from the internet into a shell means trusting the server, the
transport, and the file. You do not have to. Download it, check it, then run it:

```sh
curl --proto '=https' --tlsv1.2 -fsSLO \
  https://github.com/worxbend/yc/releases/latest/download/install.sh
curl --proto '=https' --tlsv1.2 -fsSLO \
  https://github.com/worxbend/yc/releases/latest/download/install.sh.sha256
sha256sum -c install.sh.sha256
less install.sh
bash install.sh
```

The same file is in the repository at
[`scripts/install.sh`](../scripts/install.sh), so you can diff the release asset
against the source you reviewed.

### What The Installer Will And Will Not Do

It will:

- Refuse to run on anything but Linux amd64/arm64, and tell you what to do
  instead.
- Download over HTTPS only, on the first request **and on every redirect**.
- Download the checksum as a separate asset and compare it against the binary
  it just fetched. A mismatch deletes the download, installs nothing, and exits
  non-zero.
- Install atomically: it writes a staging file in the destination directory and
  renames it into place, so an interrupted run never leaves a half-written `yc`
  on your `PATH`, and upgrading a currently-running `yc` works.
- Run `yc --version` afterwards and print what it installed.
- Warn, with the exact line to add, when the install directory is not on `PATH`.

It will not:

- Pipe any downloaded byte into a shell.
- Install an artifact whose checksum did not match.
- Use `sudo`, or write anywhere except the install directory and its own
  private temporary directory.
- Edit your `~/.bashrc`, `~/.zshrc`, or any other file. It tells you what to
  add and leaves the decision to you.
- Touch your config, credentials, or quota ledger — including on `--uninstall`.

## 2. Manual Download And Verification

The path to use when you want to see every step, or when you are provisioning a
machine from a script of your own.

Pick the asset for your architecture — `uname -m` prints `x86_64` for amd64 and
`aarch64` for arm64:

```sh
version=v0.1.0
arch=amd64   # or arm64

curl --proto '=https' --tlsv1.2 -fsSLO \
  "https://github.com/worxbend/yc/releases/download/${version}/yc_linux_${arch}"
curl --proto '=https' --tlsv1.2 -fsSLO \
  "https://github.com/worxbend/yc/releases/download/${version}/yc_linux_${arch}.sha256"
```

Verify **before** you make it executable:

```sh
sha256sum -c "yc_linux_${arch}.sha256"
```

That must print `yc_linux_amd64: OK`. If it prints `FAILED`, stop: delete the
file and report it. Do not run it.

Then install it:

```sh
install -m 0755 "yc_linux_${arch}" "$HOME/.local/bin/yc"
yc --version
```

Every release also carries a `checksums.txt` covering all published assets at
once, which is the more convenient file when you downloaded several:

```sh
curl --proto '=https' --tlsv1.2 -fsSLO \
  "https://github.com/worxbend/yc/releases/download/${version}/checksums.txt"
sha256sum -c --ignore-missing checksums.txt
```

`--ignore-missing` checks only the files you actually downloaded instead of
failing on the ones you did not.

There is no GPG signature, no Sigstore/cosign signature, no SBOM, and no
provenance attestation. The checksum proves the bytes match what the release
page publishes; it does not prove who published them. See
[release.md](release.md#what-is-deliberately-not-done).

## 3. go install

Works on every platform Go supports, including macOS, and needs no release
artifact at all. Requires Go 1.26 or newer.

```sh
go install github.com/worxbend/yc/cmd/yc@latest
```

Pin a version:

```sh
go install github.com/worxbend/yc/cmd/yc@v0.1.0
```

The binary lands in `$(go env GOBIN)`, or `$(go env GOPATH)/bin` when `GOBIN`
is unset — usually `~/go/bin`. Add that to your `PATH`:

```sh
export PATH="$(go env GOPATH)/bin:$PATH"
```

One difference worth knowing: a `go install` build reports `yc dev` from
`yc --version`, because the release version is stamped in with a linker flag
that only the release pipeline passes. The code is exactly the tagged code; only
the reported build identity differs.

## 4. Docker

Useful for a repeatable build or a host where you would rather not install a
binary. Interactive chat still needs a real TTY, so keep `-it`.

```sh
docker build -t yc:local .
docker run --rm -it yc:local chat --mock
```

No image is published to any registry, so building locally is the only Docker
path. Full details, including how to pass credentials without baking them into
the image, are in [docker.md](docker.md).

## 5. Build From Source

The path for contributors, for architectures with no published binary, and for
anyone who wants to read what they run.

```sh
git clone https://github.com/worxbend/yc.git
cd yc
go build -o bin/yc ./cmd/yc
./bin/yc chat --mock
```

To reproduce a release binary byte-for-byte, use the same flags the pipeline
uses:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -trimpath \
  -ldflags "-s -w -X github.com/worxbend/yc/internal/cli.Version=0.1.0" \
  -o yc_linux_amd64 ./cmd/yc
```

Or just run the release script, which does that for both targets and checks its
own work:

```sh
scripts/release-dry-run.sh --version v0.1.0 --skip-docker
```

## After Installing

```sh
yc --version      # confirm which build you have
yc chat --mock    # the whole UI, no Google account, no API key, no quota
yc doctor         # what is configured, and what is missing
```

Live chat needs your own Google Cloud project. Follow
[register-google-app.md](register-google-app.md), then `yc setup`. Read
[quota.md](quota.md) before your first live run: the daily allowance is 10,000
units and it is easier to burn than you would expect.

## PATH

`~/.local/bin` is on `PATH` by default on most modern Linux distributions.
When it is not, add it to the profile your shell actually reads and restart the
shell:

```sh
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
```

Check what you are actually running when something looks stale:

```sh
command -v yc
yc --version
```

## Upgrading

Re-run whichever path you installed with. The curl-pipe installer replaces the
existing binary in place, including while a `yc` process is running:

```sh
curl --proto '=https' --tlsv1.2 -sSf \
  https://github.com/worxbend/yc/releases/latest/download/install.sh | bash
```

For `go install`, re-run it with `@latest`. For Docker, rebuild the image.
Read [../CHANGELOG.md](../CHANGELOG.md) for behavior changes before upgrading.

## Uninstalling

```sh
curl --proto '=https' --tlsv1.2 -sSf \
  https://github.com/worxbend/yc/releases/latest/download/install.sh | bash -s -- --uninstall
```

Or simply remove the binary:

```sh
rm -f ~/.local/bin/yc
```

Neither removes your config, credentials, or quota ledger. Remove those
yourself when you want them gone:

```sh
rm -rf "${XDG_CONFIG_HOME:-$HOME/.config}/yc" "${XDG_CACHE_HOME:-$HOME/.cache}/yc"
```

If you had logged in, revoke the token with Google first — `yc logout` does
that remotely, so run it *before* deleting the credential file. See
[auth.md](auth.md).

## Troubleshooting An Install

| Symptom | Cause | Fix |
| --- | --- | --- |
| `prebuilt binaries are published for Linux only` | macOS, BSD, or WSL reporting a non-Linux kernel | Use `go install` or build from source. |
| `unsupported CPU architecture` | 32-bit x86, armv7, riscv64, or similar | Build from source; `GOARCH` supports far more than the release matrix does. |
| `this installer requires bash` | Piped into `sh`, `dash`, `zsh`, or `fish` | Pipe into `bash` instead. |
| `could not download ... (is release 'vX.Y.Z' published for linux/...?)` | Wrong tag, or an asset missing from that release | Check the [releases page](https://github.com/worxbend/yc/releases) for the exact tag and asset names. |
| `CHECKSUM MISMATCH` | Corrupted download, an interfering proxy, or tampering | Nothing was installed. Retry. If it repeats, report it via [../SECURITY.md](../SECURITY.md). |
| `the published checksum file is not a SHA-256 digest` | An HTML error page arrived instead of the checksum | Usually the release or asset does not exist. Check the tag. |
| `<dir> is not writable` | Installing into a root-owned directory | Use `--bin-dir "$HOME/.local/bin"`, or run the install command under `sudo` deliberately. |
| `yc: command not found` after a successful install | Install directory is not on `PATH` | See [PATH](#path) above. |
| Wrong version reported | An older `yc` earlier on `PATH` | `command -v yc` shows which one wins. |

For runtime problems after a successful install, read
[troubleshooting.md](troubleshooting.md) and run `yc doctor`.

## Status Of Each Path

Following the honesty contract in [index.md](index.md):

| Path | Status | What that means |
| --- | --- | --- |
| Build from source | **Ready** | Built and run continuously in this repository. |
| `scripts/release-dry-run.sh` | **Ready** | Executed on a Linux amd64 host: both targets built, checksums written and verified, a tampered artifact rejected, the native binary smoked, and the Docker image built and smoked. |
| Docker | **Ready** | `docker build` and the credential-free container smokes were run on a Docker-enabled host. |
| `scripts/install.sh`, offline paths | **Ready** | `--help`, `--dry-run`, `--uninstall`, the non-Linux refusal, the unsupported-architecture refusal, the non-bash refusal, and malformed-tag rejection were all executed. |
| `scripts/install.sh`, download path | **Partial** | The full download, checksum verification, atomic install, upgrade-in-place, and every failure mode were exercised end to end against a local server holding real release artifacts. It has **never** run against a real GitHub Release, because none exists yet. |
| `go install` | **Planned** | Correct by construction for a public module, but not exercised: the module is not published yet. |
| Published release assets | **Planned** | No tag has been pushed, so no release exists and no URL on this page resolves yet. |
