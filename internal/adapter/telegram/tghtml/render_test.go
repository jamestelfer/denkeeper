package tghtml

import (
	"errors"
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

// TestRender_ThematicBreakEmitsNoElement covers R2 and R5 at the point upstream
// breaks both: it writes "<hr" and then the separator, producing "<hr* * *" —
// an unterminated tag Telegram rejects outright. Telegram's subset has no hr, so
// the separator has to be text.
func TestRender_ThematicBreakEmitsNoElement(t *testing.T) {
	got := renderOne(t, "---")
	if strings.Contains(got, "<") {
		t.Errorf("Render(\"---\") = %q — must contain no < at all (upstream emitted %q)", got, "<hr* * *")
	}
	if got == "" {
		t.Error("a thematic break must render as something visible")
	}
	assertTagBalanced(t, got)
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
		t.Run("block"+string(rune('0'+i)), func(t *testing.T) {
			assertTagBalanced(t, b.HTML())
		})
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
