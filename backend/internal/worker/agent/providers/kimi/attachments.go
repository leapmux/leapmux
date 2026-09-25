package kimi

import (
	"encoding/base64"
	"fmt"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kimi Code's prompt content.
//
// A prompt is a list of content parts. The text travels as one text part, and a
// text attachment joins it inline, as the other providers send one: the model
// quotes and reasons about the file as if the user had pasted it. An image
// travels as an image part with its bytes inline, and only when the current
// model takes image input -- the server would accept an image for a text-only
// model and drop it on the way to the model.
//
// A PDF and a binary file are refused. The server's `file` part hands the model
// a path, which leaves the reading to the model's own tools and states nothing
// about a file LeapMux holds only as bytes.

// kimiMaxAttachmentBytes caps one prompt's total attachment payload. The server
// states no cap for inline parts; the one it applies to uploads is the sensible
// ceiling for a request body that grows by a third in base64.
const kimiMaxAttachmentBytes = 20 * 1024 * 1024

// kimiAttachmentLabel names the provider in an attachment refusal.
const kimiAttachmentLabel = "Kimi Code"

// buildKimiContent turns a user message and its attachments into prompt parts.
func buildKimiContent(content string, attachments []*leapmuxv1.Attachment, takesImages bool) ([]map[string]any, error) {
	classified := agent.ClassifyAttachments(attachments)
	text := content
	var images []map[string]any
	total := 0
	var blocks []string
	for _, attachment := range classified {
		if err := providerkit.RejectPDFAndBinaryAttachment(kimiAttachmentLabel, attachment); err != nil {
			return nil, err
		}
		total += len(attachment.Data)
		if total > kimiMaxAttachmentBytes {
			return nil, fmt.Errorf("the attachments exceed the %d MB that one Kimi Code prompt takes", kimiMaxAttachmentBytes/(1024*1024))
		}
		switch attachment.Kind {
		case agent.AttachmentKindText:
			blocks = append(blocks, providerkit.BuildInlineTextAttachmentBlock(attachment))
		case agent.AttachmentKindImage:
			if !takesImages {
				return nil, fmt.Errorf("the current Kimi Code model does not take images: %s", attachment.Filename)
			}
			images = append(images, map[string]any{
				"type": "image",
				"source": map[string]any{
					"kind":       "base64",
					"media_type": attachment.MIMEType,
					"data":       base64.StdEncoding.EncodeToString(attachment.Data),
				},
			})
		case agent.AttachmentKindPDF, agent.AttachmentKindBinary:
			// RejectPDFAndBinaryAttachment refused both above.
		}
	}
	if len(blocks) > 0 {
		if strings.TrimSpace(text) != "" {
			blocks = append([]string{text}, blocks...)
		}
		text = strings.Join(blocks, "\n\n")
	}
	parts := make([]map[string]any, 0, len(images)+1)
	if text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	parts = append(parts, images...)
	if len(parts) == 0 {
		return nil, fmt.Errorf("a Kimi Code prompt needs text or an attachment")
	}
	return parts, nil
}
