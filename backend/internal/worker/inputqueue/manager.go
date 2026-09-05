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

func (m *Manager) Enqueue(ctx context.Context, input NewItem) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(input.AgentID)
	c.mu.Lock()
	snapshot, err := m.store.Enqueue(ctx, input)
	if err == nil {
		m.observer.QueueChanged(snapshot)
	}
	c.mu.Unlock()
	if err == nil {
		m.scheduleDrain(input.AgentID)
	}
	return snapshot, err
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
	c := m.coordinator(agentID)
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot, text, attachments, err := m.store.BeginEdit(ctx, agentID, inputID, clientID, takeover)
	if err == nil {
		m.observer.QueueChanged(snapshot)
	}
	return snapshot, text, attachments, err
}

func (m *Manager) Update(ctx context.Context, agentID, inputID, clientID string, expectedVersion uint64, text string, attachments []Attachment) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	snapshot, err := m.store.Update(ctx, agentID, inputID, clientID, expectedVersion, text, attachments)
	if err == nil {
		m.observer.QueueChanged(snapshot)
	}
	c.mu.Unlock()
	if err == nil {
		m.scheduleDrain(agentID)
	}
	return snapshot, err
}

func (m *Manager) CancelEdit(ctx context.Context, agentID, inputID, clientID string) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	snapshot, err := m.store.CancelEdit(ctx, agentID, inputID, clientID)
	if err == nil {
		m.observer.QueueChanged(snapshot)
	}
	c.mu.Unlock()
	if err == nil {
		m.scheduleDrain(agentID)
	}
	return snapshot, err
}

func (m *Manager) Delete(ctx context.Context, agentID, inputID string) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	snapshot, err := m.store.Delete(ctx, agentID, inputID)
	if err == nil {
		m.observer.QueueChanged(snapshot)
	}
	c.mu.Unlock()
	if err == nil {
		m.scheduleDrain(agentID)
	}
	return snapshot, err
}

func (m *Manager) Move(ctx context.Context, agentID, inputID, beforeInputID string) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	snapshot, err := m.store.Move(ctx, agentID, inputID, beforeInputID)
	if err == nil {
		m.observer.QueueChanged(snapshot)
	}
	c.mu.Unlock()
	if err == nil {
		m.scheduleDrain(agentID)
	}
	return snapshot, err
}

func (m *Manager) SetPaused(ctx context.Context, agentID string, paused bool) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	reason := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED
	if paused {
		reason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL
	}
	c := m.coordinator(agentID)
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
	c := m.coordinator(agentID)
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot, err := m.store.Pause(ctx, agentID, reason)
	if err == nil {
		m.observer.QueueChanged(snapshot)
	}
	return snapshot, err
}

func (m *Manager) TurnEnded(ctx context.Context, agentID string) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	snapshot, err := m.store.TurnEnded(ctx, agentID)
	if err == nil {
		m.observer.QueueChanged(snapshot)
	}
	c.mu.Unlock()
	if err == nil {
		m.scheduleDrain(agentID)
	}
	return snapshot, err
}

func (m *Manager) TurnStarted(ctx context.Context, agentID string) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot, err := m.store.TurnStarted(ctx, agentID)
	if err == nil {
		m.observer.QueueChanged(snapshot)
	}
	return snapshot, err
}

func (m *Manager) Retry(ctx context.Context, agentID, inputID string, confirmUncertain bool) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot, err := m.store.Retry(ctx, agentID, inputID, confirmUncertain)
	if err != nil {
		return Snapshot{}, err
	}
	m.observer.QueueChanged(snapshot)
	return m.dispatchPrepared(ctx, agentID, m.store.PrepareRetry)
}

func (m *Manager) Steer(ctx context.Context, agentID, inputID string) (Snapshot, error) {
	if !m.beginActivity() {
		return Snapshot{}, ErrManagerStopped
	}
	defer m.endActivity()
	if m.dispatcher == nil || !m.dispatcher.SupportsSteering(agentID) {
		return Snapshot{}, ErrSteeringUnsupported
	}
	c := m.coordinator(agentID)
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
			_ = snapshot
			return
		}
		m.observer.QueueChanged(snapshot)
		result, err := m.dispatcher.Dispatch(prepared.Item)
		if err != nil {
			_, storeErr := m.recordDispatchFailure(context.Background(), *prepared, err)
			if storeErr != nil {
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
		if !persisted {
			c.draining = false
			c.mu.Unlock()
			return
		}
		if result.StartsTurn {
			c.draining = false
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
	}
}

type prepareFunc func(context.Context, string) (*PreparedDispatch, Snapshot, error)

func (m *Manager) dispatchPrepared(ctx context.Context, agentID string, prepare prepareFunc) (Snapshot, error) {
	prepared, snapshot, err := prepare(ctx, agentID)
	if err != nil || prepared == nil {
		return snapshot, err
	}
	m.observer.QueueChanged(snapshot)
	result, err := m.dispatcher.Dispatch(prepared.Item)
	if err != nil {
		return m.recordDispatchFailure(ctx, *prepared, err)
	}
	snapshot, _, err = m.acceptDispatch(ctx, *prepared, result)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
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

func (m *Manager) recordDispatchFailure(ctx context.Context, prepared PreparedDispatch, dispatchErr error) (Snapshot, error) {
	var deliveryErr *DeliveryError
	uncertain := errors.As(dispatchErr, &deliveryErr) && deliveryErr.Uncertain
	snapshot, err := m.store.FailDispatch(ctx, prepared.Item.AgentID, prepared.Item.ID, dispatchErr, uncertain)
	if err != nil {
		return Snapshot{}, err
	}
	m.observer.QueueChanged(snapshot)
	return snapshot, nil
}
