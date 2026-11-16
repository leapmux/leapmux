package opencode

import (
	"encoding/json"
	"log/slog"
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// AutoApproveGroup is the client's Auto-approve option.
//
// LeapMux answers eligible permission requests once while this option is on.
// The native server applies deny rules before raising a request. The ACP
// family's session-rule update appends rules instead of removing them.
// Client replies preserve the session rules when this option changes.
var AutoApproveGroup = agent.SelectGroup(contracts.OpenCodeOptionAutoApprove, "Auto-approve", agent.OptionOrderPermissionMode, "false", []agent.OptionDef{
	{Id: "false", Name: "Off", Description: "Ask before each tool call that no rule allows", Default: true},
	{Id: "true", Name: "On", Description: "Approve eligible permission requests once. Explicit deny rules still apply."},
})

// acpPermissionKindAllowAlways is the ACP kind of a permission option that
// allows the call always. Only the worker reads it, so it stays out of the
// shared ACP contract.
const acpPermissionKindAllowAlways = "allow_always"

// autoApproveFromOptions reads the launch value of the Auto-approve option.
func autoApproveFromOptions(options optionmap.Map) (bool, error) {
	return options.Bool(contracts.OpenCodeOptionAutoApprove)
}

// localOptionGroups reports the Auto-approve option with its current value,
// and the groups of the member (MemberOptionGroups).
func (b *FamilyBase) localOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	b.Mu.Lock()
	autoApprove := b.autoApprove
	b.Mu.Unlock()
	groups := []*leapmuxv1.AvailableOptionGroup{providerkit.LiveGroup(AutoApproveGroup, strconv.FormatBool(autoApprove))}
	if b.MemberOptionGroups != nil {
		groups = append(groups, b.MemberOptionGroups()...)
	}
	return groups
}

// applyLocalOption applies a new Auto-approve value at once. The base then
// approves each request that already waits (Base.UpdateSettings).
func (b *FamilyBase) applyLocalOption(id, value string) (bool, error) {
	if id != contracts.OpenCodeOptionAutoApprove {
		return false, nil
	}
	autoApprove, err := optionmap.Map{id: value}.Bool(id)
	if err != nil {
		return true, err
	}
	b.setAutoApprove(autoApprove)
	return true, nil
}

// setAutoApprove sets the value of the Auto-approve option.
func (b *FamilyBase) setAutoApprove(autoApprove bool) {
	b.Mu.Lock()
	b.autoApprove = autoApprove
	b.Mu.Unlock()
}

// UpdateSettings refuses an Auto-approve value that is neither true nor
// false before any other setting changes. A failed local write would ask the
// service for a restart, and the restart would refuse the same value.
func (b *FamilyBase) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	if _, err := autoApproveFromOptions(options); err != nil {
		return b.SettingsSnapshot()
	}
	return b.Base.UpdateSettings(options)
}

// withAutoApprove runs claim while Auto-approve is on. The claim runs under
// the settings lock, so a change of the option cannot pass between the check
// and the approval.
func (b *FamilyBase) withAutoApprove(claim func()) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if b.autoApprove && !b.StoppedLocked() {
		claim()
	}
}

// permissionOffersAlways reports whether Auto-approve may answer a permission
// request: the request must offer an option that allows the call always.
//
// Kilo offers no such option for a request that it reserves for a person: a
// batch of skill shell commands, or a command that leaves the sandbox. Kilo's
// own `--auto` does not approve those either. OpenCode offers the option on
// every request, so Auto-approve answers each of them.
func permissionOffersAlways(line *providerkit.ParsedLine) bool {
	var request struct {
		Options []struct {
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(line.Params, &request) != nil {
		return false
	}
	for _, option := range request.Options {
		if option.Kind == acpPermissionKindAllowAlways {
			return true
		}
	}
	return false
}

func (s *openCodeSessionRuntime) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	s.mu.Lock()
	selectedAgentID, selectedModel, catalog := s.selectedAgentID, s.selectedModel, s.catalog
	s.mu.Unlock()
	primary := selectedAgentID
	if primary == "" {
		primary = catalog.defaultAgent
	}
	model, err := catalog.effectiveModel(selectedModel, primary)
	if err != nil && selectedModel != nil {
		model = *selectedModel
	}
	effort := agent.EffortAuto
	if model.Variant != "default" {
		effort = openCodeVariantOption(model.Variant)
	}
	groups := providerkit.ModelAndEffortGroups(catalog.modelOptions(), openCodeModelKey(model), effort, agent.EffortGroupLabel, nil)
	if err != nil && selectedModel != nil {
		groups = append(groups, agent.ReadOnlyValueGroup(agent.OptionIDEffort, agent.EffortGroupLabel, agent.OptionOrderEffort, effort, ""))
	}

	primaryGroup := agent.SelectGroup(agent.OptionIDPrimaryAgent, "Agent", agent.OptionOrderPrimaryAgent, catalog.defaultAgent, catalog.primaryAgents())
	return append(groups, providerkit.LiveGroup(primaryGroup, primary))
}

func (s *openCodeSessionRuntime) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(s.OptionGroups()))
}

// applySettingsChange retains a newer native observation when an HTTP reply is late.
func (s *openCodeSessionRuntime) applySettingsChange(send func() error, apply func()) error {
	s.mu.Lock()
	version := s.settingsVersion
	s.mu.Unlock()
	if err := send(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.settingsVersion == version {
		apply()
		s.settingsVersion++
	}
	s.mu.Unlock()
	return nil
}

func (s *openCodeSessionRuntime) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	s.operations.Lock()
	defer s.operations.Unlock()
	if s.ctx.Err() != nil {
		return agent.RestartRequiredSettings(options)
	}
	s.mu.Lock()
	selectedAgentID, selectedModel, catalog := s.selectedAgentID, s.selectedModel, s.catalog
	s.mu.Unlock()
	currentAgent := selectedAgentID
	if currentAgent == "" {
		currentAgent = catalog.defaultAgent
	}
	nextAgent := currentAgent
	if requested := options[agent.OptionIDPrimaryAgent]; requested != "" && catalog.offersAgent(requested) {
		nextAgent = requested
	}
	model, requestedModelAvailable := catalog.modelRef(options[agent.OptionIDModel])
	if !requestedModelAvailable {
		var err error
		model, err = catalog.effectiveModel(selectedModel, nextAgent)
		if err != nil {
			return agent.RestartRequiredSettings(options)
		}
	}
	requestedEffort := options[agent.OptionIDEffort]
	previousModel, previousModelError := catalog.effectiveModel(selectedModel, currentAgent)
	if requestedModelAvailable && previousModelError == nil && openCodeModelKey(previousModel) != openCodeModelKey(model) &&
		!catalog.offersEffortOption(openCodeModelKey(previousModel), agent.EffortAuto) && (requestedEffort == "" || requestedEffort == agent.EffortAuto) {
		variant, found, err := s.rpc.modelVariantPreference(s.ctx, s.id, model)
		if err != nil {
			slog.Warn("OpenCode could not read the model effort preference", "session_id", s.id, "error", err)
			return s.SettingsSnapshot()
		}
		if found {
			model.Variant = variant
			model, err = catalog.settleVariant(model)
			if err != nil {
				return agent.RestartRequiredSettings(options)
			}
			requestedEffort = ""
		}
	}
	modelChanged := requestedModelAvailable && (selectedModel == nil || *selectedModel != model)

	if requested := requestedEffort; requested != "" {
		if catalog.offersEffortOption(openCodeModelKey(model), requested) {
			variant, valid := openCodeVariantFromOption(requested)
			if valid {
				model.Variant = variant
				modelChanged = selectedModel == nil || *selectedModel != model
			}
		}
	}
	if nextAgent != currentAgent {
		err := s.applySettingsChange(func() error { return s.rpc.switchAgent(s.ctx, s.id, nextAgent) }, func() { s.selectedAgentID = nextAgent })
		if err != nil {
			slog.Warn("OpenCode could not change the session agent", "session_id", s.id, "error", err)
			return agent.RestartRequiredSettings(options)
		}
	}
	if modelChanged {
		err := s.applySettingsChange(func() error { return s.rpc.switchModel(s.ctx, s.id, model) }, func() { s.selectedModel = copyOpenCodeModelRef(&model) })
		if err != nil {
			slog.Warn("OpenCode could not change the session model", "session_id", s.id, "error", err)
			return agent.RestartRequiredSettings(options)
		}
	}
	return s.SettingsSnapshot()
}

func (c openCodeCatalog) offersEffortOption(modelID, optionID string) bool {
	model := agent.FindAvailableModel(c.modelOptions(), modelID)
	if model == nil {
		return false
	}
	for _, effort := range model.SupportedEfforts {
		if effort.Id == optionID {
			return true
		}
	}
	return false
}
