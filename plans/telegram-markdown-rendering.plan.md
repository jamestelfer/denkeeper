# Plan: Telegram Markdown Rendering

> Source PRD: [`telegram-markdown-rendering.prd.md`](./telegram-markdown-rendering.prd.md)

Replace `parse_mode=Markdown` on the Telegram adapter's outgoing path with locally-rendered
Telegram-flavoured HTML, and close the 4096-character message cap while we are in there.

---

## Architectural decisions

Durable decisions that apply across all phases. Changing any of these mid-plan is a replan trigger.

- **Package**: one new package, `internal/adapter/telegram/tghtml`. Two concerns (render, chunk) as
  separate files and separately callable exported functions in the same package, per PRD decision 8.
  The chunker relies on the renderer's block-and-tag invariants without those invariants becoming an
  exported cross-package contract. The adapter composes the two.
- **Public contract** (locked in shape, flexible in field naming):
  - `Render(src []byte) ([]Block, error)` — ordered blocks, each independently tag-balanced.
  - `Chunk(blocks []Block, limit int) ([]string, error)` — ordered chunk strings, each tag-balanced,
    each `len(chunk) <= limit`.
  - `Block` carries enough structure for the chunker to close and reopen wrapping elements across a
    split (R22). A bare `string` is therefore **not** sufficient.
- **Length accounting**: byte length of the raw HTML string, with rune-boundary correction (R23).
  Telegram's own 4096 limit applies to the *entity-stripped* text, so counting raw HTML over-counts.
  Over-counting is conservative and therefore safe; it costs occasional extra chunks and buys us a
  metric we can compute without a second parser.
- **Message limit**: the adapter supplies the limit; the chunker never hardcodes 4096. Adapter uses a
  headroom constant below 4096, consistent with `activityChunkMaxBytes = 3500` in
  `internal/agent/dispatcher.go:1490`.
- **Link scheme policy (R16)**: allowlist, not denylist. `http`, `https`, `tg`, `mailto` produce an
  anchor; every other scheme (including `javascript:`, `data:`, `vbscript:`, `file:`) renders the
  label as escaped text with no anchor. Allowlisting is the locked decision — a denylist of
  "dangerous" schemes fails open on the scheme nobody thought of.
- **Vendoring**: `leonid-shevtsov/telegold`, MIT. Copied into `internal/adapter/telegram/tghtml/`
  with the upstream `LICENSE` retained as a file in that directory and an attribution header on each
  vendored file. The repository is Apache-2.0; MIT is compatible, but the licence text must travel
  with the code.
- **Dependency**: `github.com/yuin/goldmark` is currently in `go.sum` only (v1.8.2, transitively
  pruned — it is not in `go.mod`). Vendoring promotes it to a **direct** dependency, together with
  `goldmark/extension` for GFM tables (R11) and strikethrough (R10). Upstream telegold pins an older
  goldmark; we take the version already in `go.sum`.
- **Adapter interface unchanged**: `adapter.OutgoingMessage.Text` stays canonical markdown and
  `ParseMode` stays caller-controlled pass-through (PRD decision 12). This is what keeps the Discord
  adapter and the activity log working — see Regression watchpoints, Phase 1.
- **Both send paths are in scope**: `Adapter.Send` (`telegram.go:292`, returns `error`) and
  `Adapter.SendAndGetID` (`telegram.go:529`, returns the message ID). Each independently performs
  today's Markdown-then-retry-plain sequence. Every adapter-layer requirement lands on both.
- **Metrics**: `otel.Meter("denkeeper.telegram")` with an `Int64Counter` created at adapter
  construction and stored on the struct, matching `internal/agent/dispatcher.go:142`. No new
  exporter wiring — the existing Prometheus exporter and `/metrics` endpoint carry it.
- **No configuration surface** (PRD decision 9). No TOML key, no env var, no toggle.

## Verified upstream findings

Upstream was cloned and executed against goldmark v1.8.2 before this plan was finalised, so the
following are measurements rather than expectations. Probe source and raw output are reproducible
from the case list in Phase 1.

**Provenance confirmed.** `leonid-shevtsov/telegold` is a single commit (`36dc899`, 2022-11-14), no
tags, MIT © 2022 Leonid Shevtsov, pinning goldmark v1.5.3 against our v1.8.2. Its test file has two
cases, both `**bold**` input — the one named `"italic"` tests bold — and it discards the
`md.Convert` error. The PRD's characterisation is accurate in every particular.

**goldmark v1.5.3 → v1.8.2 needs no code changes.** The vendored renderer compiles and runs
unmodified against the version already in our `go.sum`. This was Phase 1's largest open question and
it is now closed: there is no API drift to absorb.

**The four PRD defects, reproduced:**

| Defect | Input | Actual output |
|---|---|---|
| R6 soft line break | `alpha beta\ngamma delta` | `alpha betagamma delta` — **words fused** |
| R7 thematic break | `---` | `<hr* * *` — unclosed, malformed |
| R8 block quotation | `> line one\n> line two` | `&gt; quoted line onequoted line two` |
| R9 nested list | `- parent\n    - child` | `- parent\n- child` — flattened |

Note that R8 fails twice: the literal `&gt;` *and* the fused lines, because the quote's internal
line breaks hit the same R6 defect.

**Five findings the PRD does not cover.** These materially change scope:

1. **Hard line breaks are dropped too.** `alpha␠␠\ngamma` → `alphagamma`. `renderText` handles
   neither `SoftLineBreak()` nor `HardLineBreak()`. R6 covers only the soft case, so the hard case
   was uncovered by any requirement despite identical severity. Now `R34`, accepted.
2. **Ordered lists lose their numbering entirely.** `1. first\n2. second` → `- first\n- second`.
   `renderListItem` writes a constant `"- "` and never inspects `ast.List.IsOrdered()`. No PRD
   requirement covered this. Now `R35`, accepted.
3. **Dangerous links emit `<a href="">label</a>`, not a bare label.** Upstream keeps the anchor and
   blanks the href. R16 requires the label *without* an anchor element, so R16 is a correction of
   upstream behaviour, not an extension of it.
4. **The dangerous-scheme check is case-sensitive and trivially bypassed.**
   `[click](JaVaScRiPt:alert(1))` renders as `<a href="JaVaScRiPt:alert(1)">click</a>` — straight
   through, unmodified. `IsDangerousURL` uses `bytes.HasPrefix` against lowercase literals with no
   case folding. This is the strongest possible argument for the allowlist decided above, and it
   means the denylist must not survive into a commit. See Phase 1.
5. **`Render` does not return blocks.** Upstream is a `renderer.Renderer` writing one flat string
   into a single `util.BufWriter`, with `\n\n` between paragraphs. The `[]Block` contract (R5, PRD
   decision 6) that the entire chunker depends on is an architectural change to upstream, not a
   defect fix. This is the plan's largest single under-estimate and it lands in Phase 1.

**Two smaller observations**, both actionable:

- `errTagNotAllowed` is shared by raw HTML, raw inline HTML, and images. The adapter therefore
  cannot distinguish "must fail closed" (R15) from "should degrade" (R12) by error value, and R15's
  requirement to *name* the construct is unmet. Phase 1 splits the error; Phase 4 depends on it.
- `renderCodeSpan` type-asserts `c.(*ast.Text)` unchecked. A non-Text child panics — inside the
  adapter's send path, on user-supplied content.

**The premise holds.** `see https://example.com/a_b and https://example.com/c_d` renders
byte-identical through the CommonMark path. The originating defect does disappear at the root.

## Normalization notes

The PRD's requirements are already EARS-conformant — ubiquitous (`The renderer shall …`),
event-driven (`When …, the renderer shall …`), state-driven (`While …`), optional-feature
(`Where …`), and unwanted-behaviour (`If …, then …`) patterns are all used correctly and each
requirement is atomic and testable. No rewriting was needed.

IDs map 1:1 onto the PRD's numbering: PRD item *N* → `R`*N*, across R1–R33. Section groupings
(Renderer R1–R16, Chunker R17–R23, Adapter R24–R31, Observability R32–R33) are preserved.

One requirement is split across phases for delivery, without changing its text:

- `R25` — single-chunk send lands in Phase 1; multi-chunk ordering lands in Phase 5. The requirement
  is only fully satisfied at the end of Phase 5.

### Additional requirements beyond the source PRD — accepted

The upstream probe found two defects of the same severity as R6–R9 that no PRD requirement covered.
Both were raised as proposals and **accepted by the project owner**. They are in scope, planned into
Phase 2 alongside R6–R9, and carry no different status from any other requirement in this plan:

- `R34`: When the source contains a hard line break within a paragraph, the renderer shall emit a
  newline character.
  *Rationale: `alpha␠␠\ngamma` renders as `alphagamma`. R6 covers the soft case only, but the
  failure and the fix are identical, and fixing one while leaving the other is indefensible.*
- `R35`: When the source contains an ordered list, the renderer shall emit each item prefixed with
  its ordinal.
  *Rationale: `1. first` renders as `- first`. Numbered steps are routine in agent replies and
  renumbering them to bullets loses information the user asked for.*

Both have been added to [`telegram-markdown-rendering.prd.md`](./telegram-markdown-rendering.prd.md)
under *Renderer — added after upstream verification*, so the two documents agree.

## Standard quality gate

Project standard commands:

```bash
just hook     # cached: fmt-check, vet, lint, lint-ui, test, test-ui, llms-check
just check    # same, uncached, full output (CI equivalent)
just scan     # gosec + govulncheck
just test-pkg internal/adapter/telegram
```

- [ ] Run `just hook` as a baseline **before Phase 0**; it must pass on an unmodified tree
- [ ] If the baseline fails, stabilize before starting Phase 0 — do not fold pre-existing failures
      into this work
- [ ] Re-run `just hook` before marking each phase complete; it must pass
- [ ] Run `just scan` additionally at the end of Phase 1 (new direct dependency) and Phase 3
      (link-scheme handling)
- [ ] Coverage thresholds are quality gates. Do not lower one to make a phase pass — add tests.
      Only the project owner may approve a threshold change.

Do not begin phase *N+1* while the gate is failing on phase *N*.

---

## Phase 0: Bot seam and characterization baseline

**EARS requirements**: none — this phase adds no user-visible behaviour.

### Why this phase exists

Nothing in `internal/adapter/telegram/telegram_test.go` exercises a send. Every test constructs
`newWithBot(nil, …)` — a literal nil bot — so the adapter's send paths have never been under test.
The PRD's testing plan ("extend the existing fake-adapter pattern already used to assert on parse
mode in dispatcher tests") points at `internal/agent/dispatcher_test.go`, which fakes the *adapter*,
one layer above where the parse-mode decision is actually made. There is no seam at the layer this
work changes.

Without a seam, every adapter-layer acceptance criterion in Phases 1–7 is unverifiable except by
sending real messages to real Telegram by hand. This phase buys that verifiability once, and pins
today's behaviour so the Phase 1 diff shows exactly what changed.

### Locked decisions (non-negotiable)

- The seam is **send-only and narrow**. `Adapter.bot *tgbotapi.BotAPI` stays exactly as it is —
  `Start` needs `GetUpdatesChan`, `downloadVoiceFile` needs `GetFileDirectURL`, `Stop` needs
  `StopReceivingUpdates`, `New` needs `bot.Self.UserName`. Do not widen a single interface over all
  of `BotAPI`.
- The characterization tests assert **current** behaviour: default `ParseMode == "Markdown"`, and a
  retry with empty `ParseMode` when the first send returns an error. These tests are expected to be
  *rewritten* in Phase 1 — that is the point. They exist to make Phase 1's behaviour change explicit
  and reviewable rather than invisible.
- No behaviour change ships in this phase. A reviewer reading the diff must see test scaffolding and
  an indirection, and no altered outbound bytes.

### Flex zone (implementation choice allowed)

- Interface name, method set, and whether it is one interface or two (`Send`-shaped and
  `Request`-shaped).
- How the seam is injected: a struct field defaulting to the real bot, a functional option, or a
  package-private constructor parameter.
- How `newWithBot(nil, …)` keeps working. A nil `*tgbotapi.BotAPI` assigned to a non-nil interface
  is a live nil-pointer hazard here; handling it explicitly is preferable to relying on every test
  avoiding the send path.
- Fake bot's recording shape (slice of captured `Chattable`, typed accessors, error injection hooks).

### Open questions / risk burn-down

- `tgbotapi.Chattable` is an interface; captured values need a type assertion to
  `tgbotapi.MessageConfig` to read `Text`, `ParseMode` and `ReplyMarkup`. Confirm that assertion
  works cleanly against v5 before building the fake out. Early check: one throwaway test asserting
  on a captured `MessageConfig.ParseMode`.
- `SendAndGetID` needs the fake to return a `tgbotapi.Message` with a usable `MessageID`. Verify a
  zero-value `Message` is distinguishable from a real one, so tests cannot pass on an accident.

### End-to-end behaviour to implement

No external behaviour changes. Internally, `Send`, `SendAndGetID`, `EditText`, `EditMessage` and
`SendTyping` route their outbound calls through an injectable seam. A test-only fake records every
outbound call and can be told to fail on the *n*th call, which Phases 1, 5 and 7 all need.

### Acceptance criteria

- [ ] `[observable]` `just test-pkg internal/adapter/telegram` passes with new tests that capture an
      outbound `MessageConfig` and assert `ParseMode == "Markdown"` on the default `Send` path
- [ ] `[observable]` A characterization test drives the failure path — first send errors, second
      succeeds — and asserts the retry carries an empty `ParseMode`
- [ ] `[observable]` The same two assertions hold independently for `SendAndGetID`, and it returns
      the fake's `MessageID`
- [ ] `[observable]` The fake can inject a failure on a chosen call index, and a test proves the
      injection fires on that index and not another
- [ ] `[structural]` `Adapter.bot` remains `*tgbotapi.BotAPI`; the new seam covers send/request only
- [ ] `[structural]` `git diff` shows no change to any outbound field value — parse modes, text and
      markup are byte-identical to `main`

### Verification

Run `just test-pkg internal/adapter/telegram` and read the new test names — they should describe
today's Markdown behaviour, not tomorrow's HTML behaviour. Run `just hook` for the full gate. Then
confirm the no-op claim directly: build the binary on this branch and on `main`, point both at a
real Telegram bot token, send the same markdown message through each, and confirm both arrive
identically mangled. A Phase 0 that changes what the user sees has failed.

### Replan triggers

- The seam cannot stay narrow — if `Send` turns out to need bot state beyond `Send`/`Request`, stop
  and reconsider whether the fake belongs at the `tgbotapi` layer or behind an HTTP round-tripper.
- Capturing `MessageConfig` from `Chattable` proves unreliable across the v5 API, which would make
  every later adapter assertion fragile.
- Injecting the seam requires touching call sites outside `internal/adapter/telegram`.

---

## Phase 1: Vendored renderer and the HTML send path

**EARS requirements**: R1, R2, R3, R4, R5, R15, R24, R25 *(single-chunk)*, R28, R29, R30

**Carry-forward**: Re-run Phase 0's characterization tests before starting. They are the baseline
this phase deliberately inverts.

### Why this phase exists

This is the tracer bullet, and it is the phase that fixes the reported defect. A message containing
two URLs with underscores must arrive with both underscores intact and both links live. Everything
after this phase is coverage, robustness and observability on a path that already works end to end.

The phase is deliberately the widest in the plan because the fallbacks (R29, R30) cannot lag behind
the parse-mode switch. Shipping HTML without its plain-text safety net would replace a known silent
corruption with an unknown one.

### Locked decisions (non-negotiable)

- The originating defect is the acceptance test. A URL containing `_` survives rendering
  **byte-for-byte**. This is non-negotiable and gets a named regression test.
- Text content escapes exactly `<`, `>`, `&` (R3) and emits **no** backslash escapes for `_`, `*`,
  `` ` ``, `[` (R4). If a literal underscore leaves the renderer with a backslash in front of it,
  the phase has failed regardless of what Telegram renders.
- Raw HTML in the source fails closed with an error naming the construct (R15). It is the only
  fail-closed case in the whole plan — tables and images degrade instead (Phase 4). Upstream's
  single shared `errTagNotAllowed` does not name anything and is reused for images, so it **must**
  be split into distinguishable errors in this phase; Phase 4 cannot degrade images otherwise.
- **The dangerous-scheme denylist does not survive this phase.** Upstream's `IsDangerousURL` is
  case-sensitive (`JaVaScRiPt:` passes through into `href`, measured). Replace it with the Phase 3
  allowlist on arrival, even though R16's full treatment is Phase 3 work. Vendoring a known-bypassable
  URL check and leaving it in place for two phases is not acceptable, and the replacement is small.
- **`Render` returns `[]Block`.** Upstream writes one flat string; this is a structural change, not
  a fix, and it is the bulk of this phase's engineering. Do not treat it as incidental.
- `renderCodeSpan`'s unchecked `c.(*ast.Text)` assertion is made safe. A panic on user content in
  the send path is not acceptable regardless of how unlikely the input is.
- Explicit `ParseMode` is pass-through, unrendered (R28). The activity log at
  `internal/agent/dispatcher.go:1500` sends hand-built, pre-escaped HTML including
  `<blockquote expandable>`; rendering it again would corrupt every approval message.
- Render error → send original text with **no** parse mode (R29). Telegram rejects an HTML send →
  retry once with no parse mode (R30). Both fallbacks send the *original markdown*, never a
  half-rendered string.
- Both `Send` and `SendAndGetID` change together. Leaving one on Markdown would make the defect
  depend on whether the caller wanted an ID back.
- Every block is independently tag-balanced (R5). Phase 5's chunker depends on this and cannot
  defend itself against a violation.

### Flex zone (implementation choice allowed)

- How much of telegold survives. It is a starting map, not a contract — restructure freely so long
  as attribution and licence remain.
- Whether `Render` walks goldmark's AST directly or subclasses goldmark's HTML renderer as upstream
  does.
- `Block`'s concrete fields, as long as Phase 5 can reopen wrapping elements from it.
- The shared escaping helper's location and whether goldmark's own escaping is reused.
- Error type for R15: sentinel, typed, or `fmt.Errorf` — only the "names the construct" property is
  locked.

### Open questions / risk burn-down

- ~~Telegold's real state and goldmark compatibility~~ — **resolved before planning**. Upstream was
  cloned and executed against goldmark v1.8.2: it compiles unmodified, and all four PRD defects
  reproduce exactly as described. See Verified upstream findings. Five further findings are folded
  into this phase's locked decisions and Phase 2's scope.
- The `[]Block` restructure is the real risk in this phase now. Upstream's flat-string architecture
  has to become block-aware, and the tag-balance invariant (R5) is what Phase 5 rests on. Consider
  spiking the block boundary decision — where one block ends and the next begins for a blockquote
  containing a list — before committing to the `Block` shape.
- Promoting goldmark from pruned-transitive to direct may move other versions in `go.sum`. Run
  `go mod tidy` early and review the delta as its own commit.
- Telegram's HTML subset is narrower than it looks. Confirm against Telegram's current API docs
  which tags are accepted before locking the allowlist in code.

### End-to-end behaviour to implement

A single-block markdown reply sent through the Telegram adapter with no explicit parse mode is
parsed as CommonMark, rendered to Telegram-subset HTML, and sent with `parse_mode=HTML`. If
rendering errors, the original markdown goes out with no parse mode. If Telegram rejects the HTML,
the original markdown is retried once with no parse mode. Messages carrying an explicit parse mode
bypass all of this untouched. Multi-block replies may still be sent as one message this phase —
chunking arrives in Phase 5.

### Acceptance criteria

- [ ] `[observable]` Golden test: `See https://example.com/a_b and https://example.com/c_d` renders
      with both underscores present and both URLs byte-identical to input
- [ ] `[observable]` Golden tests cover R3 (`<`, `>`, `&` escaped) and R4 (literal `_ * ` [` pass
      through with no backslash), each as a named case
- [ ] `[observable]` A source containing raw HTML returns an error whose message names the construct
      (R15), distinguishable from the image error, and the adapter test for that input asserts a
      send with empty `ParseMode` (R29)
- [ ] `[observable]` `[click](JaVaScRiPt:alert(1))` does not produce an anchor — the case-sensitivity
      bypass measured in upstream is closed on arrival
- [ ] `[observable]` Adapter test: markdown in, `ParseMode == "HTML"` out, on both `Send` and
      `SendAndGetID` (R24, R25)
- [ ] `[observable]` Adapter test: `OutgoingMessage{ParseMode: "HTML"}` reaches the bot with text
      byte-identical to input — no rendering applied (R28)
- [ ] `[observable]` Adapter test: injected send failure on the HTML attempt produces exactly one
      retry carrying the original text and empty `ParseMode` (R30)
- [ ] `[structural]` Every block returned by `Render` is tag-balanced, asserted by a helper the
      chunker tests will reuse in Phase 5 (R5)
- [ ] `[structural]` `tghtml/LICENSE` present; attribution header on each vendored file; `go.mod`
      lists goldmark as a direct dependency

### Verification

Run the golden suite and the adapter suite via `just test-pkg internal/adapter/telegram`, then
`just hook`, then `just scan` for the new dependency. Then verify against the real thing: build,
point at a live bot, and send the exact message from the bug report — two URLs each containing an
underscore. Both links must be clickable and resolve. Also send a message containing a literal
`snake_case_identifier` in prose and confirm it appears with all underscores. Finally, trigger a
supervised tool approval so the activity log renders, and confirm the approval message and its
inline keyboard are unaffected.

### Regression watchpoints

- **The activity log** (`internal/agent/dispatcher.go`, `renderChunk`/`flush`). It passes
  `ParseMode: "HTML"` and hand-built markup. If R28's pass-through is wrong, every approval message
  breaks, including the inline keyboards. Highest blast radius in the phase.
- **The Discord adapter.** It must remain untouched. Any change under `internal/adapter/discord/` in
  this phase's diff is a mistake.
- **Voice replies.** `Send` returns early for the TTS path (`telegram.go:299`). Rendering must not
  run before that branch, or TTS starts synthesizing HTML tags aloud.
- **`EditText` / `EditMessage`.** Both take an explicit parse mode from callers today. They are not
  in this phase's scope, but confirm the seam change did not alter them.
- **Non-LLM adapter messages** — the `/debug` toggle confirmation, voice-failure notices — currently
  go out as plain text with no parse mode. Confirm they still render sensibly once that path means
  "render as CommonMark".

### Replan triggers

- Telegold's actual defects diverge materially from the PRD's four, changing Phase 2's scope
- Vendoring proves a worse starting point than writing the renderer fresh against goldmark's AST
- Telegram's HTML subset turns out to exclude a tag the plan depends on
- The underscore-URL regression test cannot be made to pass — the premise of the whole plan
- Promoting goldmark forces an unrelated dependency upgrade with its own breakage

---

## Phase 2: Fix the vendored-in defects

**EARS requirements**: R6, R7, R8, R9, R34, R35

**Carry-forward**: Re-verify Phase 1 before starting — at minimum the underscore-URL golden test and
one live Telegram send.

### Why this phase exists

Upstream telegold carries measured defects, and one is disqualifying on its own: dropped line breaks
fuse the last word of a wrapped source line to the first word of the next. This is not theoretical —
`alpha beta\ngamma delta` was measured rendering as `alpha betagamma delta`. LLM output is wrapped
prose, so this would corrupt a large share of ordinary replies: a different corruption from the one
we just fixed, shipped by our own hand.

The rest are visible quality defects in routine agent output: a malformed `<hr* * *` tag that
Telegram will reject outright, block quotations as a literal `&gt;` with their internal lines fused
as well, flattened nested lists, and — beyond the PRD — dropped *hard* line breaks (R34) and ordered
lists renumbered into bullets (R35).

### Locked decisions (non-negotiable)

- Soft line break inside a paragraph emits a newline character (R6). Words must never fuse.
- Thematic break emits a **text** separator containing no HTML element (R7) — upstream's malformed
  tag is removed, not repaired.
- Block quotation emits a real `blockquote` element (R8), not a literal `>` character.
- Nested lists indent each nested item relative to its parent (R9). Telegram's HTML subset has no
  list elements, so indentation is textual — that constraint is locked, the exact whitespace is not.
- Hard line breaks emit a newline (R34) — same treatment as R6, fixed in the same place.
- Ordered list items carry their ordinal (R35). `ast.List.IsOrdered()` and `Start` are the inputs;
  upstream ignores both.
- Each fix gets a golden test derived from the probe that found it, with the measured broken output
  quoted in a comment so a regression is recognisable on sight: a paragraph wrapped across two
  source lines, a hard-break paragraph, a thematic break, a block quotation spanning two lines, a
  two-level nested list, and an ordered list.
- The block quotation test asserts **both** its defects are fixed — the element *and* the internal
  line break. Fixing only the visible one leaves the quote's lines fused.

### Flex zone (implementation choice allowed)

- The separator string for R7 (e.g. a run of em dashes or hyphens) and its length.
- Indent unit and character for R9 (spaces vs non-breaking spaces, width per level).
- Whether nesting depth is capped, and at what depth deep nesting stops indenting further.
- Whether fixes are applied to the vendored renderer in place or by overriding its node handlers.

### Open questions / risk burn-down

- Telegram collapses some leading whitespace in HTML mode. Confirm empirically which indent
  character actually survives to the rendered message before locking R9's implementation — a fix
  that is invisible on the device is not a fix. Early check: send both space- and
  non-breaking-space-indented nested lists to a live chat and compare.
- `blockquote` nesting rules in Telegram's subset may forbid a quote inside a list item. Probe a
  nested case and decide degradation behaviour if it is rejected.

### End-to-end behaviour to implement

A reply containing wrapped paragraph prose, a horizontal rule, a block quotation and a two-level
bullet list arrives in Telegram with words unfused, a visible separator line, a real quote block, and
visibly indented sub-items.

### Acceptance criteria

- [ ] `[observable]` Golden test: a paragraph wrapped across two source lines renders with a newline
      between them and no fused words (R6)
- [ ] `[observable]` Golden test: a thematic break renders as text containing no `<` character (R7)
- [ ] `[observable]` Golden test: `> quoted` renders as a `blockquote` element (R8)
- [ ] `[observable]` Golden test: a two-level nested list renders with the second level indented
      relative to the first (R9)
- [ ] `[observable]` Golden test: a hard line break emits a newline (R34)
- [ ] `[observable]` Golden test: `1. first\n2. second` retains its ordinals, and a list starting at
      a non-1 ordinal is honoured rather than renumbered from 1 (R35)
- [ ] `[observable]` A live Telegram send of a message containing every construct renders correctly
      on the device
- [ ] `[structural]` All blocks remain tag-balanced under the Phase 1 helper (R5 still holds)

### Verification

Run the golden suite, then `just hook`. Then the device check, which is the one that matters for R9:
send a message with a two-level nested list to a real chat and look at it. Indentation that exists in
the HTML but collapses on the device fails this phase. Send a realistic multi-paragraph LLM-style
reply and read it for fused words.

### Regression watchpoints

- Phase 1's escaping. A separator or indent string containing `<`, `>` or `&` must be escaped like
  any other text content.
- Tag balance. `blockquote` is the first multi-line wrapping element introduced; an unclosed one
  breaks Phase 5's chunker invariant before that chunker exists to catch it.

### Replan triggers

- Telegram silently strips the chosen indent character, and no available character survives
- A fix requires restructuring the renderer's traversal in a way that invalidates Phase 1's
  block-boundary contract
- The probe reveals a fifth defect of comparable severity

---

## Phase 3: Inline and block typography

**EARS requirements**: R10, R13, R14, R16

**Carry-forward**: Re-verify Phases 1–2 — golden suite plus one live send containing wrapped prose.

### Why this phase exists

Headings, fenced code blocks and strikethrough are routine in LLM replies; a heading that renders as
a literal `##` or a code block that loses its language annotation is exactly the unstyled outcome
this work exists to avoid. R16 is bundled here because it is the security-relevant requirement in the
renderer and belongs with the other inline-span work rather than trailing behind it.

### Locked decisions (non-negotiable)

- Strikethrough emits `s` (R10).
- Fenced code with a language annotation emits `pre`-wrapped `code` carrying the corresponding
  language class (R13) — the nesting order is `pre` outside, `code` inside, per Telegram's subset.
- Heading emits `b` followed by a line break (R14), at every heading level. Telegram has no heading
  element; level information is not otherwise representable.
- **Link scheme allowlist** (R16): `http`, `https`, `tg`, `mailto` produce anchors. Everything else
  emits the label as escaped text with no anchor element. Allowlist, never denylist. A link dropped
  to plain text is an acceptable outcome; a `javascript:` anchor is not.
- **The anchor is dropped, not blanked.** Upstream emits `<a href="">label</a>` for a rejected
  destination (measured). R16 requires the label with *no* anchor element, so this is a correction.
  An empty-href anchor is also a plausible Telegram rejection, which would route the whole message
  to plain text.
- Autolinks (`ast.KindAutoLink`) go through the same allowlist. Upstream writes their href
  unconditionally, bypassing its scheme check entirely.
- Code block content is escaped as text content — a `<` inside a code fence must not become markup.

### Flex zone (implementation choice allowed)

- Language class naming, subject to Telegram accepting it (upstream convention is `language-xxx`).
- Whether unannotated fenced code emits bare `pre` or `pre`+`code`.
- Whether heading levels differ visually beyond bold (e.g. leading marker for deeper levels).
- Where scheme validation lives and how the URL is parsed, provided parsing is total — a malformed
  URL must not panic and must not fail open.

### Open questions / risk burn-down

- Whether Telegram accepts `tg:` in anchor `href`. If not, drop it from the allowlist; this is a
  one-line change and the requirement is satisfied either way.
- Scheme-relative (`//host/path`) and relative destinations have no scheme to check. Decide
  explicitly: treat as not-allowlisted and render the label. Confirm this is acceptable for typical
  agent output before locking.
- Case and whitespace tricks must not slip past the allowlist. This is not hypothetical: upstream's
  denylist was measured passing `JaVaScRiPt:alert(1)` straight into an `href`. Phase 1 closes it;
  this phase's tests are what keep it closed. Include leading control characters and embedded
  newlines alongside the case-varied cases.

### End-to-end behaviour to implement

A reply containing a heading, a language-annotated fenced code block, a strikethrough span, and both
an ordinary link and a `javascript:` link arrives with the heading bold on its own line, the code
block monospaced and syntax-tagged, the strikethrough struck, the ordinary link clickable, and the
`javascript:` link present as plain unclickable text.

### Acceptance criteria

- [ ] `[observable]` Golden tests for R10, R13 and R14, one named case each
- [ ] `[observable]` Golden test: a `javascript:` destination renders its label with no anchor
      element, and the same holds for `data:`, `vbscript:` and `file:` (R16)
- [ ] `[observable]` Golden test: case-varied and whitespace-padded dangerous schemes are also
      rejected — the allowlist is not defeated by `JaVaScRiPt:` or a leading space
- [ ] `[observable]` Golden test: a fenced code block containing `<script>` renders escaped, not as
      markup
- [ ] `[observable]` A live send containing all four constructs renders correctly, and the
      `javascript:` link is not clickable
- [ ] `[observable]` `just scan` passes with no new gosec finding on the URL-handling code

### Verification

Golden suite plus `just hook` plus `just scan`. Then the device check: send a message with a
`javascript:` link and confirm Telegram neither renders it as an anchor nor autolinks it into one —
Telegram autolinks post-parse text, so a bare `javascript:...` string in the output needs looking at
directly, not assuming. Send a Go code fence and confirm monospacing.

### Regression watchpoints

- Telegram autolinking plain text. Dropping an anchor does not guarantee the destination stays
  inert; verify on the device rather than in the golden file.
- Phase 1's R4 guarantee. Backtick handling for code spans must not start escaping literal backticks
  in ordinary prose.

### Replan triggers

- Telegram rejects the chosen language class format, and no accepted format carries the language
- A scheme-validation approach cannot be made total without a dependency
- `tg:` proves unsupported *and* is needed by existing agent output

---

## Phase 4: Degrade tables and images

**EARS requirements**: R11, R12

**Carry-forward**: Re-verify Phases 1–3, including the R16 hostile-scheme cases.

### Why this phase exists

Upstream fails closed on tables and images. LLM replies contain tables routinely, so fail-closed
would route a large share of ordinary messages to the unstyled plain-text fallback — discarding
exactly the formatting this work exists to deliver. PRD decision 4 makes degradation the rule and
raw HTML (R15) the sole remaining fail-closed case. This phase is small in requirement count and
disproportionately large in how often it fires.

### Locked decisions (non-negotiable)

- A table renders as a **preformatted block that preserves column alignment** (R11). Telegram's
  subset has no table elements; monospaced fixed-width columns are the representable form.
- An image renders as an **anchor whose label is the image's alternative text** (R12). Telegram's
  HTML subset cannot embed an image inline in a text message.
- Neither construct returns an error. After this phase, only R15 (raw HTML) fails closed. Upstream
  returns the *same* `errTagNotAllowed` for images and raw HTML; Phase 1 splits them, and this phase
  depends on that split having happened.
- GFM table parsing requires `goldmark/extension` — enabled at parser construction alongside the
  strikethrough extension from Phase 3.
- **Do not enable `extension.GFM` wholesale.** It bundles `linkify`, which autolinks bare URLs and
  would emit `<a>` elements around the exact underscore-bearing URLs that motivated this work.
  Today they render as plain text and Telegram autolinks them post-parse (measured). Select the
  `Table` and `Strikethrough` extensions individually.

### Flex zone (implementation choice allowed)

- Column width algorithm, padding character, whether a header separator row is drawn, and how
  over-wide tables are handled (truncate cells, wrap, or let the Phase 5 chunker split them).
- Whether alignment markers from the source (`:---`, `---:`) are honoured or all columns left-align.
- Image anchor behaviour when alt text is empty — fall back to the URL, to a placeholder, or drop.
- Whether the image destination goes through Phase 3's scheme allowlist. Recommended: yes, reuse it.

### Open questions / risk burn-down

- Fixed-width alignment assumes monospaced rendering, which assumes `pre`. Confirm Telegram renders
  `pre` monospaced on both mobile and desktop before locking the column algorithm — misaligned
  columns are worse than no table.
- Wide tables will routinely exceed a chunk. Decide now whether Phase 4 truncates or defers to
  Phase 6's block splitting; deferring is cleaner but means wide tables look wrong until Phase 6.
- CJK and emoji cells break fixed-width alignment because display width ≠ rune count. Decide whether
  to attempt width-aware padding or accept misalignment for non-Latin content, and write the choice
  down.

### End-to-end behaviour to implement

A reply containing a three-column markdown table and an inline image arrives with the table as an
aligned monospaced block and the image as a clickable link labelled with its alt text. Neither
triggers the plain-text fallback.

### Acceptance criteria

- [ ] `[observable]` Golden test: a three-column table renders as a preformatted block with columns
      aligned across all rows (R11)
- [ ] `[observable]` Golden test: `![alt](url)` renders as an anchor labelled `alt` (R12)
- [ ] `[observable]` Neither input returns an error — asserted explicitly, since fail-closed is the
      upstream behaviour being changed
- [ ] `[observable]` Golden test: an image with a `javascript:` destination renders without an
      anchor, consistent with R16
- [ ] `[observable]` A live send of a realistic three-column table renders with visibly aligned
      columns on a mobile client
- [ ] `[structural]` Both constructs emit tag-balanced blocks (R5 still holds)

### Verification

Golden suite and `just hook`, then the device check — open the message on a phone and look at the
columns. This phase's whole value is that it does not fall back, so also confirm the fallback counter
groundwork is not firing: after Phase 7 this becomes a metric assertion; until then, check the logs
are quiet on a table-bearing reply.

### Regression watchpoints

- Enabling `goldmark/extension` changes parsing for *all* input, not just tables. Re-run the full
  golden suite from Phases 1–3 after enabling it.
- **The underscore-URL regression test is the one to watch.** If `linkify` sneaks in via
  `extension.GFM`, bare URLs start emitting anchors and the originating defect's test case changes
  shape. It should still pass — CommonMark still won't eat the underscores — but a change in that
  test's expected output during this phase needs justifying, not updating.
- Pipe characters in ordinary prose may now parse as table syntax. Add a case.

### Replan triggers

- Telegram does not render `pre` monospaced consistently across clients, invalidating alignment
- Enabling GFM extensions regresses an earlier golden test in a way that cannot be resolved by
  selecting individual extensions
- Table degradation proves to need block splitting to be usable at all, forcing Phase 6 earlier

---

## Phase 5: Chunker and multi-message send

**EARS requirements**: R17, R18, R19, R20, R21, R23, R25 *(multi-chunk)*, R26, R27, R31

**Carry-forward**: Re-verify Phases 1–4. The full golden suite must pass — the chunker's safety
depends entirely on the renderer's tag-balance invariant.

### Why this phase exists

Long replies fail today. There is no chunking on this path at all; only the activity log chunks
(`internal/agent/dispatcher.go:1490`). Emitting HTML makes naive splitting actively dangerous,
because a split can land inside an element. This is the second tracer bullet: a reply longer than
Telegram's cap arrives as several correctly-formatted messages instead of one rejection.

### Locked decisions (non-negotiable)

- Chunk length never exceeds the supplied limit (R17). The limit is a parameter; the chunker does not
  know about 4096.
- Source order of blocks is preserved across chunks (R18). Reordering is worse than not chunking.
- Every emitted chunk is tag-balanced (R19). This is asserted on **every** chunk in **every** chunker
  test, not on a sampled subset.
- Greedy packing: while the active chunk has capacity, append (R20); when appending would exceed the
  limit, start a new chunk (R21).
- Split points move back to the preceding rune boundary (R23). Never emit a partial UTF-8 sequence.
- Inline keyboard attaches to the **final** chunk only (R26); a multi-chunk `SendAndGetID` returns
  the **final** chunk's ID (R27). Both per PRD decision 11.
- A failed chunk aborts the remaining chunks and returns an error (R31). No partial-recovery, no
  best-effort continuation — a gap in the middle of a reply is worse than a truncated one.
- Oversized single blocks are **out of scope this phase** — that is R22, Phase 6. Until then an
  oversized block may exceed the limit; the chunker must not crash, loop, or silently drop it.

### Flex zone (implementation choice allowed)

- The adapter's headroom constant below 4096, and how it is derived.
- Whether blocks are joined with a newline, a blank line, or nothing, and whether the joiner counts
  against the limit (it must, if used).
- Interim behaviour for oversized blocks before Phase 6 — emit alone and over-limit, or truncate.
  Document whichever is chosen; do not leave it implicit.
- Whether `Send` and `SendAndGetID` share one internal chunked-send helper (recommended).

### Open questions / risk burn-down

- Rate limiting. Sending several messages in immediate succession may hit Telegram's per-chat flow
  limits, surfacing as a 429 mid-sequence. R31 says abort — confirm that is the desired user
  experience for a rate limit specifically, or whether a bounded backoff belongs here. Probe with a
  deliberately long reply against a live bot.
- Telegram counts message length in UTF-16 code units of entity-stripped text, while the chunker
  counts raw HTML bytes. This over-counts, which is safe, but heavily-tagged content may chunk more
  eagerly than necessary. Measure on a realistic long reply and confirm the chunk count is sane.
- Interaction with the voice path: `IsVoice` returns before the text path. Confirm chunking is not
  reachable from a TTS send.

### End-to-end behaviour to implement

A reply longer than the limit is rendered to blocks, packed greedily into ordered chunks each within
the limit and each tag-balanced, and sent as several Telegram messages in source order. Buttons ride
on the last message; `SendAndGetID` returns the last message's ID. A failure on any chunk stops the
sequence and returns an error.

### Acceptance criteria

- [ ] `[observable]` Chunker tests with a small synthetic limit cover exact-fit, one-over, and
      multi-chunk packing (R17, R20, R21)
- [ ] `[observable]` Every chunk in every chunker test passes the tag-balance helper, and block order
      across chunks matches source order (R18, R19)
- [ ] `[observable]` A split that would land mid-rune moves back to the rune boundary; the
      concatenated chunks decode as valid UTF-8 identical to the input (R23)
- [ ] `[observable]` Adapter test: a long reply produces N sends in order, with buttons on the last
      only and none on the others (R25, R26)
- [ ] `[observable]` Adapter test: multi-chunk `SendAndGetID` returns the final chunk's ID (R27)
- [ ] `[observable]` Adapter test: an injected failure on chunk 2 of 4 produces exactly 2 send
      attempts and a returned error (R31)
- [ ] `[observable]` A live send of a >4096-character reply arrives as multiple correctly-formatted
      messages with no lost or reordered content

### Verification

Run the chunker and adapter suites, then `just hook`. Then the device check, which is where ordering
and duplication problems actually show up: send a long numbered reply (say 60 numbered lines with
mixed formatting) to a live chat, and read the received messages end to end confirming every number
appears exactly once, in order, with formatting intact across the boundaries. Separately trigger a
multi-chunk approval-bearing message and confirm the buttons appear under the last message only.

### Regression watchpoints

- Single-chunk replies must still send as exactly one message. The common case must not gain an
  extra empty message or a trailing separator.
- The activity log has its own chunker and its own explicit HTML parse mode. It must not start
  routing through this one.
- Message ID semantics. Callers of `SendAndGetID` — the activity log's edit path — depend on the
  returned ID being editable. Returning a mid-sequence ID would make edits land on the wrong message.
- Dispatcher in-flight tracking keys by `adapter:externalID`; confirm multi-message sends do not
  disturb it.

### Replan triggers

- Telegram rate limiting makes abort-on-failure (R31) an unacceptable user experience, requiring a
  retry policy the PRD does not specify
- Byte-count over-counting produces absurd chunk counts on realistic replies
- The tag-balance invariant cannot be maintained across a boundary without Phase 6's split logic,
  forcing the two phases to merge

---

## Phase 6: Oversized block splitting

**EARS requirements**: R22

**Carry-forward**: Re-verify Phase 5 in full — every chunker test and the long-reply device check.

### Why this phase exists

One requirement, but the hardest correctness problem in the plan, which is why it gets its own
session. A single fenced code block longer than the limit is the realistic case and Phase 5 cannot
place it. Truncating loses content silently — the exact failure class this work exists to eliminate.
Falling back to plain text for the whole message penalises everything else in the reply. So the block
splits, and its wrapping elements close at the end of one chunk and reopen at the start of the next.

### Locked decisions (non-negotiable)

- An oversized block is split, with wrapping elements closed and reopened across the split (R22).
- Every resulting chunk remains tag-balanced (R19 continues to hold) and within the limit (R17).
- Rune-boundary correction (R23) applies to splits inside a block, not just between blocks.
- No content is lost. Concatenating the split chunks' text content reproduces the original block's
  text content exactly.
- No silent truncation anywhere in this path.

### Flex zone (implementation choice allowed)

- Split-point preference: newline, then whitespace, then hard rune boundary.
- How nested wrapping is handled (`pre` > `code` is the realistic depth) and whether nesting deeper
  than that is supported or degraded.
- Whether the language class is repeated on each reopened `code`, or only the first.
- Whether a continuation marker is added, and its text if so.

### Open questions / risk burn-down

- Telegram may render two consecutive `pre` blocks with visible separation that reads as a broken
  code block. Check on the device; if it is jarring, a continuation marker may be warranted.
- Reopening tags consumes budget. A block split into chunks must account for the reopened prefix and
  the closing suffix against the limit, or the split chunks themselves exceed it. This is the most
  likely place for an off-by-N — write the test before the code.
- Pathological input: a single 100KB code block, or a block whose wrapping tags alone approach the
  limit. Confirm the algorithm terminates and does not loop.

### End-to-end behaviour to implement

A reply containing a fenced code block several times the limit arrives as several messages, each a
correctly-formed, syntax-tagged code block, together containing the original code in order with
nothing lost.

### Acceptance criteria

- [ ] `[observable]` Chunker test: a single block exceeding the limit splits into multiple chunks,
      each within the limit and each tag-balanced (R22, R17, R19)
- [ ] `[observable]` Test: concatenated text content of the split chunks equals the original block's
      text content exactly — no loss, no duplication
- [ ] `[observable]` Test: a `pre`-wrapped `code` block splits with both elements closed and reopened
      on each chunk, language class preserved per the chosen policy
- [ ] `[observable]` Test: a split landing mid-rune inside an oversized block moves to the preceding
      boundary; all chunks are valid UTF-8
- [ ] `[observable]` Test: a block whose wrapping overhead is large relative to the limit still
      produces chunks within the limit, and the algorithm terminates
- [ ] `[observable]` A live send of a ~10,000-character code block arrives as several readable,
      correctly-formatted code blocks

### Verification

Chunker suite and `just hook`. Then the device check that matters: send a long Go file as a fenced
block and read the received messages — the code must be complete, in order, monospaced throughout,
with no visible tag fragments and no dropped lines. Diff the reassembled received text against the
source to prove no loss rather than eyeballing it.

### Regression watchpoints

- Phase 5's between-block packing must not change. Blocks that fit continue to pack greedily.
- Budget accounting. Reopened tags counted against the wrong chunk is the classic bug here and shows
  up as an over-limit chunk only on specific input sizes — test at the boundary, not just well over.
- Tables from Phase 4, which are also `pre`-wrapped, now become splittable. Confirm a wide table
  splits without destroying alignment, or document that it does.

### Replan triggers

- Split code blocks render unacceptably on the device, requiring a different strategy such as
  attaching oversized code as a document
- Reopening logic cannot be made general across wrapping elements, forcing per-element special cases
  that outgrow this phase
- Any test demonstrates content loss that cannot be fixed within the current chunker structure

---

## Phase 7: Fallback counter and warning log

**EARS requirements**: R32, R33

**Carry-forward**: Re-verify Phases 1–6, focusing on the fallback paths this phase instruments —
render error (R29) and HTML rejection (R30).

### Why this phase exists

This is the PRD's most transferable lesson. The originating defect was undetectable from inside the
system: no error, no log, no metric. It surfaced only because a human compared a rendered message
against the audit log. The fallback counter converts the next instance of this class from an
archaeology exercise into an alert.

It lands last, deliberately: by now every fallback reason exists, so the counter's reason attribute
has a complete and stable value set on first release rather than growing phase by phase.

### Locked decisions (non-negotiable)

- Every plain-text fallback increments an `Int64Counter` carrying a **reason** attribute (R32).
- Every plain-text fallback emits a **warning**-level `slog` record identifying the reason (R33).
  Warning, not debug — today's code logs these at debug (`telegram.go:334`, `telegram.go:551`),
  which is a large part of why the defect was invisible.
- Both fallback causes are distinguishable by reason: render failure (R29) and Telegram rejection of
  an HTML send (R30). A single undifferentiated counter does not satisfy R32.
- The counter is created at adapter construction with `otel.Meter("denkeeper.telegram")` and stored
  on the struct, matching `internal/agent/dispatcher.go:142`. No new exporter wiring.
- Attribute cardinality stays bounded: reason is a small closed set of constants, never an error
  string. An unbounded label set would degrade the Prometheus endpoint.
- No configuration surface (PRD decision 9).

### Flex zone (implementation choice allowed)

- Metric name, subject to the `denkeeper.*` convention.
- Reason constant spellings and whether additional low-cardinality attributes are carried.
- Whether the counter and log emission share one helper (recommended — they must never diverge).
- Whether the existing debug-level lines are removed or promoted in place.

### Open questions / risk burn-down

- Confirm the adapter is constructed after the OTel meter provider is initialised in
  `cmd/denkeeper/main.go`. A counter built against the global no-op provider records nothing and
  fails silently — precisely the failure mode this phase exists to prevent. Check the wiring order
  before writing the counter, and assert the metric appears on `/metrics` rather than assuming.
- Decide whether Phase 6's per-block degradations warrant their own reason values or stay
  uninstrumented. R32 covers plain-text fallback only; anything beyond is scope growth.

### End-to-end behaviour to implement

When the adapter sends plain text because rendering failed or because Telegram rejected the HTML, it
increments a counter tagged with the reason and writes a warning log naming it. The counter is
visible on the existing `/metrics` endpoint and is usable as an alert source.

### Acceptance criteria

- [ ] `[observable]` Adapter test: a render failure increments the counter with the render-failure
      reason and emits exactly one warning record naming it (R32, R33)
- [ ] `[observable]` Adapter test: an injected HTML rejection increments the counter with the
      send-rejection reason and emits one warning record (R32, R33)
- [ ] `[observable]` Adapter test: a successful HTML send increments nothing and logs no warning —
      the metric must be quiet on the happy path or it is useless as an alert
- [ ] `[observable]` The counter appears on `/metrics` with its reason label after a fallback is
      triggered on a running instance
- [ ] `[structural]` Reason values are a closed set of named constants, not interpolated error text
- [ ] `[structural]` The former debug-level fallback logs are gone or promoted — no fallback path
      logs below warning

### Verification

Run the adapter suite and `just hook`. Then verify on a running instance, because a silently no-op
meter passes unit tests: start the binary with OTel enabled, force a render failure by sending a
message containing raw HTML, then `curl` `/metrics` and confirm the counter is present with the
expected reason label and a non-zero value. Confirm the warning appears in the logs at warning level.
Then send a normal message and confirm the counter does not move.

### Regression watchpoints

- Log volume. A reason that fires on ordinary content would flood warnings. If the counter moves on
  routine replies during the device check, that is a Phase 1–6 defect this phase has just made
  visible — investigate it rather than lowering the log level.
- Metric cardinality on the Prometheus endpoint.
- The adapter had no metrics before this; confirm construction changes do not alter behaviour when
  the meter provider is absent, such as in tests using `newWithBot`.

### Replan triggers

- The meter provider is not initialised before adapter construction, requiring a wiring change in
  `cmd/denkeeper/main.go` beyond this phase's scope
- The counter fires on routine content, indicating an unresolved renderer defect
- Bounded reason values prove insufficient to diagnose real failures, suggesting the observability
  design needs revisiting

---

## Requirements coverage matrix

| Requirement ID | Phase(s) | Notes |
|---|---|---|
| R1 | Phase 1 | CommonMark parsing via vendored goldmark |
| R2 | Phase 1 | Telegram HTML subset; allowlist confirmed against current API docs |
| R3 | Phase 1 | `<`, `>`, `&` escaped in text content |
| R4 | Phase 1 | Literal `_ * ` [` preserved — the originating defect |
| R5 | Phase 1 | Tag-balance invariant; Phase 5's chunker depends on it |
| R6 | Phase 2 | Measured: `alpha beta\ngamma` → `alpha betagamma` |
| R7 | Phase 2 | Measured: emits `<hr* * *` |
| R8 | Phase 2 | Measured: `&gt;` literal **and** internal lines fused |
| R9 | Phase 2 | Measured: nesting flattened to one level |
| R10 | Phase 3 | Requires `Strikethrough` extension, selected individually |
| R11 | Phase 4 | Degrade, not fail closed; requires `Table` extension |
| R12 | Phase 4 | Degrade; needs Phase 1's error split to distinguish from R15 |
| R13 | Phase 3 | `pre` > `code` with language class |
| R14 | Phase 3 | All heading levels → `b` + line break |
| R15 | Phase 1 | Only fail-closed case; upstream's shared error names nothing — must be split |
| R16 | Phase 3 | Scheme **allowlist**; corrects upstream's empty-href anchor and case bypass |
| R17 | Phase 5 | Limit is a parameter; re-verified in Phase 6 |
| R18 | Phase 5 | Source order preserved |
| R19 | Phase 5 | Asserted on every chunk in every test; re-verified in Phase 6 |
| R20 | Phase 5 | Greedy packing |
| R21 | Phase 5 | New chunk on overflow |
| R22 | Phase 6 | Oversized block split with tag close/reopen |
| R23 | Phase 5, Phase 6 | Between blocks in Phase 5; within a block in Phase 6 |
| R24 | Phase 1 | Outgoing text is CommonMark |
| R25 | Phase 1, Phase 5 | Single-chunk in Phase 1; multi-chunk ordering in Phase 5 |
| R26 | Phase 5 | Buttons on final chunk only |
| R27 | Phase 5 | Final chunk's ID returned |
| R28 | Phase 1 | Protects the activity log — highest blast radius |
| R29 | Phase 1 | Render error → plain text; ships with the parse-mode switch |
| R30 | Phase 1 | Telegram rejection → one plain-text retry; ships with the switch |
| R31 | Phase 5 | Abort remaining chunks on failure |
| R32 | Phase 7 | Counter with bounded reason attribute |
| R33 | Phase 7 | Warning level, not debug |
| R34 | Phase 2 | Beyond source PRD, accepted. Measured: `alpha␠␠\ngamma` → `alphagamma` |
| R35 | Phase 2 | Beyond source PRD, accepted. Measured: `1. first` → `- first` |

All 35 requirements are mapped — the PRD's 33 plus R34 and R35, both accepted. No gaps. R23 and R25
appear in two phases each, by design and noted above; no other requirement is duplicated.

R34 and R35 came out of executing upstream rather than reading it, and were added to the PRD after
acceptance — see *Renderer — added after upstream verification* there.

**Phase 0 carries no requirement IDs** — it delivers the test seam that makes every adapter-layer
criterion in Phases 1, 5 and 7 verifiable without sending live messages by hand.
