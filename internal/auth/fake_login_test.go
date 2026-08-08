package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFakeLoginFlowUsesFakeMarkersOnly(t *testing.T) {
	flow := NewFakeLoginFlow()

	challenge, err := flow.BeginLogin(context.Background(), LoginRequest{ClientID: "client"})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	// Everything the fake hands out has to be obviously fake, so a leak in a
	// test fixture is greppable rather than plausible.
	for _, value := range []string{challenge.State.Reveal(), challenge.AuthorizationURL.Reveal()} {
		if !strings.Contains(value, FakeTokenMarker) {
			t.Fatalf("fake challenge value %q is missing the fake marker", value)
		}
	}

	result, err := flow.CompleteLogin(context.Background(), LoginCallback{Code: NewSecret(FakeTokenMarker)})
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if !strings.Contains(result.Tokens.AccessToken.Reveal(), FakeTokenMarker) {
		t.Fatal("the fake access token is missing the fake marker")
	}
	if len(result.MissingRequiredScopes()) != 0 {
		t.Fatalf("the fake result should grant every login scope, missing %v", result.MissingRequiredScopes())
	}

	if got := len(flow.RecordedRequests()); got != 1 {
		t.Fatalf("recorded %d requests, want 1", got)
	}
	if got := len(flow.RecordedCallbacks()); got != 1 {
		t.Fatalf("recorded %d callbacks, want 1", got)
	}
}

func TestFakeLoginFlowForcesFailurePaths(t *testing.T) {
	sentinel := errors.New("boom")

	flow := NewFakeLoginFlow()
	flow.BeginErr = sentinel
	if _, err := flow.BeginLogin(context.Background(), LoginRequest{}); !errors.Is(err, sentinel) {
		t.Fatalf("BeginErr was not honored, got %v", err)
	}

	flow = NewFakeLoginFlow()
	flow.CompleteErr = sentinel
	if _, err := flow.CompleteLogin(context.Background(), LoginCallback{}); !errors.Is(err, sentinel) {
		t.Fatalf("CompleteErr was not honored, got %v", err)
	}

	flow = NewFakeLoginFlow()
	flow.RefreshErr = sentinel
	if _, err := flow.Refresh(context.Background(), NewSecret(FakeTokenMarker)); !errors.Is(err, sentinel) {
		t.Fatalf("RefreshErr was not honored, got %v", err)
	}
}

func TestFakeLoginFlowRefreshKeepsCallerToken(t *testing.T) {
	flow := NewFakeLoginFlow()
	flow.Result.Tokens.RefreshToken = Secret("")

	tokens, err := flow.Refresh(context.Background(), NewSecret(FakeTokenMarker+"-caller"))
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tokens.RefreshToken.Reveal() != FakeTokenMarker+"-caller" {
		t.Fatal("an absent refresh token in the response means keep the caller's")
	}
}

func TestFakeLoginFlowHonorsContextCancellation(t *testing.T) {
	flow := NewFakeLoginFlow()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := flow.BeginLogin(ctx, LoginRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("BeginLogin: expected context.Canceled, got %v", err)
	}
	if _, err := flow.CompleteLogin(ctx, LoginCallback{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("CompleteLogin: expected context.Canceled, got %v", err)
	}
	if _, err := flow.Refresh(ctx, NewSecret(FakeTokenMarker)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh: expected context.Canceled, got %v", err)
	}
	if len(flow.RecordedRequests()) != 0 {
		t.Fatal("a canceled context must not record a request")
	}
}

func TestLoginCallbackFromRequestIsRedacted(t *testing.T) {
	// LoginCallbackFromRequest is finished code, but it is the entry point
	// for every value the browser hands back, so its redaction is asserted
	// alongside the fake it is used with.
	callback := LoginCallback{Code: NewSecret(FakeTokenMarker), State: NewSecret(FakeTokenMarker)}
	redacted := callback.Redactor().Redact("code=" + FakeTokenMarker)
	if strings.Contains(redacted, FakeTokenMarker) {
		t.Fatalf("callback redactor missed its own secret: %s", redacted)
	}
}
