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

// RejectPDFAndBinaryAttachment enforces the "text and image only" policy shared by Codex and Pi:
// neither has an input representation for a PDF or binary content block. label names the provider in
// the rejection message so the single policy body can't drift between the two providers.
func RejectPDFAndBinaryAttachment(label string, attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == agent.AttachmentKindPDF {
		return fmt.Errorf("%s does not support PDF attachments: %s", label, attachment.Filename)
	}
	if attachment.Kind == agent.AttachmentKindBinary {
		return fmt.Errorf("%s does not support binary attachments: %s", label, attachment.Filename)
	}
	return nil
}

// BuildInlineTextAttachmentBlock renders a text attachment as a delimited block
// inside the prompt text, for a provider that sends a text file as prompt text.
// The markers state the file name and the MIME type. The block always ends the
// file content with a newline, so the END marker stays on a line of its own.
func BuildInlineTextAttachmentBlock(attachment agent.ClassifiedAttachment) string {
	var builder strings.Builder
	builder.WriteString("----- BEGIN ATTACHED FILE: ")
	builder.WriteString(attachment.Filename)
	builder.WriteString(" (")
	builder.WriteString(attachment.MIMEType)
	builder.WriteString(") -----\n")
	builder.Write(attachment.Data)
	if len(attachment.Data) == 0 || attachment.Data[len(attachment.Data)-1] != '\n' {
		builder.WriteByte('\n')
	}
	builder.WriteString("----- END ATTACHED FILE: ")
	builder.WriteString(attachment.Filename)
	builder.WriteString(" -----")
	return builder.String()
}
