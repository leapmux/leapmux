package providerkit

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// EncodeDataURI builds a data URI from a MIME type and raw bytes.
func EncodeDataURI(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// RejectPDFAndBinaryAttachment enforces the "text and image only" policy of each provider that has no
// input representation for a PDF or binary content block. label gives the provider name in the
// rejection message, so every provider that calls this function shares one policy body.
func RejectPDFAndBinaryAttachment(label string, attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == agent.AttachmentKindPDF {
		return fmt.Errorf("%s does not support PDF attachments: %s", label, attachment.Filename)
	}
	if attachment.Kind == agent.AttachmentKindBinary {
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
