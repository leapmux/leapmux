package mimo

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// MiMo keeps no current selection for a session: every prompt states the agent,
// the model and the variant it runs on. So the model, the effort and the mode
// are LeapMux's own state, and a change takes effect at the next prompt with no
// request to MiMo. The permission policy is the one axis the server holds, as
// two runtime switches that a change sets at once.

// mimoSettings is one complete choice of the four axes.
type mimoSettings struct {
	model            string
	effort           string
	mode             string
	permissionPolicy string
}

// settingsLocked reads the current choice. The caller holds a.Mu.
func (a *Agent) settingsLocked() mimoSettings {
	return mimoSettings{model: a.model, effort: a.effort, mode: a.mode, permissionPolicy: a.permissionPolicy}
}

// OptionGroups returns the model, the model's effort, the mode and the
// permission policy.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	catalog, current := a.catalog, a.settingsLocked()
	a.Mu.Unlock()
	groups := providerkit.ModelAndEffortGroups(catalog.models, current.model, current.effort, agent.EffortGroupLabel, nil)
	modes := catalog.modes
	if len(modes) == 0 {
		modes = mimoStaticModes
	}
	return append(groups, mimoModeGroup(current.mode, modes), mimoPermissionPolicyGroup(current.permissionPolicy))
}

// SettingsSnapshot confirms what every group states now.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

// UpdateSettings applies the requested axes live.
//
// A value the server does not offer -- a model outside the catalog, an agent
// the server does not list -- is not applied, and the snapshot reports the
// value that stays. A permission policy the server refuses leaves the old
// policy in place, and asks for a restart, which applies the policy at startup.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.Mu.Lock()
	catalog, current := a.catalog, a.settingsLocked()
	a.Mu.Unlock()
	next := resolveSettings(catalog, current, options)

	if next.permissionPolicy != current.permissionPolicy {
		if err := a.applyPermissionPolicy(a.Context(), next.permissionPolicy); err != nil {
			slog.Warn("mimo permission policy change failed; restarting to apply", "agent_id", a.AgentID(), "policy", next.permissionPolicy, "error", err)
			// The first switch can take effect before the second one fails. The
			// server must not run a policy that the agent does not report, so
			// the old policy goes back until the restart applies the new one.
			if restoreErr := a.applyPermissionPolicy(a.Context(), current.permissionPolicy); restoreErr != nil {
				slog.Warn("mimo restore the permission policy", "agent_id", a.AgentID(), "policy", current.permissionPolicy, "error", restoreErr)
			}
			return agent.RestartRequiredSettings(options)
		}
	}

	a.Mu.Lock()
	a.model, a.effort, a.mode, a.permissionPolicy = next.model, next.effort, next.mode, next.permissionPolicy
	a.Mu.Unlock()
	a.sink.PersistSettingsRefresh(optionmap.Map{
		agent.OptionIDModel:                  next.model,
		agent.OptionIDEffort:                 next.effort,
		agent.OptionIDPermissionMode:         next.mode,
		contracts.MiMoOptionPermissionPolicy: next.permissionPolicy,
	})
	return a.SettingsSnapshot()
}

// resolveSettings folds the requested options over current, keeping each axis
// whose request the catalog cannot serve. An effort the settled model does not
// offer falls back to Auto, the model's own default.
func resolveSettings(catalog mimoCatalog, current mimoSettings, options optionmap.Map) mimoSettings {
	next := current
	if requested := options[agent.OptionIDModel]; requested != "" {
		if model := catalog.resolveModel(requested); model != "" {
			next.model = model
		}
	}
	if requested := options[agent.OptionIDEffort]; requested != "" {
		next.effort = requested
	}
	next.effort = settleEffort(catalog, next.model, next.effort)
	if requested := options[agent.OptionIDPermissionMode]; requested != "" && catalogOffersMode(catalog, requested) {
		next.mode = requested
	}
	if requested := options[contracts.MiMoOptionPermissionPolicy]; requested != "" && isPermissionPolicy(requested) {
		next.permissionPolicy = requested
	}
	return next
}

// settleEffort returns effort when model offers it, else Auto for a model with
// variants and "" for a model without.
func settleEffort(catalog mimoCatalog, model, effort string) string {
	info := agent.FindAvailableModel(catalog.models, model)
	if info == nil || len(info.SupportedEfforts) == 0 {
		return ""
	}
	for _, level := range info.SupportedEfforts {
		if level.GetId() == effort {
			return effort
		}
	}
	return agent.EffortAuto
}

func catalogOffersMode(catalog mimoCatalog, mode string) bool {
	if len(catalog.modes) == 0 {
		for _, def := range mimoStaticModes {
			if def.Id == mode {
				return true
			}
		}
		return false
	}
	return catalog.hasMode(mode)
}

func isPermissionPolicy(policy string) bool {
	for _, def := range mimoPermissionPolicies {
		if def.Id == policy {
			return true
		}
	}
	return false
}

// applyPermissionPolicy sets the server's two switches for policy. Each route
// sets its switch to a value, so a repeat is harmless.
func (a *Agent) applyPermissionPolicy(ctx context.Context, policy string) error {
	skipAll, approveDelete := false, false
	switch policy {
	case contracts.MiMoPermissionPolicyAsk:
	case contracts.MiMoPermissionPolicySkip:
		skipAll = true
	case contracts.MiMoPermissionPolicyBypass:
		skipAll, approveDelete = true, true
	default:
		return fmt.Errorf("unknown permission policy %q", policy)
	}
	if err := a.rpc.setSkipAll(ctx, skipAll); err != nil {
		return fmt.Errorf("set skip-all: %w", err)
	}
	if err := a.rpc.setAutoApproveDelete(ctx, approveDelete); err != nil {
		return fmt.Errorf("set auto-approve-delete: %w", err)
	}
	return nil
}

// adoptMode records a mode that MiMo moved the session to by itself, which a
// plan approval does. The next prompt must state it, or it would move the
// session back.
func (a *Agent) adoptMode(mode string) {
	a.Mu.Lock()
	changed := a.mode != mode
	a.mode = mode
	a.Mu.Unlock()
	if changed {
		a.sink.UpdatePermissionMode(mode)
	}
}
