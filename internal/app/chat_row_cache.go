package app

import (
	"github.com/worxbend/yc/internal/render"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// chatRenderParams captures everything outside a single message that changes
// what render.Rows produces for it. Any difference invalidates the whole cache,
// because these change together and rarely - a resize, a theme swap, a layout
// toggle - and re-rendering the backlog once is cheaper than working out which
// messages a given change actually affected.
type chatRenderParams struct {
	width        int
	layout       render.LayoutMode
	badges       render.BadgeMode
	highlight    bool
	fullUsername bool
	showAvatars  bool
	palette      theme.Palette
}

// chatRowCacheLimit bounds the entry map. The backlog is capped per chat, but
// switching chats and trimming scrollback both leave entries behind, so the map
// is cleared wholesale once it outgrows a few chats' worth of history.
const chatRowCacheLimit = 8192

type chatRowCacheEntry struct {
	continuesGroup bool
	deleted        bool
	meta           render.AuthorMeta
	rows           []render.Row
}

// chatRowCache memoizes render.Rows per message.
//
// Rendering the backlog is the dominant cost of a repaint and the same messages
// render identically frame after frame: only the newest message and anything
// still animating actually change. Without this, frame time grows linearly with
// retained history, which on a busy YouTube chat is exactly when the terminal
// can least afford it.
//
// shellModel is copied by value on every Update, so the cache is held behind a
// pointer that every copy shares; it would otherwise be discarded on each
// keystroke. Sharing is safe because Bubble Tea runs Update and View on a
// single goroutine.
type chatRowCache struct {
	params  chatRenderParams
	entries map[string]chatRowCacheEntry
}

func newChatRowCache() *chatRowCache {
	return &chatRowCache{entries: make(map[string]chatRowCacheEntry)}
}

// rows returns cached rows for message, calling renderRows on a miss.
//
// Messages without a stable ID bypass the cache: local echoes and system
// notices reuse or omit IDs, so keying on one risks serving another message's
// rows. meta is compared in full because author context changes as the roster
// learns more, and because its Now field is quantized by the caller to the
// granularity the renderer actually displays.
func (c *chatRowCache) rows(
	message youtube.Message,
	continuesGroup bool,
	meta render.AuthorMeta,
	params chatRenderParams,
	renderRows func() []render.Row,
) []render.Row {
	if c == nil || message.ID == "" {
		return renderRows()
	}
	if c.entries == nil || c.params != params {
		c.params = params
		c.entries = make(map[string]chatRowCacheEntry)
	}
	if entry, ok := c.entries[message.ID]; ok &&
		entry.continuesGroup == continuesGroup &&
		entry.deleted == message.Deleted &&
		entry.meta == meta {
		return entry.rows
	}
	rows := renderRows()
	if len(c.entries) >= chatRowCacheLimit {
		c.entries = make(map[string]chatRowCacheEntry)
	}
	c.entries[message.ID] = chatRowCacheEntry{
		continuesGroup: continuesGroup,
		deleted:        message.Deleted,
		meta:           meta,
		rows:           rows,
	}
	return rows
}
