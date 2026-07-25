Work on the Telegram markdown rendering plan on branch `claude/prd-to-plan-workspace-g5zvpj`.

Read these first, in order:

- `plans/telegram-markdown-rendering.prd.md` — requirements, R1–R35
- `plans/telegram-markdown-rendering.plan.md` — phases, locked decisions, quality gate
- `plans/telegram-markdown-rendering.progress.md` — phase checkboxes and lessons learned

**Scope: one phase per session — the phase named when this prompt was invoked.** If no phase was
named, run the pre-Phase-0 baseline and then Phase 0 only (bot seam and characterization baseline).
Stop at that phase's gate and report; do not begin the next phase.

## Implementation approach

Implement with the `work:tdd` skill. Invoke it before writing any code and follow its loop: one test
derived from the requirement, then the minimal code to pass it, then the next. Do **not** write all
the tests for a phase and then all the implementation — the skill names horizontal slicing as its
primary anti-pattern, and this plan's phases are large enough to make it tempting. Never refactor
while red.

Do not stop to get approval on the behaviour list or the interface shape. Each phase's "Flex zone" is
genuinely open — choose, write the choice down in "Lessons learned", and proceed. Work autonomously
through to the phase gate.

## Process

1. Run `just hook` before touching anything. For Phase 0 this is the plan's required baseline on an
   unmodified tree; for later phases it confirms the carry-forward. If it fails, stop and report — do
   not fold pre-existing failures into this work.
2. Work the red/green loop through the phase's acceptance criteria. They are the definition of done:
   `[observable]` items need a test that fails without the change; `[structural]` items are verified
   in the diff.
3. Re-run `just hook` before marking the phase complete. It must pass. Phases 1 and 3 additionally
   require `just scan` — the new direct dependency and the link-scheme handling respectively.
4. Commit with a conventional-commit message scoped to the phase.

## Verification is test-only — no live bot

**No phase in this plan requires a Telegram bot token, a real chat, or a look at a device.** The
plan's "No live bot in any phase" section is binding. Every acceptance criterion is assertable from
`go test`:

- Renderer behaviour → golden tests on `Render` output.
- Adapter behaviour → assertions on `tgbotapi.MessageConfig` values captured by the Phase 0 fake bot.
- Multi-message behaviour → concatenate the fake's captured sends in call order and assert against
  the source input.
- Metrics → a real OTel SDK meter provider backed by `metric.NewManualReader()`, collected and
  asserted in-process.

If you find yourself wanting a token to answer a question, the question is about **appearance** —
what Telegram displays — not about **behaviour**, which is what we send. For those: decide from
Telegram's published API documentation, pin the resulting bytes with a test so the decision cannot
silently regress, and record the appearance question in "Lessons learned" as one for system testing.
System testing is a separate exercise tracked outside this plan. Never add a test that needs
credentials, and never leave a criterion unverified on the grounds that it needs a device.

Broken markdown being forwarded verbatim is itself a unit-testable fact — that is what the Phase 0
characterization tests pin, and what Phase 1 inverts at the same assertion sites.

## Constraints

- Treat every "Locked decisions (non-negotiable)" bullet as binding. If one blocks you, that is a
  replan trigger — stop and report, do not route around it.
- If any listed "Replan triggers" fires, stop and report what you hit before continuing.
- Coverage thresholds are quality gates. If coverage drops, add tests. Never lower a threshold — only
  the project owner may approve that.
- Phase 0 ships no behaviour change: outbound parse modes, text and markup must be byte-identical to
  `main`.

## Progress file discipline

- Tick a phase checkbox only after `just hook` passes for that phase.
- Add "Lessons learned" entries for decisions made during implementation and problems solved by
  implementing — one entry per item, tagged with its phase. Follow the comment in the file: no general
  notes, no status updates, no restatement of the plan. If a phase taught you nothing meeting that
  bar, add nothing.
- Record every question deferred to system testing there too, tagged with its phase.
- Update the progress file in the same commit as the phase work.
