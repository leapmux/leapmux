package antigravity

import (
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const antigravityMaxAttachmentBytes = 20 * 1024 * 1024

type antigravityMedia struct {
	MIMEType    string `json:"mimeType"`
	InlineData  []byte `json:"inlineData"`
	DisplayName string `json:"displayName"`
}

func (a *Agent) buildMessageItems(content string, attachments []*leapmuxv1.Attachment, command string) ([]map[string]any, []antigravityMedia, error) {
	variant, _, err := a.catalog.resolveModel(a.model, a.effort)
	if err != nil {
		return nil, nil, err
	}
	blocks := []string{}
	if content != "" {
		blocks = append(blocks, content)
	}
	media := []antigravityMedia{}
	total := 0
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		total += len(attachment.Data)
		if total > antigravityMaxAttachmentBytes {
			return nil, nil, fmt.Errorf("attachments for Antigravity CLI exceed %d bytes in one message", antigravityMaxAttachmentBytes)
		}
		if attachment.Kind == contracts.AttachmentMediaKindText {
			blocks = append(blocks, providerkit.BuildInlineTextAttachmentBlock(attachment))
			continue
		}
		if !variant.mimeTypes[attachment.MIMEType] {
			label := a.model
			if agent.UsesAccountDefaultModel(label) {
				label = "the default Antigravity model"
			}
			return nil, nil, fmt.Errorf("%s cannot read %s. Choose another model or remove the attachment", label, attachment.MIMEType)
		}
		media = append(media, antigravityMedia{MIMEType: attachment.MIMEType, InlineData: attachment.Data, DisplayName: attachment.Filename})
	}
	items := []map[string]any{}
	if command != "" {
		items = append(items, slashCommandItem(command))
	} else if a.mode == contracts.AntigravityModePlan {
		items = append(items, slashCommandItem("plan"))
	}
	if len(blocks) > 0 {
		items = append(items, map[string]any{"text": strings.Join(blocks, "\n\n")})
	}
	if len(items) == 0 {
		items = append(items, map[string]any{"text": ""})
	}
	return items, media, nil
}

func (a *Agent) slashCommand(content string) (name, rest string, matched bool) {
	if !strings.HasPrefix(content, "/") {
		return "", content, false
	}
	name, rest, _ = strings.Cut(content[1:], " ")
	for _, candidate := range a.slashCommands {
		if name == candidate {
			return name, rest, true
		}
	}
	return "", content, false
}
