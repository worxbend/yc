package app

import (
	"strings"
	"testing"

	"github.com/worxbend/yc/internal/youtube"
)

func TestTargetPickerHeaderStates(t *testing.T) {
	tests := []struct {
		name       string
		query, err string
		loading    bool
		want       string
	}{
		{name: "idle", want: "video ID, URL, or @handle"},
		{name: "typing", query: "@creator", want: "@creator"},
		{name: "loading", loading: true, want: "loading subscriptions"},
		// A failed subscriptions lookup is inline text, not an error that
		// stops the picker: open chats, configured defaults, and any typed
		// target still work.
		{name: "error", err: "subscriptions unavailable", want: "subscriptions unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := targetPickerHeader(test.query, test.err, test.loading)
			if !strings.Contains(got, test.want) {
				t.Fatalf("header = %q, want it to contain %q", got, test.want)
			}
		})
	}
}

// A credential-shaped string pasted into the picker must not be echoed back.
func TestTargetPickerHeaderRedactsErrors(t *testing.T) {
	got := targetPickerHeader("", "failed: access_token=test-not-a-real-token", false)
	if strings.Contains(got, "test-not-a-real-token") {
		t.Fatalf("target picker echoed a credential: %q", got)
	}
}

// The detail column tells the user whether enter will switch to something
// already open - free - or resolve something new, which costs a quota unit.
func TestTargetPickerDetailClassifiesRows(t *testing.T) {
	open := []string{"Launch Day Stream"}
	configured := []string{"@creator"}
	tests := map[string]string{
		"Launch Day Stream": string(targetSourceOpen),
		"@creator":          string(targetSourceConfigured),
		"dQw4w9WgXcQ":       string(targetSourceLiteral),
	}
	for item, want := range tests {
		if got := targetPickerDetail(item, open, configured); got != want {
			t.Errorf("targetPickerDetail(%q) = %q, want %q", item, got, want)
		}
	}
}

// A handle is what a person can read back and confirm they picked the right
// creator, so it wins over a channel ID.
func TestSubscriptionLabelsPreferHandles(t *testing.T) {
	labels := targetPickerSubscriptionLabels([]youtube.Subscription{
		{ChannelID: "UC-1", Title: "Creator One", Handle: "@creatorone"},
		{ChannelID: "UC-2", Title: "Creator Two"},
		{ChannelID: "UC-3"},
		{},
	})
	want := []string{"@creatorone", "Creator Two", "UC-3"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("label %d = %q, want %q", i, labels[i], want[i])
		}
	}
}
