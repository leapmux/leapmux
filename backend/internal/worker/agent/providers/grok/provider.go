package grok

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// grokProvider is the wire-format plugin for Grok Build. It is an ACP provider,
// and it adds what only Grok knows: where its session store lives, and the
// replies of Grok's own dialogs.
//
// The browser answers each dialog in the shared shape of its surface: a plan
// approval with the neutral allow and deny envelope, an MCP form with the
// neutral elicitation envelope, and folder trust with the selected option of a
// permission reply. This type rewrites each into the reply that Grok reads. A
// question and a tool permission need no rewrite: the browser already sends
// Grok's own reply.
type grokProvider struct {
	acp.Provider
}

// ListStoredSessions reads Grok's own session store; see sessions.go.
func (grokProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return grokStoredSessions(ctx, q)
}

// PlanModePermissionMode gives Grok's own two modes. An approved plan leaves
// plan mode for the default mode, which Grok then reports itself.
func (grokProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	switch kind {
	case agent.PlanModeControlEnter:
		return contracts.GrokModePlan
	case agent.PlanModeControlExit:
		return contracts.GrokModeDefault
	default:
		return ""
	}
}

// ResolveControlResponse rewrites the answer of one of Grok's own dialogs, and
// forwards every other answer through the shared ACP resolution.
func (p grokProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	var request struct {
		Method string `json:"method"`
	}
	if len(ctx.RequestPayload) == 0 || json.Unmarshal(ctx.RequestPayload, &request) != nil {
		return p.Provider.ResolveControlResponse(ctx)
	}
	switch request.Method {
	case contracts.GrokMethodExitPlanMode:
		return resolveGrokPlanApproval(ctx)
	case contracts.GrokMethodMcpElicit:
		return resolveGrokElicitation(ctx)
	case contracts.GrokMethodFolderTrust:
		return resolveGrokFolderTrust(ctx)
	default:
		return p.Provider.ResolveControlResponse(ctx)
	}
}

// grokReply wraps one result as the JSON-RPC reply to the stored request.
func grokReply(ctx agent.ControlResponseContext, result any) ([]byte, bool) {
	id, _, ok := agent.ExtractJSONRPCID(ctx.RequestPayload)
	if !ok {
		return nil, false
	}
	content, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil {
		return nil, false
	}
	return content, true
}

// withheld is the resolution of an answer that Grok could not read. The service
// refuses it and keeps the request open for another answer.
func withheld(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	result := agent.DefaultControlResponseResolution(ctx)
	result.Withhold = true
	return result
}

// decisionFor reads the neutral allow and deny envelope, and checks that it
// answers the stored request.
func decisionFor(ctx agent.ControlResponseContext) (behavior, message string, ok bool) {
	requestID, behavior, message, ok := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !ok {
		return "", "", false
	}
	_, storedID, found := agent.ExtractJSONRPCID(ctx.RequestPayload)
	if !found || requestID == "" || requestID != agent.StoredControlRequestID(ctx, storedID) {
		return "", "", false
	}
	return behavior, message, true
}

// resolveGrokPlanApproval answers a plan approval.
//
//   - Approve: `approved`. Grok leaves plan mode and implements the plan.
//   - Reject: `cancelled`, with the reason as `feedback` when the reader typed
//     one. Grok stays in plan mode and revises the plan, and its turn goes on.
//
// Grok's third answer, `abandoned`, leaves plan mode without implementing. The
// shared approval has no such button: a reader who wants it rejects the plan and
// changes the mode.
func resolveGrokPlanApproval(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	behavior, message, ok := decisionFor(ctx)
	if !ok {
		return withheld(ctx)
	}
	result := agent.DefaultControlResponseResolution(ctx)
	reply := map[string]any{}
	switch behavior {
	case agent.ControlBehaviorAllow:
		reply[contracts.GrokReplyFieldOutcome] = contracts.GrokPlanOutcomeApproved
		result.PlanModeControl = agent.PlanModeControlExit
	case agent.ControlBehaviorDeny:
		reply[contracts.GrokReplyFieldOutcome] = contracts.GrokPlanOutcomeCancelled
		if message != "" {
			reply[contracts.GrokReplyFieldFeedback] = message
		}
	default:
		return withheld(ctx)
	}
	content, ok := grokReply(ctx, reply)
	if !ok {
		return withheld(ctx)
	}
	result.Content = content
	return result
}

// resolveGrokElicitation answers an MCP form or URL. Grok reads MCP's answer
// under `outcome` rather than MCP's own `action`, so the shared MCP resolution
// builds the reply and this renames the one field.
func resolveGrokElicitation(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	result, _ := providerkit.ResolveMCPElicitationResponse(ctx, contracts.GrokMethodMcpElicit, nil)
	if result.Withhold {
		return result
	}
	var reply struct {
		JSONRPC string                     `json:"jsonrpc"`
		ID      json.RawMessage            `json:"id"`
		Result  map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(result.Content, &reply); err != nil || reply.Result == nil {
		slog.Warn("grok elicitation reply unreadable", "error", err)
		return withheld(ctx)
	}
	action, found := reply.Result["action"]
	if !found {
		return withheld(ctx)
	}
	delete(reply.Result, "action")
	reply.Result[contracts.GrokReplyFieldOutcome] = action
	content, err := json.Marshal(reply)
	if err != nil {
		return withheld(ctx)
	}
	result.Content = content
	return result
}

// resolveGrokFolderTrust answers the question whether the repository's own
// configuration may load. The browser draws it as a permission with two
// options, and sends the id of the one the reader chose. The shared Allow and
// Deny pair, which answers a request whose options the browser cannot read,
// maps onto the same two words.
//
// Grok reads any word but `trust` as a rejection. It keeps a rejection for the
// rest of the process and asks about that repository no more, and it stores
// only a grant for the workspace. So an answer this cannot read is withheld,
// never sent as reject.
func resolveGrokFolderTrust(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	outcome, ok := grokFolderTrustOutcome(ctx)
	if !ok {
		return withheld(ctx)
	}
	content, ok := grokReply(ctx, map[string]string{contracts.GrokReplyFieldOutcome: outcome})
	if !ok {
		return withheld(ctx)
	}
	result := agent.DefaultControlResponseResolution(ctx)
	result.Content = content
	return result
}

// grokFolderTrustOutcome reads the reader's choice from either envelope.
func grokFolderTrustOutcome(ctx agent.ControlResponseContext) (string, bool) {
	if behavior, _, ok := decisionFor(ctx); ok {
		switch behavior {
		case agent.ControlBehaviorAllow:
			return contracts.GrokTrustOutcomeTrust, true
		case agent.ControlBehaviorDeny:
			return contracts.GrokTrustOutcomeReject, true
		}
		return "", false
	}
	var reply struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		} `json:"result"`
	}
	if json.Unmarshal(ctx.ResponseContent, &reply) != nil || reply.Result.Outcome.Outcome != contracts.ACPPermissionOutcomeSelected {
		return "", false
	}
	_, responseID, found := agent.ExtractJSONRPCID(ctx.ResponseContent)
	_, storedID, stored := agent.ExtractJSONRPCID(ctx.RequestPayload)
	if !found || !stored || responseID != agent.StoredControlRequestID(ctx, storedID) {
		return "", false
	}
	switch reply.Result.Outcome.OptionID {
	case contracts.GrokTrustOutcomeTrust, contracts.GrokTrustOutcomeReject:
		return reply.Result.Outcome.OptionID, true
	default:
		return "", false
	}
}

// SupportsChildInterrupt is true: a subagent's tab can stop its running turn.
// See InterruptChild in subagent.go.
func (grokProvider) SupportsChildInterrupt() bool { return true }
