package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/quota"
)

// TestExportedTypesNeverFormatACredential formats every exported type in this
// package that can hold or reach a credential, in every way a careless caller
// might: %v, %+v, %#v, and json.Marshal.
//
// This is the test that keeps a debug record, a status line, or a future
// snapshot from leaking a live token. It is cheap to run and the failure it
// prevents is not recoverable once it has happened.
func TestExportedTypesNeverFormatACredential(t *testing.T) {
	credentials := StaticCredentials{
		Token: auth.NewSecret(testToken),
		Key:   auth.NewSecret(testAPIKey),
	}
	client, err := NewClient(ClientConfig{
		Credentials: credentials,
		Endpoint:    "https://example.invalid/youtube/v3",
	})
	if err != nil {
		t.Fatalf("NewClient error = %v", err)
	}

	values := []struct {
		name  string
		value any
	}{
		{name: "StaticCredentials", value: credentials},
		{name: "ClientConfig", value: ClientConfig{Credentials: credentials, HL: "en"}},
		{name: "Client", value: client},
		{name: "APIError", value: &APIError{StatusCode: 403, Reason: "forbidden", Method: quota.EndpointMessagesList, Message: "denied"}},
		{name: "SendResult", value: SendResult{MessageID: "sent-1", Detail: "ok"}},
		{name: "ChatTarget", value: ChatTarget{Raw: "@handle", Kind: TargetHandle, Handle: "@handle"}},
		{name: "Identity", value: Identity{ChannelID: "UCme00000000000000000001", Scopes: []string{string(auth.ScopeYouTubeReadonly)}}},
		{name: "ListResult", value: ListResult{NextPageToken: "page-2"}},
	}

	for _, subject := range values {
		t.Run(subject.name, func(t *testing.T) {
			rendered := []string{
				fmt.Sprintf("%v", subject.value),
				fmt.Sprintf("%+v", subject.value),
				fmt.Sprintf("%#v", subject.value),
				fmt.Sprintf("%s", subject.value),
			}
			if encoded, err := json.Marshal(subject.value); err == nil {
				rendered = append(rendered, string(encoded))
			}
			for _, output := range rendered {
				for _, secret := range []string{testToken, testAPIKey} {
					if strings.Contains(output, secret) {
						t.Fatalf("formatted %s leaked a credential: %s", subject.name, output)
					}
				}
			}
		})
	}
}

func TestAPIErrorRedactsWhateverTheEndpointEchoedBack(t *testing.T) {
	client, _ := newTestClient(t, StaticCredentials{
		Token: auth.NewSecret(testToken),
		Key:   auth.NewSecret(testAPIKey),
	}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error":{"code":403,"message":"bad request access_token=%s key=%s Bearer %s","errors":[{"reason":"forbidden"}]}}`,
			testToken, testAPIKey, testToken)
	})

	_, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
	if err == nil {
		t.Fatal("error = nil, want a 403")
	}

	var apiErr *APIError
	if !asAPIErrorInto(err, &apiErr) {
		t.Fatalf("error = %T, want *APIError", err)
	}
	for _, output := range []string{apiErr.Error(), apiErr.Message, fmt.Sprintf("%+v", apiErr)} {
		for _, secret := range []string{testToken, testAPIKey} {
			if strings.Contains(output, secret) {
				t.Fatalf("APIError leaked a credential: %s", output)
			}
		}
	}
	if !strings.Contains(apiErr.Message, auth.RedactedSecret) {
		t.Fatalf("Message = %q, want the redaction marker in place of the credentials", apiErr.Message)
	}
}

func TestErrorsNeverQuoteTheRequestURL(t *testing.T) {
	// The API key travels in the query string, so a URL in an error is a
	// credential in an error even when the token itself is absent.
	client, server := newTestClient(t, keyCredentials(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "boom")
	})

	calls := []struct {
		name string
		run  func() error
	}{
		{name: "list", run: func() error {
			_, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
			return err
		}},
		{name: "broadcast", run: func() error {
			_, err := client.Broadcast(context.Background(), "dQw4w9WgXcQ")
			return err
		}},
		{name: "resolve handle", run: func() error {
			_, err := client.ResolveHandle(context.Background(), "@handle")
			return err
		}},
		{name: "categories", run: func() error {
			_, err := client.Categories(context.Background())
			return err
		}},
	}

	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			err := call.run()
			if err == nil {
				t.Fatal("error = nil, want a 500")
			}
			assertNoCredentialLeak(t, err.Error(), server.URL)
			if strings.Contains(err.Error(), "http://") || strings.Contains(err.Error(), "https://") {
				t.Fatalf("error quotes a URL: %q", err.Error())
			}
		})
	}
}

// asAPIErrorInto is errors.As spelled out so the test file does not need to
// import errors just for one call.
func asAPIErrorInto(err error, target **APIError) bool {
	found, ok := asAPIError(err)
	if ok {
		*target = found
	}
	return ok
}
