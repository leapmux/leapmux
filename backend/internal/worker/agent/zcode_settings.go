package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
)

// Errors the setters return for a request the catalog or the session cannot satisfy.
var errNoZCodeSession = errors.New("agent has no ZCode session")

// newInvalidZCodeModelError reports a model id the catalog does not hold. The
// message gives the id, because the usual cause is a model that the user's
// `~/.zcode/v2/config.json` no longer lists.
func newInvalidZCodeModelError(modelID string) error {
	return fmt.Errorf("ZCode's configuration lists no model %q", modelID)
}

// ZCode exposes three settable axes, and every one of them is applied through a
// session RPC that returns the app-server's own state snapshot. So each setter
// reports the OBSERVED value, never the requested one: the app-server clamps a
// thought level a model does not offer and refuses a mode it does not know, and a
// UI showing the request rather than the result would be lying about what the next
// turn will do.

// ZCodeThoughtLevelLabel is ZCode's display label for its effort axis. The
// app-server calls it a "thought level" (session/setThoughtLevel), not a generic
// reasoning effort, so the settings popover uses the same label.
const ZCodeThoughtLevelLabel = "Thought Level"

// ZCodeModeLabel labels the mode axis, which LeapMux carries on its permission-mode
// channel so the plan-mode toggle and the mode chip drive it.
const ZCodeModeLabel = "Mode"

// zcodeAutoEffort is LeapMux's sentinel for "send no thought level at all", which
// leaves the app-server on whatever default it resolved for the model.
var zcodeAutoEffort = &EffortInfo{
	Id:          EffortAuto,
	Name:        effortLabel(EffortAuto),
	Description: "Use ZCode's default thought level for the model",
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
func zcodeEffortTier(value, label, description string) *EffortInfo {
	name := effortLabel(value)
	if label != "" && !strings.EqualFold(label, value) {
		name = label
	}
	return &EffortInfo{Id: value, Name: name, Description: description}
}

// zcodeFallbackModels is the static seed the settings popover shows before an
// agent runs.
//
// It is deliberately EMPTY. Every ZCode model comes from the user's own
// `~/.zcode/v2/config.json` -- the provider ids, the model ids and the thought
// levels are all theirs -- so a hardcoded entry would name a model that a given
// installation does not have, and picking it would fail the launch. An empty
// catalog renders no model group until the agent reports its real one.
var zcodeFallbackModels []*ModelInfo

// zcodeStaticOptionGroups returns the option groups that do NOT depend on a
// running agent: the mode axis, whose four values are fixed by the app-server.
//
// It has ONE caller, the factory registration in zcode.go. Every reader takes the
// registered copy through AvailableOptionGroupsForProvider, so the template has one
// home: a second call here would be a second source of truth that can drift from the
// list the settings popover actually offers.
//
// `auto` is absent on purpose. It is in the app-server's own enumeration and is
// not implemented in the shipped build: every tool call under it is denied with
// `permission.resolved {reason:"Auto mode is reserved but not implemented yet"}`,
// so offering it would give the user a mode in which nothing works.
func zcodeStaticOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return []*leapmuxv1.AvailableOptionGroup{
		{
			Id:           OptionIDPermissionMode,
			Label:        ZCodeModeLabel,
			DefaultValue: contracts.ZCodeDefaultMode,
			Mutable:      true,
			Order:        OptionOrderPermissionMode,
			Options: []*leapmuxv1.AvailableOption{
				{Id: contracts.ZCodeModePlan, Name: "Plan", Description: "Research and plan; no edits and no commands"},
				{Id: contracts.ZCodeModeBuild, Name: "Build", Description: "Edit files and run commands, asking before a risky action"},
				{Id: contracts.ZCodeModeEdit, Name: "Edit", Description: "Edit files freely; ask before running a command"},
				{Id: contracts.ZCodeModeYolo, Name: "Yolo", Description: "Run everything without asking"},
			},
		},
	}
}

// zcodeSettingsRequest is the trio a caller ASKS a session to run on: the launch
// options at startup, and the current axes at a context clear.
//
// It exists because opening a session folds the app-server's own settings over the
// agent's mirror, so the request is unreadable from those fields afterwards. The caller
// captures it before, and applyStartupSettings re-pins it. An empty field means "the
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

// zcodeSettingsSnapshot is the `settings` object every state-changing session RPC
// returns, and that the top-level state.updated notification patches.
//
// Each axis is a POINTER, because a state.updated patch carries only the axes that
// changed -- an absent axis must leave the agent's value alone, which a value type
// could not express.
type zcodeSettingsSnapshot struct {
	// AppliedProviderRevision echoes the registry revision the session resolved its
	// credential from. A mismatch with our own revision means the registry push has
	// not landed yet.
	AppliedProviderRevision string `json:"appliedProviderRevision"`
	Mode                    *struct {
		// Current is the AUTHORITATIVE mode. `session.mode` is the creation-time
		// record and never tracks a switch, so it is deliberately not read.
		Current string `json:"current"`
	} `json:"mode"`
	Model *struct {
		Current   zcodeModelRef `json:"current"`
		Available []struct {
			Ref            zcodeModelRef `json:"ref"`
			Label          string        `json:"label"`
			ProviderLabel  string        `json:"providerLabel"`
			Description    string        `json:"description"`
			ContextWindow  int64         `json:"contextWindow"`
			SupportsImages bool          `json:"supportsImages"`
			SupportsPdf    bool          `json:"supportsPdf"`
			DisabledReason string        `json:"disabledReason"`
		} `json:"available"`
		LastUsed *zcodeModelRef `json:"lastUsed"`
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
// Caller holds a.mu.
//
// Every value read here is the app-server's own: `mode.current`, `model.current`
// and `thoughtLevel.current`. Where a field is absent the previous value stands,
// which is what makes this safe to call with a partial state.updated patch.
func (a *zcodeAgent) applySettingsSnapshotLocked(snap *zcodeSettingsSnapshot) {
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
		if m.Current.ModelID != "" {
			a.model = zcodeModelID(m.Current.ProviderID, m.Current.ModelID)
		} else if ref := m.LastUsed; ref != nil && ref.ModelID != "" && a.model == "" {
			a.model = zcodeModelID(ref.ProviderID, ref.ModelID)
		}
	}
	if tl := snap.ThoughtLevel; tl != nil {
		levels := make([]*EffortInfo, 0, len(tl.Available)+1)
		available := make(map[string]bool, len(tl.Available))
		for _, l := range tl.Available {
			if l.Value == "" {
				continue
			}
			available[l.Value] = true
			levels = append(levels, zcodeEffortTier(l.Value, l.Label, l.Description))
		}
		// Strongest first, exactly as the configured catalog orders them
		// (zcodeModelInfo). The app-server reports the levels in the order the
		// model's own configuration lists them, which is the order this replaces --
		// so without the same sort here the menu reordered itself the moment the
		// agent reported its first snapshot.
		sortEffortsByStrength(levels)
		// Auto leads the list: it is LeapMux's "send no level" sentinel, and every
		// model accepts it because it produces no RPC at all. It is not a strength,
		// so it joins after the sort.
		levels = append([]*EffortInfo{zcodeAutoEffort}, levels...)
		if !tl.Enabled || len(available) == 0 {
			// The model offers no thought level at all. Auto alone keeps the axis
			// selectable-but-inert rather than showing a level the model would refuse.
			a.observedThoughtLevels = []*EffortInfo{zcodeAutoEffort}
			a.observedThoughtDefault = EffortAuto
			a.thoughtLevel = EffortAuto
			return
		}
		a.observedThoughtLevels = levels
		a.observedThoughtDefault = tl.DefaultLevel
		if a.observedThoughtDefault == "" {
			a.observedThoughtDefault = EffortAuto
		}
		switch {
		case tl.Current != "" && available[tl.Current]:
			a.thoughtLevel = tl.Current
		case a.thoughtLevel != "" && a.thoughtLevel != EffortAuto && !available[a.thoughtLevel]:
			// The level was set for a different model, and this one does not offer it.
			// Auto is the honest report: no level is pinned.
			a.thoughtLevel = EffortAuto
		}
	}
}

// zcodeModelsForUI returns the model catalog to surface, with the CURRENT model's
// effort tiers replaced by the levels the app-server reported for it.
//
// The configured `reasoning.variants` are the base list, because they cover every
// model. The live `settings.thoughtLevel.available` covers only the running model,
// and where the two disagree the live one is authoritative -- it is what the next
// setThoughtLevel will accept.
func (a *zcodeAgent) zcodeModelsForUI() ([]*ModelInfo, string, string) {
	a.mu.Lock()
	current, level := a.model, a.thoughtLevel
	observed, observedDefault := a.observedThoughtLevels, a.observedThoughtDefault
	a.mu.Unlock()

	models := a.catalog.Models
	if len(observed) == 0 || current == "" {
		return models, current, level
	}
	out := make([]*ModelInfo, 0, len(models))
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
func (a *zcodeAgent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	models, model, effort := a.zcodeModelsForUI()
	groups := modelAndEffortGroups(models, model, effort, ZCodeThoughtLevelLabel, nil)

	a.mu.Lock()
	mode := a.mode
	a.mu.Unlock()
	registered := AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE)
	if g := liveGroup(optionids.GroupByID(registered, OptionIDPermissionMode), mode); g != nil {
		groups = append(groups, g)
	}
	return groups
}

// --- the setters ---

// applyZCodeModel pins the session's model and reports what the app-server settled
// on.
//
// The runtimeModel overlay carries the provider entry WITH its inline API key, so
// the switch resolves a credential immediately instead of racing the workspace-scoped
// registry push.
func (a *zcodeAgent) applyZCodeModel(modelID string, timeout time.Duration) error {
	resolved, ok := a.catalog.resolveModelID(modelID)
	if !ok {
		return newInvalidZCodeModelError(modelID)
	}
	a.mu.Lock()
	sessionID, level := a.sessionID, a.thoughtLevel
	a.mu.Unlock()
	if sessionID == "" {
		return errNoZCodeSession
	}

	overlay, ok := a.catalog.runtimeModelFor(resolved, a.registryRevision, time.Now().UnixMilli())
	if !ok {
		return newInvalidZCodeModelError(modelID)
	}
	// A concrete level rides along so a model switch does not drop it. Auto resolves
	// to the model's OWN declared default, because a session that is told no level at
	// all runs on the app-server's fallback -- the lowest level, not the default the
	// model declares.
	if level != "" && level != EffortAuto {
		overlay.ThoughtLevel = level
	} else {
		overlay.ThoughtLevel = a.catalog.defaultThoughtLevel(resolved)
	}

	raw, err := a.sendZCodeRequest(ZCodeMethodSetModel, map[string]any{
		"sessionId":    sessionID,
		"model":        overlay.Model,
		"runtimeModel": overlay,
	}, timeout)
	if err != nil {
		return err
	}
	// The requested id stands only where the snapshot states nothing. Apply it
	// first, then fold the reply: settings.model.current OVERWRITES this when the
	// app-server clamped the model, so the picker never shows an id the session
	// does not have.
	a.mu.Lock()
	a.model = resolved
	a.mu.Unlock()
	a.applyStateSnapshot(raw)
	return nil
}

// applyZCodeThoughtLevel sets the session's thought level.
//
// The parameter is REQUIRED even though the app-server's schema marks it optional:
// the handler throws for a missing value. EffortAuto never reaches here -- it is
// LeapMux's "send nothing" sentinel, which the callers filter.
func (a *zcodeAgent) applyZCodeThoughtLevel(level string, timeout time.Duration) error {
	a.mu.Lock()
	sessionID := a.sessionID
	a.mu.Unlock()
	if sessionID == "" {
		return errNoZCodeSession
	}
	raw, err := a.sendZCodeRequest(ZCodeMethodSetThoughtLevel, map[string]any{
		"sessionId":    sessionID,
		"thoughtLevel": level,
	}, timeout)
	if err != nil {
		return err
	}
	a.mu.Lock()
	// Set first, then fold in the snapshot: the reply reports
	// `settings.thoughtLevel.current`, which OVERWRITES this when the app-server
	// clamped the level -- so the observed value wins and the requested value only
	// stands where the snapshot states nothing.
	a.thoughtLevel = level
	a.mu.Unlock()
	a.applyStateSnapshot(raw)
	return nil
}

// applyZCodeMode switches the session's mode and reports the observed value.
func (a *zcodeAgent) applyZCodeMode(mode string, timeout time.Duration) error {
	a.mu.Lock()
	sessionID := a.sessionID
	a.mu.Unlock()
	if sessionID == "" {
		return errNoZCodeSession
	}
	raw, err := a.sendZCodeRequest(ZCodeMethodSetMode, map[string]any{
		"sessionId": sessionID,
		"mode":      mode,
	}, timeout)
	if err != nil {
		return err
	}
	// The snapshot carries settings.mode.current, which is the only field that tracks
	// a switch, so no local assignment happens here.
	a.applyStateSnapshot(raw)
	return nil
}

// UpdateSettings applies a settings change live.
//
// Returning false asks the caller to RESTART the agent with the requested values as
// launch options. That is the honest answer for any failed apply: reporting success
// would strand the picker on a value the running session does not have.
func (a *zcodeAgent) UpdateSettings(options optionmap.Map) bool {
	a.mu.Lock()
	curModel, curEffort, curMode := a.model, a.thoughtLevel, a.mode
	a.mu.Unlock()

	// Switching effort to Auto means "let the app-server pick", which the wire cannot
	// express: setThoughtLevel requires a level. A restart re-resolves the default.
	if IsEffortAutoTransition(options[OptionIDEffort], curEffort) {
		return false
	}

	timeout := a.APITimeout()
	applied := true

	if model := options[OptionIDModel]; model != "" && normalizeZCodeModelID(model) != curModel {
		if err := a.applyZCodeModel(model, timeout); err != nil {
			slog.Warn("zcode UpdateSettings setModel failed; restarting to apply", "agent_id", a.agentID, "model", model, "error", err)
			applied = false
		}
	}
	if effort := options[OptionIDEffort]; effort != "" && effort != EffortAuto && effort != curEffort {
		if err := a.applyZCodeThoughtLevel(effort, timeout); err != nil {
			slog.Warn("zcode UpdateSettings setThoughtLevel failed; restarting to apply", "agent_id", a.agentID, "level", effort, "error", err)
			applied = false
		}
	}
	if mode := options[OptionIDPermissionMode]; mode != "" && mode != curMode {
		if err := a.applyZCodeMode(mode, timeout); err != nil {
			slog.Warn("zcode UpdateSettings setMode failed; restarting to apply", "agent_id", a.agentID, "mode", mode, "error", err)
			applied = false
		}
	}

	if !applied {
		// A partial apply (the model landed, the thought level was refused) is reported
		// as-is, and the caller restarts to settle it.
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
		return false
	}

	a.mu.Lock()
	model, effort, mode := a.model, a.thoughtLevel, a.mode
	a.mu.Unlock()
	a.sink.PersistSettingsRefresh(map[string]string{
		OptionIDModel:          model,
		OptionIDEffort:         effort,
		OptionIDPermissionMode: mode,
	})
	return true
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
	Runtime *zcodeRuntimeState `json:"runtime"`
	// Status is the only other session-scope key the build emits (reason
	// `prompt_started`). It is named so the shape is documented, not consumed:
	// turn.started/turn.completed already drive the turn state.
	Status string `json:"status"`
}

// hasSettings reports whether the patch carried any settings axis at all. A patch
// that changed only `status` must not reach applySettingsSnapshotLocked, because an
// all-nil snapshot would compare equal and persist a pointless settings refresh.
func (b *zcodeStatePatchBody) hasSettings() bool {
	return b.Mode != nil || b.Model != nil || b.ThoughtLevel != nil
}

// handleZCodeStateUpdated folds a top-level state.updated notification into the
// agent's settings and usage, so a mode or model the AGENT changed mid-turn
// (ZCode's own EnterPlanMode / ExitPlanMode tools do exactly that) reaches the
// picker, and the context-usage readout tracks the turn.
func (a *zcodeAgent) handleZCodeStateUpdated(params json.RawMessage) {
	if len(params) == 0 {
		return
	}
	var notif zcodeStatePatch
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("zcode state.updated unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	if len(notif.Patch) == 0 {
		return
	}
	var body zcodeStatePatchBody
	if err := json.Unmarshal(notif.Patch, &body); err != nil {
		// The patch is declared as free-form on the wire, so a shape LeapMux does not
		// read is normal rather than an error worth a warning at every arrival.
		slog.Debug("zcode state.updated patch not a session patch", "agent_id", a.agentID, "scope", notif.Scope)
		return
	}
	if body.Runtime != nil {
		a.applyZCodeRuntimeState(body.Runtime)
	}
	// Only the session scope carries settings. The workspace scope patches
	// `modelCatalog`, whose keys this struct does not read, so it must not be
	// mistaken for an all-absent settings patch.
	if notif.Scope != ZCodeScopeSession || !body.hasSettings() {
		return
	}
	a.mu.Lock()
	before := zcodeSettingsTriple{a.model, a.thoughtLevel, a.mode}
	a.applySettingsSnapshotLocked(&body.zcodeSettingsSnapshot)
	after := zcodeSettingsTriple{a.model, a.thoughtLevel, a.mode}
	a.mu.Unlock()
	if before == after {
		return
	}
	// The AGENT changed a setting, so the persisted row is stale. Refreshing it here
	// is what makes a mid-turn plan-mode entry survive a restart.
	a.sink.PersistSettingsRefresh(map[string]string{
		OptionIDModel:          after.model,
		OptionIDEffort:         after.effort,
		OptionIDPermissionMode: after.mode,
	})
}

// zcodeSettingsTriple is the comparable snapshot of the three settable axes, used to
// detect whether a state patch actually changed anything.
type zcodeSettingsTriple struct {
	model  string
	effort string
	mode   string
}
