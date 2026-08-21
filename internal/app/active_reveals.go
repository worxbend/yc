package app

import "github.com/worxbend/yc/internal/youtube"

// activeReveals is the messages currently mid-reveal in one chat: arrived, on
// screen animating, but not yet in the retained history.
//
// It is an ordered map. order is the sequence rows are drawn in and messages
// is what each row holds, and the two have to change together - a message
// removed from one but not the other is either a row with nothing to draw or a
// message nothing can reach. Keeping the pair behind one type is what stops
// that from being two statements a caller has to remember to write in the same
// place; before it was, and the removal path did the map delete inline while
// the order removal sat behind a separate method.
//
// The animation itself lives in the chat's animation.Queue. This type only
// tracks what is in flight, not how far along it is.
type activeReveals struct {
	order    []string
	messages map[string]youtube.Message
}

// add files a message as newly in flight, at the end of the draw order.
func (a *activeReveals) add(id string, message youtube.Message) {
	if a.messages == nil {
		a.messages = make(map[string]youtube.Message)
	}
	a.order = append(a.order, id)
	a.messages[id] = message
}

// take removes one reveal and returns the message it held, so a finished
// animation can be moved into history in a single step.
func (a *activeReveals) take(id string) (youtube.Message, bool) {
	message, ok := a.messages[id]
	if !ok {
		return youtube.Message{}, false
	}
	delete(a.messages, id)
	for i, existing := range a.order {
		if existing == id {
			a.order = append(a.order[:i], a.order[i+1:]...)
			break
		}
	}
	return message, true
}

// messageFor returns the message a reveal is drawing.
func (a *activeReveals) messageFor(id string) (youtube.Message, bool) {
	message, ok := a.messages[id]
	return message, ok
}

// ids returns the reveals in draw order.
func (a *activeReveals) ids() []string { return a.order }

// len is how many reveals are in flight.
func (a *activeReveals) len() int { return len(a.order) }

// drain empties the set and returns every message it held, in draw order, so
// they can be moved into history at once.
func (a *activeReveals) drain() []youtube.Message {
	if len(a.order) == 0 {
		a.clear()
		return nil
	}
	drained := make([]youtube.Message, 0, len(a.order))
	for _, id := range a.order {
		if message, ok := a.messages[id]; ok {
			drained = append(drained, message)
		}
	}
	a.clear()
	return drained
}

// update rewrites the message a reveal is drawing, for a redaction that lands
// while it is still animating.
func (a *activeReveals) update(id string, message youtube.Message) {
	if _, ok := a.messages[id]; !ok {
		return
	}
	a.messages[id] = message
}

// each visits every in-flight message. The id is passed because callers key
// rollback records by it.
func (a *activeReveals) each(visit func(id string, message youtube.Message)) {
	for _, id := range a.order {
		if message, ok := a.messages[id]; ok {
			visit(id, message)
		}
	}
}

// clear drops every reveal, returning the set to its zero state.
func (a *activeReveals) clear() {
	a.order = nil
	a.messages = make(map[string]youtube.Message)
}
