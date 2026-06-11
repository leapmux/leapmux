package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"google.golang.org/protobuf/proto"
)

// ErrAgentNotFound is returned when an agent process does not exist.
var ErrAgentNotFound = errors.New("agent not found")

// Manager tracks active agents and routes messages.
type Manager struct {
	mu                 sync.RWMutex
	agents             map[string]Agent                             // agentID -> Agent
	cachedModels       map[string][]*leapmuxv1.AvailableModel       // agentID -> last known models
	cachedOptionGroups map[string][]*leapmuxv1.AvailableOptionGroup // agentID -> last known option groups
	lifecycleLocks     map[string]*lifecycleEntry                   // agentID -> refcounted mutex
	onExit             ExitHandler
}

// lifecycleEntry is a per-agent mutex whose refcount is guarded by Manager.mu.
// Entries are evicted when no caller holds or is waiting for the lock.
type lifecycleEntry struct {
	mu       sync.Mutex
	refcount int
}

// NewManager creates a new agent Manager.
// The optional onExit handler is called when any agent process exits.
func NewManager(onExit ExitHandler) *Manager {
	return &Manager{
		agents:             make(map[string]Agent),
		cachedModels:       make(map[string][]*leapmuxv1.AvailableModel),
		cachedOptionGroups: make(map[string][]*leapmuxv1.AvailableOptionGroup),
		lifecycleLocks:     make(map[string]*lifecycleEntry),
		onExit:             onExit,
	}
}

// SetOnExit replaces the exit handler. The runner uses this to wire a
// service-aware handler (which has access to OutputHandler / DB queries)
// after the service.Context is constructed. The handler is read inside
// the per-agent Wait goroutine under m.mu so a concurrent swap is
// observed atomically by every in-flight exit.
func (m *Manager) SetOnExit(onExit ExitHandler) {
	m.mu.Lock()
	m.onExit = onExit
	m.mu.Unlock()
}

// LockAgent acquires a per-agent mutex that serializes multi-step lifecycle
// operations (typically stop-then-start) against concurrent callers. Without
// this, a second restart can slip in between the first's stop and start and
// race the "agent already running" check in StartAgent. The returned function
// releases the lock — callers should defer it.
func (m *Manager) LockAgent(agentID string) func() {
	m.mu.Lock()
	entry, ok := m.lifecycleLocks[agentID]
	if !ok {
		entry = &lifecycleEntry{}
		m.lifecycleLocks[agentID] = entry
	}
	entry.refcount++
	m.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		m.mu.Lock()
		entry.refcount--
		if entry.refcount == 0 {
			delete(m.lifecycleLocks, agentID)
		}
		m.mu.Unlock()
	}
}

// RestartAgent atomically stops any running agent for opts.AgentID, waits
// for it to fully exit, then starts a new one. Concurrent restarts for the
// same agent ID are serialized via LockAgent. Callers that need to interleave
// work between stop and start should use LockAgent directly.
func (m *Manager) RestartAgent(ctx context.Context, opts Options, sink OutputSink) (*leapmuxv1.AgentSettings, error) {
	unlock := m.LockAgent(opts.AgentID)
	defer unlock()

	m.stopAndWait(opts.AgentID, false)
	return m.StartAgent(ctx, opts, sink)
}

// startFunc is the function signature for starting an agent process.
type startFunc func(ctx context.Context, opts Options, sink OutputSink) (Agent, error)

// StartAgent spawns an agent for the given agent ID, dispatching based on
// opts.AgentProvider.
// The sink receives parsed output events.
// Returns the confirmed settings from the startup handshake (e.g. permission
// mode, discovered model).
func (m *Manager) StartAgent(ctx context.Context, opts Options, sink OutputSink) (*leapmuxv1.AgentSettings, error) {
	reg, ok := agentFactoryRegistry[opts.AgentProvider]
	if !ok {
		return nil, fmt.Errorf("unsupported agent provider: %v", opts.AgentProvider)
	}
	return m.startAgentWith(ctx, opts, sink, reg.start)
}

func (m *Manager) startAgentWith(ctx context.Context, opts Options, sink OutputSink, start startFunc) (*leapmuxv1.AgentSettings, error) {
	m.mu.Lock()
	if _, exists := m.agents[opts.AgentID]; exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("agent already running for agent %s", opts.AgentID)
	}
	m.mu.Unlock()

	provider, err := start(ctx, opts, sink)
	if err != nil {
		return nil, err
	}

	confirmedSettings := provider.CurrentSettings()

	m.mu.Lock()
	m.agents[opts.AgentID] = provider
	if models := provider.AvailableModels(); len(models) > 0 {
		m.cachedModels[opts.AgentID] = models
	}
	if groups := provider.AvailableOptionGroups(); len(groups) > 0 {
		m.cachedOptionGroups[opts.AgentID] = groups
	}
	m.mu.Unlock()

	// Wait for the agent to exit in the background, then clean up.
	go func() {
		err := provider.Wait()
		m.mu.Lock()
		delete(m.agents, opts.AgentID)
		delete(m.cachedModels, opts.AgentID)
		delete(m.cachedOptionGroups, opts.AgentID)
		m.mu.Unlock()

		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}

		if provider.IsStopped() {
			slog.Info("agent stopped",
				"agent_id", opts.AgentID,
			)
		} else if err != nil {
			stderr := provider.Stderr()
			slog.Warn("agent exited with error",
				"agent_id", opts.AgentID,
				"error", err,
				"stderr", stderr,
			)
		} else {
			slog.Info("agent exited",
				"agent_id", opts.AgentID,
			)
		}

		m.mu.RLock()
		onExit := m.onExit
		m.mu.RUnlock()
		if onExit != nil {
			onExit(opts.AgentID, exitCode, err)
		}
	}()

	return confirmedSettings, nil
}

// SendInput routes a user message to the specified agent.
func (m *Manager) SendInput(agentID, content string, attachments []*leapmuxv1.Attachment) error {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}

	return p.SendInput(content, attachments)
}

// SendRawInput writes raw bytes directly to the specified agent's stdin
// without wrapping in a UserInputMessage.
func (m *Manager) SendRawInput(agentID string, data []byte) error {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}

	return p.SendRawInput(data)
}

// Interrupt aborts the agent's current turn using the provider-specific
// signal. Returns ErrAgentNotFound when the agent isn't running.
func (m *Manager) Interrupt(agentID string) error {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}
	return p.Interrupt()
}

// StopAgent stops the agent with the given agent ID.
// Returns true if the agent was found (and will eventually trigger onExit),
// false if the agent had already exited.
func (m *Manager) StopAgent(agentID string) bool {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	m.mu.RUnlock()

	if ok {
		p.Stop()
	}
	return ok
}

// StopAndWaitAgent stops the agent and waits for it to fully exit and be
// removed from the manager's map. This is necessary before restarting an
// agent to avoid the "agent already running" error from StartAgent.
// Returns true if the agent was found and stopped, false if it was not running.
func (m *Manager) StopAndWaitAgent(agentID string) bool {
	return m.stopAndWait(agentID, false)
}

// DiscardOutputAndStopAgent marks the agent to discard remaining output,
// then stops and waits for it to exit. Use this when restarting an agent
// (e.g. plan execution) to avoid persisting spurious error messages from
// closed streams.
func (m *Manager) DiscardOutputAndStopAgent(agentID string) bool {
	return m.stopAndWait(agentID, true)
}

func (m *Manager) stopAndWait(agentID string, discardOutput bool) bool {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	m.mu.RUnlock()

	if !ok {
		return false
	}

	if discardOutput {
		p.DiscardOutput()
	}
	p.Stop()
	_ = p.Wait()

	// Remove the map entry eagerly so that StartAgent can proceed
	// immediately. The background goroutine's delete will be a no-op.
	m.mu.Lock()
	delete(m.agents, agentID)
	m.mu.Unlock()

	return true
}

// ClearContext attempts to clear the agent's context in-place (e.g. by
// starting a new Codex thread). Returns the new session ID and true if
// successful, or ("", false) if the provider doesn't support it.
func (m *Manager) ClearContext(agentID string) (string, bool) {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	m.mu.RUnlock()
	if !ok {
		return "", false
	}
	return p.ClearContext()
}

// AvailableModels returns the models reported by the agent process.
// Falls back to the cached model list, then to the provider's static defaults.
func (m *Manager) AvailableModels(agentID string, provider leapmuxv1.AgentProvider) []*leapmuxv1.AvailableModel {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	cached := m.cachedModels[agentID]
	m.mu.RUnlock()

	if ok {
		if models := p.AvailableModels(); len(models) > 0 {
			return withDefaultModelMarked(models, provider)
		}
	}
	if len(cached) > 0 {
		return withDefaultModelMarked(cached, provider)
	}
	if reg, ok := agentFactoryRegistry[provider]; ok {
		return withDefaultModelMarked(reg.defaultModels, provider)
	}
	return nil
}

// defaultModelForList resolves which model id should carry the IsDefault badge
// for this (possibly account-specific) list. Priority:
//  1. The explicit LEAPMUX_*_DEFAULT_MODEL operator override.
//  2. A provider-reported default the list itself designates -- for Claude Code
//     this is the CLI's DefaultModelSentinel ("default") entry, which tracks the
//     account's own default across plan tiers (e.g. Sonnet vs Fable).
//  3. The provider's configured default. When the list contains it, return it;
//     when the list omits it (an account-specific list whose configured default
//     isn't offered, e.g. a Claude CLI reporting concrete models but no "default"
//     sentinel), fall back to the highest-preference entry present so the picker
//     still shows a badge.
//
// Returning "" means "don't touch the list's existing badges": that's the case
// for a provider with no configured default at all (ACP providers registered with
// nil defaultModels, which self-mark the currently-selected model in
// buildACPModels). The step-3 fallback is gated on a non-empty configured default
// precisely so it doesn't clobber that per-agent marking.
func defaultModelForList(models []*leapmuxv1.AvailableModel, provider leapmuxv1.AgentProvider) string {
	if env := DefaultModelEnvOverride(provider); env != "" {
		// Honor the operator override only when it actually names a model in this
		// (possibly account-specific) list, matching by exact id or provider-
		// normalized alias. A stale or differently-spelled override -- a fully-
		// qualified "claude-opus-4-8[1m]" against the catalog's "opus[1m]", or a
		// model the account simply doesn't offer -- falls through to the rest of
		// the ladder so the picker still shows a default badge. Returning an absent
		// id unconditionally would make withDefaultModelMarked clear IsDefault from
		// every entry (none match) and badge nothing.
		//
		// This step outranks the sentinel (step 2) deliberately: an explicit operator
		// override naming the account's resolved concrete model (which
		// ensureSettledModelListed surfaces into the list once startup settles it)
		// badges that concrete model rather than the "default" placeholder -- the
		// sentinel does not retain the badge once a concrete identity the operator
		// pinned is present.
		if id := matchModelID(models, provider, env); id != "" {
			return id
		}
	}
	// DefaultModelSentinel is a Claude-Code-specific concept (the CLI's "default"
	// entry); gate the check to that provider so a non-Claude provider that
	// happens to report a model literally id'd "default" doesn't get its
	// self-marked/CLI-marked badge hijacked onto that entry.
	if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE && FindAvailableModel(models, DefaultModelSentinel) != nil {
		return DefaultModelSentinel
	}
	configured := DefaultModel(provider)
	if configured == "" {
		// No configured default: preserve whatever IsDefault the per-agent list
		// already set (e.g. buildACPModels marking the current model) instead of
		// moving the badge to the first entry.
		return ""
	}
	if FindAvailableModel(models, configured) != nil {
		return configured
	}
	// The configured default isn't in this (account-specific) list. Respect a
	// default the provider already designated on the list itself -- e.g. Codex's
	// queryAvailableModels copies the CLI's isDefault onto an entry -- before
	// falling back, so a stale registry default (configured but absent from the
	// live list) doesn't move the badge off the model the CLI actually marked.
	if id := markedModelID(models); id != "" {
		return id
	}
	// Nothing designated: mark the highest-preference entry the list does contain
	// so the picker always shows a default badge (e.g. a Claude CLI reporting
	// concrete models but no "default" sentinel falls back to its first model).
	return firstModelID(models)
}

// matchModelID returns the id of the model in the list the given id refers to,
// matched first by exact id then by provider-normalized alias (so a fully-
// qualified spelling like "claude-opus-4-8[1m]" resolves to the catalog's
// "opus[1m]"). Returns "" when the list contains no such model. Used to resolve
// an operator default-model override against an account-specific catalog.
func matchModelID(models []*leapmuxv1.AvailableModel, provider leapmuxv1.AgentProvider, id string) string {
	if m := FindAvailableModel(models, id); m != nil {
		return m.Id
	}
	want := NormalizeModelID(provider, id)
	for _, m := range models {
		if m != nil && NormalizeModelID(provider, m.Id) == want {
			return m.Id
		}
	}
	return ""
}

// markedModelID returns the id of the first model already flagged IsDefault, or ""
// if none is. firstModelID returns the id of the first non-nil model, or "". Both
// tolerate nil-bearing slices (the catalogs are treated as possibly nil-bearing).
func markedModelID(models []*leapmuxv1.AvailableModel) string {
	for _, m := range models {
		if m != nil && m.IsDefault {
			return m.Id
		}
	}
	return ""
}

func firstModelID(models []*leapmuxv1.AvailableModel) string {
	for _, m := range models {
		if m != nil {
			return m.Id
		}
	}
	return ""
}

func withDefaultModelMarked(models []*leapmuxv1.AvailableModel, provider leapmuxv1.AgentProvider) []*leapmuxv1.AvailableModel {
	if len(models) == 0 {
		return nil
	}

	defaultModel := defaultModelForList(models, provider)
	if defaultModel == "" {
		return models
	}

	// Fast path: if every model already has the correct IsDefault, reuse the input.
	needsCopy := false
	for _, model := range models {
		if model != nil && model.IsDefault != (model.Id == defaultModel) {
			needsCopy = true
			break
		}
	}
	if !needsCopy {
		return models
	}

	out := make([]*leapmuxv1.AvailableModel, len(models))
	for i, model := range models {
		if model == nil {
			continue
		}
		shouldBeDefault := model.Id == defaultModel
		if model.IsDefault == shouldBeDefault {
			out[i] = model
		} else {
			c := proto.Clone(model).(*leapmuxv1.AvailableModel)
			c.IsDefault = shouldBeDefault
			out[i] = c
		}
	}
	return out
}

// AvailableOptionGroups returns the option groups for an agent, preferring the
// running provider's runtime groups, then cached groups, then static defaults.
func (m *Manager) AvailableOptionGroups(agentID string, provider leapmuxv1.AgentProvider) []*leapmuxv1.AvailableOptionGroup {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	cached := m.cachedOptionGroups[agentID]
	m.mu.RUnlock()

	if ok {
		if groups := p.AvailableOptionGroups(); len(groups) > 0 {
			return groups
		}
	}
	if len(cached) > 0 {
		return cached
	}
	return AvailableOptionGroupsForProvider(provider)
}

// AvailableOptionGroupsForProvider returns the static option groups for a
// provider from the provider registry. This is a package-level function
// that does not require a Manager instance.
func AvailableOptionGroupsForProvider(provider leapmuxv1.AgentProvider) []*leapmuxv1.AvailableOptionGroup {
	if reg, ok := agentFactoryRegistry[provider]; ok {
		return reg.optionGroups
	}
	return nil
}

// PreloadCache populates the cached models and option groups for an agent
// that is not currently running. This is used to restore DB-persisted data
// so that AvailableModels/AvailableOptionGroups return the correct values
// without the agent process being active.
func (m *Manager) PreloadCache(agentID string, models []*leapmuxv1.AvailableModel, groups []*leapmuxv1.AvailableOptionGroup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(models) > 0 {
		m.cachedModels[agentID] = models
	}
	if len(groups) > 0 {
		m.cachedOptionGroups[agentID] = groups
	}
}

// UpdateSettings applies setting changes to a running agent so that
// the next turn picks them up without a restart. Returns true if the
// provider accepted the update, false if it requires a restart.
func (m *Manager) UpdateSettings(agentID string, s *leapmuxv1.AgentSettings) bool {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	return p.UpdateSettings(s)
}

// CurrentSettings returns a snapshot of the running agent's confirmed settings,
// or nil if no agent is running with that ID. The snapshot reflects the
// in-memory state that the live-update (refreshSettingsFromAgent) and restart
// paths write synchronously, so callers can read back the model/effort the
// agent actually confirmed without a DB round-trip.
func (m *Manager) CurrentSettings(agentID string) *leapmuxv1.AgentSettings {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	return p.CurrentSettings()
}

// HasAgent returns true if an agent is running with the given agent ID.
func (m *Manager) HasAgent(agentID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.agents[agentID]
	return ok
}

// ListAgentIDs returns the IDs of all currently tracked agents.
func (m *Manager) ListAgentIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.agents))
	for id := range m.agents {
		ids = append(ids, id)
	}
	return ids
}

// StopAll stops all running agents.
func (m *Manager) StopAll() {
	m.mu.Lock()
	providers := make([]Agent, 0, len(m.agents))
	for _, p := range m.agents {
		providers = append(providers, p)
	}
	m.mu.Unlock()

	for _, p := range providers {
		p.Stop()
	}
}
