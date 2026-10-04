package codewhale

import (
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Codewhale's four settings axes.
//
//   - model: the thread's model, within the provider the thread runs on. The
//     thread update route sets it.
//   - effort: the reasoning effort. The runtime takes it PER TURN only -- its
//     thread update has no effort field -- so LeapMux holds the value and sends
//     it with each turn. EffortAuto sends none, and the runtime uses its own.
//   - codewhale_mode: agent or plan. Plan mode is a thread setting: the runtime
//     refuses every mutating call centrally, and no tool asks to leave it.
//   - permissionMode: the thread's permission posture.

// ModeLabel labels the mode axis.
const ModeLabel = "Mode"

// PostureLabel labels the permission-posture axis.
const PostureLabel = "Permission Posture"

// codewhaleModeGroup is the static template of the mode axis. The registration
// and OptionGroups both read it, so the static fallback and a running agent
// offer one list.
var codewhaleModeGroup = &leapmuxv1.AvailableOptionGroup{
	Id:           contracts.CodewhaleOptionMode,
	Label:        ModeLabel,
	DefaultValue: contracts.CodewhaleDefaultMode,
	Mutable:      true,
	Order:        agent.OptionOrderProviderFirst,
	Options: []*leapmuxv1.AvailableOption{
		{Id: contracts.CodewhaleModeAgent, Name: "Agent", Description: "Read, edit files and run commands"},
		{Id: contracts.CodewhaleModePlan, Name: "Plan", Description: "Research and plan; the runtime refuses edits and commands"},
	},
}

// codewhalePostureGroup is the static template of the permission-posture axis.
var codewhalePostureGroup = &leapmuxv1.AvailableOptionGroup{
	Id:           agent.OptionIDPermissionMode,
	Label:        PostureLabel,
	DefaultValue: contracts.CodewhalePostureAsk,
	Mutable:      true,
	Order:        agent.OptionOrderPermissionMode,
	Options: []*leapmuxv1.AvailableOption{
		{Id: contracts.CodewhalePostureAsk, Name: "Ask", Description: "Ask before a command or an edit that the policy restricts"},
		{Id: contracts.CodewhalePostureAutoReview, Name: "Auto Review", Description: "Decide each restricted call by Codewhale's own review rules"},
		{Id: contracts.CodewhalePostureFullAccess, Name: "Full Access", Description: "Run every call without asking, outside the sandbox"},
	},
}

// codewhaleStaticOptionGroups holds the axes that do not depend on a running
// agent.
var codewhaleStaticOptionGroups = []*leapmuxv1.AvailableOptionGroup{codewhaleModeGroup, codewhalePostureGroup}

// codewhaleSettings is the agent's view of its thread's settings.
type codewhaleSettings struct {
	model string
	// provider and providerID address the catalog of the thread's model
	// provider. The runtime states both on the thread record.
	provider   string
	providerID string
	// effort is LeapMux's own: the runtime keeps no per-thread effort that the
	// agent could read back.
	effort  string
	mode    string
	posture string
	// defaultModel is the model the runtime chose for a thread that LeapMux
	// opened with no model, which is the provider's default. It is empty when
	// nothing states the default: a resumed thread, a thread opened with an
	// explicit model, and a thread whose provider changed since.
	defaultModel string
}

// applyThreadRecordLocked takes the settings a thread record states. It
// reports whether the provider changed, which changes the model catalog. The
// caller holds Mu.
func (a *Agent) applyThreadRecordLocked(thread threadRecord) (providerChanged bool) {
	if thread.Model != "" {
		a.settings.model = thread.Model
	}
	provider := thread.ModelProvider
	providerID := thread.ModelProviderID
	if provider != "" && (provider != a.settings.provider || providerID != a.settings.providerID) {
		providerChanged = a.settings.provider != ""
		a.settings.provider = provider
		a.settings.providerID = providerID
		if providerChanged {
			// The default belonged to the old provider's catalog.
			a.settings.defaultModel = ""
		}
	}
	if thread.Mode != "" {
		a.settings.mode = thread.Mode
	}
	if thread.PermissionPosture != "" {
		a.settings.posture = thread.PermissionPosture
	}
	return providerChanged
}

// OptionGroups returns every settings axis with its current value.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.optionGroupsLocked()
}

func (a *Agent) optionGroupsLocked() []*leapmuxv1.AvailableOptionGroup {
	effort := a.settings.effort
	if effort == "" {
		effort = agent.EffortAuto
	}
	groups := providerkit.ModelAndEffortGroups(a.models, a.settings.model, effort, agent.EffortGroupLabel, nil)
	return append(groups,
		providerkit.LiveGroup(codewhaleModeGroup, a.settings.mode),
		providerkit.LiveGroup(codewhalePostureGroup, a.settings.posture),
	)
}

// SettingsSnapshot reports the current values of every axis.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

// UpdateSettings applies each requested axis to the running thread.
//
// The model, the mode and the posture ride ONE thread update, and the reply
// states the values the runtime settled on. The effort is LeapMux's own and
// takes effect at the next turn.
func (a *Agent) UpdateSettings(requested optionmap.Map) agent.SettingsApplyResult {
	result := agent.SettingsApplyResult{AppliedLive: true, Settlements: make(agent.OptionSettlements)}
	var update updateThreadRequest
	patched := false
	for key, value := range requested {
		if value == "" {
			continue
		}
		switch key {
		case agent.OptionIDModel:
			update.Model = new(string)
			*update.Model = value
			patched = true
		case contracts.CodewhaleOptionMode:
			if !slices.Contains([]string{contracts.CodewhaleModeAgent, contracts.CodewhaleModePlan}, value) {
				continue
			}
			update.Mode = new(string)
			*update.Mode = value
			patched = true
		case agent.OptionIDPermissionMode:
			if !postureIsKnown(value) {
				continue
			}
			update.PermissionPosture = new(string)
			*update.PermissionPosture = value
			patched = true
		case agent.OptionIDEffort:
			a.Mu.Lock()
			a.settings.effort = value
			a.Mu.Unlock()
			confirmed := value
			result.Settlements[key] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &confirmed}
			continue
		default:
			continue
		}
		result.Settlements[key] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
	}

	if patched {
		threadID := a.currentThreadID()
		thread, err := a.updateThread(threadID, update)
		if err != nil {
			slog.Warn("codewhale apply settings", "agent_id", a.AgentID(), "error", err)
		} else {
			a.Mu.Lock()
			providerChanged := a.applyThreadRecordLocked(thread)
			a.Mu.Unlock()
			if providerChanged {
				a.refreshModelCatalog()
			}
		}
	}

	result.SurfacedOptions = agent.CurrentOptions(a.OptionGroups())
	for key, settlement := range result.Settlements {
		if settlement.State == agent.OptionSettlementConfirmed {
			continue
		}
		if value := result.SurfacedOptions[key]; value == requested[key] {
			result.Settlements[key] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &value}
		}
	}
	a.sink.PersistSettingsRefresh(result.SurfacedOptions)
	return result
}

// postureIsKnown reports whether a posture is one that LeapMux offers.
func postureIsKnown(posture string) bool {
	for _, option := range codewhalePostureGroup.GetOptions() {
		if option.GetId() == posture {
			return true
		}
	}
	return false
}

// handleThreadUpdated takes a settings change that reached the thread from
// anywhere: this agent's own update, a remembered approval that raised the
// posture, or another client of the same runtime.
func (a *Agent) handleThreadUpdated(env codewhaleEnvelope) {
	var payload struct {
		Thread threadRecord `json:"thread"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.Thread.ID == "" {
		return
	}
	a.Mu.Lock()
	before := agent.CurrentOptions(a.optionGroupsLocked())
	providerChanged := a.applyThreadRecordLocked(payload.Thread)
	after := agent.CurrentOptions(a.optionGroupsLocked())
	a.Mu.Unlock()
	if providerChanged {
		// The catalog read is a request, and the dispatch must not wait for it.
		go a.refreshModelCatalog()
		return
	}
	if !optionsEqual(before, after) {
		a.sink.PersistSettingsRefresh(after)
	}
}

// optionsEqual reports whether two option maps hold the same values.
func optionsEqual(left, right optionmap.Map) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
