package tghtml

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// bugReportSource is the message from the defect that motivated this package.
// Telegram's legacy Markdown parser has no intraword-emphasis rule, so it
// consumes the underscore in each URL as an emphasis delimiter, italicises the
// text between them and leaves both autolinked destinations unresolvable.
//
// CommonMark does not treat an intraword underscore as emphasis, so parsing
// locally makes the defect disappear at the root. This is the regression test
// the whole package exists to keep passing.
const bugReportSource = "See https://example.com/a_b and https://example.com/c_d"

func TestRender_UnderscoreBearingURLsSurviveByteForByte(t *testing.T) {
	blocks, err := Render([]byte(bugReportSource))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("block count = %d, want 1", len(blocks))
	}

	got := blocks[0].HTML()
	// Bare URLs are plain text under CommonMark — nothing autolinks them here,
	// and Telegram autolinks the visible text after parsing. The requirement is
	// that the destination survives unaltered, not that we wrap it.
	if got != bugReportSource {
		t.Errorf("Render() = %q, want byte-identical input %q", got, bugReportSource)
	}
	for _, url := range []string{"https://example.com/a_b", "https://example.com/c_d"} {
		if !contains(got, url) {
			t.Errorf("output is missing %q: %q", url, got)
		}
	}
}

// renderOne renders src and requires it to produce exactly one block.
func renderOne(t *testing.T, src string) string {
	t.Helper()
	blocks, err := Render([]byte(src))
	if err != nil {
		t.Fatalf("Render(%q): %v", src, err)
	}
	if len(blocks) != 1 {
		t.Fatalf("Render(%q): block count = %d, want 1", src, len(blocks))
	}
	return blocks[0].HTML()
}

// TestRender_EscapesMarkupCharactersInText covers R3. Telegram documents these
// three characters as the ones needing escapes in HTML parse mode.
func TestRender_EscapesMarkupCharactersInText(t *testing.T) {
	got := renderOne(t, "a < b & c > d")
	want := "a &lt; b &amp; c &gt; d"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

// TestRender_LeavesDoubleQuoteLiteral pins the decision to escape exactly the
// three documented characters. goldmark's own escaper emits &quot;, which
// Telegram does not document as supported, so it is restored to a literal.
func TestRender_LeavesDoubleQuoteLiteral(t *testing.T) {
	got := renderOne(t, `she said "hello"`)
	want := `she said "hello"`
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

// TestRender_SourceEscapedAmpersandEntitySurvives guards the &quot; restoration
// against corrupting an ampersand the source escaped itself.
func TestRender_SourceEscapedAmpersandEntitySurvives(t *testing.T) {
	got := renderOne(t, "&amp;quot;")
	want := "&amp;quot;"
	if got != want {
		t.Errorf("Render() = %q, want %q — the entity must not be decoded to a quote", got, want)
	}
}

// TestRender_PreservesLiteralMarkdownPunctuation covers R4. These are the
// characters Telegram's legacy Markdown parser consumes as markup; the whole
// point of rendering HTML is that they travel as themselves. In particular no
// backslash may appear in front of any of them.
func TestRender_PreservesLiteralMarkdownPunctuation(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"intraword underscores", "snake_case_identifier", "snake_case_identifier"},
		{"asterisk in arithmetic", "2 * 3 = 6", "2 * 3 = 6"},
		{"unmatched bracket", "array[0] is first", "array[0] is first"},
		{"lone backtick", "a ` backtick", "a ` backtick"},
		{"backslash-escaped underscore", `\_not italic\_`, "_not italic_"},
		{"backslash-escaped asterisk", `\*not bold\*`, "*not bold*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderOne(t, tc.src)
			if got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.src, got, tc.want)
			}
			if indexOf(got, `\`) >= 0 {
				t.Errorf("Render(%q) = %q — output must contain no backslash escape", tc.src, got)
			}
		})
	}
}

func TestRender_Emphasis(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"single asterisk is italic", "*italic*", "<i>italic</i>"},
		{"single underscore is italic", "_italic_", "<i>italic</i>"},
		{"double asterisk is bold", "**bold**", "<b>bold</b>"},
		{"nested emphasis", "**bold with *italic* inside**", "<b>bold with <i>italic</i> inside</b>"},
		{"emphasis in prose", "a *b* c", "a <i>b</i> c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderOne(t, tc.src); got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

func TestRender_CodeSpan(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"simple span", "use `go test` here", "use <code>go test</code> here"},
		{"span preserves underscores", "`snake_case`", "<code>snake_case</code>"},
		{"span escapes markup", "`a < b`", "<code>a &lt; b</code>"},
		{"span keeps quotes literal", "`os.Getenv(\"HOME\")`", "<code>os.Getenv(\"HOME\")</code>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderOne(t, tc.src); got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

// TestRender_CodeSpanWithNonTextChildDoesNotPanic guards the unchecked
// c.(*ast.Text) assertion carried by upstream. A code span containing a raw
// newline puts a non-Text child in the span, and a panic here would happen
// inside the adapter's send path on user-supplied content.
func TestRender_CodeSpanWithNonTextChildDoesNotPanic(t *testing.T) {
	for _, src := range []string{
		"`multi\nline span`",
		"`span with ` + \"`\" + ` backtick`",
		"``a ` b``",
		"`  padded  `",
	} {
		blocks, err := Render([]byte(src))
		if err != nil {
			t.Fatalf("Render(%q): unexpected error %v", src, err)
		}
		if len(blocks) == 0 {
			t.Errorf("Render(%q) produced no blocks", src)
		}
	}
}

func TestRender_LinkWithAllowedScheme(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"https", "[label](https://example.com/p)", `<a href="https://example.com/p">label</a>`},
		{"http", "[label](http://example.com)", `<a href="http://example.com">label</a>`},
		{"mailto", "[mail](mailto:a@example.com)", `<a href="mailto:a@example.com">mail</a>`},
		{"tg", "[chat](tg://user?id=1)", `<a href="tg://user?id=1">chat</a>`},
		{
			"destination with underscore survives",
			"[doc](https://example.com/a_b)",
			`<a href="https://example.com/a_b">doc</a>`,
		},
		{
			"uppercase scheme is allowed",
			"[label](HTTPS://example.com)",
			`<a href="HTTPS://example.com">label</a>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderOne(t, tc.src); got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

// TestRender_LinkWithRejectedSchemeDropsTheAnchor closes the case-sensitivity
// bypass measured in upstream: its denylist compares lowercase prefixes without
// folding, so JaVaScRiPt: went straight into an href. The allowlist replaces it
// on arrival rather than surviving into a commit.
//
// Upstream also kept the anchor and blanked the href. The label must carry no
// anchor at all, and the destination must not appear anywhere in the output —
// Telegram autolinks plain text after parsing, so a destination left visible
// could be turned back into a link.
func TestRender_LinkWithRejectedSchemeDropsTheAnchor(t *testing.T) {
	cases := []struct {
		name string
		src  string
		dest string
	}{
		{"javascript", "[click](javascript:alert(1))", "javascript:alert(1)"},
		{"javascript case varied", "[click](JaVaScRiPt:alert(1))", "JaVaScRiPt:alert(1)"},
		{"data", "[click](data:text/html;base64,AAAA)", "data:text/html"},
		{"vbscript", "[click](vbscript:msgbox)", "vbscript:msgbox"},
		{"file", "[click](file:///etc/passwd)", "file:///etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderOne(t, tc.src)
			if got != "click" {
				t.Errorf("Render(%q) = %q, want the bare label %q", tc.src, got, "click")
			}
			if indexOf(got, "<a") >= 0 {
				t.Errorf("Render(%q) = %q — must emit no anchor element", tc.src, got)
			}
			if indexOf(got, tc.dest) >= 0 {
				t.Errorf("Render(%q) = %q — destination %q must not reach the output", tc.src, got, tc.dest)
			}
		})
	}
}

// TestRender_RawHTMLFailsClosed covers R15. Raw HTML is the only fail-closed
// case in this package: it has no representable form in Telegram's subset, and
// forwarding it would let the source inject markup we never validated.
func TestRender_RawHTMLFailsClosed(t *testing.T) {
	for _, src := range []string{
		"<div>a block of html</div>",
		"text with an inline <span>element</span> in it",
		"<script>alert(1)</script>",
		"<br>",
	} {
		blocks, err := Render([]byte(src))
		if err == nil {
			t.Errorf("Render(%q) = %v, want an error", src, blocks)
			continue
		}
		if !errors.Is(err, ErrRawHTML) {
			t.Errorf("Render(%q) error = %v, want ErrRawHTML", src, err)
		}
		if !strings.Contains(err.Error(), "raw HTML") {
			t.Errorf("Render(%q) error = %q, must name the construct", src, err)
		}
		if blocks != nil {
			t.Errorf("Render(%q) returned %v alongside the error, want nil", src, blocks)
		}
	}
}

// TestRender_ImageErrorIsDistinguishableFromRawHTML covers the error split.
// Upstream returns one shared errTagNotAllowed for raw HTML and images, so the
// adapter cannot tell a must-fail-closed case from a should-degrade one, and
// R15's obligation to name the construct is unmet. Degrading images is a later
// phase, and it needs this split to exist first.
func TestRender_ImageErrorIsDistinguishableFromRawHTML(t *testing.T) {
	_, err := Render([]byte("![alt text](https://example.com/i.png)"))
	if err == nil {
		t.Fatal("Render of an image: want an error at this phase")
	}
	if !errors.Is(err, ErrImage) {
		t.Errorf("error = %v, want ErrImage", err)
	}
	if errors.Is(err, ErrRawHTML) {
		t.Error("the image error must not be indistinguishable from the raw-HTML error")
	}
	if !strings.Contains(err.Error(), "image") {
		t.Errorf("error = %q, must name the construct", err)
	}
}

// TestRender_ParagraphsBecomeSeparateBlocks covers R5's block boundary: one
// top-level markdown node produces at most one block.
func TestRender_ParagraphsBecomeSeparateBlocks(t *testing.T) {
	blocks, err := Render([]byte("first para\n\nsecond para\n\nthird para"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("block count = %d, want 3", len(blocks))
	}
	for i, want := range []string{"first para", "second para", "third para"} {
		if got := blocks[i].HTML(); got != want {
			t.Errorf("block %d = %q, want %q", i, got, want)
		}
	}
}

// TestRender_HeadingIsBold covers Telegram's lack of a heading element.
func TestRender_HeadingIsBold(t *testing.T) {
	for _, src := range []string{"# Title", "## Title", "###### Title"} {
		if got := renderOne(t, src); got != "<b>Title</b>" {
			t.Errorf("Render(%q) = %q, want %q", src, got, "<b>Title</b>")
		}
	}
}

// TestRender_FencedCodeBlockCarriesWrappers covers the Block shape the chunker
// depends on: a splittable block keeps its enclosing elements out of Content so
// they can be closed and reopened across a split.
func TestRender_FencedCodeBlockCarriesWrappers(t *testing.T) {
	blocks, err := Render([]byte("```go\nfmt.Println(\"hi\")\n```"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("block count = %d, want 1", len(blocks))
	}
	b := blocks[0]

	if len(b.Wrappers) != 2 {
		t.Fatalf("wrappers = %v, want two (pre outside, code inside)", b.Wrappers)
	}
	if b.Wrappers[0].Open != "<pre>" || b.Wrappers[0].Close != "</pre>" {
		t.Errorf("outer wrapper = %+v, want <pre>", b.Wrappers[0])
	}
	if b.Wrappers[1].Open != `<code class="language-go">` {
		t.Errorf("inner wrapper open = %q, want the language class", b.Wrappers[1].Open)
	}
	if b.Content != "fmt.Println(\"hi\")\n" {
		t.Errorf("Content = %q, want the code with no enclosing tags", b.Content)
	}
	want := "<pre><code class=\"language-go\">fmt.Println(\"hi\")\n</code></pre>"
	if got := b.HTML(); got != want {
		t.Errorf("HTML() = %q, want %q", got, want)
	}
}

// TestRender_CodeBlockContentIsEscaped guards against code fences becoming an
// injection vector: a < inside a fence is content, not markup.
func TestRender_CodeBlockContentIsEscaped(t *testing.T) {
	blocks, err := Render([]byte("```\n<script>alert(1)</script>\n```"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := blocks[0].HTML()
	if strings.Contains(got, "<script") {
		t.Errorf("HTML() = %q — the fence content must be escaped, not emitted as markup", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("HTML() = %q, want the escaped script tag", got)
	}
}

// TestRender_ThematicBreakEmitsNoElement covers R7, and R2 and R5 at the point
// upstream breaks both: it writes "<hr" and then a separator, producing the
// unterminated "<hr* * *" that Telegram rejects outright. Telegram's subset has
// no hr element, so the separator has to be text.
//
// A run of em dashes is chosen over upstream's "* * *", which reads as unrendered
// markdown source rather than as a rule; em dashes abut into a continuous line.
func TestRender_ThematicBreakEmitsNoElement(t *testing.T) {
	for _, src := range []string{"---", "***", "___", "- - -"} {
		got := renderOne(t, src)
		if strings.Contains(got, "<") {
			t.Errorf("Render(%q) = %q — must contain no < at all (upstream emitted %q)", src, got, "<hr* * *")
		}
		if got != thematicBreakText {
			t.Errorf("Render(%q) = %q, want %q", src, got, thematicBreakText)
		}
		assertTagBalanced(t, got)
	}
}

// TestRender_ThematicBreakSeparatorIsPinned fixes the chosen bytes so the
// decision cannot drift silently. Whether it looks like a rule on a given client
// is an appearance question for system testing.
func TestRender_ThematicBreakSeparatorIsPinned(t *testing.T) {
	if thematicBreakText != "——————————" {
		t.Errorf("thematicBreakText = %q, want ten em dashes", thematicBreakText)
	}
	if strings.ContainsAny(thematicBreakText, "<>&") {
		t.Errorf("thematicBreakText = %q must not contain a character that needs escaping", thematicBreakText)
	}
}

// TestRender_NoConstructIsSilentlyDropped is the content-preservation guard. A
// renderer that quietly skips a node kind loses user content with no error and
// no fallback, which is the failure class this whole package exists to remove.
func TestRender_NoConstructIsSilentlyDropped(t *testing.T) {
	cases := map[string]struct {
		src  string
		want string // a substring that must survive
	}{
		"bullet list":     {"- alpha\n- beta", "alpha"},
		"ordered list":    {"1. alpha\n2. beta", "alpha"},
		"nested list":     {"- parent\n    - child", "child"},
		"block quote":     {"> quoted", "quoted"},
		"indented code":   {"    indented code", "indented code"},
		"setext heading":  {"Title\n=====", "Title"},
		"loose list item": {"- alpha\n\n- beta", "beta"},
		"table syntax":    {"| a | b |\n|---|---|\n| 1 | 2 |", "a"},
		"strikethrough":   {"~~struck~~", "struck"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			blocks, err := Render([]byte(tc.src))
			if err != nil {
				t.Fatalf("Render(%q): %v", tc.src, err)
			}
			var all strings.Builder
			for _, b := range blocks {
				all.WriteString(b.HTML())
				all.WriteString("\n")
			}
			if !strings.Contains(all.String(), tc.want) {
				t.Errorf("Render(%q) = %q, must retain %q", tc.src, all.String(), tc.want)
			}
		})
	}
}

// TestRender_EveryBlockIsTagBalanced covers R5 across the whole construct set.
func TestRender_EveryBlockIsTagBalanced(t *testing.T) {
	src := strings.Join([]string{
		"# Heading",
		"",
		"Prose with *italic*, **bold**, `code` and a [link](https://example.com/a_b).",
		"",
		"- bullet one",
		"- bullet two",
		"",
		"1. first",
		"2. second",
		"",
		"> a quotation",
		"",
		"---",
		"",
		"```go",
		"if a < b { return }",
		"```",
		"",
		"A rejected [link](javascript:alert(1)) and an escaped & ampersand.",
	}, "\n")

	blocks, err := Render([]byte(src))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(blocks) == 0 {
		t.Fatal("no blocks rendered")
	}
	for i, b := range blocks {
		t.Run("block"+strconv.Itoa(i), func(t *testing.T) {
			assertTagBalanced(t, b.HTML())
		})
	}
}

// TestRender_SoftLineBreakEmitsNewline covers R6.
//
// Measured before the fix: "alpha beta\ngamma delta" rendered as
// "alpha betagamma delta" — the last word of one source line fused to the first
// word of the next. LLM output is wrapped prose, so this corrupted a large share
// of ordinary replies.
func TestRender_SoftLineBreakEmitsNewline(t *testing.T) {
	const src = "alpha beta\ngamma delta"
	got := renderOne(t, src)
	if got != src {
		t.Errorf("Render(%q) = %q, want %q", src, got, src)
	}
	if strings.Contains(got, "betagamma") {
		t.Errorf("Render(%q) = %q — words fused across the line break", src, got)
	}
}

// TestRender_HardLineBreakEmitsNewline covers R34.
//
// Measured before the fix: "alpha␠␠\ngamma" rendered as "alphagamma". R6 covered
// only the soft case, but the failure and the fix are identical.
func TestRender_HardLineBreakEmitsNewline(t *testing.T) {
	for _, src := range []string{"alpha  \ngamma", "alpha\\\ngamma"} {
		got := renderOne(t, src)
		if got != "alpha\ngamma" {
			t.Errorf("Render(%q) = %q, want %q", src, got, "alpha\ngamma")
		}
	}
}

// TestRender_WrappedProseNeverFusesWords is the realistic form of R6 and R34.
// A per-construct test can pass while ordinary wrapped prose still breaks, so
// this renders a multi-paragraph reply wrapped at 80 columns and checks every
// source line boundary.
func TestRender_WrappedProseNeverFusesWords(t *testing.T) {
	src := strings.Join([]string{
		"The quick brown fox jumps over the lazy dog and continues running until it",
		"reaches the far side of the meadow where the grass grows tall and green in",
		"the summer sunshine without any interruption whatsoever from the weather.",
		"",
		"A second paragraph follows the first one here so that the block boundary is",
		"exercised alongside the soft line breaks inside each individual paragraph.",
	}, "\n")

	blocks, err := Render([]byte(src))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := Join(blocks)

	// Every word in the source must appear as a whole word in the output.
	for _, word := range strings.Fields(src) {
		if !containsWord(got, word) {
			t.Errorf("word %q is not present as a whole word in %q", word, got)
		}
	}
	// And the source's line count must survive.
	if wantLines, gotLines := strings.Count(src, "\n"), strings.Count(got, "\n"); gotLines != wantLines {
		t.Errorf("newline count = %d, want %d — a line break was dropped", gotLines, wantLines)
	}
}

// containsWord reports whether s contains word delimited by whitespace or the
// string boundary, which is what catches a fused pair like "dogA".
func containsWord(s, word string) bool {
	for _, field := range strings.Fields(s) {
		if strings.Trim(field, ".,;:") == strings.Trim(word, ".,;:") {
			return true
		}
	}
	return false
}

// TestRender_BlockquoteEmitsElement covers R8, and both of its defects.
//
// Measured before the fix: "> line one\n> line two" rendered as
// "&gt; line oneline two" — a literal &gt; instead of the supported element, and
// the quote's internal line break dropped through the same path as R6. Fixing
// only the visible one leaves the quotation's lines fused.
func TestRender_BlockquoteEmitsElement(t *testing.T) {
	got := renderOne(t, "> line one\n> line two")

	if !strings.HasPrefix(got, "<blockquote>") || !strings.HasSuffix(got, "</blockquote>") {
		t.Errorf("Render() = %q, want a blockquote element", got)
	}
	if strings.Contains(got, "&gt;") {
		t.Errorf("Render() = %q — must not emit a literal &gt; quote marker", got)
	}
	if got != "<blockquote>line one\nline two</blockquote>" {
		t.Errorf("Render() = %q, want the two lines separated by a newline", got)
	}
	assertTagBalanced(t, got)
}

func TestRender_BlockquoteWithMultipleParagraphs(t *testing.T) {
	got := renderOne(t, "> first para\n>\n> second para")
	want := "<blockquote>first para\n\nsecond para</blockquote>"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
	assertTagBalanced(t, got)
}

// TestRender_NestedBlockquoteIsFlattened records the nesting decision. Telegram
// represents a quotation as a message entity over a text range, and a blockquote
// entity cannot contain another, so a nested quote collapses to one level rather
// than emitting markup Telegram would reject.
func TestRender_NestedBlockquoteIsFlattened(t *testing.T) {
	got := renderOne(t, "> outer\n> > inner")

	if n := strings.Count(got, "<blockquote>"); n != 1 {
		t.Errorf("Render() = %q — want exactly one blockquote element, got %d", got, n)
	}
	if !strings.Contains(got, "outer") || !strings.Contains(got, "inner") {
		t.Errorf("Render() = %q — both quote levels' text must survive", got)
	}
	assertTagBalanced(t, got)
}

// TestRender_NestedListIndents covers R9.
//
// Measured before the fix: "- parent\n    - child" rendered as
// "- parent\n- child" — upstream writes a constant "- " at every depth, so the
// nesting was flattened and the structure lost.
//
// Telegram's HTML subset has no list elements, so the indent must be textual.
// Ordinary spaces are used: Telegram's HTML parse mode is not an HTML renderer —
// tags are parsed into message entities over the text and the text is kept
// verbatim, which is the same property the newline fixes above rely on and which
// the documented blockquote example demonstrates by putting literal newlines
// inside the element.
func TestRender_NestedListIndents(t *testing.T) {
	got := renderOne(t, "- parent\n    - child")
	want := "- parent\n  - child"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

func TestRender_NestedListIndentsPerLevel(t *testing.T) {
	src := "- one\n    - two\n        - three"
	got := renderOne(t, src)
	want := "- one\n  - two\n    - three"
	if got != want {
		t.Errorf("Render(%q) = %q, want %q", src, got, want)
	}

	// Nesting depth must be recoverable from the output.
	for i, line := range strings.Split(got, "\n") {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if want := i * len(listIndent); indent != want {
			t.Errorf("line %d %q has indent %d, want %d", i, line, indent, want)
		}
	}
}

// TestRender_OrderedListKeepsOrdinals covers R35.
//
// Measured before the fix: "1. first\n2. second" rendered as
// "- first\n- second" — numbered steps silently became bullets.
func TestRender_OrderedListKeepsOrdinals(t *testing.T) {
	got := renderOne(t, "1. first\n2. second\n3. third")
	want := "1. first\n2. second\n3. third"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

// TestRender_OrderedListHonoursNonOneStart guards against a fix that simply
// counts items from the top, which would silently renumber the list.
func TestRender_OrderedListHonoursNonOneStart(t *testing.T) {
	got := renderOne(t, "5. five\n6. six")
	want := "5. five\n6. six"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

func TestRender_NestedOrderedListInsideBullet(t *testing.T) {
	got := renderOne(t, "- parent\n    1. first\n    2. second")
	want := "- parent\n  1. first\n  2. second"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

// KitchenSinkSource contains every construct fixed in this phase, together, so
// the fixes are shown not to interfere with each other. Exported for the adapter
// tests, which send this same document through the send path.
const KitchenSinkSource = "Wrapped prose that runs across\ntwo source lines here.\n\n" +
	"A hard break line  \nfollows immediately.\n\n" +
	"---\n\n" +
	"> quoted line one\n> quoted line two\n\n" +
	"- parent bullet\n    - child bullet\n\n" +
	"1. first step\n2. second step\n"

// KitchenSinkHTML is the expected rendering of KitchenSinkSource.
const KitchenSinkHTML = "Wrapped prose that runs across\ntwo source lines here." +
	"\n\n" + "A hard break line\nfollows immediately." +
	"\n\n" + "——————————" +
	"\n\n" + "<blockquote>quoted line one\nquoted line two</blockquote>" +
	"\n\n" + "- parent bullet\n  - child bullet" +
	"\n\n" + "1. first step\n2. second step"

// TestRender_KitchenSink covers R6, R7, R8, R9, R34 and R35 in one document.
// Each construct has its own case above; this one exists because a per-construct
// fix can pass while two constructs in sequence still interfere.
func TestRender_KitchenSink(t *testing.T) {
	blocks, err := Render([]byte(KitchenSinkSource))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := Join(blocks)
	if got != KitchenSinkHTML {
		t.Errorf("Render() mismatch\n got: %q\nwant: %q", got, KitchenSinkHTML)
	}
	for i, b := range blocks {
		assertTagBalanced(t, b.HTML())
		if b.Len() == 0 {
			t.Errorf("block %d is empty", i)
		}
	}
}

// contains is strings.Contains, spelled out to keep the failure messages above
// readable without an import that later cases do not need.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
