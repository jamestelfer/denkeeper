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
// limit is a parameter and never Telegram's 4096: the adapter owns that number
// and applies headroom to it. Length is the byte length of the raw HTML, which
// over-counts against Telegram's own limit (that one counts the entity-stripped
// text in UTF-16 code units). Over-counting is conservative — it costs an
// occasional extra chunk and needs no second parser.
//
// A single block longer than the limit is emitted alone and over-limit rather
// than truncated or dropped. Splitting one is a separate change; losing content
// silently is the failure class this package exists to remove, so the block goes
// out whole and the caller can see it is oversized.
func Chunk(blocks []Block, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("tghtml: chunk limit must be positive, got %d", limit)
	}

	var chunks []string
	var active strings.Builder
	active.Grow(limit)

	flush := func() {
		if active.Len() > 0 {
			chunks = append(chunks, active.String())
			active.Reset()
			active.Grow(limit)
		}
	}

	for _, b := range blocks {
		size := b.Len()
		if size == 0 {
			continue
		}
		if active.Len() > 0 {
			sep := separatorAfter(active.String(), BlockSeparator)
			if active.Len()+len(sep)+size > limit {
				flush()
			}
		}
		appendBlock(&active, b)
	}
	flush()

	return chunks, nil
}
