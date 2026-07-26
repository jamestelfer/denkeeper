# Progress: Telegram Markdown Rendering

> Plan: [`telegram-markdown-rendering.plan.md`](./telegram-markdown-rendering.plan.md)

## Phases

- [x] Phase 0: Bot seam and characterization baseline
- [x] Phase 1: Vendored renderer and the HTML send path
- [ ] Phase 2: Fix the vendored-in defects
- [ ] Phase 3: Inline and block typography
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

### Deferred to system testing

- **Phase 1 — whether Telegram renders a literal `"` and a `%22` in `href`
  correctly.** The bytes are pinned by test; how they display is not assertable
  here.
- **Phase 1 — whether Telegram accepts `tg:` in an anchor `href`.** It is on the
  allowlist per the locked decision; dropping it is a one-line change and R16 holds
  either way.
