package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/validate"
)

// zcodeProvider is the stateless wire-format plugin for ZCode. It answers the
// questions the service layer asks without a running agent.
// zcodeProvider embeds ProviderDefaults for the interface defaults ZCode does not override,
// the way every other provider does. The methods below state a ZCode-specific decision;
// a default ZCode simply takes needs no method here.
type zcodeProvider struct {
	agent.ProviderDefaults
}

// Classify groups ZCode's consolidatable notifications.
//
// A permission ZCode decided by itself and a steering queue notice both repeat, and
// a chat that shows each one separately is unreadable. The key includes the tool
// name so two different tools' denials stay distinguishable.
func (zcodeProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var env zcodeEventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return agent.NotificationClassification{}
	}
	switch env.Type {
	case contracts.ZCodeEventPermissionResolved:
		var payload zcodePermissionResolved
		_ = json.Unmarshal(env.Payload, &payload)
		return agent.NotificationClassification{
			Kind: agent.NotificationKindProviderScoped,
			Key:  "zcode:" + contracts.ZCodeEventPermissionResolved + ":" + payload.ToolName,
		}
	case contracts.ZCodeEventTurnSteerQueued, contracts.ZCodeEventTurnSteerDrained:
		// The steering queue flaps while the user types ahead of the model, so the
		// latest state is the only interesting one.
		return agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "zcode:steer"}
	default:
		return agent.NotificationClassification{}
	}
}

func (zcodeProvider) Merge(_ agent.NotificationClassification, _, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

// IsInterrupt recognizes ZCode's own stop frame.
//
// The frontend uses the InterruptAgent RPC. This parser supports raw provider
// input from other callers.
func (zcodeProvider) IsInterrupt(content string) bool {
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Method == MethodSessionStop
}

// IsSelfDisplayingControlTool is false: the app-server does not echo a control
// answer into its event stream, so the service's synthetic answer row is the only
// record of it.
func (zcodeProvider) IsSelfDisplayingControlTool(string) bool { return false }

// PlanModeControl classifies ZCode's plan approval.
//
// It is Exit, not Prompt: the app-server ASKED, and it is blocked until it is
// answered. Prompt means "LeapMux asked on its own and handles it entirely
// server-side", which would leave the app-server's request unanswered for good.
func (zcodeProvider) PlanModeControl(toolName string) agent.PlanModeControlKind {
	switch toolName {
	case contracts.ZCodeToolNameExitPlanMode:
		return agent.PlanModeControlExit
	case contracts.ZCodeToolNameEnterPlanMode:
		return agent.PlanModeControlEnter
	default:
		return agent.PlanModeControlNone
	}
}

// PlanModePermissionMode gives ZCode's own two modes. ZCode's mode axis is
// plan/build/edit/yolo, so an approved exit lands on `build` -- the mode the frontend
// plugin already declares as the plan banner's default. Claude's `acceptEdits` is not a
// value `session/setMode` accepts, and a session told that word stays where it was while
// the settings bar claims otherwise.
func (zcodeProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	switch kind {
	case agent.PlanModeControlEnter:
		return contracts.ZCodeModePlan
	case agent.PlanModeControlExit:
		return contracts.ZCodeModeBuild
	case agent.PlanModeControlNone, agent.PlanModeControlPrompt:
		return ""
	}
	return ""
}

// PlanApprovalOptions is empty: ZCode's plan approval settles no option beyond the
// permission mode, which the shared plan-mode path already applies.
func (zcodeProvider) PlanApprovalOptions(string) map[string]string { return nil }

// SyntheticInterruptNotice is empty: ZCode is interrupted through the InterruptAgent
// RPC rather than a forwarded raw frame, and turn.failed records the outcome.
func (zcodeProvider) SyntheticInterruptNotice() string { return "" }

// PermissionModeFromRawInput is absent: ZCode's mode changes ride session/setMode,
// never a raw control frame on stdin.
func (zcodeProvider) PermissionModeFromRawInput(string) (string, bool) { return "", false }

// ResolveResumeHandle: ZCode's resume handle is an opaque session TOKEN from
// session/list, not a path, so the default token rule applies. That rule
// refuses rather than normalizes, so an accepted handle reaches argv unchanged.
func (zcodeProvider) ResolveResumeHandle(handle, _ string) (string, error) {
	if err := validate.ValidateSessionID(handle); err != nil {
		return "", err
	}
	return handle, nil
}

// ListStoredSessions reads ZCode's own CLI session database. See
// sessions.go for the path, and opencode/sessions.go for the query --
// ZCode's `session` table is OpenCode's.
func (zcodeProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return zcodeStoredSessions(ctx, q)
}

// TurnEndToolUses reads the tool-call count off ZCode's turn end, which states it
// directly.
func (zcodeProvider) TurnEndToolUses(content []byte) (int32, bool) {
	var env zcodeEventEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		return agent.DefaultTurnEndToolUses(content)
	}
	if env.Type != contracts.ZCodeEventTurnCompleted || len(env.Payload) == 0 {
		return agent.DefaultTurnEndToolUses(content)
	}
	var payload struct {
		ToolCallCount *int32 `json:"toolCallCount"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.ToolCallCount == nil {
		return agent.DefaultTurnEndToolUses(content)
	}
	return *payload.ToolCallCount, true
}

// EndsSubagentTranscript is false: a ZCode subagent's child transcript simply stops,
// so the worker's neutral subagent-end divider closes it.
func (zcodeProvider) EndsSubagentTranscript([]byte) bool { return false }

// SupportsChildSteering is false: a ZCode subagent runs to completion and takes no
// further message.
func (zcodeProvider) SupportsChildSteering() bool { return false }

// ReportsDefaultModelSentinel is false: ZCode's catalog names every model
// explicitly, so no entry stands for the account default.
func (zcodeProvider) ReportsDefaultModelSentinel() bool { return false }

// ValidateAttachment enforces the part of ZCode's attachment policy that does not
// depend on the running model.
//
// A PDF is refused outright. The app-server's attachment normalizer recognizes
// image, video, file and audio and NOTHING else, so a PDF arrives as a generic
// file: a small one is decoded as text and reaches the model as binary garbage, and
// a large one is dropped with no message at all. Both are worse than a refusal that
// says so.
//
// The image gate is NOT here, because this check is stateless and an image's
// acceptance depends on the CURRENT model's declared input modalities. It runs in
// zcodeAgent.SendInput, which is the only place that knows the model -- see
// attachments.go.
func (zcodeProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	switch attachment.Kind {
	case agent.AttachmentKindPDF:
		return fmt.Errorf("zcode does not support PDF attachments: %s", attachment.Filename)
	case agent.AttachmentKindBinary:
		return fmt.Errorf("zcode does not support binary attachments: %s", attachment.Filename)
	default:
		return nil
	}
}

// ResolveControlResponse turns the frontend's neutral answer into the app-server's
// reply frame.
//
// This is where the two protocols meet. The frontend speaks allow/deny (plus the
// AskUserQuestion answers under `updatedInput.answers`); the app-server wants a
// permission decision or an accept/decline action, addressed by the WIRE id of its
// own request. The transformed bytes are what the service forwards to stdin, so the
// reply is a complete ZCode frame and not the envelope the frontend sent.
func (zcodeProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	res := agent.DefaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		// The pending request is gone (a teardown, or a duplicate answer that read it
		// after the winner deleted it). There is nothing to address the reply to, and
		// forwarding the frontend envelope would put a frame the app-server cannot
		// parse on its stdin.
		res.Withhold = true
		return res
	}

	var stored zcodeControlRequestPayload
	if !providerkit.WarnUnmarshal(ctx.RequestPayload, &stored, "zcode control response request") {
		res.Withhold = true
		return res
	}
	res.PlanModeControl = zcodeProvider{}.PlanModeControl(stored.Request.ToolName)

	requestID, behavior, message, ok := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !ok || (behavior != agent.ControlBehaviorAllow && behavior != agent.ControlBehaviorDeny) {
		// Not a recognizable allow/deny. Withholding the forward is the safe answer:
		// the app-server keeps waiting (and the user can answer again) rather than
		// receiving a frame that means nothing.
		res.Withhold = true
		return res
	}
	if requestID != "" && stored.RequestID != "" && requestID != stored.RequestID {
		slog.Warn("zcode control response addressed another request",
			"answered", requestID, "stored", stored.RequestID)
		res.Withhold = true
		return res
	}
	if len(stored.WireID) == 0 {
		slog.Warn("zcode stored control request carried no wire id", "request_id", stored.RequestID)
		res.Withhold = true
		return res
	}

	reply, err := zcodeReplyForAnswer(stored, behavior, message, ctx.ResponseContent)
	if err != nil {
		slog.Warn("zcode build control reply failed", "request_id", stored.RequestID, "error", err)
		res.Withhold = true
		return res
	}
	encoded, err := json.Marshal(zcodeReplyFrame{ID: stored.WireID, Result: reply})
	if err != nil {
		slog.Warn("zcode marshal control reply failed", "request_id", stored.RequestID, "error", err)
		res.Withhold = true
		return res
	}
	res.Content = encoded
	if stored.Method == contracts.ZCodeMethodRequestUserInput && behavior == agent.ControlBehaviorDeny && message != "" {
		// The RAW message, not the trimmed one, and the second decode is what reaches it.
		//
		// The app-server cannot carry this text, so the service queues it as the next USER
		// INPUT. That makes it the reader's own typed message, and LeapMux delivers a typed
		// message byte for byte. `message` above is the TRIMMED value, and it decides only
		// whether a reason exists at all -- it excludes the ControlRejectedByUserMessage
		// sentinel and a whitespace-only reason, which is what this guard needs it for.
		var original agent.ControlBehaviorEnvelope
		if json.Unmarshal(ctx.ResponseContent, &original) == nil {
			res.Feedback = original.Response.Response.Message
		}
	}
	return res
}
