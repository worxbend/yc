package app

import "strings"

// keyBinding is one documented key and what it does.
//
// The help footer, the expanded help overlay, and the command palette are
// generated from this one table rather than maintained beside it. twi learned
// that the hard way: ctrl+e opened the emote picker dozens of times a session
// and appeared in none of the three surfaces, discoverable only by reading the
// README. keymap_coverage_test.go scans the package for handled ctrl keys and
// fails when one is missing here, so the next drift is a test failure rather
// than an undiscoverable feature.
type keyBinding struct {
	// Keys is the binding as a user would type it ("ctrl+e", "space c").
	Keys string
	// Description is the sentence used in expanded help and the palette.
	Description string
	// Group orders bindings within the expanded help.
	Group keyGroup
}

type keyGroup int

const (
	keyGroupChat keyGroup = iota
	keyGroupModeration
	keyGroupChats
	keyGroupView
	keyGroupDisplay
	keyGroupSession
)

// keyGroupLabels names each help section.
var keyGroupLabels = map[keyGroup]string{
	keyGroupChat:       "Chat",
	keyGroupModeration: "Moderation",
	keyGroupChats:      "Chats",
	keyGroupView:       "View",
	keyGroupDisplay:    "Display",
	keyGroupSession:    "Session",
}

// keyGroupOrder is the order help renders the sections in.
//
// Moderation sits directly after Chat because it acts on the message the chat
// cursor has selected: the keys are meaningless without the ones above them.
var keyGroupOrder = []keyGroup{
	keyGroupChat,
	keyGroupModeration,
	keyGroupChats,
	keyGroupView,
	keyGroupDisplay,
	keyGroupSession,
}

// keyBindings is the whole documented keymap, in the order help presents it.
var keyBindings = []keyBinding{
	{Keys: "i/o/a", Description: "compose", Group: keyGroupChat},
	{Keys: "esc", Description: "back to chat", Group: keyGroupChat},
	{Keys: "j/k", Description: "select message", Group: keyGroupChat},
	{Keys: "pgup/pgdn", Description: "scroll", Group: keyGroupChat},
	{Keys: "ctrl+d/ctrl+u", Description: "half-page scroll", Group: keyGroupChat},
	{Keys: "g/G", Description: "oldest/newest", Group: keyGroupChat},
	{Keys: "/", Description: "search messages", Group: keyGroupChat},
	{Keys: "n/N", Description: "next/prev match", Group: keyGroupChat},
	{Keys: "y", Description: "copy selected message", Group: keyGroupChat},
	{Keys: "r", Description: "reply", Group: keyGroupChat},
	{Keys: "K", Description: "inspect", Group: keyGroupChat},
	{Keys: "ctrl+e", Description: "emoji picker", Group: keyGroupChat},
	{Keys: "@ then tab", Description: "complete a mention", Group: keyGroupChat},

	// Moderation is documented even when the credential cannot use it. The
	// keys stay bound and answer with a reason, so help that hid them would
	// leave the user unable to find out why nothing happened.
	{Keys: "d", Description: "delete selected message (asks first)", Group: keyGroupModeration},
	{Keys: "t", Description: "time out author (asks for a duration)", Group: keyGroupModeration},
	{Keys: "b", Description: "ban author (asks first)", Group: keyGroupModeration},

	{Keys: "space e", Description: "chats sidebar", Group: keyGroupChats},
	{Keys: "space c", Description: "open chat", Group: keyGroupChats},
	{Keys: "space x", Description: "close chat", Group: keyGroupChats},
	{Keys: "space a", Description: "activity column", Group: keyGroupChats},
	{Keys: "space i", Description: "inspect", Group: keyGroupChats},
	{Keys: "[/]", Description: "switch chat", Group: keyGroupChats},

	{Keys: "1-4", Description: "filters", Group: keyGroupView},
	{Keys: "0", Description: "reset filters", Group: keyGroupView},
	{Keys: "alt+1/2/3", Description: "tabs", Group: keyGroupView},
	{Keys: "tab", Description: "focus chat/composer/chats", Group: keyGroupView},
	{Keys: "?", Description: "help", Group: keyGroupView},
	{Keys: "</>", Description: "resize pane", Group: keyGroupView},
	{Keys: "=", Description: "reset pane sizes", Group: keyGroupView},

	{Keys: "ctrl+t", Description: "theme", Group: keyGroupDisplay},
	{Keys: "ctrl+g", Description: "layout", Group: keyGroupDisplay},
	{Keys: "ctrl+b", Description: "badges", Group: keyGroupDisplay},
	{Keys: "ctrl+y", Description: "emoji highlight", Group: keyGroupDisplay},
	{Keys: "ctrl+n", Description: "full names", Group: keyGroupDisplay},

	{Keys: "ctrl+p", Description: "commands", Group: keyGroupSession},
	{Keys: "ctrl+r", Description: "reconnect", Group: keyGroupSession},
	{Keys: "ctrl+l", Description: "clear (asks first)", Group: keyGroupSession},
	{Keys: "q", Description: "quit", Group: keyGroupSession},
	{Keys: "ctrl+c", Description: "quit", Group: keyGroupSession},
}

// keyBindingsInGroup returns the bindings for one help section.
func keyBindingsInGroup(group keyGroup) []keyBinding {
	out := make([]keyBinding, 0, len(keyBindings))
	for _, binding := range keyBindings {
		if binding.Group == group {
			out = append(out, binding)
		}
	}
	return out
}

// helpGroupLine renders one expanded-help row from the table.
func helpGroupLine(group keyGroup) string {
	bindings := keyBindingsInGroup(group)
	parts := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		parts = append(parts, binding.Keys+": "+binding.Description)
	}
	return " " + strings.Join(parts, " | ")
}

// compactFooter is the one-line footer, ordered for reading rather than in the
// order the table declares. Each entry names a binding by its Keys value and
// TestCompactFooterOnlyNamesDocumentedKeys fails when one stops existing, so
// the short form cannot outlive the key it advertises.
var compactFooter = []struct {
	Keys  string
	Label string
}{
	{Keys: "ctrl+p", Label: "ctrl+p"},
	{Keys: "i/o/a", Label: "i/esc"},
	{Keys: "j/k", Label: "jk"},
	{Keys: "space e", Label: "space e/c"},
	{Keys: "[/]", Label: "[]"},
	{Keys: "1-4", Label: "1-4/0"},
	{Keys: "?", Label: "?"},
	{Keys: "r", Label: "r/K"},
	{Keys: "ctrl+e", Label: "ctrl+e"},
	{Keys: "q", Label: "q quit/ctrl+c quit"},
}

func compactHelpLine() string {
	parts := make([]string, 0, len(compactFooter))
	for _, entry := range compactFooter {
		parts = append(parts, entry.Label)
	}
	return " " + strings.Join(parts, " | ")
}

// hasBindingForKeys reports whether the table documents a binding under
// exactly this Keys value.
func hasBindingForKeys(keys string) bool {
	for _, binding := range keyBindings {
		if binding.Keys == keys {
			return true
		}
	}
	return false
}

// documentedKeys reports every key form named in the table, for the coverage
// test that compares it against what the update loop actually handles.
func documentedKeys() map[string]bool {
	keys := make(map[string]bool, len(keyBindings)*2)
	for _, binding := range keyBindings {
		for _, key := range strings.Split(binding.Keys, "/") {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			keys[key] = true
			// A digit range such as "1-4" documents each key in it; other
			// surfaces name them individually.
			if len(key) == 3 && key[1] == '-' && isASCIIDigit(key[0]) && isASCIIDigit(key[2]) {
				for digit := key[0]; digit <= key[2]; digit++ {
					keys[string(digit)] = true
				}
			}
		}
	}
	return keys
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
