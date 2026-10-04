package grok

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Grok Build has two permission axes, and it reports only one of them.
//
//   - The SESSION mode (default, plan, ask) is standard ACP: session/set_mode
//     writes it and current_mode_update reports it. It rides LeapMux's
//     permission-mode axis, although session/new reports no mode list, so
//     grokStaticOptionGroups states the static list that Grok's own source
//     declares.
//   - The APPROVAL mode (ask, auto, always-approve) decides whether a tool call
//     asks for permission. The client states it in the `_meta` of session/new
//     and changes it with the `_x.ai/yolo_mode_changed` notification, and Grok
//     never reports it back -- not on the wire, not in its session list. LeapMux
//     therefore keeps it itself, as an option group of its own.

// approvalModeSpec is one approval mode and how Grok states it.
type approvalModeSpec struct {
	id          string
	name        string
	description string
	// yolo and auto are the two booleans of session/new `_meta` and of the
	// notification. permissionMode is the word of the notification's
	// `permission_mode`, which Grok reads beside them.
	yolo           bool
	auto           bool
	permissionMode string
}

// grokApprovalModes is the ONE table of approval modes: the option group, the
// session/new metadata and the notification all read it, so the three cannot
// disagree about what a mode means. The first entry is the safe default.
var grokApprovalModes = []approvalModeSpec{
	{
		id: contracts.GrokApprovalModeAsk, name: "Ask",
		description:    "Ask before each tool call that needs permission",
		permissionMode: "default",
	},
	{
		id: contracts.GrokApprovalModeAuto, name: "Auto",
		description:    "A classifier model approves safe tool calls and asks for the rest",
		auto:           true,
		permissionMode: contracts.GrokApprovalModeAuto,
	},
	{
		id: contracts.GrokApprovalModeAlwaysApprove, name: "Always Approve",
		description:    "Run every tool call without asking",
		yolo:           true,
		permissionMode: contracts.GrokApprovalModeAlwaysApprove,
	},
}

// approvalModeFor returns the spec of one approval mode.
func approvalModeFor(id string) (approvalModeSpec, bool) {
	index := slices.IndexFunc(grokApprovalModes, func(spec approvalModeSpec) bool { return spec.id == id })
	if index < 0 {
		return approvalModeSpec{}, false
	}
	return grokApprovalModes[index], true
}

// grokSessionModes lists Grok's session modes, in the order Grok's own mode
// cycle takes them.
func grokSessionModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: contracts.GrokModeDefault, Name: "Default", Description: "Plan and act, asking as the approval mode states"},
		{Id: contracts.GrokModePlan, Name: "Plan", Description: "Explore and write a plan; file edits are refused except the plan file"},
		{Id: contracts.GrokModeAsk, Name: "Ask", Description: "Answer questions without changing files"},
	}
}

// approvalModeGroup builds the approval-mode option group with current as its
// value. "" builds the static template the registration serves.
func approvalModeGroup(current string) *leapmuxv1.AvailableOptionGroup {
	options := make([]*leapmuxv1.AvailableOption, len(grokApprovalModes))
	for i, spec := range grokApprovalModes {
		options[i] = &leapmuxv1.AvailableOption{Id: spec.id, Name: spec.name, Description: spec.description}
	}
	return &leapmuxv1.AvailableOptionGroup{
		Id:           contracts.GrokOptionApprovalMode,
		Label:        "Approvals",
		Options:      options,
		CurrentValue: current,
		DefaultValue: grokApprovalModes[0].id,
		Mutable:      true,
		// A provider axis sits between the effort and the permission mode.
		Order: agent.OptionOrderProviderFirst,
	}
}

// approvalState is the approval mode LeapMux keeps for the running process.
// Guarded by Agent.stateMu.
type approvalState struct {
	current string
}

// initialApprovalMode resolves the approval mode a launch asks for: the stored
// option, else the safe default. An unknown stored value falls back too, since
// a later build of Grok can drop a mode that a stored row still holds.
func initialApprovalMode(requested string) string {
	if _, ok := approvalModeFor(requested); ok {
		return requested
	}
	return grokApprovalModes[0].id
}

// sessionMeta is the `_meta` of session/new, session/load and session/resume
// that states the approval mode. Grok reads a missing key as "keep what the
// user's config.toml states", so both keys are always sent: the mode LeapMux
// shows must be the mode Grok runs.
func (spec approvalModeSpec) sessionMeta() map[string]any {
	return map[string]any{"yoloMode": spec.yolo, "autoMode": spec.auto}
}

// grokYoloModeChangedMethod is the client notification that changes the
// approval mode of every session this client opened on the process. It is the
// only live route: a `/always-approve` prompt changes nothing over ACP.
const grokYoloModeChangedMethod = "_x.ai/yolo_mode_changed"

// grokClientIdentifier identifies LeapMux to Grok, in initialize and in the
// approval-mode notification. Grok applies the notification to the sessions
// whose client carries the same identifier.
const grokClientIdentifier = "leapmux"

// approvalNotificationParams encodes the notification that switches to spec.
func approvalNotificationParams(spec approvalModeSpec) (json.RawMessage, error) {
	params, err := json.Marshal(map[string]any{
		"yolo_mode":        spec.yolo,
		"auto_mode":        spec.auto,
		"permission_mode":  spec.permissionMode,
		"clientIdentifier": grokClientIdentifier,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal the Grok approval mode: %w", err)
	}
	return params, nil
}

// localOptionGroups serves the approval-mode group of the running process.
func (a *Agent) localOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.stateMu.Lock()
	current := a.approval.current
	a.stateMu.Unlock()
	return []*leapmuxv1.AvailableOptionGroup{approvalModeGroup(current)}
}

// applyLocalOption switches the approval mode of the running process.
func (a *Agent) applyLocalOption(id, value string) (bool, error) {
	if id != contracts.GrokOptionApprovalMode {
		return false, nil
	}
	spec, ok := approvalModeFor(value)
	if !ok {
		return true, fmt.Errorf("unknown Grok approval mode: %s", value)
	}
	params, err := approvalNotificationParams(spec)
	if err != nil {
		return true, err
	}
	if err := a.SendNotification(grokYoloModeChangedMethod, params); err != nil {
		return true, fmt.Errorf("send the Grok approval mode: %w", err)
	}
	a.stateMu.Lock()
	a.approval.current = spec.id
	a.stateMu.Unlock()
	return true, nil
}

// adjustSessionParams states the approval mode in each session request, so a
// fresh session, a resumed one and the session of a context clear all run the
// mode LeapMux shows.
func (a *Agent) adjustSessionParams(_ string, params map[string]any) {
	a.stateMu.Lock()
	current := a.approval.current
	a.stateMu.Unlock()
	spec, ok := approvalModeFor(current)
	if !ok {
		spec = grokApprovalModes[0]
	}
	params["_meta"] = spec.sessionMeta()
}

// decorateModel reads the context window Grok states in a model's `_meta`.
func decorateModel(model *agent.ModelInfo, meta json.RawMessage) {
	var info struct {
		TotalContextTokens int64 `json:"totalContextTokens"`
	}
	if len(meta) == 0 || json.Unmarshal(meta, &info) != nil || info.TotalContextTokens <= 0 {
		return
	}
	model.ContextWindow = info.TotalContextTokens
}
