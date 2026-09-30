package dirac

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// diracPromptParams puts text attachment bytes into blocks Dirac actually reads.
// Dirac's ACP parser reads only a resource URI and drops resource.text.
func diracPromptParams(params map[string]any) {
	blocks, ok := params["prompt"].([]map[string]interface{})
	if !ok {
		return
	}
	for i, block := range blocks {
		if block["type"] != "resource" {
			continue
		}
		resource, ok := block["resource"].(map[string]interface{})
		if !ok {
			continue
		}
		content, ok := resource["text"].(string)
		if !ok {
			continue
		}
		filename, _ := resource["uri"].(string)
		mimeType, _ := resource["mimeType"].(string)
		text := providerkit.BuildInlineTextAttachmentBlock(agent.ClassifiedAttachment{
			Filename: filename, MIMEType: mimeType, Data: []byte(content), Kind: agent.AttachmentKindText,
		})
		blocks[i] = map[string]interface{}{"type": "text", "text": text}
	}
}
