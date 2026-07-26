package tghtml

import (
	"strings"
	"testing"
)

// telegramTags is the set of elements Telegram's HTML parse mode accepts that
// this renderer emits. Telegram documents a closed list; anything outside it is
// rejected by their parser, which would route the whole message to the
// plain-text fallback.
//
// Kept deliberately narrow: constructs Telegram supports but the agent does not
// emit (tg-spoiler, tg-emoji) are absent until something needs them.
var telegramTags = map[string]bool{
	"b":          true,
	"i":          true,
	"u":          true,
	"s":          true,
	"a":          true,
	"code":       true,
	"pre":        true,
	"blockquote": true,
}

// assertTagBalanced fails unless every element opened in html is closed within
// html, in the correct order, and every tag name is one Telegram accepts.
//
// This is the R5 invariant, and the chunker's safety rests on it entirely: a
// chunker cannot defend itself against a renderer that emits an unclosed
// element. The same helper is applied to every chunk the chunker emits.
func assertTagBalanced(t *testing.T, html string) {
	t.Helper()

	var stack []string
	for i := 0; i < len(html); i++ {
		if html[i] != '<' {
			continue
		}
		end := strings.IndexByte(html[i:], '>')
		if end < 0 {
			t.Errorf("unterminated tag at offset %d in %q", i, html)
			return
		}
		tag := html[i+1 : i+end]
		i += end

		closing := strings.HasPrefix(tag, "/")
		tag = strings.TrimPrefix(tag, "/")
		// Drop any attributes; only the element name matters for balance.
		if sp := strings.IndexByte(tag, ' '); sp >= 0 {
			tag = tag[:sp]
		}
		if tag == "" {
			t.Errorf("empty tag name at offset %d in %q", i, html)
			return
		}
		if !telegramTags[tag] {
			t.Errorf("tag %q is not in Telegram's accepted set, in %q", tag, html)
			return
		}

		if !closing {
			stack = append(stack, tag)
			continue
		}
		if len(stack) == 0 {
			t.Errorf("closing </%s> with nothing open, in %q", tag, html)
			return
		}
		open := stack[len(stack)-1]
		if open != tag {
			t.Errorf("closing </%s> but <%s> is innermost open, in %q", tag, open, html)
			return
		}
		stack = stack[:len(stack)-1]
	}

	if len(stack) != 0 {
		t.Errorf("unclosed elements %v in %q", stack, html)
	}
}

func TestAssertTagBalanced_AcceptsBalancedHTML(t *testing.T) {
	for _, html := range []string{
		"",
		"plain text",
		"<b>bold</b>",
		"<b>outer <i>inner</i> outer</b>",
		`<a href="https://example.com/a_b">label</a>`,
		`<pre><code class="language-go">body</code></pre>`,
		"a &lt; b — an escaped angle bracket is not a tag",
	} {
		t.Run(html, func(t *testing.T) {
			assertTagBalanced(t, html)
		})
	}
}

// TestAssertTagBalanced_RejectsMalformedHTML proves the helper actually fails.
// A balance assertion that cannot fail would silently pass every later phase.
func TestAssertTagBalanced_RejectsMalformedHTML(t *testing.T) {
	cases := map[string]string{
		"unclosed element":     "<b>bold",
		"stray close":          "bold</b>",
		"crossed nesting":      "<b><i>x</b></i>",
		"unterminated tag":     "<hr* * *",
		"tag outside the set":  "<div>x</div>",
		"fragment of a tag":    "<b>x</b><co",
		"empty tag name":       "<>x",
		"close of unopened":    "<b>x</b></i>",
		"upstream broken rule": "<hr",
	}
	for name, html := range cases {
		t.Run(name, func(t *testing.T) {
			spy := &testing.T{}
			assertTagBalanced(spy, html)
			if !spy.Failed() {
				t.Errorf("assertTagBalanced(%q) passed, want a failure", html)
			}
		})
	}
}
