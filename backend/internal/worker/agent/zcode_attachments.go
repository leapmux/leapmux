package agent

import (
	"encoding/base64"
	"fmt"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ZCode's attachment path.
//
// `session/send.attachments` is the whole mechanism, and it carries the bytes
// INLINE: `{kind, filename, mimeType, sizeBytes, dataBase64}`. The three-step
// `v4/attachment/begin` -> `chunk` -> `commit` upload also works and is not needed
// -- it produces a `zcode-artifact://` reference for the desktop application's own
// attachment tray, and a reference reaches the model only after the same normalizer
// that already reads `dataBase64`.
//
// Two boundaries are real and both are enforced here rather than left to fail
// silently:
//
//   - The normalizer recognizes `image`, `video`, `file` and `audio`. A PDF has no
//     kind of its own: as a `file` a small one is decoded as TEXT (the model
//     receives binary garbage) and a large one is dropped with no message. So a PDF
//     is refused up front, in zcodeProvider.ValidateAttachment.
//   - An image sent to a text-only model is accepted by the app-server and never
//     reaches the model. Only the RUNNING agent knows which model is current, so
//     that refusal is here.

// zcodeMaxAttachmentBytes caps one send's total attachment payload.
//
// The app-server's own upload path enforces 20 MB across at most 64 chunks of
// 512 KB. `session/send` states no cap of its own, so the same limit is applied
// here: a payload the app-server would refuse on one path is not worth building on
// the other, and a base64 body of that size is already 27 MB of line.
const zcodeMaxAttachmentBytes = 20 * 1024 * 1024

// zcodeAttachmentKindImage is the normalizer discriminator for the one kind LeapMux
// sends. The normalizer also knows `video`, `audio` and `file`; the first two have no
// LeapMux attachment kind, and `file` is what makes a PDF unusable (see above).
const zcodeAttachmentKindImage = "image"

// zcodeWireAttachment is one entry of session/send.attachments.
//
// `sizeBytes` is the DECODED length. The app-server reads it to decide whether to
// treat a payload as text or to hand it over as bytes, so it must be the true
// length and not the base64 one.
type zcodeWireAttachment struct {
	Kind        string `json:"kind"`
	Filename    string `json:"filename"`
	MimeType    string `json:"mimeType"`
	SizeBytes   int    `json:"sizeBytes"`
	DataBase64  string `json:"dataBase64,omitempty"`
	TextContent string `json:"textContent,omitempty"`
}

// buildZCodeInput turns a user message and its attachments into the text and the
// wire attachments session/send takes.
//
// Text attachments are INLINED into the prompt, as Pi does: the model then quotes
// and reasons about them exactly as if the user had pasted them, and no capability
// question arises. Images travel as wire attachments, and only when the current
// model declares that it accepts them.
func (a *zcodeAgent) buildZCodeInput(content string, attachments []*leapmuxv1.Attachment, model string) (string, []zcodeWireAttachment, error) {
	classified := classifyAttachments(attachments)
	if len(classified) == 0 {
		return content, nil, nil
	}

	var blocks []string
	var wire []zcodeWireAttachment
	total := 0

	for _, attachment := range classified {
		// The static policy runs first, so a PDF or a binary is refused with the same
		// message whether it arrived through the normalizing pre-check or straight here.
		if err := (zcodeProvider{}).ValidateAttachment(attachment); err != nil {
			return "", nil, err
		}
		switch attachment.kind {
		case attachmentKindText:
			blocks = append(blocks, buildInlineTextAttachmentBlock(attachment))
		case attachmentKindImage:
			if err := a.checkZCodeImageSupport(model, attachment); err != nil {
				return "", nil, err
			}
			total += len(attachment.data)
			if total > zcodeMaxAttachmentBytes {
				return "", nil, fmt.Errorf("attachments exceed ZCode's %d MB limit", zcodeMaxAttachmentBytes/(1024*1024))
			}
			wire = append(wire, zcodeWireAttachment{
				Kind:       zcodeAttachmentKindImage,
				Filename:   attachment.filename,
				MimeType:   attachment.mimeType,
				SizeBytes:  len(attachment.data),
				DataBase64: base64.StdEncoding.EncodeToString(attachment.data),
			})
		default:
			// Unreachable: ValidateAttachment above refuses every other kind. Reported
			// rather than dropped, so a new attachment kind cannot be lost in silence.
			return "", nil, fmt.Errorf("zcode cannot send attachment %s of kind %q", attachment.filename, attachment.kind)
		}
	}

	return zcodeJoinPrompt(content, blocks), wire, nil
}

// checkZCodeImageSupport refuses an image the current model cannot read.
//
// The app-server ACCEPTS such a send and the image never reaches the model, so
// without this the user sees a confident answer about an image the model never saw.
// A refusal identifies the model, because the remedy is to switch it.
func (a *zcodeAgent) checkZCodeImageSupport(model string, attachment classifiedAttachment) error {
	if model == "" {
		// No model is pinned, so the app-server picked one from the registry and its
		// capabilities are unknown here. Sending is the better failure: an image the
		// model ignores is recoverable, and refusing every image on an unpinned session
		// would not be.
		return nil
	}
	if a.catalog.acceptsInputModality(model, zcodeModalityImage) {
		return nil
	}
	return fmt.Errorf("the ZCode model %s does not accept image attachments (%s); choose a model that declares image input",
		model, attachment.filename)
}

// zcodeJoinPrompt appends the inlined attachment blocks to the user's message.
func zcodeJoinPrompt(content string, blocks []string) string {
	if len(blocks) == 0 {
		return content
	}
	joined := strings.Join(blocks, "\n\n")
	if strings.TrimSpace(content) == "" {
		return joined
	}
	return content + "\n\n" + joined
}
