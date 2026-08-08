// Package emoji provides small Unicode helpers used by render and app code.
//
// The package keeps emoji cluster detection separate from chat rendering so the
// composer, the emoji picker, and the message renderer share one tokenization
// rule. Detection is grapheme-cluster based; callers must never split emoji by
// byte or rune.
package emoji
