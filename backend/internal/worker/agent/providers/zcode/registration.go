package zcode

import (
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// zcodeLocator finds ZCode through its own resolver. ZCode ships no executable
// of its own, so the "probe a bare name in the login shell" model cannot find
// it -- see resolve.go.
var zcodeLocator = launch.Custom(resolveZCodeLaunch)

// Registration states everything the worker knows about ZCode before any of
// its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider:      leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		Plugin:        zcodeProvider{},
		Start:         Start,
		Locator:       zcodeLocator,
		DefaultModels: zcodeFallbackModels,
		OptionGroups:  zcodeStaticOptionGroups,
		// The model-dependent axis is ZCode's thought level, which is not the generic
		// "Effort" label, so the static fallback and the model-switch sub_groups match
		// what the live OptionGroups reports.
		ModelSubGroups:      agent.EffortSubGroupsLabeled(ThoughtLevelLabel),
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		// A composite model id is spelled with `/` by the app-server itself; the
		// normalizer accepts a backslash spelling so a re-spelling is not read as a
		// model switch.
		NormalizeModelID: normalizeZCodeModelID,
		// ZCode ships no safe-mode preset; Build is the mode a session with none takes.
		PermissionDefaults: agent.PermissionDefaults{
			Fallback: contracts.ZCodeDefaultMode,
		},
		EnvModelKey:  "LEAPMUX_ZCODE_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_ZCODE_DEFAULT_EFFORT",
	}
}

// zcodeAutoEffort is LeapMux's sentinel for "send no thought level at all", which
// leaves the app-server on whatever default it resolved for the model.
var zcodeAutoEffort = &agent.EffortInfo{
	Id:          agent.EffortAuto,
	Name:        providerkit.EffortLabel(agent.EffortAuto),
	Description: "Use ZCode's default thought level for the model",
}

// zcodeEffortsWithAuto orders a model's thought levels for the menu: Auto first,
// then the rest strongest first. It sorts `levels` in place and returns the new
// slice.
//
// One function for the two places that build such a list -- the configured
// catalog (zcodeModelInfo) and the live snapshot that REPLACES it for the
// running model (applySettingsSnapshotLocked). Built separately, the two ordered
// the same levels differently and the menu reordered itself under the reader the
// moment the first snapshot landed.
//
// It also discharges providerkit.SortEffortsDescending's one caller obligation. Auto is not
// a strength -- it means "send no level at all" -- so it must not reach the
// sort, and putting it back afterwards is the step a caller can forget.
func zcodeEffortsWithAuto(levels []*agent.EffortInfo) []*agent.EffortInfo {
	providerkit.SortEffortsDescending(levels)
	// A fresh slice with room for both, rather than a prepend onto the caller's:
	// `append` to a one-element literal reallocates anyway, and a caller sized
	// for the levels alone.
	out := make([]*agent.EffortInfo, 0, len(levels)+1)
	out = append(out, zcodeAutoEffort)
	return append(out, levels...)
}

// zcodeEffortTier builds the display entry for one ZCode thought level.
//
// ZCode spells a level as a bare id ("low", "max", "enabled") and repeats that id as
// its label, so the shared effortLabels table -- not the wire label -- decides how a
// level READS. Without this the SAME level renders two ways in one popover: the
// configured catalog capitalizes it and the live snapshot does not.
//
// A label that DIFFERS from the id carries something the shared table cannot know,
// so it wins.
func zcodeEffortTier(value, label, description string) *agent.EffortInfo {
	name := providerkit.EffortLabel(value)
	if label != "" && !strings.EqualFold(label, value) {
		name = label
	}
	return &agent.EffortInfo{Id: value, Name: name, Description: description}
}

// zcodeFallbackModels is the static seed that the settings popover shows before
// an agent runs.
//
// It is deliberately empty. ZCode owns the provider ids, model ids, and thought
// levels. A hardcoded entry can identify a model that the installation does not
// have. An empty catalog renders no model group until the agent reports one.
var zcodeFallbackModels []*agent.ModelInfo

// zcodeStaticOptionGroups holds the option groups that do NOT depend on a
// running agent: the mode axis, whose four values are fixed by the app-server.
//
// It is the one home of that template. The registration in agent.go and
// Agent.OptionGroups both read this value, so the list the static fallback
// offers and the list a running agent offers cannot drift apart.
//
// `auto` is absent on purpose. It is in the app-server's own enumeration and is
// not implemented in the shipped build: every tool call under it is denied with
// `permission.resolved {reason:"Auto mode is reserved but not implemented yet"}`,
// so offering it would give the user a mode in which nothing works.
var zcodeStaticOptionGroups = []*leapmuxv1.AvailableOptionGroup{
	{
		Id:           agent.OptionIDPermissionMode,
		Label:        ModeLabel,
		DefaultValue: contracts.ZCodeDefaultMode,
		Mutable:      true,
		Order:        agent.OptionOrderPermissionMode,
		Options: []*leapmuxv1.AvailableOption{
			{Id: contracts.ZCodeModePlan, Name: "Plan", Description: "Research and plan; no edits and no commands"},
			{Id: contracts.ZCodeModeBuild, Name: "Build", Description: "Edit files and run commands, asking before a risky action"},
			{Id: contracts.ZCodeModeEdit, Name: "Edit", Description: "Edit files freely; ask before running a command"},
			{Id: contracts.ZCodeModeYolo, Name: "Yolo", Description: "Run everything without asking"},
		},
	},
}
