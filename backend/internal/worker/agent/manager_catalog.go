package agent

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
)

// cachedCatalog holds the last catalog and the model that supplies it.
// Offline reads can reuse or rebuild groups from that model.
// registration identifies the runtime that supplied a live sample.
// A persisted catalog has no registration.
// Only that same registration can reuse the sample for a publication.
type cachedCatalog struct {
	groups       []*leapmuxv1.AvailableOptionGroup
	model        string
	registration *agentRegistration
}

// OptionGroups returns every configuration axis for an agent as config option
// groups, preferring the running provider's runtime groups, then cached groups,
// then static defaults. The model group's default badge is re-derived on every
// read (it depends on the LEAPMUX_*_DEFAULT_MODEL operator override, which is
// not intrinsic catalog data and must not be persisted stale).
// currentModel is the agent's persisted or selected model ID. When no non-empty
// live catalog is available, it selects the static fallback groups and determines
// whether cached model-dependent groups need rebuilding. This also applies when a
// running provider returns a transiently empty catalog. Rebuilt groups use the
// selected model's effort tiers and omit effort for a model without them when the
// cached or static catalog lists that model. A non-empty live catalog already
// carries its own per-model groups and is not overridden by currentModel.
// Pass "" when the model is unknown: an existing cache is trusted as-is, and the
// static fallback uses the provider default.
// OptionGroups returns the agent's option-group catalog: the running provider's live catalog,
// the cached catalog, or the static fallback, in that order of preference.
//
// The returned slice and its group pointers are READ-ONLY and may alias the in-memory cached
// catalog (and, in the live case, the provider's own snapshot). Callers must clone-on-write
// before mutating a group (as overlayOptionGroupCurrents / withModelGroupDefaultMarked do) --
// appending to or mutating the result in place would corrupt the catalog served to every other
// reader.
func (m *Manager) OptionGroups(agentID string, provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	groups, running, cached := m.resolveLiveCatalog(agentID, provider, currentModel)
	if running {
		return groups
	}
	return m.registry.withModelGroupDefaultMarked(m.registry.optionGroupsFromCached(cached, provider, currentModel), provider)
}

// LiveOptionGroups samples one live provider's catalog for a settings publication.
// It returns nil if the provider exits or changes during the sample.
// An empty sample uses only that process's existing live cache.
// It uses no persisted catalog or static fallback.
// The returned groups are read-only, as OptionGroups documents.
// The process can exit after this snapshot, so a publication must carry no lifecycle status.
func (m *Manager) LiveOptionGroups(agentID string, provider leapmuxv1.AgentProvider) []*leapmuxv1.AvailableOptionGroup {
	m.mu.RLock()
	entry := m.agents[agentID]
	p := entry.providerOrNil()
	exiting := entry != nil && entry.exiting
	m.mu.RUnlock()
	if p == nil || exiting {
		return nil
	}

	live := p.OptionGroups()
	m.mu.Lock()
	exiting = entry.exiting
	if m.agents[agentID] != entry || exiting {
		m.mu.Unlock()
		return nil
	}
	if len(live) > 0 {
		m.cachedOptionGroups[agentID] = cachedCatalog{groups: live, model: optionids.CurrentValue(live, OptionIDModel), registration: entry}
	} else {
		cached := m.cachedOptionGroups[agentID]
		if cached.registration == entry {
			live = cached.groups
		} else {
			live = nil
		}
	}
	m.mu.Unlock()
	return m.registry.withModelGroupDefaultMarked(live, provider)
}

// resolveLiveCatalog returns a RUNNING agent's served option-group catalog -- the provider's live
// catalog (refreshing the shared cache), or, on a transiently-empty live read, the shared cached
// catalog -- with the model default re-marked. `running` reports whether the agent was registered;
// when false, `groups` is nil and the caller resolves the NOT-running catalog from its own source:
// the shared cache for OptionGroups, the caller's row snapshot for OptionGroupsForRow. `cached` is
// the snapshot read under the same lock, so a not-running OptionGroups caller stays self-consistent
// with it. Extracting the running-agent resolution into one place keeps the two callers' live/cache
// precedence (and the cache refresh) from drifting.
func (m *Manager) resolveLiveCatalog(agentID string, provider leapmuxv1.AgentProvider, currentModel string) (groups []*leapmuxv1.AvailableOptionGroup, running bool, cached cachedCatalog) {
	m.mu.RLock()
	entry, ok := m.agents[agentID]
	p := entry.providerOrNil()
	cached = m.cachedOptionGroups[agentID]
	m.mu.RUnlock()
	if !ok {
		return nil, false, cached
	}
	// Snapshot the live catalog once: each p.OptionGroups() call re-locks the provider and rebuilds
	// the slice, and calling it twice could also observe two different catalogs across a concurrent
	// refresh.
	if live := p.OptionGroups(); len(live) > 0 {
		m.refreshCachedCatalog(agentID, entry, live)
		return m.registry.withModelGroupDefaultMarked(live, provider), true, cached
	}
	// Transiently-empty live read: serve the freshest known cached catalog (refreshCachedCatalog
	// keeps it), NOT a caller's persisted snapshot.
	return m.registry.withModelGroupDefaultMarked(m.registry.optionGroupsFromCached(cached, provider, currentModel), provider), true, cached
}

// OptionGroupsForRow returns an agent's option-group catalog, preferring a running agent's live
// catalog and otherwise building from `persisted` -- the CALLER'S OWN row snapshot -- rather than
// the shared per-agent cache. For a NOT-running agent the row is authoritative (the cache entry was
// dropped on exit and is only ever re-seeded from per-caller row snapshots via PreloadCache), so
// reading the shared cache races concurrent readers holding different snapshots: last-writer-wins
// could install a staler catalog that a broadcast/proto then serves. Sourcing the not-running
// catalog from the caller's own snapshot makes each read self-consistent with the row that produced
// it. It still warms the shared cache so the internal OptionGroups readers stay seeded.
//
// The returned slice and its group pointers are READ-ONLY, exactly as OptionGroups documents: a
// not-running result may alias the caller's own `persisted` slice (and, via the warmed cache, the
// shared cached catalog), so a caller that mutates a group must clone-on-write first.
func (m *Manager) OptionGroupsForRow(agentID string, provider leapmuxv1.AgentProvider, currentModel string, persisted []*leapmuxv1.AvailableOptionGroup) []*leapmuxv1.AvailableOptionGroup {
	// For a RUNNING agent the live catalog (or the shared cache on a transiently empty live read)
	// is authoritative, exactly as OptionGroups resolves it -- shared via resolveLiveCatalog so the
	// two can't drift. Only the NOT-running source differs: the caller's own row snapshot below,
	// rather than the shared cache.
	if groups, running, _ := m.resolveLiveCatalog(agentID, provider, currentModel); running {
		return groups
	}
	m.PreloadCache(agentID, persisted)
	rowCached := cachedCatalog{groups: persisted, model: optionids.CurrentValue(persisted, OptionIDModel)}
	// Surface the row's model even when no selectable model group was built (a dynamic-model ACP
	// provider with a model-default env override but no discovered catalog), so the remote CLI's
	// by-id model read doesn't report "" for a model the row holds.
	return ensureModelGroup(m.registry.withModelGroupDefaultMarked(m.registry.optionGroupsFromCached(rowCached, provider, currentModel), provider), currentModel)
}

// refreshCachedCatalog keeps the cache coherent with the live catalog while the agent
// runs. StartAgent seeds it once (and only when the start-time catalog was non-empty), but
// ACP dynamic-model providers report their models only after the handshake, and live
// setting changes mutate the catalog afterward. Refreshing here means that if the running
// provider later returns a transiently EMPTY live catalog (OptionGroups' fallback case), we
// serve the freshest known catalog rather than the stale start-time one -- or none at all
// for a provider that started empty. Identity-checked so a concurrent restart's new
// provider isn't clobbered. (The entry is dropped on exit, so this does not carry the
// catalog into the post-exit offline window; that is served from the persisted
// option_groups column.)
func (m *Manager) refreshCachedCatalog(agentID string, entry *agentRegistration, live []*leapmuxv1.AvailableOptionGroup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agents[agentID] == entry {
		m.cachedOptionGroups[agentID] = cachedCatalog{groups: live, model: optionids.CurrentValue(live, OptionIDModel), registration: entry}
	}
}

// PreloadCache populates the cached option groups for an agent that is not
// currently running. This restores DB-persisted catalog data so that
// OptionGroups returns the correct values without the agent process being active.
func (m *Manager) PreloadCache(agentID string, groups []*leapmuxv1.AvailableOptionGroup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Never clobber a running (or concurrently-starting) agent's cache with a persisted-row
	// snapshot: callers gate on HasAgent first, but that check and this write aren't atomic,
	// so a StartAgent/RestartAgent that registered the agent and seeded a fresh, model-correct
	// cache in between would otherwise be reverted to the stale persisted stamp. Re-checking
	// membership under the same lock closes that window -- a live agent's catalog (refreshed in
	// OptionGroups) is authoritative over anything we would preload here.
	if _, running := m.agents[agentID]; running {
		return
	}
	if len(groups) > 0 {
		// Stamp the entry with the model the groups were built for (the model group's
		// current value), so OptionGroups can detect a since-changed model and rebuild.
		m.cachedOptionGroups[agentID] = cachedCatalog{groups: groups, model: optionids.CurrentValue(groups, OptionIDModel)}
	}
}
