// Package render converts normalized chat messages into terminal-safe rows.
//
// It owns semantic fragments, width-aware wrapping, and color decisions.
// Avatars, badges, and emoji always render as text (initials, labels, and
// Unicode/shortcode fallbacks) - there is no image rendering path, because the
// YouTube live chat API supplies no badge imagery and no per-message emote
// metadata. The package performs no network work and owns no API client.
package render
