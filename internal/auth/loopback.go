package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultLoopbackAddress binds 127.0.0.1 on an OS-assigned port. Loopback
// redirection is still the recommended mechanism for installed apps; the
// deprecation Google published applies to mobile apps only.
const DefaultLoopbackAddress = "127.0.0.1:0"

// DefaultCallbackTimeout bounds how long yc waits for the browser to come back.
// A login that never completes must not leave a listener and a goroutine alive
// for the life of the process.
const DefaultCallbackTimeout = 5 * time.Minute

// loopbackShutdownTimeout bounds the graceful shutdown of the callback server
// so Close cannot block a CLI exit.
const loopbackShutdownTimeout = 2 * time.Second

// LoopbackConfig configures the local OAuth callback listener.
type LoopbackConfig struct {
	// Address is the bind address. Empty means DefaultLoopbackAddress.
	Address string
	// Path is the callback path, for an OAuth client registered against a
	// redirect URI more specific than "/". Empty means "/". A request for
	// any other path is answered with 404 and cannot consume the one-shot
	// delivery.
	Path string
	// ExpectedState is compared against the callback's state in constant
	// time. A mismatch is answered with the failure page and is never
	// delivered to the waiter as a usable callback.
	ExpectedState Secret
	// Timeout bounds Wait. Zero means DefaultCallbackTimeout.
	Timeout time.Duration
	// SuccessMessage and FailureMessage replace the default page text.
	SuccessMessage string
	FailureMessage string
}

// LoopbackServer is the one-shot local HTTP listener that receives Google's
// OAuth redirect.
//
// It binds 127.0.0.1 on an OS-assigned port, answers exactly one callback,
// renders a static page that never echoes any query value back into the
// browser, and refuses a callback whose state does not match.
type LoopbackServer struct {
	listener    net.Listener
	server      *http.Server
	redirectURI string
	path        string
	timeout     time.Duration
	expected    Secret

	success string
	failure string

	results   chan LoginCallback
	deliver   sync.Once
	closeOnce sync.Once
	closeErr  error
}

// NewLoopbackServer binds the callback listener and starts serving. The caller
// must Close it, whether or not a callback arrives.
func NewLoopbackServer(cfg LoopbackConfig) (*LoopbackServer, error) {
	address := strings.TrimSpace(cfg.Address)
	if address == "" {
		address = DefaultLoopbackAddress
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		// The bind address is a loopback host:port and carries no
		// credential material, but the error still goes through the
		// pattern redactor for uniformity.
		return nil, safeError("bind OAuth callback listener", err, Redactor{})
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultCallbackTimeout
	}
	success := strings.TrimSpace(cfg.SuccessMessage)
	if success == "" {
		success = "yc is signed in. You can close this tab and return to your terminal."
	}
	failure := strings.TrimSpace(cfg.FailureMessage)
	if failure == "" {
		failure = "yc could not complete sign-in. Return to your terminal for details."
	}

	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	server := &LoopbackServer{
		listener:    listener,
		redirectURI: loopbackRedirectURI(listener.Addr(), path),
		path:        path,
		timeout:     timeout,
		expected:    cfg.ExpectedState,
		success:     success,
		failure:     failure,
		results:     make(chan LoginCallback, 1),
	}
	server.server = &http.Server{
		Handler:           http.HandlerFunc(server.handle),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		// http.ErrServerClosed is the normal outcome of Close; every other
		// serve failure surfaces through Wait's timeout rather than a
		// second error channel, because a dead listener cannot deliver a
		// callback either way.
		_ = server.server.Serve(listener)
	}()
	return server, nil
}

// RedirectURI returns the loopback address actually bound, including the
// OS-assigned port. It is what must be sent as redirect_uri in both the
// authorization request and the token exchange.
func (s *LoopbackServer) RedirectURI() string {
	if s == nil {
		return ""
	}
	return s.redirectURI
}

// Wait blocks until the browser hits the callback, the context is canceled, or
// the configured timeout elapses.
//
// The returned callback carries the expected state and an empty verifier; the
// login flow fills the verifier in from the attempt it recorded.
func (s *LoopbackServer) Wait(ctx context.Context) (LoginCallback, error) {
	if s == nil {
		return LoginCallback{}, errors.New("wait for OAuth callback: no callback listener")
	}

	timer := time.NewTimer(s.timeout)
	defer timer.Stop()

	select {
	case callback := <-s.results:
		return callback, nil
	case <-ctx.Done():
		return LoginCallback{}, safeError("wait for OAuth callback", ctx.Err(), Redactor{})
	case <-timer.C:
		return LoginCallback{}, safeErrorf("wait for OAuth callback",
			"no response after "+s.timeout.String()+"; run `yc login` again",
			Redactor{}, ErrLoginTimeout)
	}
}

// Close stops the listener. It is safe to call more than once.
func (s *LoopbackServer) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), loopbackShutdownTimeout)
		defer cancel()
		if err := s.server.Shutdown(ctx); err != nil {
			_ = s.server.Close()
			s.closeErr = safeError("close OAuth callback listener", err, Redactor{})
		}
	})
	return s.closeErr
}

// handle answers the single OAuth redirect.
//
// The response body is static: echoing any query parameter would put the
// authorization code, the state, or a provider error description into a browser
// tab, a browser history entry, and quite possibly a screen share.
func (s *LoopbackServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL == nil || r.URL.EscapedPath() != s.path {
		http.NotFound(w, r)
		return
	}
	query := r.URL.Query()
	if !query.Has("code") && !query.Has("error") {
		// Browsers ask for /favicon.ico and similar; those are not the
		// callback and must not consume the one-shot delivery.
		http.NotFound(w, r)
		return
	}

	callback := LoginCallbackFromRequest(r, s.expected)
	accepted := s.stateMatches(callback.State) && !callback.Denied()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if accepted {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(loopbackPage("Signed in", s.success)))
	} else {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(loopbackPage("Sign-in failed", s.failure)))
	}

	// A state mismatch is not delivered at all: an attacker-supplied
	// callback must not be able to end the wait or reach the token endpoint.
	if !s.stateMatches(callback.State) {
		return
	}
	s.deliver.Do(func() {
		s.results <- callback
	})
}

// stateMatches compares the callback state against the expected value in
// constant time. An unset expectation accepts any state, which is the shape
// tests use when they drive the listener directly.
func (s *LoopbackServer) stateMatches(state Secret) bool {
	expected := strings.TrimSpace(s.expected.Reveal())
	if expected == "" {
		return true
	}
	got := strings.TrimSpace(state.Reveal())
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

// loopbackRedirectURI renders the bound address as the redirect URI Google is
// given, bracketing an IPv6 host as the URL grammar requires.
func loopbackRedirectURI(addr net.Addr, path string) string {
	host := "127.0.0.1"
	port := ""
	if tcp, ok := addr.(*net.TCPAddr); ok {
		port = strconv.Itoa(tcp.Port)
		if tcp.IP != nil && tcp.IP.To4() == nil {
			host = "[::1]"
		}
	} else if h, p, err := net.SplitHostPort(addr.String()); err == nil {
		host, port = h, p
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	if path == "" {
		path = "/"
	}
	redirect := url.URL{Scheme: "http", Host: host, Path: path}
	if port != "" {
		redirect.Host = host + ":" + port
	}
	return redirect.String()
}

// loopbackPage renders the static browser response.
func loopbackPage(title, message string) string {
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>yc</title>
<style>
body { background: #14110f; color: #e8e3dd; font: 16px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace; margin: 0; display: grid; place-items: center; min-height: 100vh; }
main { max-width: 34rem; padding: 2rem; text-align: center; }
h1 { font-size: 1.25rem; letter-spacing: 0.04em; margin: 0 0 0.75rem; }
p { margin: 0; color: #b8b0a6; }
</style>
</head>
<body><main><h1>` + htmlEscape(title) + `</h1><p>` + htmlEscape(message) + `</p></main></body>
</html>
`
}

// htmlEscape escapes the fixed page strings. Nothing user-controlled reaches
// this page, but escaping keeps that true if a caller sets a custom message.
func htmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(value)
}
