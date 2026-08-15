// Package chatlog writes normalized chat events to an opt-in on-disk log.
//
// The log is one JSON Lines (JSONL) file per chat session: every line is one
// JSON object describing one normalized event, so the file can be processed
// with standard tools (jq, a spreadsheet import, `yc export superchats`)
// without any custom parser. The package consumes the normalized
// internal/youtube models only - raw YouTube API JSON never reaches disk.
//
// Files are written with owner-only permissions (0600 in a 0700 directory),
// matching internal/storage: a chat log records what the user was watching and
// who said what, which is private even though it is not credential material.
// Credentials themselves can never appear here by construction - the writer
// only serializes chat-message fields - but every free-text field is still
// passed through an injectable redactor as a second line of defence.
//
// The writer rotates by size rather than by time, because a chat session's
// length is unknowable up front: when the current file exceeds the configured
// budget a fresh file is started, and only the newest N files are kept.
package chatlog
