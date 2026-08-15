package youtube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/auth"
)

// The API key rides in the query string of every key-mode request, so the
// request URL is a credential. These tests walk the whole error value - its
// message, every fmt verb, and the full unwrap chain - because safeError keeps
// its cause reachable for errors.Is, and a raw *url.Error retained there would
// carry the URL even though Error() never prints it.

const (
	redactionTestKey   = "AIzaSyTESTNOTAREALKEY0123456789012345"
	redactionTestToken = "ya29.test-not-a-real-token-abcdefghijklmnop"
)

// errorChainText renders every error reachable from err, which is the surface a
// caller can reach with errors.Unwrap, errors.Join's Error, or a %+v of a cause.
func errorChainText(err error) string {
	var b strings.Builder
	var walk func(error, int)
	walk = func(e error, depth int) {
		if e == nil || depth > 8 {
			return
		}
		b.WriteString(e.Error())
		b.WriteString("\n")
		switch typed := e.(type) {
		case interface{ Unwrap() error }:
			walk(typed.Unwrap(), depth+1)
		case interface{ Unwrap() []error }:
			for _, cause := range typed.Unwrap() {
				walk(cause, depth+1)
			}
		}
	}
	walk(err, 0)
	return b.String()
}

// assertNoCredential fails when any rendering of err quotes a credential.
func assertNoCredential(t *testing.T, err error, secrets ...string) {
	t.Helper()
	renderings := map[string]string{
		"Error()": err.Error(),
		"%v":      fmt.Sprintf("%v", err),
		"%+v":     fmt.Sprintf("%+v", err),
		"%#v":     fmt.Sprintf("%#v", err),
		"chain":   errorChainText(err),
	}
	for label, rendering := range renderings {
		for _, secret := range secrets {
			if strings.Contains(rendering, secret) {
				t.Errorf("%s leaked a credential: %s", label, rendering)
			}
		}
	}
}

func TestTransportFailureKeepsAPIKeyOutOfTheErrorChain(t *testing.T) {
	client, err := NewClient(ClientConfig{
		Credentials: StaticCredentials{Key: auth.NewSecret(redactionTestKey)},
		// Port 1 refuses immediately, so net/http produces the *url.Error
		// carrying the full request URL that this test exists to catch.
		Endpoint: "https://127.0.0.1:1/youtube/v3",
		Timeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var out any
	callErr := client.doJSON(context.Background(), http.MethodGet, EndpointMessagesList,
		"liveChat/messages", map[string]string{"liveChatId": "abc"}, nil, &out)
	if callErr == nil {
		t.Fatal("expected the request to fail")
	}
	assertNoCredential(t, callErr, redactionTestKey)

	if !errors.Is(callErr, ErrTransient) {
		t.Error("the redacted cause must still classify as transient")
	}
	// The diagnostic has to survive redaction, or the fix trades a leak for an
	// unreadable error.
	if !strings.Contains(errorChainText(callErr), "connection refused") {
		t.Errorf("the transport reason was lost: %s", errorChainText(callErr))
	}
}

func TestTransportCancellationStaysClassifiable(t *testing.T) {
	client, err := NewClient(ClientConfig{
		Credentials: StaticCredentials{Key: auth.NewSecret(redactionTestKey)},
		Endpoint:    "https://127.0.0.1:1/youtube/v3",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out any
	callErr := client.doJSON(ctx, http.MethodGet, EndpointMessagesList, "liveChat/messages", nil, nil, &out)
	if !errors.Is(callErr, context.Canceled) {
		t.Errorf("a canceled call must remain context.Canceled, got %v", callErr)
	}
}

func TestErrorBodyEchoingCredentialsIsRedacted(t *testing.T) {
	// A hostile or misconfigured endpoint that echoes the request back is the
	// one way a credential re-enters yc from outside.
	envelope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error":{"code":403,"message":"key %s rejected calling %s","status":"PERMISSION_DENIED"}}`,
			r.URL.Query().Get("key"), r.URL.String())
	}))
	defer envelope.Close()

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "upstream failed for %s", r.URL.String())
	}))
	defer plain.Close()

	bearer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, "denied: %s", r.Header.Get("Authorization"))
	}))
	defer bearer.Close()

	for _, tc := range []struct {
		name        string
		endpoint    string
		credentials StaticCredentials
		secret      string
	}{
		{"google error envelope", envelope.URL, StaticCredentials{Key: auth.NewSecret(redactionTestKey)}, redactionTestKey},
		{"unstructured body", plain.URL, StaticCredentials{Key: auth.NewSecret(redactionTestKey)}, redactionTestKey},
		{"echoed bearer header", bearer.URL, StaticCredentials{Token: auth.NewSecret(redactionTestToken)}, redactionTestToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewClient(ClientConfig{Credentials: tc.credentials, Endpoint: tc.endpoint})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			var out any
			callErr := client.doJSON(context.Background(), http.MethodGet, EndpointMessagesList,
				"liveChat/messages", nil, nil, &out)
			if callErr == nil {
				t.Fatal("expected the request to fail")
			}
			assertNoCredential(t, callErr, tc.secret)
		})
	}
}
