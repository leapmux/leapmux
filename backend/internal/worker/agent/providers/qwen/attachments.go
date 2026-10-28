package qwen

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const qwenProvidersStatusMethod = "qwen/status/workspace/providers"

// prepareAudioPrompt uses the live native modality before bypassing Qwen's
// voice bridge. That bridge substitutes transcripts on ordinary text models.
// The ACP base keeps model changes and context clears outside this operation.
func (a *Agent) prepareAudioPrompt(sessionID string, attachments []agent.ClassifiedAttachment, blocks []map[string]any) error {
	hasAudio, err := providerkit.ValidateAudioAttachments(attachments, (qwenProvider{}).ValidateAttachment)
	if err != nil {
		return err
	}
	if !hasAudio {
		return nil
	}
	params, err := json.Marshal(map[string]string{"sessionId": sessionID})
	if err != nil {
		return fmt.Errorf("encode Qwen Code model query: %w", err)
	}
	raw, err := a.SendRequest(qwenProvidersStatusMethod, params, a.APITimeout())
	if err != nil {
		return fmt.Errorf("check Qwen Code audio model: %w", err)
	}
	if err := validateQwenAudioModel(raw, a.CurrentModel()); err != nil {
		return err
	}
	for index, block := range blocks {
		resource, ok := block["resource"].(map[string]any)
		if !ok || block["type"] != "resource" {
			continue
		}
		mime, _ := resource["mimeType"].(string)
		if strings.HasPrefix(mime, "audio/") {
			blocks[index] = map[string]any{"type": "audio", "mimeType": mime, "data": resource["blob"], "uri": resource["uri"]}
		}
	}
	return nil
}

func validateQwenAudioModel(raw json.RawMessage, selectedModel string) error {
	var status struct {
		Initialized bool `json:"initialized"`
		Providers   []struct {
			Kind   string `json:"kind"`
			Status string `json:"status"`
			Models []struct {
				ModelID    string `json:"modelId"`
				Modalities struct {
					Audio bool `json:"audio"`
				} `json:"modalities"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return fmt.Errorf("read Qwen Code audio capability: %w", err)
	}
	matches, audio := 0, false
	for _, provider := range status.Providers {
		if provider.Kind != "model_provider" || provider.Status != "ok" {
			continue
		}
		for _, model := range provider.Models {
			if model.ModelID == selectedModel {
				matches++
				audio = model.Modalities.Audio
			}
		}
	}
	if !status.Initialized || selectedModel == "" || matches != 1 || !audio {
		return fmt.Errorf("the selected Qwen Code model %q does not accept audio attachments", selectedModel)
	}
	return nil
}
