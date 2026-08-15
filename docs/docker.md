# Docker Guide

`yc` is a terminal app. Docker packaging is useful for repeatable builds, smoke
tests, and running the same binary on a host that has Docker installed.
Interactive chat still needs a real TTY. For the wider documentation map, read
[index.md](index.md); for Docker-specific failures, read
[troubleshooting.md](troubleshooting.md).

> **Status: built and smoked, never run against Google.** The image builds on a
> Docker-enabled host and `--help`, `--version`, `doctor`, `config show`, and
> `chat --mock` all pass inside the container; `scripts/release-dry-run.sh` does
> exactly that on every run and fails the release if any of them regresses.
> What is still unverified is the same thing that is unverified everywhere else:
> no container has ever held a real Google credential or reached the live API.
> See [manual-validation.md](manual-validation.md).

## Build The Image

```sh
docker build -t yc:local .
```

Multi-stage:

- `golang:1.26.6-bookworm` compiles `cmd/yc` with `CGO_ENABLED=0 -trimpath
  -ldflags="-s -w"`, plus `-X …/internal/cli.Version=${VERSION}` from the
  `VERSION` build argument. Without `--build-arg VERSION=…` the image reports
  `yc dev`, which is correct for a local build and wrong for a released one:

  ```sh
  docker build --build-arg VERSION=0.1.0 -t yc:0.1.0 .
  ```
- `debian:bookworm-slim` runs it as a non-root `yc` user, UID/GID `10001`.
- CA certificates are copied into the runtime image, because every YouTube Data
  API and Google OAuth call is HTTPS.

Only `go.mod`, `go.sum`, `cmd/`, and `internal/` are copied into the build stage.
`.dockerignore` keeps `.env`, `config.toml`, `credentials.json`, the quota
ledger, logs, and the AI-agent directories out of the context entirely — the
image cannot contain a credential it was never sent.

The image is not a release artifact. `yc` publishes `linux/amd64` and
`linux/arm64` binaries with SHA-256 checksums — see [install.md](install.md) and
[release.md](release.md) — and pushes to no registry, so `docker build` from a
checkout is how you get an image.

## Run Mock Chat

Ready today, needs no credentials, no network, and no quota.

```sh
docker run --rm -it yc:local chat --mock
```

With Compose:

```sh
docker compose run --rm mock
```

## Run Diagnostics

```sh
docker run --rm yc:local doctor
docker compose run --rm doctor
docker compose run --rm quota
```

`doctor` reports the credential store, the effective capability, the quota budget
arithmetic, config load, theme and display modes, cache writability, configured
chats, the cost table, today's ledger, API reachability, and identity. It redacts
tokens, secrets, and API keys — but the output still contains local paths and
terminal details, so review it before pasting it anywhere.

## Run Live Chat

**Status: credentialed and unverified** — like every live path in this project.

Set credentials in your shell or in an ignored local `.env`, and pass them at
runtime:

```sh
export YC_YOUTUBE_API_KEY="AIza…"

docker run --rm -it \
  -e YC_YOUTUBE_API_KEY \
  yc:local chat --video dQw4w9WgXcQ
```

With Compose:

```sh
cp .env.example .env
$EDITOR .env
YC_CHAT_TARGET=dQw4w9WgXcQ docker compose run --rm live
```

`compose.yaml` reads `YC_*` variables from your shell or local `.env`. Keep real
secrets out of tracked files, and never bake them into the image.

### `yc login` in a container

It will not work in a bare container, and that is expected: the flow binds a
loopback listener and opens a browser, and the container has neither a browser
nor a loopback the host browser can reach without extra plumbing.

Do the login on the host and share the result instead:

```sh
yc login                                   # on the host
docker run --rm -it \
  -v "$HOME/.config/yc:/config/yc:ro" \
  yc:local chat --video dQw4w9WgXcQ
```

A read-only mount is the safer choice: the container reads the credential but
cannot rotate it, so a refresh happens in memory for that process only. If you
want refreshed tokens persisted, mount it writable and make sure the directory is
owned by UID/GID `10001` with mode `0700` — the credential store **rejects**
anything else rather than repairing it.

## Use A Mounted Config File

The runtime image sets:

```text
XDG_CONFIG_HOME=/config
XDG_CACHE_HOME=/cache
```

so inside the container the paths are:

```text
/config/yc/config.toml        flat config
/config/yc/credentials.json   private credential file (0700 dir, 0600 file)
/cache/yc/quota/              persisted estimated quota ledger
```

Example:

```sh
mkdir -p .local/yc-config/yc .local/yc-cache
sudo chown -R 10001:10001 .local/yc-config .local/yc-cache
$EDITOR .local/yc-config/yc/config.toml

docker run --rm -it \
  -v "$PWD/.local/yc-config:/config" \
  -v "$PWD/.local/yc-cache:/cache" \
  yc:local chat --video dQw4w9WgXcQ
```

**Mount the cache volume for any live run.** The quota ledger lives there, and a
container that starts with an empty cache starts with an empty meter — which
hands you a false sense of budget for the rest of the day. `yc doctor` reports a
non-writable cache as a warning for exactly this reason.

Do not commit `.local`, config files with credentials, shell history containing
tokens, or exported logs.

## Write Config Without A Prompt

```sh
mkdir -p "$PWD/.local/yc-config"
sudo chown 10001:10001 "$PWD/.local/yc-config"
docker run --rm \
  -v "$PWD/.local/yc-config:/config" \
  yc:local setup --non-interactive \
    --client-id "…apps.googleusercontent.com" \
    --chat dQw4w9WgXcQ \
    --theme nord \
    --login-dry-run
```

`setup` never writes a client secret, access token, refresh token, authorization
code, OAuth state, or API key. Use environment variables, a private config file,
or a mounted credential file for those.

## Deploy Notes

For `yc`, deployment is packaging rather than running a background service.

Reasonable:

- Copy the `yc` binary to a workstation or jumpbox where a human will use it.
- Build the image and push it to a private registry.
- Run the container with `-it` on the machine where the terminal UI should appear.

Avoid:

- Baking a token, refresh token, client secret, or API key into the image.
- Running live chat without a TTY. It renders one frame and exits — useful as a
  smoke, useless as a session.
- Treating `yc` as a daemon. It is not that shape, and a background instance
  quietly spending your project's daily quota is the worst possible failure mode.

## Image Commands

```sh
docker run --rm yc:local --help
docker run --rm yc:local --version
docker run --rm -e YC_YOUTUBE_API_KEY yc:local config show
docker run --rm -it -e YC_ANIMATION_MODE=off yc:local chat --mock
```
