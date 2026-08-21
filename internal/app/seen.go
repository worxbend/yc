package app

import "strings"

// seenMessageSet is the bounded set of message IDs one chat has already filed,
// and the only defense against a re-delivered backlog.
//
// The poller dedupes within one session, but its ring dies with it: ctrl+r and
// the reconnect ladder both build a fresh transport, which primes with no page
// token and an empty ring, so YouTube re-sends the whole recent history.
// Without this the user's own reconnect key printed every message on screen a
// second time.
//
// The set and the ring are one type because they are evicted together: the
// ring names the ID that leaves and the set has to forget exactly that one.
// Split across two fields it was two statements that had to stay in step.
type seenMessageSet struct {
	ids  map[string]struct{}
	ring boundedRing
}

// admit records a message ID and reports whether it is new to this chat.
//
// A message with no ID is always new: locally generated system rows carry none
// and there is nothing to key them by.
//
// echoed marks an ID this chat printed itself, optimistically, before the
// server confirmed it. Such an ID is let through even though it is already in
// the set, because the authoritative copy carries the same ID and appendMessage
// has to see it in order to swap the optimistic row for the real one.
func (s *seenMessageSet) admit(id string, echoed bool) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return true
	}
	if s.ids == nil {
		s.ids = make(map[string]struct{}, seenMessageRingSize)
	}
	if !echoed {
		if _, ok := s.ids[id]; ok {
			return false
		}
	}
	if evicted := s.ring.record(id, seenMessageRingSize); evicted != "" {
		delete(s.ids, evicted)
	}
	s.ids[id] = struct{}{}
	return true
}

// size is how many IDs are currently filed.
func (s *seenMessageSet) size() int { return len(s.ids) }

// reset forgets every ID, returning the set to its zero state.
func (s *seenMessageSet) reset() {
	s.ids = nil
	s.ring.reset()
}
