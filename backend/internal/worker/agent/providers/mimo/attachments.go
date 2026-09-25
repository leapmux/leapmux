package mimo

import (
	"fmt"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// MiMo's attachment path.
//
// A prompt carries a file as a part with a data URL, and the server converts it
// by its type: a `text/plain` file becomes a synthetic read of the file, and an
// image, audio, video or PDF file reaches the model as media. A model that lacks
// the modality gets a text notice in its place ("this model does not support
// image input") instead of a failed request, so the worker needs no per-model
// gate. A file of any other type passes through unconverted, and the model's API
// can refuse the whole request for it, so a binary attachment is refused here.
//
// Text is inlined into the prompt instead, as Pi and ZCode do. MiMo converts
// only the exact type `text/plain`, and a Markdown or source file would pass
// through as an unknown type.

// mimoMaxAttachmentBytes caps one prompt's total attachment payload. MiMo's own
// default cap for one attachment is 50 MB, and a base64 body of that size is
// larger than any request a local server should parse in one piece, so the
// worker applies the tighter cap that ZCode's upload route states.
const mimoMaxAttachmentBytes = 20 * 1024 * 1024

// buildPromptParts turns a user message and its attachments into prompt parts:
// one text part that holds the message and every text attachment, then one file
// part for each image and PDF, in the order the user attached them.
func buildPromptParts(content string, attachments []*leapmuxv1.Attachment) ([]mimoPromptPart, error) {
	classified := agent.ClassifyAttachments(attachments)
	blocks := []string{}
	if content != "" {
		blocks = append(blocks, content)
	}
	var files []mimoPromptPart
	total := 0
	for _, attachment := range classified {
		if err := (mimoProvider{}).ValidateAttachment(attachment); err != nil {
			return nil, err
		}
		total += len(attachment.Data)
		if total > mimoMaxAttachmentBytes {
			return nil, fmt.Errorf("MiMo Code attachments exceed %d bytes in one message", mimoMaxAttachmentBytes)
		}
		switch attachment.Kind {
		case agent.AttachmentKindText:
			blocks = append(blocks, providerkit.BuildInlineTextAttachmentBlock(attachment))
		case agent.AttachmentKindImage, agent.AttachmentKindPDF:
			files = append(files, mimoPromptPart{
				Type:     promptPartFile,
				URL:      providerkit.EncodeDataURI(attachment.MIMEType, attachment.Data),
				Mime:     attachment.MIMEType,
				Filename: attachment.Filename,
			})
		default:
			return nil, fmt.Errorf("MiMo Code does not support binary attachments: %s", attachment.Filename)
		}
	}
	parts := make([]mimoPromptPart, 0, 1+len(files))
	if len(blocks) > 0 {
		parts = append(parts, mimoPromptPart{Type: promptPartText, Text: strings.Join(blocks, "\n\n")})
	}
	parts = append(parts, files...)
	// The server refuses a prompt with no part. An empty message with no
	// attachment is the one input that reaches here with none, and it is sent as
	// what it is: one empty text part. A message of files alone carries no empty
	// text part, because a model API can refuse an empty text block.
	if len(parts) == 0 {
		parts = append(parts, mimoPromptPart{Type: promptPartText})
	}
	return parts, nil
}
