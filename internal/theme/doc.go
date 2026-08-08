// Package theme contains shared palette and contrast helpers.
//
// Rendering code uses these helpers to derive readable per-author colors and
// chrome gradients against the terminal background without mixing palette rules
// into message normalization or app state. Every helper is pure and returns its
// input unchanged on an unparseable color, so a broken custom theme degrades
// decoration rather than failing the app.
package theme
