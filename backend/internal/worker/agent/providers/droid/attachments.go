package droid

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const droidAttachmentLabel = "Factory Droid"

// Droid replaces larger PDF sources with extracted text instead of sending the file.
const droidMaxPDFBytes = 3 * 1024 * 1024

// ValidateAttachment checks file kinds and sizes independently of a live model.
func (droidProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == contracts.AttachmentMediaKindPdf {
		if len(attachment.Data) == 0 {
			return fmt.Errorf("factory Droid does not support an empty PDF attachment: %s", attachment.Filename)
		}
		if len(attachment.Data) > droidMaxPDFBytes {
			return fmt.Errorf("factory Droid PDF attachments cannot exceed %d MiB: %s", droidMaxPDFBytes/(1024*1024), attachment.Filename)
		}
		return nil
	}
	if err := providerkit.ValidateTextAndImageAttachment(droidAttachmentLabel, attachment); err != nil {
		return err
	}
	if attachment.Kind == contracts.AttachmentMediaKindImage && len(attachment.Data) == 0 {
		return fmt.Errorf("factory Droid does not support an empty image attachment: %s", attachment.Filename)
	}
	return nil
}

// validatePDFModelLocked checks the native model selected for this admission.
// The caller holds Process.Mu and serializes admission with settings changes.
func (a *Agent) validatePDFModelLocked() error {
	for _, model := range a.catalog.models {
		if strings.TrimSpace(model.id) != "" && model.id == a.settings.model && model.supportsPDF {
			return nil
		}
	}
	return errors.New("the selected Factory Droid model cannot receive this PDF; choose a model with PDF support")
}

// droidUserMessage holds the native inputs for one user message.
type droidUserMessage struct {
	Text   string
	Images []droidInputImage
	Files  []droidInputFile
}

// buildUserMessage keeps text inline and encodes images and PDFs as native sources.
func buildUserMessage(content string, attachments []*leapmuxv1.Attachment) (droidUserMessage, error) {
	parts := []string{}
	images := []droidInputImage{}
	files := []droidInputFile{}
	if text := strings.TrimSpace(content); text != "" {
		parts = append(parts, text)
	}
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if err := (droidProvider{}).ValidateAttachment(attachment); err != nil {
			return droidUserMessage{}, err
		}
		switch attachment.Kind {
		case contracts.AttachmentMediaKindImage:
			images = append(images, droidInputImage{
				Type: "base64", MediaType: attachment.MIMEType,
				Data: base64.StdEncoding.EncodeToString(attachment.Data),
			})
		case contracts.AttachmentMediaKindPdf:
			files = append(files, droidInputFile{
				Type: "base64", MediaType: attachment.MIMEType,
				Data: base64.StdEncoding.EncodeToString(attachment.Data), Name: attachment.Filename,
			})
		case contracts.AttachmentMediaKindText:
			parts = append(parts, providerkit.BuildInlineTextAttachmentBlock(attachment))
		default:
			return droidUserMessage{}, fmt.Errorf("factory Droid cannot send the attachment %s", attachment.Filename)
		}
	}
	return droidUserMessage{Text: strings.Join(parts, "\n\n"), Images: images, Files: files}, nil
}
