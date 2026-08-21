package youtube

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEventDocumentationCoversEveryKind keeps docs/events.md honest.
//
// That document is the reference a contributor reads before adding a chat event
// kind, so it is the file most likely to send someone to the wrong set of
// places when it is stale — and nothing about adding an EventKind forces anyone
// to open it. A row missing from the table is a wire type the reader will not
// know yc already handles; worse, a wrong entry is a reader confidently doing
// the wrong thing.
//
// The check is deliberately shallow: it asserts that every snippet.type and
// every EventKind is mentioned somewhere in the document. It cannot tell
// whether the prose around a row is still true, so it is a drift alarm rather
// than a proof of correctness.
func TestEventDocumentationCoversEveryKind(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "events.md")
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(doc)

	for _, snippetType := range DocumentedSnippetTypes() {
		if !strings.Contains(text, "`"+snippetType+"`") {
			t.Errorf("snippet.type %q is decoded by yc but is not in docs/events.md", snippetType)
		}
	}
	for _, kind := range AllEventKinds() {
		if !strings.Contains(text, "`"+string(kind)+"`") {
			t.Errorf("EventKind %q is produced by yc but is not in docs/events.md", kind)
		}
	}
}
