package agent

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ReplyText returns the text of the agent's reply that one stored row holds,
// as the browser's transcript shows it, and ok=false for a row that holds no
// reply. `content` is the row as DecodeMessageSupplement reads its decoded
// content and its decoded rendering supplement, with the row's
// AgentSessionID: the inputs that the browser reads.
//
// It follows the browser's order (messageClassifier.ts and
// rowExtraction.ts):
//
//  1. The assembled kind that the Worker stores beside the row decides first:
//     a reasoning row or a plan row is no reply, whatever its envelope holds.
//  2. Resolve the provider's supplement into the frame.
//  3. A notification thread is no reply: the Worker gathers into a thread
//     only the frames that the provider's Classify marks as notifications.
//  4. A valid assembled envelope, which LeapMux owns, holds the text of a
//     `text` row.
//  5. Otherwise the provider reads its own native frame, or an assembled
//     envelope that step 4 refused as malformed.
//
// The reply is the row's quotable text, as the browser's quotableTextForRow
// copies it: the text without the white space at either end. A row whose
// text is all white space holds no reply.
func ReplyText(provider Provider, content MessageContent) (string, bool) {
	origFields, origOK := decodeAssembledEnvelopeFields(content.Original)
	if origOK {
		if kind, _ := origFields.metadata(content.Completion); kind != leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED &&
			kind != leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_TEXT {
			return "", false
		}
	}
	frame := ResolveMessageContent(provider, content)
	var (
		envelope assembledEnvelopeFields
		ok       bool
	)
	if len(frame) == len(content.Original) && (len(frame) == 0 || &frame[0] == &content.Original[0]) {
		envelope, ok = origFields, origOK
	} else {
		envelope, ok = decodeAssembledEnvelopeFields(frame)
	}
	if !ok {
		return "", false
	}
	switch envelope.envelopeType() {
	case contracts.NotificationThreadWrapperType:
		return "", false
	case contracts.AssembledMessageType:
		if text, ok, valid := assembledReplyText(envelope); valid {
			return replyOf(text, ok)
		}
	}
	return replyOf(provider.ReplyText(MessageContent{
		Original:       frame,
		Supplemental:   content.Supplemental,
		AgentSessionID: content.AgentSessionID,
	}))
}

// assembledReplyText reads an assembled envelope. valid is false for an
// envelope that the browser does not accept as one, which then reaches the
// provider as any other frame does.
func assembledReplyText(envelope assembledEnvelopeFields) (text string, ok, valid bool) {
	var kind, completion string
	if json.Unmarshal(envelope.Kind, &kind) != nil ||
		json.Unmarshal(envelope.Completion, &completion) != nil {
		return "", false, false
	}
	if AssembledMessageKindFromToken(kind) == 0 || messageCompletionFromToken(completion) == 0 {
		return "", false, false
	}
	// The browser reads a missing or non-string text as empty.
	_ = json.Unmarshal(envelope.Text, &text)
	return text, AssembledMessageKind(kind) == AssembledMessageKindText, true
}

// replyOf trims a reply as JavaScript's String.prototype.trim does, and
// keeps it when anything remains.
func replyOf(text string, ok bool) (string, bool) {
	text = TrimJavaScriptSpace(text)
	if !ok || text == "" {
		return "", false
	}
	return text, true
}

// TrimJavaScriptSpace trims text as JavaScript's String.prototype.trim
// does, so a Worker reads typed text as the browser reads it.
func TrimJavaScriptSpace(text string) string {
	return strings.TrimFunc(text, isJavaScriptWhiteSpace)
}

// isJavaScriptWhiteSpace reports a character that JavaScript's trim removes:
// the WhiteSpace and LineTerminator productions of ECMAScript. They are
// Unicode's White_Space property without U+0085 (NEXT LINE), plus U+FEFF
// (ZERO WIDTH NO-BREAK SPACE), so unicode.IsSpace alone would disagree with
// the browser on those two.
func isJavaScriptWhiteSpace(r rune) bool {
	return r == '\uFEFF' || r != '\u0085' && unicode.IsSpace(r)
}
