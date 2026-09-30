package codebuddy

import (
	"encoding/base64"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codebuddyUserContent keeps plain prompts as strings and sends files as native blocks.
func codebuddyUserContent(content string, attachments []*leapmuxv1.Attachment) any {
	classified := agent.ClassifyAttachments(attachments)
	if len(classified) == 0 {
		return content
	}
	return codebuddyContentBlocks(content, classified)
}

// codebuddyContentBlocks carries file bytes to CodeBuddy's stream-JSON input.
func codebuddyContentBlocks(content string, attachments []agent.ClassifiedAttachment) []any {
	blocks := make([]any, 0, len(attachments)*2+1)
	if content != "" {
		blocks = append(blocks, codebuddyTextBlock{Type: "text", Text: content})
	}
	for _, attachment := range attachments {
		if attachment.Kind == agent.AttachmentKindText {
			blocks = append(blocks, codebuddyTextBlock{
				Type: "text",
				Text: providerkit.BuildInlineTextAttachmentBlock(attachment),
			})
			continue
		}

		blocks = append(blocks, codebuddyTextBlock{
			Type: "text",
			Text: fmt.Sprintf("Attached file %q (%s)", attachment.Filename, attachment.MIMEType),
		})
		blockType := "document"
		if attachment.Kind == agent.AttachmentKindImage {
			blockType = "image"
		}
		blocks = append(blocks, codebuddyMediaBlock{
			Type: blockType,
			Source: codebuddyMediaSource{
				Type:      "base64",
				MediaType: attachment.MIMEType,
				Data:      base64.StdEncoding.EncodeToString(attachment.Data),
			},
		})
	}
	return blocks
}
