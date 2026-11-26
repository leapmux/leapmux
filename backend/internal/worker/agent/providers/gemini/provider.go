package gemini

import (
	"context"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

type geminiProvider struct{ acp.Provider }

// ValidateAttachment accepts native Gemini media, including audio resources.
func (geminiProvider) ValidateAttachment(agent.ClassifiedAttachment) error { return nil }

func (geminiProvider) ListStoredSessions(ctx context.Context, query agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return geminiStoredSessions(ctx, query)
}

func (p geminiProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if _, _, _, plan := geminiPlanPermission(ctx.RequestPayload); !plan || acp.IsJSONRPCReply(ctx.ResponseContent) {
		return p.Provider.ResolveControlResponse(ctx)
	}
	return acp.ResolvePlanPermission(ctx, acp.PlanPermissionResolver{
		AllowOption: func(agent.ControlResponseContext) string { return "proceed_once" },
		DenyOption:  "cancel",
		ValidateOption: func(ctx agent.ControlResponseContext, optionID string, result *agent.ControlResponseResolution) bool {
			for _, candidate := range acp.PermissionOptions(ctx.RequestPayload) {
				if candidate.GetOptionId() == optionID {
					return true
				}
			}
			result.Refuse(agent.RefusalUnofferedOption(leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI, optionID))
			return false
		},
	})
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
