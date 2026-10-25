package reasonix

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// reasonixProvider is the wire-format plugin for Reasonix. Reasonix speaks ACP,
// so the embedded Provider answers the protocol questions; this type adds
// where Reasonix keeps its sessions and its text-only attachment policy.
type reasonixProvider struct {
	acp.Provider
}

// Classify identifies a stored compaction notice as a compaction boundary.
// The worker stores the notice chunk whole, so the classification reads the
// chunk's text prefix.
func (reasonixProvider) Classify(content json.RawMessage) agent.NotificationClassification {
	var chunk struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(content, &chunk) == nil && strings.HasPrefix(chunk.Content.Text, contracts.ReasonixNoticePrefixCompacted) {
		return agent.CompactionBoundaryNotification()
	}
	return agent.NotificationClassification{}
}

// PlanModePermissionMode gives Reasonix's modes. An approved plan leaves Plan
// mode for Normal, which the native runtime also selects itself.
func (reasonixProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	switch kind {
	case agent.PlanModeControlEnter:
		return contracts.ReasonixModePlan
	case agent.PlanModeControlExit:
		return contracts.ReasonixModeNormal
	default:
		return ""
	}
}

// ResolveControlResponse answers Reasonix's MCP request on its own method.
func (p reasonixProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if len(ctx.RequestPayload) > 0 && isPlanApproval(ctx.RequestPayload) && !isReasonixJSONRPCReply(ctx.ResponseContent) {
		return resolvePlanApproval(ctx)
	}
	if result, ok := providerkit.ResolveMCPElicitationResponse(ctx, contracts.ReasonixMethodMcpRequestInteraction, nil); ok {
		return result
	}
	return p.Provider.ResolveControlResponse(ctx)
}

// ListStoredSessions reads Reasonix's own session store; see sessions.go.
func (reasonixProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return reasonixStoredSessions(ctx, q)
}

// ValidateAttachment enforces Reasonix's text-only policy: it advertises
// image:false/audio:false and drops any non-text content block, so reject
// everything but text up front. A shared handoff of a PDF as a file path:
// https://github.com/leapmux/leapmux/issues/537.
func (reasonixProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind != agent.AttachmentKindText {
		return fmt.Errorf("reasonix only supports text attachments: %s", attachment.Filename)
	}
	return nil
}
