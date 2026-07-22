package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"google.golang.org/protobuf/proto"
)

// ErrAgentNotFound is returned when an agent process does not exist.
var ErrAgentNotFound = errors.New("agent not found")

// Manager tracks active agents and routes messages.
type Manager struct {
	mu                 sync.RWMutex
	agents             map[string]Agent           // agentID -> Agent
	cachedOptionGroups map[string]cachedCatalog   // agentID -> last known option groups
	lifecycleLocks     map[string]*lifecycleEntry // agentID -> refcounted mutex
	// exitDone maps each running provider to a channel closed once its background Wait
	// goroutine has fully finished -- past its onExit cleanup. stopAndWait blocks on it so a
	// restart's new provider is never registered (and so can never persist control requests)
	// until the old process's onExit has run. See stopAndWait and startAgentWith.
	exitDone map[Agent]chan struct{}
	onExit   ExitHandler
}

// cachedCatalog is the last-known option-group set for a not-running agent, stamped
// with the model it was built for. OptionGroups serves it only when the requested
// current model still matches: an offline model edit (which rewrites options.model but
// not the persisted catalog) would otherwise keep serving the prior model's effort
// group instead of rebuilding for the new model.
type cachedCatalog struct {
	groups []*leapmuxv1.AvailableOptionGroup
	model  string
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
		cachedOptionGroups: make(map[string]cachedCatalog),
		lifecycleLocks:     make(map[string]*lifecycleEntry),
		exitDone:           make(map[Agent]chan struct{}),
		onExit:             onExit,
	}
}

// SetOnExit replaces the exit handler. The runner uses this to wire a
// service-aware handler (which has access to OutputHandler / DB queries)
// after the service.Service is constructed. The handler is read inside
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
//
// stopAndWait waits for the old process's background exit goroutine to finish -- including its
// onExit cleanup (ClearPendingControlRequests, keyed by agent id) -- BEFORE returning, so the
// new provider started here can never have its freshly-persisted control requests wiped by the
// old process's late onExit.
func (m *Manager) RestartAgent(ctx context.Context, opts Options, sink OutputSink) (map[string]string, error) {
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
// Returns the confirmed option values from the startup handshake (e.g.
// permission mode, discovered model), keyed by option-group id.
func (m *Manager) StartAgent(ctx context.Context, opts Options, sink OutputSink) (map[string]string, error) {
	reg, ok := agentFactoryRegistry[opts.AgentProvider]
	if !ok {
		return nil, fmt.Errorf("unsupported agent provider: %v", opts.AgentProvider)
	}
	return m.startAgentWith(ctx, opts, sink, reg.start)
}

func (m *Manager) startAgentWith(ctx context.Context, opts Options, sink OutputSink, start startFunc) (map[string]string, error) {
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

	groups := provider.OptionGroups()
	confirmedOptions := CurrentOptions(groups)

	// done is closed once the exit goroutine below has fully finished (past onExit), so
	// stopAndWait can block on it before returning -- guaranteeing a restart's new provider is
	// not registered until this process's onExit (which clears control_requests by agent id)
	// has run, and so cannot have its own requests wiped by it.
	done := make(chan struct{})

	m.mu.Lock()
	m.agents[opts.AgentID] = provider
	m.exitDone[provider] = done
	if len(groups) > 0 {
		m.cachedOptionGroups[opts.AgentID] = cachedCatalog{groups: groups, model: confirmedOptions[OptionIDModel]}
	}
	m.mu.Unlock()

	// Wait for the agent to exit in the background, then clean up.
	go func() {
		// Close `done` last -- AFTER onExit has run -- so a stopAndWait blocked on it observes
		// the completed cleanup. The exitDone entry is removed under the lock first.
		defer func() {
			m.mu.Lock()
			delete(m.exitDone, provider)
			m.mu.Unlock()
			close(done)
		}()

		err := provider.Wait()
		m.mu.Lock()
		// Only remove the entries if they still point at THIS provider. The slot is normally
		// freed by this goroutine, but stopAndWait (used by every restart) waits for this
		// goroutine to finish before it returns and re-registers a new provider, so the
		// identity check is a defensive guard rather than a race the cleanup paths can trigger.
		if m.agents[opts.AgentID] == provider {
			delete(m.agents, opts.AgentID)
			delete(m.cachedOptionGroups, opts.AgentID)
		}
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

		// onExit clears the exited process's pending control_requests (by agent id). It fires
		// for EVERY exit including a relaunch's old-process stop; this is safe because
		// stopAndWait blocks until this goroutine (and thus this onExit) completes before any
		// new provider for the same agent id is registered -- so the requests it clears
		// genuinely belong only to the process that just went away.
		m.mu.RLock()
		onExit := m.onExit
		m.mu.RUnlock()
		if onExit != nil {
			onExit(opts.AgentID, exitCode, err)
		}
	}()

	return confirmedOptions, nil
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
	var done chan struct{}
	if ok {
		done = m.exitDone[p]
	}
	m.mu.RUnlock()

	if !ok {
		return false
	}
	if done == nil {
		// Invariant: every m.agents entry is seeded together with its m.exitDone channel
		// (startAgentWith, under a single m.mu critical section). A registered agent with no
		// exitDone channel means some new registration path skipped that pairing -- which would
		// silently skip the restart happen-before wait below and reopen the race where the old
		// process's onExit wipes the new process's freshly-persisted control_requests. Log
		// loudly so a future violation surfaces instead of degrading in silence.
		slog.Error("agent registered without an exitDone channel; restart serialization may race",
			"agent_id", agentID)
	}

	if discardOutput {
		p.DiscardOutput()
	}
	p.Stop()
	_ = p.Wait()

	// p.Wait() only guarantees the process is gone, NOT that p's background exit goroutine has
	// run its cleanup. Block until that goroutine has fully finished -- past its onExit, which
	// clears the agent's pending control_requests by agent id alone. Without this wait, a caller
	// that re-registers a NEW provider for this agent id after we return (every restart does)
	// could race the old goroutine: the new process persists a control request and the old
	// goroutine's late onExit then deletes it. Waiting here makes the old process's full
	// teardown happen-before the new provider is ever registered, so it can only ever clear its
	// own (now-gone) requests. We hold no lock across the wait, so the exit goroutine -- which
	// takes m.mu for its own cleanup -- can make progress.
	if done != nil {
		<-done
	}

	// Remove the map entry and its cache eagerly so that StartAgent can proceed
	// immediately. The background goroutine's identity-checked delete already ran (we waited
	// for it above), so these deletes are typically no-ops; they also cover the rare path where
	// the agent was registered but its exit goroutine had not yet been scheduled.
	m.mu.Lock()
	delete(m.agents, agentID)
	delete(m.cachedOptionGroups, agentID)
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
func defaultModelForList(models []*ModelInfo, provider leapmuxv1.AgentProvider) string {
	ids := make([]string, 0, len(models))
	for _, m := range models {
		if m != nil {
			ids = append(ids, m.Id)
		}
	}
	return defaultModelIDForList(ids, markedModelID(models), firstModelID(models), provider)
}

// defaultModelIDForList runs the default-model badge ladder over a plain id list plus the id
// the list already flags as default (marked) and the highest-preference entry present (first),
// both "" when the list designates none. The ModelInfo catalog (defaultModelForList) and the
// projected "model" option group (withModelGroupDefaultMarked) each reduce to this, so the
// ladder lives in ONE place and the hot OptionGroups read path skips a proto<->ModelInfo
// round-trip. See defaultModelForList for the priority rationale.
func defaultModelIDForList(ids []string, marked, first string, provider leapmuxv1.AgentProvider) string {
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
		if id := matchModelID(ids, provider, env); id != "" {
			return id
		}
	}
	// DefaultModelSentinel is a Claude-Code-specific concept (the CLI's "default"
	// entry); gate the check to that provider so a non-Claude provider that
	// happens to report a model literally id'd "default" doesn't get its
	// self-marked/CLI-marked badge hijacked onto that entry.
	if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE && slices.Contains(ids, DefaultModelSentinel) {
		return DefaultModelSentinel
	}
	configured := DefaultModel(provider)
	if configured == "" {
		// No configured default: preserve whatever IsDefault the per-agent list
		// already set (e.g. buildACPModels marking the current model) instead of
		// moving the badge to the first entry.
		return ""
	}
	if slices.Contains(ids, configured) {
		return configured
	}
	// The configured default isn't in this (account-specific) list. Respect a
	// default the provider already designated on the list itself -- e.g. Codex's
	// queryAvailableModels copies the CLI's isDefault onto an entry -- before
	// falling back, so a stale registry default (configured but absent from the
	// live list) doesn't move the badge off the model the CLI actually marked.
	if marked != "" {
		return marked
	}
	// Nothing designated: mark the highest-preference entry the list does contain
	// so the picker always shows a default badge (e.g. a Claude CLI reporting
	// concrete models but no "default" sentinel falls back to its first model).
	return first
}

// matchModelID returns the id in the list the given id refers to, matched first by exact id
// then by provider-normalized alias (so a fully-qualified spelling like "claude-opus-4-8[1m]"
// resolves to the catalog's "opus[1m]"). Returns "" when the list contains no such id. Used
// to resolve an operator default-model override against an account-specific catalog.
func matchModelID(ids []string, provider leapmuxv1.AgentProvider, id string) string {
	if slices.Contains(ids, id) {
		return id
	}
	want := NormalizeModelID(provider, id)
	for _, mid := range ids {
		if NormalizeModelID(provider, mid) == want {
			return mid
		}
	}
	return ""
}

// markedModelID returns the id of the first model already flagged IsDefault, or ""
// if none is. firstModelID returns the id of the first non-nil model, or "". Both
// tolerate nil-bearing slices (the catalogs are treated as possibly nil-bearing).
func markedModelID(models []*ModelInfo) string {
	for _, m := range models {
		if m != nil && m.IsDefault {
			return m.Id
		}
	}
	return ""
}

func firstModelID(models []*ModelInfo) string {
	for _, m := range models {
		if m != nil {
			return m.Id
		}
	}
	return ""
}

func withDefaultModelMarked(models []*ModelInfo, provider leapmuxv1.AgentProvider) []*ModelInfo {
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

	out := make([]*ModelInfo, len(models))
	for i, model := range models {
		if model == nil {
			continue
		}
		shouldBeDefault := model.Id == defaultModel
		if model.IsDefault == shouldBeDefault {
			out[i] = model
		} else {
			c := model.clone()
			c.IsDefault = shouldBeDefault
			out[i] = c
		}
	}
	return out
}

// OptionGroups returns every configuration axis for an agent as config option
// groups, preferring the running provider's runtime groups, then cached groups,
// then static defaults. The model group's default badge is re-derived on every
// read (it depends on the LEAPMUX_*_DEFAULT_MODEL operator override, which is
// not intrinsic catalog data and must not be persisted stale).
// currentModel is the agent's persisted/selected model id; it is only consulted
// for the static-fallback path (a non-running agent with no cached catalog), so
// the effort group reflects the agent's ACTUAL model -- not the provider default
// -- and disappears for effort-less models (e.g. Haiku). A running agent's
// groups already carry the correct per-model effort group; pass "" when the
// model is unknown (the fallback then uses the provider default).
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
	return withModelGroupDefaultMarked(optionGroupsFromCached(cached, provider, currentModel), provider)
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
	p, ok := m.agents[agentID]
	cached = m.cachedOptionGroups[agentID]
	m.mu.RUnlock()
	if !ok {
		return nil, false, cached
	}
	// Snapshot the live catalog once: each p.OptionGroups() call re-locks the provider and rebuilds
	// the slice, and calling it twice could also observe two different catalogs across a concurrent
	// refresh.
	if live := p.OptionGroups(); len(live) > 0 {
		m.refreshCachedCatalog(agentID, p, live)
		return withModelGroupDefaultMarked(live, provider), true, cached
	}
	// Transiently-empty live read: serve the freshest known cached catalog (refreshCachedCatalog
	// keeps it), NOT a caller's persisted snapshot.
	return withModelGroupDefaultMarked(optionGroupsFromCached(cached, provider, currentModel), provider), true, cached
}

// optionGroupsFromCached projects a not-running (or transiently-empty-live) agent's cached catalog
// into the served option groups: the cache as-is when it is usable for the requested model, a
// model-dependent rebuild when ONLY the per-model groups are stale, else the static fallback.
// Shared by OptionGroups (which sources `cached` from the shared per-agent cache) and
// OptionGroupsForRow (which sources it from a caller's own row snapshot), so both resolve a
// not-running catalog identically. Does NOT mark the model default -- the caller does.
func optionGroupsFromCached(cached cachedCatalog, provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	switch {
	case cachedCatalogUsable(cached, currentModel, provider):
		return cached.groups
	case len(cached.groups) > 0 && providerHasModelDependentGroups(provider) && currentModel != "":
		// The cache exists but is stale ONLY by model: its per-model effort/thinking groups were
		// built for a different model (an offline model edit moved the model column without
		// re-persisting the catalog -- see optionGroupsView). Rebuild just those for currentModel
		// and keep every other cached group, so dynamically-discovered model-INDEPENDENT groups
		// (Claude's Output Style / Fast Mode, surfaced only at runtime and absent from the static
		// templates) and any live-filtered group survive the edit instead of vanishing from the
		// settings popover until relaunch.
		return withModelDependentGroupsRebuilt(cached.groups, provider, currentModel)
	default:
		return staticOptionGroupsForProvider(provider, currentModel)
	}
}

// ensureModelGroup guarantees the catalog surfaces the model axis when the row knows a model but
// the projected groups carry no model group -- a dynamic-model ACP provider configured with a
// LEAPMUX_*_DEFAULT_MODEL override but never run, so it has no discovered model list to build a
// selectable group from. It prepends a read-only model group carrying that value, so the stored
// model stays visible to a by-id reader (the remote CLI reads the model from the option groups,
// not the options map). A no-op when a model group already exists or no model is known.
func ensureModelGroup(groups []*leapmuxv1.AvailableOptionGroup, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	if currentModel == "" || optionids.GroupByID(groups, OptionIDModel) != nil {
		return groups
	}
	model := readOnlyValueGroup(OptionIDModel, ModelGroupLabel, OptionOrderModel, currentModel, "")
	return append([]*leapmuxv1.AvailableOptionGroup{model}, groups...)
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
	return ensureModelGroup(withModelGroupDefaultMarked(optionGroupsFromCached(rowCached, provider, currentModel), provider), currentModel)
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
func (m *Manager) refreshCachedCatalog(agentID string, p Agent, live []*leapmuxv1.AvailableOptionGroup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agents[agentID] == p {
		m.cachedOptionGroups[agentID] = cachedCatalog{groups: live, model: optionids.CurrentValue(live, OptionIDModel)}
	}
}

// cachedCatalogUsable reports whether the cached catalog can be served for the requested
// model, rather than falling through to the static fallback (rebuilt for the requested
// model). It is NOT usable -- a model-dependent provider's cache is stale -- for a
// MODEL-DEPENDENT provider (Claude/Codex/Pi -- per-model effort tiers, Claude's extended
// thinking) whenever the requested model is known and the cache's stamp doesn't match it:
// either a concrete-but-different stamp (an offline model edit) OR an UNSTAMPED cache
// (model == "", e.g. a static-fallback catalog persisted before a model resolved, whose
// effort group was built for the provider default). Serving such a cache would show the
// wrong model's effort tiers.
//
// A stale-by-model cache is NOT discarded wholesale: OptionGroups rebuilds only the
// model-dependent groups for the requested model (withModelDependentGroupsRebuilt) and keeps
// the rest, because these providers CAN carry dynamically-discovered model-INDEPENDENT groups
// that live only in the cache -- Claude surfaces Output Style (from availableOutputStyles) and
// Fast Mode at runtime, neither of which staticOptionGroupsForProvider reproduces. Dropping the
// whole cache for the bare static fallback would lose those groups from the popover until the
// agent relaunches. The caller then overlays the persisted currents.
//
// Providers WITHOUT model-dependent groups -- the ACP permission-mode / primary-agent
// providers, whose effort/reasoning axes ARE model-independent server-driven config options
// living only in the cache -- always keep serving the cache across a model edit (the caller
// overlays the new model as current); falling through to their degenerate static fallback
// would silently drop those option groups from the popover until relaunch. An unknown
// requested model (currentModel == "") is also trusted as-is for everyone.
func cachedCatalogUsable(cached cachedCatalog, currentModel string, provider leapmuxv1.AgentProvider) bool {
	return len(cached.groups) > 0 &&
		(currentModel == "" || cached.model == currentModel || !providerHasModelDependentGroups(provider))
}

// providerHasModelDependentGroups reports whether a provider's catalog carries
// model-dependent sub-groups (per-model effort tiers, and for Claude the per-model
// extended-thinking group) that must be rebuilt when the model changes. A provider has
// them exactly when it owns a model-dependent effort catalog -- ProviderManagesEffort
// (Claude/Codex/Pi). The ACP permission-mode / primary-agent providers do NOT: although
// every provider shares the default effortSubGroups builder, it produces nothing for a
// model with no SupportedEfforts, and their effort/reasoning axes are model-independent
// server-driven config options -- so a model change doesn't invalidate any cached group, and
// falling through to the static fallback would needlessly drop those config options.
func providerHasModelDependentGroups(provider leapmuxv1.AgentProvider) bool {
	return ProviderManagesEffort(provider)
}

// modelDependentGroups builds the model group plus the per-model sub-groups for currentModel:
// its effort tiers, and for Claude the extended-thinking group whose label ("Adaptive" vs "On")
// is per model. These are exactly the groups that must be rebuilt when the selected model
// changes; the provider's static option-group templates (sandbox/network/permission/...) and
// any dynamically-discovered group are model-INDEPENDENT and handled separately. Current values
// are empty here; the caller overlays DB selections. Returns nil for an unknown provider.
func modelDependentGroups(provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	reg, ok := agentFactoryRegistry[provider]
	if !ok {
		return nil
	}
	if currentModel == "" {
		currentModel = DefaultModel(provider)
	}
	var groups []*leapmuxv1.AvailableOptionGroup
	if mg := modelOptionGroup(reg.defaultModels, "", reg.modelSubGroups); mg != nil {
		groups = append(groups, mg)
		// Emit the current model's model-dependent groups (its effort tiers, and
		// for Claude the extended-thinking group whose label is per model) as
		// top-level groups. This fallback is served while an agent is restarting
		// (not registered): without these, the settings popover briefly loses the
		// effort/thinking groups mid-restart, which flickers and can race a click.
		if reg.modelSubGroups != nil {
			if m := FindAvailableModel(reg.defaultModels, currentModel); m != nil {
				groups = append(groups, reg.modelSubGroups(m)...)
			}
		}
	}
	return groups
}

// staticOptionGroupsForProvider builds the fallback option groups for a provider
// that is not running and has no cached catalog: the model-dependent groups for
// currentModel (the agent's selected model, defaulting to the provider default when
// unknown -- so a Haiku agent shows no effort group while a Sonnet agent shows Sonnet's
// tiers), followed by the provider's static option-group templates. Current values are
// empty here; the caller overlays DB selections.
func staticOptionGroupsForProvider(provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	reg, ok := agentFactoryRegistry[provider]
	if !ok {
		return nil
	}
	return append(modelDependentGroups(provider, currentModel), reg.optionGroups...)
}

// withModelDependentGroupsRebuilt returns the cached catalog with its model-dependent groups
// (model, effort, and per-model sub-groups) rebuilt for currentModel, preserving every OTHER
// cached group untouched. Served on an OFFLINE model edit of a model-dependent provider
// (Claude/Codex/Pi): the cached catalog's per-model effort/thinking groups were built for the
// PRIOR model and are stale, but the catalog also carries dynamically-discovered, model-
// INDEPENDENT groups (Claude's Output Style / Fast Mode, surfaced only at runtime and absent
// from the static templates) and any live-filtered group. Falling through to the bare static
// fallback would drop those until the agent relaunches; this swaps only the stale per-model
// groups and keeps the rest. The caller overlays the persisted currents afterward.
func withModelDependentGroupsRebuilt(cached []*leapmuxv1.AvailableOptionGroup, provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	fresh := modelDependentGroups(provider, currentModel)
	if len(fresh) == 0 {
		return cached
	}
	freshByID := make(map[string]*leapmuxv1.AvailableOptionGroup, len(fresh))
	for _, g := range fresh {
		freshByID[g.GetId()] = g
	}
	out := make([]*leapmuxv1.AvailableOptionGroup, 0, len(cached)+len(fresh))
	rebuilt := make(map[string]bool, len(fresh))
	for _, g := range cached {
		if f, ok := freshByID[g.GetId()]; ok {
			out = append(out, f)
			rebuilt[g.GetId()] = true
		} else {
			out = append(out, g)
		}
	}
	// A model-dependent group the stale cache lacked (e.g. the new model offers an effort
	// group the prior model had none of) must still appear; order is irrelevant since the
	// frontend sorts by each group's Order field.
	for _, f := range fresh {
		if !rebuilt[f.GetId()] {
			out = append(out, f)
		}
	}
	return out
}

// withModelGroupDefaultMarked re-derives the "model" group's DefaultValue via the
// LEAPMUX_*_DEFAULT_MODEL / sentinel / configured-default ladder (defaultModelIDForList).
// Returns the input unchanged when there is no model group or the default is
// already correct; otherwise returns a copy with only the model group replaced,
// leaving the shared catalog groups untouched.
func withModelGroupDefaultMarked(groups []*leapmuxv1.AvailableOptionGroup, provider leapmuxv1.AgentProvider) []*leapmuxv1.AvailableOptionGroup {
	mg := optionids.GroupByID(groups, OptionIDModel)
	if mg == nil || len(mg.GetOptions()) == 0 {
		return groups
	}
	ids := make([]string, 0, len(mg.GetOptions()))
	for _, o := range mg.GetOptions() {
		ids = append(ids, o.GetId())
	}
	// The group's existing DefaultValue is the already-designated default (the option flagged
	// default), but only when it is actually one of the options; ids[0] is the highest-
	// preference present entry. This mirrors the markedModelID/firstModelID reduction
	// defaultModelForList performs on a ModelInfo catalog, without allocating throwaway ModelInfos.
	marked := ""
	if slices.Contains(ids, mg.GetDefaultValue()) {
		marked = mg.GetDefaultValue()
	}
	def := defaultModelIDForList(ids, marked, ids[0], provider)
	if def == "" || def == mg.GetDefaultValue() {
		return groups
	}
	out := make([]*leapmuxv1.AvailableOptionGroup, len(groups))
	for i, g := range groups {
		if g.GetId() == OptionIDModel {
			c := proto.Clone(g).(*leapmuxv1.AvailableOptionGroup)
			c.DefaultValue = def
			out[i] = c
		} else {
			out[i] = g
		}
	}
	return out
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

// UpdateSettings applies the agent's FULL current option map (id->value, NOT a sparse delta)
// to a running agent so the next turn picks the change up without a restart. Returns true if the
// provider accepted the update, false if it requires a restart. See Agent.UpdateSettings for the
// empty-value semantics (ignored here, not a delete).
func (m *Manager) UpdateSettings(agentID string, options optionmap.Map) bool {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	return p.UpdateSettings(options)
}

// CurrentOptions returns a snapshot of the running agent's confirmed option
// values (id->value), or nil if no agent is running with that ID. The snapshot
// reflects the in-memory state that the live-update (refreshSettingsFromAgent)
// and restart paths write synchronously, so callers can read back the
// model/effort the agent actually confirmed without a DB round-trip.
func (m *Manager) CurrentOptions(agentID string) map[string]string {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	return CurrentOptions(p.OptionGroups())
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
