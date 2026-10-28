package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type ClassifiedAttachment struct {
	Filename string
	MIMEType string
	Data     []byte
	Kind     contracts.AttachmentMediaKind
}

var supportedImageMIMETypes = map[string]struct{}{
	contracts.AttachmentMediaImageMIMEPng:  {},
	contracts.AttachmentMediaImageMIMEJpeg: {},
	contracts.AttachmentMediaImageMIMEGif:  {},
	contracts.AttachmentMediaImageMIMEWebp: {},
}

var mimeByExtension = map[string]string{
	".wav":  contracts.AttachmentMediaAudioMIMEWav,
	".mp3":  contracts.AttachmentMediaAudioMIMEMpeg,
	".m4a":  contracts.AttachmentMediaAudioMIMEMp4,
	".webm": contracts.AttachmentMediaAudioMIMEWebm,
	".ogg":  contracts.AttachmentMediaAudioMIMEOgg,
	".flac": contracts.AttachmentMediaAudioMIMEFlac,
	".aac":  contracts.AttachmentMediaAudioMIMEAac,
	".doc":  contracts.AttachmentMediaDocumentMIMEDoc,
	".docx": contracts.AttachmentMediaDocumentMIMEDocx,
	".xls":  contracts.AttachmentMediaDocumentMIMEXls,
	".xlsx": contracts.AttachmentMediaDocumentMIMEXlsx,

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
	".pdf":          contracts.AttachmentMediaDocumentMIMEPdf,
	".png":          contracts.AttachmentMediaImageMIMEPng,
	".jpg":          contracts.AttachmentMediaImageMIMEJpeg,
	".jpeg":         contracts.AttachmentMediaImageMIMEJpeg,
	".gif":          contracts.AttachmentMediaImageMIMEGif,
	".webp":         contracts.AttachmentMediaImageMIMEWebp,
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
		if attachment.Kind == contracts.AttachmentMediaKindAudio && len(attachment.Data) == 0 {
			return nil, fmt.Errorf("audio attachment %q is empty", attachment.Filename)
		}
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
		return ClassifiedAttachment{Filename: filename, MIMEType: mimeType, Data: data, Kind: contracts.AttachmentMediaKindImage}
	case mimeType == contracts.AttachmentMediaDocumentMIMEPdf:
		return ClassifiedAttachment{Filename: filename, MIMEType: mimeType, Data: data, Kind: contracts.AttachmentMediaKindPdf}
	case strings.HasPrefix(mimeType, "audio/"):
		return ClassifiedAttachment{Filename: filename, MIMEType: mimeType, Data: data, Kind: contracts.AttachmentMediaKindAudio}
	case isTextAttachmentMimeType(mimeType) && utf8.Valid(data):
		return ClassifiedAttachment{Filename: filename, MIMEType: mimeType, Data: data, Kind: contracts.AttachmentMediaKindText}
	default:
		return ClassifiedAttachment{Filename: filename, MIMEType: mimeType, Data: data, Kind: contracts.AttachmentMediaKindBinary}
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

// ValidateAttachment requires each provider to implement its audio route explicitly.
// Other kinds retain the provider's existing policy.
func (ProviderDefaults) ValidateAttachment(attachment ClassifiedAttachment) error {
	if attachment.Kind == contracts.AttachmentMediaKindAudio {
		return fmt.Errorf("audio attachments are not supported by this provider")
	}
	return nil
}
