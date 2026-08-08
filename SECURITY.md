# Security Policy

`yc` handles Google OAuth access tokens, refresh tokens, client secrets, YouTube
API keys, authorization codes, OAuth state, PKCE verifiers, authorization URLs,
private config files, credential files, and debug logs. Treat all of those as
sensitive.

Two are worth calling out specifically:

- **The API key travels in a query string.** A YouTube Data API request URL is
  therefore itself a credential, and `net/http` errors echo the URL. Anything
  that surfaces a transport error must reduce it to its class rather than pass it
  through.
- **The PKCE verifier and the OAuth state are single-use secrets during login.**
  Logging either one defeats the flow's protection against interception and
  replay.

## Supported Scope

Security fixes target the current `main` branch and the documented Unix-like
terminal and Docker support boundary. Saved credentials are supported only on Go
`unix` builds through the restrictive credential-file store. Windows and other
non-Unix saved-credential backends are not supported and must keep returning a
redacted unsupported-platform error.

## Reporting Issues

Do not open a public issue, pull request, screenshot, terminal recording, or
discussion that contains a real credential, a callback URL with a code, a debug
log with private context, or a private config or credential file.

If you discover a secret leak or a credential-handling bug:

1. **Rotate immediately.** Revoke the OAuth grant
   (<https://myaccount.google.com/permissions>, or `yc logout`) and delete or
   regenerate the API key in the Google Cloud console.
2. Prepare a minimal reproduction using fake credential-shaped values such as
   `test-not-a-real-token` or `AIzaSyTEST-not-a-real-key-000000000000000`.
3. Share only redacted logs and the exact command path that produced the unsafe
   output.

If private reporting is not yet configured for the repository, open a public
issue describing the affected command and its impact **without** including
secrets, and state that private details are available to the maintainer.

## Quota As A Security Concern

The daily quota is a finite, non-replenishing, per-project resource, so a bug
that spends it is a denial-of-service against the user's own project. Treat the
following as security-relevant, not merely as correctness bugs:

- A retry loop against `quotaExceeded` or `rateLimitExceeded`.
- Any path that polls faster than `pollingIntervalMillis`.
- A resolution path that reaches `search.list` without explicit consent.
- A background or daemonized instance polling without the user's knowledge.
- A ledger that under-reports spend and hides any of the above.

## Debug Log Safety

Debug logs are opt-in and redacted, but they can still include video IDs, live
chat IDs, channel names, display names, message IDs, hostnames, counts, terminal
details, and timing. Review a log before sharing it. Never attach one captured
while real credentials or a private broadcast were active without checking it
first.

Log files are created with mode `0600` in a `0700` directory. Existing files that
are directories, symlinks, or group/other-accessible are rejected; Unix builds
open the final path with `O_NOFOLLOW` and validate the opened descriptor.
`yc doctor` reports which guarantee the running build makes.

## Credential File Safety

On Unix, `~/.config/yc/credentials.json` must be exactly `0600` inside a `0700`
directory. Symlinks at either path, mismatched modes, and special mode bits are
**rejected rather than repaired**, and writes go through a temp file and a
same-directory rename. A report that any of those checks can be bypassed is a
security issue.

## Maintainer Checklist

- Reproduce with fake credential values.
- Add a regression test that scans output, logs, and errors for the fake markers.
- Keep the fix narrowly scoped to the leaking path.
- Check the three surfaces that most often leak: an error message from
  `net/http`, a `%v` of a struct holding a credential, and a debug record built
  from a raw struct instead of curated fields.
- Update [docs/troubleshooting.md](docs/troubleshooting.md),
  [docs/auth.md](docs/auth.md), or [docs/config.md](docs/config.md) if user
  guidance changes.
