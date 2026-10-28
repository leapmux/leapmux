package providerkit

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// EncodeDataURI builds a data URI from a MIME type and raw bytes.
func EncodeDataURI(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// ValidateAudioAttachment checks nonempty bytes and the provider's accepted
// media types. The caller selects audio and supplies its diagnostic description.
func ValidateAudioAttachment(attachment agent.ClassifiedAttachment, description string, allowedMIMEs []string) error {
	if len(attachment.Data) == 0 || !slices.Contains(allowedMIMEs, attachment.MIMEType) {
		return fmt.Errorf("cannot send %s %q with media type %s", description, attachment.Filename, attachment.MIMEType)
	}
	return nil
}

// ValidateAudioAttachments checks each audio attachment through the provider's
// validator. It returns whether audio is present and the first validation error.
// Non-audio attachments keep their existing provider policy.
func ValidateAudioAttachments(attachments []agent.ClassifiedAttachment, validate func(agent.ClassifiedAttachment) error) (bool, error) {
	if validate == nil {
		return false, fmt.Errorf("audio attachment validation requires a provider validator")
	}
	found := false
	for _, attachment := range attachments {
		if attachment.Kind != contracts.AttachmentMediaKindAudio {
			continue
		}
		found = true
		if err := validate(attachment); err != nil {
			return found, err
		}
	}
	return found, nil
}

// ValidateTextAndImageAttachment enforces the "text and image only" policy of each provider that has no
// input representation for PDF, audio or binary content blocks. label gives the provider name in the
// rejection message, so every provider that calls this function shares one policy body.
//
// A shared handoff of a PDF as a file path, for the agents without PDF input:
// https://github.com/leapmux/leapmux/issues/537.
func ValidateTextAndImageAttachment(label string, attachment agent.ClassifiedAttachment) error {
	if err := (agent.ProviderDefaults{}).ValidateAttachment(attachment); err != nil {
		return err
	}
	if attachment.Kind == contracts.AttachmentMediaKindPdf {
		return fmt.Errorf("%s does not support PDF attachments: %s", label, attachment.Filename)
	}
	if attachment.Kind == contracts.AttachmentMediaKindBinary {
		return fmt.Errorf("%s does not support binary attachments: %s", label, attachment.Filename)
	}
	return nil
}

// BuildInlineTextAttachmentBlock renders a text attachment as an attached-file
// element inside the prompt text, for a provider that sends a text file as
// prompt text:
//
//	<attached-file name="notes.txt" mime-type="text/plain">
//	line one
//	</attached-file>
//
// The element opens with an XML-style tag on purpose. Claude Code and Qoder skip
// a text block that opens with such a tag when they look for the prompt that
// gives a session its title (Claude Code 2.1.289: uGe and Udn; Qoder 1.1.65:
// cTe). A text file that comes before the prompt therefore never becomes the
// session title.
//
// The function escapes the attribute values, so a file name cannot end the
// opening tag or break it across lines. The file content stays byte for byte,
// so the model reads the exact file. The element always ends the content with a
// newline, so the closing tag stays on a line of its own.
func BuildInlineTextAttachmentBlock(attachment agent.ClassifiedAttachment) string {
	var builder strings.Builder
	builder.Grow(len(attachment.Filename) + len(attachment.MIMEType) + len(attachment.Data) + 64)
	builder.WriteString(`<attached-file name="`)
	builder.WriteString(attachedFileAttribute.Replace(attachment.Filename))
	builder.WriteString(`" mime-type="`)
	builder.WriteString(attachedFileAttribute.Replace(attachment.MIMEType))
	builder.WriteString("\">\n")
	builder.Write(attachment.Data)
	if len(attachment.Data) == 0 || attachment.Data[len(attachment.Data)-1] != '\n' {
		builder.WriteByte('\n')
	}
	builder.WriteString("</attached-file>")
	return builder.String()
}

// attachedFileAttribute escapes an attribute value of the attached-file
// element. It escapes the markup characters, so the value cannot end the
// attribute or the tag. It also escapes the line breaks and the tab, so the
// opening tag stays on one line.
var attachedFileAttribute = strings.NewReplacer(
	"&", "&amp;",
	`"`, "&quot;",
	"<", "&lt;",
	">", "&gt;",
	"\n", "&#10;",
	"\r", "&#13;",
	"\t", "&#9;",
)
