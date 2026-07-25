# Telegram Markdown Rendering

## Problem Statement

Agent replies reach Telegram as raw markdown with `parse_mode=Markdown`, Telegram's legacy parser, and nothing escapes the text. That parser has no intraword-emphasis rule, so an underscore inside a URL opens an italic entity and is consumed as markup. A recently sent message demonstrated the failure: two URLs each containing an underscore had both underscores eaten, the text between them was italicised, and the autolinked destinations no longer resolved. The audit log showed the underscores were present on the wire, so the data was never corrupted — only the rendering.

The existing safety net cannot catch this class of failure. It retries with no parse mode only when the Telegram API returns an error, and this message was accepted successfully. Corruption is therefore silent, unbounded in frequency, and invisible in logs and metrics.

The blast radius is wider than cosmetics. Any identifier containing `_`, `*`, `` ` `` or `[` can be mangled, and because Telegram autolinks post-parse text, mangled URLs produce dead links. LLM output contains such identifiers routinely.

## Solution

Stop asking Telegram to parse markdown. Parse it locally with a CommonMark parser and render to the HTML subset Telegram supports, then send with `parse_mode=HTML`. A compliant CommonMark parser does not treat an intraword underscore as emphasis, so the defect disappears at the root rather than being escaped around. HTML parse mode also has a three-character escape surface instead of a dozen-plus, which makes the remaining failure modes tractable.

The renderer is derived from an existing MIT-licensed goldmark-to-Telegram-HTML renderer, vendored so its known defects can be fixed and its coverage extended. It returns a sequence of independently valid HTML blocks rather than one string, which also lets a chunker split long replies at safe boundaries — closing Telegram's 4096-character message cap as a second, adjacent failure mode.

Rendering lives inside the Telegram adapter. Outgoing message text stays canonical markdown at the adapter interface, so the Discord adapter — which relies on Discord's own native markdown rendering — is untouched.

## Requirements

### Renderer

1. The renderer shall parse its input as CommonMark.
2. The renderer shall emit only HTML elements accepted by Telegram's HTML parse mode.
3. The renderer shall escape `<`, `>` and `&` in text content.
4. The renderer shall preserve literal `_`, `*`, `` ` `` and `[` characters in text content without emitting escape characters.
5. The renderer shall emit blocks in which every opened element is closed within the same block.
6. When the source contains a soft line break within a paragraph, the renderer shall emit a newline character.
7. When the source contains a thematic break, the renderer shall emit a text separator containing no HTML element.
8. When the source contains a block quotation, the renderer shall emit a `blockquote` element.
9. When the source contains a nested list, the renderer shall indent each nested item relative to its parent item.
10. When the source contains a strikethrough span, the renderer shall emit an `s` element.
11. When the source contains a table, the renderer shall emit the table as a preformatted block that preserves column alignment.
12. When the source contains an image, the renderer shall emit an anchor element whose label is the image's alternative text.
13. When the source contains a fenced code block annotated with a language, the renderer shall emit a `pre`-wrapped `code` element carrying the corresponding language class.
14. When the source contains a heading, the renderer shall emit a `b` element followed by a line break.
15. If the source contains raw HTML, then the renderer shall return an error naming the unsupported construct.
16. If a link destination uses a scheme classified as dangerous, then the renderer shall emit the link's label without an anchor element.

### Renderer — added after upstream verification

Added after the vendored renderer was executed rather than read; see *Renderer provenance*. Numbered from 34 to preserve the existing identifiers, which are referenced by the implementation plan.

34. When the source contains a hard line break within a paragraph, the renderer shall emit a newline character.
35. When the source contains an ordered list, the renderer shall emit each item prefixed with its ordinal.

### Chunker

17. The chunker shall emit chunks whose length does not exceed the supplied limit.
18. The chunker shall preserve the source order of blocks across chunks.
19. The chunker shall emit chunks in which every opened element is closed.
20. While the active chunk has capacity for the next block, the chunker shall append that block to the active chunk.
21. When appending a block would exceed the limit, the chunker shall begin a new chunk.
22. When a single block exceeds the limit, the chunker shall split the block and close and reopen its wrapping elements across the split.
23. If a split point falls inside a multi-byte character, then the chunker shall move the split to the preceding rune boundary.

### Adapter

24. The adapter shall treat outgoing message text as CommonMark markdown.
25. When sending a message without an explicit parse mode, the adapter shall render the text and send one Telegram message per chunk, in order.
26. When a message is sent as multiple chunks, the adapter shall attach any inline keyboard to the final chunk only.
27. When a multi-chunk send is required to return a message identifier, the adapter shall return the identifier of the final chunk.
28. Where an outgoing message specifies an explicit parse mode, the adapter shall send the text unmodified.
29. If rendering returns an error, then the adapter shall send the original text with no parse mode.
30. If Telegram rejects a send using HTML parse mode, then the adapter shall retry the message once with no parse mode.
31. If a chunk fails to send, then the adapter shall abort the remaining chunks and return an error.

### Observability

32. When the adapter sends a message with no parse mode as a result of a rendering or send failure, the adapter shall increment a fallback counter carrying a reason attribute.
33. When the adapter falls back to plain text, the adapter shall emit a warning-level log record identifying the reason.

## Implementation Decisions

**Renderer provenance.** The renderer is vendored from an existing MIT-licensed goldmark renderer that adapts goldmark's own HTML renderer to Telegram's tag subset, with attribution and licence retained. Vendoring rather than importing is deliberate: upstream has had one commit since 2022, pins a goldmark three minor versions behind current, and carries six measured defects — dropped soft line breaks (which fuses adjacent words and is disqualifying for wrapped LLM output), dropped hard line breaks, a malformed thematic-break tag, block quotations rendered as a literal `>` character rather than the supported element, flattened nested lists, and ordered lists renumbered as bullets. Requirements 6–9, 34 and 35 are those fixes.

The last two were found by executing the vendored renderer against the goldmark version this project already resolves, rather than by reading it. That exercise also confirmed every defect above, established that the version gap requires no code changes, and surfaced three further problems that are corrections to existing requirements rather than new ones: a rejected link destination emits an empty-href anchor rather than a bare label (requirement 16 is therefore a correction, not an extension); the dangerous-scheme check is case-sensitive and passes `JaVaScRiPt:` through unmodified, which is why requirement 16 is implemented as an allowlist; and the single error value shared by raw HTML and images cannot satisfy requirement 15's obligation to name the construct, nor let the adapter tell a fail-closed case from a degrading one.

A fourth observation is structural rather than a defect: upstream renders to one flat string, so the block sequence described under *Module boundary* is an architectural change to the vendored code, not a small fix to it.

**Degradation over failure.** Upstream fails closed on tables and images. For arbitrary LLM output that would route a large share of ordinary replies to the plain-text fallback, which defeats the purpose. Tables and images degrade (requirements 11–12); only raw HTML, which has no representable form, still fails closed.

**Module boundary.** One package, two concerns. Rendering and chunking are separate, separately callable, separately tested functions in the same package; the adapter composes them. Keeping them in one package lets the chunker rely on the renderer's block-and-tag invariants without exporting them.

**Adapter interface unchanged.** Outgoing message text remains markdown and an explicit parse mode remains caller-controlled pass-through. This preserves two existing behaviours: the Discord adapter continues to send text verbatim to a platform that renders markdown natively, and the activity log's hand-built, pre-escaped HTML is not double-rendered.

**Metrics idiom.** Instrumentation uses the OpenTelemetry meter and `Int64Counter` pattern already used by eight subsystems in this codebase, exported to Prometheus through the existing exporter and `/metrics` endpoint. The adapter layer currently has no metrics at all; this is the first.

**No configuration.** No toggle is added. The alternative code path a toggle would select is the defective one, and the plain-text fallback already handles failure without operator intervention.

## Testing Decisions

**Renderer** — table-driven golden tests over markdown inputs and expected HTML. Each of requirements 3–16, 34 and 35 gets at least one case. Mandatory regression case: a URL containing an underscore must survive rendering byte-for-byte, since this is the originating defect. The six vendored-in bugs each get a case derived from the probe that found them: a paragraph wrapped across two source lines, a paragraph broken with a hard break, a thematic break, a block quotation spanning two lines, a two-level nested list, and an ordered list. Each case should record the measured broken output alongside the expected one, so a regression is recognisable rather than merely red.

Two of those cases carry a second assertion. The block quotation must be checked for both its defects — the element and the internal line break, which fails through the same path as requirement 6 — and the ordered list must include one that starts at an ordinal other than 1, which a fix that simply counts items from the top would silently renumber.

Requirement 16 needs hostile inputs rather than representative ones: case-varied schemes, leading whitespace and control characters, and destinations with no scheme at all. Upstream's check fails the first of these today.

Upstream's own test file is not a useful starting point — it has two cases, both misnamed, and it discards the conversion error.

**Chunker** — unit tests with a small synthetic limit so cases stay readable. Cover the boundary conditions in requirements 17–23: exact-fit, one-over, a block larger than the limit, and a split landing mid-rune. Assert tag balance on every emitted chunk, since that invariant is what makes chunking safe.

**Adapter send paths** — extend the existing fake-adapter pattern already used to assert on parse mode in dispatcher tests. Cover: markdown in and HTML out with the correct parse mode; explicit parse mode passing through unrendered; a render failure producing a plain-text send; a multi-chunk send attaching buttons to the last chunk and returning the last identifier; and a mid-sequence chunk failure aborting the remainder.

Every requirement above maps to at least one of these conditions.

The plan derived from this document is [`telegram-markdown-rendering.plan.md`](./telegram-markdown-rendering.plan.md), which carries the phase breakdown and the requirement-to-phase coverage matrix.

## Out of Scope

- The Discord adapter. Discord renders markdown natively and needs no conversion.
- The activity-log rendering path. It builds pre-escaped HTML from fixed templates and already passes an explicit parse mode.
- Migrating to MarkdownV2. Its escape surface is larger and context-dependent, and it retains the silent-misparse failure mode this PRD exists to eliminate. Reasoning in full under decision 13.
- Constructing `MessageEntity` values directly. It is the most robust option, since it removes the second parser entirely, but the current bot library requires hand-computed UTF-16 offsets, which is a worse risk trade than HTML.
- Upstreaming the renderer fixes. Worth doing as a courtesy; not a dependency of this work.
- Rich formatting features Telegram supports but the agent does not currently emit — spoilers, custom emoji, expandable block quotations.

## Further Notes

The plain-text fallback is load-bearing and should stay. A fail-closed renderer plus an unstyled fallback means the worst outcome is a correct message that looks plain; the current design's worst outcome is a confidently wrong message that looks fine.

The observability gap is the most transferable lesson. This defect was undetectable from inside the system: no error, no log, no metric. It surfaced only because a human compared a rendered message against an audit log. The fallback counter is what converts the next instance of this class from an archaeology exercise into an alert.

---

## Appendix: Decisions

Decisions taken during the design interview, with the context that drove them.

| # | Decision | Reason |
|---|---|---|
| 1 | Render in the Telegram adapter, not the dispatcher | The Discord adapter sends message text verbatim and depends on Discord's native markdown. Rendering upstream of the adapter would corrupt Discord output to fix Telegram. |
| 2 | Vendor the existing renderer and fix it, rather than import it or write fresh | Upstream is one commit from 2022 with no tags, pinning goldmark three minor versions back, and has four defects — one of which fuses words across wrapped lines and is disqualifying on its own. But its structure is right: it forks goldmark's own HTML renderer, reuses upstream escaping, and fails closed on disallowed tags. It is a better map than a blank file and a worse dependency than a vendored copy. |
| 3 | Fall back to plain text with no parse mode on failure | Matches the existing retry behaviour, and pairs with a fail-closed renderer so the worst case is unstyled-but-correct rather than silently corrupted. |
| 4 | Degrade tables and images rather than fail closed on them | LLM replies contain tables and lists routinely. Failing closed would route a large share of normal responses to plain text, discarding the formatting the feature exists to provide. |
| 5 | Bring 4096-character chunking into scope | Long replies already fail today — there is no chunking on this path, only on the activity log. Emitting HTML makes naive byte-splitting actively unsafe, because a split can land mid-element. The renderer is the only component that knows where a safe boundary is, so the two concerns are the same piece of work. |
| 6 | Renderer returns blocks; chunker assembles them | Gives the chunker a tag-balance invariant to rely on and keeps both halves independently testable, rather than post-processing an opaque HTML blob. |
| 7 | Split oversized blocks by closing and reopening wrapping elements | A long fenced code block is the realistic case. Truncating loses content silently; falling back to plain text for the whole message penalises everything else in it. |
| 8 | One package, two separated concerns | Rendering and chunking are distinct enough to test apart but coupled by shared invariants; separate packages would force those invariants into an exported contract. |
| 9 | Add no configuration surface | The only thing a toggle could select is the broken path, and the fallback already covers failure without human intervention. |
| 10 | Instrument with an OTel counter plus a warning log | Conditional on prior art, which exists and is unambiguous: eight subsystems use the OTel meter and `Int64Counter` idiom with a Prometheus exporter already wired. The adapter layer is the only uninstrumented layer, so this follows house style rather than introducing a pattern. |
| 11 | Inline keyboards on the final chunk; return the final chunk's identifier | Buttons under a mid-message fragment read as a broken message, and subsequent edits should target the tail of the conversation. |
| 12 | Keep the outgoing-message contract unchanged | Text stays markdown and an explicit parse mode stays pass-through, which preserves Discord's behaviour and prevents double-rendering the activity log's hand-built HTML. |
| 13 | Stay on HTML rather than MarkdownV2 or direct message entities | MarkdownV2 has three context-dependent escape alphabets against HTML's one, gains no formatting capability, and keeps the silent-misparse failure mode. Direct entity construction is more robust still, but the current bot library would require hand-computed UTF-16 offsets. Expanded below. |
| 14 | Take requirements 34 and 35 into scope rather than deferring them | Both were measured, not predicted, when the vendored renderer was executed. Hard line breaks fail through the same code path as requirement 6 and fixing one while leaving the other is indefensible; ordered lists silently become bullets, which loses information in numbered steps that agent replies produce routinely. Both are the same size and shape as the fixes already agreed, and both are defects a user would report in the first week. |
| 15 | Number the additions 34 and 35 rather than inserting them into the renderer sequence | The existing identifiers are referenced by the implementation plan and its coverage matrix. Renumbering to keep the list tidy would invalidate every reference for no benefit. |

### Decision 13 in detail: why not MarkdownV2

MarkdownV2 is the obvious alternative to HTML — it is the maintained markdown mode, it is at feature parity with HTML on entity types, and it produces shorter output. It was rejected for five reasons.

**The escape alphabet is context-dependent.** In ordinary text, eighteen characters must be escaped: `_ * [ ] ( ) ~ ` > # + - = | { } . !`. Inside `pre` and `code` entities only the backtick and backslash must be escaped. Inside the parenthesised part of an inline link, only the closing parenthesis and the backslash. The backslash itself generally needs escaping. A correct emitter therefore has to know which of three contexts it is writing into and switch alphabets accordingly. HTML has one rule applied uniformly to text content — three characters, no context tracking, no state.

**That context-dependence is where implementations actually fail.** The leading Go MarkdownV2 renderer applies the text alphabet inside inline code spans, so `os.Getenv("HOME")` in backticks emits as `os\.Getenv\("HOME"\)` and the backslashes render literally to the user. It handles fenced blocks correctly and inline spans incorrectly, because its escaping is a flat byte-level character map with no notion of the enclosing node. This is not an unusual bug; it is the predictable consequence of a grammar with three alphabets.

**The failure modes are worse-shaped.** MarkdownV2 fails both loudly, with a parse-entities rejection — commonly an unescaped hyphen in interpolated content — and silently, by misparsing, which is precisely the class of defect this PRD exists to eliminate. HTML fails one way: unsupported or malformed markup is rejected. There is no HTML equivalent of two stray underscores in different URLs pairing into an emphasis span, because HTML markup is delimited by characters a renderer emits deliberately rather than by characters that occur naturally in prose.

**There is an ambiguity in the grammar with no clean resolution.** Double underscore is parsed greedily left-to-right as an underline delimiter, so combined italic-and-underline requires interposing a carriage return as an ignored separator. Shipping a control-character workaround to disambiguate our own generated output is a poor foundation.

**Nothing is gained.** MarkdownV2 and HTML support the same entity set, and the nesting restrictions are identical because they are properties of message entities rather than of either syntax. Only the legacy markdown mode is impoverished.

The one genuine cost of HTML is message-length budget: its tags are longer than MarkdownV2's delimiters. MarkdownV2 escaping inflates as well, and agent replies are punctuation-dense prose in which many characters double in width, so the two roughly cancel with a mild edge to MarkdownV2. Since chunking is in scope, that edge buys nothing.

The structurally correct answer is to construct message entities directly and send no parse mode at all, eliminating the second parser and the need to escape anything. It is out of scope only because the current bot library requires hand-computed UTF-16 offsets, which trades a small well-understood risk for a subtler one around emoji and non-BMP characters. If the library is ever replaced, this decision should be revisited first.
