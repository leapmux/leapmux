package zcode

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ZCode's attachment path.
//
// `session/send.attachments` is the whole mechanism, and it carries the bytes
// INLINE: `{kind, filename, MIMEType, sizeBytes, dataBase64}`. The three-step
// `v4/attachment/begin` -> `chunk` -> `commit` upload also works and is not needed
// -- it produces a `zcode-artifact://` reference for the desktop application's own
// attachment tray, and a reference reaches the model only after the same normalizer
// that already reads `dataBase64`.
//
// An image and a PDF each travel with their bytes, as the `image` and the `pdf`
// kind. The app-server gives the model an image as an image part and a PDF as a
// file part.
//
// The app-server ACCEPTS an image or a PDF that the current model declares no
// input for, and the file never reaches the model: ZCode replaces it with a note
// that the media was omitted. Only the RUNNING agent knows which model is current,
// so that refusal is here and not in ValidateAttachment. A binary file has no kind
// that keeps its bytes, so ValidateAttachment refuses it.

// zcodeMaxAttachmentBytes caps one send's total attachment payload.
//
// The app-server's own upload path enforces 20 MB across at most 64 chunks of
// 512 KB. `session/send` states no cap of its own, so the same limit is applied
// here: a payload the app-server would refuse on one path is not worth building on
// the other, and a base64 body of that size is already 27 MB of line.
const zcodeMaxAttachmentBytes = 20 * 1024 * 1024

// The normalizer discriminators for the kinds that LeapMux sends with their bytes.
// The normalizer also knows `video`, `audio` and `file`. This provider sends
// images and PDFs with their bytes. It inlines text into the prompt, and
// ValidateAttachment refuses audio and every other binary file.
const (
	zcodeAttachmentKindImage = "image"
	zcodeAttachmentKindPDF   = "pdf"
)

// zcodeMediaKind is how one LeapMux attachment kind travels with its bytes.
type zcodeMediaKind struct {
	// wire is the normalizer discriminator.
	wire string
	// modality is the input modality that the model must declare.
	modality string
	// label states the kind in a refusal.
	label string
}

// zcodeMediaKinds lists the LeapMux attachment kinds that travel with their bytes.
var zcodeMediaKinds = map[contracts.AttachmentMediaKind]zcodeMediaKind{
	contracts.AttachmentMediaKindImage: {wire: zcodeAttachmentKindImage, modality: zcodeModalityImage, label: "image"},
	contracts.AttachmentMediaKindPdf:   {wire: zcodeAttachmentKindPDF, modality: zcodeModalityPDF, label: "PDF"},
}

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
// question arises. Images and PDFs travel as wire attachments, and only when the
// current model declares that it accepts them.
func (a *Agent) buildZCodeInput(content string, attachments []*leapmuxv1.Attachment, model string) (string, []zcodeWireAttachment, error) {
	classified := agent.ClassifyAttachments(attachments)
	if len(classified) == 0 {
		return content, nil, nil
	}

	var blocks []string
	var wire []zcodeWireAttachment
	total := 0

	for _, attachment := range classified {
		// The static policy runs first, so a binary is refused with the same message
		// whether it arrived through the normalizing pre-check or straight here.
		if err := (zcodeProvider{}).ValidateAttachment(attachment); err != nil {
			return "", nil, err
		}
		if attachment.Kind == contracts.AttachmentMediaKindText {
			blocks = append(blocks, providerkit.BuildInlineTextAttachmentBlock(attachment))
			continue
		}
		media, ok := zcodeMediaKinds[attachment.Kind]
		if !ok {
			// Unreachable: ValidateAttachment above refuses every other kind. Reported
			// rather than dropped, so a new attachment kind cannot be lost in silence.
			return "", nil, fmt.Errorf("zcode cannot send attachment %s of kind %q", attachment.Filename, attachment.Kind)
		}
		if err := a.checkZCodeModelInput(model, media, attachment); err != nil {
			return "", nil, err
		}
		total += len(attachment.Data)
		if total > zcodeMaxAttachmentBytes {
			return "", nil, fmt.Errorf("attachments exceed ZCode's %d MB limit", zcodeMaxAttachmentBytes/(1024*1024))
		}
		wire = append(wire, zcodeWireAttachment{
			Kind:       media.wire,
			Filename:   attachment.Filename,
			MimeType:   attachment.MIMEType,
			SizeBytes:  len(attachment.Data),
			DataBase64: base64.StdEncoding.EncodeToString(attachment.Data),
		})
	}

	return zcodeJoinPrompt(content, blocks), wire, nil
}

// checkZCodeModelInput refuses an image or a PDF that the current model cannot
// read.
//
// The app-server ACCEPTS such a send and the file never reaches the model, so
// without this the user sees a confident answer about a file the model never saw.
// A refusal identifies the model, because the remedy is to switch it.
func (a *Agent) checkZCodeModelInput(model string, media zcodeMediaKind, attachment agent.ClassifiedAttachment) error {
	if model == "" {
		// No model is pinned, so the app-server picked one from the registry and its
		// capabilities are unknown here. Sending is the better failure: a file the
		// model ignores is recoverable, and refusing every file on an unpinned session
		// would not be.
		return nil
	}
	a.Mu.Lock()
	record, live := a.liveModels[a.normalizeZCodeSelectionLocked(model)]
	complete := a.liveCatalogComplete
	a.Mu.Unlock()
	accepts := false
	if live {
		accepts = slices.Contains(record.modalities, media.modality)
	} else if !complete {
		a.Mu.Lock()
		accepts = a.catalog.acceptsInputModality(model, media.modality)
		a.Mu.Unlock()
	}
	if accepts {
		return nil
	}
	return fmt.Errorf("the ZCode model %s does not accept %s attachments (%s); choose a model that declares %s input",
		model, media.label, attachment.Filename, media.label)
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
