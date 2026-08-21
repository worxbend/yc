package app

import (
	"sort"
	"strings"
	"time"

	"github.com/worxbend/yc/internal/youtube"
)

// chatRoster is who has been seen speaking in one chat.
//
// YouTube reports no presence, so this is best-effort: it is the authors
// observed sending messages, not a member list. It backs @mention completion
// and the grouped layout's author meta, and it answers what role the signed-in
// user holds in this chat.
//
// It is bounded. Mention completion only ever needs recent speakers, so the
// oldest is evicted once the ring is full rather than letting a long broadcast
// with a large distinct-chatter count grow an entry per person for the life of
// the session.
//
// The three fields are one type rather than three fields on chatState because
// they have to be evicted together. authors and firstSeen are keyed the same
// way and ring decides what leaves; dropping a key from one and not the other
// would leave a speaker with a timestamp and no author, or the reverse. Keeping
// them behind one type means no caller can evict half of a speaker.
type chatRoster struct {
	authors   map[string]youtube.Author
	firstSeen map[string]time.Time
	ring      boundedRing
}

// observe records the author of a message as a speaker in this chat.
func (r *chatRoster) observe(message youtube.Message) {
	r.remember(message.Author, message.Timestamp)
}

// remember files one author, first seen at the given time.
//
// A zero time means "seen, but with no usable timestamp": the speaker is
// recorded and firstSeenAt keeps reporting the zero time, which the renderer
// omits rather than guessing at.
func (r *chatRoster) remember(author youtube.Author, at time.Time) {
	identity := author.Identity()
	if identity == "" {
		return
	}
	if r.authors == nil {
		r.authors = make(map[string]youtube.Author, rosterRingSize)
	}
	if r.firstSeen == nil {
		r.firstSeen = make(map[string]time.Time, rosterRingSize)
	}
	if _, known := r.authors[identity]; !known {
		// New speaker: take the oldest slot. Every other per-chat structure
		// is bounded - scrollback, moderations, the seen-ID ring, the row
		// cache - and without this one the roster is the one that grows
		// without limit.
		if evicted := r.ring.record(identity, rosterRingSize); evicted != "" {
			delete(r.authors, evicted)
			delete(r.firstSeen, evicted)
		}
	}
	r.authors[identity] = author
	if _, ok := r.firstSeen[identity]; !ok && !at.IsZero() {
		r.firstSeen[identity] = at
	}
}

// lookup returns the author filed under an identity.
func (r *chatRoster) lookup(identity string) (youtube.Author, bool) {
	author, ok := r.authors[identity]
	return author, ok
}

// firstSeenAt reports when an author was first seen in this chat, for the
// grouped layout's author meta. An unknown author yields the zero time, which
// the renderer omits rather than guessing.
func (r *chatRoster) firstSeenAt(identity string) time.Time {
	if identity == "" {
		return time.Time{}
	}
	return r.firstSeen[identity]
}

// size is how many distinct speakers are currently filed.
func (r *chatRoster) size() int { return len(r.authors) }

// names returns the display names seen speaking, sorted and de-duplicated
// case-insensitively, for the mention completion strip.
func (r *chatRoster) names() []string {
	if len(r.authors) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.authors))
	seen := make(map[string]bool, len(r.authors))
	for _, author := range r.authors {
		name := strings.TrimSpace(author.DisplayName)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
