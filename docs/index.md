# Documentation

This directory is the source of truth for `yc`. The [README](../README.md) gives
the fast path; these documents explain how to run, configure, budget, debug,
contribute, and validate the project without relying on tribal knowledge.

The rendered site lives at <https://worxbend.github.io/yc/>, built from these same
sources. Everything here is readable as plain Markdown on GitHub too.

## Every Document

| Document | Audience | Purpose |
| --- | --- | --- |
| [install.md](install.md) | New user | Every way to install `yc`: the checksum-verifying installer, a manual verified download, `go install`, Docker, and from source. |
| [quickstart.md](quickstart.md) | New user | Run mock chat, configure a live chat, learn the key bindings. |
| [register-google-app.md](register-google-app.md) | First live run | Create a Google Cloud project, enable YouTube Data API v3, and get either an API key or a Desktop OAuth client. |
| [quota.md](quota.md) | Anyone reading live chat | The 10,000-unit day, what each call costs, how `yc` paces itself, and how to stretch it. |
| [auth.md](auth.md) | Operator | Credential sources, the login flow, refresh, revocation, and what is never printed. |
| [config.md](config.md) | Operator | Every config key and `YC_*` variable, precedence, and redaction. |
| [keybindings.md](keybindings.md) | Any user | Every key in every context, generated from the keymap so it cannot drift. |
| [themes.md](themes.md) | Any user | All 58 presets, all nine palette roles, and the custom-hex format. |
| [events.md](events.md) | Any user | Every `snippet.type` yc understands, how each is normalized, and how each renders. |
| [moderation.md](moderation.md) | Moderator or owner | Delete, timeout, and ban: the capability ladder, the confirmations, and what the API cannot do. |
| [faq.md](faq.md) | Anyone | The questions that come before the manual. |
| [docker.md](docker.md) | Docker user | Build and run the container without baking secrets into the image. |
| [troubleshooting.md](troubleshooting.md) | Anyone diagnosing a failure | Symptoms, causes, and fixes, starting from `yc doctor`. |
| [security.md](security.md) | Security reviewer | Threat model, trust boundaries, the redaction guarantees, and the explicit non-goals. |
| [architecture.md](architecture.md) | Contributor | Package map, boundaries, and the runtime data flow. |
| [development.md](development.md) | Contributor | Implementation state, toolchain, quality gates, and testing strategy. |
| [code-style.md](code-style.md) | Contributor | Package ownership, rendering rules, redaction rules, comments, and tests. |
| [release.md](release.md) | Maintainer | Cutting a release: the dry run, the workflows, the artifacts, and the checks that gate a tag. |
| [manual-validation.md](manual-validation.md) | Anyone checking a claim | What has actually been run, and what has not. |
| [adr/README.md](adr/README.md) | Contributor | How to read the architecture decision records. |
| [../CONTRIBUTING.md](../CONTRIBUTING.md) | Contributor | Support boundary, safe workflow, verification commands, PR checklist, secret rules. |
| [../SECURITY.md](../SECURITY.md) | Security reviewer | Report credential-handling issues without exposing secrets. |
| [../CHANGELOG.md](../CHANGELOG.md) | Anyone upgrading | What changed, including behavior changes. |

## Start Here

| Audience | Read this first |
| --- | --- |
| New user | [install.md](install.md), then [quickstart.md](quickstart.md) |
| Learning the keyboard | [keybindings.md](keybindings.md) |
| Picking a theme | [themes.md](themes.md) |
| Moderating a chat | [moderation.md](moderation.md) |
| In a hurry | [faq.md](faq.md) |
| First live run | [register-google-app.md](register-google-app.md), then [auth.md](auth.md) |
| Budgeting a stream | [quota.md](quota.md) |
| Operator | [config.md](config.md) and [auth.md](auth.md) |
| Docker user | [docker.md](docker.md) |
| Contributor | [../CONTRIBUTING.md](../CONTRIBUTING.md), [development.md](development.md), [code-style.md](code-style.md) |
| Maintainer cutting a release | [release.md](release.md) |
| Security reviewer | [security.md](security.md), then [../SECURITY.md](../SECURITY.md) |
| Anyone diagnosing a failure | [troubleshooting.md](troubleshooting.md) |
| Anyone checking a claim | [manual-validation.md](manual-validation.md) |

## Current Support Boundary

`yc` targets Unix-like terminals and Docker. Published release binaries are
`linux/amd64` and `linux/arm64` only; there is no macOS build, no Windows build,
no snap, and no package-manager manifest. Build from source on any other
platform.

Saved credentials are supported only on Go `unix` builds through the restrictive
credential-file store; non-Unix builds keep them disabled and must use
environment variables or a private flat config file.

## The Honesty Contract

Every document here labels behavior as one of:

| Label | Means |
| --- | --- |
| **Ready** | Exercised by automated tests and credential-free smokes in this environment. |
| **Partial** | Shipped, but for a narrower behavior than the feature name suggests. |
| **Credentialed** | Implemented and unit-tested against fakes or `httptest`, and **never run against Google**. |
| **Planned** | Not built. Sometimes the transport exists and no caller does. |
| **Manual** | Requires a human at a terminal; recorded in [manual-validation.md](manual-validation.md) or not claimed. |
| **Out of scope** | Deliberately absent, with a reason. |

No document may claim a credentialed YouTube path was verified.
[manual-validation.md](manual-validation.md) currently records **zero**
credentialed runs, and that is the only place such evidence may be recorded.

## Architecture Records

The ADRs capture decisions that should not be re-litigated casually:

- [adr/README.md](adr/README.md) — how to read these, and where they no longer match the code
- [adr/0001-poll-live-chat-messages-list.md](adr/0001-poll-live-chat-messages-list.md)
- [adr/0002-hand-roll-the-rest-client.md](adr/0002-hand-roll-the-rest-client.md)
- [adr/0003-normalize-live-chat-events-before-rendering.md](adr/0003-normalize-live-chat-events-before-rendering.md)
- [adr/0004-pace-polling-against-an-estimated-quota-budget.md](adr/0004-pace-polling-against-an-estimated-quota-budget.md)
- [adr/0005-render-everything-as-text.md](adr/0005-render-everything-as-text.md)

For the package-level map and runtime data flow, read
[architecture.md](architecture.md).

## Assets

Screenshots under [assets/screenshots/](assets/screenshots/) are generated from
real `View()` output by `internal/app/screenshot_test.go`, so they cannot drift
from what the app prints:

```sh
YC_WRITE_SCREENSHOTS=1 go test ./internal/app -run TestWriteDocsScreenshots
```

## Quality Rules

The default quality gate is credential-free. It uses isolated config and cache
directories and empty `YC_*`/`GOOGLE_*` variables so checks never depend on a
developer's local account. Contributor commands live in
[../CONTRIBUTING.md](../CONTRIBUTING.md); deeper architecture and testing notes
live in [development.md](development.md).
