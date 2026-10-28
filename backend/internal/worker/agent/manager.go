package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/config"
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

	// turnRetries counts the goroutines that run accepted turn retries. Only
	// WaitTurnRetriesForTest reads it: no production caller waits for a retry.
	turnRetries sync.WaitGroup
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

// WaitTurnRetriesForTest waits until every turn retry that RetryTurn accepted
// finished its phases. A test reads the phases that ran after this returns,
// instead of a sleep that guesses when the retry goroutine ends.
func (m *Manager) WaitTurnRetriesForTest() {
	m.turnRetries.Wait()
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

// retainFailedStartBackgroundTasks ends only output accepted by an invoked failed attempt.
// It does not run registered-process cleanup or enter lifecycle APIs from publication.
func retainFailedStartBackgroundTasks(agentID string, output OutputEffectServices) {
	if output == nil {
		return
	}
	operation := output.CaptureBackgroundTaskStartFailure()
	if operation == nil {
		return
	}
	queue := output.OutputQueue()
	if queue == nil {
		slog.Error("the failed startup registry has no output queue", "agent_id", agentID)
		return
	}
	queue.Append(OutputWrite{Label: "finalize the failed startup registry", Run: operation})
	if err := queue.Flush(); err != nil && !errors.Is(err, ErrOutputInFlight) {
		slog.Warn("retain background tasks after startup failure", "agent_id", agentID, "error", err)
	}
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
	startInvoked := false
	invokeStart := func() (Agent, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		startInvoked = true
		return start(ctx, opts, sink)
	}
	provider, err := func() (Agent, error) {
		if !background {
			return invokeStart()
		}
		release, err := m.acquireStartupSlot(ctx)
		if err != nil {
			return nil, fmt.Errorf("wait for an agent startup slot: %w", err)
		}
		defer release()
		return invokeStart()
	}()
	if err != nil {
		if startInvoked {
			retainFailedStartBackgroundTasks(opts.AgentID, sink)
		}
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
			onExit(ProcessExit{AgentID: opts.AgentID, Err: err, Stopped: stopped, Output: sink})
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
// session. Kilo needs no observer: it reports its goal through its daemon's
// native session metadata, so the card follows that report instead.
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

// SteerPolicy reads the running provider's constant steering declarations.
func (m *Manager) SteerPolicy(agentID string) SteerPolicy {
	m.mu.RLock()
	provider := m.agents[agentID].providerOrNil()
	m.mu.RUnlock()
	var policy SteerPolicy
	if reporter, ok := provider.(SteerEchoReporter); ok {
		policy.RowAtEcho = reporter.SteerRowAtEcho()
	}
	if survivor, ok := provider.(SteerSurvivor); ok {
		policy.SurvivesTurnEnd = survivor.SteerSurvivesTurnEnd()
	}
	return policy
}

// PrepareSteer lets the provider persist buffered text before a steer row.
// The provider call runs outside the manager lock. An unknown agent changes nothing.
func (m *Manager) PrepareSteer(agentID string) {
	m.mu.RLock()
	provider := m.agents[agentID].providerOrNil()
	m.mu.RUnlock()
	if preparer, ok := provider.(SteerPreparer); ok {
		preparer.PrepareSteer()
	}
}

// SteerInputToSession uses the provider instance and session that the caller validated.
func SteerInputToSession(provider Agent, sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	steerer, ok := provider.(SessionInputSteerer)
	if !ok {
		return ErrSteeringUnsupported
	}
	err := steerer.SteerInputForSession(sessionID, content, attachments)
	if err == nil {
		observeGoalCommand(provider, GoalDeliverySteer, content)
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
	entry, unlock := m.lockRegistration(agentID)
	return entry.providerOrNil(), unlock
}

// lockRegistration returns the current registration, which can be nil, and
// retains the lifecycle lock. Always call release.
func (m *Manager) lockRegistration(agentID string) (*agentRegistration, func()) {
	unlock := m.LockAgent(agentID)

	m.mu.RLock()
	entry := m.agents[agentID]
	m.mu.RUnlock()

	return entry, unlock
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

// ApproveAutoReviewDenial asks the agent's running provider to record the user's
// approval of one denied automatic review, as AutoReviewDenialApprover states. A
// provider without that capability answers ErrAutoReviewApprovalUnsupported.
func (m *Manager) ApproveAutoReviewDenial(agentID string, entries []json.RawMessage, reviewID string) error {
	// providerAfterLifecycle for the reason UpdateGoal states: a bare read during a
	// restart hands back the process that the restart is destroying.
	p, err := m.providerAfterLifecycle(agentID)
	if err != nil {
		return err
	}
	approver, ok := p.(AutoReviewDenialApprover)
	if !ok {
		return ErrAutoReviewApprovalUnsupported
	}
	return approver.ApproveAutoReviewDenial(entries, reviewID)
}

// RetryTurn stops the running turn and sends its input again on another model.
//
// It returns when the provider accepts the retry. A separate goroutine changes history and resends the input.
// Each phase reports its failure in the transcript. The RPC caller waits for validation only.
//
// BeginTurnRetry runs under the lifecycle lock, so the offer check addresses one
// provider instance. Replace submits the history change under that lock.
// Start confirms the change after the lock releases, so context clear remains available during a delayed reply.
func (m *Manager) RetryTurn(agentID, turnID, model string) error {
	entry, release := m.lockRegistration(agentID)
	p := entry.providerOrNil()
	if p == nil {
		release()
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}
	retrier, ok := p.(TurnRetrier)
	if !ok {
		release()
		return ErrTurnRetryUnavailable
	}
	retry, err := retrier.BeginTurnRetry(turnID, model)
	release()
	if err != nil {
		return err
	}
	m.turnRetries.Add(1)
	go func() {
		defer m.turnRetries.Done()
		m.runTurnRetry(agentID, entry, retry)
	}()
	return nil
}

// runTurnRetry runs the phases of one accepted turn retry.
//
// The registration pointer identifies the provider instance that accepted the
// retry. A provider value cannot do that, because a provider type need not be
// comparable. A restart or a stop between the acceptance and this goroutine
// replaces or retires the registration, and the retry then ends without a
// request.
func (m *Manager) runTurnRetry(agentID string, accepted *agentRegistration, retry TurnRetry) {
	entry, release := m.lockRegistration(agentID)
	m.mu.RLock()
	current := entry == accepted && !entry.exiting
	m.mu.RUnlock()
	if !current {
		release()
		retry.Abandon(fmt.Errorf("the agent %s restarted or stopped before the turn retry ran", agentID))
		return
	}
	err := retry.Replace()
	release()
	// The provider states each failure in the transcript and logs it, and a user
	// stop also ends a phase with an error. So these records are for debugging
	// only.
	if err != nil {
		slog.Debug("turn retry ended before the history change", "agent_id", agentID, "error", err)
		return
	}
	if err := retry.Start(); err != nil {
		slog.Debug("turn retry ended before its turn started", "agent_id", agentID, "error", err)
	}
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
// ProcessExit keeps the agent, the provider Wait error, and its original output.
// Stopped is true when the exit was driven by an explicit Stop (a user
// interrupt, a relaunch, or a shutdown) rather than a crash. The
// background-task registry uses stopped to label rows 'stopped' vs
// 'interrupted'.
//
// Err is nil after a clean exit. A non-zero exit status is an *exec.ExitError
// inside err, which a provider's Wait can join with other errors, so read it
// with errors.As, never with a type assertion.
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
type ExitHandler func(ProcessExit)

// ProcessExit retains the output services of the physical process that ended.
// A replacement may already have published a different sink for the same agent ID.
type ProcessExit struct {
	AgentID string
	Err     error
	Stopped bool
	Output  OutputEffectServices
}
