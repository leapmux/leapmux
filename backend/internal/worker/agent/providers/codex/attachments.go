package codex

import (
	"fmt"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func codexAudioMIME(mime string) bool {
	return slices.Contains(contracts.CodexAudioMIMEMembers, mime)
}

// validateCodexAudioInputLocked prevents the native runtime from silently
// removing audio for a model that does not declare audio input. Caller holds Mu.
func (a *Agent) validateCodexAudioInputLocked(input []map[string]interface{}, modelID string) error {
	for _, block := range input {
		if block["type"] != "audio" {
			continue
		}
		model := agent.FindAvailableModel(a.availableModels, modelID)
		if model == nil || !model.SupportsAudioInput {
			return fmt.Errorf("codex model %q does not accept audio attachments; choose a model that accepts audio input", modelID)
		}
	}
	return nil
}
