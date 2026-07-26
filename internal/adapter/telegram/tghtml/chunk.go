package tghtml

import (
	"fmt"
	"strings"
)

// Chunk packs blocks into ordered chunk strings, each no longer than limit and
// each independently tag-balanced.
//
// Packing is greedy: while the active chunk has room for the next block it takes
// it, and when appending would exceed the limit a new chunk begins. Source order
// is preserved — a reordered reply is worse than an unchunked one.
//
// A block too large to fit on its own is split. Because a block is a token
// sequence rather than a string, the split is taken inside a text token and the
// elements enclosing it are closed at the end of one chunk and reopened at the
// start of the next. Tag balance is therefore a property of how a chunk is
// built, not something checked afterwards: a chunk always ends by closing
// whatever is open and begins by reopening it.
//
// limit is a parameter and never Telegram's 4096: the adapter owns that number
// and applies headroom to it. Length is the byte length of the raw HTML, which
// over-counts against Telegram's own limit (that one counts the entity-stripped
// text in UTF-16 code units). Over-counting is conservative — it costs an
// occasional extra chunk and needs no second parser.
func Chunk(blocks []Block, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("tghtml: chunk limit must be positive, got %d", limit)
	}

	c := chunker{limit: limit}
	for _, b := range blocks {
		c.addBlock(b)
	}
	c.flush()
	return c.chunks, nil
}

// chunker accumulates tokens into size-bounded, tag-balanced chunks.
type chunker struct {
	limit  int
	chunks []string

	cur   strings.Builder
	open  []token // elements currently open, outermost first
	tail  string  // trailing run of newlines in cur, for separator collapsing
	empty bool    // true until cur holds something other than reopened tags
}

// addBlock appends one block, starting a new chunk in preference to splitting
// the block when the block would fit in a chunk of its own.
func (c *chunker) addBlock(b Block) {
	size := b.Len()
	if size == 0 {
		return
	}

	if !c.empty && c.cur.Len() > 0 {
		sep := c.separator(BlockSeparator)
		// Break between blocks rather than inside one whenever the block could
		// start a chunk of its own — a split mid-paragraph is a worse read than
		// a message boundary at a paragraph break.
		if c.used()+len(sep)+size > c.limit && size <= c.limit {
			c.flush()
		} else {
			c.writeText(sep)
		}
	}

	for _, t := range b.tokens {
		c.add(t)
	}
}

// add appends one token, wrapping to a new chunk or splitting the token's text
// when it does not fit.
func (c *chunker) add(t token) {
	if !t.isText() {
		if c.used()+t.size()+c.closeCost(t) > c.limit && c.hasContent() {
			c.wrap()
		}
		c.writeTag(t)
		return
	}

	text := t.text
	for text != "" {
		room := c.limit - c.used()
		cut := textCut(text, room)
		if cut == 0 {
			if !c.hasContent() {
				// A fresh chunk cannot hold even the smallest indivisible piece
				// once the reopened elements are paid for. Emitting nothing would
				// loop forever, and emitting part of a rune or part of an entity
				// is not a thing that can be sent, so the chunk goes over the
				// limit rather than the content being lost.
				cut = minCut(text)
			} else {
				c.wrap()
				continue
			}
		}
		c.writeText(text[:cut])
		text = text[cut:]
		if text != "" {
			c.wrap()
		}
	}
}

// used is the emitted length of the active chunk plus what it will cost to close
// the elements currently open. A chunk is only ever completed by closing them,
// so that cost is part of the budget from the moment they are opened.
func (c *chunker) used() int { return c.cur.Len() + c.closeCost(token{}) }

// closeCost is the cost of closing everything open, plus extra if it is an
// opening tag about to be added.
func (c *chunker) closeCost(extra token) int {
	n := 0
	for _, t := range c.open {
		n += t.closer().size()
	}
	if !extra.isText() && extra.open {
		n += extra.closer().size()
	}
	return n
}

// hasContent reports whether the active chunk holds anything beyond the elements
// reopened at its start. Wrapping an empty chunk would emit an empty message and
// make no progress.
func (c *chunker) hasContent() bool { return !c.empty }

func (c *chunker) writeText(s string) {
	if s == "" {
		return
	}
	c.cur.WriteString(s)
	c.empty = false
	if trimmed := strings.TrimRight(s, "\n"); trimmed == "" {
		c.tail += s
	} else {
		c.tail = s[len(trimmed):]
	}
}

func (c *chunker) writeTag(t token) {
	c.cur.WriteString(t.emitted())
	c.tail = ""
	c.empty = false
	if t.open {
		c.open = append(c.open, t)
		return
	}
	if k := len(c.open) - 1; k >= 0 {
		c.open = c.open[:k]
	}
}

// separator returns the part of sep the active chunk does not already end with,
// matching how blocks are joined when they are not chunked.
func (c *chunker) separator(sep string) string {
	trailing := len(c.tail)
	if trailing > len(sep) {
		trailing = len(sep)
	}
	return sep[trailing:]
}

// wrap ends the active chunk and begins the next one with the same elements
// open, so content that spans a boundary stays inside its formatting.
func (c *chunker) wrap() {
	reopen := c.open
	c.flush()
	for _, t := range reopen {
		c.cur.WriteString(t.emitted())
		c.open = append(c.open, t)
	}
	// The reopened tags are not content: a chunk holding only them is still
	// empty, and wrapping again must not emit it.
	c.empty = true
	c.tail = ""
}

// flush emits the active chunk, closing everything still open.
func (c *chunker) flush() {
	for i := len(c.open) - 1; i >= 0; i-- {
		c.cur.WriteString(c.open[i].closer().emitted())
	}
	c.open = c.open[:0]
	if !c.empty && c.cur.Len() > 0 {
		c.chunks = append(c.chunks, c.cur.String())
	}
	c.cur.Reset()
	c.cur.Grow(c.limit)
	c.empty = true
	c.tail = ""
}
