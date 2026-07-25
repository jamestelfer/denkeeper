# Progress: Telegram Markdown Rendering

> Plan: [`telegram-markdown-rendering.plan.md`](./telegram-markdown-rendering.plan.md)

## Phases

- [x] Phase 0: Bot seam and characterization baseline
- [ ] Phase 1: Vendored renderer and the HTML send path
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
