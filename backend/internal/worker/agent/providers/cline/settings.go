package cline

import (
	"log/slog"
	"maps"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Cline's settings.
//
// Three axes:
//
//   - The model, within the provider that the user's Cline settings select.
//     session.update_connection changes it between two model calls.
//   - The reasoning effort. A level and Off change through
//     session.update_connection. Auto sends no reasoning setting, so Cline
//     uses the one that the user's Cline settings state; a return to Auto
//     rebuilds the session, because an update cannot remove a setting.
//   - The mode, on LeapMux's permission-mode axis. Plan and Act are Cline's own
//     session modes, with Cline's own approval policy: Cline's interactive CLI
//     runs the tools that only read without asking and asks before every other
//     tool. Auto-approve is Act with every tool answered at once, as the CLI's
//     auto-approve switch does. Plan and Act also load no hook and no plugin
//     (configExtensions): Cline runs both with no approval, so a mode that asks
//     before a command must not load them. Cline builds a session's tools, its
//     system prompt and its extensions when it creates the session, so a change
//     of mode rebuilds the session (session_lifecycle.go), as Cline's CLI does
//     for a change between Plan and Act.
//
// A rebuild cannot run inside a turn. A change that needs one while a turn
// runs waits for the turn's end, and its settlement stays unresolved until
// then.

// modeLabel is the label of the permission-mode axis. Cline calls Plan and Act
// modes.
const modeLabel = "Mode"

// permissionModes are the values of the permission-mode axis.
var permissionModes = []agent.OptionDef{
	{Id: contracts.ClinePermissionModePlan, Name: "Plan", Description: "Plan with you before any change. Cline reads and searches without asking, and asks before each command, each web fetch and each subagent. Your Cline hooks and plugins do not run"},
	{Id: contracts.ClinePermissionModeAct, Name: "Act", Default: true, Description: "Cline reads and searches without asking, and asks before each edit, each command, each web fetch and every other tool. Your Cline hooks and plugins do not run"},
	{Id: contracts.ClinePermissionModeAutoApprove, Name: "Auto-approve", Description: "Run every tool without asking, with your Cline hooks and plugins"},
}

// permissionGroup is the permission-mode axis with current as its value.
func permissionGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(agent.OptionIDPermissionMode, modeLabel, agent.OptionOrderPermissionMode, current, permissionModes)
}

// staticOptionGroups are the axes that do not depend on a running agent. The
// registration and OptionGroups both read them.
var staticOptionGroups = []*leapmuxv1.AvailableOptionGroup{permissionGroup("")}

// validPermissionMode reports whether mode is a value of the axis.
func validPermissionMode(mode string) bool {
	return slices.ContainsFunc(permissionModes, func(def agent.OptionDef) bool { return def.Id == mode })
}

// clineSettings is the live configuration of the session.
type clineSettings struct {
	// model is Cline's model id within the session's provider.
	model string
	// effort is LeapMux's effort value: EffortAuto, effortOff, or a level.
	effort string
	// permissionMode is a value of the permission-mode axis.
	permissionMode string
}

// sessionMode is Cline's session mode for the settings.
func (s clineSettings) sessionMode() string {
	if s.permissionMode == contracts.ClinePermissionModePlan {
		return sessionModePlan
	}
	return sessionModeAct
}

// configExtensions are the kinds of configuration that the session loads, or
// nil for Cline's own default, which loads every kind. Plan and Act load the
// text kinds alone, so no hook and no plugin runs in a mode that asks. Cline's
// switch covers the workspace's files and the user's own alike.
func (s clineSettings) configExtensions() []string {
	if s.permissionMode == contracts.ClinePermissionModeAutoApprove {
		return nil
	}
	return []string{extensionRules, extensionSkills, extensionWorkflows}
}

// needsRebuild reports whether moving the session from s to next needs a new
// runtime: a change of Cline's session mode, a return to Auto effort, or
// extensions of next that differ from loaded, the ones that the runtime loaded
// (Agent.loadedExtensions). The extensions are compared with the runtime's and
// not with s's, because a move between Act and Auto-approve during a turn
// applies its approval policy at once and leaves the extensions for the turn's
// end.
func (s clineSettings) needsRebuild(next clineSettings, loaded []string) bool {
	return s.sessionMode() != next.sessionMode() ||
		!slices.Equal(loaded, next.configExtensions()) ||
		(next.effort == agent.EffortAuto && s.effort != agent.EffortAuto)
}

// connectionUpdates is the session.update_connection change from s to next,
// for the axes an update can change. It is empty when nothing changes.
func (s clineSettings) connectionUpdates(next clineSettings) map[string]any {
	updates := map[string]any{}
	if next.model != s.model {
		updates["modelId"] = next.model
	}
	if next.effort != s.effort && next.effort != agent.EffortAuto {
		for key, value := range reasoningFields(next.effort) {
			updates[key] = value
		}
	}
	return updates
}

// reasoningFields are the reasoning fields of Cline's runtime for effort. Off
// turns the reasoning off, because Cline's reasoning-effort field refuses the
// word; Auto states nothing.
func reasoningFields(effort string) map[string]any {
	switch effort {
	case "", agent.EffortAuto:
		return nil
	case effortOff:
		return map[string]any{"thinking": false}
	default:
		return map[string]any{"thinking": true, "reasoningEffort": effort}
	}
}

// configuredModel is the model that the user's Cline settings select for the
// session's provider, or the provider's first model when they select none.
func (a *Agent) configuredModel() string {
	if a.selection.Model != "" {
		return a.selection.Model
	}
	return defaultModelFor(a.selection.Provider)
}

// sessionModels returns the models the session offers: the table's models of
// its provider, and the configured model.
func (a *Agent) sessionModels() []*agent.ModelInfo {
	return sessionCatalog(a.selection.Provider, a.configuredModel())
}

// modelInfo returns the session's catalog entry of model, or nil for a model
// the session does not offer.
func (a *Agent) modelInfo(model string) *agent.ModelInfo {
	for _, m := range a.sessionModels() {
		if m.Id == model {
			return m
		}
	}
	return nil
}

// contextWindow is the context window of the session's model, or 0 when the
// catalog states none.
func (a *Agent) contextWindow() int64 {
	a.Mu.Lock()
	model := a.settings.model
	a.Mu.Unlock()
	if info := a.modelInfo(model); info != nil {
		return info.ContextWindow
	}
	return 0
}

// modelTakesEffort reports whether model offers effort. Auto fits every model.
func (a *Agent) modelTakesEffort(model, effort string) bool {
	if effort == agent.EffortAuto {
		return true
	}
	info := a.modelInfo(model)
	return info != nil && slices.ContainsFunc(info.SupportedEfforts, func(e *agent.EffortInfo) bool { return e.GetId() == effort })
}

// OptionGroups returns every axis with its current value.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	settings := a.settings
	a.Mu.Unlock()
	groups := providerkit.ModelAndEffortGroups(a.sessionModels(), settings.model, settings.effort, agent.EffortGroupLabel, nil)
	return append(groups, permissionGroup(settings.permissionMode))
}

// SettingsSnapshot confirms every current value.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

// settingsFromOptions reads LeapMux's option values into the settings. An axis
// the options leave empty, or state a value the session cannot take for,
// keeps its value in base. An effort the new model does not offer falls back
// to Auto.
func (a *Agent) settingsFromOptions(base clineSettings, options optionmap.Map) clineSettings {
	out := base
	if model := options.Get(agent.OptionIDModel); model != "" && !agent.UsesAccountDefaultModel(model) && a.modelInfo(model) != nil {
		out.model = model
	}
	if effort := options.Get(agent.OptionIDEffort); effort != "" && a.modelTakesEffort(out.model, effort) {
		out.effort = effort
	}
	if !a.modelTakesEffort(out.model, out.effort) {
		out.effort = agent.EffortAuto
	}
	if mode := options.Get(agent.OptionIDPermissionMode); validPermissionMode(mode) {
		out.permissionMode = mode
	}
	return out
}

// UpdateSettings applies the requested axes. A change that an update can make
// applies at once. A change that needs a rebuild applies at once between two
// turns; during a turn it waits for the turn's end, its settlement stays
// unresolved, and the part of it that an update can make applies at once.
//
// During a turn, a change starts from the settings that the session takes at
// the turn's end: a change that waits for the end already moved them, and the
// new change joins it. A change that needs no new runtime any more drops the
// one that waits.
//
// A rebuild between two turns holds the input queue with a settling turn, as a
// rebuild after a turn does. A message sent meanwhile then waits as for a
// running turn, and never reaches the runtime that the rebuild detaches. The
// read of the turn and the arm of the settling turn take sendMu, so no send
// arms a turn between the two.
func (a *Agent) UpdateSettings(requested optionmap.Map) agent.SettingsApplyResult {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.sendMu.Lock()
	a.Mu.Lock()
	have, busy, loaded, pending := a.settings, a.turn.active, a.loadedExtensions, a.modeRebuild
	a.Mu.Unlock()
	base, continuePlan := have, false
	if busy && pending != nil {
		base, continuePlan = pending.settings, pending.continuePlan
	}
	want := a.settingsFromOptions(base, requested)
	rebuild := have.needsRebuild(want, loaded)
	if rebuild && !busy {
		a.Mu.Lock()
		a.turn = turnState{active: true, settling: true, startedAt: a.clock.Now()}
		a.Mu.Unlock()
	}
	a.sendMu.Unlock()

	switch {
	case busy:
		a.Mu.Lock()
		a.modeRebuild = nil
		if rebuild {
			a.modeRebuild = &pendingModeChange{settings: want, continuePlan: continuePlan}
		}
		a.Mu.Unlock()
		a.applyLive(have, livePart(have, want))
	case rebuild:
		a.PublishTurnActive()
		if err := a.rebuildSession(want); err != nil {
			slog.Warn("cline rebuild the session for new settings", "agent_id", a.AgentID(), "error", err)
		}
		a.disarmTurn()
	default:
		a.applyLive(have, want)
	}

	result := a.SettingsSnapshot()
	// The refresh is a delta, and an axis it leaves out keeps its stored value.
	// It leaves out each axis that a change waiting for the turn's end sets, so
	// the stored value stays the one the user asked for. A value the session
	// cannot take is corrected to the one that runs.
	refresh := maps.Clone(result.SurfacedOptions)
	for key, value := range requested {
		if value == "" {
			continue
		}
		if confirmed, ok := result.SurfacedOptions[key]; !ok || confirmed != value {
			result.Settlements[key] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
			if a.pendingSets(key, value) {
				delete(refresh, key)
			}
		}
	}
	a.sink.PersistSettingsRefresh(refresh)
	return result
}

// pendingSets reports whether the change that waits for the turn's end sets
// the axis key to value.
func (a *Agent) pendingSets(key, value string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.modeRebuild == nil {
		return false
	}
	pending := a.modeRebuild.settings
	switch key {
	case agent.OptionIDModel:
		return pending.model == value
	case agent.OptionIDEffort:
		return pending.effort == value
	case agent.OptionIDPermissionMode:
		return pending.permissionMode == value
	default:
		return false
	}
}

// livePart is the part of the move from have to want that an update can make:
// the model, a reasoning level or Off, and a move between Act and
// Auto-approve.
func livePart(have, want clineSettings) clineSettings {
	live := want
	if want.effort == agent.EffortAuto && have.effort != agent.EffortAuto {
		live.effort = have.effort
	}
	if want.sessionMode() != have.sessionMode() {
		live.permissionMode = have.permissionMode
	}
	return live
}

// applyLive moves the running session from have to want with one
// session.update_connection, and records what applied. The permission mode
// between Act and Auto-approve is the worker's own policy, and needs no
// command. The caller reports the change: UpdateSettings through its result,
// and a change after a turn through the sink.
func (a *Agent) applyLive(have, want clineSettings) {
	applied := have
	applied.permissionMode = want.permissionMode
	if updates := have.connectionUpdates(want); len(updates) > 0 {
		sessionID := a.currentSession()
		ctx, cancel := a.requestContext()
		_, err := a.hub.command(ctx, commandUpdateConnection, sessionID, map[string]any{"sessionId": sessionID, "updates": updates})
		cancel()
		if err != nil {
			slog.Warn("cline update the session connection", "agent_id", a.AgentID(), "error", err)
		} else {
			applied.model, applied.effort = want.model, want.effort
		}
	}
	a.Mu.Lock()
	a.settings = applied
	a.Mu.Unlock()
}
