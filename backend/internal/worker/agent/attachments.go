package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type attachmentKind string

const (
	attachmentKindText   attachmentKind = "text"
	attachmentKindImage  attachmentKind = "image"
	attachmentKindPDF    attachmentKind = "pdf"
	attachmentKindBinary attachmentKind = "binary"
)

type classifiedAttachment struct {
	filename string
	mimeType string
	data     []byte
	kind     attachmentKind
}

var supportedImageMIMETypes = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/webp": {},
}

var mimeByExtension = map[string]string{
	".txt":          "text/plain",
	".md":           "text/markdown",
	".markdown":     "text/markdown",
	".csv":          "text/csv",
	".tsv":          "text/tab-separated-values",
	".css":          "text/css",
	".html":         "text/html",
	".htm":          "text/html",
	".xml":          "application/xml",
	".json":         "application/json",
	".jsonc":        "application/json",
	".yaml":         "application/yaml",
	".yml":          "application/yaml",
	".toml":         "application/toml",
	".ini":          "text/plain",
	".cfg":          "text/plain",
	".conf":         "text/plain",
	".env":          "text/plain",
	".log":          "text/plain",
	".sh":           "text/x-shellscript",
	".bash":         "text/x-shellscript",
	".zsh":          "text/x-shellscript",
	".fish":         "text/x-shellscript",
	".js":           "text/javascript",
	".mjs":          "text/javascript",
	".cjs":          "text/javascript",
	".ts":           "text/typescript",
	".tsx":          "text/typescript",
	".jsx":          "text/javascript",
	".py":           "text/x-python",
	".rb":           "text/plain",
	".go":           "text/plain",
	".rs":           "text/plain",
	".java":         "text/plain",
	".kt":           "text/plain",
	".swift":        "text/plain",
	".c":            "text/plain",
	".cc":           "text/plain",
	".cpp":          "text/plain",
	".cxx":          "text/plain",
	".h":            "text/plain",
	".hh":           "text/plain",
	".hpp":          "text/plain",
	".sql":          "text/plain",
	".graphql":      "application/graphql",
	".gql":          "application/graphql",
	".dockerfile":   "text/plain",
	".gitignore":    "text/plain",
	".editorconfig": "text/plain",
	".svg":          "image/svg+xml",
	".pdf":          "application/pdf",
	".png":          "image/png",
	".jpg":          "image/jpeg",
	".jpeg":         "image/jpeg",
	".gif":          "image/gif",
	".webp":         "image/webp",
}

func classifyAttachments(attachments []*leapmuxv1.Attachment) []classifiedAttachment {
	result := make([]classifiedAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment == nil {
			continue
		}
		result = append(result, classifyAttachment(attachment))
	}
	return result
}

func NormalizeAttachmentsForProvider(provider leapmuxv1.AgentProvider, attachments []*leapmuxv1.Attachment) ([]*leapmuxv1.Attachment, error) {
	plugin := ProviderFor(provider)
	classified := classifyAttachments(attachments)
	normalized := make([]*leapmuxv1.Attachment, 0, len(classified))
	for _, attachment := range classified {
		if err := plugin.ValidateAttachment(attachment); err != nil {
			return nil, err
		}
		normalized = append(normalized, &leapmuxv1.Attachment{
			Filename: attachment.filename,
			MimeType: attachment.mimeType,
			Data:     attachment.data,
		})
	}
	return normalized, nil
}

func classifyAttachment(attachment *leapmuxv1.Attachment) classifiedAttachment {
	filename := attachment.GetFilename()
	data := attachment.GetData()
	mimeType := inferAttachmentMimeType(filename, attachment.GetMimeType(), data)

	switch {
	case isSupportedImageMimeType(mimeType):
		return classifiedAttachment{filename: filename, mimeType: mimeType, data: data, kind: attachmentKindImage}
	case mimeType == "application/pdf":
		return classifiedAttachment{filename: filename, mimeType: mimeType, data: data, kind: attachmentKindPDF}
	case isTextAttachmentMimeType(mimeType) && utf8.Valid(data):
		return classifiedAttachment{filename: filename, mimeType: mimeType, data: data, kind: attachmentKindText}
	default:
		return classifiedAttachment{filename: filename, mimeType: mimeType, data: data, kind: attachmentKindBinary}
	}
}

func inferAttachmentMimeType(filename, mimeType string, data []byte) string {
	normalizedMime := strings.TrimSpace(strings.ToLower(mimeType))
	if normalizedMime != "" && normalizedMime != "application/octet-stream" {
		return normalizedMime
	}

	if inferred := mimeTypeFromFilename(filename); inferred != "" {
		return inferred
	}

	if utf8.Valid(data) {
		return "text/plain"
	}

	if normalizedMime != "" {
		return normalizedMime
	}
	return "application/octet-stream"
}

func mimeTypeFromFilename(filename string) string {
	lower := strings.ToLower(strings.TrimSpace(filename))
	switch lower {
	case "dockerfile", ".gitignore", ".editorconfig", ".env":
		return "text/plain"
	}
	ext := strings.ToLower(filepath.Ext(lower))
	return mimeByExtension[ext]
}

func isSupportedImageMimeType(mimeType string) bool {
	_, ok := supportedImageMIMETypes[mimeType]
	return ok
}

func isTextAttachmentMimeType(mimeType string) bool {
	return strings.HasPrefix(mimeType, "text/") ||
		mimeType == "application/json" ||
		mimeType == "application/xml" ||
		mimeType == "application/yaml" ||
		mimeType == "application/toml" ||
		mimeType == "application/graphql" ||
		mimeType == "image/svg+xml" ||
		strings.HasSuffix(mimeType, "+json") ||
		strings.HasSuffix(mimeType, "+xml")
}

// ValidateAttachment defaults to accepting every classified attachment. Providers with no
// restriction (Cursor, Copilot, Kilo, OpenCode, Goose) and unknown providers (via the ProviderFor
// noop fallback) inherit this; the ACP providers reach it through their noopProvider embedding
// unless they register a restrictive validateAttachment hook.
func (noopProvider) ValidateAttachment(classifiedAttachment) error { return nil }

// Claude Code accepts text, image, and PDF blocks but has no binary content block.
func (claudeProvider) ValidateAttachment(attachment classifiedAttachment) error {
	if attachment.kind == attachmentKindBinary {
		return fmt.Errorf("claude code does not support binary attachments: %s", attachment.filename)
	}
	return nil
}

// rejectPDFAndBinaryAttachment enforces the "text and image only" policy shared by Codex and Pi:
// neither has an input representation for a PDF or binary content block. label names the provider in
// the rejection message so the single policy body can't drift between the two providers.
func rejectPDFAndBinaryAttachment(label string, attachment classifiedAttachment) error {
	if attachment.kind == attachmentKindPDF {
		return fmt.Errorf("%s does not support PDF attachments: %s", label, attachment.filename)
	}
	if attachment.kind == attachmentKindBinary {
		return fmt.Errorf("%s does not support binary attachments: %s", label, attachment.filename)
	}
	return nil
}

// Codex accepts text and image blocks; PDF and binary have no representation in its input.
func (codexProvider) ValidateAttachment(attachment classifiedAttachment) error {
	return rejectPDFAndBinaryAttachment("codex", attachment)
}

// Pi accepts text and image blocks; PDF and binary have no representation in its input.
func (piProvider) ValidateAttachment(attachment classifiedAttachment) error {
	return rejectPDFAndBinaryAttachment("pi", attachment)
}

// ValidateAttachment dispatches to the ACP provider's registered policy hook. It must override the
// embedded noopProvider method explicitly -- the embedded default would ignore validateAttachment.
// nil hook accepts everything (Cursor, Copilot, Kilo, OpenCode, Goose).
func (p acpProvider) ValidateAttachment(attachment classifiedAttachment) error {
	if p.validateAttachment != nil {
		return p.validateAttachment(attachment)
	}
	return nil
}

// reasonixValidateAttachment enforces Reasonix's text-only policy: it advertises
// image:false/audio:false and drops any non-text content block, so reject everything but text up
// front. Registered as the ACP validateAttachment hook for Reasonix.
func reasonixValidateAttachment(attachment classifiedAttachment) error {
	if attachment.kind != attachmentKindText {
		return fmt.Errorf("reasonix only supports text attachments: %s", attachment.filename)
	}
	return nil
}

func buildInlineTextAttachmentBlock(attachment classifiedAttachment) string {
	var builder strings.Builder
	builder.WriteString("----- BEGIN ATTACHED FILE: ")
	builder.WriteString(attachment.filename)
	builder.WriteString(" (")
	builder.WriteString(attachment.mimeType)
	builder.WriteString(") -----\n")
	builder.Write(attachment.data)
	if len(attachment.data) == 0 || attachment.data[len(attachment.data)-1] != '\n' {
		builder.WriteByte('\n')
	}
	builder.WriteString("----- END ATTACHED FILE: ")
	builder.WriteString(attachment.filename)
	builder.WriteString(" -----")
	return builder.String()
}
