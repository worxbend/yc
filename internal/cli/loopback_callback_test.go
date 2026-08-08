package cli

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/auth"
)

// The loopback delivery is one-shot. Anything that consumes it other than
// Google's redirect ends the login, and the state check downstream then rejects
// the real callback as a mismatch - so a browser prefetch, or any other process
// on the machine, could otherwise deny a user their login.
func TestLoopbackWaiterIgnoresRequestsThatAreNotTheCallback(t *testing.T) {
	waiter, err := newLoopbackCallbackWaiter("", auth.NewSecret(""))
	if err != nil {
		t.Fatalf("bind loopback waiter: %v", err)
	}
	defer waiter.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	for _, probe := range []string{"", "?", "?foo=bar", "?state=only-state"} {
		resp, err := client.Get(waiter.RedirectURI() + probe)
		if err != nil {
			t.Fatalf("probe %q: %v", probe, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("probe %q returned %d, want 404 so the one-shot survives", probe, resp.StatusCode)
		}
	}

	// The real callback must still be delivered after all of that.
	resp, err := client.Get(waiter.RedirectURI() + "?code=test-not-a-real-code&state=test-state")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback returned %d, want 200", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	callback, err := waiter.Wait(ctx, auth.NewSecret("test-state"))
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if callback.Code.Reveal() != "test-not-a-real-code" {
		t.Errorf("the callback code did not survive the probes")
	}
}

// The browser page is static: a code echoed into it lands in a tab, a history
// entry, and quite possibly a screen share.
func TestLoopbackWaiterPageEchoesNothing(t *testing.T) {
	waiter, err := newLoopbackCallbackWaiter("", auth.NewSecret(""))
	if err != nil {
		t.Fatalf("bind loopback waiter: %v", err)
	}
	defer waiter.Close()

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(
		waiter.RedirectURI() + "?code=test-not-a-real-code&state=test-not-a-real-state")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		t.Fatalf("read callback page: %v", err)
	}
	page := string(body)
	for _, secret := range []string{"test-not-a-real-code", "test-not-a-real-state"} {
		if strings.Contains(page, secret) {
			t.Errorf("the callback page echoed %q: %s", secret, page)
		}
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}
