package agent

import (
	"path/filepath"
	"strings"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type AttachmentKind string

const (
	AttachmentKindText   AttachmentKind = "text"
	AttachmentKindImage  AttachmentKind = "image"
	AttachmentKindPDF    AttachmentKind = "pdf"
	AttachmentKindBinary AttachmentKind = "binary"
)

type ClassifiedAttachment struct {
	Filename string
	MIMEType string
	Data     []byte
	Kind     AttachmentKind
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

func ClassifyAttachments(attachments []*leapmuxv1.Attachment) []ClassifiedAttachment {
	result := make([]ClassifiedAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment == nil {
			continue
		}
		result = append(result, classifyAttachment(attachment))
	}
	return result
}

func (r *Registry) NormalizeAttachments(provider leapmuxv1.AgentProvider, attachments []*leapmuxv1.Attachment) ([]*leapmuxv1.Attachment, error) {
	plugin := r.Plugin(provider)
	classified := ClassifyAttachments(attachments)
	normalized := make([]*leapmuxv1.Attachment, 0, len(classified))
	for _, attachment := range classified {
		if err := plugin.ValidateAttachment(attachment); err != nil {
			return nil, err
		}
		normalized = append(normalized, &leapmuxv1.Attachment{
			Filename: attachment.Filename,
			MimeType: attachment.MIMEType,
			Data:     attachment.Data,
		})
	}
	return normalized, nil
}

func classifyAttachment(attachment *leapmuxv1.Attachment) ClassifiedAttachment {
	filename := attachment.GetFilename()
	data := attachment.GetData()
	mimeType := inferAttachmentMimeType(filename, attachment.GetMimeType(), data)

	switch {
	case isSupportedImageMimeType(mimeType):
		return ClassifiedAttachment{Filename: filename, MIMEType: mimeType, Data: data, Kind: AttachmentKindImage}
	case mimeType == "application/pdf":
		return ClassifiedAttachment{Filename: filename, MIMEType: mimeType, Data: data, Kind: AttachmentKindPDF}
	case isTextAttachmentMimeType(mimeType) && utf8.Valid(data):
		return ClassifiedAttachment{Filename: filename, MIMEType: mimeType, Data: data, Kind: AttachmentKindText}
	default:
		return ClassifiedAttachment{Filename: filename, MIMEType: mimeType, Data: data, Kind: AttachmentKindBinary}
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
// restriction (Cursor, Copilot, Kilo, OpenCode, Goose) and unknown providers (via the
// ProviderDefaults that Registry.Plugin answers for them) inherit this; an ACP provider reaches it
// through its ProviderDefaults embedding unless its own plugin type states a restrictive policy.
func (ProviderDefaults) ValidateAttachment(ClassifiedAttachment) error { return nil }
