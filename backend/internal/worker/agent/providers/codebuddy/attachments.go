package codebuddy

import (
	"encoding/base64"
	"fmt"
	"mime"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ValidateAttachment admits audio through CodeBuddy's generic native file input.
func (codebuddyProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind != contracts.AttachmentMediaKindAudio {
		return (agent.ProviderDefaults{}).ValidateAttachment(attachment)
	}
	mediaType, _, err := mime.ParseMediaType(attachment.MIMEType)
	if err != nil || !strings.HasPrefix(mediaType, "audio/") {
		return fmt.Errorf("codebuddy audio attachment %q has an invalid media type", attachment.Filename)
	}
	if len(attachment.Data) == 0 {
		return fmt.Errorf("audio attachment %q is empty", attachment.Filename)
	}
	return nil
}

// codebuddyUserContent keeps plain prompts as strings and sends files as native blocks.
func codebuddyUserContent(content string, attachments []*leapmuxv1.Attachment) any {
	classified := agent.ClassifyAttachments(attachments)
	if len(classified) == 0 {
		return content
	}
	return codebuddyContentBlocks(content, classified)
}

// codebuddyContentBlocks carries file bytes to CodeBuddy's stream-JSON input.
//
// Every attachment comes first, in the order that the user attached it, and the
// prompt text comes last. A media file keeps its label text block directly
// before its own block.
//
// CodeBuddy takes the LAST plain text block of a user message as the words of
// the user (its extractTextContent). These read that block:
//   - The UserPromptSubmit hooks, as their prompt.
//   - The review of sensitive user input.
//   - The compaction summary and the session summary.
//
// A prompt before the attachments loses to the label of the last media file, or
// to the last attached text file. CodeBuddy finds a slash command and an
// @-mention in any text block, so the order does not change those.
func codebuddyContentBlocks(content string, attachments []agent.ClassifiedAttachment) []any {
	blocks := make([]any, 0, len(attachments)*2+1)
	for _, attachment := range attachments {
		if attachment.Kind == contracts.AttachmentMediaKindText {
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
		if attachment.Kind == contracts.AttachmentMediaKindImage {
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
	if content != "" {
		blocks = append(blocks, codebuddyTextBlock{Type: "text", Text: content})
	}
	return blocks
}
