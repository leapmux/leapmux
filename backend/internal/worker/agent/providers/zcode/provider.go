package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/util/optionmap"
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
	storageQuery zcodeStoragePathQuery
}

// Classify groups notifications that retain only their latest state.
// Permission notices use one key per tool. Steer notices use a separate provider key.
// Compaction ends clear transient status. A compaction start is live state and has no stored row.
func (zcodeProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var env zcodeEventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return agent.NotificationClassification{}
	}
	switch env.Type {
	case contracts.ZCodeEventPermissionResolved:
		var payload zcodePermissionResolved
		_ = json.Unmarshal(env.Payload, &payload)
		return agent.ProviderScopedNotification("zcode:" + contracts.ZCodeEventPermissionResolved + ":" + payload.ToolName)
	case contracts.ZCodeEventTurnSteerQueued, contracts.ZCodeEventTurnSteerDrained:
		// The latest steer notice remains separate from the compaction state.
		return agent.ProviderScopedNotification("zcode:steer")
	case contracts.ZCodeEventSessionUpdated:
		var payload zcodeSessionUpdatedPayload
		if json.Unmarshal(env.Payload, &payload) != nil {
			return agent.NotificationClassification{}
		}
		switch payload.kind() {
		case zcodeSessionUpdateCompaction:
			status, _ := providerkit.RawString(payload.Status)
			switch status {
			case contracts.ZCodeCompactionStatusCompleted, contracts.ZCodeCompactionStatusSkipped,
				contracts.ZCodeCompactionStatusFailed, contracts.ZCodeCompactionStatusInterrupted:
				return agent.CompactionBoundaryNotification()
			}
		case zcodeSessionUpdateNetworkStatus:
			// A burst of retries repeats one event. The notice keeps the latest state.
			status, _ := providerkit.RawString(payload.Type)
			if status == contracts.ZCodeNetworkStatusRetryScheduled && payload.networkStatusHasAPIRetry() {
				return agent.APIRetryNotification()
			}
		case zcodeSessionUpdateSteerDiscarded:
			// The discard replaces the queued notice the same key already holds.
			return agent.ProviderScopedNotification("zcode:steer")
		default:
			// Every other `session.updated` shape is classified per row. A new
			// kind therefore lands here and gets no folding key, which is the
			// right outcome until someone gives it one.
		}
		return agent.NotificationClassification{}
	default:
		return agent.NotificationClassification{}
	}
}

// IsInterrupt recognizes ZCode's own stop frames: the legacy session/stop and
// the guarded v4 `stop` command Agent.Interrupt emits.
//
// The frontend uses the InterruptAgent RPC. This parser supports raw provider
// input from other callers.
func (zcodeProvider) IsInterrupt(content string) bool {
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	if msg.Method == MethodSessionStop {
		return true
	}
	if msg.Method != MethodCommand {
		return false
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(msg.Params, &envelope) != nil {
		return false
	}
	return envelope.Type == zcodeCommandStop
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

// PlanModePermissionMode gives ZCode's own two mode words. The axis of LeapMux for
// ZCode is plan/build/edit/yolo, where `plan` is the plan flag of ZCode. An approved
// ExitPlanMode turns the flag off, and the session keeps the native mode that it had
// under plan mode. LeapMux then applies the mode that the plan banner chose. The
// banner default is `build`: the frontend plugin declares it, and this method
// returns it. An approval with no choice therefore ends in `build`, also for a
// session that entered plan mode from `edit`. The exit event reports `edit` first
// (handleZCodeModeChanged), and the deferred mode replaces it (DeferPlanExitMode).
// Claude's `acceptEdits` is not a value that `session/setMode` accepts. A session
// that receives that word stays where it was, while the settings bar claims
// otherwise.
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
func (p zcodeProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return zcodeStoredSessions(ctx, q, p.storageQuery)
}

// TurnEndToolUses reads the tool-call count of a turn end. The Worker's own count,
// which persistZCodeTurnDivider writes onto every divider, comes first: a stopped
// turn's turn.completed states toolCallCount 0 for the call that the stop cut. ZCode's own
// toolCallCount answers for a turn end that carries no Worker count.
func (zcodeProvider) TurnEndToolUses(content []byte) (int32, bool) {
	if count, ok := agent.DefaultTurnEndToolUses(content); ok {
		return count, true
	}
	var env zcodeEventEnvelope
	if err := json.Unmarshal(content, &env); err != nil || env.Type != contracts.ZCodeEventTurnCompleted || len(env.Payload) == 0 {
		return 0, false
	}
	var payload struct {
		ToolCallCount *int32 `json:"toolCallCount"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.ToolCallCount == nil {
		return 0, false
	}
	return *payload.ToolCallCount, true
}

// ChildCapabilities states the native child operations that this provider supports.
//
// AcceptsMessages is false: a ZCode subagent runs to completion and takes no further
// message. AcceptsInterrupt is true: a background subagent's tab stops it through its
// task. A foreground subagent runs inside its parent's turn and refuses the
// interrupt; see InterruptChild in subagent.go.
func (zcodeProvider) ChildCapabilities(optionmap.Map) agent.ChildCapabilities {
	return agent.ChildCapabilities{AcceptsMessages: false, AcceptsInterrupt: true}
}

// ReportsDefaultModelSentinel is false: ZCode's catalog lists every model
// explicitly, so no entry stands for the account default.
func (zcodeProvider) ReportsDefaultModelSentinel() bool { return false }

// ValidateAttachment enforces the part of ZCode's attachment policy that does not
// depend on the running model: a binary file has no kind that keeps its bytes.
//
// The image and PDF gates are NOT here, because this check is stateless and their
// acceptance depends on the CURRENT model's declared input modalities. They run in
// Agent.SendInput, which is the only place that knows the model -- see
// attachments.go.
func (zcodeProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if err := (agent.ProviderDefaults{}).ValidateAttachment(attachment); err != nil {
		return err
	}
	if attachment.Kind == contracts.AttachmentMediaKindBinary {
		return fmt.Errorf("zcode does not support binary attachments: %s", attachment.Filename)
	}
	return nil
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
		res.Refuse(agent.RefusalUnreadableRequest)
		return res
	}
	// LeapMux asks the hook-trust question itself (workspace_trust.go).
	if result, ok := providerkit.ResolveWorkspaceTrustResponse(ctx); ok {
		return result
	}

	var stored zcodeControlRequestPayload
	if !providerkit.WarnUnmarshal(ctx.RequestPayload, &stored, "zcode control response request") {
		res.Refuse(agent.RefusalUnreadableRequest)
		return res
	}
	res.PlanModeControl = zcodeProvider{}.PlanModeControl(stored.Request.ToolName)
	if stored.Method == contracts.ZCodeMethodRequestUserInput && stored.Request.ToolName == zcodeControlMCPElicitation {
		// An MCP server's form: the browser answers it as an MCP form, not with
		// allow or deny.
		return zcodeResolveMCPElicitation(stored, ctx.ResponseContent, res)
	}

	requestID, behavior, message, ok := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !ok || (behavior != agent.ControlBehaviorAllow && behavior != agent.ControlBehaviorDeny) {
		// Not a recognizable allow/deny. Withholding the forward is the safe answer:
		// the app-server keeps waiting (and the user can answer again) rather than
		// receiving a frame that means nothing.
		res.Refuse(agent.RefusalNoDecision)
		return res
	}
	if requestID != "" && stored.RequestID != "" && requestID != stored.RequestID {
		slog.Warn("zcode control response addressed another request",
			"answered", requestID, "stored", stored.RequestID)
		res.Refuse(agent.RefusalOtherRequest)
		return res
	}
	if len(stored.WireID) == 0 {
		slog.Warn("zcode stored control request carried no wire id", "request_id", stored.RequestID)
		res.Refuse(agent.RefusalUnreadableRequest)
		return res
	}

	reply, err := zcodeReplyForAnswer(stored, behavior, message, ctx.ResponseContent)
	if err != nil {
		slog.Warn("zcode build control reply failed", "request_id", stored.RequestID, "error", err)
		res.Refuse(agent.RefusalUnreadableRequest)
		return res
	}
	encoded, err := json.Marshal(zcodeReplyFrame{ID: stored.WireID, Result: reply})
	if err != nil {
		slog.Warn("zcode marshal control reply failed", "request_id", stored.RequestID, "error", err)
		res.Refuse(agent.RefusalUnencodableReply)
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
			res.FollowUp = agent.FeedbackFollowUp(original.Response.Response.Message)
		}
	}
	return res
}
