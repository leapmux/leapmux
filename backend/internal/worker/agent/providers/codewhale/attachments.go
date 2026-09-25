package codewhale

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Codewhale's attachment path.
//
// A turn takes text and inline images, and nothing else: its `images` field is
// `[{mime, dataBase64}]`, and the runtime has no file field -- an `@path` in the
// prompt is only a prompt convention. So a text attachment is inlined into the
// prompt, an image travels in `images`, and a PDF or any other binary is
// refused. codewhaleProvider.ValidateAttachment states the same policy for the
// stateless check, and the browser plugin's `configuration.attachments` states
// it for the composer.
//
// The runtime refuses an image for a model whose route does not report
// `image_input: supported`, with a 400 that names the rule. The catalog states
// the same capability, so a model it marks `unsupported` is refused here with a
// message about the model, before the request. A model it marks `unknown` is left
// to the runtime.

// Image limits of the runtime (`crates/protocol/src/runtime/mod.rs`): at most
// ten images, each at most 4 MiB and all at most 5 MiB, measured decoded.
const (
	maxTurnImages           = 10
	maxTurnImageBytes       = 4 << 20
	maxTurnImagesTotalBytes = 5 << 20
)

// validateCodewhaleAttachment is the static attachment policy.
func validateCodewhaleAttachment(attachment agent.ClassifiedAttachment) error {
	return providerkit.RejectPDFAndBinaryAttachment("Codewhale", attachment)
}

// buildTurnInput turns a message and its attachments into the prompt and the
// images of one turn.
func buildTurnInput(content string, classified []agent.ClassifiedAttachment, imageInput imageInputSupport) (string, []turnImage, error) {
	if len(classified) == 0 {
		return content, nil, nil
	}
	blocks := make([]string, 0, len(classified)+1)
	if content != "" {
		blocks = append(blocks, content)
	}
	var images []turnImage
	total := 0
	for _, attachment := range classified {
		if err := validateCodewhaleAttachment(attachment); err != nil {
			return "", nil, err
		}
		switch attachment.Kind {
		case agent.AttachmentKindText:
			blocks = append(blocks, providerkit.BuildInlineTextAttachmentBlock(attachment))
		case agent.AttachmentKindImage:
			if imageInput == imageInputUnsupported {
				return "", nil, fmt.Errorf("the current Codewhale model does not accept images: %s", attachment.Filename)
			}
			if len(images) == maxTurnImages {
				return "", nil, fmt.Errorf("the Codewhale runtime accepts at most %d images in one message", maxTurnImages)
			}
			if len(attachment.Data) > maxTurnImageBytes {
				return "", nil, fmt.Errorf("the image %s is larger than the %d MiB that Codewhale accepts", attachment.Filename, maxTurnImageBytes>>20)
			}
			total += len(attachment.Data)
			if total > maxTurnImagesTotalBytes {
				return "", nil, fmt.Errorf("the images of one message are larger than the %d MiB that Codewhale accepts", maxTurnImagesTotalBytes>>20)
			}
			images = append(images, turnImage{
				Mime:       attachment.MIMEType,
				DataBase64: base64.StdEncoding.EncodeToString(attachment.Data),
			})
		case agent.AttachmentKindPDF, agent.AttachmentKindBinary:
			// validateCodewhaleAttachment refused both above: a turn carries text
			// and images alone.
		}
	}
	return strings.Join(blocks, "\n\n"), images, nil
}
