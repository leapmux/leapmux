package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Codex collaboration mode values.
const (
	CollaborationDefault = "default"
	CollaborationPlan    = "plan"
)

// Codex service tier values.
const ServiceTierFast = "fast"

// The Codex approvals reviewer values. The worker alone reads them: the browser
// sees only the permission mode that codexPermissionModeFromWire derives.
const (
	codexReviewerUser       = "user"
	codexReviewerAutoReview = "auto_review"
)

// codexSettingsUpdateTimeout limits how long a live permission change waits for
// thread/settings/update. The call runs under the service lifecycle lock, so a
// slow app server would otherwise hold every other operation on the agent for the
// full API timeout. Claude's permission-mode apply limits its wait for the same
// reason. A timeout keeps the requested values and reports them unresolved.
const codexSettingsUpdateTimeout = 5 * time.Second

// codexPermissionOptionIDs lists the two axes that Codex receives together as its
// permission settings.
var codexPermissionOptionIDs = []string{agent.OptionIDPermissionMode, contracts.CodexOptionPermissionProfile}

// codexAxis describes one Codex configuration axis. Lifecycle responses,
// OptionGroups, live updates, and provider defaults use this table.
type codexAxis struct {
	id  string
	get func(*Agent) string // reads the live value from agent state; call under a.Mu
	// set writes a (non-empty) requested value into agent state; call under a.Mu. Having
	// it on the table means "add a Codex axis = one table row" holds for the live-update
	// writes too, so a new axis can't be silently dropped from UpdateSettings while still
	// appearing in the picker via get.
	set func(*Agent, string)
	// refreshFallback derives a value that Codex computes implicitly. Call under a.Mu.
	refreshFallback func(*Agent)
	// defaultValue is the Codex-specific default resolveProviderDefaults stamps for a
	// provider option axis (permission profile, collaboration mode, service tier). Empty
	// for model, effort, and permission mode, which the shared model, effort, and
	// permission logic default.
	defaultValue string
	// lifecyclePolicy states whether thread/start and thread/resume own this axis.
	// Effort is authoritative only while its requested value is automatic.
	lifecyclePolicy  codexLifecyclePolicy
	lifecycleDefault string
	projectGroup     func(*leapmuxv1.AvailableOptionGroup, string, codexPermissionCatalog) *leapmuxv1.AvailableOptionGroup
}

var codexAxes = []codexAxis{
	{id: agent.OptionIDModel, get: func(a *Agent) string { return a.model }, set: func(a *Agent, v string) { a.model = v }, lifecyclePolicy: codexLifecycleAuthoritative},
	{id: agent.OptionIDEffort, get: func(a *Agent) string { return a.effort }, set: func(a *Agent, v string) { a.effort = v }, refreshFallback: codexEffortRefreshFallback, lifecyclePolicy: codexLifecycleWhenAutomatic, lifecycleDefault: agent.EffortAuto},
	{id: agent.OptionIDPermissionMode, get: func(a *Agent) string { return a.permissionMode }, set: func(a *Agent, v string) { a.permissionMode = v }, lifecyclePolicy: codexLifecycleAuthoritative,
		projectGroup: func(group *leapmuxv1.AvailableOptionGroup, current string, catalog codexPermissionCatalog) *leapmuxv1.AvailableOptionGroup {
			return codexPermissionModeGroup(group, catalog.refusedModes, current)
		}},
	{id: contracts.CodexOptionPermissionProfile, get: func(a *Agent) string { return a.permissionProfile }, set: func(a *Agent, v string) { a.permissionProfile = v }, defaultValue: contracts.CodexOptionDefaultPermissionProfile, lifecyclePolicy: codexLifecycleAuthoritative,
		projectGroup: func(group *leapmuxv1.AvailableOptionGroup, current string, catalog codexPermissionCatalog) *leapmuxv1.AvailableOptionGroup {
			return codexPermissionProfileGroup(group, catalog.profiles, current)
		}},
	{id: contracts.CodexOptionCollaborationMode, get: func(a *Agent) string { return a.collaborationMode }, set: func(a *Agent, v string) { a.collaborationMode = v }, defaultValue: contracts.CodexOptionDefaultCollaborationMode},
	{id: contracts.CodexOptionServiceTier, get: func(a *Agent) string { return a.serviceTier }, set: func(a *Agent, v string) { a.serviceTier = v }, defaultValue: contracts.CodexOptionDefaultServiceTier, lifecyclePolicy: codexLifecycleAuthoritative, lifecycleDefault: contracts.CodexOptionDefaultServiceTier},
}

// codexAxisByID returns the axis with the given option id, or nil.
func codexAxisByID(id string) *codexAxis {
	for i := range codexAxes {
		if codexAxes[i].id == id {
			return &codexAxes[i]
		}
	}
	return nil
}

// codexEffortRefreshFallback mirrors the model preset's implicit effort default.
// It applies only while the requested value is automatic. Caller holds a.Mu.
func codexEffortRefreshFallback(a *Agent) {
	if a.effort != agent.EffortAuto {
		return
	}
	if m := agent.FindAvailableModel(a.availableModels, a.model); m != nil && m.DefaultEffort != "" {
		a.effort = m.DefaultEffort
	}
}

// codexAxisValuesLocked snapshots every axis's live value into an id->value map. Caller
// holds a.Mu.
func (a *Agent) codexAxisValuesLocked() map[string]string {
	vals := make(map[string]string, len(codexAxes))
	for _, ax := range codexAxes {
		vals[ax.id] = ax.get(a)
	}
	return vals
}

// codexOptionDefaults returns the Codex provider-option defaults (id->default), registered
// as Registration.ProviderOptionDefaults so resolveProviderDefaults stamps them
// uniformly without re-listing each axis or branching on the provider.
func codexOptionDefaults() map[string]string {
	out := make(map[string]string)
	for _, ax := range codexAxes {
		if ax.defaultValue != "" {
			out[ax.id] = ax.defaultValue
		}
	}
	return out
}

// codexPermissionSettings is the pair of axes that Codex receives together as its
// permission settings: the permission mode and the permission profile.
type codexPermissionSettings struct {
	mode    string
	profile string
}

// permissionSettingsLocked returns the agent's permission settings. Caller holds a.Mu.
func (a *Agent) permissionSettingsLocked() codexPermissionSettings {
	return codexPermissionSettings{mode: a.permissionMode, profile: a.permissionProfile}
}

// setPermissionSettingsLocked replaces the agent's permission settings. Caller holds a.Mu.
func (a *Agent) setPermissionSettingsLocked(s codexPermissionSettings) {
	a.permissionMode = s.mode
	a.permissionProfile = s.profile
}

// codexApprovalWire maps a LeapMux permission mode to the native approval policy and
// approvals reviewer that Codex receives. Approve for Me is the on-request policy with
// the auto-review reviewer. Every other mode is its own policy with the user as the
// reviewer, which also returns a thread from auto-review to the user.
func codexApprovalWire(mode string) (policy, reviewer string) {
	if mode == contracts.CodexPermissionModeApproveForMe {
		return ModeAskForApproval, codexReviewerAutoReview
	}
	return mode, codexReviewerUser
}

// codexPermissionModeFromWire is the inverse of codexApprovalWire, for the settings
// that Codex reports. A reviewer on any policy other than on-request reviews nothing,
// so that pair reads as the policy alone.
func codexPermissionModeFromWire(policy, reviewer string) string {
	if policy == ModeAskForApproval && reviewer == codexReviewerAutoReview {
		return contracts.CodexPermissionModeApproveForMe
	}
	return policy
}

// addCodexPermissionParams writes the permission fields that thread/start,
// thread/resume, turn/start, and thread/settings/update share. It always states the
// reviewer beside the policy, because Codex keeps a reviewer across turns until a
// request changes it. It sends the permission profile, and never the legacy
// sandbox field, which Codex refuses beside a profile.
func addCodexPermissionParams(params map[string]interface{}, s codexPermissionSettings) {
	if s.mode != "" {
		policy, reviewer := codexApprovalWire(s.mode)
		params["approvalPolicy"] = policy
		params["approvalsReviewer"] = reviewer
	}
	if s.profile != "" {
		params["permissions"] = s.profile
	}
}

// bypassActiveLocked reports whether the standing approval answers permission
// requests: every axis of the bypass preset holds its preset value, and Codex
// acknowledged those values. Until Codex acknowledges them, Codex can still refuse
// them, and an approval in their name would decide a request that the user's
// effective settings would still put to the user. Caller holds a.Mu.
func (a *Agent) bypassActiveLocked() bool {
	if !a.permissionsAcknowledged {
		return false
	}
	for id, value := range contracts.CodexBypassOptions() {
		ax := codexAxisByID(id)
		if ax == nil || ax.get(a) != value {
			return false
		}
	}
	return true
}

// acknowledgePermissions records that Codex accepted the permission settings s. It
// changes nothing when the agent's settings moved on since s was sent, because then
// Codex acknowledged values that are no longer the agent's.
func (a *Agent) acknowledgePermissions(s codexPermissionSettings) {
	a.Mu.Lock()
	if a.permissionSettingsLocked() == s {
		a.permissionsAcknowledged = true
	}
	a.Mu.Unlock()
}

// offersPermissionModeLocked reports whether the requirements allow a permission mode.
// Caller holds a.Mu.
func (a *Agent) offersPermissionModeLocked(mode string) bool {
	_, refused := a.refusedModes[mode]
	return !refused
}

// offersPermissionProfileLocked reports whether the discovered catalog offers a
// permission profile. An empty catalog offers the static seed. Caller holds a.Mu.
func (a *Agent) offersPermissionProfileLocked(profile string) bool {
	options := a.permissionProfiles.selectableOptions()
	return slices.ContainsFunc(options, func(o *leapmuxv1.AvailableOption) bool { return o.GetId() == profile })
}

// OptionGroups returns the model and effort groups plus the static Codex
// option groups (service tier, collaboration mode, approval policy, permissions),
// each overlaid with the agent's confirmed current value.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	vals := a.codexAxisValuesLocked()
	models := a.availableModels
	catalog := codexPermissionCatalog{profiles: a.permissionProfiles, refusedModes: a.refusedModes}
	a.Mu.Unlock()

	groups := providerkit.ModelAndEffortGroups(models, vals[agent.OptionIDModel], vals[agent.OptionIDEffort], agent.EffortGroupLabel, nil)

	// Current values are sourced per-axis from the snapshot; the display order is
	// carried on each registered template (so a newly-registered group can't lose its
	// order or sort ahead of the model group), and providerkit.LiveGroup defaults an unsupplied
	// current to the template's default. The model/effort entries in vals are unused
	// here -- they are rendered by providerkit.ModelAndEffortGroups above.
	for _, sg := range codexStaticOptionGroups {
		current := vals[sg.GetId()]
		if axis := codexAxisByID(sg.GetId()); axis != nil && axis.projectGroup != nil {
			sg = axis.projectGroup(sg, current, catalog)
		}
		groups = append(groups, providerkit.LiveGroup(sg, current))
	}
	return groups
}

// codexPermissionModeGroup narrows the template to the modes that the requirements
// allow. The current mode stays listed even when the requirements refuse it, so the
// picker never loses the session's real state.
func codexPermissionModeGroup(static *leapmuxv1.AvailableOptionGroup, refused map[string]struct{}, current string) *leapmuxv1.AvailableOptionGroup {
	if len(refused) == 0 {
		return static
	}
	return providerkit.FilterGroupOptions(static, func(o *leapmuxv1.AvailableOption) bool {
		_, isRefused := refused[o.GetId()]
		return !isRefused || o.GetId() == current
	})
}

// codexPermissionProfileGroup adds the current value to the selectable list for display.
// A known empty catalog shows only the current value.
func codexPermissionProfileGroup(static *leapmuxv1.AvailableOptionGroup, discovered codexPermissionProfileCatalog, current string) *leapmuxv1.AvailableOptionGroup {
	options := discovered.selectableOptions()
	if current != "" && !slices.ContainsFunc(options, func(o *leapmuxv1.AvailableOption) bool { return o.GetId() == current }) {
		options = append(slices.Clone(options), &leapmuxv1.AvailableOption{Id: current, Name: current})
	}
	return providerkit.ReplaceGroupOptions(static, options)
}

// UpdateSettings applies new settings. A permission change goes to the running thread
// at once through thread/settings/update. Every other axis waits for the next
// turn/start, which carries every axis.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.Mu.Lock()
	curEffort := a.effort
	curModel := a.model
	// Switching to EffortAuto can't be done live: Codex's session config
	// remembers the last reasoning_effort across turns, so simply
	// omitting the field on the next turn keeps the prior effort
	// applied. A restart is the only way to hand control back to the
	// CLI's own default.
	if agent.IsEffortAutoTransition(options[agent.OptionIDEffort], curEffort) {
		a.Mu.Unlock()
		return agent.RestartRequiredSettings(options)
	}
	// Switching to the account default can't be done live either, and for the same
	// shape of reason as the effort sentinel above. thread/start resolves an omitted
	// model, but turn/start sends the stored string as it is, and Codex rejects the
	// literal id "default" ("The 'default' model is not supported"). A relaunch runs
	// codexThreadParams again, which omits the model and lets Codex resolve it.
	// Test the sentinel EXACTLY, not UsesAccountDefaultModel: in this map an empty
	// value means "not supplied" (see the axis loop below), so the wider test would
	// demand a restart on every edit that leaves the model alone.
	if m := options[agent.OptionIDModel]; m == agent.DefaultModelSentinel && m != curModel {
		a.Mu.Unlock()
		return agent.RestartRequiredSettings(options)
	}
	prior := a.permissionSettingsLocked()
	priorAcknowledged := a.permissionsAcknowledged
	// Table-driven so every axis applies the same "non-empty value overwrites" rule and
	// a newly-added axis can't be forgotten here. The effort-auto guard above stays out
	// of the loop -- it vetoes the whole update, which a per-axis setter can't express.
	//
	// Skipping an empty value does NOT violate the optionmap empty-deletes wire contract: that
	// contract is honored UPSTREAM, at the persistence/merge boundary (mergeOptions drops a cleared
	// key, resolveProviderDefaults refills the axis default), so every map that reaches UpdateSettings
	// is already a fully-resolved snapshot with no empties to clear -- the edit path also rejects an
	// empty value before it gets here (acceptExposedOptions). An empty here is therefore a phantom
	// "unset", and keeping the prior value is the correct response, not a missed clear.
	for _, ax := range codexAxes {
		if v := options[ax.id]; v != "" {
			ax.set(a, v)
		}
	}
	// A permission value that the live catalog does not offer keeps the prior value. The
	// requirements refuse the mode, or config.toml no longer defines the profile, so Codex
	// would refuse the request, and the snapshot then reports the refusal.
	if !a.offersPermissionModeLocked(a.permissionMode) {
		a.permissionMode = prior.mode
	}
	if !a.offersPermissionProfileLocked(a.permissionProfile) {
		a.permissionProfile = prior.profile
	}
	settings := a.turnSettingsLocked()
	requested := settings.permissions
	permissionsChanged := requested != prior
	if permissionsChanged {
		// Disarm the standing approval until Codex accepts the new values. The reader
		// must never approve a request in the name of values that Codex can refuse.
		a.permissionsAcknowledged = false
	}
	threadID := a.threadID
	turnID := a.turnID
	a.Mu.Unlock()

	unresolved := false
	if permissionsChanged && threadID != "" {
		unresolved = a.applyPermissionSettingsLive(threadID, turnID, prior, priorAcknowledged, settings)
	}
	a.approveOutstandingForBypass()
	a.publishSettings()
	result := a.SettingsSnapshot()
	if unresolved {
		for _, id := range codexPermissionOptionIDs {
			result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
		}
	}
	return result
}

// applyPermissionSettingsLive sends the requested permission settings to the running
// thread through thread/settings/update, as the Codex TUI and the ChatGPT app do. It
// runs with a.Mu released, because the reader takes a.Mu before it delivers the reply.
//
// The request carries every turn setting, not only the permission axes. Codex answers
// with a thread/settings/updated echo of the thread's complete settings, and
// handleThreadSettingsUpdated adopts the model, the effort, and the collaboration mode
// from it. An axis that the request omitted would come back at the value of the
// thread's last turn, which undoes a change that waits for the next turn/start, such
// as the collaboration mode that a plan approval selects.
//
// The outcomes:
//   - Codex accepts: the values are acknowledged, and a reviewer change also reaches
//     the active turn through turn/settings/update.
//   - Codex lacks the method (an older Codex): the values stay. The next turn/start
//     carries them, and its turn/started acknowledges them.
//   - Codex refuses: the prior values return, so the snapshot reports the refusal.
//   - The delivery is uncertain: the values stay, and it returns true, so the caller
//     reports both permission axes unresolved.
func (a *Agent) applyPermissionSettingsLive(threadID, turnID string, prior codexPermissionSettings, priorAcknowledged bool, settings turnSettings) (unresolved bool) {
	requested := settings.permissions
	params := map[string]interface{}{"threadId": threadID}
	addCodexTurnSettingsParams(params, settings)
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		slog.Error("codex marshal thread/settings/update params", "agent_id", a.AgentID(), "error", err)
		return true
	}
	_, err = a.SendRequest(codexMethodThreadSettingsUpdate, paramsJSON, codexSettingsUpdateTimeout)
	switch classifyCodexSettingsUpdate(err) {
	case codexSettingsUpdateAccepted:
		a.acknowledgePermissions(requested)
		_, priorReviewer := codexApprovalWire(prior.mode)
		_, reviewer := codexApprovalWire(requested.mode)
		if turnID != "" && reviewer != priorReviewer {
			return a.updateActiveTurnReviewer(threadID, turnID, reviewer)
		}
		return false
	case codexSettingsUpdateUnsupported:
		slog.Info("codex lacks thread/settings/update; the next turn applies the permission settings",
			"agent_id", a.AgentID())
		return false
	case codexSettingsUpdateRefused:
		slog.Warn("codex refused the permission settings", "agent_id", a.AgentID(),
			"permissionMode", requested.mode, "permissionProfile", requested.profile, "error", err)
		a.Mu.Lock()
		if a.permissionSettingsLocked() == requested {
			a.setPermissionSettingsLocked(prior)
			a.permissionsAcknowledged = priorAcknowledged
		}
		a.Mu.Unlock()
		return false
	default:
		slog.Warn("codex did not confirm the permission settings", "agent_id", a.AgentID(), "error", err)
		return true
	}
}

// codexSettingsUpdateOutcome is what a thread/settings/update reply states about the
// settings that it carried.
type codexSettingsUpdateOutcome uint8

const (
	// codexSettingsUpdateAccepted: Codex holds the settings now.
	codexSettingsUpdateAccepted codexSettingsUpdateOutcome = iota + 1
	// codexSettingsUpdateUnsupported: an older Codex lacks the method, so the next
	// turn/start carries the settings.
	codexSettingsUpdateUnsupported
	// codexSettingsUpdateRefused: Codex answered and refused the settings, for example
	// because the managed requirements forbid them.
	codexSettingsUpdateRefused
	// codexSettingsUpdateUncertain: no answer arrived, so Codex can hold either value.
	codexSettingsUpdateUncertain
)

// classifyCodexSettingsUpdate reads the error of a thread/settings/update request.
// Only a JSON-RPC answer is a refusal: a timeout or a failed write states nothing
// about what Codex holds.
func classifyCodexSettingsUpdate(err error) codexSettingsUpdateOutcome {
	var response *providerkit.JSONRPCResponseError
	switch {
	case err == nil:
		return codexSettingsUpdateAccepted
	case providerkit.HasJSONRPCErrorCode(err, providerkit.JSONRPCErrMethodNotFound):
		return codexSettingsUpdateUnsupported
	case errors.As(err, &response):
		return codexSettingsUpdateRefused
	default:
		return codexSettingsUpdateUncertain
	}
}

// updateActiveTurnReviewer reports unresolved delivery only while the requested turn remains active.
// The thread already holds the new reviewer for later turns.
func (a *Agent) updateActiveTurnReviewer(threadID, turnID, reviewer string) (unresolved bool) {
	paramsJSON, err := json.Marshal(map[string]interface{}{
		"threadId":          threadID,
		"turnId":            turnID,
		"approvalsReviewer": reviewer,
	})
	if err != nil {
		slog.Error("codex marshal turn/settings/update params", "agent_id", a.AgentID(), "error", err)
	} else {
		var response json.RawMessage
		response, err = a.SendRequest(codexMethodTurnSettingsUpdate, paramsJSON, codexSettingsUpdateTimeout)
		if err == nil {
			var result struct {
				Status string `json:"status"`
			}
			err = json.Unmarshal(response, &result)
			if err == nil {
				if result.Status == "applied" || result.Status == "targetUnavailable" {
					return false
				}
				err = fmt.Errorf("unexpected active settings status %q", result.Status)
			}
		}
	}
	slog.Warn("codex did not confirm the active reviewer", "agent_id", a.AgentID(), "turn_id", turnID, "error", err)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.threadID == threadID && a.turnID == turnID
}

// The settings methods that LeapMux sends. Only the worker sends them, so they are
// not in the shared protocol contract.
const (
	codexMethodThreadSettingsUpdate = "thread/settings/update"
	codexMethodTurnSettingsUpdate   = "turn/settings/update"
)

func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

// publishSettings broadcasts the active thread and turn settings. config/read
// reports global layers and cannot confirm active overrides.
func (a *Agent) publishSettings() {
	a.Mu.Lock()
	for _, ax := range codexAxes {
		if ax.refreshFallback != nil {
			ax.refreshFallback(a)
		}
	}
	vals := a.codexAxisValuesLocked()
	a.Mu.Unlock()

	slog.Info("codex agent settings published",
		"agent_id", a.AgentID(),
		"model", vals[agent.OptionIDModel],
		"effort", vals[agent.OptionIDEffort],
		"permissionMode", vals[agent.OptionIDPermissionMode],
		"permissionProfile", vals[contracts.CodexOptionPermissionProfile],
		"collaborationMode", vals[contracts.CodexOptionCollaborationMode],
		"serviceTier", vals[contracts.CodexOptionServiceTier],
	)

	a.sink.PersistSettingsRefresh(vals)
}
