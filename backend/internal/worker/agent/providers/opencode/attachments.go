package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// prepareAudioPrompt checks the model of the explicit target session against
// the same daemon's catalog. Google preserves embedded audio bytes; other
// native adapters can refuse the file or replace it with a text placeholder.
func (b *FamilyBase) prepareAudioPrompt(sessionID string, attachments []agent.ClassifiedAttachment, _ []map[string]any) error {
	hasAudio, err := providerkit.ValidateAudioAttachments(attachments, (FamilyProvider{}).ValidateAttachment)
	if err != nil {
		return err
	}
	if !hasAudio {
		return nil
	}
	modelID := ""
	if b.IsCurrentSession(sessionID) {
		modelID = b.CurrentModel()
	} else {
		b.childSessionsMu.Lock()
		for _, child := range b.childSessions {
			if child.id == sessionID && child.loaded {
				modelID = child.modelID
				break
			}
		}
		b.childSessionsMu.Unlock()
	}
	if modelID == "" {
		return fmt.Errorf("cannot verify audio input because the target session has no confirmed model")
	}
	ctx, cancel := context.WithTimeout(b.Context(), b.APITimeout())
	defer cancel()
	raw, err := b.ReadDaemon(ctx, "/provider")
	if err != nil {
		return fmt.Errorf("check %s audio model: %w", b.ProviderName(), err)
	}
	return validateFamilyAudioModel(raw, modelID)
}

func validateFamilyAudioModel(raw []byte, selectedModel string) error {
	providerID, modelID, qualified := strings.Cut(selectedModel, "/")
	if !qualified || providerID == "" || modelID == "" {
		return fmt.Errorf("cannot verify audio input for model %q", selectedModel)
	}
	var catalog struct {
		All []struct {
			ID     string `json:"id"`
			Models map[string]struct {
				ID         string `json:"id"`
				ProviderID string `json:"providerID"`
				API        struct {
					NPM string `json:"npm"`
				} `json:"api"`
				Capabilities struct {
					Input struct {
						Audio bool `json:"audio"`
					} `json:"input"`
				} `json:"capabilities"`
			} `json:"models"`
		} `json:"all"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return fmt.Errorf("read the native audio model catalog: %w", err)
	}
	providers, models := 0, 0
	audio, google := false, false
	for _, provider := range catalog.All {
		if provider.ID != providerID {
			continue
		}
		providers++
		model, ok := provider.Models[modelID]
		if !ok || model.ID != modelID || model.ProviderID != providerID {
			continue
		}
		models++
		audio = model.Capabilities.Input.Audio
		google = model.API.NPM == "@ai-sdk/google"
	}
	if providers != 1 || models != 1 {
		return fmt.Errorf("cannot verify audio input for model %q from the native catalog", selectedModel)
	}
	if !audio || !google {
		return fmt.Errorf("the selected model %q does not accept audio attachments through this agent; choose a Google model with audio input", selectedModel)
	}
	return nil
}

func validateOpenCodeAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == contracts.AttachmentMediaKindAudio {
		// OpenCode's attachmentContent discards audio before model input, including for models that advertise audio.
		// See packages/core/src/session/runner/to-llm-message.ts in the native source.
		return fmt.Errorf("OpenCode does not support audio attachments: %q", attachment.Filename)
	}
	if attachment.Kind == contracts.AttachmentMediaKindBinary {
		return fmt.Errorf("OpenCode does not support this binary attachment: %s", attachment.Filename)
	}
	return (agent.ProviderDefaults{}).ValidateAttachment(attachment)
}

// buildOpenCodePrompt validates the complete input before it encodes media.
// Text files use the shared inline representation. Media keeps its original bytes.
func buildOpenCodePrompt(content string, attachments []*leapmuxv1.Attachment, model *openCodeModelInfo) (openCodePrompt, error) {
	classified := agent.ClassifyAttachments(attachments)
	remaining := contracts.MaxMessageSize - len(content)
	if remaining < 0 {
		return openCodePrompt{}, fmt.Errorf("OpenCode input exceeds %d bytes", contracts.MaxMessageSize)
	}
	for _, attachment := range classified {
		if err := validateOpenCodeAttachment(attachment); err != nil {
			return openCodePrompt{}, err
		}
		if len(attachment.Data) > remaining {
			return openCodePrompt{}, fmt.Errorf("OpenCode input exceeds %d bytes", contracts.MaxMessageSize)
		}
		remaining -= len(attachment.Data)
		if attachment.Kind == contracts.AttachmentMediaKindText {
			continue
		}
		if len(attachment.Data) == 0 {
			return openCodePrompt{}, fmt.Errorf("media attachment %q is empty", attachment.Filename)
		}
		if model == nil {
			return openCodePrompt{}, fmt.Errorf("cannot verify attachment %q because the selected OpenCode model is unknown", attachment.Filename)
		}
		if !slices.Contains(model.Capabilities.Input, string(attachment.Kind)) {
			return openCodePrompt{}, fmt.Errorf("OpenCode model %q does not accept %s attachments", model.Name, attachment.Kind)
		}
	}
	var blocks []string
	if content != "" {
		blocks = append(blocks, content)
	}
	request := openCodePrompt{}
	for _, attachment := range classified {
		if attachment.Kind == contracts.AttachmentMediaKindText {
			blocks = append(blocks, providerkit.BuildInlineTextAttachmentBlock(attachment))
			continue
		}
		request.Files = append(request.Files, openCodePromptFile{
			URI: providerkit.EncodeDataURI(attachment.MIMEType, attachment.Data), Name: attachment.Filename,
		})
	}
	request.Text = strings.Join(blocks, "\n\n")
	return request, nil
}
