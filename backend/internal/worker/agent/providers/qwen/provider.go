package qwen

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// qwenProvider is the wire-format plugin for Qwen Code. It is an ACP provider,
// and it adds what only Qwen knows: where its session store lives, and the
// answer to its plan approval.
//
// A question needs no rewrite: the browser already sends Qwen's own reply,
// with the answers beside the selected option.
type qwenProvider struct {
	acp.Provider
}

// ListStoredSessions reads Qwen's own session store; see sessions.go.
func (qwenProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return qwenStoredSessions(ctx, q)
}

// PlanModePermissionMode gives Qwen's own modes. An approved plan leaves plan
// mode for the default mode when the reader chose none.
func (qwenProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	switch kind {
	case agent.PlanModeControlEnter:
		return contracts.QwenModePlan
	case agent.PlanModeControlExit:
		return contracts.QwenModeDefault
	default:
		return ""
	}
}

// ResolveControlResponse answers a plan approval that the shared approval
// decided, and forwards every other answer through the shared ACP resolution
// -- a selected option of the plan approval included, which is already Qwen's
// own reply.
func (p qwenProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if len(ctx.RequestPayload) > 0 && isPlanApproval(ctx.RequestPayload) && !isJSONRPCReply(ctx.ResponseContent) {
		return resolvePlanApproval(ctx)
	}
	return p.Provider.ResolveControlResponse(ctx)
}

// isJSONRPCReply reports whether content is a JSON-RPC reply, rather than the
// shared allow and deny envelope.
func isJSONRPCReply(content []byte) bool {
	var reply map[string]json.RawMessage
	if json.Unmarshal(content, &reply) != nil {
		return false
	}
	_, hasResult := reply["result"]
	_, hasError := reply["error"]
	return hasResult || hasError
}

// SupportsChildInterrupt is true: a subagent's tab can stop its running turn.
// See InterruptChild in subagent.go.
func (qwenProvider) SupportsChildInterrupt() bool { return true }
