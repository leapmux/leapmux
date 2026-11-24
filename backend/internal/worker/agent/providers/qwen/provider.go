package qwen

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/internal/util/optionmap"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// qwenProvider is the wire-format plugin for Qwen Code. It is an ACP provider,
// and it adds what only Qwen knows: where its session store lives, and the
// answer to its plan approval.
//
// A question needs no rewrite: AnswerControlRequest already builds Qwen's own
// reply, with the answers beside the selected option.
type qwenProvider struct {
	acp.Provider
}

// Classify identifies the native completed compaction as a notification boundary.
func (qwenProvider) Classify(content json.RawMessage) agent.NotificationClassification {
	var update struct {
		SessionUpdate string                     `json:"sessionUpdate"`
		Meta          map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(content, &update) == nil && update.SessionUpdate == contracts.ACPUpdateAgentMessageChunk &&
		providerkit.StringField(update.Meta[contracts.QwenMetaContextCompression], "phase") == contracts.QwenCompressionPhaseDone {
		return agent.CompactionBoundaryNotification()
	}
	return agent.NotificationClassification{}
}

// ValidateAttachment refuses generic binary input that Qwen turns into a
// placeholder before the model sees its bytes.
func (qwenProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == contracts.AttachmentMediaKindAudio {
		return providerkit.ValidateAudioAttachment(attachment, "Qwen Code audio attachment", contracts.QwenAudioMIMEMembers)
	}
	if err := (agent.ProviderDefaults{}).ValidateAttachment(attachment); err != nil {
		return err
	}
	if attachment.Kind == contracts.AttachmentMediaKindBinary {
		return fmt.Errorf("qwen code cannot send generic binary attachment %q", attachment.Filename)
	}
	return nil
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
// decided, and forwards every other answer through the shared ACP resolution.
// isPlanApproval decides both this and the prompt that ReadControlRequest
// reads, so a plan's answer is always the neutral allow or deny that
// AnswerControlRequest builds for a plan.
func (p qwenProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if len(ctx.RequestPayload) > 0 && isPlanApproval(ctx.RequestPayload) {
		return resolvePlanApproval(ctx)
	}
	return p.Provider.ResolveControlResponse(ctx)
}

// ChildCapabilities.AcceptsInterrupt is true: a subagent's tab can stop its running turn.
// See InterruptChild in subagent.go.

// ChildCapabilities states the native child operations that this provider supports.
func (qwenProvider) ChildCapabilities(optionmap.Map) agent.ChildCapabilities {
	return agent.ChildCapabilities{AcceptsMessages: false, AcceptsInterrupt: true}
}
