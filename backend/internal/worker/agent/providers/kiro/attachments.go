package kiro

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ValidateAttachment permits Kiro's native Word and Excel document blobs.
// Text, image and PDF input retain their existing policy; audio stays separate.
func (kiroProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if err := (agent.ProviderDefaults{}).ValidateAttachment(attachment); err != nil {
		return err
	}
	if attachment.Kind != contracts.AttachmentMediaKindBinary {
		return nil
	}
	if !slices.Contains(contracts.KiroBinaryMIMEMembers, attachment.MIMEType) {
		return fmt.Errorf("the Kiro agent cannot read the binary attachment %q", attachment.Filename)
	}
	if len(attachment.Data) == 0 {
		return fmt.Errorf("the Kiro document attachment %q is empty", attachment.Filename)
	}
	var header []byte
	switch attachment.MIMEType {
	case contracts.AttachmentMediaDocumentMIMEDoc, contracts.AttachmentMediaDocumentMIMEXls:
		header = []byte{0xd0, 0xcf, 0x11, 0xe0}
	case contracts.AttachmentMediaDocumentMIMEDocx, contracts.AttachmentMediaDocumentMIMEXlsx:
		header = []byte{'P', 'K', 3, 4}
	default:
		return fmt.Errorf("the Kiro document type of %q has no supported header", attachment.Filename)
	}
	if !bytes.HasPrefix(attachment.Data, header) {
		return fmt.Errorf("the attachment %q does not match its declared Office file type", attachment.Filename)
	}
	return nil
}

// kiroPromptParams removes the file URI from image blocks before Kiro v3 reads
// them. With both data and URI, Kiro v3 drops the image from its model request.
// Text resources keep their original URI. Document blobs get payload identities:
// Kiro strips extensions and silently keeps an older document with the same name.
func kiroPromptParams(params map[string]any) {
	blocks, ok := params["prompt"].([]map[string]interface{})
	if !ok {
		return
	}
	for _, block := range blocks {
		if block["type"] == "image" {
			delete(block, "uri")
			continue
		}
		if block["type"] != "resource" {
			continue
		}
		resource, ok := block["resource"].(map[string]interface{})
		if !ok {
			continue
		}
		mime, ok := resource["mimeType"].(string)
		if !ok || mime != contracts.AttachmentMediaDocumentMIMEPdf && !slices.Contains(contracts.KiroBinaryMIMEMembers, mime) {
			continue
		}
		blob, ok := resource["blob"].(string)
		if !ok {
			continue
		}
		filename, ok := resource["uri"].(string)
		if !ok {
			continue
		}
		resource["uri"] = kiroDocumentURI(filename, mime, blob)
	}
}

// The shared prompt builder uses the original filename as the resource URI.
// Prefix the full content digest so native normalization cannot merge distinct bytes.
func kiroDocumentURI(filename, mime, blob string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, mime)
	_, _ = io.WriteString(hash, "\x00")
	_, _ = io.WriteString(hash, blob)
	prefix := hex.EncodeToString(hash.Sum(nil)) + "-"
	name := path.Base(strings.ReplaceAll(filename, "\\", "/"))
	if strings.HasPrefix(name, prefix) {
		return filename
	}
	if name == "." || name == "/" {
		name = "attachment"
	}
	return (&url.URL{Path: prefix + name}).String()
}
