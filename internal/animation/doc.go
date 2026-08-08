// Package animation turns rendered chat rows into bounded reveal state and
// drives every chrome effect from one shared frame clock.
//
// The package works on grapheme-safe render units instead of raw bytes or
// runes, which keeps emoji, combining characters, and ANSI styling intact
// during typed-in message animation. Text effects return styled cells rather
// than escape sequences, so the package owns no terminal I/O and every effect
// stays a pure function of elapsed time.
package animation
