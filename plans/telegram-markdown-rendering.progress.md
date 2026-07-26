# Progress: Telegram Markdown Rendering

> Plan: [`telegram-markdown-rendering.plan.md`](./telegram-markdown-rendering.plan.md)

## Phases

- [x] Phase 0: Bot seam and characterization baseline
- [x] Phase 1: Vendored renderer and the HTML send path
- [x] Phase 2: Fix the vendored-in defects
- [x] Phase 3: Inline and block typography
- [ ] Phase 4: Degrade tables and images
- [ ] Phase 5: Chunker and multi-message send
- [ ] Phase 6: Oversized block splitting
- [ ] Phase 7: Fallback counter and warning log

## Lessons learned

<!--
Decisions made during implementation and problems solved by implementing.
One entry per item, tagged with the phase it came from. No general notes,
no status updates, no restatement of the plan.
-->

- **Phase 0 — the seam is one interface, not two.** `botSender` carries both `Send` and `Request`.
  The flex zone allowed splitting them, but `Send`-shaped and `Request`-shaped calls both live on
  `Adapter` (`Send`/`SendTyping` vs `EditText`/`EditMessage`/`handleCallbackQuery`), so two
  interfaces would have meant two fields and two fakes describing one collaborator.
- **Phase 0 — a nil bot gets an `unavailableSender`, not a nil interface value.** Assigning a nil
  `*tgbotapi.BotAPI` to a `botSender` produces a non-nil interface holding a nil pointer, which
  panics on the first outbound call. `senderFor` returns a stub that fails with `errNoBot` instead,
  so `newWithBot(nil, …)` keeps working and the nil hazard cannot reach a send path.
- **Phase 0 — every `Send`/`Request` call site was routed, not just the five paths the plan named.**
  A partially routed seam leaves adapters built by `newWithSender` with a nil `Adapter.bot`, so any
  unrouted call panics. `GetUpdates`, `GetUpdatesChan`, `GetFileDirectURL` and
  `StopReceivingUpdates` stay on `Adapter.bot`, which keeps the interface narrow as locked.
- **Phase 0 — injection is a package-private constructor, not a functional option.** `newWithSender`
  sits beside the existing `newWithBot`, which now delegates to it. `New` also delegates, so the
  three constructors cannot drift in how they populate the struct.
- **Phase 0 — `Chattable` → `MessageConfig` assertion is clean in tgbotapi v5.5.1** (open question
  closed). It is not one assertion, though: typing indicators arrive as `ChatActionConfig` and edits
  as `EditMessageTextConfig`, so the fake needs a typed accessor per config type rather than one
  `messages()` helper. Later phases asserting on edits should use `edits()`.
- **Phase 0 — fake message IDs start at 9001 and advance only on an accepted send** (the second open
  question). A rejected send returns the zero-value `tgbotapi.Message`, so a test that accidentally
  asserts against a zero ID fails instead of passing, and `SendAndGetID`'s retry path provably
  returns the ID of the send that actually succeeded.
- **Phase 0 — the activity-log pass-through characterization landed here rather than waiting for
  Phase 1.** `TestSend_ExplicitParseModePassesThrough` and `TestSend_ExplicitParseModeIsNotRetried`
  pin today's explicit-parse-mode behaviour, including the inline keyboard. R28 is a Phase 1
  requirement, but the assertion is about current behaviour, and it is the guard that makes Phase
  1's highest-blast-radius regression visible the moment it appears.
- **Phase 1 — upstream was obtained through the Go module proxy, not a git clone.**
  `api.github.com` is blocked by egress policy in this environment, but
  `proxy.golang.org/github.com/leonid-shevtsov/telegold/@latest` resolves to
  `v0.0.0-20221113220506-36dc899eb9ea`, whose commit hash matches the one the plan
  recorded. The `@v/….zip` carries the full source including `LICENSE`.
- **Phase 1 — the renderer walks the AST itself rather than subclassing goldmark's
  HTML renderer.** Reusing upstream's `renderer.Renderer` per top-level node was
  tempting and would have produced blocks, but the wrapping tags come back baked
  into the rendered string, and `Block.Wrappers` needs them separated. Recovering
  them would mean parsing the output back apart.
- **Phase 1 — `Block` is `{Wrappers []Wrapper, Content string}`,** wrappers
  outermost first. Only blocks that are realistically splittable carry wrappers —
  today just code blocks. Everything else puts its markup in `Content`, which is
  tag-balanced on its own. A heading is `<b>…</b>` in Content, not a wrapper,
  because splitting a heading across chunks is not a real scenario.
- **Phase 1 — a code block nested inside a list item or quotation renders inline,
  with its `pre`/`code` in Content.** Wrappers describe what encloses a *block*,
  which is what a split has to reopen; a nested code block is not the block.
- **Phase 1 — block boundary: one top-level markdown node is one block.** This
  answers the plan's open question about a blockquote containing a list — the
  quotation is a single block, and its nested content is joined inside `Content`.
- **Phase 1 — escaping is hand-rolled down to exactly `<`, `>`, `&`.** goldmark's
  escaper also emits `&quot;` for a double quote, and Telegram's documentation
  names only those three characters, never `&quot;`. Rather than reimplement
  goldmark's character-reference and backslash-unescape logic, the output goes
  through goldmark and `&quot;` is then restored to a literal quote. That
  substitution is safe because goldmark produces the sequence only from a literal
  quote byte: a source-escaped `&amp;quot;` renders as `&amp;quot;`, which does not
  contain `&quot;`. A literal `"` in text content is unambiguous to any parser
  where a named entity depends on the parser's entity table.
- **Phase 1 — attribute context keeps the quote escaped, as `%22`.** A literal
  quote inside `href` would end the attribute. Percent-encoding it rather than
  emitting `&quot;` avoids relying on Telegram's entity table in the one place
  where the character cannot simply be left alone. The rest of the destination is
  emitted byte-for-byte — percent-encoding a whole URL would alter it, and
  preserving it exactly is the defect this package exists to fix.
- **Phase 1 — `schemeOf` strips bytes `<= 0x20` and `0x7f` before matching.** The
  stripping applies only to the classification, never to what is emitted, and it
  can only make the decision stricter: a scheme that matches after stripping would
  not have matched at all otherwise. That closes padded and line-broken variants
  alongside the case-varied bypass.
- **Phase 1 — a destination with no scheme is rejected.** Relative and
  scheme-relative (`//host/path`) destinations have nothing to check against the
  allowlist, so they render as a bare label. Phase 3's open question is answered
  the strict way.
- **Phase 1 — the thematic break had to be corrected here, not in Phase 2.** R7 is
  Phase 2 work, but upstream's `<hr* * *` is an unterminated tag, and R5's
  tag-balance invariant plus R2's tag allowlist are Phase 1 requirements that it
  violates. It emits the text `* * *` from this phase. Phase 2 still owns the
  choice of separator string; it will find R7's test already passing.
- **Phase 1 — the plan's Verification prose asks for the bug-report URLs "both
  wrapped in anchors"; they are not, and must not be.** CommonMark does not
  autolink bare URLs, and Phase 4's locked decision explicitly forbids enabling
  `linkify` for exactly this input. The acceptance criterion — byte-identical, both
  underscores present — is what the test asserts, and Telegram autolinks the
  visible text after parsing.
- **Phase 1 — every carried-forward defect was re-verified against this
  implementation, not assumed.** Probed output: `alpha beta\ngamma delta` →
  `alpha betagamma delta` (R6), `alpha␠␠\ngamma` → `alphagamma` (R34),
  `> line one\n> line two` → `&gt; line oneline two` (R8, both defects),
  `- parent\n    - child` → `- parent\n- child` (R9), `1. first\n2. second` →
  `- first\n- second` (R35). Phase 2 has exactly the defect set the plan describes.
- **Phase 1 — `just scan` fails, and did so before this work.** The clean tree
  reports the identical 3 gosec findings (config writer, `audit/emitter.go`,
  `cmd/denkeeper/main.go` — all carrying `nolint` annotations gosec still counts),
  and govulncheck reports `GO-2026-5970` in `golang.org/x/text@v0.38.0`, fixed in
  v0.39.0. Neither is attributable to goldmark: the phase's actual question — does
  the new direct dependency introduce a finding — is answered no, the gosec count
  is unchanged and goldmark v1.8.2 requires no modules. Bumping `x/text` is an
  unrelated dependency change and is left for the project owner.
- **Phase 1 — `go mod tidy` moved no dependency versions.** The `go.sum` delta is
  goldmark promoted to direct plus the removal of superseded duplicate entries for
  `coreos/go-oidc`, `dop251/goja` and `pelletier/go-toml`. The replan trigger about
  an unrelated forced upgrade did not fire.

- **Phase 2 — the R9 indent question is settled by what Telegram's HTML mode
  actually is, so ordinary spaces are used.** Telegram's HTML parse mode is not an
  HTML renderer: the tags are parsed into `MessageEntity` offsets over the message
  text, and the text is kept verbatim. The documented `<blockquote>` example
  demonstrates it by placing literal newlines inside the element and describing
  them as separate lines — a real HTML renderer would collapse those to spaces.
  The same property is what makes R6's and R34's newline fixes work at all, and
  what the pre-existing `\n\n` between paragraphs already relied on. Two spaces per
  level, no non-breaking space needed.
- **Phase 2 — nested-list indentation is capped at six levels.** Beyond that,
  further nesting reuses the deepest indent rather than marching the list off the
  side of a narrow screen. Content is never dropped, only the indent stops growing.
- **Phase 2 — a nested blockquote is flattened to one level.** Telegram represents
  a quotation as a message entity spanning a text range, and such an entity cannot
  contain another of its own kind, so `> outer\n> > inner` emits a single
  `blockquote` containing both texts. The same rule applies to a quotation inside a
  list item, which answers the plan's second open question with one rule rather
  than two. Both levels' text always survives.
- **Phase 2 — the thematic-break separator is ten em dashes, not upstream's
  `* * *`.** Asterisks read as unrendered markdown source; em dashes abut into a
  continuous line. Neither form contains a character needing escaping, so Phase 1's
  escaping is unaffected either way.
- **Phase 2 — top-level blockquotes carry a `Wrapper`, matching code blocks.** It
  is the first multi-line wrapping element in the renderer, and it is exactly the
  shape Phase 6 has to close and reopen, so giving it a wrapper now costs nothing
  and means the split logic will not need a special case for it.
- **Phase 2 — R6 and R34 are one line of code, and the realistic test is the one
  that matters.** `ast.Text` carries `SoftLineBreak()` and `HardLineBreak()` flags
  and upstream inspected neither. The per-construct cases pass trivially once the
  flags are read; the test with teeth renders a two-paragraph reply wrapped at 80
  columns and asserts every source word appears as a whole word and the newline
  count survives, which is what catches a partial fix.
- **Phase 2 — R7's test was already passing on arrival**, because Phase 1 had to
  correct the malformed `<hr* * *` to satisfy its own tag-balance and tag-allowlist
  requirements. This phase owned only the choice of separator string.

- **Phase 3 — the heading carries its own newline, and the joiner deducts it.**
  R14 names the line break as part of the heading, so `<b>…</b>\n` is what the
  heading emits — otherwise the requirement holds only where the surrounding
  joiner happens to be one. Writing `BlockSeparator` on top of that newline
  opened two blank lines between a heading and its first sentence, so
  `separatorAfter(prev, sep)` deducts the newlines `prev` already contributed.
  It is applied at all four join sites (`Join`, `writeChildBlocks`, `writeList`,
  `writeListItem`) so a heading behaves the same wherever it appears.
- **Phase 3 — `extension.Strikethrough` is selected on its own, and the parser
  is a package-level `mdParser` rather than `goldmark.DefaultParser()`.** Phase
  4 needs `Table` in the same place, and the locked decision forbids
  `extension.GFM`, which would bundle linkify and wrap the originating defect's
  bare URLs in anchors.
- **Phase 3 — GFM matches one *or* two tildes, so `~b~` is strikethrough too.**
  The first test asserted a single tilde stayed literal and was wrong against
  the spec. The real risk enabling the extension introduces is prose pairing —
  `cd ~/foo and ~/bar`, `approx ~5 to ~10` — which GFM's flanking rules already
  prevent. Probed and pinned as cases rather than assumed.
- **Phase 3 — R13's and R16's tests were already passing on arrival.** Phase 1
  had to build the `pre`/`code` language class to give the chunker a wrapper
  pair, and had to replace upstream's bypassable denylist rather than ship it.
  This phase owned the hostile-input coverage: case variation, padding, embedded
  tabs and control bytes, scheme-relative and relative destinations.
- **Phase 3 — a destination CommonMark refuses to parse as a link is not a
  renderer failure.** `[click](<java\nscript:alert(1)>)` is not a link at all —
  a bracketed destination may not contain a newline — so the whole source
  survives as prose. The table marks that case `notALink`: the anchor and `href`
  assertions still apply, the destination-absent assertion does not, because the
  renderer emits any other `javascript:` string in prose verbatim too.
- **Phase 3 — a rejected *autolink* keeps its label, which is its destination.**
  For `[label](dest)` the destination is dropped entirely; for
  `<javascript:alert(1)>` the label is the destination and dropping it would
  lose content silently. It is emitted as escaped text, which is exactly what
  the same string gets as ordinary prose, and Telegram does not autolink a
  `javascript:` scheme in visible text.
- **Phase 3 — `just scan` is unchanged from the Phase 1 baseline.** The same
  three `nolint`-annotated gosec findings (config writer, `audit/emitter.go`,
  `cmd/denkeeper/main.go`) and the same `GO-2026-5970` in
  `golang.org/x/text@v0.38.0`. No finding touches `url.go` or any other
  URL-handling code, which is what this phase's gate asks.

### Deferred to system testing

- **Phase 1 — whether Telegram renders a literal `"` and a `%22` in `href`
  correctly.** The bytes are pinned by test; how they display is not assertable
  here.
- **Phase 1 — whether Telegram accepts `tg:` in an anchor `href`.** It is on the
  allowlist per the locked decision; dropping it is a one-line change and R16 holds
  either way.
- **Phase 2 — whether the two-space nested-list indent is *visible* on a phone.**
  The bytes are correct and pinned, and nesting depth is recoverable from the
  output; how it looks at a given font and width is not assertable here.
- **Phase 2 — whether ten em dashes read as a horizontal rule.** The bytes are
  pinned; whether the glyphs abut into a continuous line depends on the client font.
- **Phase 2 — whether a flattened nested quotation is confusing to a reader.** The
  text of both levels survives, but the level distinction is gone by construction.
- **Phase 3 — whether Telegram accepts and monospaces `language-xxx` on a
  nested `code`.** The structure and the class bytes are pinned; whether the
  fence renders monospaced and syntax-tagged on a given client is not assertable
  here.
- **Phase 3 — whether one blank line after a bold heading reads as a heading.**
  The bytes are pinned at exactly one; whether that is enough visual separation
  at a phone's line height is an appearance question.
