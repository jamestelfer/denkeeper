package tghtml

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// textBlock builds a Block of plain content, for chunker cases where what is in
// the block does not matter, only how long it is.
func textBlock(content string) Block { return Block{Content: content} }

// assertChunksValid applies the invariants every chunker test shares: no chunk
// over the limit, and every chunk tag-balanced. R19's balance check is asserted
// on every chunk in every test rather than on a sample, because the chunker's
// safety rests on it entirely.
func assertChunksValid(t *testing.T, chunks []string, limit int) {
	t.Helper()
	for i, c := range chunks {
		if len(c) > limit {
			t.Errorf("chunk %d is %d bytes, over the limit of %d: %q", i, len(c), limit, c)
		}
		if !utf8.ValidString(c) {
			t.Errorf("chunk %d is not valid UTF-8: %q", i, c)
		}
		assertTagBalanced(t, c)
	}
}

// TestChunk_PacksGreedilyWithinTheLimit covers R17, R20 and R21: while the
// active chunk has room for the next block it takes it, and when it does not a
// new chunk begins.
func TestChunk_PacksGreedilyWithinTheLimit(t *testing.T) {
	// Four blocks of ten bytes each. With the two-byte separator, two blocks fit
	// in 22 bytes and three do not.
	blocks := []Block{
		textBlock("aaaaaaaaaa"),
		textBlock("bbbbbbbbbb"),
		textBlock("cccccccccc"),
		textBlock("dddddddddd"),
	}

	chunks, err := Chunk(blocks, 22)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	want := []string{
		"aaaaaaaaaa\n\nbbbbbbbbbb",
		"cccccccccc\n\ndddddddddd",
	}
	if len(chunks) != len(want) {
		t.Fatalf("chunk count = %d, want %d: %q", len(chunks), len(want), chunks)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Errorf("chunk %d = %q, want %q", i, chunks[i], want[i])
		}
	}
	assertChunksValid(t, chunks, 22)
}

// TestChunk_ExactFitStaysInOneChunk is the boundary R21 turns on: a join that
// lands exactly on the limit still fits.
func TestChunk_ExactFitStaysInOneChunk(t *testing.T) {
	blocks := []Block{textBlock("aaaaaaaaaa"), textBlock("bbbbbbbbbb")}
	joined := Join(blocks)

	chunks, err := Chunk(blocks, len(joined))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunk count = %d, want 1 — an exact fit is a fit: %q", len(chunks), chunks)
	}
	if chunks[0] != joined {
		t.Errorf("chunk = %q, want %q", chunks[0], joined)
	}
	assertChunksValid(t, chunks, len(joined))
}

// TestChunk_OneOverTheLimitSplits is the other side of the same boundary.
func TestChunk_OneOverTheLimitSplits(t *testing.T) {
	blocks := []Block{textBlock("aaaaaaaaaa"), textBlock("bbbbbbbbbb")}
	limit := len(Join(blocks)) - 1

	chunks, err := Chunk(blocks, limit)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunk count = %d, want 2 — one byte over must split: %q", len(chunks), chunks)
	}
	assertChunksValid(t, chunks, limit)
}

// TestChunk_PreservesEverythingInSourceOrder covers R18 and R23 as one property,
// swept across every limit the input admits.
//
// Rejoining the chunks with the block separator must reproduce Join exactly:
// that is simultaneously "no block was reordered", "no block was dropped",
// "nothing was duplicated" and — because the content is multi-byte throughout —
// "no split landed inside a rune". A per-case assertion can pass on the one
// limit it happens to pick; this cannot.
func TestChunk_PreservesEverythingInSourceOrder(t *testing.T) {
	blocks := []Block{
		textBlock("ünïcödé blöck öne"),
		textBlock("日本語のブロック"),
		textBlock("emoji 🎉🎊 block"),
		textBlock("plain ascii block"),
		textBlock("mixed ünï 日本 🎉 block"),
	}
	joined := Join(blocks)

	widest := 0
	for _, b := range blocks {
		if b.Len() > widest {
			widest = b.Len()
		}
	}

	// Below the widest block every limit produces an oversized chunk, which is
	// this phase's documented interim behaviour and a separate case below.
	for limit := widest; limit <= len(joined)+1; limit++ {
		chunks, err := Chunk(blocks, limit)
		if err != nil {
			t.Fatalf("Chunk(limit=%d): %v", limit, err)
		}
		assertChunksValid(t, chunks, limit)
		if got := strings.Join(chunks, BlockSeparator); got != joined {
			t.Fatalf("limit=%d: rejoined chunks = %q, want %q", limit, got, joined)
		}
	}
}

// TestChunk_OversizedBlockGoesOutWholeAndAlone pins this phase's interim
// behaviour for a block that cannot fit on its own. Splitting it is a separate
// change; until then it must not be truncated, dropped, looped over, or packed
// with a neighbour.
func TestChunk_OversizedBlockGoesOutWholeAndAlone(t *testing.T) {
	huge := strings.Repeat("x", 100)
	blocks := []Block{textBlock("before"), textBlock(huge), textBlock("after")}

	chunks, err := Chunk(blocks, 20)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	want := []string{"before", huge, "after"}
	if len(chunks) != len(want) {
		t.Fatalf("chunk count = %d, want %d: %q", len(chunks), len(want), chunks)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Errorf("chunk %d = %q, want %q", i, chunks[i], want[i])
		}
	}
}

// TestChunk_OversizedBlockTerminatesAtAnyLimit is the non-termination guard. A
// packing loop that cannot place a block is the classic place to spin forever.
func TestChunk_OversizedBlockTerminatesAtAnyLimit(t *testing.T) {
	blocks := []Block{
		textBlock(strings.Repeat("a", 500)),
		textBlock("b"),
		textBlock(strings.Repeat("c", 500)),
	}
	for _, limit := range []int{1, 2, 7, 499, 500, 501, 1200} {
		chunks, err := Chunk(blocks, limit)
		if err != nil {
			t.Fatalf("Chunk(limit=%d): %v", limit, err)
		}
		if len(chunks) == 0 {
			t.Errorf("Chunk(limit=%d) produced no chunks — content was dropped", limit)
		}
		var total int
		for _, c := range chunks {
			total += len(c)
		}
		if total < 1001 {
			t.Errorf("Chunk(limit=%d) emitted %d bytes, want at least the 1001 bytes of content", limit, total)
		}
	}
}

// TestChunk_EmptyInputProducesNoChunks keeps the adapter from sending an empty
// message for a reply that rendered to nothing.
func TestChunk_EmptyInputProducesNoChunks(t *testing.T) {
	chunks, err := Chunk(nil, 100)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("chunks = %q, want none", chunks)
	}
}

// TestChunk_RejectsANonPositiveLimit is the total-function guard: a limit that
// nothing can fit into is a caller bug, not something to loop over.
func TestChunk_RejectsANonPositiveLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if _, err := Chunk([]Block{textBlock("x")}, limit); err == nil {
			t.Errorf("Chunk(limit=%d) returned no error", limit)
		}
	}
}
