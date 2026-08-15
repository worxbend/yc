package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ctrlKeyPattern finds the tea.KeyCtrlX constants the shell actually handles.
var ctrlKeyPattern = regexp.MustCompile(`tea\.KeyCtrl([A-Z])\b`)

// TestEveryHandledCtrlKeyIsDocumented is the guard the keymap table exists for.
//
// In twi, key handling, the help footer, the expanded help, and the command
// palette were four separately maintained things, and ctrl+e - used dozens of
// times a session - had fallen out of every documented surface. Scanning the
// source for handled keys and comparing against the table catches the next one.
func TestEveryHandledCtrlKeyIsDocumented(t *testing.T) {
	// Keys handled only inside an input, or inside a modal that labels itself
	// on screen.
	exempt := map[string]bool{
		"ctrl+h": true, // backspace alias
		"ctrl+u": true, // clear line
		"ctrl+s": true, // stream info: save, labeled on the tab itself
	}

	documented := documentedKeys()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range ctrlKeyPattern.FindAllStringSubmatch(string(source), -1) {
			key := "ctrl+" + strings.ToLower(match[1])
			if _, ok := seen[key]; !ok {
				seen[key] = name
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("found no tea.KeyCtrl* handlers; the scan is broken, not the keymap")
	}

	for key, file := range seen {
		if exempt[key] || documented[key] {
			continue
		}
		t.Errorf("%s is handled in %s but is not in keyBindings, so it appears in no help surface", key, file)
	}
}

// TestCompactFooterOnlyNamesDocumentedKeys keeps the short footer from
// advertising a binding the table no longer has.
func TestCompactFooterOnlyNamesDocumentedKeys(t *testing.T) {
	for _, entry := range compactFooter {
		if !hasBindingForKeys(entry.Keys) {
			t.Errorf("compact footer names %q, which is not in keyBindings", entry.Keys)
		}
	}
}

// TestExpandedHelpCoversEveryBinding makes the table the thing that is
// rendered, rather than a list that happens to sit beside the rendering.
func TestExpandedHelpCoversEveryBinding(t *testing.T) {
	lines := make([]string, 0, len(keyGroupOrder))
	for _, group := range keyGroupOrder {
		lines = append(lines, helpGroupLine(group))
	}
	rendered := strings.Join(lines, "\n")

	for _, binding := range keyBindings {
		if !strings.Contains(rendered, binding.Keys) {
			t.Errorf("binding %q is in the table but reaches no help group", binding.Keys)
		}
	}
}

// TestEveryKeyGroupHasALabel keeps a new group from rendering as an unnamed
// block of keys.
func TestEveryKeyGroupHasALabel(t *testing.T) {
	for _, group := range keyGroupOrder {
		if strings.TrimSpace(keyGroupLabels[group]) == "" {
			t.Errorf("key group %d has no label", group)
		}
		if len(keyBindingsInGroup(group)) == 0 {
			t.Errorf("key group %q is empty", keyGroupLabels[group])
		}
	}
}

// TestCommandPaletteReachesEveryDisplayKey covers the discoverability gap the
// palette exists to close: a display toggle reachable only by already knowing
// its key is not discoverable at all.
func TestCommandPaletteReachesEveryDisplayKey(t *testing.T) {
	shortcuts := make([]string, 0)
	for _, command := range paletteCommands() {
		shortcuts = append(shortcuts, command.shortcut)
	}
	joined := strings.Join(shortcuts, " ")
	for _, key := range []string{"ctrl+e", "ctrl+t", "ctrl+g", "ctrl+b", "ctrl+y", "ctrl+n", "ctrl+r"} {
		if !strings.Contains(joined, key) {
			t.Errorf("%s has no command palette entry, so it is discoverable only by already knowing it", key)
		}
	}
}

// TestCommandPaletteShortcutsAreDocumentedKeys keeps the palette's shortcut
// column from naming a binding the keymap no longer has.
func TestCommandPaletteShortcutsAreDocumentedKeys(t *testing.T) {
	documented := documentedKeys()
	for _, command := range paletteCommands() {
		shortcut := strings.TrimSpace(command.shortcut)
		if shortcut == "" {
			continue
		}
		if hasBindingForKeys(shortcut) {
			continue
		}
		if documented[shortcut] {
			continue
		}
		t.Errorf("palette entry %q advertises %q, which is not in keyBindings", command.title, shortcut)
	}
}

// TestModerationKeysAreDocumented extends the coverage guard to the keys that
// cannot be discovered any other way.
//
// The ctrl-key scan above cannot see these: they are plain runes, dispatched
// from a switch on msg.Runes rather than from a tea.KeyCtrl constant. They are
// also the three most consequential keys in the program - each one removes
// somebody's words from a live broadcast - so an undocumented moderation key is
// worse than an undocumented display toggle, not better.
func TestModerationKeysAreDocumented(t *testing.T) {
	documented := documentedKeys()
	for _, key := range []string{
		string(moderationDeleteRune),
		string(moderationTimeoutRune),
		string(moderationBanRune),
	} {
		if !documented[key] {
			t.Errorf("moderation key %q is handled but is not in keyBindings", key)
		}
		if !strings.Contains(helpGroupLine(keyGroupModeration), key+":") {
			t.Errorf("moderation key %q is missing from the expanded help", key)
		}
	}
}

// TestModerationKeysStayDocumentedWhenTheyAreDisabled is the contract's
// promise, asserted rather than trusted: without the scope or the role the keys
// are inert, and inert keys that vanish from help leave the user with no way to
// learn why nothing happened.
func TestModerationKeysStayDocumentedWhenTheyAreDisabled(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.client = &fakeReadOnlyClient{}
	if capability := model.moderationCapability(); capability.Available {
		t.Fatal("a read-only source reported moderation as available")
	}

	groups := make([]string, 0, len(keyGroupOrder))
	for _, group := range keyGroupOrder {
		groups = append(groups, helpGroupLine(group))
	}
	rendered := strings.Join(groups, "\n")
	for _, binding := range keyBindingsInGroup(keyGroupModeration) {
		if !strings.Contains(rendered, binding.Keys+": "+binding.Description) {
			t.Errorf("moderation binding %q left help while the capability was unavailable", binding.Keys)
		}
	}
}

// TestEmojiPickerKeyIsDiscoverable pins twi's specific regression by name.
func TestEmojiPickerKeyIsDiscoverable(t *testing.T) {
	if !documentedKeys()["ctrl+e"] {
		t.Fatal("ctrl+e (emoji picker) is undocumented again")
	}
	if !strings.Contains(compactHelpLine(), "ctrl+e") {
		t.Fatal("ctrl+e is missing from the compact footer")
	}
	if !strings.Contains(helpGroupLine(keyGroupChat), "ctrl+e") {
		t.Fatal("ctrl+e is missing from the expanded help")
	}
}
