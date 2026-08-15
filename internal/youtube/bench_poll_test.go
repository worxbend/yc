package youtube

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// BenchmarkPollerDelivery measures ingestion throughput end to end: an
// in-process server hands the poller pages of pre-rendered wire items, and the
// benchmark counts normalized messages arriving on Messages(). The cost per
// message therefore covers the HTTP round trip, JSON decoding, normalization,
// dedupe, and channel delivery — everything between the API and the app.
//
// The harness's stubbed Sleep returns immediately, so the poller free-runs;
// msgs/s sustained is 1e9 / ns/op. Item IDs are unique across pages so the
// dedupe ring forwards every message rather than dropping the whole run.
func BenchmarkPollerDelivery(b *testing.B) {
	const pageSize = 50

	// Pre-render one page as a template with a per-call marker, so the
	// handler's own cost per request is a strings.Replace and a write. IDs
	// must be unique across calls or the poller's dedupe ring would start
	// dropping repeats and the receive loop below would stall.
	items := make([]string, 0, pageSize)
	for i := 0; i < pageSize; i++ {
		items = append(items, textItem(
			fmt.Sprintf("bench-CALL-%d", i),
			"a plain chat line with an emoji 😀 and an @mention",
		))
	}
	template := fmt.Sprintf(`{"items":[%s],"nextPageToken":"next","pollingIntervalMillis":1000}`,
		strings.Join(items, ","))

	harness := newPollHarness(b, PollerConfig{}, func(call int, w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.ReplaceAll(template, "CALL", fmt.Sprintf("%d", call)))
	})

	b.ReportAllocs()
	b.ResetTimer()
	received := 0
	for received < b.N {
		message, ok := <-harness.poller.Messages()
		if !ok {
			b.Fatalf("message stream closed after %d of %d messages", received, b.N)
		}
		if message.ID == "" {
			b.Fatal("received a message with no ID")
		}
		received++
	}
}
