package deepseekharness

import (
	"encoding/base64"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// promptParts uses native image admission and native uploaded-file receipts.
func (a *Agent) promptParts(sessionID, content string, attachments []*leapmuxv1.Attachment) ([]map[string]any, error) {
	parts := []map[string]any{{"type": "text", "text": content}}
	for _, attachment := range attachments {
		if attachment == nil {
			return nil, fmt.Errorf("DeepSeek Harness received an absent attachment")
		}
		kind := agent.ClassifyAttachments([]*leapmuxv1.Attachment{attachment})[0]
		if kind.Kind == agent.AttachmentKindImage {
			parts = append(parts, map[string]any{"type": "image", "mediaType": kind.MIMEType, "data": base64.StdEncoding.EncodeToString(attachment.GetData()), "name": attachment.GetFilename()})
			continue
		}
		var receipt struct {
			ID string `json:"receiptId"`
		}
		err := a.rpc.call(a.Context(), "fileUploads/upload", map[string]any{"agentId": sessionID, "request": map[string]any{"data": base64.StdEncoding.EncodeToString(attachment.GetData()), "name": attachment.GetFilename()}}, &receipt)
		if err != nil {
			return nil, err
		}
		if receipt.ID == "" {
			return nil, fmt.Errorf("DeepSeek Harness did not confirm the file upload")
		}
		parts = append(parts, map[string]any{"type": "file", "receiptId": receipt.ID})
	}
	return parts, nil
}
