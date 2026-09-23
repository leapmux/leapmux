package agent

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// This file gives the external tests of package agent the internals that they
// read. The external tests exist because they use agenttest, and agenttest
// imports agent, so an in-package test cannot import it.
//
// Each hook takes the lock that guards the state that it touches. A test thus
// cannot read or write the Manager's maps without the lock.

// StartBackgroundAgentWithForTest starts an agent through start as a background
// spawn. A background spawn waits for a permit from the startup pool.
// StartAgentWith starts a spawn that the user asked for, which takes no permit.
func (m *Manager) StartBackgroundAgentWithForTest(ctx context.Context, opts Options, sink ProviderServices, start StartFunc) (map[string]string, error) {
	return m.startAgentWith(ctx, opts, sink, start, true)
}

// AgentForTest returns the agent that the manager holds under agentID.
func (m *Manager) AgentForTest(agentID string) (Agent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.agents[agentID]
	return a, ok
}

// SeedCachedCatalogForTest stores groups as the cached catalog of agentID,
// stamped with model. An empty model leaves the cache unstamped.
func (m *Manager) SeedCachedCatalogForTest(agentID string, groups []*leapmuxv1.AvailableOptionGroup, model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cachedOptionGroups[agentID] = cachedCatalog{groups: groups, model: model}
}

// CachedCatalogForTest returns the cached catalog of agentID and its model
// stamp. ok is false when the manager caches no catalog for agentID.
func (m *Manager) CachedCatalogForTest(agentID string) (groups []*leapmuxv1.AvailableOptionGroup, model string, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.cachedOptionGroups[agentID]
	return c.groups, c.model, ok
}

// ProviderHasModelDependentGroupsForTest reports whether a model change
// invalidates the cached per-model groups of provider.
func (r *Registry) ProviderHasModelDependentGroupsForTest(provider leapmuxv1.AgentProvider) bool {
	return r.providerHasModelDependentGroups(provider)
}

// WithModelGroupDefaultMarkedForTest derives the DefaultValue of the model group
// in groups again, as the Manager does before it serves a catalog.
func (r *Registry) WithModelGroupDefaultMarkedForTest(groups []*leapmuxv1.AvailableOptionGroup, provider leapmuxv1.AgentProvider) []*leapmuxv1.AvailableOptionGroup {
	return r.withModelGroupDefaultMarked(groups, provider)
}
