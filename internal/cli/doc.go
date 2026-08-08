// Package cli wires command-line input to config, auth, storage, diagnostics,
// logging, and app startup.
//
// The package is responsible for redacting user-facing output, keeping mock and
// diagnostic paths credential-free by default, and constructing live chat
// dependencies without leaking secrets into logs or terminal output. It must not
// contain Bubble Tea view behavior or YouTube wire-format parsing.
package cli
