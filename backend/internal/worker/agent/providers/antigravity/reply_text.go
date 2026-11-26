package antigravity

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ReplyText uses the same planner text that the transcript shows.
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
	var planner antigravityPlannerResponse
	_ = json.Unmarshal(frame.PlannerResponse.ModifiedResponse, &planner.ModifiedResponse)
	_ = json.Unmarshal(frame.PlannerResponse.Response, &planner.Response)
	text, _ := planner.visibleContent()
	return text, text != ""
}
