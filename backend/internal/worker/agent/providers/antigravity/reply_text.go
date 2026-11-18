package antigravity

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ReplyText reads the reply of a planner-response step, as the browser's
// antigravityExtractRow does: the modified response when the planner states
// one, and its response otherwise.
func (antigravityProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame struct {
		Name            string `json:"name"`
		Type            string `json:"type"`
		PlannerResponse struct {
			ModifiedResponse json.RawMessage `json:"modifiedResponse"`
			Response         json.RawMessage `json:"response"`
		} `json:"plannerResponse"`
	}
	if json.Unmarshal(message.Original, &frame) != nil || frame.Name == contracts.AntigravityFrameTurnEnd ||
		frame.Name == contracts.AntigravityFrameInteraction || frame.Type != contracts.AntigravityStepTypePlannerResponse {
		return "", false
	}
	var modified, response string
	_ = json.Unmarshal(frame.PlannerResponse.ModifiedResponse, &modified)
	if modified != "" {
		return modified, true
	}
	_ = json.Unmarshal(frame.PlannerResponse.Response, &response)
	return response, true
}
