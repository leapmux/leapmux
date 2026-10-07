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
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/config"
	"google.golang.org/protobuf/proto"
)

// ErrAgentNotFound is returned when an agent process does not exist.
var ErrAgentNotFound = errors.New("agent not found")

// Manager tracks active agents and routes messages.
type Manager struct {
	mu                 sync.RWMutex
	agents             map[string]*agentRegistration // agentID -> runtime registration
	cachedOptionGroups map[string]cachedCatalog      // agentID -> last known option groups
	lifecycleLocks     map[string]*lifecycleEntry    // agentID -> refcounted mutex
	onExit             ExitHandler
	// registry states every provider this manager can start. It is fixed at
	// construction and never nil, so every read of a provider's registration goes
	// through the one value the worker was wired with.
	registry *Registry

	// agentDirs is where the agents of this worker create their private
	// directories. PrepareAgentDirs sets it once, before anything can start an
	// agent, and startAgentWith hands it to each start through Options.
	agentDirs *agentdir.Dirs

	// startupSlots caps how many BACKGROUND agent startups run at once: one
	// token is held from just before the provider's start func spawns the
	// process until that func returns, which is the point the provider has
	// completed its handshake (the initialize control_response, thread/start,
	// session/new, get_state -- one per provider). It never covers a running
	// agent, so it does not limit how many agents this machine hosts.
	//
	// Only a background spawn draws on it. A spawn the user asked for -- an
	// open, a restart, a /clear, a cold start behind a message -- takes no
	// permit and never waits, because the user is waiting on it: the send path
	// calls the cold start INLINE, and the client gives that RPC about fifteen
	// seconds, so a permit wait would fail the send although the message row is
	// already durable. The boot-time resume sweep is the one caller nobody is
	// waiting on, and it is the one this pool exists to hold back.
	//
	// Buffered and never nil: NewManager fills it, because a nil channel blocks
	// for ever and this manager is constructed before the startup concurrency is
	// read from the configuration. SetStartupConcurrency replaces it.
	startupSlots chan struct{}
}

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

// lifecycleEntry is a per-agent mutex whose refcount is guarded by Manager.mu.
// Entries are evicted when no caller holds or is waiting for the lock.
type lifecycleEntry struct {
	mu       sync.Mutex
	refcount int
}

// NewManager creates a new agent Manager that starts the providers registry
// states. The optional onExit handler is called when any agent process exits.
//
// It panics on a nil registry: a manager without one could start nothing, and
// every caller that reads a provider's registration would read nothing, so the
// wiring mistake must surface at construction rather than at the first spawn.
func NewManager(registry *Registry, onExit ExitHandler) *Manager {
	if registry == nil {
		panic("agent: NewManager requires a registry")
	}
	return &Manager{
		registry:           registry,
		agents:             make(map[string]*agentRegistration),
		cachedOptionGroups: make(map[string]cachedCatalog),
		lifecycleLocks:     make(map[string]*lifecycleEntry),
		onExit:             onExit,
		startupSlots:       newStartupSlots(config.ResolveStartupConcurrency(0)),
	}
}

// newStartupSlots builds a permit channel of capacity n. n is already resolved
// by config.ResolveStartupConcurrency, so a non-positive value here would be a
// caller bug; the max keeps it from producing an unbuffered channel, which
// admits nobody and would wedge every background spawn.
func newStartupSlots(n int) chan struct{} {
	return make(chan struct{}, max(n, 1))
}

// SetStartupConcurrency replaces the background startup permit pool. n <= 0
// restores the default (see config.ResolveStartupConcurrency).
//
// Call it before the manager is reachable by anything that spawns. Wire does,
// beside SetOnExit and well before SetChannelMgr publishes the manager to the
// connect loop, so no in-flight startup can be holding a permit from the pool
// this discards -- a swap under load would let the two pools admit
// old+new callers at once, briefly doubling the configured concurrency.
func (m *Manager) SetStartupConcurrency(n int) {
	slots := newStartupSlots(config.ResolveStartupConcurrency(n))
	m.mu.Lock()
	m.startupSlots = slots
	m.mu.Unlock()
}

// errAgentDirsPrepared refuses a second PrepareAgentDirs.
var errAgentDirsPrepared = errors.New("agent: the agent directories are prepared already")

// PrepareAgentDirs prepares the private directories of this worker's agents,
// with the spec of each provider that the registry states one for, and with a
// base under dataDir. It starts the sweep of the directories that ended
// workers left, and returns without waiting for it: an agent that creates its
// directory waits for the sweep of that directory's parent. The sweep stops
// early when ctx ends.
//
// Call it once, before anything can start an agent. Wire does. It fails only
// for a wiring mistake: specs that NewRegistry would refuse, or a second call.
func (m *Manager) PrepareAgentDirs(ctx context.Context, dataDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agentDirs != nil {
		return errAgentDirsPrepared
	}
	dirs, err := agentdir.Start(ctx, agentdir.Config{DataDir: dataDir, Specs: m.registry.AgentDirSpecs()})
	if err != nil {
		return fmt.Errorf("prepare the agent directories: %w", err)
	}
	m.agentDirs = dirs
	return nil
}

// StartupConcurrency reports the background permit pool's current capacity.
//
// It is the ONE source of truth for the configured number. The resume sweep
// reads it to size its own fan-out, so the depth of the queue in front of the
// pool matches the pool, and neither can drift when a wiring line is missed.
// A caller must not branch on it to decide WHETHER to spawn -- the pool itself
// enforces that, on the spawn path, for every background caller.
func (m *Manager) StartupConcurrency() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cap(m.startupSlots)
}

// acquireStartupSlot takes one background startup permit, blocking while the
// configured number of background startups are already in flight. The returned
// function releases it. Defer it around the start func, so a start that returns
// an error AND a start that panics both give the permit back -- a leaked permit
// shrinks the pool for the life of the process.
//
// It selects on ctx.Done() so a startup whose tab is closed while it waits
// gives its place up instead of spawning a process nobody wants. The permit is
// read once under the lock rather than through m.startupSlots at both ends, so
// a concurrent SetStartupConcurrency cannot make the release land in a
// different pool than the acquire.
func (m *Manager) acquireStartupSlot(ctx context.Context) (release func(), err error) {
	m.mu.RLock()
	slots := m.startupSlots
	m.mu.RUnlock()

	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
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

// ExitHandlerForTest returns the installed exit handler so a wiring test can
// RUN it.
//
// A non-nil check proves only that some handler is installed. Only the
// handler's BEHAVIOR identifies the service-aware one. Nothing else fails when
// the wiring is dropped: a dead process leaves every in-flight subagent and
// shell row 'running' for good. The sidebar then shows work that does not
// happen, and no error leads back to the missing call.
func (m *Manager) ExitHandlerForTest() ExitHandler {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.onExit
}

// PutAgentForTest registers a as the running agent agentID, without a process,
// so a test can reach the manager's dispatch for an agent it built itself. It
// replaces any agent already registered under that id, and starts no Wait
// goroutine, so nothing removes the entry when a exits.
func (m *Manager) PutAgentForTest(agentID string, a Agent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents[agentID] = &agentRegistration{provider: a}
}

// LifecycleLockCallersForTest reports how many callers hold or wait for the
// lifecycle lock of agentID. LockAgent counts a caller before it blocks on the
// lock, so a count of two while one caller holds the lock proves that a second
// caller waits on it. A test reads that instead of a sleep that guesses how
// long the second caller takes to arrive.
func (m *Manager) LifecycleLockCallersForTest(agentID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if entry, ok := m.lifecycleLocks[agentID]; ok {
		return entry.refcount
	}
	return 0
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
func (m *Manager) RestartAgent(ctx context.Context, opts Options, sink ProviderServices) (map[string]string, error) {
	unlock := m.LockAgent(opts.AgentID)
	defer unlock()

	m.stopAndWait(opts.AgentID, false)
	return m.StartAgent(ctx, opts, sink)
}

// StartAgent spawns an agent for the given agent ID, dispatching based on
// opts.AgentProvider.
// The sink receives parsed output events.
// Returns the confirmed option values from the startup handshake (e.g.
// permission mode, discovered model), keyed by option-group id.
func (m *Manager) StartAgent(ctx context.Context, opts Options, sink ProviderServices) (map[string]string, error) {
	return m.startAgent(ctx, opts, sink, false)
}

// StartBackgroundAgent is StartAgent for a spawn nobody is waiting on -- today
// the boot-time resume sweep. It is the only entry point that draws on the
// startup permit pool, so a machine restoring two hundred tabs cannot run two
// hundred handshakes at once, while a tab the user opens by hand never waits
// behind them.
func (m *Manager) StartBackgroundAgent(ctx context.Context, opts Options, sink ProviderServices) (map[string]string, error) {
	return m.startAgent(ctx, opts, sink, true)
}

func (m *Manager) startAgent(ctx context.Context, opts Options, sink ProviderServices, background bool) (map[string]string, error) {
	reg, ok := m.registry.Registration(opts.AgentProvider)
	if !ok {
		return nil, fmt.Errorf("unsupported agent provider: %v", opts.AgentProvider)
	}
	return m.startAgentWith(ctx, opts, sink, reg.Start, background)
}

// StartAgentWith is StartAgent with the start function supplied by the caller
// instead of read from the registry. Everything else is StartAgent's own path:
// the duplicate-agent check, the registration of the running agent, and its exit
// handling. A caller that must run a program other than the provider's own -- a
// test that stands a mock process in for a provider -- uses it, and the Manager
// treats the result exactly as it treats any other agent.
func (m *Manager) StartAgentWith(ctx context.Context, opts Options, sink ProviderServices, start StartFunc) (map[string]string, error) {
	return m.startAgentWith(ctx, opts, sink, start, false)
}

// Registry returns the registry this manager was built with. Every caller that
// reads a provider's registration reads it here, so the worker has exactly one.
func (m *Manager) Registry() *Registry {
	return m.registry
}

func (m *Manager) startAgentWith(ctx context.Context, opts Options, sink ProviderServices, start StartFunc, background bool) (map[string]string, error) {
	m.mu.Lock()
	if _, exists := m.agents[opts.AgentID]; exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("agent already running for agent %s", opts.AgentID)
	}
	// The worker's one set of agent directories, unless the caller states its
	// own: a test that starts a provider through the manager does.
	if opts.AgentDirs == nil {
		opts.AgentDirs = m.agentDirs
	}
	m.mu.Unlock()

	// Throttle the SPAWN, not the agent, and only a background one. start blocks
	// from the exec through the provider's startup handshake, so the permit is
	// held for exactly the window this cap is about and is given back before the
	// agent is registered.
	//
	// A spawn the user asked for takes no permit and never waits. That is the
	// whole point of the split: the cap exists so a boot-time sweep does not
	// start two hundred CLIs at once, and a user who opens a tab in the middle of
	// that sweep is not the one who should pay for it.
	//
	// The release is deferred inside a closure rather than called after start
	// returns. A provider that panics mid-handshake would otherwise keep its
	// permit for the life of the process: a recovered panic leaves the worker
	// running one permit short, and after enough of them the sweep blocks in
	// acquireStartupSlot with nothing logged.
	provider, err := func() (Agent, error) {
		if !background {
			return start(ctx, opts, sink)
		}
		release, err := m.acquireStartupSlot(ctx)
		if err != nil {
			return nil, fmt.Errorf("wait for an agent startup slot: %w", err)
		}
		defer release()
		return start(ctx, opts, sink)
	}()
	if err != nil {
		return nil, err
	}

	groups := provider.OptionGroups()
	confirmedOptions := provider.SettingsSnapshot().ConfirmedOptions()

	// done is closed once the exit goroutine below has fully finished (past onExit), so
	// stopAndWait waits on it before returning, so a restart's new provider is
	// not registered until this process's onExit (which clears control_requests by agent id)
	// has run, and so cannot have its own requests wiped by it.
	done := make(chan struct{})
	entry := &agentRegistration{provider: provider, done: done}

	m.mu.Lock()
	m.agents[opts.AgentID] = entry
	if len(groups) > 0 {
		m.cachedOptionGroups[opts.AgentID] = cachedCatalog{groups: groups, model: optionids.CurrentValue(groups, OptionIDModel), registration: entry}
	}
	m.mu.Unlock()

	// The first moment SupportedGoalActions can answer for this process: it
	// type-asserts the agent this map now holds, and every earlier publication
	// ran while the lookup still missed. See ProviderServices.PublishGoalCapabilities
	// for why an early answer is worse than a late one here.
	sink.PublishGoalCapabilities()

	// Wait for the agent to exit in the background, then clean up.
	go func() {
		// Close done after onExit returns, so stopAndWait observes completed cleanup.
		// The registration drops its channel under the lock first.
		defer func() {
			m.mu.Lock()
			entry.done = nil
			m.mu.Unlock()
			close(done)
		}()

		err := provider.Wait()
		// The process ended. The slot stays registered until onExit finishes,
		// so record the state that the slot no longer carries.
		m.mu.Lock()
		entry.exiting = true
		m.mu.Unlock()
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}

		// Read IsStopped once. It reflects mutable process state; reading it
		// twice (once for the log, once for onExit) could diverge if it changed
		// between the reads, mislabeling the registry rows as 'interrupted'
		// (crash) instead of 'stopped' (user action).
		stopped := provider.IsStopped()
		if stopped {
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
			onExit(opts.AgentID, exitCode, err, stopped)
		}

		m.mu.Lock()
		// Clear this registration's exit state even if another registration replaced it.
		entry.exiting = false
		// Release the slot only after onExit pauses durable input. A turn-end
		// drain can otherwise see no provider and restart a process after a crash.
		// Only remove entries that still point at this provider. The identity
		// check protects a defensive replacement from an old exit goroutine.
		if m.agents[opts.AgentID] == entry {
			delete(m.agents, opts.AgentID)
			delete(m.cachedOptionGroups, opts.AgentID)
		}
		m.mu.Unlock()
	}()

	return confirmedOptions, nil
}

// SendInput routes a user message to the specified agent, waiting out any
// lifecycle operation that is in flight for it.
//
// A restart stops the old process and starts a new process. Resolve the provider
// under the lifecycle lock so input that arrives during this interval reaches
// the new process.
//
// The lock covers only provider resolution. A blocked provider write must not
// block a restart or explicit steering. A restart can still start between the
// resolution and the write, but the interval is only the provider call setup.
// A text-route provider observes the goal command after a successful delivery.
//
// Never call this from a caller that already holds the lifecycle lock
// (LockAgent, RestartAgent): the lock is not reentrant, so it deadlocks. Send
// after the lifecycle call returns, the way the plan-execution path does. The
// same applies to an ExitHandler, which runs while a lifecycle caller waits --
// see the contract on that type.
func (m *Manager) SendInput(agentID, content string, attachments []*leapmuxv1.Attachment) error {
	p, err := m.providerAfterLifecycle(agentID)
	if err != nil {
		return err
	}
	return deliverProviderInput(p, nil, content, attachments)
}

// SendInputToSession uses a provider instance that the caller validated under its lifecycle lock.
// The provider checks the expected session while constructing the native request.
func SendInputToSession(provider Agent, sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return deliverProviderInput(provider, &sessionID, content, attachments)
}

func deliverProviderInput(p Agent, expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	var err error
	if expected == nil {
		err = p.SendInput(content, attachments)
	} else {
		err = p.SendInputForSession(*expected, content, attachments)
	}
	if errors.Is(err, ErrAgentBusy) {
		// The refusal disproves the Worker's view of the turn, and both consumers
		// of the turn flag -- the activity state and the input queue's dispatch
		// guard -- are wrong at exactly this moment. Repairing here rather than
		// in each provider's SendInput stops another provider from
		// leaving it out. SendChildInput does NOT do this: a collab child's
		// activity comes from its background-task registry row.
		state := p.PublishTurnActive()
		return &AgentBusyError{Err: err, ActiveTurnSteerable: state.Steerable}
	}
	if err == nil {
		observeGoalCommand(p, GoalDeliverySend, content)
	}
	return err
}

// observeGoalCommand tells a text-route provider what LeapMux just delivered.
//
// Every route that reaches a provider process calls this, because the provider
// alone knows which channel its command parser reads. A route that skipped it
// would leave the CLI holding a goal the card never shows, for the life of the
// session: no text-route provider reports a command-driven goal change back.
func observeGoalCommand(p Agent, delivery GoalCommandDelivery, content string) {
	if commander, ok := p.(GoalTextCommander); ok {
		commander.ObserveGoalCommand(delivery, content)
	}
}

func (m *Manager) CompactContext(agentID string) error {
	p, err := m.providerAfterLifecycle(agentID)
	if err != nil {
		return err
	}
	compactor, ok := p.(ContextCompactor)
	if !ok {
		return ErrCompactionUnsupported
	}
	return compactor.CompactContext()
}

// SteerInput interrupts the active turn with more text.
//
// A queued goal command is an ordinary user message, so the user can steer it.
// The provider decides whether its steer channel reaches its command parser --
// Claude Code steers by writing the same user message, and Goose steers through
// a separate ACP method that LeapMux did not verify.
func (m *Manager) SteerInput(agentID, content string, attachments []*leapmuxv1.Attachment) error {
	p, err := m.providerAfterLifecycle(agentID)
	if err != nil {
		return err
	}
	steerer, ok := p.(InputSteerer)
	if !ok {
		return ErrSteeringUnsupported
	}
	err = steerer.SteerInput(content, attachments)
	if err == nil {
		observeGoalCommand(p, GoalDeliverySteer, content)
	}
	return err
}

// SupportsSteering reports whether the running agent can steer its active turn.
// It asks the provider and reports the answer. It answers false for an agent
// that does not run, and for a provider that does not implement InputSteerer.
func (m *Manager) SupportsSteering(agentID string) bool {
	m.mu.RLock()
	entry, ok := m.agents[agentID]
	p := entry.providerOrNil()
	m.mu.RUnlock()
	if !ok {
		return false
	}
	steerer, ok := p.(InputSteerer)
	return ok && steerer.SupportsSteering()
}

// providerAfterLifecycle resolves an agent's running provider, first waiting out
// any lifecycle operation in flight for it.
//
// The whole reason to take the lifecycle lock on an input path: the map read
// must happen AFTER a restart finishes, or it hands back the process that
// restart is destroying. The lock is released before the caller writes, so
// nothing that blocks on a provider ever holds it.
func (m *Manager) providerAfterLifecycle(agentID string) (Agent, error) {
	provider, release := m.LockProvider(agentID)
	release()
	if provider == nil {
		return nil, fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}
	return provider, nil
}

// LockProvider returns the current provider, which can be nil, and retains the lifecycle lock.
// Always call release. Release the lock before an input operation waits for its response.
func (m *Manager) LockProvider(agentID string) (Agent, func()) {
	unlock := m.LockAgent(agentID)

	m.mu.RLock()
	entry := m.agents[agentID]
	p := entry.providerOrNil()
	m.mu.RUnlock()

	return p, unlock
}

// SendRawInput writes raw bytes directly to the specified agent's stdin
// without wrapping in a UserInputMessage.
func (m *Manager) SendRawInput(agentID string, data []byte, stop StopContext) error {
	m.mu.RLock()
	entry, ok := m.agents[agentID]
	p := entry.providerOrNil()
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}

	return p.SendRawInput(data, stop)
}

// Interrupt aborts the agent's current turn using the provider-specific
// signal. Returns ErrAgentNotFound when the agent isn't running.
func (m *Manager) Interrupt(agentID string, stop StopContext) error {
	m.mu.RLock()
	entry, ok := m.agents[agentID]
	p := entry.providerOrNil()
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}
	return p.Interrupt(stop)
}

// InterruptEscalationReady reports whether an earlier interrupt of this agent
// was PROVEN ineffective and the caller may escalate the next one past the
// provider's own signal -- the ZCode case, whose app-server acknowledges a stop
// and then ignores it. Only a provider that can state the fact implements the
// probe; every other provider answers false and every interrupt stays ordinary.
func (m *Manager) InterruptEscalationReady(agentID string) bool {
	m.mu.RLock()
	entry, ok := m.agents[agentID]
	p := entry.providerOrNil()
	m.mu.RUnlock()
	if !ok {
		return false
	}
	escalatable, ok := p.(interface{ InterruptEscalationReady() bool })
	return ok && escalatable.InterruptEscalationReady()
}

// SendChildInput routes a user message to a subagent conversation (identified
// by childKey, the provider linkage key stored in the registry row_key) inside
// the owner process rootAgentID. It type-asserts the running Agent to
// ChildSteerer; providers that cannot steer a subagent return
// ErrChildOperationUnsupported. The service resolves childKey from the registry
// before calling here.
// Like SendInput, it waits for an active lifecycle operation on the owner.
// The lock covers only provider resolution because child steering can block.
func (m *Manager) SendChildInput(rootAgentID, childKey, content string, attachments []*leapmuxv1.Attachment) error {
	p, err := m.providerAfterLifecycle(rootAgentID)
	if err != nil {
		return err
	}
	steerer, ok := p.(ChildSteerer)
	if !ok {
		return ErrChildOperationUnsupported
	}
	err = steerer.SendChildInput(childKey, content, attachments)
	if errors.Is(err, ErrAgentBusy) {
		return &AgentBusyError{Err: err, ActiveTurnSteerable: steerer.ActiveChildTurnState(childKey).Steerable}
	}
	return err
}

func (m *Manager) SteerChildInput(rootAgentID, childKey, content string, attachments []*leapmuxv1.Attachment) error {
	p, err := m.providerAfterLifecycle(rootAgentID)
	if err != nil {
		return err
	}
	steerer, ok := p.(ChildSteerer)
	if !ok {
		return ErrChildOperationUnsupported
	}
	return steerer.SteerChildInput(childKey, content, attachments)
}

// InterruptChild aborts a subagent's current turn inside the owner process.
// Input and interrupt capabilities stay separate because Codex Multi-Agent V2
// permits direct interruption but rejects direct input.
func (m *Manager) InterruptChild(rootAgentID, childKey string, stop StopContext) error {
	m.mu.RLock()
	entry, ok := m.agents[rootAgentID]
	p := entry.providerOrNil()
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, rootAgentID)
	}
	interrupter, ok := p.(ChildInterrupter)
	if !ok {
		return ErrChildOperationUnsupported
	}
	return interrupter.InterruptChild(childKey, stop)
}

// SupportedGoalActions reports the session-goal actions the RUNNING agent can
// perform, by type-asserting it to GoalCapable.
//
// An agent that is not running answers with nothing, and so does a provider
// that reports its goal without being able to change it (Oh My Pi). The browser
// disables every control it does not find here, so "nothing" is the safe answer
// in both cases -- and it is why this is read from the live process rather than
// a per-provider table: goal support is version-dependent (Claude Code shipped
// /goal in 2.1.139, ZCode in 3.10.2), so a table would offer a button that does
// nothing against an older CLI.
//
// Same shape as the AgentInfo.accepts_messages decision, which reads the
// provider capability that owns direct child input.
// It reads the agent map DIRECTLY rather than through providerAfterLifecycle,
// and that is deliberate: StartAgent publishes the capabilities while a
// lifecycle caller holds LockAgent, and the ExitHandler broadcasts them while
// RestartAgent waits on it. The lifecycle lock is not reentrant, so taking it
// here deadlocks both paths. A capability read is safe on a stale entry -- the
// worst answer is one extra broadcast that names what the old process could do.
func (m *Manager) SupportedGoalActions(agentID string) []GoalAction {
	m.mu.RLock()
	entry, ok := m.agents[agentID]
	p := entry.providerOrNil()
	exiting := entry != nil && entry.exiting
	m.mu.RUnlock()
	// An EXITING process can do nothing, and the exit handler is exactly when
	// this is asked: onExit runs before the map entry is deleted, so a bare
	// lookup still finds the dying process and answers with its full action
	// list. The broadcast that exists to settle the controls would then ship
	// live Pause and Clear for a process that is gone.
	if !ok || exiting {
		return nil
	}
	capable, ok := p.(GoalCapable)
	if !ok {
		return nil
	}
	return capable.SupportedGoalActions()
}

// UpdateGoal performs one session-goal action on the running agent. A non-empty
// result is user-message text that the caller must enqueue before it takes effect.
//
// It refuses an action the agent does not list in SupportedGoalActions, so a
// browser acting on a stale capability list gets a refusal instead of a call
// the provider silently ignores. That check lives here, beside the dispatch,
// rather than in the service: the two answers must come from the same agent
// instance, and a service-side check would read the capability through a second
// lookup that could resolve a different process.
func (m *Manager) UpdateGoal(agentID string, action GoalAction, objective string) (string, error) {
	// providerAfterLifecycle, like every other command dispatch here: the map
	// read must happen AFTER a restart finishes, or it hands back the process
	// that restart is destroying. A bare read can also miss the window between
	// stopAndWait clearing the old entry and StartAgent registering the new
	// one, and answer ErrAgentNotFound for an agent the user can see running.
	//
	// The two capability queries beside this one must NOT take that lock. Read
	// their comment: both run from callers that already hold it.
	p, err := m.providerAfterLifecycle(agentID)
	if err != nil {
		return "", err
	}
	writer, ok := p.(GoalWriter)
	if !ok {
		return "", ErrGoalControlUnsupported
	}
	if !slices.Contains(writer.SupportedGoalActions(), action) {
		return "", ErrGoalControlUnsupported
	}
	outcome, err := writer.PerformGoalAction(action, objective)
	return outcome.QueuedInput, err
}

// StopAgent stops the agent with the given agent ID.
// Returns true if the agent was found (and will eventually trigger onExit),
// false if the agent had already exited.
func (m *Manager) StopAgent(agentID string) bool {
	m.mu.RLock()
	entry, ok := m.agents[agentID]
	p := entry.providerOrNil()
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
	entry, ok := m.agents[agentID]
	p := entry.providerOrNil()
	var done chan struct{}
	if ok {
		done = entry.done
	}
	m.mu.RUnlock()

	if !ok {
		return false
	}
	if done == nil {
		// Production registration creates the provider and its done channel in one critical section.
		// A missing channel prevents the wait for onExit and can let cleanup remove replacement controls.
		slog.Error("agent registration has no completion channel; restart cleanup can remove replacement controls",
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
	if m.agents[agentID] == entry {
		delete(m.agents, agentID)
		delete(m.cachedOptionGroups, agentID)
	}
	m.mu.Unlock()

	return true
}

// ClearContext returns the new session ID or the provider's refusal or failure.
func (m *Manager) ClearContext(agentID string) (string, error) {
	unlock := m.LockAgent(agentID)
	defer unlock()

	m.mu.RLock()
	entry, ok := m.agents[agentID]
	p := entry.providerOrNil()
	m.mu.RUnlock()
	if !ok {
		return "", ErrAgentNotFound
	}
	return p.ClearContext()
}

// defaultModelIDForList resolves which model id should carry the default badge for a
// (possibly account-specific) model list, given the list's ids, the id the list already
// designates as default (marked) and the highest-preference entry present (first) --
// both "" when the list designates none. It is the ONE place the badge ladder lives;
// withModelGroupDefaultMarked reduces the projected "model" option group to these three
// arguments on every OptionGroups read. Priority:
//  1. The explicit LEAPMUX_*_DEFAULT_MODEL operator override.
//  2. The DefaultModelSentinel entry, for a provider that reports the sentinel in
//     its own catalog (Claude Code today). It tracks the account's own default
//     across plan tiers (e.g. Sonnet vs Fable).
//  3. The provider's configured default, when the list contains it. Codex's
//     configured default is the sentinel too, and reconcileModelCatalog keeps the
//     sentinel in the live catalog, so Codex badges the sentinel at this step for
//     a stopped AND a running agent.
//  4. A default the list itself designates -- what queryAvailableModels copied
//     from the CLI's own isDefault. Reached when the configured default is absent
//     from this account's list, so a stale registry default cannot move the badge
//     off the model the CLI marked.
//  5. The highest-preference entry present, so the picker always shows a badge.
//
// Returning "" means "don't touch the list's existing default": that's the case for a
// provider with no configured default at all (ACP providers registered with nil
// defaultModels, which self-mark the currently-selected model in buildACPModels). The
// steps 4 and 5 run only for a non-empty configured default, precisely so they don't
// clobber that per-agent marking.
func (r *Registry) defaultModelIDForList(ids []string, marked, first string, provider leapmuxv1.AgentProvider) string {
	if env := r.DefaultModelEnvOverride(provider); env != "" {
		// Honor the operator override only when it actually names a model in this
		// (possibly account-specific) list, matching by exact id or provider-
		// normalized alias. A stale or differently-spelled override -- a fully-
		// qualified "claude-opus-4-8[1m]" against the catalog's "opus[1m]", or a
		// model the account simply doesn't offer -- falls through to the rest of
		// the ladder so the picker still shows a default badge. Returning an absent
		// id unconditionally would leave withModelGroupDefaultMarked pointing the
		// group's DefaultValue at an id none of its options carry, badging nothing.
		//
		// This step outranks the sentinel (step 2) deliberately: an explicit operator
		// override naming the account's resolved concrete model (which
		// ensureSettledModelListed surfaces into the list once startup settles it)
		// badges that concrete model rather than the "default" placeholder -- the
		// sentinel does not retain the badge once a concrete identity the operator
		// pinned is present.
		if id := r.matchModelID(ids, provider, env); id != "" {
			return id
		}
	}
	// Each provider states for itself whether its catalog reports the sentinel, so
	// this ladder stays provider-neutral. Only Claude Code answers true today.
	if r.Plugin(provider).ReportsDefaultModelSentinel() && slices.Contains(ids, DefaultModelSentinel) {
		return DefaultModelSentinel
	}
	configured := r.DefaultModel(provider)
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
func (r *Registry) matchModelID(ids []string, provider leapmuxv1.AgentProvider, id string) string {
	if slices.Contains(ids, id) {
		return id
	}
	want := r.NormalizeModelID(provider, id)
	for _, mid := range ids {
		if r.NormalizeModelID(provider, mid) == want {
			return mid
		}
	}
	return ""
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

// optionGroupsFromCached projects a not-running (or transiently-empty-live) agent's cached catalog
// into the served option groups: the cache as-is when it is usable for the requested model, a
// model-dependent rebuild when ONLY the per-model groups are stale, else the static fallback.
// Shared by OptionGroups (which sources `cached` from the shared per-agent cache) and
// OptionGroupsForRow (which sources it from a caller's own row snapshot), so both resolve a
// not-running catalog identically. Does NOT mark the model default -- the caller does.
func (r *Registry) optionGroupsFromCached(cached cachedCatalog, provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	switch {
	case r.cachedCatalogUsable(cached, currentModel, provider):
		return cached.groups
	case len(cached.groups) > 0 && r.providerHasModelDependentGroups(provider) && currentModel != "":
		// The cache exists but is stale ONLY by model: its per-model effort/thinking groups were
		// built for a different model (an offline model edit moved the model column without
		// re-persisting the catalog -- see optionGroupsView). Rebuild just those for currentModel
		// and keep every other cached group, so dynamically-discovered model-INDEPENDENT groups
		// (Claude's Output Style / Fast Mode, surfaced only at runtime and absent from the static
		// templates) and any live-filtered group survive the edit instead of vanishing from the
		// settings popover until relaunch.
		return r.withModelDependentGroupsRebuilt(cached.groups, provider, currentModel)
	default:
		return r.fallbackOptionGroups(provider, currentModel)
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
	model := ReadOnlyValueGroup(OptionIDModel, ModelGroupLabel, OptionOrderModel, currentModel, "")
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
// Fast Mode at runtime, neither of which fallbackOptionGroups reproduces. Dropping the
// whole cache for the bare static fallback would lose those groups from the popover until the
// agent relaunches. The caller then overlays the persisted currents.
//
// Providers WITHOUT model-dependent groups -- the ACP permission-mode / primary-agent
// providers, whose effort/reasoning axes ARE model-independent server-driven config options
// living only in the cache -- always keep serving the cache across a model edit (the caller
// overlays the new model as current); falling through to their degenerate static fallback
// would silently drop those option groups from the popover until relaunch. An unknown
// requested model (currentModel == "") is also trusted as-is for everyone.
func (r *Registry) cachedCatalogUsable(cached cachedCatalog, currentModel string, provider leapmuxv1.AgentProvider) bool {
	return len(cached.groups) > 0 &&
		(currentModel == "" || cached.model == currentModel || !r.providerHasModelDependentGroups(provider))
}

// providerHasModelDependentGroups reports whether a provider's catalog carries
// model-dependent sub-groups (per-model effort tiers, and for Claude the per-model
// extended-thinking group) that must be rebuilt when the model changes. A provider has
// them exactly when it owns a model-dependent effort catalog -- Registry.ManagesEffort
// (Claude/Codex/Pi, and native Copilot, whose account decides both the models and each
// model's effort tiers). The ACP permission-mode / primary-agent providers do NOT:
// although every provider shares the default EffortSubGroups builder, it produces nothing
// for a model with no SupportedEfforts, and their effort/reasoning axes are
// model-independent server-driven config options -- so a model change doesn't invalidate
// any cached group, and falling through to the static fallback would needlessly drop
// those config options.
func (r *Registry) providerHasModelDependentGroups(provider leapmuxv1.AgentProvider) bool {
	return r.ManagesEffort(provider)
}

// modelDependentGroups builds the model group plus the per-model sub-groups for currentModel:
// its effort tiers, and for Claude the extended-thinking group whose label ("Adaptive" vs "On")
// is per model. These are exactly the groups that must be rebuilt when the selected model
// changes; the provider's static option-group templates (sandbox/network/permission/...) and
// any dynamically-discovered group are model-INDEPENDENT and handled separately. Current values
// are empty here; the caller overlays DB selections. Returns nil for an unknown provider.
func (r *Registry) modelDependentGroups(provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	reg, ok := r.byProvider[provider]
	if !ok {
		return nil
	}
	if currentModel == "" {
		currentModel = r.DefaultModel(provider)
	}
	var groups []*leapmuxv1.AvailableOptionGroup
	if mg := ModelOptionGroup(reg.DefaultModels, "", reg.ModelSubGroups); mg != nil {
		groups = append(groups, mg)
		// Emit the current model's model-dependent groups (its effort tiers, and
		// for Claude the extended-thinking group whose label is per model) as
		// top-level groups. This fallback is served while an agent is restarting
		// (not registered): without these, the settings popover briefly loses the
		// effort/thinking groups mid-restart, which flickers and can race a click.
		if reg.ModelSubGroups != nil {
			if m := FindAvailableModel(reg.DefaultModels, currentModel); m != nil {
				groups = append(groups, reg.ModelSubGroups(m)...)
			}
		}
	}
	return groups
}

// fallbackOptionGroups builds the fallback option groups for a provider
// that is not running and has no cached catalog: the model-dependent groups for
// currentModel (the agent's selected model, defaulting to the provider default when
// unknown -- so a Haiku agent shows no effort group while a Sonnet agent shows Sonnet's
// tiers), followed by the provider's static option-group templates. Current values are
// empty here; the caller overlays DB selections.
func (r *Registry) fallbackOptionGroups(provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	reg, ok := r.byProvider[provider]
	if !ok {
		return nil
	}
	return append(r.modelDependentGroups(provider, currentModel), reg.OptionGroups...)
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
//
// The cached model group is the first source of the new model's groups. It is the catalog of the
// last live run: it lists the models that the CLI reported, and each model option carries its own
// sub_groups. The static seed is only a fallback for a catalog that states no per-model groups,
// or does not list the new model. The seed can differ from the live list, and Pi and Cline seed a
// single placeholder model. A rebuild from the seed alone replaces the live model list with the
// seed, and it cannot describe a live model at all.
//
// The rebuild drops a stale group that the new model does not carry (the effort group after a
// switch to a model with no effort axis), but only when a source lists the new model. When no
// source lists it, the existing groups stay, as the browser does for the same case
// (withSelectedModelSubGroups).
func (r *Registry) withModelDependentGroupsRebuilt(cached []*leapmuxv1.AvailableOptionGroup, provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	fresh := r.cachedModelDependentGroups(cached, provider, currentModel)
	known := fresh != nil
	if !known {
		fresh = r.modelDependentGroups(provider, currentModel)
		known = r.staticModelListed(provider, currentModel)
	}
	if len(fresh) == 0 {
		return cached
	}
	freshByID := make(map[string]*leapmuxv1.AvailableOptionGroup, len(fresh))
	for _, g := range fresh {
		freshByID[g.GetId()] = g
	}
	// A group that varies by model is a sub_group of some model option, in the source that built
	// `fresh` or in the cached catalog.
	dependent := subGroupIDs(optionids.GroupByID(fresh, OptionIDModel))
	for id := range subGroupIDs(optionids.GroupByID(cached, OptionIDModel)) {
		dependent[id] = true
	}
	out := make([]*leapmuxv1.AvailableOptionGroup, 0, len(cached)+len(fresh))
	rebuilt := make(map[string]bool, len(fresh))
	for _, g := range cached {
		if f, ok := freshByID[g.GetId()]; ok {
			out = append(out, f)
			rebuilt[g.GetId()] = true
		} else if known && dependent[g.GetId()] {
			continue
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

// cachedModelDependentGroups returns what modelDependentGroups returns, read from the cached
// model group: the model group without a current value, then the sub_groups of the option for
// currentModel. It returns nil when the cached catalog cannot state them, so that the caller falls
// back to the static seed. That holds when the catalog has no model group, when no model option
// carries sub_groups (a stub, or a provider that keeps no per-model groups), and when no option
// matches currentModel. An option that matches and carries no sub_groups is an answer: that model
// has no model-dependent groups.
func (r *Registry) cachedModelDependentGroups(cached []*leapmuxv1.AvailableOptionGroup, provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	mg := optionids.GroupByID(cached, OptionIDModel)
	if len(subGroupIDs(mg)) == 0 {
		return nil
	}
	var option *leapmuxv1.AvailableOption
	for _, o := range mg.GetOptions() {
		if o.GetId() == currentModel {
			option = o
			break
		}
	}
	if option == nil {
		want := r.NormalizeModelID(provider, currentModel)
		for _, o := range mg.GetOptions() {
			if r.NormalizeModelID(provider, o.GetId()) == want {
				option = o
				break
			}
		}
	}
	if option == nil {
		return nil
	}
	model := proto.Clone(mg).(*leapmuxv1.AvailableOptionGroup)
	model.CurrentValue = ""
	return append([]*leapmuxv1.AvailableOptionGroup{model}, option.GetSubGroups()...)
}

// staticModelListed reports whether the static seed of the provider lists model. The seed states
// the model-dependent groups of a listed model, so a group that such a model lacks is really absent.
func (r *Registry) staticModelListed(provider leapmuxv1.AgentProvider, model string) bool {
	reg, ok := r.byProvider[provider]
	if !ok {
		return false
	}
	if model == "" {
		model = r.DefaultModel(provider)
	}
	return FindAvailableModel(reg.DefaultModels, model) != nil
}

// subGroupIDs collects the ids of the groups that the options of a model group carry as
// sub_groups. Each id names a group that varies by model.
func subGroupIDs(modelGroup *leapmuxv1.AvailableOptionGroup) map[string]bool {
	ids := map[string]bool{}
	for _, o := range modelGroup.GetOptions() {
		for _, g := range o.GetSubGroups() {
			ids[g.GetId()] = true
		}
	}
	return ids
}

// withModelGroupDefaultMarked re-derives the "model" group's DefaultValue via the
// LEAPMUX_*_DEFAULT_MODEL / sentinel / configured-default ladder (defaultModelIDForList).
// Returns the input unchanged when there is no model group or the default is
// already correct; otherwise returns a copy with only the model group replaced,
// leaving the shared catalog groups untouched.
func (r *Registry) withModelGroupDefaultMarked(groups []*leapmuxv1.AvailableOptionGroup, provider leapmuxv1.AgentProvider) []*leapmuxv1.AvailableOptionGroup {
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
	// preference present entry. Reducing the group to these three ids is what lets the hot
	// OptionGroups read path share the ladder without a proto<->ModelInfo round-trip.
	marked := ""
	if slices.Contains(ids, mg.GetDefaultValue()) {
		marked = mg.GetDefaultValue()
	}
	def := r.defaultModelIDForList(ids, marked, ids[0], provider)
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

// UpdateSettings applies the included options to a running agent. The result
// identifies live confirmations and values that still need a restart.
func (m *Manager) UpdateSettings(agentID string, options optionmap.Map) SettingsApplyResult {
	m.mu.RLock()
	entry, ok := m.agents[agentID]
	p := entry.providerOrNil()
	m.mu.RUnlock()
	if !ok {
		return RestartRequiredSettings(options)
	}
	return p.UpdateSettings(options)
}

// CurrentSettings returns the running provider's typed settings snapshot.
func (m *Manager) CurrentSettings(agentID string) SettingsApplyResult {
	m.mu.RLock()
	entry, ok := m.agents[agentID]
	p := entry.providerOrNil()
	m.mu.RUnlock()
	if !ok {
		return SettingsApplyResult{}
	}
	return p.SettingsSnapshot()
}

// NativeTurnRestartRequired asks the running provider whether its native turn
// changed launch-only state. A stopped or other provider needs no replacement.
func (m *Manager) NativeTurnRestartRequired(agentID string) bool {
	m.mu.RLock()
	entry, ok := m.agents[agentID]
	p := entry.providerOrNil()
	if ok {
		if exiting := entry != nil && entry.exiting; exiting {
			ok = false
		}
	}
	m.mu.RUnlock()
	if !ok {
		return false
	}
	restarter, ok := p.(NativeTurnRestarter)
	return ok && restarter.NativeTurnRestartRequired()
}

// HasAgent returns true while the Manager owns the agent's lifecycle slot. The
// slot stays owned through the exit callback so that callback can pause input
// before another process starts.
func (m *Manager) HasAgent(agentID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.agents[agentID]
	return ok
}

// RunningAgent returns the live provider of an agent, or nil when none runs.
//
// It takes NO lifecycle lock, so a caller that already holds LockAgent can use
// it where LockProvider would deadlock on the non-reentrant mutex. The agent
// may stop right after the read; a caller that needs it to stay must hold the
// lifecycle lock itself.
func (m *Manager) RunningAgent(agentID string) Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if entry := m.agents[agentID]; entry != nil {
		return entry.provider
	}
	return nil
}

// AgentAlive reports whether a LIVE process serves the agent.
//
// HasAgent answers a different question: it keeps the slot registered through
// the whole exit callback, so that the callback pauses durable input before a
// drain can restart the process. Between the process exit and the end of that
// callback the two answers differ, and the callback does several database
// writes, so the window is wide enough to lose a race. A caller that is about
// to WRITE to the provider must ask this one, or it writes to a closed pipe.
func (m *Manager) AgentAlive(agentID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.agents[agentID]
	if !ok {
		return false
	}
	return !entry.exiting
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
		providers = append(providers, p.provider)
	}
	m.mu.Unlock()

	for _, p := range providers {
		p.Stop()
	}
}

// ExitHandler is called when an agent process exits.
// agentID identifies the agent, exitCode is the process exit code, err is
// non-nil if the process exited with an error, and stopped is true when the
// exit was driven by an explicit Stop (a user interrupt, a relaunch, or a
// shutdown) rather than a crash. The background-task registry uses stopped to
// label rows 'stopped' vs 'interrupted'.
//
// The Manager keeps the exiting provider registered until this handler returns.
// This lets the handler pause durable input before the slot permits a restart.
//
// A handler must NEVER acquire the agent's lifecycle lock, directly or through
// a Manager method that takes it (SendInput, SendChildInput, RestartAgent,
// StopAndWaitAgent). It runs on the exit goroutine, and a lifecycle caller is
// normally waiting for that goroutine to finish while it holds that very lock,
// so an acquire here deadlocks the agent for the life of the process. Do the
// work that needs the lock AFTER the lifecycle call returns, the way the
// plan-execution path does.
type ExitHandler func(agentID string, exitCode int, err error, stopped bool)
