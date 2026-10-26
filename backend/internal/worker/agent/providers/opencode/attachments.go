package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
