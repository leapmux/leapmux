package agent

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/util/validate"
)

// zcodeProvider is the stateless wire-format plugin for ZCode. It answers the
// questions the service layer asks without a running agent.
type zcodeProvider struct{}

// Classify groups ZCode's consolidatable notifications.
//
// A permission ZCode decided by itself and a steering queue notice both repeat, and
// a chat that shows each one separately is unreadable. The key includes the tool
// name so two different tools' denials stay distinguishable.
func (zcodeProvider) Classify(raw json.RawMessage) NotificationClassification {
	var env zcodeEventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return NotificationClassification{}
	}
	switch env.Type {
	case contracts.ZCodeEventPermissionResolved:
		var payload zcodePermissionResolved
		_ = json.Unmarshal(env.Payload, &payload)
		return NotificationClassification{
			Kind: NotificationKindProviderScoped,
			Key:  "zcode:" + contracts.ZCodeEventPermissionResolved + ":" + payload.ToolName,
		}
	case contracts.ZCodeEventTurnSteerQueued, contracts.ZCodeEventTurnSteerDrained:
		// The steering queue flaps while the user types ahead of the model, so the
		// latest state is the only interesting one.
		return NotificationClassification{Kind: NotificationKindStatus, Key: "zcode:steer"}
	default:
		return NotificationClassification{}
	}
}

func (zcodeProvider) Merge(_ NotificationClassification, _, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

// IsInterrupt recognizes ZCode's own stop frame.
//
// The frontend never sends one -- LeapMux interrupts ZCode through the
// InterruptAgent RPC, which reaches zcodeAgent.Interrupt -- but the parser is the
// counterpart of the plugin's buildInterruptContent and stays in step with it.
func (zcodeProvider) IsInterrupt(content string) bool {
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Method == ZCodeMethodSessionStop
}

// DefaultPermissionMode is ZCode's own default session mode.
func (zcodeProvider) DefaultPermissionMode() string { return contracts.ZCodeDefaultMode }

// IsSelfDisplayingControlTool is false: the app-server does not echo a control
// answer into its event stream, so the service's synthetic answer row is the only
// record of it.
func (zcodeProvider) IsSelfDisplayingControlTool(string) bool { return false }

// PlanModeControl classifies ZCode's plan approval.
//
// It is Exit, not Prompt: the app-server ASKED, and it is blocked until it is
// answered. Prompt means "LeapMux asked on its own and handles it entirely
// server-side", which would leave the app-server's request unanswered for good.
func (zcodeProvider) PlanModeControl(toolName string) PlanModeControlKind {
	switch toolName {
	case contracts.ZCodeToolNameExitPlanMode:
		return PlanModeControlExit
	case contracts.ZCodeToolNameEnterPlanMode:
		return PlanModeControlEnter
	default:
		return PlanModeControlNone
	}
}

// PlanModePermissionMode gives ZCode's own two modes. ZCode's mode axis is
// plan/build/edit/yolo, so an approved exit lands on `build` -- the mode the frontend
// plugin already declares as the plan banner's default. Claude's `acceptEdits` is not a
// value `session/setMode` accepts, and a session told that word stays where it was while
// the settings bar claims otherwise.
func (zcodeProvider) PlanModePermissionMode(kind PlanModeControlKind) string {
	switch kind {
	case PlanModeControlEnter:
		return contracts.ZCodeModePlan
	case PlanModeControlExit:
		return contracts.ZCodeModeBuild
	case PlanModeControlNone, PlanModeControlPrompt:
		return ""
	}
	return ""
}

// PlanApprovalOptions is empty: ZCode's plan approval settles no option beyond the
// permission mode, which the shared plan-mode path already applies.
func (zcodeProvider) PlanApprovalOptions() PlanApprovalOptions { return PlanApprovalOptions{} }

// SyntheticInterruptNotice is empty: ZCode is interrupted through the InterruptAgent
// RPC rather than a forwarded raw frame, and turn.failed records the outcome.
func (zcodeProvider) SyntheticInterruptNotice() string { return "" }

// PermissionModeFromRawInput is absent: ZCode's mode changes ride session/setMode,
// never a raw control frame on stdin.
func (zcodeProvider) PermissionModeFromRawInput(string) (string, bool) { return "", false }

// ValidateResumeHandle: ZCode's resume handle is an opaque session TOKEN from
// session/list, not a path, so the default token rule applies.
func (zcodeProvider) ValidateResumeHandle(handle, _ string) error {
	return validate.ValidateSessionID(handle)
}

// TurnEndToolUses reads the tool-call count off ZCode's turn end, which states it
// directly.
func (zcodeProvider) TurnEndToolUses(content []byte) (int32, bool) {
	var env zcodeEventEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		return defaultTurnEndToolUses(content)
	}
	if env.Type != contracts.ZCodeEventTurnCompleted || len(env.Payload) == 0 {
		return defaultTurnEndToolUses(content)
	}
	var payload struct {
		ToolCallCount *int32 `json:"toolCallCount"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.ToolCallCount == nil {
		return defaultTurnEndToolUses(content)
	}
	return *payload.ToolCallCount, true
}

// EndsSubagentTranscript is false: a ZCode subagent's child transcript simply stops,
// so the worker's neutral subagent-end divider closes it.
func (zcodeProvider) EndsSubagentTranscript([]byte) bool { return false }

// SupportsChildSteering is false: a ZCode subagent runs to completion and takes no
// further message.
func (zcodeProvider) SupportsChildSteering() bool { return false }
