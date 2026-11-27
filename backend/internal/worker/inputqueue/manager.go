package inputqueue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/drain"
)

type coordinator struct {
	mu          sync.Mutex
	draining    bool
	dispatches  drain.Counter
	steerPolicy agent.SteerPolicy
	echoSteer   *pendingEchoSteer
	// drainAgain records a drain request that arrived while the drain loop held
	// the flag above. scheduleDrain cannot start a second loop for it, and the
	// running loop released the coordinator lock across a provider call, so the
	// request can carry state that the loop still misses: a turn that ended
	// inside that call, or an item enqueued during it. The loop consumes the
	// request before it stops, and the request dies with the read that answers
	// it.
	drainAgain bool
	// plannedRestarts is zero or one. A restart refuses another restart until
	// its guard finishes, so one guard owns the temporary pause.
	//
	// This replaces a mutex that BeginPlannedRestart held until Finish. That
	// mutex deadlocked the Worker: Finish runs only after StopAndWaitAgent
	// returns, StopAndWaitAgent waits for the exit goroutine, and a crashed
	// process makes that goroutine call Pause, which took the same mutex. The
	// durable pause_owner column now carries what the mutex protected, so no
	// lock spans a process stop.
	plannedRestarts int
	// publications retain committed data until unlocked observer delivery.
	publications []queuePublication
	publishing   bool
	// maturity is the one timer for the earliest scheduled deadline; see
	// maturity.go.
	maturity maturityTimer
	// retired records that the agent closed. A closed agent takes no input,
	// so its queue arms no maturity timer and starts no drain again.
	retired bool
	// pauseEvents increments under mu whenever a pause, failure, crash,
	// unechoed-steer resolution, planned restart, or retirement is recorded.
	// Advance compares this counter across an unlocked Steer so a crash or
	// failure that kept an earlier MANUAL pause reason still stops the
	// follow-up resume.
	pauseEvents uint64
	// advancing holds true while Advance runs for this agent so concurrent
	// Advance calls return the current snapshot instead of racing a steer or
	// resuming behind an in-flight advance.
	advancing bool
}

// pendingEchoSteer retains an early callback until its provider call returns.
// Each caller keeps its own pointer, even when the next steer starts before this call returns.
type pendingEchoSteer struct {
	inputID string
	outcome agent.SteerOutcome
}

type queuePublication struct {
	snapshot    Snapshot
	accepted    []AcceptedTranscript
	afterAccept func()
	failure     *queuePublicationFailure
}

type queuePublicationFailure struct {
	level      slog.Level
	message    string
	attributes []any
}

func cloneQueueSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Items = slices.Clone(snapshot.Items)
	for index := range snapshot.Items {
		snapshot.Items[index].Metadata = slices.Clone(snapshot.Items[index].Metadata)
	}
	return snapshot
}

// enqueueSnapshotLocked records one snapshot publication. The caller holds c.mu.
func (m *Manager) enqueueSnapshotLocked(c *coordinator, snapshot Snapshot) {
	m.enqueuePublicationLocked(c, queuePublication{snapshot: cloneQueueSnapshot(snapshot)})
}

// enqueuePublicationLocked records one publication in commit order and
// re-arms the maturity timer from its snapshot. Every publication that
// carries a snapshot passes through here, so no committed order escapes the
// timer. The caller holds c.mu.
func (m *Manager) enqueuePublicationLocked(c *coordinator, publication queuePublication) {
	c.publications = append(c.publications, publication)
	m.rearmMaturityLocked(c, publication.snapshot)
}

// enqueueFailureLocked retains the failure in drain order. The caller holds c.mu.
func (m *Manager) enqueueFailureLocked(c *coordinator, message string, attributes ...any) {
	m.enqueueLogLocked(c, slog.LevelError, message, attributes...)
}

func (m *Manager) enqueueLogLocked(c *coordinator, level slog.Level, message string, attributes ...any) {
	c.publications = append(c.publications, queuePublication{failure: &queuePublicationFailure{
		level: level, message: message, attributes: slices.Clone(attributes),
	}})
}

func (m *Manager) unlockAndPublish(c *coordinator) {
	c.mu.Unlock()
	m.drainPublications(c)
}

// drainPublications calls observers outside the coordinator and admission leases.
// Reentry leaves delivery to the current drainer.
func (m *Manager) drainPublications(c *coordinator) {
	c.mu.Lock()
	if c.publishing || len(c.publications) == 0 {
		c.mu.Unlock()
		return
	}
	c.publishing = true
	c.mu.Unlock()
	claimed := true
	defer func() {
		if !claimed {
			return
		}
		c.mu.Lock()
		c.publishing = false
		c.mu.Unlock()
	}()
	for {
		c.mu.Lock()
		if len(c.publications) == 0 {
			c.publishing = false
			claimed = false
			c.mu.Unlock()
			return
		}
		publication := c.publications[0]
		c.publications[0] = queuePublication{}
		c.publications = c.publications[1:]
		c.mu.Unlock()
		if publication.failure != nil {
			slog.Log(context.Background(), publication.failure.level, publication.failure.message, publication.failure.attributes...)
			continue
		}
		for _, accepted := range publication.accepted {
			m.observer.InputAccepted(accepted)
		}
		m.observer.QueueChanged(publication.snapshot)
		if publication.afterAccept != nil {
			publication.afterAccept()
		}
	}
}

// ReconcileProviderTurn writes only while the original provider admission remains exact.
func (m *Manager) ReconcileProviderTurn(ctx context.Context, agentID string, admission agent.TurnStateAdmission) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	if admission == nil {
		return Snapshot{}, nil
	}
	state, lease := admission.Acquire()
	if lease == nil {
		return Snapshot{}, nil
	}
	defer lease.Release()
	c := m.coordinator(agentID)
	c.mu.Lock()
	snapshot, changed, err := m.store.setTurnState(ctx, agentID, state.Active, state.Steerable, c.steerPolicy.RowAtEcho && !c.steerPolicy.SurvivesTurnEnd)
	if err == nil && changed {
		m.enqueueSnapshotLocked(c, snapshot)
	}
	c.mu.Unlock()
	lease.Release()
	m.drainPublications(c)
	if err != nil {
		return snapshot, err
	}
	m.drainIfReleased(agentID, snapshot)
	return snapshot, nil
}

// PlannedRestart keeps explicit dispatch operations behind one agent restart.
// Automatic drains stay blocked by the durable pause that BeginPlannedRestart
// creates.
type PlannedRestart struct {
	manager     *Manager
	coordinator *coordinator
	agentID     string
	resumeQueue bool
	recovery    *inputRecoveryPause
	once        sync.Once
	err         error
}

type Manager struct {
	store      *Store
	dispatcher Dispatcher
	observer   Observer

	mu           sync.Mutex
	coordinators map[string]*coordinator

	lifecycleMu sync.Mutex
	// activity counts the active queue work that beginActivity admitted. A
	// counter rather than a sync.WaitGroup, so the worker shutdown can wait
	// for it only until its deadline.
	activity  drain.Counter
	stopped   bool
	recovered map[string]struct{}
}

func NewManager(store *Store, dispatcher Dispatcher, observer Observer) *Manager {
	if observer == nil {
		observer = NopObserver{}
	}
	return &Manager{
		store: store, dispatcher: dispatcher, observer: observer,
		coordinators: make(map[string]*coordinator),
		recovered:    make(map[string]struct{}),
	}
}

// mutateAndDrain runs one store mutation under the agent's coordinator lock,
// broadcasts the new snapshot, and then schedules a drain OUTSIDE that lock.
// The order is load-bearing: scheduleDrain takes the same non-reentrant mutex,
// so a copy that called it while holding the lock would deadlock.
//
// A mutation that reports no change broadcasts nothing. The turn signal
// reconciles the queue on every publish of the provider's flag, including the
// many that repeat the state the queue already holds.
func (m *Manager) mutateAndDrain(agentID string, mutate func() (Snapshot, bool, error)) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	snapshot, changed, err := mutate()
	if err == nil && changed {
		m.enqueueSnapshotLocked(c, snapshot)
	}
	m.unlockAndPublish(c)
	if err != nil {
		return snapshot, err
	}
	if changed {
		m.scheduleDrain(agentID)
		return snapshot, nil
	}
	// A mutation that moved nothing still restarts a drain loop that a store
	// error stopped. That loop leaves the items queued with no turn and no
	// pause, and it schedules nothing further, so the queue waits for an event
	// that only a new user action produces. The turn signal repeats often
	// enough to be that event.
	m.drainIfReleased(agentID, snapshot)
	return snapshot, nil
}

// drainIfReleased schedules an unpaused queue with a waiting item.
// An active turn permits a recorded plan replacement or a steerable question answer.
//
// The caller must NOT hold the coordinator lock, because scheduleDrain takes
// that same non-reentrant mutex.
func (m *Manager) drainIfReleased(agentID string, snapshot Snapshot) {
	if canDrainAutomatically(snapshot) {
		m.scheduleDrain(agentID)
	}
}

// canDrainAutomatically selects work that the store must validate again before delivery.
func canDrainAutomatically(snapshot Snapshot) bool {
	head := snapshot.HeadIndex()
	if head < 0 {
		return false
	}
	// A head whose scheduled instant has not arrived does not drain. The
	// maturity timer's edge is what re-enables the drain.
	if snapshot.Items[head].ReorderGroup != "" {
		return false
	}
	if snapshot.Paused {
		return canSteerAutomatically(snapshot)
	}
	backgroundBlocked := blockedByBackgroundWork(snapshot.ActiveBackgroundWork, snapshot.BypassBackgroundWorkInputID, snapshot.Items[head].OrderingMetadata)
	return (!snapshot.ActiveTurn && !backgroundBlocked) || snapshot.Items[head].requiresContextReplacement() || canSteerAutomatically(snapshot)
}

func canSteerAutomatically(snapshot Snapshot) bool {
	head := snapshot.HeadIndex()
	canAnswer := !snapshot.Paused || snapshot.PauseReason == leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_PROCESS_RECOVERY
	return canAnswer && snapshot.ActiveTurn && head >= 0 &&
		automaticSteeringKind(snapshot.Items[head].Kind) && snapshot.Items[head].CanSteer
}

// alwaysChanged adapts a store call whose every success moves the queue. The
// changed Boolean belongs to the two calls that reconcile against a signal the
// provider repeats; for an explicit user action the answer is always yes.
func alwaysChanged(mutate func() (Snapshot, error)) func() (Snapshot, bool, error) {
	return func() (Snapshot, bool, error) {
		snapshot, err := mutate()
		return snapshot, true, err
	}
}

func (m *Manager) beginActivity() bool {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.stopped {
		return false
	}
	m.activity.Add()
	return true
}

// beginDrainActivity is beginActivity for a drain. It also refuses an agent
// whose recovered queue still waits for DrainRecovered.
func (m *Manager) beginDrainActivity(agentID string) bool {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.stopped {
		return false
	}
	if _, held := m.recovered[agentID]; held {
		return false
	}
	m.activity.Add()
	return true
}

func (m *Manager) endActivity() {
	m.activity.Done()
}

func (m *Manager) isStopped() bool {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	return m.stopped
}

// Stop refuses new queue work and returns the channel that closes once active
// work ends, so the caller can join it with a limit of its own.
// An active dispatch finishes its current item. Maturity timers are stopped
// without holding lifecycleMu while a coordinator lock is taken: a maturity
// callback in flight holds beginActivity's registration, so the join below
// covers it, and one that has not started finds admission closed.
//
// The channel is sampled after admission closed, so no Add can follow it:
// beginActivity refuses under the same lock that set stopped.
func (m *Manager) Stop() <-chan struct{} {
	m.lifecycleMu.Lock()
	m.stopped = true
	m.lifecycleMu.Unlock()
	m.mu.Lock()
	coordinators := make([]*coordinator, 0, len(m.coordinators))
	for _, c := range m.coordinators {
		coordinators = append(coordinators, c)
	}
	m.mu.Unlock()
	for _, c := range coordinators {
		c.mu.Lock()
		stopMaturityLocked(c)
		c.mu.Unlock()
	}
	return m.activity.DoneChan()
}

// StopAndWait closes queue admission and then joins active work.
func (m *Manager) StopAndWait() {
	<-m.Stop()
}

// stopRoundAndUnlock ends one pass of the drain loop, releases the coordinator
// lock, and reports whether the loop stops. A drain request that arrived during
// the provider call is news that the loop still misses, so the loop repeats for
// it instead of stopping. Nothing else can take that request, because
// scheduleDrain refused to start a second loop while this one held the draining
// flag.
//
// The repeat has a limit: it happens once for each request, and each request
// costs one scheduleDrain call from a real state change.
//
// The caller must hold c.mu. This method releases it on every path.
func (c *coordinator) stopRoundAndUnlock() bool {
	defer c.mu.Unlock()
	if c.drainAgain {
		c.drainAgain = false
		return false
	}
	c.draining = false
	return true
}

func (m *Manager) stopRoundAndPublish(c *coordinator) bool {
	stopped := c.stopRoundAndUnlock()
	m.drainPublications(c)
	return stopped
}

func (m *Manager) coordinator(agentID string) *coordinator {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.coordinators[agentID]
	if c == nil {
		c = &coordinator{}
		m.coordinators[agentID] = c
	}
	return c
}

// refuseUnacceptedKind rejects a kind that this agent can never take, BEFORE
// the mutation opens its write transaction.
//
// The test must not run inside that transaction. The dispatcher answers it
// from the agents table, and SQLite admits one writer, so a read issued on a
// second connection while the mutation holds the write lock waits for a
// transaction that waits for the read.
func (m *Manager) refuseUnacceptedKind(agentID string, kind leapmuxv1.AgentInputKind) error {
	if m.dispatcher == nil || m.dispatcher.AcceptsKind(agentID, kind) {
		return nil
	}
	return fmt.Errorf("%w: this agent does not accept that input", ErrInvalidInput)
}

// mutateLocked runs one store mutation under the agent's coordinator lock and
// broadcasts the new snapshot when the mutation changed the durable state.
//
// It never drains, and the name says so: a caller whose mutation can release
// the queue uses mutateAndDrain, or schedules the drain itself AFTER it
// unlocks. scheduleDrain takes this same non-reentrant mutex, so a drain from
// inside the locked section deadlocks the Worker.
func (m *Manager) mutateLocked(agentID string, mutate func() (Snapshot, bool, error)) (Snapshot, error) {
	return m.mutateAdmitted(agentID, nil, mutate)
}

// mutateAdmitted keeps a captured provider owner current through the commit.
// Observer delivery runs after both the coordinator and owner lease release.
func (m *Manager) mutateAdmitted(agentID string, admission agent.AutoRetryAdmission, mutate func() (Snapshot, bool, error)) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	var lease agent.AutoRetryLease
	if admission != nil {
		lease = admission.Acquire()
		if lease == nil {
			return Snapshot{}, nil
		}
	}
	c := m.coordinator(agentID)
	c.mu.Lock()
	defer func() {
		c.mu.Unlock()
		if lease != nil {
			lease.Release()
		}
		m.drainPublications(c)
	}()
	snapshot, changed, err := mutate()
	if err == nil && changed {
		m.enqueueSnapshotLocked(c, snapshot)
	}
	return snapshot, err
}

func (m *Manager) Enqueue(ctx context.Context, input NewItem) (Snapshot, error) {
	return m.EnqueueWithMutation(ctx, input, nil)
}

// StageInitialMessage writes the first input for a root agent created in tx.
// The new agent is private to tx, so the queue needs no coordinator lock or
// publication. When inputID is blank, a fresh ID is generated; otherwise the
// caller-supplied ID is validated and stored verbatim. The caller wakes the
// queue after it records the startup outcome.
func (m *Manager) StageInitialMessage(ctx context.Context, tx *sql.Tx, agentID, inputID, text string, sender *AgentSender) error {
	itemID := strings.TrimSpace(inputID)
	if itemID == "" {
		itemID = id.Generate()
	}
	prepared, err := m.store.prepareNewItemForEnqueue(NewItem{
		ID: itemID, AgentID: agentID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Sender: sender, Text: text, ReclassifyOnEdit: true,
	})
	if err != nil {
		return err
	}
	_, err = enqueuePreparedItemWithEphemeral(ctx, tx, prepared.item, prepared.key, m.store.now(), nil)
	return err
}

// EnqueueReportingAdded is Enqueue that also reports whether this call added the
// item. A repeat of an item that the queue holds, or that it already delivered,
// adds nothing and reports false. A caller that must act once for each item acts
// only on true.
func (m *Manager) EnqueueReportingAdded(ctx context.Context, input NewItem) (Snapshot, bool, error) {
	snapshot, receipt, err := m.EnqueueWithReceipt(ctx, input)
	return snapshot, receipt.Added, err
}

// EnqueueWithReceipt returns the queue snapshot and the verified outcome
// of the enqueue, including an accepted input's transcript sequence.
func (m *Manager) EnqueueWithReceipt(ctx context.Context, input NewItem) (Snapshot, EnqueueReceipt, error) {
	return m.enqueue(ctx, input, nil)
}

// EnqueueWithMutation commits the queued input and related database changes together.
// The callback must not commit the transaction or perform provider I/O.
func (m *Manager) EnqueueWithMutation(ctx context.Context, input NewItem, apply func(*sql.Tx) error) (Snapshot, error) {
	snapshot, _, err := m.enqueue(ctx, input, apply)
	return snapshot, err
}

func (m *Manager) enqueue(ctx context.Context, input NewItem, apply func(*sql.Tx) error) (Snapshot, EnqueueReceipt, error) {
	// Prepare and hash once outside the coordinator lock: classification
	// rewrites slash commands and agent-sent messages into their stored kind,
	// and hashing attachments reads up to MaxItemBytes.
	prepared, err := m.store.prepareNewItemForEnqueue(input)
	if err != nil {
		return Snapshot{}, EnqueueReceipt{}, err
	}
	if err := m.refuseUnacceptedKind(prepared.item.AgentID, prepared.item.Kind); err != nil {
		return Snapshot{}, EnqueueReceipt{}, err
	}
	// mutateAndDrain can refuse before it calls the store, and then nothing was
	// added.
	var receipt EnqueueReceipt
	snapshot, err := m.mutateAndDrain(prepared.item.AgentID, alwaysChanged(func() (Snapshot, error) {
		inner, innerReceipt, innerErr := m.store.enqueuePrepared(ctx, prepared, apply)
		receipt = innerReceipt
		return inner, innerErr
	}))
	return snapshot, receipt, err
}

func (m *Manager) Snapshot(ctx context.Context, agentID string) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot, err := m.store.Snapshot(ctx, agentID)
	if err == nil {
		// A plain read is an arm point too: time moved since the last one, so
		// a deadline may have come due while its timer still waits.
		m.rearmMaturityLocked(c, snapshot)
	}
	return snapshot, err
}

// GetItem returns a snapshot and the full un-truncated text of inputID from
// one consistent read without acquiring a mutating edit lease.
func (m *Manager) GetItem(ctx context.Context, agentID, inputID string) (Snapshot, string, error) {
	if !m.beginActivity() {
		return Snapshot{}, "", ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	defer c.mu.Unlock()
	snap, text, err := m.store.GetItem(ctx, agentID, inputID)
	if err != nil {
		return Snapshot{}, "", err
	}
	m.rearmMaturityLocked(c, snap)
	return snap, text, nil
}

func (m *Manager) BeginEdit(ctx context.Context, agentID, inputID, clientID string, takeover bool) (Snapshot, string, []Attachment, error) {
	if !m.beginActivity() {
		return Snapshot{}, "", nil, ErrManagerStopped
	}
	defer m.endActivity()
	var text string
	var attachments []Attachment
	snapshot, err := m.mutateLocked(agentID, func() (Snapshot, bool, error) {
		var inner Snapshot
		var innerErr error
		inner, text, attachments, innerErr = m.store.BeginEdit(ctx, agentID, inputID, clientID, takeover)
		return inner, true, innerErr
	})
	return snapshot, text, attachments, err
}

func (m *Manager) Update(ctx context.Context, request UpdateRequest) (Snapshot, error) {
	// An edit can reclassify the item, so the new text can carry a kind this
	// agent cannot take. Store.Update refuses it below on a stale read only;
	// this refuses it before the write transaction opens.
	kind, err := m.store.KindAfterEdit(ctx, request.AgentID, request.InputID, request.Text)
	if err == nil {
		err = m.refuseUnacceptedKind(request.AgentID, kind)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Snapshot{}, err
	}
	return m.mutateAndDrain(request.AgentID, alwaysChanged(func() (Snapshot, error) {
		return m.store.Update(ctx, request)
	}))
}

func (m *Manager) CancelEdit(ctx context.Context, agentID, inputID, clientID string) (Snapshot, error) {
	return m.mutateAndDrain(agentID, alwaysChanged(func() (Snapshot, error) {
		return m.store.CancelEdit(ctx, agentID, inputID, clientID)
	}))
}

func (m *Manager) Delete(ctx context.Context, agentID, inputID string) (Snapshot, error) {
	return m.mutateAndDrain(agentID, alwaysChanged(func() (Snapshot, error) {
		return m.store.Delete(ctx, agentID, inputID)
	}))
}

func (m *Manager) Move(ctx context.Context, agentID, inputID, beforeInputID string) (Snapshot, error) {
	return m.mutateAndDrain(agentID, alwaysChanged(func() (Snapshot, error) {
		return m.store.Move(ctx, agentID, inputID, beforeInputID)
	}))
}

func (m *Manager) SetPaused(ctx context.Context, agentID string, paused bool) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	reason := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED
	if paused {
		reason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL
	}
	c.mu.Lock()
	var snapshot Snapshot
	var err error
	if !paused && c.plannedRestarts == 0 {
		snapshot, err = m.store.resumeWithNoRestart(ctx, agentID)
	} else {
		snapshot, err = m.store.SetPaused(ctx, agentID, paused, reason)
	}
	if err == nil {
		if paused {
			c.pauseEvents++
		}
		m.enqueueSnapshotLocked(c, snapshot)
	}
	m.unlockAndPublish(c)
	if err == nil && !paused {
		m.scheduleDrain(agentID)
	}
	return snapshot, err
}

func (m *Manager) RefreshSnapshot(ctx context.Context, agentID string) (Snapshot, error) {
	return m.mutateLocked(agentID, alwaysChanged(func() (Snapshot, error) {
		return m.store.RefreshSnapshot(ctx, agentID)
	}))
}

func (m *Manager) Pause(ctx context.Context, agentID string, reason leapmuxv1.AgentInputQueuePauseReason) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	return m.mutateLocked(agentID, func() (Snapshot, bool, error) {
		snapshot, err := m.store.Pause(ctx, agentID, reason)
		if err == nil {
			c.pauseEvents++
		}
		return snapshot, true, err
	})
}

// PauseForArchive stops automatic dispatch before an archived agent process
// stops. It preserves a pause that another cause created.
func (m *Manager) PauseForArchive(ctx context.Context, agentID string) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	return m.mutateLocked(agentID, func() (Snapshot, bool, error) {
		snapshot, changed, err := m.store.PauseForArchive(ctx, agentID)
		if err == nil {
			c.pauseEvents++
		}
		return snapshot, changed, err
	})
}

// ResumeAfterArchive resumes only a pause that PauseForArchive created.
func (m *Manager) ResumeAfterArchive(ctx context.Context, agentID string) (Snapshot, error) {
	return m.mutateAndDrain(agentID, func() (Snapshot, bool, error) {
		return m.store.ResumeAfterArchive(ctx, agentID)
	})
}

// TurnEnded and TurnStarted reconcile the queue against the turn state that the
// agent's provider publishes. The provider owns the flag and its optional
// steering classification. Its own SendInput refuses input from the same flag.
// The queue follows that state instead of keeping a second answer.
//
// Both are idempotent, because a provider republishes the unchanged value
// freely. A call that moves nothing broadcasts nothing.
func (m *Manager) TurnEnded(ctx context.Context, agentID string) (Snapshot, error) {
	c := m.coordinator(agentID)
	return m.mutateAndDrain(agentID, func() (Snapshot, bool, error) {
		return m.store.TurnEnded(ctx, agentID, c.steerPolicy.RowAtEcho && !c.steerPolicy.SurvivesTurnEnd)
	})
}

// TurnAbandoned clears a turn that no dispatch owns. A process boundary calls
// it: a process that starts or exits runs no turn, and a turn the agent process
// reported has no other end once that process is gone. See
// Store.AbandonUnownedTurn for why a turn a dispatch owns is left alone.
func (m *Manager) TurnAbandoned(ctx context.Context, agentID string) (Snapshot, error) {
	return m.mutateAndDrain(agentID, func() (Snapshot, bool, error) {
		return m.store.AbandonUnownedTurn(ctx, agentID)
	})
}

func (m *Manager) TurnStarted(ctx context.Context, agentID string, steerable bool) (Snapshot, error) {
	return m.mutateAndDrain(agentID, func() (Snapshot, bool, error) {
		return m.store.TurnStarted(ctx, agentID, steerable)
	})
}

// BackgroundWorkChanged refreshes queued metadata and drains once active
// background work finishes and unblocks automatic dispatch.
func (m *Manager) BackgroundWorkChanged(ctx context.Context, agentID string) (Snapshot, error) {
	return m.mutateAndDrain(agentID, func() (Snapshot, bool, error) {
		return m.store.RefreshForBackgroundWork(ctx, agentID)
	})
}

// BackgroundWorkChangedTree reconciles every agent in rootAgentID's tree that
// currently holds queued input. ListQueuedAgentIDsInTree runs outside any
// coordinator lock so lock order stays acyclic.
func (m *Manager) BackgroundWorkChangedTree(ctx context.Context, rootAgentID string) error {
	if !m.beginActivity() {
		return ErrManagerStopped
	}
	defer m.endActivity()
	agentIDs, err := m.store.ListQueuedAgentIDsInTree(ctx, rootAgentID)
	if err != nil {
		return err
	}
	var errs error
	for _, agentID := range agentIDs {
		if _, err := m.BackgroundWorkChanged(ctx, agentID); err != nil && !errors.Is(err, ErrManagerStopped) {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

func (m *Manager) Retry(ctx context.Context, agentID, inputID string, confirmUncertain bool) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	if c.retired {
		c.mu.Unlock()
		return Snapshot{}, ErrConflict
	}
	c.dispatches.Add()
	defer c.dispatches.Done()
	snapshot, err := m.store.Retry(ctx, agentID, inputID, confirmUncertain)
	if err != nil {
		m.unlockAndPublish(c)
		return Snapshot{}, err
	}
	m.enqueueSnapshotLocked(c, snapshot)
	prepared, snapshot, err := m.store.PrepareRetry(ctx, agentID)
	if err != nil || prepared == nil {
		m.unlockAndPublish(c)
		if err == nil {
			m.drainIfReleased(agentID, snapshot)
		}
		return snapshot, err
	}
	m.enqueueSnapshotLocked(c, snapshot)
	// The store reserved the item as DISPATCHING, so no other caller can take
	// it. Release the lock for the provider call: it can stop and relaunch the
	// agent process, and the exit goroutine that a stop wakes needs this same
	// lock to pause the queue.
	m.unlockAndPublish(c)
	result, dispatchErr := m.dispatcher.Dispatch(prepared.Item)
	c.mu.Lock()
	if dispatchErr != nil {
		snapshot, storeErr := m.recordDispatchFailure(c, ctx, *prepared, dispatchErr)
		m.unlockAndPublish(c)
		if storeErr != nil {
			return snapshot, storeErr
		}
		// A busy refusal returns the item to an OPEN queue, and the turn that
		// refused it delivers it. A turn that ended inside the provider call
		// leaves no later edge, so only this drain moves the item.
		m.drainIfReleased(agentID, snapshot)
		return snapshot, nil
	}
	snapshot, _, err = m.acceptDispatch(c, ctx, *prepared, result)
	if err != nil {
		m.unlockAndPublish(c)
		return Snapshot{}, err
	}
	m.unlockAndPublish(c)
	// Retry lifted the delivery pause, so the items behind the retried head
	// must dispatch too. Without this the queue stalls with no visible reason.
	m.scheduleDrain(agentID)
	return snapshot, nil
}

// dispatchPreparedBypassLocked dispatches an already-prepared background-work
// bypass item via Dispatcher.Dispatch. The caller must hold c.mu on entry, and
// c.mu remains held on return so callers can apply atomic post-accept
// transitions before publishing and draining.
func (m *Manager) dispatchPreparedBypassLocked(
	c *coordinator,
	ctx context.Context,
	agentID string,
	prepared PreparedDispatch,
	snapshot Snapshot,
	hasRecovery func(string) bool,
) (Snapshot, bool, bool, error) {
	m.enqueueSnapshotLocked(c, snapshot)
	m.unlockAndPublish(c)

	result, dispatchErr := m.dispatcher.Dispatch(prepared.Item)
	recoveryAfter := hasRecovery != nil && hasRecovery(agentID)

	c.mu.Lock()
	if dispatchErr != nil {
		failedSnapshot, storeErr := m.recordDispatchFailure(c, ctx, prepared, dispatchErr)
		if storeErr != nil {
			return failedSnapshot, false, recoveryAfter, storeErr
		}
		return failedSnapshot, false, recoveryAfter, nil
	}
	acceptedSnapshot, persisted, acceptErr := m.acceptDispatch(c, ctx, prepared, result)
	if acceptErr != nil {
		return Snapshot{}, false, recoveryAfter, acceptErr
	}
	return acceptedSnapshot, persisted, recoveryAfter, nil
}

// finishPreparedBypassLocked finishes an already-prepared background-work
// bypass dispatch, releases c.mu, publishes queued snapshots, and schedules
// the follow-up drain. The caller must hold c.mu on entry; c.mu is released
// on every return path.
func (m *Manager) finishPreparedBypassLocked(
	c *coordinator,
	ctx context.Context,
	agentID string,
	prepared PreparedDispatch,
	snapshot Snapshot,
	alwaysDrain bool,
) (Snapshot, error) {
	finished, _, _, err := m.dispatchPreparedBypassLocked(c, ctx, agentID, prepared, snapshot, nil)
	m.unlockAndPublish(c)
	if err == nil {
		if alwaysDrain {
			m.scheduleDrain(agentID)
		} else {
			m.drainIfReleased(agentID, finished)
		}
	}
	return finished, err
}

// Steer prepares output ordering, delivers one input, and applies the provider's echo policy.
func (m *Manager) Steer(ctx context.Context, agentID, inputID string) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	if m.dispatcher == nil || !m.dispatcher.SupportsSteering(agentID) {
		return Snapshot{}, ErrSteeringUnsupported
	}
	c.mu.Lock()
	if c.retired {
		c.mu.Unlock()
		return Snapshot{}, ErrConflict
	}
	c.dispatches.Add()
	c.mu.Unlock()
	defer c.dispatches.Done()
	policy, err := m.dispatcher.SteerPolicy(agentID)
	if err != nil {
		return Snapshot{}, err
	}
	c.mu.Lock()
	peek, err := m.store.Snapshot(ctx, agentID)
	if err != nil {
		c.mu.Unlock()
		return Snapshot{}, err
	}
	if peek.ActiveTurn {
		// PrepareSteer may flush output sinks that re-enter Manager, so keep
		// c.mu unlocked across it.
		c.mu.Unlock()
		m.dispatcher.PrepareSteer(agentID)
		c.mu.Lock()
	}
	prepared, snapshot, err := m.store.PrepareSteer(ctx, agentID, inputID, policy.RowAtEcho)
	if err != nil {
		m.unlockAndPublish(c)
		return Snapshot{}, err
	}
	if prepared == nil {
		// prepare states every steering refusal as an error, so this guard
		// only keeps a nil dispatch out of the provider call.
		m.unlockAndPublish(c)
		return snapshot, ErrSteeringState
	}
	if !prepared.Steering {
		return m.finishPreparedBypassLocked(c, ctx, agentID, *prepared, snapshot, false)
	}
	c.steerPolicy = policy
	var echo *pendingEchoSteer
	if policy.RowAtEcho {
		echo = &pendingEchoSteer{inputID: inputID}
		c.echoSteer = echo
	}
	m.enqueueSnapshotLocked(c, snapshot)
	m.unlockAndPublish(c)

	result, steerErr := m.dispatcher.Steer(prepared.Item)
	c.mu.Lock()
	finished, outcome, postDrain, retErr := m.finishSteerLocked(c, ctx, *prepared, result, steerErr, echo, policy)
	if retErr == nil && outcome == steerTurnEnded && !finished.ActiveTurn && finished.ActiveBackgroundWork {
		bypassPrepared, bypassSnap, bypassErr := m.store.prepareBypass(ctx, agentID, inputID, dispatchBypassSteer)
		if bypassErr == nil {
			return m.finishPreparedBypassLocked(c, ctx, agentID, *bypassPrepared, bypassSnap, false)
		}
		if !errors.Is(bypassErr, ErrSteeringState) {
			m.unlockAndPublish(c)
			return Snapshot{}, bypassErr
		}
	}
	m.unlockAndPublish(c)
	if retErr == nil || errors.Is(retErr, ErrSteeringUnsupported) {
		m.runSteerPostDrain(agentID, finished, postDrain)
	}
	return finished, retErr
}

type steerOutcome uint8

const (
	steerAccepted steerOutcome = iota
	steerTurnEnded
	steerFailed
)

type steerPostDrain uint8

const (
	steerDrainNone steerPostDrain = iota
	steerDrainIfIdle
	steerDrainAlways
)

// finishSteer records the result for manual and automatic steering. echo and
// policy are the in-flight steer's echo tracker and the provider policy that
// Steer read, or nil and the zero value when the provider writes its row at
// once.
func (m *Manager) finishSteer(c *coordinator, ctx context.Context, prepared PreparedDispatch, result DispatchResult, err error, echo *pendingEchoSteer, policy agent.SteerPolicy) (Snapshot, error) {
	c.mu.Lock()
	snapshot, _, postDrain, retErr := m.finishSteerLocked(c, ctx, prepared, result, err, echo, policy)
	m.unlockAndPublish(c)
	if retErr == nil || errors.Is(retErr, ErrSteeringUnsupported) {
		m.runSteerPostDrain(prepared.Item.AgentID, snapshot, postDrain)
	}
	return snapshot, retErr
}

func (m *Manager) runSteerPostDrain(agentID string, snapshot Snapshot, postDrain steerPostDrain) {
	switch postDrain {
	case steerDrainNone:
	case steerDrainAlways:
		m.drainIfReleased(agentID, snapshot)
	case steerDrainIfIdle:
		if !snapshot.ActiveTurn {
			m.drainIfReleased(agentID, snapshot)
		}
	}
}

// finishSteerLocked stores the steer result under c.mu without releasing it,
// so Advance can decide whether to resume in the same lock hold.
func (m *Manager) finishSteerLocked(c *coordinator, ctx context.Context, prepared PreparedDispatch, result DispatchResult, err error, echo *pendingEchoSteer, policy agent.SteerPolicy) (Snapshot, steerOutcome, steerPostDrain, error) {
	agentID, inputID := prepared.Item.AgentID, prepared.Item.ID
	if c.echoSteer == echo {
		c.echoSteer = nil
	}
	if echo != nil && (echo.outcome == agent.SteerOutcomeEchoed || echo.outcome == agent.SteerOutcomeUncertain) {
		if err != nil {
			m.enqueueLogLocked(c, slog.LevelWarn, "provider reported an error after the steer outcome", "agent_id", agentID, "error", err)
		}
		snapshot, snapshotErr := m.store.Snapshot(ctx, agentID)
		outcome := steerAccepted
		if echo.outcome == agent.SteerOutcomeUncertain {
			outcome = steerFailed
		}
		return snapshot, outcome, steerDrainNone, snapshotErr
	}
	if echo != nil && echo.outcome == agent.SteerOutcomeDropped {
		err = ErrTurnEnded
	}
	if errors.Is(err, ErrSteeringUnsupported) {
		snapshot, storeErr := m.store.RequeuePrepared(ctx, agentID, inputID)
		if storeErr != nil {
			return Snapshot{}, steerFailed, steerDrainNone, storeErr
		}
		m.enqueueSnapshotLocked(c, snapshot)
		return snapshot, steerFailed, steerDrainIfIdle, ErrSteeringUnsupported
	}
	turnEnded := errors.Is(err, ErrTurnEnded)
	if err != nil && !turnEnded {
		snapshot, storeErr := m.recordDispatchFailure(c, ctx, prepared, err)
		if storeErr != nil {
			return snapshot, steerFailed, steerDrainNone, storeErr
		}
		return snapshot, steerFailed, steerDrainIfIdle, nil
	}
	rowAtEchoSteer := policy.RowAtEcho && !controlReplyKind(prepared.Item.Kind)
	hasNativeReceipt := result.NativeReceipt.SessionID != "" || result.NativeReceipt.InputID != "" || result.NativeReceipt.TurnID != ""
	if !turnEnded && rowAtEchoSteer && hasNativeReceipt {
		snapshot, storeErr := m.recordDispatchFailure(c, ctx, prepared, &DeliveryError{
			Outcome: DispatchUncertain,
			Err:     fmt.Errorf("RowAtEcho steer returned a synchronous native input receipt"),
		})
		if storeErr != nil {
			return snapshot, steerFailed, steerDrainNone, storeErr
		}
		return snapshot, steerFailed, steerDrainIfIdle, nil
	}
	// For non-RowAtEcho providers, a native receipt confirms acceptance even if
	// the target turn ended while dispatch waited. Sending that input again
	// would duplicate it.
	confirmedAdmission := !rowAtEchoSteer && result.NativeReceipt.SessionID != "" && result.NativeReceipt.InputID != ""
	if !turnEnded && !policy.SurvivesTurnEnd && !confirmedAdmission {
		current, storeErr := m.store.Snapshot(ctx, agentID)
		if storeErr != nil {
			return Snapshot{}, steerFailed, steerDrainNone, storeErr
		}
		turnEnded = !current.ActiveTurn
	}
	if turnEnded {
		snapshot, storeErr := m.store.RequeuePrepared(ctx, agentID, inputID)
		if storeErr != nil {
			return Snapshot{}, steerFailed, steerDrainNone, storeErr
		}
		m.enqueueSnapshotLocked(c, snapshot)
		return snapshot, steerTurnEnded, steerDrainIfIdle, nil
	}
	result.StartsTurn = true
	result.Steering = true
	var snapshot Snapshot
	var persisted bool
	// A control reply's row is LeapMux's own record, accepted at the steer:
	// a provider may repeat a steered answer at its own echo long after the
	// model call that holds it, and the answer's row must not wait for that.
	if rowAtEchoSteer {
		snapshot, persisted, err = m.holdForEcho(c, ctx, prepared)
	} else {
		snapshot, persisted, err = m.acceptDispatch(c, ctx, prepared, result)
	}
	if err != nil {
		return Snapshot{}, steerFailed, steerDrainNone, err
	}
	outcome := steerAccepted
	if !persisted {
		outcome = steerFailed
	}
	return snapshot, outcome, steerDrainAlways, nil
}

// AcceptEchoedSteers commits the oldest echoed inputs and publishes their rows in order.
func (m *Manager) AcceptEchoedSteers(ctx context.Context, agentID string, count int, spanLines string) (Snapshot, error) {
	if count <= 0 {
		return m.Snapshot(ctx, agentID)
	}
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	inFlightID := ""
	if c.echoSteer != nil && c.echoSteer.outcome == agent.SteerOutcomePending {
		inFlightID = c.echoSteer.inputID
	}
	rows, inFlight, snapshot, err := m.store.AcceptEchoedSteers(ctx, agentID, count, spanLines, inFlightID)
	if err == nil {
		if inFlight {
			c.echoSteer.outcome = agent.SteerOutcomeEchoed
		}
		if len(rows) > 0 {
			for index := range rows {
				rows[index].Content = slices.Clone(rows[index].Content)
			}
			m.enqueuePublicationLocked(c, queuePublication{snapshot: cloneQueueSnapshot(snapshot), accepted: rows})
		}
		if len(rows) < count {
			m.enqueueLogLocked(c, slog.LevelWarn, "steer echo count exceeds pending inputs", "agent_id", agentID, "reported", count, "accepted", len(rows))
		}
	}
	m.unlockAndPublish(c)
	if err == nil {
		m.drainIfReleased(agentID, snapshot)
	}
	return snapshot, err
}

// RequeueDroppedSteers returns dropped inputs to the queue without a transcript row.
func (m *Manager) RequeueDroppedSteers(ctx context.Context, agentID string, count int) (Snapshot, error) {
	if count <= 0 {
		return m.Snapshot(ctx, agentID)
	}
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	snapshot, requeued, err := m.store.RequeueUnechoedSteers(ctx, agentID, count)
	if err == nil {
		if requeued > 0 {
			m.enqueueSnapshotLocked(c, snapshot)
		}
		// A drop can precede the provider reply. Keep it until Steer can requeue that dispatch.
		if requeued < count && c.echoSteer != nil && c.echoSteer.outcome == agent.SteerOutcomePending {
			c.echoSteer.outcome = agent.SteerOutcomeDropped
			requeued++
		}
		if requeued < count {
			m.enqueueLogLocked(c, slog.LevelWarn, "steer drop count exceeds pending inputs", "agent_id", agentID, "reported", count, "requeued", requeued)
		}
	}
	m.unlockAndPublish(c)
	if err == nil {
		m.drainIfReleased(agentID, snapshot)
	}
	return snapshot, err
}

// ResolveUnechoedSteers pauses unproved deliveries. A negative count selects all pending inputs at process exit.
func (m *Manager) ResolveUnechoedSteers(ctx context.Context, agentID string, count int) (Snapshot, error) {
	if count == 0 {
		return m.Snapshot(ctx, agentID)
	}
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	inFlightID := ""
	if c.echoSteer != nil && c.echoSteer.outcome == agent.SteerOutcomePending {
		inFlightID = c.echoSteer.inputID
	}
	snapshot, resolved, inFlight, err := m.store.ResolveUnechoedSteers(ctx, agentID, count, inFlightID)
	if err == nil {
		if inFlight {
			c.echoSteer.outcome = agent.SteerOutcomeUncertain
		}
		if resolved > 0 {
			c.pauseEvents++
			m.enqueueSnapshotLocked(c, snapshot)
		}
		if count > resolved {
			m.enqueueLogLocked(c, slog.LevelWarn, "uncertain steer count exceeds pending inputs", "agent_id", agentID, "reported", count, "resolved", resolved)
		}
	}
	m.unlockAndPublish(c)
	return snapshot, err
}

// holdForEcho keeps the accepted input or records uncertain delivery if that write fails.
// The Boolean reports whether the input stayed held for echo (false on FailDispatch fallback).
// The caller holds c.mu.
func (m *Manager) holdForEcho(c *coordinator, ctx context.Context, prepared PreparedDispatch) (Snapshot, bool, error) {
	snapshot, err := m.store.HoldForEcho(ctx, prepared)
	if err != nil {
		uncertain := fmt.Errorf("provider accepted the steer but echo tracking failed: %w", err)
		var failErr error
		snapshot, failErr = m.store.FailDispatch(ctx, prepared.Item.AgentID, prepared.Item.ID, uncertain, true)
		if failErr != nil {
			return Snapshot{}, false, errors.Join(err, failErr)
		}
		c.pauseEvents++
		m.enqueueSnapshotLocked(c, snapshot)
		return snapshot, false, nil
	}
	m.enqueueSnapshotLocked(c, snapshot)
	return snapshot, true, nil
}

// Preempt cancels the agent's active turn so the specified queued item is sent as
// the next ordinary dispatch.
//
// Preempt is the steer of a provider that cannot inject into a running turn
// (Cursor): it INTERRUPTS the turn instead, and the message follows the
// moment the cancelled turn reports its end. Nothing is delivered here -- the
// item stays queued, and the drain that the provider's turn-end publish
// triggers is what sends it. That drain is also why the item is only
// validated, never reserved: a reserved head is invisible to PrepareDispatch,
// and a cancel whose turn-end lands before a requeue would strand it.
func (m *Manager) Preempt(ctx context.Context, agentID, inputID string) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	if m.dispatcher == nil || !m.dispatcher.SupportsPreemption(agentID) {
		return Snapshot{}, ErrPreemptionUnsupported
	}
	c := m.coordinator(agentID)
	c.mu.Lock()
	if c.retired {
		c.mu.Unlock()
		return Snapshot{}, ErrConflict
	}
	c.dispatches.Add()
	defer c.dispatches.Done()
	return m.preemptWithBypassLocked(c, ctx, agentID, inputID, false)
}

// preemptWithBypassLocked atomically validates preemption and either dispatches
// inputID directly (when idle with active background work) or records the
// preemption bypass and interrupts the active turn. The caller must hold c.mu
// on entry; c.mu is released on every return path.
func (m *Manager) preemptWithBypassLocked(
	c *coordinator,
	ctx context.Context,
	agentID, inputID string,
	alwaysDrain bool,
) (Snapshot, error) {
	prepared, snapshot, err := m.store.PreparePreempt(ctx, agentID, inputID)
	if err != nil {
		m.unlockAndPublish(c)
		return snapshot, err
	}
	return m.finishPreparedPreemptLocked(c, ctx, agentID, inputID, prepared, snapshot, alwaysDrain)
}

func (m *Manager) finishPreparedPreemptLocked(
	c *coordinator,
	ctx context.Context,
	agentID, inputID string,
	prepared *PreparedDispatch,
	snapshot Snapshot,
	alwaysDrain bool,
) (Snapshot, error) {
	if prepared != nil {
		return m.finishPreparedBypassLocked(c, ctx, agentID, *prepared, snapshot, alwaysDrain)
	}
	m.enqueueSnapshotLocked(c, snapshot)
	m.unlockAndPublish(c)
	return m.finishPreemptInterrupt(ctx, agentID, c, snapshot, alwaysDrain, inputID)
}

func (m *Manager) finishPreemptInterrupt(ctx context.Context, agentID string, c *coordinator, fallback Snapshot, alwaysDrain bool, bypassInputID string) (Snapshot, error) {
	interruptErr := m.dispatcher.Interrupt(agentID)

	// Whatever the cancel did, the queue's own drain owns the delivery: a
	// cancel that already ended the turn is caught by the drain below, and one
	// still settling is reported by the provider's turn-end publish. The
	// answer must reflect the world AFTER the interrupt, not before it.
	c.mu.Lock()
	if interruptErr != nil && bypassInputID != "" {
		cleared, clearErr := m.store.ClearPreemptBypass(ctx, agentID, bypassInputID)
		if clearErr == nil {
			m.enqueueSnapshotLocked(c, cleared)
			m.unlockAndPublish(c)
			if alwaysDrain {
				m.scheduleDrain(agentID)
			} else {
				m.drainIfReleased(agentID, cleared)
			}
			return cleared, interruptErr
		}
		m.enqueueFailureLocked(c, "agent input queue clear preempt bypass failed", "agent_id", agentID, "input_id", bypassInputID, "error", clearErr)
	}
	current, snapshotErr := m.store.Snapshot(ctx, agentID)
	m.unlockAndPublish(c)
	if snapshotErr != nil {
		return fallback, interruptErr
	}
	if alwaysDrain {
		m.scheduleDrain(agentID)
	} else {
		m.drainIfReleased(agentID, current)
	}
	return current, interruptErr
}

func hasDispatchingItem(snapshot Snapshot) bool {
	for i := range snapshot.Items {
		if snapshot.Items[i].State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING {
			return true
		}
	}
	return false
}

func headCanSteer(snapshot Snapshot, head int, supportsSteer, recoveryRequired bool) bool {
	if !supportsSteer || head < 0 || !snapshot.Items[head].CanSteer {
		return false
	}
	return !recoveryRequired || snapshot.Items[head].Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER
}

func headCanPreempt(snapshot Snapshot, head int, supportsPreempt, recoveryRequired bool) bool {
	if !supportsPreempt || head < 0 || !snapshot.Items[head].CanPreempt {
		return false
	}
	return !recoveryRequired || snapshot.Items[head].Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER
}

// Advance performs one keyboard advance step against the agent's input queue.
//
// The order follows what the store admits:
//   - Steer first, then resume a paused queue under the same coordinator lock
//     when the steer succeeded or cleanly requeued on turn end and no crash,
//     pause, failure, or login-recovery proposal landed while it ran.
//   - Preempt directly when the queue is running and the head can preempt.
//   - Return the current snapshot without resuming when login recovery blocks
//     the queue, so the client can open the approval dialog.
//   - Resume a paused queue and atomically check whether the original head
//     became preemptable before scheduling a drain, removing the round-trip
//     window between resume and preempt.
//
// hasRecovery reads whether the agent currently carries a login-recovery
// proposal. Advance calls it outside c.mu so lock ordering stays acyclic.
func (m *Manager) Advance(ctx context.Context, agentID string, hasRecovery func(string) bool) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()

	c := m.coordinator(agentID)
	c.mu.Lock()
	if c.retired || c.advancing {
		snapshot, err := m.store.Snapshot(ctx, agentID)
		c.mu.Unlock()
		return snapshot, err
	}
	c.advancing = true
	c.dispatches.Add()
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.advancing = false
		c.mu.Unlock()
		c.dispatches.Done()
	}()

	return m.advanceClaimed(ctx, agentID, c, hasRecovery)
}

type advancePauseState struct {
	paused          bool
	reason          leapmuxv1.AgentInputQueuePauseReason
	pauseEvents     uint64
	recoveryAtStart bool
}

// resumeAfterAdvanceLocked resumes a paused queue under c.mu when an Advance
// steer or background-work bypass succeeded without any intervening pause,
// restart, retirement, or login-recovery proposal.
func (m *Manager) resumeAfterAdvanceLocked(
	c *coordinator,
	ctx context.Context,
	agentID string,
	before advancePauseState,
	finished Snapshot,
	eligible bool,
	recoveryAfter bool,
) (Snapshot, bool, error) {
	shouldResume := before.paused &&
		!before.recoveryAtStart &&
		!recoveryAfter &&
		eligible &&
		!c.retired &&
		c.plannedRestarts == 0 &&
		c.pauseEvents == before.pauseEvents &&
		finished.Paused &&
		finished.PauseReason == before.reason
	if !shouldResume {
		return finished, false, nil
	}
	resumed, err := m.store.resumeWithNoRestart(ctx, agentID)
	if err != nil {
		return finished, false, err
	}
	m.enqueueSnapshotLocked(c, resumed)
	return resumed, true, nil
}

func (m *Manager) advanceClaimed(ctx context.Context, agentID string, c *coordinator, hasRecovery func(string) bool) (Snapshot, error) {
	supportsSteer := m.dispatcher != nil && m.dispatcher.SupportsSteering(agentID)
	supportsPreempt := !supportsSteer && m.dispatcher != nil && m.dispatcher.SupportsPreemption(agentID)
	recoveryAtStart := hasRecovery != nil && hasRecovery(agentID)
	var steerPolicy agent.SteerPolicy
	if supportsSteer {
		var err error
		steerPolicy, err = m.dispatcher.SteerPolicy(agentID)
		if err != nil {
			return Snapshot{}, err
		}
		c.mu.Lock()
		skipPeek := c.retired || c.plannedRestarts > 0
		var peek Snapshot
		if !skipPeek {
			peek, err = m.store.Snapshot(ctx, agentID)
		}
		c.mu.Unlock()
		if err != nil {
			return Snapshot{}, err
		}
		if !skipPeek && !hasDispatchingItem(peek) && peek.ActiveTurn && headCanSteer(peek, peek.HeadIndex(), supportsSteer, recoveryAtStart) {
			m.dispatcher.PrepareSteer(agentID)
			recoveryAtStart = hasRecovery != nil && hasRecovery(agentID)
		}
	}

	c.mu.Lock()
	snapshot, err := m.store.Snapshot(ctx, agentID)
	if err != nil {
		c.mu.Unlock()
		return Snapshot{}, err
	}
	m.rearmMaturityLocked(c, snapshot)
	if c.retired || c.plannedRestarts > 0 || hasDispatchingItem(snapshot) {
		c.mu.Unlock()
		return snapshot, nil
	}

	head := snapshot.HeadIndex()
	if headCanSteer(snapshot, head, supportsSteer, recoveryAtStart) {
		headID := snapshot.Items[head].ID
		pauseBefore := advancePauseState{
			paused:          snapshot.Paused,
			reason:          snapshot.PauseReason,
			pauseEvents:     c.pauseEvents,
			recoveryAtStart: recoveryAtStart,
		}
		prepared, prepSnapshot, prepErr := m.store.PrepareSteer(ctx, agentID, headID, steerPolicy.RowAtEcho)
		if prepErr != nil {
			m.unlockAndPublish(c)
			return Snapshot{}, prepErr
		}
		if prepared == nil {
			m.unlockAndPublish(c)
			return prepSnapshot, ErrSteeringState
		}
		if !prepared.Steering {
			finished, accepted, recoveryAfter, retErr := m.dispatchPreparedBypassLocked(c, ctx, agentID, *prepared, prepSnapshot, hasRecovery)
			var resumed bool
			if retErr == nil {
				finished, resumed, retErr = m.resumeAfterAdvanceLocked(c, ctx, agentID, pauseBefore, finished, accepted, recoveryAfter)
			}
			m.unlockAndPublish(c)
			if resumed && retErr == nil {
				m.scheduleDrain(agentID)
			} else if retErr == nil {
				m.drainIfReleased(agentID, finished)
			}
			return finished, retErr
		}
		c.steerPolicy = steerPolicy
		var echo *pendingEchoSteer
		if steerPolicy.RowAtEcho {
			echo = &pendingEchoSteer{inputID: headID}
			c.echoSteer = echo
		}
		m.enqueueSnapshotLocked(c, prepSnapshot)
		m.unlockAndPublish(c)

		result, steerErr := m.dispatcher.Steer(prepared.Item)
		recoveryAfterSteer := hasRecovery != nil && hasRecovery(agentID)

		c.mu.Lock()
		finished, outcome, postDrain, retErr := m.finishSteerLocked(c, ctx, *prepared, result, steerErr, echo, steerPolicy)
		if retErr == nil && outcome == steerTurnEnded && !finished.ActiveTurn && finished.ActiveBackgroundWork {
			bypassPrepared, bypassSnap, bypassErr := m.store.prepareBypass(ctx, agentID, headID, dispatchBypassSteer)
			switch {
			case bypassErr == nil:
				var accepted, recoveryAfterBypass bool
				finished, accepted, recoveryAfterBypass, retErr = m.dispatchPreparedBypassLocked(c, ctx, agentID, *bypassPrepared, bypassSnap, hasRecovery)
				recoveryAfterSteer = recoveryAfterSteer || recoveryAfterBypass
				if accepted {
					outcome = steerAccepted
					postDrain = steerDrainAlways
				} else {
					outcome = steerFailed
					postDrain = steerDrainIfIdle
				}
			case errors.Is(bypassErr, ErrSteeringState):
				// Background work ended before bypass preparation; fall through
				// to resumeAfterAdvanceLocked and runSteerPostDrain.
			default:
				retErr = bypassErr
			}
		}
		var resumed bool
		if retErr == nil {
			eligible := outcome == steerAccepted || outcome == steerTurnEnded
			finished, resumed, retErr = m.resumeAfterAdvanceLocked(c, ctx, agentID, pauseBefore, finished, eligible, recoveryAfterSteer)
		}
		m.unlockAndPublish(c)
		if resumed && retErr == nil {
			m.scheduleDrain(agentID)
		} else if retErr == nil || errors.Is(retErr, ErrSteeringUnsupported) {
			m.runSteerPostDrain(agentID, finished, postDrain)
		}
		return finished, retErr
	}

	if headCanPreempt(snapshot, head, supportsPreempt, recoveryAtStart) {
		headID := snapshot.Items[head].ID
		return m.preemptWithBypassLocked(c, ctx, agentID, headID, false)
	}

	if recoveryAtStart || !snapshot.Paused {
		c.mu.Unlock()
		return snapshot, nil
	}

	headAtKeypress := ""
	if head >= 0 {
		headAtKeypress = snapshot.Items[head].ID
	}
	resumed, err := m.store.resumeWithNoRestart(ctx, agentID)
	if err != nil {
		m.unlockAndPublish(c)
		return Snapshot{}, err
	}
	m.enqueueSnapshotLocked(c, resumed)
	shouldPreempt := false
	if supportsPreempt && headAtKeypress != "" {
		if nextHead := resumed.HeadIndex(); nextHead >= 0 &&
			resumed.Items[nextHead].ID == headAtKeypress &&
			resumed.Items[nextHead].CanPreempt {
			shouldPreempt = true
		}
	}
	if !shouldPreempt {
		m.unlockAndPublish(c)
		m.scheduleDrain(agentID)
		return resumed, nil
	}
	prepared, prepSnap, prepErr := m.store.PreparePreempt(ctx, agentID, headAtKeypress)
	if prepErr != nil {
		m.unlockAndPublish(c)
		m.scheduleDrain(agentID)
		if errors.Is(prepErr, ErrPreemptionState) || errors.Is(prepErr, ErrNotHead) {
			return resumed, nil
		}
		return resumed, prepErr
	}
	return m.finishPreparedPreemptLocked(c, ctx, agentID, headAtKeypress, prepared, prepSnap, true)
}

// BeginPlannedRestart pauses automatic dispatch before a provider replacement.
// The pause carries pauseOwnerPlannedRestart, so Retry and Steer refuse to
// write into the stopping process and Finish resumes only its own pause.
//
// This guard holds NO lock across the restart. It held one before, and that
// deadlocked the Worker whenever the old process crashed during the stop: the
// exit goroutine calls Pause, Pause took the guard's lock, and the stop waits
// for that same exit goroutine.
func (m *Manager) BeginPlannedRestart(ctx context.Context, agentID string) (*PlannedRestart, error) {
	return m.beginPlannedRestart(ctx, agentID, false)
}

// BeginInputRecovery keeps recovery's pause authority until the replacement
// prepares. A crash or a user pause takes that authority before finalization.
func (m *Manager) BeginInputRecovery(ctx context.Context, agentID string) (*PlannedRestart, error) {
	return m.beginPlannedRestart(ctx, agentID, true)
}

func (m *Manager) beginPlannedRestart(ctx context.Context, agentID string, inputRecovery bool) (*PlannedRestart, error) {
	if !m.beginActivity() {
		return nil, ErrManagerStopped
	}
	c := m.coordinator(agentID)
	c.mu.Lock()
	if c.plannedRestarts != 0 {
		c.mu.Unlock()
		m.endActivity()
		return nil, ErrPlannedRestart
	}
	var snapshot Snapshot
	var resumeQueue bool
	var recovery *inputRecoveryPause
	var err error
	if inputRecovery {
		var previous inputRecoveryPause
		snapshot, previous, err = m.store.pauseForInputRecovery(ctx, agentID)
		recovery = &previous
	} else {
		snapshot, resumeQueue, err = m.store.pauseForPlannedRestart(ctx, agentID)
	}
	if err == nil {
		c.plannedRestarts++
		c.pauseEvents++
		if resumeQueue || inputRecovery {
			m.enqueueSnapshotLocked(c, snapshot)
		}
	}
	m.unlockAndPublish(c)
	if err != nil {
		m.endActivity()
		return nil, err
	}
	return &PlannedRestart{
		manager: m, coordinator: c, agentID: agentID, resumeQueue: resumeQueue, recovery: recovery,
	}, nil
}

// Finish clears an old turn that cannot send another end signal. A process
// replacement or an already observed native turn end makes oldTurnEnded true.
// A successful replacement restores only this guard's temporary pause.
func (r *PlannedRestart) Finish(ctx context.Context, oldTurnEnded, restartSucceeded bool) error {
	return r.finish(func() (Snapshot, bool, error) {
		resumeQueue := r.resumeQueue && (!oldTurnEnded || restartSucceeded)
		return r.manager.store.finishPlannedRestart(ctx, r.agentID, oldTurnEnded, resumeQueue)
	})
}

// FinishInputRecovery preserves current turn state and releases only the
// recovery's own pause. An unspecified failure reason means success.
func (r *PlannedRestart) FinishInputRecovery(ctx context.Context, oldProcessEnded bool, failureReason leapmuxv1.AgentInputQueuePauseReason) error {
	if r == nil {
		return nil
	}
	if r.recovery == nil {
		return fmt.Errorf("%w: this restart does not own input recovery", ErrConflict)
	}
	return r.finish(func() (Snapshot, bool, error) {
		snapshot, changed, err := r.manager.store.finishInputRecovery(ctx, r.agentID, *r.recovery, oldProcessEnded, failureReason)
		if err == nil && failureReason != leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED {
			r.coordinator.pauseEvents++
		}
		return snapshot, changed, err
	})
}

func (r *PlannedRestart) finish(mutate func() (Snapshot, bool, error)) error {
	if r == nil {
		return nil
	}
	var snapshot Snapshot
	var shouldDrain, finished bool
	r.once.Do(func() {
		r.coordinator.mu.Lock()
		r.coordinator.plannedRestarts--
		// A failed transaction can leave the durable marker behind. The guard
		// still ended; explicit resume or a new recovery can repair that marker.
		var changed bool
		var err error
		snapshot, changed, err = mutate()
		if err == nil && changed {
			r.manager.enqueueSnapshotLocked(r.coordinator, snapshot)
		}
		shouldDrain = err == nil && changed
		r.coordinator.mu.Unlock()
		r.err = err
		finished = true
	})
	if finished {
		defer r.manager.endActivity()
	}
	r.manager.drainPublications(r.coordinator)
	if shouldDrain {
		r.manager.drainIfReleased(r.agentID, snapshot)
	}
	return r.err
}

func (m *Manager) Recover(ctx context.Context) error {
	if err := m.RecoverState(ctx, nil); err != nil {
		return err
	}
	return m.DrainRecovered(ctx)
}

// RetireAgent ends the queue's work for an agent that closed. It cancels the
// agent's pending automatic retries, stops its maturity timer, and refuses
// every later arm and drain, so a scheduled item of a closed agent never
// matures into a dispatch that fails and pauses the queue.
//
// Call it after closed_at is stamped. The scheduler refuses a closed agent
// from that commit on, so no retry can appear after this cancellation, and
// one that a late provider report created before it is removed here.
//
// Retirement is permanent. A closed agent ID never takes input again,
// because reopening a tab creates a new agent with a copy of the transcript.
func (m *Manager) RetireAgent(ctx context.Context, agentID string) error {
	if !m.beginActivity() {
		return ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	c.retired = true
	c.pauseEvents++
	stopMaturityLocked(c)
	snapshot, changed, err := m.store.cancelAutoRetries(ctx, agentID, autoRetryKinds...)
	m.store.ClearEphemeralState(agentID)
	if err == nil && changed {
		m.enqueueSnapshotLocked(c, snapshot)
	}
	m.unlockAndPublish(c)
	return err
}

// WaitForRetiredDispatch joins input dispatch admitted before RetireAgent.
// It includes effects that publish after the coordinator releases its lock.
func (m *Manager) WaitForRetiredDispatch(ctx context.Context, agentID string) error {
	c := m.coordinator(agentID)
	select {
	case <-c.dispatches.DoneChan():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RecoverState reconciles interrupted persisted state without dispatching
// queued input. Bootstrap uses it before the Worker receives its owner and
// supplies beforeRecover to sweep active background tasks inside the same
// SQLite write transaction.
func (m *Manager) RecoverState(ctx context.Context, beforeRecover func(context.Context, *sql.Tx) error) error {
	if !m.beginActivity() {
		return ErrManagerStopped
	}
	defer m.endActivity()
	snapshots, err := m.store.Recover(ctx, beforeRecover)
	if err != nil {
		return err
	}
	// Install every hold before any timer or observer can start a drain.
	// Release lifecycleMu before taking a coordinator lock.
	m.lifecycleMu.Lock()
	if !m.stopped {
		for _, snapshot := range snapshots {
			if !snapshot.Paused {
				m.recovered[snapshot.AgentID] = struct{}{}
			}
		}
	}
	m.lifecycleMu.Unlock()
	for i := range snapshots {
		c := m.coordinator(snapshots[i].AgentID)
		c.mu.Lock()
		m.enqueueSnapshotLocked(c, snapshots[i])
		m.unlockAndPublish(c)
	}
	return nil
}

// DrainRecovered starts each recovered, unpaused queue once bootstrap confirms
// that the Hub still owns its agent tab and supplies the Worker owner.
func (m *Manager) DrainRecovered(ctx context.Context) error {
	if !m.beginActivity() {
		return ErrManagerStopped
	}
	defer m.endActivity()
	m.lifecycleMu.Lock()
	agentIDs := make([]string, 0, len(m.recovered))
	for agentID := range m.recovered {
		agentIDs = append(agentIDs, agentID)
	}
	m.lifecycleMu.Unlock()
	var drainErr error
	for _, agentID := range agentIDs {
		// The hold ends here on every path. AgentResumer.Start calls this once,
		// so an agent left in the set after a failed read would refuse every
		// drain for the life of the process.
		m.lifecycleMu.Lock()
		delete(m.recovered, agentID)
		m.lifecycleMu.Unlock()
		c := m.coordinator(agentID)
		c.mu.Lock()
		snapshot, err := m.store.Snapshot(ctx, agentID)
		if err == nil {
			m.rearmMaturityLocked(c, snapshot)
		}
		c.mu.Unlock()
		if err != nil {
			drainErr = errors.Join(drainErr, fmt.Errorf("read recovered queue %s: %w", agentID, err))
			continue
		}
		if !snapshot.Paused && len(snapshot.Items) > 0 {
			m.scheduleDrain(agentID)
		}
	}
	return drainErr
}

// NotifyDependencyReady checks queued input after an external dependency becomes ready.
// Normal pause and delivery checks still apply.
func (m *Manager) NotifyDependencyReady(agentID string) {
	m.scheduleDrain(agentID)
}

// NotifyInputReady checks the original source before scheduling a queue pass.
// It changes no native turn or provisional input reservation.
func (m *Manager) NotifyInputReady(agentID string, admission agent.InputReadinessAdmission) {
	if admission != nil {
		m.scheduleDrainFrom(agentID, admission)
	}
}

// scheduleDrain starts the agent's drain loop, or records a request for the
// loop that already runs. It is the one admission point of every automatic
// dispatch, so it holds the two refusals that no source of drains may bypass:
// a recovered queue waits for DrainRecovered, which confirms the Worker owner
// and the Hub's tab, and a retired (closed) agent takes no input at all.
func (m *Manager) scheduleDrain(agentID string) {
	m.scheduleDrainFrom(agentID, nil)
}

func (m *Manager) scheduleDrainFrom(agentID string, admission agent.InputReadinessAdmission) {
	if !m.beginDrainActivity(agentID) {
		return
	}
	var lease agent.InputReadinessLease
	if admission != nil {
		lease = admission.Acquire()
		if lease == nil {
			m.endActivity()
			return
		}
		defer func() {
			if lease != nil {
				lease.Release()
			}
		}()
	}
	c := m.coordinator(agentID)
	c.mu.Lock()
	if c.retired {
		c.mu.Unlock()
		m.endActivity()
		return
	}
	if c.draining {
		c.drainAgain = true
		c.mu.Unlock()
		m.endActivity()
		return
	}
	c.draining = true
	c.dispatches.Add()
	c.mu.Unlock()
	if lease != nil {
		lease.Release()
		lease = nil
	}
	go func() {
		defer m.endActivity()
		defer c.dispatches.Done()
		m.drain(agentID, c)
	}()
}

// drain delivers queued input until the queue empties or a turn starts.
//
// The coordinator lock covers the store calls only. It is NEVER held across
// dispatcher.Dispatch, and that rule is load-bearing three times over. Dispatch
// stops and relaunches the agent process for a clear or a plan execution, and
// the exit goroutine that a stop wakes calls Pause, which takes this same lock:
// holding it deadlocks the Worker. Dispatch also waits for the provider to
// acknowledge the turn, and the provider's reader goroutine reports that turn
// through TurnStarted, which takes this lock too. And every queue RPC, down to
// a plain list, waits behind it.
//
// Releasing the lock is safe because the store already reserved the item:
// PrepareDispatch commits state = DISPATCHING with active_turn = 1, and Accept
// re-checks both before it writes the transcript, so no concurrent caller can
// take or deliver the same item twice.
func (m *Manager) drain(agentID string, c *coordinator) {
	for {
		c.mu.Lock()
		if m.isStopped() {
			c.draining = false
			c.drainAgain = false
			m.unlockAndPublish(c)
			return
		}
		// The read below answers every drain request made so far, so a request
		// recorded before it is spent. Only one that arrives during the provider
		// call carries news that this loop still misses.
		c.drainAgain = false
		policy, policyErr := m.dispatcher.SteerPolicy(agentID)
		if policyErr != nil {
			m.enqueueFailureLocked(c, "read the agent input queue steering policy", "agent_id", agentID, "error", policyErr)
			c.draining = false
			m.unlockAndPublish(c)
			return
		}
		// The steer-row policy decides the reservation: a question answer that
		// the active turn steers holds its row for the provider's echo, exactly
		// as a manual steer does.
		prepared, snapshot, err := m.store.PrepareDispatch(context.Background(), agentID, policy.RowAtEcho)
		if err != nil {
			m.enqueueFailureLocked(c, "agent input queue prepare failed", "agent_id", agentID, "error", err)
			c.draining = false
			m.unlockAndPublish(c)
			return
		}
		if prepared == nil {
			// The refused head leaves the durable order unchanged, but the
			// read is still an arm point, because time moved since the last.
			m.rearmMaturityLocked(c, snapshot)
			c.draining = false
			m.unlockAndPublish(c)
			return
		}
		m.enqueueSnapshotLocked(c, snapshot)
		m.unlockAndPublish(c)

		if prepared.Steering {
			// Mirror Steer: the provider policy owns the row timing, and the
			// echo tracker is what a later report resolves.
			m.dispatcher.PrepareSteer(agentID)
			c.mu.Lock()
			c.steerPolicy = policy
			var echo *pendingEchoSteer
			if policy.RowAtEcho {
				echo = &pendingEchoSteer{inputID: prepared.Item.ID}
				c.echoSteer = echo
			}
			c.mu.Unlock()
			var result DispatchResult
			steerErr := ErrSteeringUnsupported
			if m.dispatcher.SupportsSteering(agentID) {
				result, steerErr = m.dispatcher.Steer(prepared.Item)
			}
			_, finishErr := m.finishSteer(c, context.Background(), *prepared, result, steerErr, echo, policy)
			c.mu.Lock()
			if finishErr != nil && !errors.Is(finishErr, ErrSteeringUnsupported) {
				m.enqueueFailureLocked(c, "agent input queue steering persistence failed", "agent_id", agentID, "error", finishErr)
			}
			if m.stopRoundAndPublish(c) {
				return
			}
			continue
		}

		result, dispatchErr := m.dispatcher.Dispatch(prepared.Item)

		c.mu.Lock()
		if dispatchErr != nil {
			snapshot, storeErr := m.recordDispatchFailure(c, context.Background(), *prepared, dispatchErr)
			if storeErr != nil {
				m.enqueueFailureLocked(c, "agent input queue failure persistence failed", "agent_id", agentID, "input_id", prepared.Item.ID, "error", storeErr)
			} else if canSteerAutomatically(snapshot) {
				// A busy ordinary dispatch can reveal a turn that accepts this answer.
				c.drainAgain = true
			}
			if m.stopRoundAndPublish(c) {
				return
			}
			continue
		}
		snapshot, persisted, err := m.acceptDispatch(c, context.Background(), *prepared, result)
		if err != nil {
			m.enqueueFailureLocked(c, "agent input queue acceptance persistence failed", "agent_id", agentID, "input_id", prepared.Item.ID, "error", err)
			if m.stopRoundAndPublish(c) {
				return
			}
			continue
		}
		// The COMMITTED state decides, not the dispatch's own report. A turn
		// that ended while the provider call ran cleared the flag, and the
		// scheduleDrain it fired found this loop still marked draining and did
		// nothing -- so reading result.StartsTurn here left the queue holding
		// the rest of its items with no turn to wait for.
		if !persisted || !canDrainAutomatically(snapshot) {
			if m.stopRoundAndPublish(c) {
				return
			}
			continue
		}
		m.unlockAndPublish(c)
	}
}

func (m *Manager) acceptDispatch(c *coordinator, ctx context.Context, prepared PreparedDispatch, result DispatchResult) (Snapshot, bool, error) {
	transcript, snapshot, err := m.store.Accept(ctx, prepared, result)
	if err == nil {
		transcript.Content = slices.Clone(transcript.Content)
		transcript.SupplementalContent = slices.Clone(transcript.SupplementalContent)
		m.enqueuePublicationLocked(c, queuePublication{snapshot: cloneQueueSnapshot(snapshot),
			accepted: []AcceptedTranscript{transcript}, afterAccept: result.AfterAccept})
		return snapshot, true, nil
	}
	uncertainErr := fmt.Errorf("provider accepted input but transcript persistence failed: %w", err)
	snapshot, failErr := m.store.FailDispatch(ctx, prepared.Item.AgentID, prepared.Item.ID, uncertainErr, true)
	if failErr != nil {
		return Snapshot{}, false, errors.Join(err, failErr)
	}
	c.pauseEvents++
	m.enqueueSnapshotLocked(c, snapshot)
	return snapshot, false, nil
}

// recordDispatchFailure stores the outcome of a dispatch that the provider
// refused. DispatchOutcome distinguishes delivery failures, busy turns, pending
// admission and unavailable processes. Each outcome has its own store operation.
func (m *Manager) recordDispatchFailure(c *coordinator, ctx context.Context, prepared PreparedDispatch, dispatchErr error) (Snapshot, error) {
	item := prepared.Item
	outcome := dispatchOutcome(dispatchErr)
	var record func() (Snapshot, error)
	var pauses bool
	switch outcome {
	case DispatchPending:
		record = func() (Snapshot, error) {
			return m.store.RequeuePending(ctx, item.AgentID, item.ID)
		}
	case DispatchBusy:
		activeTurnSteerable := false
		var deliveryErr *DeliveryError
		if errors.As(dispatchErr, &deliveryErr) {
			activeTurnSteerable = deliveryErr.ActiveTurnSteerable
		}
		record = func() (Snapshot, error) {
			return m.store.RequeueBusy(ctx, item.AgentID, item.ID, activeTurnSteerable)
		}
	case DispatchNotReady:
		pauses = true
		// The pause carries the caller's own reason, because the reader reads it.
		// A provider that is not ready yet means AGENT_STOPPED; a failed read of
		// the worker's own store does not.
		reason := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED
		var notReady *DeliveryError
		if errors.As(dispatchErr, &notReady) && notReady.PauseReason != leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED {
			reason = notReady.PauseReason
		}
		record = func() (Snapshot, error) {
			return m.store.RequeueAndPause(ctx, item.AgentID, item.ID, dispatchErr, reason)
		}
	case DispatchUncertain:
		pauses = true
		record = func() (Snapshot, error) {
			return m.store.FailDispatch(ctx, item.AgentID, item.ID, dispatchErr, true)
		}
	case DispatchFailed:
		pauses = true
		record = func() (Snapshot, error) {
			return m.store.FailDispatch(ctx, item.AgentID, item.ID, dispatchErr, false)
		}
	}
	snapshot, err := record()
	if err != nil {
		return Snapshot{}, err
	}
	if pauses {
		c.pauseEvents++
	}
	m.enqueueSnapshotLocked(c, snapshot)
	return snapshot, nil
}
