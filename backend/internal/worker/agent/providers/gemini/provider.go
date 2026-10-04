package gemini

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

type geminiProvider struct{ acp.Provider }

func (geminiProvider) ListStoredSessions(ctx context.Context, query agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return geminiStoredSessions(ctx, query)
}

func (p geminiProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if _, _, _, plan := geminiPlanPermission(ctx.RequestPayload); !plan || geminiJSONRPCReply(ctx.ResponseContent) {
		return p.Provider.ResolveControlResponse(ctx)
	}
	result := agent.DefaultControlResponseResolution(ctx)
	requestID, behavior, feedback, ok := agent.DecodeControlBehavior(ctx.ResponseContent)
	id, storedID, found := agent.ExtractJSONRPCID(ctx.RequestPayload)
	if !ok || !found || requestID == "" || requestID != agent.StoredControlRequestID(ctx, storedID) {
		result.Withhold = true
		return result
	}
	option := ""
	switch behavior {
	case agent.ControlBehaviorAllow:
		option = "proceed_once"
		result.PlanModeControl = agent.PlanModeControlExit
	case agent.ControlBehaviorDeny:
		option = "cancel"
		result.Feedback = feedback
	default:
		result.Withhold = true
		return result
	}
	var request struct {
		Params struct {
			Options []struct {
				OptionID string `json:"optionId"`
			} `json:"options"`
		} `json:"params"`
	}
	if json.Unmarshal(ctx.RequestPayload, &request) != nil {
		result.Withhold = true
		return result
	}
	offered := false
	for _, candidate := range request.Params.Options {
		if candidate.OptionID == option {
			offered = true
			break
		}
	}
	if !offered {
		result.Withhold = true
		return result
	}
	content, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: map[string]any{"outcome": map[string]string{"outcome": contracts.ACPPermissionOutcomeSelected, "optionId": option}}})
	if err != nil {
		result.Withhold = true
		return result
	}
	result.Content = content
	return result
}

func geminiJSONRPCReply(content []byte) bool {
	var reply map[string]json.RawMessage
	if json.Unmarshal(content, &reply) != nil {
		return false
	}
	_, result := reply["result"]
	_, failure := reply["error"]
	return result || failure
}

func (geminiProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	switch kind {
	case agent.PlanModeControlEnter:
		return contracts.GeminiModePlan
	case agent.PlanModeControlExit:
		return contracts.GeminiModeDefault
	default:
		return ""
	}
}
