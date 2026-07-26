# Progress: Telegram Markdown Rendering

> Plan: [`telegram-markdown-rendering.plan.md`](./telegram-markdown-rendering.plan.md)

## Phases

- [x] Phase 0: Bot seam and characterization baseline
- [x] Phase 1: Vendored renderer and the HTML send path
- [x] Phase 2: Fix the vendored-in defects
- [x] Phase 3: Inline and block typography
- [x] Phase 4: Degrade tables and images
- [x] Phase 5: Chunker and multi-message send
- [ ] Phase 6: Oversized block splitting — mechanism landed with the seam
      change below (R22 satisfied and tested at the chunker level); the phase's
      adapter-level fixture reassembly test is still outstanding
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

- **Phase 4 — the column arithmetic is `text/tabwriter`'s, not this package's.**
  The first implementation hand-rolled width computation and padding. The
  standard library already does it, it is the convention CLAUDE.md names for
  tabular output, and it removed about sixty lines of arithmetic this package
  would otherwise have had to keep correct. `tabwriter.Debug` draws the column
  bar; a single leading space on every cell after the first turns its `a |b`
  into `a | b`.
- **Phase 4 — the header rule is derived from the formatted header line, not
  from the column widths.** `strings.Map` turns each bar into `+` and everything
  else into `-`, so the junctions cannot drift out of line with the bars above
  and below — and the rule needs no access to tabwriter's internal widths. It is
  then extended to the widest row, because tabwriter leaves the final column
  unpadded and a body cell can run past the header.
- **Phase 4 — a table cell renders as plain text, with no inline markup.** A
  `<b>` inside the block would add bytes occupying no display width, which is
  exactly what breaks fixed-width alignment; Telegram's subset does not accept
  arbitrary nesting inside `pre` either. Tabs and newlines are replaced with
  spaces before the cell reaches tabwriter, where they would otherwise open a
  phantom column or end the row.
- **Phase 4 — all columns are left-aligned; the source's `:---` markers are
  ignored.** At chat width the distinction buys nothing and it keeps one
  arithmetic rule for the whole grid.
- **Phase 4 — CJK and emoji cells will not line up, and that is accepted.**
  tabwriter counts runes, not display cells. Width-aware padding needs a
  display-width table, which is a dependency this package does not otherwise
  want.
- **Phase 4 — wide tables are left to Phase 6 rather than truncated here.** The
  table is a `pre`-wrapped block like a code block, so Phase 6's split logic
  reaches it with no special case. Until then a wide table may exceed a chunk;
  truncating would lose content, which is the failure class this work exists to
  remove.
- **Phase 4 — `ErrImage` is removed rather than left defined.** Nothing returns
  it now that images degrade, and a dead exported error invites a caller to
  check for a case that cannot happen. `UnsupportedError`'s named-construct
  shape stays, because that is what a future unsupported construct reuses.
- **Phase 4 — an image with a rejected scheme *and* no alt text emits nothing.**
  The only two things available to show are the destination, which R16 forbids,
  and an invented placeholder, which is text the author never wrote. With an
  allowlisted destination and no alt text, the destination becomes the label —
  an anchor with an empty label renders as nothing at all in Telegram, which
  would drop the image silently.
- **Phase 4 — enabling `extension.Table` was probed against ordinary prose.**
  `run a | b`, `(foo|bar)` and a lone leading pipe all still render
  byte-identical; GFM requires a delimiter row, so a stray pipe cannot form a
  table. Pinned as cases rather than assumed, and the underscore-URL regression
  test is unchanged, which is what confirms linkify did not arrive with the
  extension.

- **Phase 5 — `Send` and `SendAndGetID` share one `sendText` helper, and both
  shrank to a parse plus a call.** They independently reimplemented the whole
  render-fallback-retry sequence, which is how a chunking change could easily
  have landed on one path and not the other. `SendAndGetID` is now `sendText`
  plus the ID formatting, so the two cannot drift.
- **Phase 5 — R30's plain-text retry applies only to a single-chunk reply; R31
  governs beyond that.** The retry replaces the *whole* message, so on a
  multi-chunk send it would re-deliver the chunks that already arrived. That is
  how the plan's two acceptance criteria reconcile: "exactly one retry" for one
  message, "exactly 2 send attempts of 4" for a chunked one.
- **Phase 5 — an oversized block is emitted whole and over-limit, not
  truncated.** Documented on `Chunk`, pinned by a test, and paired with a
  termination guard that sweeps limits from 1 upward — a packing loop that
  cannot place a block is the classic place to spin forever.
- **Phase 5 — R18 and R23 are asserted as one swept property rather than as
  cases.** Rejoining the chunks with `BlockSeparator` must reproduce `Join`
  exactly, checked at every limit from the widest block up. That single equality
  is simultaneously no-reordering, no-loss, no-duplication and no-mid-rune-split,
  and the content is multi-byte throughout so the last of those has teeth. A
  per-case assertion passes on whichever limit it happens to pick.
- **Phase 5 — a rate limit is not special-cased.** Telegram documents roughly
  one message per second per chat with bursts tolerated, and a chunked reply is
  a handful of messages, so a 429 mid-sequence is unlikely. When it happens R31
  applies unchanged: abort. A bounded backoff would leave a reply half-delivered
  and resume after an arbitrary pause, which reads worse than a visible failure.
  Covered by injecting a `tgbotapi.Error{Code: 429}` at chunk 3 of 4.
- **Phase 5 — a reply that renders to no blocks sends nothing, and
  `SendAndGetID` calls that an error.** Telegram rejects an empty message, and a
  caller that asked for an identifier would otherwise use `""` to edit a message
  that does not exist.
- **Phase 5 — the chunk count on a realistic long reply is sane, so the
  over-counting worry did not bite.** 60 formatted lines (~5.4 KB of HTML)
  produce 2 chunks and 120 produce 4, against a 3500-byte limit. Counting raw
  HTML bytes over-counts against Telegram's entity-stripped UTF-16 measure, but
  not by enough to matter.

- **Seam change — a `Block` is a token sequence, not a rendered string.** The
  `{Wrappers, Content}` shape chosen in Phase 1 could not carry Phase 6: a
  blockquote is wrapper-bearing *and* markup-bearing, so `len(Wrappers) > 0` did
  not tell a splitter whether `Content` was safe to cut, and `Content` being
  pre-escaped meant a cut could also bisect `&lt;`. The alternatives were a flag
  distinguishing text-content from markup-content blocks — a special case on
  shared infrastructure, and one that would refuse to split a long quotation and
  drop the whole reply to plain text — or re-parsing the HTML in the chunker,
  which is the second parser this package exists to avoid. `Render` was
  destroying structure it had just computed from the AST; the fix was to stop
  doing that.
- **Seam change — the locked public contract did not have to change.**
  `Render(src) ([]Block, error)` and `Chunk(blocks, limit) ([]string, error)`
  are untouched. The plan locks *that `Block` carries enough structure for the
  chunker to close and reopen wrapping elements*, so replacing an insufficient
  structure with a sufficient one satisfies the contract rather than breaking
  it. No replan trigger fired. `Wrapper` is gone as a type — the chunker's tag
  stack is what it was standing in for.
- **Seam change — tag balance is now structural.** A chunk ends by closing
  whatever is open and begins by reopening it, so an unbalanced chunk is not
  expressible. R19 stops being something a test checks after the fact. R22 falls
  out with no special case, and the language class reopens on each chunk for
  free because the stack holds the opening token's attributes.
- **Seam change — text tokens hold *escaped* text, not raw.** Raw text would
  make a split safe by construction, but it would also mean re-escaping on every
  `Len()` — which the chunker calls per token — and rewriting every escaping call
  site. Escaped text keeps `Len()` free and the renderer's escaping untouched, at
  the cost of one explicit rule: a cut must clear entity boundaries as well as
  rune boundaries (`textCut`, `entityStart`). That rule is swept exhaustively
  rather than sampled.
- **Seam change — the exhaustive sweep found a real behaviour question the
  hand-picked cases missed.** Chunking a rich document at *every* limit from 1
  byte upward showed that a chunk boundary landing between two blocks absorbs the
  `\n\n` separator. That is correct — two chunks are two Telegram messages,
  already visually separated, and a trailing blank line is trimmed anyway — but
  it is not what a naive reassembly assertion expects. The test now compares
  block by block: whitespace *between* blocks is free, everything *inside* a
  block is byte-exact, which is what keeps a dropped soft line break visible.
- **Seam change — the sweep was verified by sabotage, not by passing.** Dropping
  one rune per split and skipping the tag reopen were each injected into the
  chunker and each caught. A property test over hundreds of limits is worthless
  if its matcher cannot fail, and the first matcher written for this could not
  distinguish a lost block separator from a lost line break inside a block.

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
- **Phase 4 — whether a degraded table's columns *look* aligned on a narrow
  screen.** The offsets are asserted arithmetically on every row, which is a
  stronger check than reading a phone; whether `pre` is monospaced with a stable
  advance width on a given client is not assertable here. A table that is
  arithmetically aligned and still too wide to read is a design question for
  Phase 6's splitting, not a defect in this phase.
- **Phase 4 — whether Telegram renders a degraded image anchor usefully.** The
  anchor and its label are pinned; whether tapping it opens the image or a
  browser depends on the client.
- **Phase 5 — whether several messages arriving back to back read as one
  reply.** Order, completeness and per-chunk size are asserted from the captured
  sends; how a split reply feels in a real chat thread is not assertable here.
- **Phase 5 — whether real traffic ever trips Telegram's per-chat flow limit.**
  The abort behaviour on a 429 is pinned by an injected error; how often a
  chunked reply actually provokes one is a question for a live instance.
