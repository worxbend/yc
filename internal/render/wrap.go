package render

import "strings"

// This file is the line-wrapping engine: how a row of fragments becomes one or
// more terminal lines at a given width, with continuation rows hanging under
// the prefix.
//
// It is separate from message.go because the two change for unrelated reasons.
// message.go answers "what does a YouTube chat event look like" - badges,
// author colors, timestamps, Super Chat chips - and changes when YouTube adds
// an event kind or a badge rule. This answers "how does text break across a
// narrow terminal", and changes when the wrapping is wrong. Nothing here knows
// what a chat message is; it works on Fragments and Rows.

// minimumWrapColumns is the narrowest usable text column. Below it, wrapping
// stops producing readable lines, so the hanging indent gives way instead.
const minimumWrapColumns = 16

// wrap lays prefix and content out at width, hanging every continuation row
// under the prefix so wrapped text stays visually attached to its author.
func wrap(prefix, content []Fragment, width int) []Row {
	if width <= 0 {
		return nil
	}

	indentWidth := fragmentsWidth(prefix)
	// Cap the hanging indent so a continuation row always has room for whole
	// words. On a narrow terminal the prefix can be most of the row, and
	// hanging text under a 28-cell prefix at width 32 leaves four columns to
	// wrap into, which shreds prose into a vertical ribbon of syllables.
	if indentWidth > 0 && width-indentWidth < minimumWrapColumns {
		indentWidth = width - minimumWrapColumns
	}
	if indentWidth < 0 {
		indentWidth = 0
	}
	if indentWidth >= width {
		indentWidth = width / 2
	}

	w := newWrapper(width)
	w.add(prefix)
	w.setIndent(indentWidth)
	w.add(content)
	return w.finish()
}

// wrapper lays fragments out into rows of a fixed width. It owns the row still
// under construction and the columns that row has used, so a caller can hand it
// several fragment lists in turn without threading that state itself.
//
// indentWidth is the hanging indent continuation rows start at. Callers change
// it between passes: an author prefix wraps from column zero, and the message
// text that follows hangs underneath it.
type wrapper struct {
	rows        []Row
	current     Row
	used        int
	width       int
	indentWidth int
}

func newWrapper(width int) *wrapper {
	return &wrapper{rows: make([]Row, 0, 2), width: width}
}

// setIndent changes the hanging indent used from the next wrap onwards. The row
// being built is left alone - it was laid out under the previous indent.
func (w *wrapper) setIndent(indentWidth int) {
	w.indentWidth = indentWidth
}

// finish emits the row still under construction and returns every row. The
// wrapper is not usable afterwards.
func (w *wrapper) finish() []Row {
	return append(w.rows, w.current)
}

func (w *wrapper) breakRow(indentWidth int) {
	w.rows = append(w.rows, w.current)
	w.current = continuationRow(indentWidth)
	w.used = indentWidth
}

func (w *wrapper) add(fragments []Fragment) {
	width, indentWidth := w.width, w.indentWidth
	for _, fragment := range fragments {
		if fragment.WidthCells > 0 || isAtomicFragment(fragment) {
			fragmentWidth := fragment.Width()
			if fragmentWidth == 0 {
				continue
			}
			if w.used+fragmentWidth > width && w.used > indentWidth {
				w.breakRow(indentWidth)
			}
			if w.used+fragmentWidth > width && w.used == indentWidth && w.used > 0 && fragmentWidth <= width {
				// The fragment cannot fit beside the indent but would fit
				// on a full-width row, so give up the indent.
				//
				// The row being abandoned must be emitted first if it holds
				// anything real. On the content pass indentWidth is exactly
				// the prefix width, so used == indentWidth is also true on
				// the very first row - where the row holds the timestamp,
				// badges, and author name. Discarding it unconditionally
				// would drop the whole prefix whenever a message opened
				// with a long mention or an amount chip, leaving an
				// unattributed line in chat at ordinary terminal widths.
				if rowHasContent(w.current) {
					w.rows = append(w.rows, w.current)
				}
				w.current = Row{}
				w.used = 0
			}
			if w.used+fragmentWidth <= width {
				w.current.Append(fragment)
				w.used += fragmentWidth
				continue
			}
		}

		// Prefer a break between words. Chat is prose, and breaking mid-word
		// makes it materially harder to read at the speed a busy chat moves.
		// A word wider than the line still falls through to
		// cluster-by-cluster wrapping below, so nothing becomes unrenderable.
		for _, chunk := range wrapChunks(fragment.Text) {
			chunkWidth := textWidth(chunk)
			if chunkWidth > 0 && chunkWidth <= width-indentWidth &&
				w.used+chunkWidth > width && w.used > indentWidth &&
				strings.TrimSpace(chunk) != "" {
				w.breakRow(indentWidth)
			}
			w.addClusters(fragment, chunk)
		}
	}
}

// wrapChunks splits text into word-sized pieces, each a run of non-space
// clusters together with the spaces that follow it. Keeping the trailing spaces
// attached means a break taken before a chunk lands between words rather than
// stranding a space at the start of the next row.
func wrapChunks(text string) []string {
	if text == "" {
		return nil
	}
	var chunks []string
	var current strings.Builder
	inTrailingSpace := false
	for _, cluster := range graphemeStrings(text) {
		if cluster == "\n" {
			if current.Len() > 0 {
				chunks = append(chunks, current.String())
				current.Reset()
			}
			chunks = append(chunks, cluster)
			inTrailingSpace = false
			continue
		}
		isSpace := strings.TrimSpace(cluster) == ""
		if !isSpace && inTrailingSpace {
			chunks = append(chunks, current.String())
			current.Reset()
			inTrailingSpace = false
		}
		current.WriteString(cluster)
		if isSpace {
			inTrailingSpace = true
		}
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}
	return chunks
}

// addClusters is the fallback for text that will not fit a row whole: it lays
// the chunk out one grapheme cluster at a time, breaking wherever the row runs
// out of columns.
func (w *wrapper) addClusters(fragment Fragment, text string) {
	width, indentWidth := w.width, w.indentWidth
	for _, cluster := range graphemeStrings(text) {
		if cluster == "\n" {
			w.breakRow(indentWidth)
			continue
		}

		clusterWidth := textWidth(cluster)
		if w.used+clusterWidth > width && w.used > indentWidth {
			w.breakRow(indentWidth)
			if strings.TrimSpace(cluster) == "" {
				continue
			}
		}
		if w.used+clusterWidth > width && w.used == indentWidth && w.used > 0 {
			w.breakRow(0)
		}

		next := fragment
		next.Text = cluster
		next.WidthCells = 0
		w.current.Append(next)
		w.used += clusterWidth
	}
}

// isAtomicFragment reports the fragments that must move to the next row whole.
// A mention, an emoji, or a price chip split across a wrap stops being the
// thing it is; ordinary prose does not.
func isAtomicFragment(fragment Fragment) bool {
	switch fragment.Kind {
	case FragmentMention, FragmentEmojiFallback, FragmentShortcode, FragmentAmount, FragmentMembership:
		return true
	default:
		return false
	}
}

func continuationRow(indentWidth int) Row {
	if indentWidth <= 0 {
		return Row{}
	}
	return Row{Fragments: []Fragment{{
		Kind: FragmentText,
		Text: strings.Repeat(" ", indentWidth),
	}}}
}

// Append adds fragment to the row, merging it into the previous fragment when
// the two carry the same kind and style. Empty text is dropped: a fragment with
// nothing in it would only add a styled run nobody can see.
//
// Every builder of rows goes through here, in this package and in
// internal/animation, so the merge rule stays in one place.
func (r *Row) Append(fragment Fragment) {
	if fragment.Text == "" {
		return
	}
	lastIndex := len(r.Fragments) - 1
	if lastIndex >= 0 && mergeableFragments(r.Fragments[lastIndex], fragment) {
		r.Fragments[lastIndex].Text += fragment.Text
		return
	}
	r.Fragments = append(r.Fragments, fragment)
}

// coalesceAdjacent merges neighboring fragments that share a kind and style.
// Callers building fragment lists by hand use it once, at the end.
func coalesceAdjacent(in []Fragment) []Fragment {
	if len(in) == 0 {
		return nil
	}
	row := Row{Fragments: make([]Fragment, 0, len(in))}
	for _, fragment := range in {
		row.Append(fragment)
	}
	return row.Fragments
}

// mergeableFragments reports whether two neighbors can be merged into one
// styled run. Fixed-width fragments never merge: their reserved columns are the
// point, and concatenating two of them would collapse a column.
func mergeableFragments(a, b Fragment) bool {
	if a.WidthCells > 0 || b.WidthCells > 0 {
		return false
	}
	return a.Kind == b.Kind && a.Style == b.Style
}
