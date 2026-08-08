package emoji

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// Emoji detection decides whether a cluster gets the highlight treatment in a
// chat row. It runs on API-supplied text, so it has to be right about the
// shapes real chat actually contains - and, more importantly, it must never
// claim that something spanning two clusters is one.
func TestIsClusterCoversTheShapesRealChatContains(t *testing.T) {
	emoji := map[string]string{
		"simple":                       "😀",
		"skin tone modifier":           "👋🏽",
		"ZWJ family":                   "👨‍👩‍👧‍👦",
		"ZWJ profession":               "👩‍💻",
		"regional indicator flag":      "🇯🇵",
		"tag sequence flag":            "🏴󠁧󠁢󠁳󠁣󠁴󠁿",
		"keycap":                       "1️⃣",
		"text presentation selector":   "❤️",
		"emoji with variation dropped": "❤",
		"gendered with skin tone":      "🧑🏿‍🚀",
	}
	for name, cluster := range emoji {
		t.Run(name, func(t *testing.T) {
			// The fixture is only meaningful if it is genuinely one cluster.
			if got := uniseg.GraphemeClusterCount(cluster); got != 1 {
				t.Fatalf("the fixture %q is %d clusters, not 1", cluster, got)
			}
			if !IsCluster(cluster) {
				t.Errorf("IsCluster(%q) = false, want true", cluster)
			}
		})
	}

	notEmoji := map[string]string{
		"ascii letter":            "a",
		"digit without keycap":    "1",
		"cjk":                     "日",
		"combining accent":        "é",
		"space":                   " ",
		"empty":                   "",
		"punctuation":             "!",
		"cyrillic":                "д",
		"zero width joiner alone": "\u200d",
	}
	for name, cluster := range notEmoji {
		t.Run(name, func(t *testing.T) {
			if IsCluster(cluster) {
				t.Errorf("IsCluster(%q) = true, want false", cluster)
			}
		})
	}
}

// A multi-cluster string is never one emoji. Accepting one would let a chatter
// smuggle arbitrary text into a highlighted emoji cell.
func TestIsClusterRejectsAnythingSpanningMoreThanOneCluster(t *testing.T) {
	for _, value := range []string{
		"😀😀",
		"😀a",
		"a😀",
		"👋🏽 ",
		"😀\n",
		"👨‍👩‍👧‍👦👨‍👩‍👧‍👦",
	} {
		if uniseg.GraphemeClusterCount(value) < 2 {
			t.Fatalf("the fixture %q is not multi-cluster", value)
		}
		if IsCluster(value) {
			t.Errorf("IsCluster(%q) = true; a multi-cluster string is not one emoji", value)
		}
	}
}

// The asset ID is a stable key derived from the cluster, so a presentation
// selector - which changes nothing about which emoji it is - must not produce a
// second identity for the same glyph.
func TestAssetIDIsStableAcrossPresentationSelectors(t *testing.T) {
	withSelector, ok := AssetID("❤️")
	if !ok {
		t.Fatal("AssetID rejected an emoji with a presentation selector")
	}
	without, ok := AssetID("❤")
	if !ok {
		t.Fatal("AssetID rejected the same emoji without a selector")
	}
	if withSelector != without {
		t.Errorf("AssetID differs by presentation selector: %q vs %q", withSelector, without)
	}

	// It must be deterministic: an ID that varied per call would defeat any
	// cache keyed on it.
	for range 10 {
		again, _ := AssetID("👨‍👩‍👧‍👦")
		first, _ := AssetID("👨‍👩‍👧‍👦")
		if again != first {
			t.Fatalf("AssetID varied between calls: %q vs %q", again, first)
		}
	}

	if _, ok := AssetID("not an emoji"); ok {
		t.Error("AssetID accepted a non-emoji")
	}
	if _, ok := AssetID(""); ok {
		t.Error("AssetID accepted an empty string")
	}
}

// Every catalog entry backs a picker row, so a malformed one would render as a
// broken cell in a UI the user is choosing from.
func TestCatalogIsUsableAsAPickerSource(t *testing.T) {
	entries := Catalog()
	if len(entries) == 0 {
		t.Fatal("the catalog is empty; the emoji picker would have nothing to show")
	}

	seen := make(map[string]bool, len(entries))
	for i, entry := range entries {
		if got := uniseg.GraphemeClusterCount(entry.Cluster); got != 1 {
			t.Errorf("entry %d (%q) is %d clusters", i, entry.Cluster, got)
		}
		if !IsCluster(entry.Cluster) {
			t.Errorf("entry %d (%q) is not detected as an emoji by the package's own check", i, entry.Cluster)
		}
		if strings.TrimSpace(entry.Name) == "" {
			t.Errorf("entry %d (%q) has no name to search by", i, entry.Cluster)
		}
		if seen[entry.Cluster] {
			t.Errorf("entry %d (%q) is a duplicate; the picker would show it twice", i, entry.Cluster)
		}
		seen[entry.Cluster] = true

		// A picker cell is budgeted in display cells, so an entry wider than
		// two would overflow the grid the picker draws.
		if width := ansi.StringWidth(entry.Cluster); width < 1 || width > 2 {
			t.Errorf("entry %d (%q) measures %d cells", i, entry.Cluster, width)
		}
	}
}
