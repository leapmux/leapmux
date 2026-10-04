package kimi

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kimi Code's settings.
//
// A session's model, thinking level, permission mode, plan mode and swarm mode
// all live on its main agent, and POST /sessions/{id}/profile changes any of
// them at once. The server reports the plan and swarm modes back on
// `agent.status.updated`, and GET /sessions/{id}/status states all five.
//
// Plan mode is a switch beside the permission mode on the server, and one value
// of the permission-mode axis in LeapMux: the plan toggle and the plan approval
// read one axis for every provider. `plan` therefore means "plan mode on", and
// every other value means "plan mode off, in this permission mode". The
// permission mode the server keeps under plan mode stays what it was, so a plan
// the model leaves by itself returns to it.

// kimiOptionSwarmMode is the option-group id of the swarm-mode switch.
const kimiOptionSwarmMode = "swarmMode"

// The two values of the swarm-mode switch.
const (
	kimiSwarmOff = "off"
	kimiSwarmOn  = "on"
)

// kimiSettings is the live configuration of the session's main agent.
type kimiSettings struct {
	model string
	// effort is LeapMux's effort value: a thinking level, or EffortAuto, which
	// sends none and leaves the level the user's Kimi configuration states.
	effort string
	// permission is the server's permission mode, manual, yolo or auto. It
	// keeps its value under plan mode.
	permission string
	planMode   bool
	swarmMode  bool
}

// permissionOption is the value of LeapMux's permission-mode axis.
func (s kimiSettings) permissionOption() string {
	if s.planMode {
		return contracts.KimiModePlan
	}
	return s.permission
}

// swarmOption is the value of the swarm-mode switch.
func (s kimiSettings) swarmOption() string {
	if s.swarmMode {
		return kimiSwarmOn
	}
	return kimiSwarmOff
}

// kimiPermissionModes are LeapMux's permission-mode axis for Kimi Code, with
// the names and descriptions Kimi's own UI gives them.
var kimiPermissionModes = []agent.OptionDef{
	{Id: contracts.KimiModeManual, Name: "Always Ask", Default: true, Description: "Read files freely; ask before everything else"},
	{Id: contracts.KimiModeYolo, Name: "Ask When Needed", Description: "Run routine edits and commands; ask for risky actions, questions and plans"},
	{Id: contracts.KimiModeAuto, Name: "Never Ask", Description: "Run everything and decide every request automatically"},
	{Id: contracts.KimiModePlan, Name: "Plan", Description: "Research and write a plan; no edits until you approve it"},
}

// kimiSwarmModes are the two values of the swarm-mode switch.
var kimiSwarmModes = []agent.OptionDef{
	{Id: kimiSwarmOff, Name: "Off", Default: true, Description: "Run one agent, which starts subagents when it decides to"},
	{Id: kimiSwarmOn, Name: "On", Description: "Plan the work as a swarm of parallel subagents"},
}

func kimiPermissionGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(agent.OptionIDPermissionMode, "Permissions", agent.OptionOrderPermissionMode, current, kimiPermissionModes)
}

func kimiSwarmGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(kimiOptionSwarmMode, "Swarm", agent.OptionOrderProviderFirst, current, kimiSwarmModes)
}

// kimiStaticOptionGroups are the axes that do not depend on a running agent.
// The registration and OptionGroups both read them.
var kimiStaticOptionGroups = []*leapmuxv1.AvailableOptionGroup{kimiPermissionGroup(""), kimiSwarmGroup("")}

// OptionGroups returns every axis with its current value.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	settings, models := a.settings, a.catalog.models
	a.Mu.Unlock()
	groups := providerkit.ModelAndEffortGroups(models, settings.model, settings.effort, agent.EffortGroupLabel, nil)
	return append(groups, kimiPermissionGroup(settings.permissionOption()), kimiSwarmGroup(settings.swarmOption()))
}

// SettingsSnapshot confirms every current value.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

// kimiStatus is GET /sessions/{id}/status.
type kimiStatus struct {
	Busy             bool    `json:"busy"`
	Model            string  `json:"model"`
	ThinkingLevel    string  `json:"thinking_level"`
	Permission       string  `json:"permission"`
	PlanMode         bool    `json:"plan_mode"`
	SwarmMode        bool    `json:"swarm_mode"`
	ContextTokens    int64   `json:"context_tokens"`
	MaxContextTokens int64   `json:"max_context_tokens"`
	ContextUsage     float64 `json:"context_usage"`
}

// readStatus reads the session's status. A read also loads a session the
// server has not loaded, which is what makes a stored session subscribable.
func (a *Agent) readStatus(ctx context.Context, sessionID string) (kimiStatus, error) {
	var status kimiStatus
	err := a.api.get(ctx, kimiSessionPath(sessionID, "/status"), &status)
	return status, err
}

// applyStatus folds a status read into the settings. An effort of Auto stays
// Auto: the user asked the server to choose, and the level it chose is not a
// choice the user made.
func (a *Agent) applyStatus(status kimiStatus) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	a.applyStatusLocked(status)
}

// applyStatusLocked is applyStatus for a caller that holds a.Mu.
func (a *Agent) applyStatusLocked(status kimiStatus) {
	if status.Model != "" {
		a.settings.model = status.Model
	}
	if a.settings.effort != agent.EffortAuto && status.ThinkingLevel != "" {
		a.settings.effort = status.ThinkingLevel
	}
	if status.Permission != "" {
		a.settings.permission = status.Permission
	}
	a.settings.planMode = status.PlanMode
	a.settings.swarmMode = status.SwarmMode
	if status.MaxContextTokens > 0 || status.ContextTokens > 0 {
		a.recordContextLocked(status.ContextTokens, status.MaxContextTokens)
	}
}

// foldStatus folds a status read into the settings and the context readout,
// and reports what moved, as handleStatusUpdated reports an update event. A
// reconnect reads the status because the server never replays that event.
func (a *Agent) foldStatus(status kimiStatus) {
	a.Mu.Lock()
	before := a.settings
	a.applyStatusLocked(status)
	after := a.settings
	var usage map[string]any
	if status.MaxContextTokens > 0 || status.ContextTokens > 0 {
		usage = maps.Clone(a.contextUsage)
	}
	a.Mu.Unlock()
	if len(usage) > 0 {
		a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: usage})
	}
	a.reportSettingsChange(before, after)
}

// reportSettingsChange reports each axis that moved from before to after, for
// a move the server made by itself. The permission-mode axis goes through
// UpdatePermissionMode, and the other axes are persisted as a settings refresh.
func (a *Agent) reportSettingsChange(before, after kimiSettings) {
	if before.permissionOption() != after.permissionOption() {
		a.sink.UpdatePermissionMode(after.permissionOption())
	}
	refresh := optionmap.Map{}
	if before.model != after.model {
		refresh[agent.OptionIDModel] = after.model
	}
	if before.effort != after.effort {
		refresh[agent.OptionIDEffort] = after.effort
	}
	if before.swarmMode != after.swarmMode {
		refresh[kimiOptionSwarmMode] = after.swarmOption()
	}
	if len(refresh) > 0 {
		a.sink.PersistSettingsRefresh(refresh)
	}
}

// profileConfig builds the `agent_config` that moves the session to want.
// It states every axis whose value differs from have, and every axis when have
// is nil -- the first profile of a session.
func (a *Agent) profileConfig(want kimiSettings, have *kimiSettings) map[string]any {
	config := map[string]any{}
	if want.model != "" && (have == nil || want.model != have.model) {
		config[kimiConfigModel] = want.model
	}
	if want.effort != "" && want.effort != agent.EffortAuto && (have == nil || want.effort != have.effort) {
		config[kimiConfigThinking] = want.effort
	}
	if want.permission != "" && (have == nil || want.permission != have.permission) {
		config[kimiConfigPermissionMode] = want.permission
	}
	if have == nil || want.planMode != have.planMode {
		config[kimiConfigPlanMode] = want.planMode
	}
	if have == nil || want.swarmMode != have.swarmMode {
		config[kimiConfigSwarmMode] = want.swarmMode
	}
	return config
}

// kimiSettingsFromOptions reads LeapMux's option values into the server's
// settings. An axis the options leave empty keeps its value in base.
func kimiSettingsFromOptions(base kimiSettings, options optionmap.Map) kimiSettings {
	out := base
	if model := options.Get(agent.OptionIDModel); model != "" {
		out.model = model
	}
	if effort := options.Get(agent.OptionIDEffort); effort != "" {
		out.effort = effort
	}
	switch mode := options.Get(agent.OptionIDPermissionMode); mode {
	case "":
	case contracts.KimiModePlan:
		out.planMode = true
	default:
		out.permission = mode
		out.planMode = false
	}
	switch options.Get(kimiOptionSwarmMode) {
	case kimiSwarmOn:
		out.swarmMode = true
	case kimiSwarmOff:
		out.swarmMode = false
	}
	return out
}

// kimiValidPermission reports whether mode is one of the server's permission
// modes. `plan` is not: it is plan mode, on LeapMux's axis.
func kimiValidPermission(mode string) bool {
	return mode == contracts.KimiModeManual || mode == contracts.KimiModeYolo || mode == contracts.KimiModeAuto
}

// UpdateSettings applies the requested axes to the running session with one
// profile write, then reads the status back and settles each axis against it.
func (a *Agent) UpdateSettings(requested optionmap.Map) agent.SettingsApplyResult {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	result := agent.SettingsApplyResult{AppliedLive: true, Settlements: make(agent.OptionSettlements)}

	a.Mu.Lock()
	have := a.settings
	sessionID := a.sessionID
	catalog := a.catalog
	a.Mu.Unlock()

	valid := optionmap.Map{}
	for key, value := range requested {
		if value == "" {
			continue
		}
		result.Settlements[key] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
		switch key {
		case agent.OptionIDModel:
			if !catalog.has(value) {
				continue
			}
		case agent.OptionIDEffort:
			model := requested.Get(agent.OptionIDModel)
			if model == "" {
				model = have.model
			}
			if value != agent.EffortAuto && !kimiModelTakesEffort(catalog.model(model), value) {
				continue
			}
		case agent.OptionIDPermissionMode:
			if value != contracts.KimiModePlan && !kimiValidPermission(value) {
				continue
			}
		case kimiOptionSwarmMode:
			if value != kimiSwarmOn && value != kimiSwarmOff {
				continue
			}
		default:
			continue
		}
		valid[key] = value
	}
	want := kimiSettingsFromOptions(have, valid)
	config := a.profileConfig(want, &have)
	if want.effort == agent.EffortAuto && have.effort != agent.EffortAuto {
		// Auto sends no level at launch. Switching BACK to it must send one, or the
		// server would keep the level the user left. The level sent is the one the
		// user's configuration states for the model.
		if level := a.autoThinking(want.model); level != "" {
			config[kimiConfigThinking] = level
		}
	}

	if len(config) > 0 {
		if err := kimiCheckID("session", sessionID); err != nil {
			slog.Warn("kimi apply settings", "agent_id", a.AgentID(), "error", err)
			return result
		}
		ctx, cancel := a.requestContext()
		err := a.postProfile(ctx, sessionID, config)
		cancel()
		if err != nil {
			slog.Warn("kimi apply settings", "agent_id", a.AgentID(), "error", err)
			return result
		}
	}
	if _, requestedEffort := valid[agent.OptionIDEffort]; requestedEffort {
		// The effort the user picked is the axis value from now on: Auto, or the
		// level. The status read below then settles a level against the one the
		// server runs, and leaves Auto as Auto (applyStatus).
		a.Mu.Lock()
		a.settings.effort = want.effort
		a.Mu.Unlock()
	}
	ctx, cancel := a.requestContext()
	status, err := a.readStatus(ctx, sessionID)
	cancel()
	if err != nil {
		slog.Warn("kimi read settings after an update", "agent_id", a.AgentID(), "error", err)
		return result
	}
	a.applyStatus(status)
	result.SurfacedOptions = agent.CurrentOptions(a.OptionGroups())
	for key := range result.Settlements {
		value := result.SurfacedOptions[key]
		if value != "" && value == requested[key] {
			result.Settlements[key] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &value}
		}
	}
	a.sink.PersistSettingsRefresh(result.SurfacedOptions)
	return result
}

// kimiModelTakesEffort reports whether model offers the thinking level effort.
func kimiModelTakesEffort(model *agent.ModelInfo, effort string) bool {
	if model == nil {
		return false
	}
	return slices.ContainsFunc(model.SupportedEfforts, func(e *agent.EffortInfo) bool { return e.GetId() == effort })
}

// autoThinking is the level Auto stands for on model: the one the user's Kimi
// configuration states, or `on`, which the server reads as the model's own
// default level. A model that cannot think takes nothing.
func (a *Agent) autoThinking(model string) string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	entry := a.catalog.model(model)
	if entry == nil || len(entry.SupportedEfforts) == 0 {
		return ""
	}
	defaults := a.catalog.thinking
	switch {
	case defaults.Enabled != nil && !*defaults.Enabled && kimiModelTakesEffort(entry, kimiThinkingOff):
		return kimiThinkingOff
	case defaults.Effort != "" && kimiModelTakesEffort(entry, defaults.Effort):
		return defaults.Effort
	default:
		return kimiThinkingOn
	}
}

// applyApprovalPermissionMode moves the session to the permission mode an
// approval switches to, before the worker posts the approval.
//
// A change of LeapMux's permission-mode axis is reported here, because the
// server reports a mode switch on no event. Under plan mode the axis reads
// Plan, so a plan approval changes nothing yet: the status update that leaves
// plan mode reports the new mode.
func (a *Agent) applyApprovalPermissionMode(sessionID, mode string) error {
	if !kimiValidPermission(mode) {
		return fmt.Errorf("the permission mode %q is not one Kimi Code offers", mode)
	}
	ctx, cancel := a.requestContext()
	defer cancel()
	if err := a.postProfile(ctx, sessionID, map[string]any{kimiConfigPermissionMode: mode}); err != nil {
		return fmt.Errorf("switch the permission mode for the approval: %w", err)
	}
	a.Mu.Lock()
	before := a.settings.permissionOption()
	a.settings.permission = mode
	after := a.settings.permissionOption()
	a.Mu.Unlock()
	if before != after {
		a.sink.UpdatePermissionMode(after)
	}
	return nil
}

// kimiStatusUpdate is the part of agent.status.updated the worker reads. Each
// field is optional: an update states only what changed.
type kimiStatusUpdate struct {
	Model            string `json:"model"`
	ThinkingEffort   string `json:"thinkingEffort"`
	PlanMode         *bool  `json:"planMode"`
	SwarmMode        *bool  `json:"swarmMode"`
	ContextTokens    *int64 `json:"contextTokens"`
	MaxContextTokens *int64 `json:"maxContextTokens"`
	Usage            *struct {
		CurrentTurn *kimiTokenUsage `json:"currentTurn"`
	} `json:"usage"`
}

// kimiTokenUsage is one usage object: the tokens of a step, a turn or a model.
type kimiTokenUsage struct {
	InputOther         int64 `json:"inputOther"`
	Output             int64 `json:"output"`
	InputCacheRead     int64 `json:"inputCacheRead"`
	InputCacheCreation int64 `json:"inputCacheCreation"`
}

// handleStatusUpdated folds a status update of the main agent into the settings
// and the context readout.
//
// A plan mode the model entered or left by itself changes LeapMux's
// permission-mode axis, which the update reports and persists. A model or level
// the server moved on its own is persisted as a settings refresh.
func (a *Agent) handleStatusUpdated(event kimiEvent) {
	if event.AgentID != kimiMainAgentID {
		return
	}
	var update kimiStatusUpdate
	if !event.decode(&update) {
		return
	}
	a.Mu.Lock()
	before := a.settings
	if update.PlanMode != nil {
		a.settings.planMode = *update.PlanMode
	}
	if update.SwarmMode != nil {
		a.settings.swarmMode = *update.SwarmMode
	}
	if update.Model != "" {
		a.settings.model = update.Model
	}
	if update.ThinkingEffort != "" && a.settings.effort != agent.EffortAuto {
		a.settings.effort = update.ThinkingEffort
	}
	after := a.settings
	var usage map[string]any
	if update.ContextTokens != nil || update.MaxContextTokens != nil {
		var tokens, window int64
		if update.ContextTokens != nil {
			tokens = *update.ContextTokens
		}
		if update.MaxContextTokens != nil {
			window = *update.MaxContextTokens
		}
		a.recordContextLocked(tokens, window)
		if update.Usage != nil && update.Usage.CurrentTurn != nil {
			turn := update.Usage.CurrentTurn
			providerkit.ContextTokenCounts{
				Input: turn.InputOther, CacheRead: turn.InputCacheRead,
				CacheWrite: turn.InputCacheCreation, Output: turn.Output,
			}.Into(a.contextUsage)
		}
		usage = maps.Clone(a.contextUsage)
	}
	a.Mu.Unlock()

	if len(usage) > 0 {
		a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: usage})
	}
	a.reportSettingsChange(before, after)
}

// recordContextLocked records the context the main agent holds. The caller
// holds a.Mu.
func (a *Agent) recordContextLocked(tokens, window int64) {
	if a.contextUsage == nil {
		a.contextUsage = providerkit.ContextUsageMap(providerkit.ContextTokenCounts{})
	}
	if tokens > 0 {
		a.contextUsage[contracts.ContextUsageFieldContextTokens] = tokens
	}
	if window > 0 {
		a.contextUsage[contracts.ContextUsageFieldContextWindow] = window
	}
}

// recordStepUsage folds one step's token counts into the readout.
func (a *Agent) recordStepUsage(event kimiEvent) {
	var step struct {
		Usage *kimiTokenUsage `json:"usage"`
	}
	if !event.decode(&step) || step.Usage == nil {
		return
	}
	a.Mu.Lock()
	if a.contextUsage == nil {
		a.contextUsage = providerkit.ContextUsageMap(providerkit.ContextTokenCounts{})
	}
	providerkit.ContextTokenCounts{
		Input: step.Usage.InputOther, CacheRead: step.Usage.InputCacheRead,
		CacheWrite: step.Usage.InputCacheCreation, Output: step.Usage.Output,
	}.Into(a.contextUsage)
	usage := maps.Clone(a.contextUsage)
	a.Mu.Unlock()
	a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: usage})
}
