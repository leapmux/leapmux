package service

import (
	"fmt"
	"maps"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// persistOptionChanges changes only the supplied keys. Concurrent provider updates retain their other keys.
// The caller decides whether values are preferences or confirmed live settings.
func (svc *Service) persistOptionChanges(current db.Agent, previous, values OptionMap, notifyFirstSet bool) (db.Agent, error) {
	delta := make(OptionMap, len(previous))
	for key := range previous {
		delta[key] = values[key]
	}
	if len(delta) == 0 {
		return current, nil
	}
	settled, _, err := casPersistAgentOptions(bgCtx(), svc.Queries, current.ID, current.Options, delta)
	if err != nil {
		return current, fmt.Errorf("persist agent settings: %w", err)
	}
	current.Options = settled
	svc.broadcastSettingsStatusChange(current)
	changes := svc.buildSettingsChanges(&current, previous, values, sortedOptionKeys(delta), notifyFirstSet)
	if len(changes) > 0 {
		svc.Output.PersistLeapMuxNotification(current.ID, current.AgentProvider, map[string]interface{}{
			"type": contracts.NotificationTypeSettingsChanged, "changes": changes,
		})
	}
	return current, nil
}

// applyPlanOptionsLocked requires exact confirmations from a running provider.
// A stopped provider receives stored preferences that its next launch must confirm.
// The caller holds the agent's lifecycle lock and validates its session before this call.
func (svc *Service) applyPlanOptionsLocked(current db.Agent, wanted OptionMap) (db.Agent, error) {
	if len(wanted) == 0 {
		return current, nil
	}
	if svc.Agents.HasAgent(current.ID) {
		result := svc.updateAgentSettingsFn(current.ID, maps.Clone(wanted))
		if !result.AppliedLive || result.SurfacedOptions == nil {
			return current, fmt.Errorf("the provider did not confirm the plan settings")
		}
		// Every requested axis needs an EXACT confirmation, and a key with no
		// settlement at all fails here too: an absent entry carries a nil Value,
		// and it also carries the ZERO OptionSettlementState, which is neither
		// Confirmed nor Unresolved -- OptionSettlementConfirmed is iota + 1. Both
		// terms below are load-bearing; neither rejects the absent key alone. An
		// axis the provider did
		// not confirm is an axis the running process may still hold at its old
		// value, and the plan would then execute under settings nobody chose --
		// Codex's collaboration_mode is the plan-mode axis itself, so an
		// unconfirmed one leaves the agent planning instead of executing.
		for key, value := range wanted {
			settlement := result.Settlements[key]
			if settlement.State != agent.OptionSettlementConfirmed || settlement.Value == nil || *settlement.Value != value {
				return current, fmt.Errorf("the provider did not confirm the %s plan setting", key)
			}
		}
	}
	values := loadOptions(current.Options, current.AgentProvider)
	previous := make(OptionMap, len(wanted))
	for key, value := range wanted {
		previous[key] = values[key]
		values[key] = value
	}
	return svc.persistOptionChanges(current, previous, values, true)
}
