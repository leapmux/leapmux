package kiro

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// Kiro has two permission axes, and it reports only one of them.
//
//   - The MODE (vibe, spec, plan, ...) is the `mode` config option and the
//     standard ACP mode list. It rides LeapMux's permission-mode axis, which
//     LeapMux labels Mode, as Kiro labels it. Plan mode is one of its values.
//   - The POLICY PRESET decides which tool calls run without a permission
//     request. The client states it in the `_meta.kiro` of session/new and
//     session/load, Kiro keeps it for the life of the session, and nothing
//     reports it back or changes it later. LeapMux therefore keeps it itself,
//     as an option group of its own, and a change of it opens the session
//     again under the new preset.
//
// Kiro's `autopilot` config option (Autopilot or Supervised) is a third,
// narrower axis: Supervised holds the file edits of a turn for a review at the
// end of the turn. The base surfaces it as an option group, as it surfaces
// every config option that no channel claims.

// Kiro's config options that the base surfaces as option groups of their own.
// The effort axis is in the contract, because the browser reads it too.
const (
	// kiroConfigThinking turns the model's thinking on and off, for a model
	// that can toggle it.
	kiroConfigThinking = "thinking"
	// kiroConfigAutopilot selects Autopilot or Supervised.
	kiroConfigAutopilot = "autopilot"
	// kiroConfigContentCollection states whether Kiro may use the content of
	// the process for service improvement. Kiro keeps it for the whole
	// process, and each LeapMux agent runs a process of its own.
	kiroConfigContentCollection = "contentCollection"
)

// kiroModes lists the modes of Kiro's own build, in the order its session
// reports them. The session replaces this list as soon as it reports its own,
// which adds the custom agents of the user and the workspace.
func kiroModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: contracts.KiroModeDefault, Name: "Default", Description: "General coding assistance"},
		{Id: "spec", Name: "Spec", Description: "Structured feature development"},
		{Id: "quick-spec", Name: "Quick Spec", Description: "Clarify, then generate requirements, design, and tasks"},
		{Id: "bug-fix", Name: "Bug Fix", Description: "Investigate, diagnose, and resolve bugs"},
		{Id: contracts.KiroModePlan, Name: "Plan", Description: "Break an idea down into an implementation plan without making any changes"},
		{Id: "autonomous", Name: "Autonomous", Description: "Autonomous agent execution"},
	}
}

// kiroPolicyAsk is the policy option value that states no preset: Kiro's own
// rules then decide, and every call they do not allow asks. LeapMux owns the
// word, because Kiro has no preset that means "none".
const kiroPolicyAsk = "ask"

// policyPresetSpec is one value of the policy option and the Kiro presets it
// states.
type policyPresetSpec struct {
	id          string
	name        string
	description string
	// presets are the Kiro preset ids of `_meta.kiro.policyPreset`. Empty for
	// the value that states no preset.
	presets []string
}

// kiroPolicyPresets is the ONE table of policy values: the option group, the
// session metadata and the resume of a goal all read it, so the three cannot
// disagree about what a value means. The first entry is the safe default.
//
// Two of Kiro's six presets are absent, because each adds nothing to Kiro's
// own default rules: `read-workspace` allows the reads in the workspace, and
// `read-only-shell` allows the read-only commands, which both run without a
// request already.
var kiroPolicyPresets = []policyPresetSpec{
	{
		id: kiroPolicyAsk, name: "Ask",
		description: "Kiro's own rules: reads in the workspace and read-only commands run, and every other call asks",
	},
	{
		id: "edit-workspace", name: "Edit workspace",
		description: "Also read and write files in the workspace without asking",
		presets:     []string{"edit-workspace"},
	},
	{
		id: "dev-shell", name: "Development shell",
		description: "Also run common development commands (git, builds, tests, package managers) without asking",
		presets:     []string{"dev-shell"},
	},
	{
		id: "read-all", name: "Read anywhere",
		description: "Also read files outside the workspace and fetch web pages without asking",
		presets:     []string{"read-all"},
	},
	{
		id: contracts.KiroPolicyPresetAllowAll, name: "Allow all",
		description: "Run every tool call without asking, including file access outside the workspace",
		presets:     []string{contracts.KiroPolicyPresetAllowAll},
	},
}

// policyPresetFor returns the spec of one policy value.
func policyPresetFor(id string) (policyPresetSpec, bool) {
	index := slices.IndexFunc(kiroPolicyPresets, func(spec policyPresetSpec) bool { return spec.id == id })
	if index < 0 {
		return policyPresetSpec{}, false
	}
	return kiroPolicyPresets[index], true
}

// initialPolicyPreset resolves the policy value a launch asks for: the stored
// option, else the safe default. An unknown stored value falls back too,
// because a later build of LeapMux can drop a value that a stored row still
// states.
func initialPolicyPreset(requested string) string {
	if _, ok := policyPresetFor(requested); ok {
		return requested
	}
	return kiroPolicyPresets[0].id
}

// policyPresetGroup builds the policy option group with current as its value.
// "" builds the static template that the registration serves.
func policyPresetGroup(current string) *leapmuxv1.AvailableOptionGroup {
	options := make([]*leapmuxv1.AvailableOption, len(kiroPolicyPresets))
	for i, spec := range kiroPolicyPresets {
		options[i] = &leapmuxv1.AvailableOption{Id: spec.id, Name: spec.name, Description: spec.description}
	}
	return &leapmuxv1.AvailableOptionGroup{
		Id:           contracts.KiroOptionPolicyPreset,
		Label:        "Permissions",
		Options:      options,
		CurrentValue: current,
		DefaultValue: kiroPolicyPresets[0].id,
		Mutable:      true,
		// A provider axis sits between the effort and the permission mode.
		Order: agent.OptionOrderProviderFirst,
	}
}

// policyState is the policy value that the running session opened with.
// Guarded by Agent.stateMu.
type policyState struct {
	current string
}

// currentPolicyPreset returns the spec of the policy the session runs.
func (a *Agent) currentPolicyPreset() policyPresetSpec {
	a.stateMu.Lock()
	current := a.policy.current
	a.stateMu.Unlock()
	spec, ok := policyPresetFor(current)
	if !ok {
		return kiroPolicyPresets[0]
	}
	return spec
}

// localOptionGroups serves the policy group of the running session.
func (a *Agent) localOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return []*leapmuxv1.AvailableOptionGroup{policyPresetGroup(a.currentPolicyPreset().id)}
}

// errPolicyAppliesAtSessionOpen is the answer to a live change of the policy.
// Kiro reads the preset only when a session opens, so the change needs the
// session opened again, which a restart does.
var errPolicyAppliesAtSessionOpen = fmt.Errorf("the Kiro policy preset applies when the session opens")

// applyLocalOption refuses a live change of the policy, which only a new
// session applies. UpdateSettings asks for that restart before the base
// reaches this. The refusal keeps the rule when some other path does.
func (a *Agent) applyLocalOption(id, _ string) (bool, error) {
	if id != contracts.KiroOptionPolicyPreset {
		return false, nil
	}
	return true, errPolicyAppliesAtSessionOpen
}

// UpdateSettings applies a change of the settings. A new policy preset needs
// the session opened again, so it asks for a restart, which opens the session
// with session/load under the new preset. Every other axis applies live.
//
// A policy value that this build does not know is dropped from the change:
// the session keeps its preset, and the snapshot reports that preset back.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	if requested, present := options[contracts.KiroOptionPolicyPreset]; present && requested != "" {
		current := a.currentPolicyPreset().id
		if _, known := policyPresetFor(requested); !known {
			slog.Warn("kiro policy preset unknown; keeping the current one", "agent_id", a.AgentID(), "requested", requested, "current", current)
			options = options.Clone()
			options[contracts.KiroOptionPolicyPreset] = current
		} else if requested != current {
			return agent.RestartRequiredSettings(options)
		}
	}
	return a.Base.UpdateSettings(options)
}

// adjustSessionParams states LeapMux's session metadata in each session
// request: the policy preset of the session, and on a load, that Kiro replays
// nothing. LeapMux keeps its own transcript, and Kiro replays every update of
// the history, which would draw the whole conversation a second time.
func (a *Agent) adjustSessionParams(method string, params map[string]any) {
	meta := map[string]any{}
	if presets := a.currentPolicyPreset().presets; len(presets) > 0 {
		meta["policyPreset"] = presets
	}
	if method == acp.MethodSessionLoad {
		meta["noReplay"] = true
	}
	if len(meta) == 0 {
		return
	}
	params["_meta"] = map[string]any{contracts.KiroMetaNamespace: meta}
}

// kiroClientCapabilities is the `_meta` of the initialize request's
// clientCapabilities, which switches on the Kiro extensions that LeapMux
// answers:
//
//   - `userInput`: questions arrive as `_kiro/userInput`. Without it, Kiro
//     turns a question with options into a permission request and drops a
//     free-form question without a word.
//   - `hooks`: Kiro turns every hook off unless the client enables hooks, and
//     `v2` makes it load the hooks of the workspace itself rather than ask the
//     client for them.
//   - `streamingShellContent`: the output of a running command streams as
//     `_kiro/tools/content_chunk`.
//   - `settings`: the goal command, workflows and the to-do list tool.
func kiroClientCapabilities() map[string]any {
	enabled := map[string]any{"enabled": true}
	return map[string]any{
		contracts.KiroMetaNamespace: map[string]any{
			"userInput":             true,
			"hooks":                 map[string]any{"enabled": true, "v2": true},
			"streamingShellContent": true,
			"settings": map[string]any{
				"goal":      enabled,
				"workflows": enabled,
				"todoList":  enabled,
			},
		},
	}
}

// kiroModelMeta is the part of a model option's `_meta.kiro` that LeapMux
// reads: the credit rate of the model.
type kiroModelMeta struct {
	RateMultiplier *float64 `json:"rateMultiplier"`
	RateUnit       string   `json:"rateUnit"`
}

// decorateModel states the credit rate of a model in its description, because
// Kiro bills a turn in credits and each model spends them at its own rate.
func decorateModel(model *agent.ModelInfo, meta json.RawMessage) {
	if len(meta) == 0 {
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(meta, &envelope) != nil {
		return
	}
	var info kiroModelMeta
	if json.Unmarshal(envelope[contracts.KiroMetaNamespace], &info) != nil || info.RateMultiplier == nil || *info.RateMultiplier <= 0 {
		return
	}
	unit := "credit"
	if info.RateUnit != "" {
		unit = lowerFirst(info.RateUnit)
	}
	rate := strconv.FormatFloat(*info.RateMultiplier, 'f', -1, 64) + "x " + unit + " rate"
	if model.Description == "" {
		model.Description = rate
		return
	}
	model.Description += " (" + rate + ")"
}

// lowerFirst lowers the first letter of a word that Kiro capitalizes.
func lowerFirst(word string) string {
	if word == "" || word[0] < 'A' || word[0] > 'Z' {
		return word
	}
	return string(word[0]+('a'-'A')) + word[1:]
}
