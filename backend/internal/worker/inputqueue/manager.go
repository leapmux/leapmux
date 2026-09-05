package inputqueue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type coordinator struct {
	mu       sync.Mutex
	draining bool
	// plannedRestarts counts the restarts in flight for this agent. The last
	// one to finish resumes the pause that the first one created.
	//
	// This replaces a mutex that BeginPlannedRestart held until Finish. That
	// mutex deadlocked the Worker: Finish runs only after StopAndWaitAgent
	// returns, StopAndWaitAgent waits for the exit goroutine, and a crashed
	// process makes that goroutine call Pause, which took the same mutex. The
	// durable pause_owner column now carries what the mutex protected, so no
	// lock spans a process stop.
	plannedRestarts int
}

// PlannedRestart keeps explicit dispatch operations behind one agent restart.
// Automatic drains stay blocked by the durable pause that BeginPlannedRestart
// creates.
type PlannedRestart struct {
	manager     *Manager
	coordinator *coordinator
	agentID     string
	resumeQueue bool
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
	activity    sync.WaitGroup
	stopped     bool
	recovered   map[string]struct{}
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
func (m *Manager) mutateAndDrain(agentID string, mutate func() (Snapshot, error)) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	snapshot, err := mutate()
	if err == nil {
		m.observer.QueueChanged(snapshot)
	}
	c.mu.Unlock()
	if err != nil {
		return snapshot, err
	}
	m.scheduleDrain(agentID)
	return snapshot, nil
}

func (m *Manager) beginActivity() bool {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.stopped {
		return false
	}
	m.activity.Add(1)
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

// Stop refuses new queue work and returns the function that joins active work.
// An active dispatch finishes its current item.
func (m *Manager) Stop() func() {
	m.lifecycleMu.Lock()
	m.stopped = true
	m.lifecycleMu.Unlock()
	return m.activity.Wait
}

// StopAndWait closes queue admission and then joins active work.
func (m *Manager) StopAndWait() {
	m.Stop()()
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
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot, changed, err := mutate()
	if err == nil && changed {
		m.observer.QueueChanged(snapshot)
	}
	return snapshot, err
}

func (m *Manager) Enqueue(ctx context.Context, input NewItem) (Snapshot, error) {
	// The classifier rewrites a slash command into its own kind, so the test
	// must see the kind the store will store, not the one the client sent.
	if err := m.refuseUnacceptedKind(input.AgentID, m.store.Classify(input.Kind, input.Text)); err != nil {
		return Snapshot{}, err
	}
	return m.mutateAndDrain(input.AgentID, func() (Snapshot, error) {
		return m.store.Enqueue(ctx, input)
	})
}

func (m *Manager) Snapshot(ctx context.Context, agentID string) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	defer c.mu.Unlock()
	return m.store.Snapshot(ctx, agentID)
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

func (m *Manager) Update(ctx context.Context, agentID, inputID, clientID string, expectedVersion uint64, text string, attachments []Attachment) (Snapshot, error) {
	// An edit can reclassify the item, so the new text can carry a kind this
	// agent cannot take. Store.Update refuses it below on a stale read only;
	// this refuses it before the write transaction opens.
	kind, err := m.store.KindAfterEdit(ctx, agentID, inputID, text)
	if err == nil {
		err = m.refuseUnacceptedKind(agentID, kind)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Snapshot{}, err
	}
	return m.mutateAndDrain(agentID, func() (Snapshot, error) {
		return m.store.Update(ctx, agentID, inputID, clientID, expectedVersion, text, attachments)
	})
}

func (m *Manager) CancelEdit(ctx context.Context, agentID, inputID, clientID string) (Snapshot, error) {
	return m.mutateAndDrain(agentID, func() (Snapshot, error) {
		return m.store.CancelEdit(ctx, agentID, inputID, clientID)
	})
}

func (m *Manager) Delete(ctx context.Context, agentID, inputID string) (Snapshot, error) {
	return m.mutateAndDrain(agentID, func() (Snapshot, error) {
		return m.store.Delete(ctx, agentID, inputID)
	})
}

func (m *Manager) Move(ctx context.Context, agentID, inputID, beforeInputID string) (Snapshot, error) {
	return m.mutateAndDrain(agentID, func() (Snapshot, error) {
		return m.store.Move(ctx, agentID, inputID, beforeInputID)
	})
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
	snapshot, err := m.store.SetPaused(ctx, agentID, paused, reason)
	if err == nil {
		m.observer.QueueChanged(snapshot)
	}
	c.mu.Unlock()
	if err == nil && !paused {
		m.scheduleDrain(agentID)
	}
	return snapshot, err
}

func (m *Manager) Pause(ctx context.Context, agentID string, reason leapmuxv1.AgentInputQueuePauseReason) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	return m.mutateLocked(agentID, func() (Snapshot, bool, error) {
		snapshot, err := m.store.Pause(ctx, agentID, reason)
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
	return m.mutateLocked(agentID, func() (Snapshot, bool, error) {
		return m.store.PauseForArchive(ctx, agentID)
	})
}

// ResumeAfterArchive resumes only a pause that PauseForArchive created.
func (m *Manager) ResumeAfterArchive(ctx context.Context, agentID string) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	snapshot, changed, err := m.store.ResumeAfterArchive(ctx, agentID)
	if err == nil && changed {
		m.observer.QueueChanged(snapshot)
	}
	c.mu.Unlock()
	if err == nil && changed {
		m.scheduleDrain(agentID)
	}
	return snapshot, err
}

func (m *Manager) TurnEnded(ctx context.Context, agentID string) (Snapshot, error) {
	return m.mutateAndDrain(agentID, func() (Snapshot, error) {
		return m.store.TurnEnded(ctx, agentID)
	})
}

func (m *Manager) TurnStarted(ctx context.Context, agentID string) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	return m.mutateLocked(agentID, func() (Snapshot, bool, error) {
		snapshot, err := m.store.TurnStarted(ctx, agentID)
		return snapshot, true, err
	})
}

func (m *Manager) Retry(ctx context.Context, agentID, inputID string, confirmUncertain bool) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	snapshot, err := m.store.Retry(ctx, agentID, inputID, confirmUncertain)
	if err != nil {
		c.mu.Unlock()
		return Snapshot{}, err
	}
	m.observer.QueueChanged(snapshot)
	prepared, snapshot, err := m.store.PrepareRetry(ctx, agentID)
	if err != nil || prepared == nil {
		c.mu.Unlock()
		return snapshot, err
	}
	m.observer.QueueChanged(snapshot)
	// The store reserved the item as DISPATCHING, so no other caller can take
	// it. Release the lock for the provider call: it can stop and relaunch the
	// agent process, and the exit goroutine that a stop wakes needs this same
	// lock to pause the queue.
	c.mu.Unlock()
	result, dispatchErr := m.dispatcher.Dispatch(prepared.Item)
	c.mu.Lock()
	if dispatchErr != nil {
		snapshot, storeErr := m.recordDispatchFailure(ctx, *prepared, dispatchErr)
		c.mu.Unlock()
		return snapshot, storeErr
	}
	snapshot, _, err = m.acceptDispatch(ctx, *prepared, result)
	if err != nil {
		c.mu.Unlock()
		return Snapshot{}, err
	}
	c.mu.Unlock()
	// Retry lifted the delivery pause, so the items behind the retried head
	// must dispatch too. Without this the queue stalls with no visible reason.
	m.scheduleDrain(agentID)
	return snapshot, nil
}

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
	prepared, snapshot, err := m.store.PrepareSteer(ctx, agentID, inputID)
	if err != nil {
		c.mu.Unlock()
		return Snapshot{}, err
	}
	if prepared == nil {
		c.mu.Unlock()
		return snapshot, ErrTurnEnded
	}
	m.observer.QueueChanged(snapshot)
	c.mu.Unlock()

	result, err := m.dispatcher.Steer(prepared.Item)
	c.mu.Lock()
	if errors.Is(err, ErrSteeringUnsupported) {
		snapshot, storeErr := m.store.RequeuePrepared(ctx, agentID, inputID)
		if storeErr == nil {
			m.observer.QueueChanged(snapshot)
		}
		shouldDrain := storeErr == nil && !snapshot.ActiveTurn && !snapshot.Paused
		c.mu.Unlock()
		if shouldDrain {
			m.scheduleDrain(agentID)
		}
		if storeErr != nil {
			return Snapshot{}, storeErr
		}
		return snapshot, ErrSteeringUnsupported
	}
	turnEnded := errors.Is(err, ErrTurnEnded)
	if err != nil && !turnEnded {
		snapshot, storeErr := m.recordDispatchFailure(ctx, *prepared, err)
		c.mu.Unlock()
		return snapshot, storeErr
	}
	if !turnEnded {
		current, storeErr := m.store.Snapshot(ctx, agentID)
		if storeErr != nil {
			c.mu.Unlock()
			return Snapshot{}, storeErr
		}
		turnEnded = !current.ActiveTurn
	}
	if turnEnded {
		snapshot, storeErr := m.store.RequeuePrepared(ctx, agentID, inputID)
		if storeErr != nil {
			c.mu.Unlock()
			return Snapshot{}, storeErr
		}
		m.observer.QueueChanged(snapshot)
		shouldDrain := !snapshot.ActiveTurn && !snapshot.Paused
		c.mu.Unlock()
		if shouldDrain {
			m.scheduleDrain(agentID)
		}
		return snapshot, nil
	}
	result.StartsTurn = true
	result.Steering = true
	snapshot, _, err = m.acceptDispatch(ctx, *prepared, result)
	if err != nil {
		c.mu.Unlock()
		return Snapshot{}, err
	}
	shouldDrain := !snapshot.ActiveTurn && !snapshot.Paused
	c.mu.Unlock()
	if shouldDrain {
		m.scheduleDrain(agentID)
	}
	return snapshot, nil
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
	if !m.beginActivity() {
		return nil, ErrManagerStopped
	}
	c := m.coordinator(agentID)
	c.mu.Lock()
	snapshot, resumeQueue, err := m.store.pauseForPlannedRestart(ctx, agentID)
	if err == nil {
		c.plannedRestarts++
		if resumeQueue {
			m.observer.QueueChanged(snapshot)
		}
	}
	c.mu.Unlock()
	if err != nil {
		m.endActivity()
		return nil, err
	}
	return &PlannedRestart{
		manager: m, coordinator: c, agentID: agentID, resumeQueue: resumeQueue,
	}, nil
}

// Finish records that the replaced process cannot finish its old turn. A
// successful replacement restores only the temporary pause that this guard
// created. A failed launch keeps that pause for an explicit retry.
func (r *PlannedRestart) Finish(ctx context.Context, processReplaced, restartSucceeded bool) error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		r.coordinator.mu.Lock()
		r.coordinator.plannedRestarts--
		// Only the last restart in flight resumes. An overlapping restart keeps
		// its own process stop ahead of the queue.
		last := r.coordinator.plannedRestarts == 0
		resumeQueue := last && r.resumeQueue && (!processReplaced || restartSucceeded)
		snapshot, changed, err := r.manager.store.finishPlannedRestart(ctx, r.agentID, processReplaced, resumeQueue)
		if err == nil && changed {
			r.manager.observer.QueueChanged(snapshot)
		}
		shouldDrain := err == nil && changed && !snapshot.Paused && !snapshot.ActiveTurn && len(snapshot.Items) > 0
		r.coordinator.mu.Unlock()
		r.manager.endActivity()
		if shouldDrain {
			r.manager.scheduleDrain(r.agentID)
		}
		r.err = err
	})
	return r.err
}

func (m *Manager) Recover(ctx context.Context) error {
	if err := m.RecoverState(ctx); err != nil {
		return err
	}
	return m.DrainRecovered(ctx)
}

// RecoverState reconciles interrupted persisted state without dispatching
// queued input. Bootstrap uses it before the Worker receives its owner.
func (m *Manager) RecoverState(ctx context.Context) error {
	if !m.beginActivity() {
		return ErrManagerStopped
	}
	defer m.endActivity()
	snapshots, err := m.store.Recover(ctx)
	if err != nil {
		return err
	}
	for i := range snapshots {
		m.observer.QueueChanged(snapshots[i])
		if snapshots[i].Paused {
			continue
		}
		m.lifecycleMu.Lock()
		if !m.stopped {
			m.recovered[snapshots[i].AgentID] = struct{}{}
		}
		m.lifecycleMu.Unlock()
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
		c := m.coordinator(agentID)
		c.mu.Lock()
		snapshot, err := m.store.Snapshot(ctx, agentID)
		c.mu.Unlock()
		if err != nil {
			drainErr = errors.Join(drainErr, fmt.Errorf("read recovered queue %s: %w", agentID, err))
			continue
		}
		m.lifecycleMu.Lock()
		delete(m.recovered, agentID)
		m.lifecycleMu.Unlock()
		if !snapshot.Paused && len(snapshot.Items) > 0 {
			m.scheduleDrain(agentID)
		}
	}
	return drainErr
}

func (m *Manager) scheduleDrain(agentID string) {
	if !m.beginActivity() {
		return
	}
	c := m.coordinator(agentID)
	c.mu.Lock()
	if c.draining {
		c.mu.Unlock()
		m.endActivity()
		return
	}
	c.draining = true
	c.mu.Unlock()
	go func() {
		defer m.endActivity()
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
			c.mu.Unlock()
			return
		}
		prepared, snapshot, err := m.store.PrepareDispatch(context.Background(), agentID)
		if err != nil {
			slog.Error("agent input queue prepare failed", "agent_id", agentID, "error", err)
			c.draining = false
			c.mu.Unlock()
			return
		}
		if prepared == nil {
			c.draining = false
			c.mu.Unlock()
			return
		}
		m.observer.QueueChanged(snapshot)
		c.mu.Unlock()

		result, dispatchErr := m.dispatcher.Dispatch(prepared.Item)

		c.mu.Lock()
		if dispatchErr != nil {
			if _, storeErr := m.recordDispatchFailure(context.Background(), *prepared, dispatchErr); storeErr != nil {
				slog.Error("agent input queue failure persistence failed", "agent_id", agentID, "input_id", prepared.Item.ID, "error", storeErr)
			}
			c.draining = false
			c.mu.Unlock()
			return
		}
		_, persisted, err := m.acceptDispatch(context.Background(), *prepared, result)
		if err != nil {
			slog.Error("agent input queue acceptance persistence failed", "agent_id", agentID, "input_id", prepared.Item.ID, "error", err)
			c.draining = false
			c.mu.Unlock()
			return
		}
		if !persisted || result.StartsTurn {
			c.draining = false
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
	}
}

func (m *Manager) acceptDispatch(ctx context.Context, prepared PreparedDispatch, result DispatchResult) (Snapshot, bool, error) {
	transcript, snapshot, err := m.store.Accept(ctx, prepared, result)
	if err == nil {
		m.observer.InputAccepted(transcript)
		m.observer.QueueChanged(snapshot)
		if result.AfterAccept != nil {
			result.AfterAccept()
		}
		return snapshot, true, nil
	}
	uncertainErr := fmt.Errorf("provider accepted input but transcript persistence failed: %w", err)
	snapshot, failErr := m.store.FailDispatch(ctx, prepared.Item.AgentID, prepared.Item.ID, uncertainErr, true)
	if failErr != nil {
		return Snapshot{}, false, errors.Join(err, failErr)
	}
	m.observer.QueueChanged(snapshot)
	return snapshot, false, nil
}

// recordDispatchFailure stores the outcome of a dispatch that the provider
// refused. Delivery has three outcomes, not two. ErrDispatchNotReady means the
// input never reached the provider, so the item returns to the queue and only
// the queue pauses: a message the user typed must not need a manual retry
// because the agent was between processes. The other two outcomes mark the
// item FAILED or DELIVERY_UNCERTAIN, which the user resolves explicitly.
func (m *Manager) recordDispatchFailure(ctx context.Context, prepared PreparedDispatch, dispatchErr error) (Snapshot, error) {
	if errors.Is(dispatchErr, ErrDispatchNotReady) {
		snapshot, err := m.store.RequeueAndPause(ctx, prepared.Item.AgentID, prepared.Item.ID, dispatchErr)
		if err != nil {
			return Snapshot{}, err
		}
		m.observer.QueueChanged(snapshot)
		return snapshot, nil
	}
	var deliveryErr *DeliveryError
	uncertain := errors.As(dispatchErr, &deliveryErr) && deliveryErr.Uncertain
	snapshot, err := m.store.FailDispatch(ctx, prepared.Item.AgentID, prepared.Item.ID, dispatchErr, uncertain)
	if err != nil {
		return Snapshot{}, err
	}
	m.observer.QueueChanged(snapshot)
	return snapshot, nil
}
