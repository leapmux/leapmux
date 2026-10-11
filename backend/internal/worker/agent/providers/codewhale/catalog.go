package codewhale

import (
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Codewhale's model catalog. The runtime lists the models of ONE provider at a
// time, and a thread runs on one provider, so the catalog is the thread
// provider's. A model id is the provider's own wire id, with no provider
// prefix.

// Capability states the catalog reports for a model's image input and
// reasoning effort.
const (
	capabilitySupported   = "supported"
	capabilityUnsupported = "unsupported"
)

// codewhaleEffortVocabulary is the runtime's documented effort vocabulary, for
// a model whose catalog entry lists no levels of its own. The runtime maps each
// value onto what the model's route supports, so every one is safe to send.
var codewhaleEffortVocabulary = []string{"off", "low", "medium", "high", "xhigh", "ultra", "max"}

// codewhaleAutoEffort is LeapMux's sentinel for "send no effort", which leaves
// the runtime on its own default for the model.
var codewhaleAutoEffort = &agent.EffortInfo{
	Id:          agent.EffortAuto,
	Name:        providerkit.EffortLabel(agent.EffortAuto),
	Description: "Use Codewhale's default reasoning effort for the model",
}

// codewhaleModelInfo converts one catalog entry.
//
// A model whose effort capability is `unsupported` gets no effort axis. A model
// that lists its levels gets those. Any other model -- the runtime reports
// `unknown` for most routes -- gets the documented vocabulary.
func codewhaleModelInfo(model providerModel, defaultModel string) *agent.ModelInfo {
	info := &agent.ModelInfo{
		Id:          model.ID,
		DisplayName: model.ID,
		IsDefault:   model.ID == defaultModel,
	}
	if model.ReasoningEffort == capabilityUnsupported {
		return info
	}
	levels := model.ReasoningEffortLevels
	if len(levels) == 0 {
		levels = codewhaleEffortVocabulary
	}
	efforts := make([]*agent.EffortInfo, 0, len(levels))
	seen := make(map[string]bool, len(levels))
	for _, level := range levels {
		level = strings.TrimSpace(level)
		if level == "" || level == agent.EffortAuto || seen[level] {
			continue
		}
		seen[level] = true
		efforts = append(efforts, providerkit.EffortTier(level))
	}
	providerkit.SortEffortsDescending(efforts)
	info.SupportedEfforts = append([]*agent.EffortInfo{codewhaleAutoEffort}, efforts...)
	info.DefaultEffort = agent.EffortAuto
	return info
}

// imageInputSupport is how far the current model takes an image.
type imageInputSupport int

const (
	// imageInputUnknown lets the runtime decide, and its refusal reaches the reader.
	imageInputUnknown imageInputSupport = iota
	imageInputSupported
	imageInputUnsupported
)

// currentImageInputLocked reads the current model's image capability from the
// catalog. The caller holds Mu.
func (a *Agent) currentImageInputLocked() imageInputSupport {
	for _, model := range a.catalog {
		if model.ID != a.settings.model {
			continue
		}
		switch model.ImageInput {
		case capabilitySupported:
			return imageInputSupported
		case capabilityUnsupported:
			return imageInputUnsupported
		}
	}
	return imageInputUnknown
}

// refreshModelCatalog reads the thread provider's catalog and publishes the
// settings it changes. A failure keeps the catalog the agent already has.
func (a *Agent) refreshModelCatalog() {
	a.Mu.Lock()
	provider, providerID := a.settings.provider, a.settings.providerID
	a.Mu.Unlock()
	if provider == "" {
		return
	}
	models, err := a.listProviderModels(provider, providerID)
	if err != nil {
		slog.Warn("codewhale read the model catalog", "agent_id", a.AgentID(), "provider", provider, "error", err)
		return
	}
	a.applyModelCatalog(models)
	a.sink.PersistSettingsRefresh(agent.CurrentOptions(a.OptionGroups()))
}

// applyModelCatalog replaces the catalog. The model the runtime chose for a
// thread opened with no model carries the default badge; the catalog itself
// marks none.
//
// A model the thread runs that the catalog does not list -- a custom route's
// model, or one the provider retired -- is kept as an entry of its own, so the
// current value stays selectable and still resolves its effort tiers.
func (a *Agent) applyModelCatalog(models []providerModel) {
	a.Mu.Lock()
	current, defaultModel := a.settings.model, a.settings.defaultModel
	a.Mu.Unlock()
	infos := make([]*agent.ModelInfo, 0, len(models)+1)
	listed := false
	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		if model.ID == current {
			listed = true
		}
		infos = append(infos, codewhaleModelInfo(model, defaultModel))
	}
	if !listed && current != "" {
		infos = append(infos, codewhaleModelInfo(providerModel{ID: current}, defaultModel))
	}
	a.Mu.Lock()
	a.catalog = models
	a.models = infos
	a.Mu.Unlock()
}
