package pi

import (
	"encoding/json"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Pi default option values.
//
// The provider and the model name a pair that Pi's own catalog carries, and
// they are sent together by applyModel as {provider, modelId}. A pair Pi does
// not know fails set_model at startup ("Model not found: <provider>/<model>"),
// and the agent then answers nothing at all -- so this is a launch default,
// not a cosmetic label. The previous openai-codex/gpt-5.5 pair named neither a
// provider nor a model that `pi --list-models` reports.
const (
	DefaultThinkingLevel = "medium"
	DefaultProvider      = "zai"
	DefaultModel         = "glm-5.3"
)

// ThinkingLevelLabel is Pi's display label for its effort axis: Pi's CLI exposes a
// "thinking level" (set_thinking_level), not a generic reasoning "effort", so the
// settings popover names it accordingly (matching the pre-unification Pi panel).
const ThinkingLevelLabel = "Thinking Level"

// Pi option keys persisted in the agents table.
const (
	// OptionProvider stores the underlying LLM provider name (e.g.
	// "openai-codex", "anthropic") so model switches can be sent with the
	// correct {provider, modelId} pair via Pi's set_model RPC.
	OptionProvider = "pi_provider"
)

// Pi thinking-level values. These match Pi's set_thinking_level wire values
// and are stored as the agent's `effort`.
const (
	ThinkingOff     = "off"
	ThinkingMinimal = "minimal"
	ThinkingLow     = "low"
	ThinkingMedium  = "medium"
	ThinkingHigh    = "high"
	ThinkingXHigh   = "xhigh"
)

// piAutoEffort is the LeapMux-side sentinel: when selected we omit the
// set_thinking_level RPC and let Pi keep its current level (typically driven
// by ~/.pi/agent/settings.json).
var piAutoEffort = &agent.EffortInfo{
	Id: agent.EffortAuto, Name: providerkit.EffortLabel(agent.EffortAuto), Description: "Use Pi's configured default thinking level",
}

// piDefaultEfforts is the static fallback list of thinking levels surfaced to
// the UI before get_available_models populates per-model SupportedEfforts.
var piDefaultEfforts = []*agent.EffortInfo{
	piAutoEffort,
	providerkit.EffortTier(ThinkingXHigh),
	providerkit.EffortTier(ThinkingHigh),
	providerkit.EffortTier(ThinkingMedium),
	providerkit.EffortTier(ThinkingLow),
	providerkit.EffortTier(ThinkingMinimal),
	providerkit.EffortTier(ThinkingOff),
}

// piNonReasoningEfforts is the trimmed effort list for models that don't
// support reasoning — only Auto and Off make sense.
var piNonReasoningEfforts = []*agent.EffortInfo{
	piAutoEffort,
	providerkit.EffortTier(ThinkingOff),
}

// piDefaultModels is the static fallback model list used until the Pi process
// answers get_available_models. The single entry mirrors the user's configured
// default; the runtime catalog supersedes this.
var piDefaultModels = []*agent.ModelInfo{
	{
		Id:               DefaultModel,
		DisplayName:      "GLM-5.3",
		Description:      "Default Pi model (overridden once Pi reports its catalog)",
		IsDefault:        true,
		DefaultEffort:    DefaultThinkingLevel,
		SupportedEfforts: piDefaultEfforts,
	},
}

// applyAvailableModels parses a get_available_models response into the
// AvailableModel proto shape and stores it for the manager.
func (a *Agent) applyAvailableModels(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var resp struct {
		Models []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Provider      string `json:"provider"`
			Reasoning     bool   `json:"reasoning"`
			ContextWindow int64  `json:"contextWindow"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		slog.Warn("pi get_available_models unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}

	models := make([]*agent.ModelInfo, 0, len(resp.Models))
	providers := make(map[string]string, len(resp.Models))
	for _, m := range resp.Models {
		if m.ID == "" {
			continue
		}
		display := m.Name
		if display == "" {
			display = m.ID
		}
		efforts := piDefaultEfforts
		if !m.Reasoning {
			// Models without reasoning support only `off`; still expose Auto.
			efforts = piNonReasoningEfforts
		}
		if m.Provider != "" {
			providers[m.ID] = m.Provider
		}
		models = append(models, &agent.ModelInfo{
			Id:               m.ID,
			DisplayName:      display,
			DefaultEffort:    DefaultThinkingLevel,
			SupportedEfforts: efforts,
			ContextWindow:    m.ContextWindow,
		})
	}

	// A response that parsed but yielded no usable model (empty list, or every entry missing an
	// id) carries no information -- like the len(raw) == 0 case above -- so leave the catalog
	// untouched rather than overwriting it with an empty list, which would blank the model picker
	// until the next non-empty response. (Today the manager's static-fallback chain backstops an
	// empty a.availableModels, but keeping the guard local makes the intent self-evident.)
	if len(models) == 0 {
		return
	}

	a.Mu.Lock()
	a.availableModels = models
	a.modelProviders = providers
	a.Mu.Unlock()
}

// providerForModel returns the underlying provider for a model id, looking it
// up in the available-models catalog. Falls back to the agent's current
// provider, then to the Pi default. Caller does not need to hold a.Mu.
func (a *Agent) providerForModel(modelID string) string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if p := a.modelProviders[modelID]; p != "" {
		return p
	}
	if a.provider != "" {
		return a.provider
	}
	return DefaultProvider
}

// applyModel sends set_model, then reads Pi's own state back so the local
// model, provider and thinking level hold what Pi settled on.
//
// The request is not the answer. set_model picks the thinking level for the new
// model by itself, and a Pi build that resolves an alias or refuses a provider
// settles on a model this caller did not name. get_state is the ONE route to
// that answer: Pi has no `model_changed` event, and the `model_change` session
// entry it appends reaches no `entry_appended` event either (see
// handlePiModelChangeEntry). Recording the request instead left the model
// segment showing a model the running agent had already left.
//
// The requested pair goes in FIRST, so a failed state read still moves off the
// prior model. set_model already succeeded, so the prior model is certainly
// wrong and the requested one is the best answer left.
func (a *Agent) applyModel(modelID, providerID string, timeout time.Duration) error {
	if providerID == "" {
		providerID = a.providerForModel(modelID)
	}
	params := map[string]any{"provider": providerID, "modelId": modelID}
	if _, err := a.sendPiCommand(CommandSetModel, params, timeout); err != nil {
		return err
	}
	a.Mu.Lock()
	a.model = modelID
	a.provider = providerID
	a.Mu.Unlock()
	stateRaw, err := a.sendPiCommand(CommandGetState, nil, timeout)
	if err != nil {
		slog.Warn("pi get_state after set_model failed; keeping the requested model",
			"agent_id", a.AgentID(), "model", modelID, "error", err)
		return nil
	}
	a.applyStateResponse(stateRaw)
	return nil
}

// applyThinkingLevel sends set_thinking_level and updates local state.
func (a *Agent) applyThinkingLevel(level string, timeout time.Duration) error {
	params := map[string]any{"level": level}
	if _, err := a.sendPiCommand(CommandSetThinkingLevel, params, timeout); err != nil {
		return err
	}
	a.Mu.Lock()
	a.thinkingLevel = level
	a.Mu.Unlock()
	return nil
}

// OptionGroups returns the model group and the thinking-level (effort) group
// for the current model. Pi exposes its underlying provider (OptionProvider)
// only as a persisted option, not a visible group.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	model, effort := a.model, a.thinkingLevel
	models := a.availableModels
	a.Mu.Unlock()

	return providerkit.ModelAndEffortGroups(models, model, effort, ThinkingLevelLabel, nil)
}

// UpdateSettings applies model, thinking-level, and provider changes live so
// the next prompt picks them up without a restart.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.Mu.Lock()
	curEffort := a.thinkingLevel
	curModel := a.model
	curProvider := a.provider
	a.Mu.Unlock()

	// Switching effort to "auto" is a LeapMux sentinel that means "let Pi
	// pick its own default" — the wire protocol has no equivalent, so a
	// restart is required (return false to signal the caller to restart).
	if agent.IsEffortAutoTransition(options[agent.OptionIDEffort], curEffort) {
		return agent.RestartRequiredSettings(options)
	}

	timeout := a.APITimeout()

	// A live apply that fails leaves the agent on its prior value. Reporting success
	// anyway (return true) would strand the UI on the requested value while the running
	// agent keeps the old one, with no error surfaced. Instead signal a restart (return
	// false): the caller relaunches with the requested settings as launch options, so the
	// change actually takes effect rather than being silently dropped.
	applied := true
	if model := options[agent.OptionIDModel]; model != "" && model != curModel {
		providerID := curProvider
		if v := options[OptionProvider]; v != "" {
			providerID = v
		}
		if err := a.applyModel(model, providerID, timeout); err != nil {
			slog.Warn("pi UpdateSettings set_model failed; restarting to apply", "agent_id", a.AgentID(), "model", model, "error", err)
			applied = false
		}
	} else if v := options[OptionProvider]; v != "" && v != curProvider {
		// Agent changed without a model change — re-send set_model so Pi
		// switches to the new provider's instance of the same model id.
		if err := a.applyModel(curModel, v, timeout); err != nil {
			slog.Warn("pi UpdateSettings provider switch failed; restarting to apply", "agent_id", a.AgentID(), "provider", v, "error", err)
			applied = false
		}
	}

	if effort := options[agent.OptionIDEffort]; effort != "" && effort != agent.EffortAuto && effort != curEffort {
		if err := a.applyThinkingLevel(effort, timeout); err != nil {
			slog.Warn("pi UpdateSettings set_thinking_level failed; restarting to apply", "agent_id", a.AgentID(), "level", effort, "error", err)
			applied = false
		}
	}

	if !applied {
		// A partial live apply (e.g. set_model landed but set_thinking_level was rejected) leaves
		// a.model/a.provider/a.thinkingLevel a half-applied mix -- applyModel/applyThinkingLevel each
		// mutate on their own success. Restore the captured pre-change values so no inconsistent pair
		// is observable (OptionGroups, a status read) in the window before the caller's restart, which
		// then applies the full requested settings atomically as launch options.
		a.Mu.Lock()
		a.model, a.thinkingLevel, a.provider = curModel, curEffort, curProvider
		a.Mu.Unlock()
		return agent.RestartRequiredSettings(options)
	}

	a.Mu.Lock()
	model, eff, prov := a.model, a.thinkingLevel, a.provider
	a.Mu.Unlock()
	// Pi has no permission-mode axis, so omit it (preserves any stored value).
	a.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:  model,
		agent.OptionIDEffort: eff,
		OptionProvider:       prov,
	})
	return a.SettingsSnapshot()
}

func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	result := agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
	a.Mu.Lock()
	provider := a.provider
	a.Mu.Unlock()
	result.Settlements[OptionProvider] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &provider}
	return result
}
