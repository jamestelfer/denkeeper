package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/Temikus/denkeeper/internal/adapter"
)

// longReply builds a reply of n numbered lines, each its own markdown paragraph
// so it renders to its own block, with enough mixed formatting that the chunks
// carry real markup rather than plain text.
func longReply(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "Line %d: **bold** and `code_span` and a [link](https://example.com/a_%d).\n\n", i, i)
	}
	return sb.String()
}

// TestSend_LongReplyIsSentAsSeveralMessagesInOrder covers R25's multi-chunk half
// and R17 at the adapter layer.
func TestSend_LongReplyIsSentAsSeveralMessagesInOrder(t *testing.T) {
	bot := newFakeBot()
	a := newWithSender(bot, nil, testLogger(), nil)

	if err := a.Send(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       longReply(60),
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msgs := bot.messages(t)
	if len(msgs) < 2 {
		t.Fatalf("send count = %d, want several — a reply over the cap must chunk", len(msgs))
	}
	for i, m := range msgs {
		if m.ParseMode != "HTML" {
			t.Errorf("chunk %d ParseMode = %q, want HTML", i, m.ParseMode)
		}
		if len(m.Text) > MessageChunkLimit {
			t.Errorf("chunk %d is %d bytes, over the %d-byte limit", i, len(m.Text), MessageChunkLimit)
		}
		if m.ChatID != 12345 {
			t.Errorf("chunk %d ChatID = %d, want 12345", i, m.ChatID)
		}
	}
}

// TestSend_LongReplyReassemblesExactly is the test that matters for chunking. It
// concatenates the captured sends in call order and asserts every ordinal is
// present exactly once and in ascending order — the two failure modes a reader
// scrolling a long thread is worst at spotting.
func TestSend_LongReplyReassemblesExactly(t *testing.T) {
	bot := newFakeBot()
	a := newWithSender(bot, nil, testLogger(), nil)

	const lines = 60
	if err := a.Send(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       longReply(lines),
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var all strings.Builder
	for _, m := range bot.messages(t) {
		all.WriteString(m.Text)
	}
	reassembled := all.String()

	prev := -1
	for i := 1; i <= lines; i++ {
		marker := fmt.Sprintf("Line %d:", i)
		at := strings.Index(reassembled, marker)
		if at < 0 {
			t.Fatalf("%q is missing from the reassembled reply", marker)
		}
		if n := strings.Count(reassembled, marker); n != 1 {
			t.Errorf("%q appears %d times, want exactly once", marker, n)
		}
		if at < prev {
			t.Errorf("%q appears at %d, before the previous line at %d — order was not preserved", marker, at, prev)
		}
		prev = at
	}
	// The formatting must survive chunking too, not just the text.
	if !strings.Contains(reassembled, "<b>bold</b>") {
		t.Error("reassembled reply lost its bold markup")
	}
	if !strings.Contains(reassembled, "<code>code_span</code>") {
		t.Error("reassembled reply lost its code spans")
	}
}

// TestSend_ButtonsRideOnTheFinalChunkOnly covers R26. Buttons under a
// mid-message fragment read as a broken message.
func TestSend_ButtonsRideOnTheFinalChunkOnly(t *testing.T) {
	bot := newFakeBot()
	a := newWithSender(bot, nil, testLogger(), nil)

	if err := a.Send(context.Background(), adapter.OutgoingMessage{
		ExternalID:   "12345",
		Text:         longReply(60),
		Buttons:      []adapter.KeyboardButton{{Label: "Approve", CallbackData: "ok"}},
		ButtonLayout: []int{1},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msgs := bot.messages(t)
	if len(msgs) < 2 {
		t.Fatalf("send count = %d, want several", len(msgs))
	}
	for i, m := range msgs[:len(msgs)-1] {
		if m.ReplyMarkup != nil {
			t.Errorf("chunk %d carries a keyboard; only the final chunk may", i)
		}
	}
	markup, ok := msgs[len(msgs)-1].ReplyMarkup.(tgbotapi.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("final chunk ReplyMarkup = %T, want tgbotapi.InlineKeyboardMarkup", msgs[len(msgs)-1].ReplyMarkup)
	}
	if len(markup.InlineKeyboard) != 1 || markup.InlineKeyboard[0][0].Text != "Approve" {
		t.Errorf("final chunk keyboard = %v, want one Approve button", markup.InlineKeyboard)
	}
}

// TestSendAndGetID_MultiChunkReturnsTheFinalChunkID covers R27. The activity
// log edits the message it was handed, and an edit must land on the tail of the
// reply, not on a fragment in the middle of it.
func TestSendAndGetID_MultiChunkReturnsTheFinalChunkID(t *testing.T) {
	bot := newFakeBot()
	a := newWithSender(bot, nil, testLogger(), nil)

	id, err := a.SendAndGetID(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       longReply(60),
	})
	if err != nil {
		t.Fatalf("SendAndGetID: %v", err)
	}

	sent := bot.sendCount()
	if sent < 2 {
		t.Fatalf("send count = %d, want several", sent)
	}
	// The fake advances its ID on every accepted send, so the final chunk's ID
	// is the first ID plus one per preceding chunk.
	want := strconv.Itoa(firstFakeMessageID + sent - 1)
	if id != want {
		t.Errorf("returned message ID = %q, want the final chunk's %q", id, want)
	}
}

// fourChunkReply is long enough to render to four chunks, so a failure can be
// injected mid-sequence with chunks still queued behind it.
func fourChunkReply(t *testing.T) string {
	t.Helper()
	src := longReply(120)
	chunks, err := renderChunks(src)
	if err != nil {
		t.Fatalf("renderChunks: %v", err)
	}
	if len(chunks) != 4 {
		t.Fatalf("fixture renders to %d chunks, want 4", len(chunks))
	}
	return src
}

// TestSend_FailedChunkAbortsTheRemainder covers R31. A gap in the middle of a
// reply is worse than a truncated one, so there is no partial recovery and no
// best-effort continuation.
func TestSend_FailedChunkAbortsTheRemainder(t *testing.T) {
	bot := newFakeBot().failOnSend(2)
	a := newWithSender(bot, nil, testLogger(), nil)

	err := a.Send(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       fourChunkReply(t),
	})
	if err == nil {
		t.Fatal("expected the chunk failure to surface as an error")
	}
	if got := bot.sendCount(); got != 2 {
		t.Errorf("send count = %d, want exactly 2 of 4 — the remaining chunks must be abandoned", got)
	}
}

// TestSendAndGetID_FailedChunkAbortsTheRemainder is R31 on the other send path.
func TestSendAndGetID_FailedChunkAbortsTheRemainder(t *testing.T) {
	bot := newFakeBot().failOnSend(2)
	a := newWithSender(bot, nil, testLogger(), nil)

	id, err := a.SendAndGetID(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       fourChunkReply(t),
	})
	if err == nil {
		t.Fatal("expected the chunk failure to surface as an error")
	}
	if id != "" {
		t.Errorf("returned message ID = %q, want empty on failure", id)
	}
	if got := bot.sendCount(); got != 2 {
		t.Errorf("send count = %d, want exactly 2", got)
	}
}

// TestSend_RateLimitedChunkAbortsWithoutBackoff records the rate-limit decision.
//
// Telegram documents roughly one message per second per chat with bursts
// tolerated, and a chunked reply is a handful of messages, so tripping the limit
// is unlikely. When it happens R31 applies unchanged: abort. A bounded backoff
// would leave a reply half-delivered and then resume after an arbitrary pause,
// which reads worse than a visible failure the operator can see and retry.
func TestSend_RateLimitedChunkAbortsWithoutBackoff(t *testing.T) {
	bot := newFakeBot().failOnSend(3)
	bot.failErr = &tgbotapi.Error{
		Code:    429,
		Message: "Too Many Requests: retry after 5",
	}
	a := newWithSender(bot, nil, testLogger(), nil)

	err := a.Send(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       fourChunkReply(t),
	})
	if err == nil {
		t.Fatal("expected the 429 to surface as an error")
	}
	if got := bot.sendCount(); got != 3 {
		t.Errorf("send count = %d, want exactly 3 — a 429 is not retried or backed off", got)
	}
}

// TestSend_SingleChunkReplyStaysOneMessage is the common case, and the one a
// chunker most easily regresses: no extra empty message, no trailing separator.
func TestSend_SingleChunkReplyStaysOneMessage(t *testing.T) {
	bot := newFakeBot()
	a := newWithSender(bot, nil, testLogger(), nil)

	if err := a.Send(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       "A short reply.\n\nWith two paragraphs.",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msgs := bot.messages(t)
	if len(msgs) != 1 {
		t.Fatalf("send count = %d, want 1: %v", len(msgs), msgs)
	}
	want := "A short reply.\n\nWith two paragraphs."
	if msgs[0].Text != want {
		t.Errorf("Text = %q, want %q", msgs[0].Text, want)
	}
}

// TestSend_EmptyRenderSendsNothing keeps a reply that renders to no blocks from
// becoming an empty Telegram message, which the API rejects.
func TestSend_EmptyRenderSendsNothing(t *testing.T) {
	bot := newFakeBot()
	a := newWithSender(bot, nil, testLogger(), nil)

	if err := a.Send(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       "   \n\n  \n",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := bot.sendCount(); got != 0 {
		t.Errorf("send count = %d, want 0 — there is nothing to send", got)
	}
}

// TestSendAndGetID_EmptyRenderIsAnError is the other half of sending nothing.
// Send has nothing to do, but a caller asking for an identifier needs one, and
// an empty string would be used to edit a message that does not exist.
func TestSendAndGetID_EmptyRenderIsAnError(t *testing.T) {
	bot := newFakeBot()
	a := newWithSender(bot, nil, testLogger(), nil)

	id, err := a.SendAndGetID(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       "   \n\n  \n",
	})
	if err == nil {
		t.Fatal("expected an error when there is no content to send")
	}
	if id != "" {
		t.Errorf("returned message ID = %q, want empty", id)
	}
	if got := bot.sendCount(); got != 0 {
		t.Errorf("send count = %d, want 0", got)
	}
}

// TestSend_OversizedReplyDoesNotRetryPlainText pins the futility half of the
// retry rule.
//
// Nothing was delivered here — the very first chunk failed — so R31 is not what
// stops the retry. What stops it is that the retry sends the original markdown
// as one message, and this reply's source is several times Telegram's 4096-char
// cap. The API would reject it on sight, so the round trip buys nothing and
// replaces the real error with a second, less useful one.
func TestSend_OversizedReplyDoesNotRetryPlainText(t *testing.T) {
	bot := newFakeBot().failOnSend(1)
	a := newWithSender(bot, nil, testLogger(), nil)

	src := longReply(60)
	if len(src) <= telegramMessageCap {
		t.Fatalf("fixture source is %d bytes, want over %d so the retry is genuinely futile", len(src), telegramMessageCap)
	}
	if err := a.Send(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       src,
	}); err == nil {
		t.Fatal("expected the failure to surface as an error")
	}

	msgs := bot.messages(t)
	if len(msgs) != 1 {
		t.Fatalf("send count = %d, want exactly 1 — a reply that cannot fit in one message is not retried", len(msgs))
	}
	if msgs[0].ParseMode != "HTML" {
		t.Errorf("ParseMode = %q, want HTML — the single attempt is the HTML one", msgs[0].ParseMode)
	}
}

// twoChunkReplyUnderCap builds the reply the retry rule turns on: markup
// inflates the rendered HTML past the chunk limit, so it splits, yet the source
// markdown still fits inside one Telegram message.
//
// Both halves are asserted here rather than in the test. A fixture that drifted
// to three chunks, or past the cap, would leave the test green while it stopped
// exercising the case it was written for.
func twoChunkReplyUnderCap(t *testing.T) string {
	t.Helper()
	src := longReply(45)
	if len(src) > telegramMessageCap {
		t.Fatalf("fixture source is %d bytes, want at most %d so the plain retry can fit in one message", len(src), telegramMessageCap)
	}
	chunks, err := renderChunks(src)
	if err != nil {
		t.Fatalf("renderChunks: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("fixture renders to %d chunks, want 2", len(chunks))
	}
	return src
}

// TestSend_FirstChunkFailureRetriesPlainTextWhenTheSourceFits covers the case
// the chunk count alone gets wrong.
//
// The reply chunks, but the failure landed on chunk one, so the chat is
// untouched and R31 has no partial delivery to protect. The source is under
// Telegram's cap, so the single-message retry can actually land. Both conditions
// hold, so R30's fallback applies exactly as it would to a short reply.
func TestSend_FirstChunkFailureRetriesPlainTextWhenTheSourceFits(t *testing.T) {
	bot := newFakeBot().failOnSend(1)
	a := newWithSender(bot, nil, testLogger(), nil)

	src := twoChunkReplyUnderCap(t)
	if err := a.Send(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       src,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msgs := bot.messages(t)
	if len(msgs) != 2 {
		t.Fatalf("send count = %d, want exactly 2 — the HTML attempt and one plain-text retry", len(msgs))
	}
	if msgs[0].ParseMode != "HTML" {
		t.Errorf("first attempt ParseMode = %q, want HTML", msgs[0].ParseMode)
	}
	if msgs[1].ParseMode != "" {
		t.Errorf("retry ParseMode = %q, want empty — the retry carries no markup", msgs[1].ParseMode)
	}
	if msgs[1].Text != src {
		t.Error("retry text is not the original markdown")
	}
}

// TestSend_VoiceReplyIsNotChunked confirms the TTS path returns before the text
// path, so chunking is unreachable from a voice reply however long it is.
func TestSend_VoiceReplyIsNotChunked(t *testing.T) {
	tts := &recordingTTS{}
	bot := newFakeBot()
	a := newWithSender(bot, nil, testLogger(), &VoiceOpts{
		TTS:            tts,
		TTSVoice:       "nova",
		AutoVoiceReply: true,
	})

	src := longReply(60)
	if err := a.Send(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       src,
		IsVoice:    true,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := bot.sendCount(); got != 1 {
		t.Errorf("send count = %d, want 1 (the voice message)", got)
	}
	if tts.text != src {
		t.Error("synthesized text is not the original markdown")
	}
}

// TestSend_ExplicitParseModeIsNotChunked keeps the activity log on its own
// chunker. It passes pre-built HTML and its own parse mode, and rerouting it
// through this one would re-split markup it already split.
func TestSend_ExplicitParseModeIsNotChunked(t *testing.T) {
	bot := newFakeBot()
	a := newWithSender(bot, nil, testLogger(), nil)

	long := "<b>" + strings.Repeat("x", MessageChunkLimit+500) + "</b>"
	if err := a.Send(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       long,
		ParseMode:  "HTML",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msgs := bot.messages(t)
	if len(msgs) != 1 {
		t.Fatalf("send count = %d, want 1 — an explicit parse mode bypasses chunking", len(msgs))
	}
	if msgs[0].Text != long {
		t.Error("Text is not byte-identical to the input")
	}
}

// TestMessageChunkLimit_LeavesHeadroomBelowTelegramsCap pins the headroom
// decision. The chunker counts raw HTML bytes while Telegram counts the
// entity-stripped text in UTF-16 code units, so the count is conservative
// already, but it must still leave room under the cap.
func TestMessageChunkLimit_LeavesHeadroomBelowTelegramsCap(t *testing.T) {
	const telegramCap = 4096
	if MessageChunkLimit >= telegramCap {
		t.Errorf("MessageChunkLimit = %d, want headroom below Telegram's %d", MessageChunkLimit, telegramCap)
	}
}

// multiByteReplyUnderCap builds a reply that fits in one Telegram message and
// does not fit in 4096 bytes. Telegram counts characters, so the two are only
// the same string for ASCII.
//
// Both halves are asserted here so the fixture cannot drift into testing
// nothing: an ASCII reply would satisfy the test for the wrong reason.
func multiByteReplyUnderCap(t *testing.T) string {
	t.Helper()
	// Japanese: three bytes per character, so 1500 characters is 4500 bytes.
	src := strings.TrimSpace(strings.Repeat("こんにちは世界。これはテストです。\n", 100))
	if n := utf8.RuneCountInString(src); n > telegramMessageCap {
		t.Fatalf("fixture is %d characters, want at most %d so one message can hold it", n, telegramMessageCap)
	}
	if len(src) <= telegramMessageCap {
		t.Fatalf("fixture is %d bytes, want over %d so a byte-counted cap would reject it", len(src), telegramMessageCap)
	}
	return src
}

// TestSend_MultiByteReplyUnderTheCapRetriesPlainText pins what the futility
// check in R30 is actually asking.
//
// The question is whether the original markdown could go out as one message,
// and Telegram answers it in characters of the entity-stripped text — the unit
// telegramMessageCap is documented in. Measuring the source in bytes instead
// answers a different question, and answers it wrongly for every reply that is
// not ASCII: a Japanese reply well inside the cap looks oversized, so the retry
// that would have succeeded is never attempted and the reply is lost.
func TestSend_MultiByteReplyUnderTheCapRetriesPlainText(t *testing.T) {
	bot := newFakeBot().failOnSend(1)
	a := newWithSender(bot, nil, testLogger(), nil)

	src := multiByteReplyUnderCap(t)
	if err := a.Send(context.Background(), adapter.OutgoingMessage{
		ExternalID: "12345",
		Text:       src,
	}); err != nil {
		t.Fatalf("Send: %v — the plain-text retry should have carried the reply", err)
	}

	msgs := bot.messages(t)
	if len(msgs) != 2 {
		t.Fatalf("send count = %d, want 2 — the failed HTML attempt and one plain-text retry", len(msgs))
	}
	if msgs[0].ParseMode != parseModeHTML {
		t.Errorf("first ParseMode = %q, want %q", msgs[0].ParseMode, parseModeHTML)
	}
	if msgs[1].ParseMode != "" {
		t.Errorf("retry ParseMode = %q, want empty — the retry carries no markup", msgs[1].ParseMode)
	}
	if msgs[1].Text != src {
		t.Errorf("retry text = %q, want the original markdown", msgs[1].Text)
	}
}
