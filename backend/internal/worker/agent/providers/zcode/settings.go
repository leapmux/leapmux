package zcode

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Errors the setters return for a request the catalog or the session cannot satisfy.
var errNoZCodeSession = errors.New("agent has no ZCode session")

// newInvalidZCodeModelError reports a model id the catalog does not hold. The
// message gives the id because the usual cause is a stale model selection.
func newInvalidZCodeModelError(modelID string) error {
	return fmt.Errorf("ZCode's configuration lists no model %q", modelID)
}

// ZCode exposes three settable axes, and every one of them is applied through a
// session RPC that returns the app-server's own state snapshot. So each setter
// reports the OBSERVED value, never the requested one: the app-server clamps a
// thought level a model does not offer and refuses a mode it does not know, and a
// UI showing the request rather than the result would be lying about what the next
// turn will do.
//
// The plan flag is the one exception, because no snapshot carries it -- see
// recordPlanRequestLocked for the evidence that replaces the snapshot there.

// ThoughtLevelLabel is ZCode's display label for its effort axis. The
// app-server calls it a "thought level" (session/setThoughtLevel), not a generic
// reasoning effort, so the settings popover uses the same label.
const ThoughtLevelLabel = "Thought Level"

// ModeLabel labels the mode axis, which LeapMux carries on its permission-mode
// channel so the plan-mode toggle and the mode chip drive it.
const ModeLabel = "Mode"

// --- the plan flag ---
//
// ZCode keeps plan mode in a `planEnabled` flag beside the native mode (build, edit,
// yolo or auto). Its mode requests -- the `mode` of session/create and of
// session/setMode -- still accept `plan`, and the runtime folds every request by one
// rule (resolveExecutionState in the app-server bundle): `plan` turns the flag on and
// keeps the native mode, and any other mode replaces the native mode and turns the
// flag off. EnterPlanMode and ExitPlanMode change the flag alone.
//
// LeapMux's permission-mode axis carries plan mode as its `plan` value, as Kimi Code
// does: the plan toggle and the plan approval read one axis for every provider. So
// `plan` means "the flag is on", and every other value means "the flag is off, in
// this native mode".

// permissionModeLocked returns the value of LeapMux's permission-mode axis.
//
// A build that kept plan mode as a native mode reports `plan` in
// `settings.mode.current`, and that value passes through unchanged.
//
// Caller holds a.Mu.
func (a *Agent) permissionModeLocked() string {
	if a.planEnabled {
		return contracts.ZCodeModePlan
	}
	return a.mode
}

// pinPermissionModeLocked stores the launch request before a session exists.
// It splits the value as ZCode's runtime splits the request. It is not an
// observation, so it leaves planObserved alone.
//
// Caller holds a.Mu.
func (a *Agent) pinPermissionModeLocked(value string) {
	if value == contracts.ZCodeModePlan {
		a.planEnabled = true
		return
	}
	a.mode = value
	a.planEnabled = false
}

// recordPlanRequestLocked records the plan flag that an ACCEPTED mode request left
// the session in.
//
// The reply of session/create and session/setMode carries `settings.mode.current`,
// the native mode, and no flag. The event that states the flag is a separate frame,
// and nothing promises that it arrives before the reply. ZCode's fold of the request
// is fixed, so the request itself is the evidence: `plan` turns the flag on, and any
// other mode turns it off. The native mode stays with the reply's snapshot.
//
// Caller holds a.Mu.
func (a *Agent) recordPlanRequestLocked(requested string) {
	a.planEnabled = requested == contracts.ZCodeModePlan
	a.planObserved = true
}

// handleZCodeModeChanged folds ZCode's SessionModeChanged event, which states the
// native mode and the plan flag together. It is the only frame that reports a plan
// flag that the AGENT changed: EnterPlanMode, and ExitPlanMode after an approval.
//
// A change of the axis is persisted, so a plan mode that the agent entered or left
// survives a restart.
//
// The result is the stored plan-exit mode that this event releases, or empty. The
// caller sends it, because the request needs a goroutine of its own.
func (a *Agent) handleZCodeModeChanged(mode string, planEnabled bool, source string) (exitMode string) {
	defer a.queuePermissionApprovals()
	a.Mu.Lock()
	planBefore := a.planEnabled
	before := a.permissionModeLocked()
	a.mode = mode
	a.modeObserved = true
	a.planEnabled = planEnabled
	a.planObserved = true
	a.modeChanges++
	// Every mode event drops a stored plan-exit mode, and only the exit itself
	// releases it: an event of a plan tool that turns the flag OFF. A flag-off
	// event of another source, such as the request of the user to leave plan
	// mode, is a later choice that the stored mode must not override. The same
	// native mode needs no request. Entering plan mode again drops the mode
	// too: that session chose to keep planning, and a stale exit mode must not
	// fire at a later exit.
	stored := a.pendingExitMode
	a.pendingExitMode = ""
	sendStored := planBefore && !planEnabled && source == zcodeModeSourceTool && stored != "" && stored != mode
	// The event states both halves of the axis, so it settles a setter reply that
	// carried no mode.
	delete(a.unresolvedSettings, agent.OptionIDPermissionMode)
	after := a.permissionModeLocked()
	a.Mu.Unlock()
	if sendStored {
		exitMode = stored
	}
	if before == after && !sendStored {
		return exitMode
	}
	a.sink.PersistSettingsRefresh(map[string]string{agent.OptionIDPermissionMode: after})
	return exitMode
}

// zcodeSettingsRequest is the trio a caller ASKS a session to run on: the launch
// options at startup, and the current axes at a context clear.
//
// It exists because opening a session folds the app-server's own settings over the
// agent's mirror, so the request is unreadable from those fields afterwards. The caller
// captures it before, and applyStartupSettings applies it. An empty field means "the
// caller asked for nothing here", and the observed value stands.
type zcodeSettingsRequest struct {
	Model        string
	ThoughtLevel string
	Mode         string
}

// --- the app-server's settings snapshot ---

// zcodeThoughtLevelOption is one selectable thought level.
type zcodeThoughtLevelOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// zcodeAvailableModel is one live model option in the settings snapshot. The
// legacy fields stay for older builds. ZCode 0.16.9 nests media support under
// properties.inputFormat and states every model's reasoning levels here.
type zcodeAvailableModel struct {
	Ref            zcodeModelRef `json:"ref"`
	Label          string        `json:"label"`
	ProviderLabel  string        `json:"providerLabel"`
	Description    string        `json:"description"`
	ContextWindow  int64         `json:"contextWindow"`
	SupportsImages bool          `json:"supportsImages"`
	SupportsPdf    bool          `json:"supportsPdf"`
	DisabledReason string        `json:"disabledReason"`
	Reasoning      *struct {
		Levels       []zcodeThoughtLevelOption `json:"levels"`
		DefaultLevel string                    `json:"defaultLevel"`
	} `json:"reasoning"`
	Properties *struct {
		InputFormat struct {
			SupportsText  bool `json:"supportsText"`
			SupportsImage bool `json:"supportsImage"`
			SupportsVideo bool `json:"supportsVideo"`
			SupportsAudio bool `json:"supportsAudio"`
			SupportsPdf   bool `json:"supportsPdf"`
		} `json:"inputFormat"`
	} `json:"properties"`
}

type zcodeLiveModelRecord struct {
	info       *agent.ModelInfo
	ref        zcodeModelRef
	modalities []string
}

// zcodeSettingsSnapshot is the `settings` object every state-changing session RPC
// returns, and that the top-level state.updated notification patches.
//
// Each axis is a POINTER, because a state.updated patch carries only the axes that
// changed -- an absent axis must leave the agent's value alone, which a value type
// could not express.
type zcodeSettingsSnapshot struct {
	// AppliedProviderRevision is the legacy registry revision that supplied the
	// session credential.
	AppliedProviderRevision string `json:"appliedProviderRevision"`
	Mode                    *struct {
		// Current is the AUTHORITATIVE native mode. `session.mode` is the
		// creation-time record and never tracks a switch, so it is deliberately not
		// read. Current does not state plan mode: a session in plan mode reports its
		// native mode here, and no snapshot carries the plan flag (see
		// Agent.planEnabled).
		Current string `json:"current"`
	} `json:"mode"`
	Model *struct {
		Current   zcodeModelRef         `json:"current"`
		Available []zcodeAvailableModel `json:"available"`
		LastUsed  *zcodeModelRef        `json:"lastUsed"`
	} `json:"model"`
	ThoughtLevel *struct {
		Enabled      bool                      `json:"enabled"`
		Current      string                    `json:"current"`
		DefaultLevel string                    `json:"defaultLevel"`
		Available    []zcodeThoughtLevelOption `json:"available"`
	} `json:"thoughtLevel"`
}

// applySettingsSnapshotLocked folds a settings snapshot into the agent's state.
//
// Caller holds a.Mu.
//
// Every value read here is the app-server's own: `mode.current`, `model.current`
// and `thoughtLevel.current`. Where a field is absent the previous value stands,
// which is what makes this safe to call with a partial state.updated patch. The plan
// flag is not in the snapshot, so it stands also: the patch that ends a turn in plan
// mode states `build` and leaves plan mode on.
func (a *Agent) applySettingsSnapshotLocked(snap *zcodeSettingsSnapshot) {
	if snap == nil {
		return
	}
	if snap.Mode != nil && snap.Mode.Current != "" {
		a.mode = snap.Mode.Current
		// applyStartupSettings skips the mode setter for a session that already runs in
		// the requested mode, and this is the only place that evidence comes from.
		a.modeObserved = true
	}
	if m := snap.Model; m != nil {
		a.mergeZCodeLiveModelsLocked(m.Available, m.Current)
		if m.Current.ModelID != "" {
			a.model = zcodeModelID(m.Current.ProviderID, m.Current.ModelID)
		} else if ref := m.LastUsed; ref != nil && ref.ModelID != "" && a.model == "" {
			a.model = zcodeModelID(ref.ProviderID, ref.ModelID)
		}
	}
	if tl := snap.ThoughtLevel; tl != nil {
		// The model offers no thought level at all. Auto alone keeps the axis
		// selectable-but-inert rather than showing a level the model would refuse.
		//
		// Checked BEFORE the list is built, because this branch discards it: the
		// build, the sort and the prepend all ran under a.Mu for a result nothing
		// read. The `available` half cannot move up with it -- it counts the
		// non-empty values, which only the loop knows.
		offAxis := func() {
			a.observedThoughtLevels = []*agent.EffortInfo{zcodeAutoEffort}
			a.observedThoughtDefault = agent.EffortAuto
			a.thoughtLevel = agent.EffortAuto
		}
		if !tl.Enabled {
			offAxis()
			return
		}
		levels := make([]*agent.EffortInfo, 0, len(tl.Available))
		available := make(map[string]bool, len(tl.Available))
		for _, l := range tl.Available {
			if l.Value == "" {
				continue
			}
			available[l.Value] = true
			levels = append(levels, zcodeEffortTier(l.Value, l.Label, l.Description))
		}
		if len(available) == 0 {
			offAxis()
			return
		}
		// Auto first, then the levels strongest first, exactly as the configured
		// catalog orders them (zcodeModelInfo). The app-server reports the levels
		// in the order the model's own configuration lists them, which is the
		// order this replaces -- so without the same sort the menu reordered
		// itself the moment the agent reported its first snapshot.
		a.observedThoughtLevels = zcodeEffortsWithAuto(levels)
		a.observedThoughtDefault = tl.DefaultLevel
		if a.observedThoughtDefault == "" {
			a.observedThoughtDefault = agent.EffortAuto
		}
		switch {
		case tl.Current != "" && available[tl.Current]:
			a.thoughtLevel = tl.Current
		case a.thoughtLevel != "" && a.thoughtLevel != agent.EffortAuto && !available[a.thoughtLevel]:
			// The level was set for a different model, and this one does not offer it.
			// Auto is the honest report: no level is pinned.
			a.thoughtLevel = agent.EffortAuto
		}
	}
}

// mergeZCodeLiveModelsLocked folds a live model list into the catalog cache.
//
// ZCode's create snapshot lists all models. A later setModel response can list
// only the selected model, so an absent entry in a later response does not remove
// an entry that the running app server already offered.
func (a *Agent) mergeZCodeLiveModelsLocked(available []zcodeAvailableModel, current zcodeModelRef) {
	if len(available) == 0 {
		return
	}
	records := make(map[string]zcodeLiveModelRecord, len(a.liveModels)+len(available))
	for id, record := range a.liveModels {
		records[id] = record
	}
	order := make([]string, 0, len(available)+len(a.liveModelOrder))
	seen := make(map[string]struct{}, len(available))
	currentID := a.model
	if current.ModelID != "" {
		currentID = zcodeModelID(current.ProviderID, current.ModelID)
	}
	for _, wire := range available {
		if wire.Ref.ProviderID == "" || wire.Ref.ModelID == "" {
			continue
		}
		id := zcodeModelID(wire.Ref.ProviderID, wire.Ref.ModelID)
		if a.liveCatalogComplete {
			if _, exists := a.liveModels[id]; !exists {
				continue
			}
		}
		info := &agent.ModelInfo{
			Id:            id,
			DisplayName:   zcodeModelDisplayName(wire.Label, wire.ProviderLabel),
			Description:   wire.Description,
			ContextWindow: wire.ContextWindow,
			IsDefault:     id == currentID,
			Hidden:        wire.DisabledReason != "",
		}
		if info.DisplayName == "" {
			info.DisplayName = zcodeModelDisplayName(wire.Ref.ModelID, wire.ProviderLabel)
		}
		if wire.Reasoning != nil && len(wire.Reasoning.Levels) > 0 {
			levels := make([]*agent.EffortInfo, 0, len(wire.Reasoning.Levels))
			for _, level := range wire.Reasoning.Levels {
				if level.Value != "" {
					levels = append(levels, zcodeEffortTier(level.Value, level.Label, level.Description))
				}
			}
			if len(levels) > 0 {
				info.SupportedEfforts = zcodeEffortsWithAuto(levels)
				info.DefaultEffort = wire.Reasoning.DefaultLevel
				if info.DefaultEffort == "" {
					info.DefaultEffort = agent.EffortAuto
				}
			}
		}
		input := []string{zcodeModalityText}
		if wire.Properties != nil {
			input = input[:0]
			formats := wire.Properties.InputFormat
			if formats.SupportsText {
				input = append(input, zcodeModalityText)
			}
			if formats.SupportsImage {
				input = append(input, zcodeModalityImage)
			}
			if formats.SupportsPdf {
				input = append(input, zcodeModalityPDF)
			}
			if formats.SupportsVideo {
				input = append(input, "video")
			}
			if formats.SupportsAudio {
				input = append(input, "audio")
			}
		} else {
			if wire.SupportsImages {
				input = append(input, zcodeModalityImage)
			}
			if wire.SupportsPdf {
				input = append(input, zcodeModalityPDF)
			}
		}
		records[id] = zcodeLiveModelRecord{info: info, ref: wire.Ref, modalities: input}
		if _, duplicate := seen[id]; !duplicate {
			order = append(order, id)
			seen[id] = struct{}{}
		}
	}
	for _, id := range a.liveModelOrder {
		if _, replaced := seen[id]; replaced {
			continue
		}
		record, ok := records[id]
		if !ok || record.info == nil {
			continue
		}
		clone := *record.info
		clone.IsDefault = clone.Id == currentID
		record.info = &clone
		records[id] = record
		order = append(order, id)
	}
	a.liveModels = records
	a.liveModelOrder = order
}

// replaceZCodeLiveModelsLocked replaces membership from an unrestricted snapshot.
// Caller holds Mu. An explicit empty list is an authoritative empty catalog.
func (a *Agent) replaceZCodeLiveModelsLocked(available []zcodeAvailableModel, current zcodeModelRef) {
	a.liveModels = make(map[string]zcodeLiveModelRecord)
	a.liveModelOrder = nil
	a.liveCatalogComplete = false
	a.mergeZCodeLiveModelsLocked(available, current)
	a.liveCatalogComplete = true
}

// resolveZCodeModelIDLocked resolves a model against the live catalog first and
// then the legacy configuration. Caller holds a.Mu.
func (a *Agent) resolveZCodeModelIDLocked(model string) (string, bool) {
	model = normalizeZCodeModelID(model)
	if model == "" {
		return "", false
	}
	if a.account != nil {
		if providerID, modelID, ok := splitZCodeModelID(model); ok {
			if accountID, mapped := a.catalog.accountProviderID(providerID); mapped {
				model = zcodeModelID(accountID, modelID)
			}
		}
	}
	if _, ok := a.liveModels[model]; ok {
		return model, true
	}
	if _, _, composite := splitZCodeModelID(model); !composite {
		for _, id := range a.liveModelOrder {
			if _, modelID, ok := splitZCodeModelID(id); ok && modelID == model {
				return id, true
			}
		}
	}
	if a.account != nil && a.liveCatalogComplete {
		return "", false
	}
	return a.catalog.resolveModelID(model)
}

// zcodeDefaultThoughtLevelLocked returns the live model default when available.
// Caller holds a.Mu.
func (a *Agent) zcodeDefaultThoughtLevelLocked(modelID string) string {
	if record, ok := a.liveModels[modelID]; ok && record.info != nil {
		return record.info.DefaultEffort
	}
	return a.catalog.defaultThoughtLevel(modelID)
}

// zcodeThoughtLevelsLocked returns the thought levels that a model offers, or nil when nothing
// states them. The live catalog wins over the legacy configuration. Caller holds a.Mu.
func (a *Agent) zcodeThoughtLevelsLocked(modelID string) []*agent.EffortInfo {
	if record, ok := a.liveModels[modelID]; ok && record.info != nil {
		return record.info.SupportedEfforts
	}
	if info := agent.FindAvailableModel(a.catalog.Models, modelID); info != nil {
		return info.SupportedEfforts
	}
	return nil
}

// zcodeModelsForUI returns the live model catalog when the app server supplied
// one. It falls back to the legacy desktop configuration.
//
// The live thought-level axis covers only the running model. It replaces that
// model's levels because it is what the next setThoughtLevel request accepts.
func (a *Agent) zcodeModelsForUI() ([]*agent.ModelInfo, string, string) {
	a.Mu.Lock()
	current, level := a.model, a.thoughtLevel
	observed, observedDefault := a.observedThoughtLevels, a.observedThoughtDefault
	models := a.catalog.Models
	if len(a.liveModelOrder) > 0 || (a.account != nil && a.liveCatalogComplete) {
		models = make([]*agent.ModelInfo, 0, len(a.liveModelOrder))
		for _, id := range a.liveModelOrder {
			if record, ok := a.liveModels[id]; ok && record.info != nil {
				models = append(models, record.info)
			}
		}
	}
	a.Mu.Unlock()

	if len(observed) == 0 || current == "" {
		return models, current, level
	}
	out := make([]*agent.ModelInfo, 0, len(models))
	for _, m := range models {
		if m == nil {
			continue
		}
		if m.GetId() != current {
			out = append(out, m)
			continue
		}
		// A shallow copy: the catalog entry is shared with every other reader, so the
		// live override must not mutate it.
		clone := *m
		clone.SupportedEfforts = observed
		if observedDefault != "" {
			clone.DefaultEffort = observedDefault
		}
		out = append(out, &clone)
	}
	return out, current, level
}

// OptionGroups reports the model, thought-level and mode axes.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	models, model, effort := a.zcodeModelsForUI()
	groups := providerkit.ModelAndEffortGroups(models, model, effort, ThoughtLevelLabel, nil)

	a.Mu.Lock()
	mode := a.permissionModeLocked()
	a.Mu.Unlock()
	if g := providerkit.LiveGroup(optionids.GroupByID(zcodeStaticOptionGroups, agent.OptionIDPermissionMode), mode); g != nil {
		groups = append(groups, g)
	}
	return groups
}

// --- the setters ---

// resolveZCodeThoughtLevel reports the reasoning level a model switch must carry.
//
// The session's own level wins, when the new model offers it. An unset level and the
// automatic one both mean "the model decides", so both fall back to the model's declared
// default, which is itself empty for a model that declares none. A level that the new model
// does not offer falls back in the same way, because the app-server refuses a selection
// that carries such a level. LeapMux's Auto sentinel is no level, so it never reaches the wire.
//
// Both request shapes need this answer -- the account-config selection carries it
// in `options.reasoningLevel`, and the legacy overlay in `ThoughtLevel` -- and the
// two spelled it out separately until one of them dropped the level on a switch.
func resolveZCodeThoughtLevel(level, defaultLevel string, offered []*agent.EffortInfo) string {
	if level != "" && level != agent.EffortAuto && zcodeOffersLevel(offered, level) {
		return level
	}
	if defaultLevel == agent.EffortAuto {
		return ""
	}
	return defaultLevel
}

// zcodeOffersLevel reports whether the concrete levels in offered include level. A list that
// states no concrete level states nothing, so it refuses nothing.
func zcodeOffersLevel(offered []*agent.EffortInfo, level string) bool {
	concrete := false
	for _, tier := range offered {
		switch tier.GetId() {
		case "", agent.EffortAuto:
		case level:
			return true
		default:
			concrete = true
		}
	}
	return !concrete
}

// --- bounded reasoning-level recovery ---

// The two verified reasoning-level failure classes of the current app-server.
// Both arrive with data.code "invalid_model_request", and the message states
// which level the model refused: either no level was sent where one is required,
// or the level the selection carried is not one the model offers. The message
// matches one class exactly -- the provider and model spell the same composite
// the wire uses -- so a schema error that merely mentions reasoning does not
// qualify.
var (
	zcodeReasoningLevelRequiredRE     = regexp.MustCompile(`^Reasoning level is required for [^/]+/.+$`)
	zcodeReasoningEffortUnsupportedRE = regexp.MustCompile(`^Reasoning effort "[^"]*" is not supported by [^/]+/.+$`)
)

// zcodeIsReasoningLevelRejection reports whether err states one of the two
// verified reasoning-level failure classes -- the only errors another selection
// of the same model switch can resolve. Everything else, including the same
// data.code with a message outside the two classes, stops the switch at once.
func zcodeIsReasoningLevelRejection(err error) bool {
	var wire *zcodeError
	if !errors.As(err, &wire) {
		return false
	}
	var data struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(wire.Data, &data) != nil || data.Code != "invalid_model_request" {
		return false
	}
	return zcodeReasoningLevelRequiredRE.MatchString(wire.Message) ||
		zcodeReasoningEffortUnsupportedRE.MatchString(wire.Message)
}

// zcodeModelLevelAttempts caps how many reasoning-level selections one model
// switch tries.
const zcodeModelLevelAttempts = 5

// zcodeModelLevelCandidates builds the reasoning-level ladder of one model
// switch: the selected or live-default level first, then the selection that
// carries no level at all, then the remaining levels the authoritative live
// model record offers. A duplicate collapses into its earlier entry, and the
// ladder stops at five selections. A model that declares no levels therefore
// supplies no alternate level.
func zcodeModelLevelCandidates(selected string, offered []*agent.EffortInfo) []string {
	candidates := make([]string, 0, zcodeModelLevelAttempts)
	seen := make(map[string]bool, zcodeModelLevelAttempts)
	add := func(level string) {
		if seen[level] || len(candidates) >= zcodeModelLevelAttempts {
			return
		}
		seen[level] = true
		candidates = append(candidates, level)
	}
	add(selected)
	// The selection with no options.reasoningLevel: the app-server keeps whatever
	// level the session already had.
	add("")
	for _, tier := range offered {
		if tier == nil {
			continue
		}
		if id := tier.GetId(); id != "" && id != agent.EffortAuto {
			add(id)
		}
	}
	return candidates
}

// zcodeModelApplyResult is what one model switch settled on.
type zcodeModelApplyResult struct {
	// ThoughtLevel is the OBSERVED level of the successful snapshot, never the
	// requested one.
	ThoughtLevel string
	// Recovered records that the ladder settled the switch on a different
	// candidate than the selected or live-default level.
	Recovered bool
}

// applyZCodeModel pins the session's model and reports what the app-server settled
// on.
//
// Legacy builds receive a runtimeModel overlay with the inline key, as one
// attempt. ZCode 0.16.9 owns the provider registry and strictly refuses that
// field, so a current account configuration sends only the model reference -- and
// may retry: an app-server that refuses a reasoning level with one of the two
// verified failure classes gets the bounded ladder of zcodeModelLevelCandidates
// instead of a dead switch that a restart would repeat.
func (a *Agent) applyZCodeModel(modelID, level string, timeout time.Duration) (zcodeModelApplyResult, error) {
	a.Mu.Lock()
	resolved, ok := a.resolveZCodeModelIDLocked(modelID)
	sessionID := a.sessionID
	accountConfig := a.account != nil
	ref := a.liveModels[resolved].ref
	defaultLevel := a.zcodeDefaultThoughtLevelLocked(resolved)
	offered := a.zcodeThoughtLevelsLocked(resolved)
	a.Mu.Unlock()
	if !ok {
		return zcodeModelApplyResult{}, newInvalidZCodeModelError(modelID)
	}
	if sessionID == "" {
		return zcodeModelApplyResult{}, errNoZCodeSession
	}
	thoughtLevel := resolveZCodeThoughtLevel(level, defaultLevel, offered)

	if !accountConfig {
		overlay, found := a.catalog.runtimeModelFor(resolved, a.registryRevision, time.Now().UnixMilli())
		if !found {
			return zcodeModelApplyResult{}, newInvalidZCodeModelError(modelID)
		}
		// A concrete level rides along so a legacy model switch does not drop it.
		overlay.ThoughtLevel = thoughtLevel
		params := map[string]any{
			"sessionId":    sessionID,
			"model":        overlay.Model,
			"runtimeModel": overlay,
		}
		raw, err := a.sendZCodeRequest(MethodSetModel, params, timeout)
		if err != nil {
			return zcodeModelApplyResult{}, err
		}
		return a.settleZCodeModelSnapshot(sessionID, resolved, raw)
	}

	if ref.ModelID == "" {
		providerID, providerModelID, valid := splitZCodeModelID(resolved)
		if !valid {
			return zcodeModelApplyResult{}, newInvalidZCodeModelError(modelID)
		}
		ref = zcodeModelRef{ProviderID: providerID, ModelID: providerModelID}
	}
	candidates := zcodeModelLevelCandidates(thoughtLevel, offered)
	var lastErr error
	for i, candidate := range candidates {
		if i > 0 && !a.isCurrentZCodeSession(sessionID) {
			// A context clear replaced the session. The next selection would reach
			// a session this switch does not serve.
			return zcodeModelApplyResult{}, errNoZCodeSession
		}
		selection := zcodeModelSelection{zcodeModelRef: ref}
		// A candidate with no level leaves the level to the app-server, so the
		// selection carries no options.reasoningLevel at all.
		if candidate != "" {
			selection.Options = &zcodeModelSelectionOptions{ReasoningLevel: candidate}
		}
		params := map[string]any{
			"sessionId": sessionID,
			"model":     selection,
			// A model switch of one agent must not rewrite the workspace default.
			"persistAsWorkspaceLastUsed": false,
		}
		raw, err := a.sendZCodeRequest(MethodSetModel, params, timeout)
		if err == nil {
			result, settleErr := a.settleZCodeModelSnapshot(sessionID, resolved, raw)
			result.Recovered = i > 0
			return result, settleErr
		}
		if !zcodeIsReasoningLevelRejection(err) {
			return zcodeModelApplyResult{}, err
		}
		lastErr = err
	}
	// Every selection carried a level the app-server refused. The last rejection
	// says why.
	return zcodeModelApplyResult{}, lastErr
}

// settleZCodeModelSnapshot folds a successful session/setModel reply through the
// session-identity check and the settings fold, and reports the observed level.
func (a *Agent) settleZCodeModelSnapshot(sessionID, resolved string, raw json.RawMessage) (zcodeModelApplyResult, error) {
	a.sessionLifecycleMu.Lock()
	defer a.sessionLifecycleMu.Unlock()
	if !a.isCurrentZCodeSession(sessionID) {
		return zcodeModelApplyResult{}, errNoZCodeSession
	}
	// The requested id stands only where the snapshot states nothing. Apply it
	// first, then fold the reply: settings.model.current OVERWRITES this when the
	// app-server clamped the model, so the picker never shows an id the session
	// does not have.
	a.Mu.Lock()
	a.model = resolved
	a.Mu.Unlock()
	snapshot, observed := a.applyStateSnapshot(raw)
	if !observed || snapshot.Settings == nil || snapshot.Settings.Model == nil || snapshot.Settings.Model.Current.ModelID == "" {
		a.markSettingUnresolved(agent.OptionIDModel)
	}
	a.Mu.Lock()
	thoughtLevel := a.thoughtLevel
	a.Mu.Unlock()
	return zcodeModelApplyResult{ThoughtLevel: thoughtLevel}, nil
}

// runsThoughtLevel reports whether the session runs at level now. A model switch can settle on the
// level that the same edit states, so the edit then needs no second request.
func (a *Agent) runsThoughtLevel(level string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.thoughtLevel == level
}

// applyZCodeThoughtLevel sets the session's thought level.
//
// The parameter is REQUIRED even though the app-server's schema marks it optional:
// the handler throws for a missing value. EffortAuto never reaches here -- it is
// LeapMux's "send nothing" sentinel, which the callers filter.
func (a *Agent) applyZCodeThoughtLevel(level string, timeout time.Duration) error {
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	if sessionID == "" {
		return errNoZCodeSession
	}
	raw, err := a.sendZCodeRequest(MethodSetThoughtLevel, map[string]any{
		"sessionId":    sessionID,
		"thoughtLevel": level,
		// A thought-level change of one agent must not rewrite the workspace default.
		"persistAsWorkspaceLastUsed": false,
	}, timeout)
	if err != nil {
		return err
	}
	a.sessionLifecycleMu.Lock()
	defer a.sessionLifecycleMu.Unlock()
	if !a.isCurrentZCodeSession(sessionID) {
		return errNoZCodeSession
	}
	a.Mu.Lock()
	// Set first, then fold in the snapshot: the reply reports
	// `settings.thoughtLevel.current`, which OVERWRITES this when the app-server
	// clamped the level -- so the observed value wins and the requested value only
	// stands where the snapshot states nothing.
	a.thoughtLevel = level
	a.Mu.Unlock()
	snapshot, observed := a.applyStateSnapshot(raw)
	if !observed || snapshot.Settings == nil || snapshot.Settings.ThoughtLevel == nil ||
		(snapshot.Settings.ThoughtLevel.Enabled && snapshot.Settings.ThoughtLevel.Current == "") {
		a.markSettingUnresolved(agent.OptionIDEffort)
	}
	return nil
}

// applyZCodeMode switches the session's mode and reports the observed value.
//
// mode is a value of LeapMux's axis, and session/setMode accepts each of them: `plan`
// turns the plan flag on, and any other mode turns it off.
func (a *Agent) applyZCodeMode(mode string, timeout time.Duration) error {
	defer a.queuePermissionApprovals()
	a.Mu.Lock()
	sessionID := a.sessionID
	modeChangesBefore := a.modeChanges
	a.Mu.Unlock()
	if sessionID == "" {
		return errNoZCodeSession
	}
	raw, err := a.sendZCodeRequest(MethodSetMode, map[string]any{
		"sessionId": sessionID,
		"mode":      mode,
	}, timeout)
	if err != nil {
		return err
	}
	a.sessionLifecycleMu.Lock()
	defer a.sessionLifecycleMu.Unlock()
	if !a.isCurrentZCodeSession(sessionID) {
		return errNoZCodeSession
	}
	// The reply states no plan flag, so the accepted request states it. A
	// SessionModeChanged event that arrived during the RPC wins instead: the event of
	// this request states the same flag, and a later one, such as an EnterPlanMode
	// that ran after the request, states a newer flag that the rule would overwrite.
	a.Mu.Lock()
	if a.modeChanges == modeChangesBefore {
		a.recordPlanRequestLocked(mode)
	}
	a.Mu.Unlock()
	// The snapshot carries settings.mode.current, which is the only snapshot field
	// that tracks a switch of the native mode, so no local assignment of the native
	// mode happens here.
	snapshot, observed := a.applyStateSnapshot(raw)
	if !observed || snapshot.Settings == nil || snapshot.Settings.Mode == nil || snapshot.Settings.Mode.Current == "" {
		a.markSettingUnresolved(agent.OptionIDPermissionMode)
	}
	return nil
}

// UpdateSettings applies a settings change live.
//
// Returning false asks the caller to RESTART the agent with the requested values as
// launch options. That is the honest answer for any failed apply: reporting success
// would strand the picker on a value the running session does not have.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	defer a.queuePermissionApprovals()
	a.Mu.Lock()
	// The axis value, not the native mode: plan mode reads `build` natively, so a
	// switch from Plan to Build would otherwise compare equal and send nothing.
	curModel, curEffort, curMode := a.model, a.thoughtLevel, a.permissionModeLocked()
	a.Mu.Unlock()

	// Switching effort to Auto means "let the app-server pick", which the wire cannot
	// express: setThoughtLevel requires a level. A restart re-resolves the default.
	if agent.IsEffortAutoTransition(options[agent.OptionIDEffort], curEffort) {
		return agent.RestartRequiredSettings(options)
	}

	timeout := a.APITimeout()
	applied := true
	// skipEffortSetter suppresses the separate thought-level request after a model
	// switch that owns the level: the ladder just rejected the request, or settled
	// on some level of its own.
	skipEffortSetter := false

	if model := options[agent.OptionIDModel]; model != "" && normalizeZCodeModelID(model) != curModel {
		effort := options[agent.OptionIDEffort]
		combinedEdit := effort != "" && effort != agent.EffortAuto && effort != curEffort
		level := curEffort
		if combinedEdit {
			// A combined edit states the pair the user asked for, so the ladder
			// starts from the requested level rather than the one the session runs.
			level = effort
		}
		res, err := a.applyZCodeModel(model, level, timeout)
		switch {
		case err == nil:
			if res.Recovered {
				// The ladder settled on another level. Reapplying the request would
				// repeat a rejected level, so the setter stays out of the way.
				skipEffortSetter = true
				if combinedEdit && res.ThoughtLevel != effort {
					a.markSettingUnresolved(agent.OptionIDEffort)
				}
			}
		case zcodeIsReasoningLevelRejection(err):
			// Every selection carried a level the app-server refused. Restarting
			// would repeat the rejected pair, so the edit reports the observed
			// settings instead and leaves the request unresolved.
			slog.Warn("zcode UpdateSettings setModel rejected every reasoning level; keeping the observed settings", "agent_id", a.AgentID(), "model", model, "error", err)
			a.markSettingUnresolved(agent.OptionIDModel)
			if combinedEdit {
				a.markSettingUnresolved(agent.OptionIDEffort)
				skipEffortSetter = true
			}
		default:
			slog.Warn("zcode UpdateSettings setModel failed; restarting to apply", "agent_id", a.AgentID(), "model", model, "error", err)
			applied = false
		}
	}
	// An effort that equals the level before the edit is the stored value that the Worker sends
	// with every edit, so it is no request for a change. The model switch above can also settle
	// on the level that this edit states.
	if effort := options[agent.OptionIDEffort]; !skipEffortSetter && effort != "" && effort != agent.EffortAuto && effort != curEffort && !a.runsThoughtLevel(effort) {
		if err := a.applyZCodeThoughtLevel(effort, timeout); err != nil {
			slog.Warn("zcode UpdateSettings setThoughtLevel failed; restarting to apply", "agent_id", a.AgentID(), "level", effort, "error", err)
			applied = false
		}
	}
	if mode := options[agent.OptionIDPermissionMode]; mode != "" && mode != curMode {
		if err := a.applyZCodeMode(mode, timeout); err != nil {
			slog.Warn("zcode UpdateSettings setMode failed; restarting to apply", "agent_id", a.AgentID(), "mode", mode, "error", err)
			applied = false
		}
	}

	if !applied {
		// A partial apply (the model landed, the thought level was refused) is reported
		// as-is, and the caller restarts to settle it. A model switch whose reasoning
		// level the app-server refused never reaches this branch: restarting would
		// repeat the rejected pair, so that edit reports the observed settings instead.
		//
		// The trio is deliberately NOT rolled back. A setter that succeeded moved the live
		// session, and applyStateSnapshot folded that model's own thought levels, its
		// default and its context window over this agent's mirror. Restoring the three
		// axes alone would leave every DERIVED field pointing at the model the rollback
		// reverted away from: the settings bar would offer the old model with the new
		// model's levels, the context gauge would size the old model with the new model's
		// window, and an image attachment would be refused by the name of a model the
		// session is no longer on. That is the half-applied state the rollback was meant
		// to prevent, one layer down. The file's own rule decides it -- each setter
		// reports the OBSERVED value, never the requested one -- so the mirror keeps what
		// the app-server settled on until the restart re-derives every axis.
		return agent.RestartRequiredSettings(options)
	}

	a.Mu.Lock()
	model, effort, mode := a.model, a.thoughtLevel, a.permissionModeLocked()
	a.Mu.Unlock()
	a.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:          model,
		agent.OptionIDEffort:         effort,
		agent.OptionIDPermissionMode: mode,
	})
	return a.SettingsSnapshot()
}

func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	result := agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
	a.Mu.Lock()
	unresolved := make([]string, 0, len(a.unresolvedSettings))
	for id := range a.unresolvedSettings {
		unresolved = append(unresolved, id)
	}
	a.Mu.Unlock()
	for _, id := range unresolved {
		result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
	}
	return result
}

// zcodeStatePatch is the top-level state.updated notification.
//
// It is NOT a session event: it arrives at the top level with its own `type`, and
// it covers three scopes. Only the `session` scope carries settings, and the patch
// is partial -- it holds the axes that changed and nothing else.
type zcodeStatePatch struct {
	Scope     string          `json:"scope"`
	SessionID string          `json:"sessionId"`
	Revision  int64           `json:"revision"`
	Reason    string          `json:"reason"`
	Patch     json.RawMessage `json:"patch"`
}

// zcodeStatePatchBody is the shape LeapMux reads out of a session-scope patch.
//
// The settings axes sit at the TOP LEVEL of the patch -- there is no `settings`
// wrapper, although `session/read` nests the same object under that name. So the
// snapshot is EMBEDDED here, which promotes `mode`, `model` and `thoughtLevel` to
// the keys the patch actually uses. A wrapper struct silently matched nothing and
// dropped every mid-turn mode switch.
//
// `runtime` is read opportunistically. The shipped build sends it only in the
// `session/read` reply, never in a patch, so the usual runtime refresh comes from
// refreshZCodeUsageFromSession; reading it here costs nothing and keeps a build
// that starts sending it working without a change.
type zcodeStatePatchBody struct {
	zcodeSettingsSnapshot
	Runtime    *zcodeRuntimeState    `json:"runtime"`
	Projection *zcodeProjectionState `json:"projection"`
	// Status is the only other session-scope key the build emits (reason
	// `prompt_started`). The struct lists it to document the shape, and nothing
	// reads it: turn.started/turn.completed already drive the turn state.
	Status string `json:"status"`
}

// zcodeWorkspacePatch is the part of a workspace-scope state.updated patch that
// LeapMux reads. `modelCatalog` announces that the app-server reloaded its
// provider registry: the session's live model catalog needs the same
// registry-plus-catalog refresh that a catalog generation triggers. The patch's
// own data never folds into session settings or the event watermark.
type zcodeWorkspacePatch struct {
	ModelCatalog json.RawMessage `json:"modelCatalog"`
}

// isCurrentZCodeSession reports whether sessionID is the session this agent
// serves right now. An empty id passes: a notification that states no session
// cannot contradict the current one.
func (a *Agent) isCurrentZCodeSession(sessionID string) bool {
	if sessionID == "" {
		return true
	}
	a.Mu.Lock()
	current := a.sessionID
	a.Mu.Unlock()
	return current == "" || current == sessionID
}

// zcodeStateReasonModeChanged is the `reason` of a state patch that records a change of the native
// mode. Source: the app-server of ZCode 3.14.4 sends a `mode_changed` patch for each mode request.
const zcodeStateReasonModeChanged = "mode_changed"

// dropStaleMode removes the native mode from a patch that cannot be trusted to state it.
//
// ZCode puts a full settings snapshot on the patch of every reason. It emits the
// `prompt_completed` patch of a turn beside the `mode_changed` patch of a mode request that the
// turn ran, and both carry the same revision. The `prompt_completed` snapshot states the mode
// that held before the request, and either patch can arrive last. Applying that snapshot after
// the `mode_changed` patch put the session back on the old mode, in the agent and in the
// persisted settings, until a restart.
//
// Once a source has stated the mode, only a `mode_changed` patch changes it. Before that, any
// snapshot is the only evidence, so it applies.
func (b *zcodeStatePatchBody) dropStaleMode(reason string, modeObserved bool) {
	if modeObserved && reason != zcodeStateReasonModeChanged {
		b.Mode = nil
	}
}

// hasSettings reports whether the patch carries any settings axis. A patch
// that changes only status needs no settings fold.
func (b *zcodeStatePatchBody) hasSettings() bool {
	return b.Mode != nil || b.Model != nil || b.ThoughtLevel != nil
}

// handleZCodeStateUpdated folds a top-level state.updated notification into the
// agent's settings and usage, so a native mode or a model that changed during a turn
// reaches the picker, and the context-usage readout tracks the turn.
//
// The plan flag is not here. ZCode's EnterPlanMode and ExitPlanMode tools change the
// flag alone, and only the SessionModeChanged event reports that -- see
// handleZCodeModeChanged.
func (a *Agent) handleZCodeStateUpdated(params json.RawMessage) {
	defer a.queuePermissionApprovals()
	if len(params) == 0 {
		return
	}
	var notif zcodeStatePatch
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("zcode state.updated unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	defer a.handleZCodeCompactionState(notif)
	if len(notif.Patch) == 0 {
		return
	}
	var body zcodeStatePatchBody
	if err := json.Unmarshal(notif.Patch, &body); err != nil {
		// The patch is declared as free-form on the wire, so a shape LeapMux does not
		// read is normal rather than an error worth a warning at every arrival.
		slog.Debug("zcode state.updated patch not a session patch", "agent_id", a.AgentID(), "scope", notif.Scope)
		return
	}
	// Only session patches can change this agent's runtime and settings. A
	// workspace patch carrying `modelCatalog` schedules the registry-plus-catalog
	// refresh beside the read loop; it must not change session state.
	if notif.Scope != ScopeSession {
		if notif.Scope == ScopeWorkspace {
			var workspace zcodeWorkspacePatch
			if err := json.Unmarshal(notif.Patch, &workspace); err == nil && workspace.ModelCatalog != nil {
				a.scheduleZCodeCatalogRefresh()
			}
		}
		return
	}
	// A prompt boundary (prompt_completed / prompt_failed) is evidence of a
	// turn end. Attribute it to a turn owner before the generic observation:
	// the attribution needs the owner's admission revision and baseline.
	a.handleZCodePromptBoundary(notif, body)
	a.applyZCodeSessionObservation(notif.SessionID, body, &notif.Reason)
}

// applyZCodeSessionObservation validates identity and commits runtime and settings
// together. Sinks run after the commit and cannot split it across two sessions.
// A nil patchReason identifies an unrestricted mode observation from session/read.
func (a *Agent) applyZCodeSessionObservation(sessionID string, body zcodeStatePatchBody, patchReason *string) {
	a.Mu.Lock()
	// A context clear adopts a new session before the old one stops. Reject
	// late observations of the old session. An empty ID cannot contradict it.
	if a.StoppedLocked() || (sessionID != "" && a.sessionID != "" && sessionID != a.sessionID) {
		a.Mu.Unlock()
		return
	}
	info := a.applyZCodeRuntimeStateLocked(body.Runtime)
	if patchReason != nil {
		body.dropStaleMode(*patchReason, a.modeObserved)
	}
	// A patch never states the goal. ZCode 3.14.5 sends a patch of status
	// alone or of the settings. Its target_changed event announces each
	// goal transition through handleZCodeGoalChanged.
	before := zcodeSettingsTriple{a.model, a.thoughtLevel, a.permissionModeLocked()}
	if body.hasSettings() {
		a.applySettingsSnapshotLocked(&body.zcodeSettingsSnapshot)
	}
	after := zcodeSettingsTriple{a.model, a.thoughtLevel, a.permissionModeLocked()}
	a.Mu.Unlock()
	if before != after {
		a.sink.PersistSettingsRefresh(map[string]string{
			agent.OptionIDModel:          after.model,
			agent.OptionIDEffort:         after.effort,
			agent.OptionIDPermissionMode: after.mode,
		})
	}
	if len(info) > 0 {
		a.sink.BroadcastSessionInfo(info)
	}
}

// handleZCodeCompactionState reacts to a native compaction state transition.
//
// The compaction lifecycle events own the compaction rows now, so a `state.updated`
// reason persists nothing: one row from both would draw the same rewrite twice.
// The reason switch stays because a final one releases the queue turn.
func (a *Agent) handleZCodeCompactionState(notif zcodeStatePatch) {
	if notif.Scope != ScopeSession || !a.isCurrentZCodeSession(notif.SessionID) {
		return
	}
	switch notif.Reason {
	case contracts.ZCodeStateReasonCompactStarted,
		contracts.ZCodeStateReasonSessionCompacted,
		contracts.ZCodeStateReasonSessionCompactFailed,
		contracts.ZCodeStateReasonSessionCompactCancelled:
	default:
		return
	}
	if notif.Reason != contracts.ZCodeStateReasonCompactStarted {
		a.finishZCodeCompaction(notif.SessionID)
	}
}

// zcodeSettingsTriple is the comparable snapshot of the three settable axes, used to
// detect whether a state patch actually changed anything. mode is the value of the
// permission-mode axis, not the native mode.
type zcodeSettingsTriple struct {
	model  string
	effort string
	mode   string
}

// DeferPlanExitMode stores the permission mode an approved plan exit chose and
// sends it once ZCode reports the exit finished.
//
// ZCode folds session/setMode as "any value other than plan turns the plan flag
// off", so a mode sent while the approved ExitPlanMode still runs turns the flag
// off early and the exit fails with "You are not in plan mode". The flag-off
// SessionModeChanged of the ExitPlanMode tool is the first moment the mode can go
// out, so that is where the stored value fires. A flag-off event of another
// source, and the end of the turn, drop the stored value without a request. An
// exit that already ran when the approval settles takes its mode at once.
func (a *Agent) DeferPlanExitMode(mode string) {
	if mode == "" || mode == contracts.ZCodeModePlan {
		return
	}
	a.Mu.Lock()
	sessionID := a.sessionID
	exitAlreadyRan := !a.planEnabled && sessionID != ""
	a.pendingExitMode = mode
	a.Mu.Unlock()
	if exitAlreadyRan {
		a.sendDeferredZCodeExitMode(mode)
	}
}

// sendDeferredZCodeExitMode sends one stored exit mode. The caller sits on the
// event goroutine, so the RPC goes on a goroutine of its own: the reader must
// keep reading frames while the request is in flight.
func (a *Agent) sendDeferredZCodeExitMode(mode string) {
	go func() {
		if err := a.applyZCodeMode(mode, a.APITimeout()); err != nil {
			slog.Warn("zcode deferred plan exit mode failed", "agent_id", a.AgentID(), "mode", mode, "error", err)
		}
	}()
}
