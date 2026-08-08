package emoji

import (
	"strings"
	"testing"

	"github.com/rivo/uniseg"
)

func TestIsClusterAcceptsEmojiShapes(t *testing.T) {
	clusters := map[string]string{
		"😀":       "single base",
		"❤️":      "base with a presentation selector",
		"👍🏽":      "base with a skin-tone modifier",
		"👨‍👩‍👧‍👦": "ZWJ family sequence",
		"🇯🇵":      "regional-indicator flag",
		"1️⃣":     "keycap with a selector",
		"#⃣":      "keycap without a selector",
		"⚡":       "symbol-block base",
		"▶️":      "geometric-shape base",
	}
	for cluster, description := range clusters {
		if !IsCluster(cluster) {
			t.Fatalf("IsCluster(%q) = false for %s, want true", cluster, description)
		}
	}
}

func TestIsClusterRejectsNonEmoji(t *testing.T) {
	for _, cluster := range []string{"", "a", "é", " ", "字", "\u200d", "\U0001F3FB", "ab"} {
		if IsCluster(cluster) {
			t.Fatalf("IsCluster(%q) = true, want false", cluster)
		}
	}
}

// A partial cluster is what byte or rune slicing produces. It must not read as
// an emoji, or a mangled row would silently render as a valid chip.
func TestIsClusterRejectsPartialSequences(t *testing.T) {
	for _, cluster := range []string{"\U0001F468\u200d", "\U0001F1EF", "1️"} {
		if IsCluster(cluster) {
			t.Fatalf("IsCluster(%q) = true for a partial sequence, want false", cluster)
		}
	}
}

func TestAssetIDDropsPresentationSelectors(t *testing.T) {
	withSelector, ok := AssetID("❤️")
	if !ok {
		t.Fatal("AssetID(\"❤️\") ok = false, want true")
	}
	withoutSelector, ok := AssetID("❤")
	if !ok {
		t.Fatal("AssetID(\"❤\") ok = false, want true")
	}
	if withSelector != withoutSelector {
		t.Fatalf("AssetID with and without a selector = %q and %q, want one key", withSelector, withoutSelector)
	}
	if want := "2764"; withSelector != want {
		t.Fatalf("AssetID(\"❤️\") = %q, want %q", withSelector, want)
	}
	if got, want := mustAssetID(t, "👍🏽"), "1f44d-1f3fd"; got != want {
		t.Fatalf("AssetID(\"👍🏽\") = %q, want %q", got, want)
	}
	if id, ok := AssetID("a"); ok || id != "" {
		t.Fatalf("AssetID(\"a\") = (%q, %v), want (\"\", false)", id, ok)
	}
}

// The catalog is what the ctrl+e picker inserts into the composer, so every
// entry has to be one cluster the renderer will measure as one chip.
func TestCatalogEntriesAreSingleValidClusters(t *testing.T) {
	entries := Catalog()
	if len(entries) < 100 {
		t.Fatalf("Catalog() has %d entries, want a usable picker set", len(entries))
	}
	seenCluster := make(map[string]string, len(entries))
	seenID := make(map[string]string, len(entries))
	for _, entry := range entries {
		if !IsCluster(entry.Cluster) {
			t.Fatalf("catalog entry %q (%s) is not an emoji cluster", entry.Cluster, entry.Name)
		}
		if got := countClusters(entry.Cluster); got != 1 {
			t.Fatalf("catalog entry %q (%s) is %d clusters, want 1", entry.Cluster, entry.Name, got)
		}
		if strings.TrimSpace(entry.Name) == "" {
			t.Fatalf("catalog entry %q has no name", entry.Cluster)
		}
		if len(entry.Keywords) == 0 {
			t.Fatalf("catalog entry %q (%s) has no search keywords", entry.Cluster, entry.Name)
		}
		if other, ok := seenCluster[entry.Cluster]; ok {
			t.Fatalf("catalog lists %q twice: %s and %s", entry.Cluster, other, entry.Name)
		}
		seenCluster[entry.Cluster] = entry.Name

		id := mustAssetID(t, entry.Cluster)
		if other, ok := seenID[id]; ok {
			t.Fatalf("catalog entries %s and %s share asset ID %q", other, entry.Name, id)
		}
		seenID[id] = entry.Name
	}
}

// Catalog hands out a copy: a picker that filters in place must not be able to
// shrink the built-in set for the rest of the process.
func TestCatalogReturnsACopy(t *testing.T) {
	first := Catalog()
	original := first[0]
	first[0] = Entry{Cluster: "😀", Name: "clobbered"}
	if got := Catalog()[0]; got.Name != original.Name || got.Cluster != original.Cluster {
		t.Fatalf("Catalog() returned shared backing storage: %+v", got)
	}
}

func mustAssetID(t *testing.T, cluster string) string {
	t.Helper()
	id, ok := AssetID(cluster)
	if !ok {
		t.Fatalf("AssetID(%q) ok = false, want true", cluster)
	}
	return id
}

func countClusters(value string) int {
	count := 0
	graphemes := uniseg.NewGraphemes(value)
	for graphemes.Next() {
		count++
	}
	return count
}
