package claude

import (
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Option keys for Claude Code option groups.
const (
	OptionOutputStyle    = "outputStyle"
	OptionFastMode       = "fastMode"
	OptionAlwaysThinking = "alwaysThinkingEnabled"
)

// Extended Thinking option IDs. Claude Code only exposes a single
// alwaysThinkingEnabled boolean — it picks thinking.type:"adaptive" vs
// "enabled" per-model — so there is nothing to store beyond on/off. The
// UI label for the "on" option is set per-model in AvailableOptionGroups
// ("Adaptive" for Opus/Sonnet, "On" for Haiku).
const (
	AlwaysThinkingOn  = "on"
	AlwaysThinkingOff = "off"
)

// Fast Mode option IDs.
const (
	FastModeOn  = "on"
	FastModeOff = "off"
)

// OptionGroups returns every Claude configuration axis as config option
// groups: model and effort (omitted for third-party providers that hide
// model/effort UI), output style (when the CLI reports styles), fast mode,
// extended thinking, and the permission mode group (with "auto" filtered when
// the startup probe rejected it). Each group carries its confirmed current
// value; the manager re-derives the model group's default badge.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	model, effort, mode := a.model, a.effort, a.confirmedPermissionMode
	outputStyle, fastMode, thinking := a.outputStyle, a.fastMode, a.alwaysThinking
	availStyles := a.availableOutputStyles
	autoAvail := a.autoModeAvailable
	a.Mu.Unlock()

	var groups []*leapmuxv1.AvailableOptionGroup

	if catalog := a.availableModelCatalog(); len(catalog) > 0 {
		// Claude carries an extra per-model sub_group (extended thinking) beyond effort, so it
		// passes claudeModelSubGroups; otherwise this is the same model+effort projection Codex
		// and Pi use.
		groups = append(groups, providerkit.ModelAndEffortGroups(catalog, model, effort, agent.EffortGroupLabel, claudeModelSubGroups)...)
	} else if model != "" {
		// Hidden model/effort UI (a third-party session, or can_change_model_and_effort
		// =false): there is no selectable catalog, but the session is running a concrete
		// model. Surface it (and a concrete effort, when set) as READ-ONLY groups so
		// `remote agent get`/list and the UI show what's running instead of a blank --
		// the model isn't user-changeable here, so the groups are non-mutable. The model
		// name is humanized (claudeFallbackDisplayName) so the readout matches the
		// selectable catalog's friendly names rather than showing the raw bracketed id.
		groups = append(groups, providerkit.ReadOnlyModelAndEffortGroups(model, claudeFallbackDisplayName(model), effort)...)
	}

	if len(availStyles) > 0 {
		defs := make([]agent.OptionDef, 0, len(availStyles))
		for _, s := range availStyles {
			defs = append(defs, agent.OptionDef{Id: s, Name: providerkit.TitleCaseID(s, ""), Default: s == "default"})
		}
		groups = append(groups, agent.SelectGroup(OptionOutputStyle, "Output Style", agent.OptionOrderProviderThird, outputStyle, defs))
	}

	groups = append(groups, agent.SelectGroup(OptionFastMode, "Fast Mode", agent.OptionOrderProviderSecond, fastMode, []agent.OptionDef{
		{Id: FastModeOn, Name: "On"},
		{Id: FastModeOff, Name: "Off", Default: true},
	}))

	groups = append(groups, thinkingGroupForModel(model, thinking))

	if pg := optionids.GroupByID(claudeStaticOptionGroups, agent.OptionIDPermissionMode); pg != nil {
		groups = append(groups, livePermissionModeGroup(pg, mode, autoAvail))
	}

	return groups
}

// thinkingGroupForModel builds the extended-thinking group for a model. The
// enabled option's id is always "on"; only its display label varies by model
// ("Adaptive" for models that pick thinking.type:"adaptive", "On" otherwise --
// see modelSupportsAdaptiveThinking), so a model switch must re-emit this group.
func thinkingGroupForModel(model, current string) *leapmuxv1.AvailableOptionGroup {
	enabledName := "On"
	if modelSupportsAdaptiveThinking(model) {
		enabledName = "Adaptive"
	}
	return agent.SelectGroup(OptionAlwaysThinking, "Extended Thinking", agent.OptionOrderProviderFirst, current, []agent.OptionDef{
		{Id: AlwaysThinkingOn, Name: enabledName, Default: true},
		{Id: AlwaysThinkingOff, Name: "Off"},
	})
}

// claudeModelSubGroups is Claude's ModelSubGroupsFunc: each model carries its
// effort group AND its extended-thinking group (whose enabled label is per
// model), so the frontend rebuilds both the instant the model selection changes
// rather than waiting for the relaunch a model switch triggers. The carried
// groups hold no current value (the frontend overlays the live selection); only
// their option lists and defaults are model-dependent.
func claudeModelSubGroups(m *agent.ModelInfo) []*leapmuxv1.AvailableOptionGroup {
	groups := agent.EffortSubGroups(m)
	if m != nil {
		groups = append(groups, thinkingGroupForModel(m.Id, ""))
	}
	return groups
}

// livePermissionModeGroup builds a writable permission-mode group from the
// static template. It sets the confirmed value and hides "auto" if the startup
// probe rejects it. The group keeps the template's DefaultValue.
func livePermissionModeGroup(static *leapmuxv1.AvailableOptionGroup, current string, autoAvail bool) *leapmuxv1.AvailableOptionGroup {
	// The catalog also serves resumed sessions, which do not request the
	// new-session mode. Registration.PermissionDefaults.NewSession defines that
	// request. A probe result must not give default_value a second meaning.
	if static != nil && !autoAvail {
		// Filter a copy of the template so the UI cannot offer an unavailable
		// mode. LiveGroup keeps the template's ID, label, default, and order.
		// It then sets the current value from the session.
		//
		// Keep the current mode even if the probe rejected it. A later live switch
		// can succeed after a transient probe failure. Removing that mode leaves
		// CurrentValue without an option, so the frontend shows the wrong mode.
		// This keeps the current value selectable, as ACP's buildOptionGroup does.
		static = providerkit.FilterGroupOptions(static, func(o *leapmuxv1.AvailableOption) bool {
			return o.GetId() != contracts.ClaudeModeAuto || o.GetId() == current
		})
	}
	return providerkit.LiveGroup(static, current)
}

// UpdateSettings applies settings through live control requests when possible.
// The result identifies values that need a restart or a later readback.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.Mu.Lock()
	curModel, curEffort := a.model, a.effort
	curPermissionMode := a.confirmedPermissionMode
	curOutputStyle, curFastMode, curThinking := a.outputStyle, a.fastMode, a.alwaysThinking
	availStyles := a.availableOutputStyles
	a.Mu.Unlock()

	// Normalize the requested model to the same canonical id space a.model and the catalog
	// use, so a re-spelled-but-identical model (a CLI alias like "claude-opus-4-8[1m]" vs the
	// stored "opus[1m]") is recognized as unchanged -- no redundant apply_flag_settings, and
	// the effort resolver's per-model lookup (which keys on normalized catalog ids) can still
	// find the model to apply its ultracode-downgrade guard. The DefaultModelSentinel ("default")
	// passes through normalization unchanged, so the sentinel restart check below still fires.
	reqModel := normalizeClaudeCodeModel(options[agent.OptionIDModel])
	reqEffort := options[agent.OptionIDEffort]

	// Switching to EffortAuto can't be done live: the CLI doesn't accept
	// effortLevel="auto" for apply_flag_settings, and the only way to go
	// back to "let Claude pick" is to re-launch without --effort. Signal
	// the caller to restart instead.
	if agent.IsEffortAutoTransition(reqEffort, curEffort) {
		return agent.RestartRequiredSettings(options)
	}

	// Switching to the account-default sentinel can't be done live either:
	// apply_flag_settings stores the model string verbatim (it does NOT resolve
	// "default" the way set_model and the --model-omitted launch path do), so it
	// would strand the session on a bogus "default" model. Re-launch so startup
	// resolves it to the concrete model, mirroring the EffortAuto restart above.
	if reqModel == agent.DefaultModelSentinel && reqModel != curModel {
		return agent.RestartRequiredSettings(options)
	}

	flagSettings := map[string]interface{}{}
	changedFlagOptionIDs := make([]string, 0, len(claudeFlagOptionIDs))

	if reqModel != "" && reqModel != curModel {
		flagSettings["model"] = reqModel
		changedFlagOptionIDs = append(changedFlagOptionIDs, agent.OptionIDModel)
	}
	// Resolve the requested effort against the model it will run under (the new model
	// when this update also switches model) so a combined model+effort change can't
	// push an unsupported effort -- e.g. {model:"sonnet", ultracode:true} -- to the
	// CLI. The UI sends single-field updates, so this only bites non-UI/raw callers,
	// but it keeps the live path consistent with buildModelEffortArgs's launch-time
	// downgrade.
	targetModel := curModel
	if reqModel != "" {
		targetModel = reqModel
	}
	effortSettings := a.effortResolver().updateFlagSettings(targetModel, reqEffort, curEffort)
	if len(effortSettings) > 0 {
		maps.Copy(flagSettings, effortSettings)
		changedFlagOptionIDs = append(changedFlagOptionIDs, agent.OptionIDEffort)
	}

	if v := options[OptionOutputStyle]; v != "" && v != curOutputStyle {
		if !slices.Contains(availStyles, v) {
			return agent.RestartRequiredSettings(options)
		}
		flagSettings[OptionOutputStyle] = v
		changedFlagOptionIDs = append(changedFlagOptionIDs, OptionOutputStyle)
	}
	if v := options[OptionFastMode]; v != "" && v != curFastMode {
		flagSettings[OptionFastMode] = flagSettingOnOff(v)
		changedFlagOptionIDs = append(changedFlagOptionIDs, OptionFastMode)
	}
	if v := options[OptionAlwaysThinking]; v != "" && v != curThinking {
		flagSettings[OptionAlwaysThinking] = flagSettingThinking(v)
		changedFlagOptionIDs = append(changedFlagOptionIDs, OptionAlwaysThinking)
	}

	if len(flagSettings) > 0 {
		if err := a.sendApplyFlagSettings(a.Context(), flagSettings, a.APITimeout()); err != nil {
			slog.Error("apply_flag_settings failed", "agent_id", a.AgentID(), "error", err)
			return agent.RestartRequiredSettings(options)
		}
	}

	permissionConfirmed := true
	if mode := options[agent.OptionIDPermissionMode]; mode != "" && mode != curPermissionMode {
		var applied bool
		applied, permissionConfirmed = a.applyPermissionModeLive(mode)
		if !applied {
			return agent.RestartRequiredSettings(options)
		}
	}

	// Read back, persist, and broadcast the flag-settings result ONLY after every live apply has
	// landed -- NOT right after sendApplyFlagSettings above. This keeps a combined change
	// all-or-nothing: if the permission-mode apply fails and returns false for a restart, no
	// half-applied model/effort is broadcast (or folded into a.model/a.effort) first. The flag
	// settings were already pushed to the CLI; deferring only their read-back/broadcast leaves the
	// in-memory state consistent on the failure path, and the restart supersedes the live apply.
	var observedSettings map[string]struct{}
	if len(flagSettings) > 0 {
		observedSettings = a.refreshSettingsFromAgent(a.APITimeout())
	}

	if len(flagSettings) > 0 {
		unresolved := make([]string, 0, len(changedFlagOptionIDs))
		for _, id := range changedFlagOptionIDs {
			if _, observed := observedSettings[id]; !observed {
				unresolved = append(unresolved, id)
			}
		}
		a.markSettingsUnresolved(unresolved...)
	}
	result := a.SettingsSnapshot()
	if !permissionConfirmed {
		result.Settlements[agent.OptionIDPermissionMode] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
	}
	return result
}

var claudeFlagOptionIDs = []string{
	agent.OptionIDModel,
	agent.OptionIDEffort,
	OptionOutputStyle,
	OptionFastMode,
	OptionAlwaysThinking,
}

func (a *Agent) markSettingsUnresolved(ids ...string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.unresolvedSettings == nil {
		a.unresolvedSettings = make(map[string]struct{})
	}
	for _, id := range ids {
		a.unresolvedSettings[id] = struct{}{}
	}
}

func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	result := agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
	a.Mu.Lock()
	unresolved := make([]string, 0, len(a.unresolvedSettings))
	for id := range a.unresolvedSettings {
		unresolved = append(unresolved, id)
	}
	permissionPending := a.deferredPermissionModeReqID != ""
	a.Mu.Unlock()
	for _, id := range unresolved {
		result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
	}
	if permissionPending {
		result.Settlements[agent.OptionIDPermissionMode] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
	}
	return result
}

// settlePermissionMode adopts the mode the CLI acknowledged and drops the deferred
// ack, in ONE critical section. The caller holds no lock.
//
// An acknowledged set_permission_mode SETTLES the axis. It supersedes two requests that
// record their own request id as the deferred ack: an older toggle, and the startup auto
// probe that timed out. An id that outlives its request costs two things.
//
//   - SettingsSnapshot reports the axis UNRESOLVED while an id is set, and
//     applyPlanOptionsLocked then refuses every plan that specifies the permission mode.
//   - claudeCodeHandleControlResponse folds the mode of the MATCHING ack back into the
//     confirmed state. A late ack of a superseded request would therefore replace the
//     mode this session runs with the mode that request asked for.
//
// A mode that LANDED on auto proves the session can enter it, so this also clears a
// stale autoModeAvailable=false that a transient startup probe failure left behind.
// Without that, OptionGroups keeps filtering "auto" out of the picker although the
// session runs it. (livePermissionModeGroup still keeps the current value selectable as
// a backstop, but the flag then states the catalog accurately instead of only
// self-correcting.)
func (a *Agent) settlePermissionMode(mode string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	a.confirmedPermissionMode = mode
	a.deferredPermissionModeReqID = ""
	if mode == contracts.ClaudeModeAuto {
		a.autoModeAvailable = true
	}
}

// applyPermissionModeLive applies a permission-mode change to the running CLI via
// set_permission_mode. The first result reports whether the caller can keep the process. The
// second result reports whether the command acknowledged the value. The caller holds no lock.
//
// The control request is capped well below APITimeout: idle, the CLI acks set_permission_mode in
// well under a second, but while a turn is streaming it defers the ack until the turn ends, so
// holding the caller (and the per-agent lifecycle lock) for the full APITimeout and then restarting
// would needlessly kill the in-flight turn.
func (a *Agent) applyPermissionModeLive(mode string) (applied, confirmed bool) {
	resp, err := a.sendSetPermissionMode(a.Context(), mode, min(permissionModeApplyTimeout, a.APITimeout()))
	switch {
	case err == nil:
		a.settlePermissionMode(resp.Mode)
		confirmed = true
	case errors.Is(err, errControlTimeout):
		// The ack is deferred because a turn is in progress, but the CLI still queues
		// and applies the mode. Treat it as accepted-pending: record the requested mode
		// optimistically and return true so the caller persists it WITHOUT restarting
		// (which would abort the turn) or rolling the optimistic UI back. When the turn
		// ends the CLI sends the deferred control_response, which claudeCodeHandleControlResponse
		// folds back -- reconciling confirmedPermissionMode (and the persisted row) to the
		// mode the CLI actually applied if it differs from this optimistic value.
		slog.Info("set_permission_mode ack deferred (turn in progress); applying optimistically",
			"agent_id", a.AgentID(), "mode", mode)
		a.Mu.Lock()
		a.confirmedPermissionMode = mode
		a.Mu.Unlock()
	default:
		slog.Error("set_permission_mode failed", "agent_id", a.AgentID(), "mode", mode, "error", err)
		return false, false
	}
	return true, confirmed
}

// refreshSettingsFromAgent sends get_settings and updates internal state with
// the actual applied values from Claude Code.
func (a *Agent) refreshSettingsFromAgent(timeout time.Duration) map[string]struct{} {
	resp, err := a.sendControlAndWait(a.Context(), `{"subtype":"get_settings"}`, timeout)
	if err != nil {
		slog.Warn("get_settings failed", "agent_id", a.AgentID(), "error", err)
		return nil
	}
	if len(resp.RawResponse) == 0 {
		return nil
	}

	var settings struct {
		Effective *struct {
			OutputStyle           string `json:"outputStyle"`
			FastMode              *bool  `json:"fastMode"`
			AlwaysThinkingEnabled *bool  `json:"alwaysThinkingEnabled"`
		} `json:"effective"`
		Applied *struct {
			Model     string  `json:"model"`
			Effort    *string `json:"effort"`
			Ultracode *bool   `json:"ultracode"`
		} `json:"applied"`
	}
	if err := json.Unmarshal(resp.RawResponse, &settings); err != nil {
		slog.Warn("get_settings response parse failed", "agent_id", a.AgentID(), "error", err)
		return nil
	}
	observed := make(map[string]struct{}, len(claudeFlagOptionIDs))
	if settings.Applied != nil {
		if settings.Applied.Effort != nil || settings.Applied.Ultracode != nil {
			observed[agent.OptionIDEffort] = struct{}{}
		}
		if settings.Applied.Model != "" {
			observed[agent.OptionIDModel] = struct{}{}
		}
	}
	if settings.Effective != nil {
		observed[OptionFastMode] = struct{}{}
		observed[OptionAlwaysThinking] = struct{}{}
		if settings.Effective.OutputStyle != "" {
			observed[OptionOutputStyle] = struct{}{}
		}
	}

	a.Mu.Lock()
	for id := range observed {
		delete(a.unresolvedSettings, id)
	}
	if settings.Applied != nil && settings.Applied.Model != "" {
		// Settle a.model onto the concrete model the CLI resolved. Once the concrete
		// identity is known it -- not the "default" sentinel -- is what the tab selects,
		// persists, broadcasts, and resolves effort/window against: the sentinel is only
		// the pre-resolution placeholder and never reclaims precedence once a concrete
		// model exists (so a relaunch pins the concrete model rather than re-resolving
		// the account default -- intended). ensureSettledModelListed then surfaces this
		// model in the picker when the CLI's selectable list omitted it.
		a.model = normalizeClaudeCodeModel(settings.Applied.Model)
	} else if a.model == agent.DefaultModelSentinel {
		// The account-default sentinel launches without --model and relies on
		// get_settings echoing the concrete model the CLI resolved. Claude Code 2.1.x
		// always populates applied.model with a concrete id -- get_settings computes it
		// eagerly via getMainLoopModel(), which resolves the account default and never
		// returns empty or the literal "default" (verified against the 2.1.170 binary).
		// So this branch is defense-in-depth against a malformed/forward-incompatible
		// response: an empty applied.model would otherwise strand a.model on the literal
		// "default" and leak it into persistence, the broadcast, and the settings-changed
		// notification. We can't synthesize the concrete model, so surface it.
		slog.Warn("get_settings omitted applied.model for the account-default launch; model stays unresolved",
			"agent_id", a.AgentID())
	}
	// a.model is updated just above from applied.model, so the ultracode/model
	// gate inside effortFromApplied sees the model the CLI actually applied.
	// effortResolver reads a.availableModels lock-free, so it is safe to call here
	// while a.Mu is held.
	if _, effortObserved := observed[agent.OptionIDEffort]; effortObserved {
		a.effort = a.effortResolver().effortFromApplied(settings.Applied.Effort, settings.Applied.Ultracode, a.effort, a.model)
	}
	if settings.Effective != nil && settings.Effective.OutputStyle != "" {
		a.outputStyle = settings.Effective.OutputStyle
	}
	// get_settings' `effective` is the CLI's MERGED settings map (verified by
	// disassembling the 2.1.170 binary: getSettings spreads PU().settings).
	// apply_flag_settings DELETES a key when sent null, so a flag cleared to its
	// default is ABSENT from `effective` and decodes here as a nil *bool. A nil thus
	// means "at the CLI default", NOT "unchanged": settle the field on that concrete
	// default rather than leaving the previous value stale. Otherwise turning Fast
	// Mode off (flagSettingOnOff sends null) or Extended Thinking on
	// (flagSettingThinking sends null) strands a.fastMode/a.alwaysThinking on the
	// prior setting, which then persists -- desyncing the settings-changed baseline
	// from the running session, so a later toggle compares against the wrong stored
	// value and its notification silently no-ops or shows a reversed transition.
	if settings.Effective != nil && settings.Effective.FastMode != nil && *settings.Effective.FastMode {
		a.fastMode = FastModeOn
	} else if settings.Effective != nil {
		a.fastMode = FastModeOff // nil == cleared to the CLI default (off)
	}
	if settings.Effective != nil && settings.Effective.AlwaysThinkingEnabled != nil && !*settings.Effective.AlwaysThinkingEnabled {
		a.alwaysThinking = AlwaysThinkingOff
	} else if settings.Effective != nil {
		a.alwaysThinking = AlwaysThinkingOn // nil == cleared to the CLI default (on)
	}
	model, effort := a.model, a.effort
	outputStyle, fastMode, thinking := a.outputStyle, a.fastMode, a.alwaysThinking
	a.Mu.Unlock()

	slog.Info("agent settings refreshed",
		"agent_id", a.AgentID(),
		"model", model,
		"effort", effort,
		"outputStyle", outputStyle,
		"fastMode", fastMode,
		"alwaysThinking", thinking,
	)

	// get_settings does not report permission mode, so OMIT it from the refresh
	// map: an absent key preserves the stored DB value, including startup-time raw
	// set_permission_mode changes that are applied again after startup. model/
	// effort/outputStyle/fastMode/thinking are all concrete here, so they upsert.
	a.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:  model,
		agent.OptionIDEffort: effort,
		OptionOutputStyle:    outputStyle,
		OptionFastMode:       fastMode,
		OptionAlwaysThinking: thinking,
	})
	return observed
}

// flagSettingOnOff maps an "on"/"off" string to a boolean flag setting value
// for apply_flag_settings. "on" → true, anything else → nil (which resets
// the flag to its default).
func flagSettingOnOff(v string) interface{} {
	if v == FastModeOn {
		return true
	}
	return nil
}

// flagSettingThinking maps an "on"/"off" string to the alwaysThinkingEnabled
// flag value for apply_flag_settings. "off" → false (thinking disabled).
// Anything else returns nil, which removes the key from flagSettings and
// lets Claude Code fall back to its default-on behavior — internally picking
// type:"adaptive" or type:"enabled" per its own model gate.
func flagSettingThinking(v string) interface{} {
	if v == AlwaysThinkingOff {
		return false
	}
	return nil
}
