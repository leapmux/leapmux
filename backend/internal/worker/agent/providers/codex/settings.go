package codex

import (
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Codex sandbox policy values.
const (
	SandboxDangerFullAccess = "danger-full-access"
	SandboxWorkspaceWrite   = "workspace-write"
	SandboxReadOnly         = "read-only"
)

// Codex network access values.
const (
	NetworkRestricted = "restricted"
	NetworkEnabled    = "enabled"
)

// Codex collaboration mode values.
const (
	CollaborationDefault = "default"
	CollaborationPlan    = "plan"
)

// Codex service tier values.
const ServiceTierFast = "fast"

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
	// defaultValue is the Codex-specific default resolveProviderDefaults stamps for an
	// provider option axis (sandbox/network/collaboration/service-tier). Empty for model, effort,
	// and approval, which are defaulted by the shared model/effort/permission logic.
	defaultValue string
	// lifecyclePolicy states whether thread/start and thread/resume own this axis.
	// Effort is authoritative only while its requested value is automatic.
	lifecyclePolicy  codexLifecyclePolicy
	lifecycleDefault string
}

var codexAxes = []codexAxis{
	{id: agent.OptionIDModel, get: func(a *Agent) string { return a.model }, set: func(a *Agent, v string) { a.model = v }, lifecyclePolicy: codexLifecycleAuthoritative},
	{id: agent.OptionIDEffort, get: func(a *Agent) string { return a.effort }, set: func(a *Agent, v string) { a.effort = v }, refreshFallback: codexEffortRefreshFallback, lifecyclePolicy: codexLifecycleWhenAutomatic, lifecycleDefault: agent.EffortAuto},
	{id: agent.OptionIDPermissionMode, get: func(a *Agent) string { return a.approvalPolicy }, set: func(a *Agent, v string) { a.approvalPolicy = v }, lifecyclePolicy: codexLifecycleAuthoritative},
	{id: contracts.CodexOptionSandboxPolicy, get: func(a *Agent) string { return a.sandboxPolicy }, set: func(a *Agent, v string) { a.sandboxPolicy = v }, defaultValue: contracts.CodexOptionDefaultSandboxPolicy, lifecyclePolicy: codexLifecycleAuthoritative},
	{id: contracts.CodexOptionNetworkAccess, get: func(a *Agent) string { return a.networkAccess }, set: func(a *Agent, v string) { a.networkAccess = v }, defaultValue: contracts.CodexOptionDefaultNetworkAccess, lifecyclePolicy: codexLifecycleAuthoritative},
	{id: contracts.CodexOptionCollaborationMode, get: func(a *Agent) string { return a.collaborationMode }, set: func(a *Agent, v string) { a.collaborationMode = v }, defaultValue: contracts.CodexOptionDefaultCollaborationMode},
	{id: contracts.CodexOptionServiceTier, get: func(a *Agent) string { return a.serviceTier }, set: func(a *Agent, v string) { a.serviceTier = v }, defaultValue: contracts.CodexOptionDefaultServiceTier, lifecyclePolicy: codexLifecycleAuthoritative, lifecycleDefault: contracts.CodexOptionDefaultServiceTier},
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

// OptionGroups returns the model and effort groups plus the static Codex
// option groups (service tier, collaboration mode, approval policy, sandbox,
// network), each overlaid with the agent's confirmed current value.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	vals := a.codexAxisValuesLocked()
	models := a.availableModels
	a.Mu.Unlock()

	groups := providerkit.ModelAndEffortGroups(models, vals[agent.OptionIDModel], vals[agent.OptionIDEffort], agent.EffortGroupLabel, nil)

	// Current values are sourced per-axis from the snapshot; the display order is
	// carried on each registered template (so a newly-registered group can't lose its
	// order or sort ahead of the model group), and providerkit.LiveGroup defaults an unsupplied
	// current to the template's default. The model/effort entries in vals are unused
	// here -- they are rendered by providerkit.ModelAndEffortGroups above.
	for _, sg := range codexStaticOptionGroups {
		groups = append(groups, providerkit.LiveGroup(sg, vals[sg.GetId()]))
	}
	return groups
}

// UpdateSettings stores new settings so the next turn/start picks them up.
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
	a.Mu.Unlock()

	a.publishSettings()
	return a.SettingsSnapshot()
}

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
		"approvalPolicy", vals[agent.OptionIDPermissionMode],
		"sandboxPolicy", vals[contracts.CodexOptionSandboxPolicy],
		"networkAccess", vals[contracts.CodexOptionNetworkAccess],
		"collaborationMode", vals[contracts.CodexOptionCollaborationMode],
		"serviceTier", vals[contracts.CodexOptionServiceTier],
	)

	a.sink.PersistSettingsRefresh(vals)
}
