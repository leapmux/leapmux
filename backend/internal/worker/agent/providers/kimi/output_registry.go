package kimi

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// kimiPublicationMutex keeps the mutex separate from the Agent allocation.
// A queued writer can retain this mutex without retaining native session history.
type kimiPublicationMutex struct {
	once  sync.Once
	value *sync.Mutex
}

func (m *kimiPublicationMutex) mutex() *sync.Mutex {
	m.once.Do(func() { m.value = &sync.Mutex{} })
	return m.value
}

func (m *kimiPublicationMutex) Lock()         { m.mutex().Lock() }
func (m *kimiPublicationMutex) Unlock()       { m.mutex().Unlock() }
func (m *kimiPublicationMutex) TryLock() bool { return m.mutex().TryLock() }

// finishDispatch releases native state before persistence invokes registry observers.
func (a *Agent) finishDispatch() {
	a.finishInvocationRecoveryIfReady()
	a.dispatchMu.Unlock()
	a.flushQueuedOutput()
}

func (a *Agent) finishSessionMutation() {
	a.sessionMu.Unlock()
	a.flushQueuedOutput()
}

func (a *Agent) flushQueuedOutput() {
	if queue := agent.OutputQueueFor(a.sink); queue != nil {
		if err := queue.FlushOwnerAfterReady(a.registryOutputOwner()); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
			slog.Debug("retain Kimi output after a state mutation", "error", err)
		}
	}
}

func (a *Agent) registryOutputOwner() *agent.OutputOwner {
	a.registryOnce.Do(func() { a.registryOwner = agent.NewOutputOwner() })
	return a.registryOwner
}

func kimiRegistryPublicationLockError(dispatch, session *sync.Mutex) error {
	if !dispatch.TryLock() {
		return agent.ErrOutputInFlight
	}
	dispatch.Unlock()
	if !session.TryLock() {
		return agent.ErrOutputInFlight
	}
	session.Unlock()
	return nil
}

// queueRegistryWrite retains the physical writer and immutable mutation inputs.
// The callback holds no provider lock and never resolves its writer through the Agent again.
func (a *Agent) queueRegistryWrite(writer agent.BackgroundTaskServices, label string, write func(agent.BackgroundTaskServices) error) error {
	queue := agent.OutputQueueFor(a.sink)
	if queue == nil || writer == nil {
		return errors.New("the Kimi background task update has no output queue or writer")
	}
	dispatch, session := a.dispatchMu.mutex(), a.sessionMu.mutex()
	queue.Append(agent.OutputWrite{Owner: a.registryOutputOwner(), ScopeID: "kimi-registry", Label: label, Run: func() error {
		if err := kimiRegistryPublicationLockError(dispatch, session); err != nil {
			return err
		}
		return write(writer)
	}})
	if err := queue.Flush(); err != nil {
		return &agent.PendingOutputError{Err: err}
	}
	return nil
}

func (a *Agent) queueRegistryUpsert(writer agent.BackgroundTaskServices, update bgtask.Upsert) error {
	if update.RowKey == "" {
		return errors.New("the Kimi background task update has no row key")
	}
	update = update.Clean()
	return a.queueRegistryWrite(writer, "publish Kimi background task", func(writer agent.BackgroundTaskServices) error {
		return writer.UpsertBackgroundTask(update)
	})
}

func (a *Agent) queueRegistryClose(writer agent.BackgroundTaskServices, key string, status bgtask.Status) error {
	return a.queueRegistryWrite(writer, "close Kimi background task", func(writer agent.BackgroundTaskServices) error {
		return writer.CloseBackgroundTask(key, status)
	})
}

func (a *Agent) queueRegistryRevive(writer agent.BackgroundTaskServices, key string) error {
	return a.queueRegistryWrite(writer, "revive Kimi background task", func(writer agent.BackgroundTaskServices) error {
		return writer.ReviveBackgroundTask(key)
	})
}

// queueRegistryMetadata reads status in its ordered position, after earlier closes and revivals.
func (a *Agent) queueRegistryMetadata(writer agent.BackgroundTaskServices, update bgtask.Upsert) error {
	return a.queueRegistryWrite(writer, "publish Kimi child metadata", func(writer agent.BackgroundTaskServices) error {
		_, status, exists, err := writer.LookupBackgroundTask(update.RowKey)
		if err != nil {
			return fmt.Errorf("read the Kimi child registry row: %w", err)
		}
		if !exists {
			return fmt.Errorf("the Kimi child registry row %q does not exist", update.RowKey)
		}
		attempt := update
		attempt.Status = status
		return writer.UpsertBackgroundTask(attempt)
	})
}

// An unknown parent result does not prove that its later identified native
// task ended. Keep both rows until native task finality can safely merge them.
func (a *Agent) queueNativeTaskAlias(writer agent.BackgroundTaskServices, oldKey, newKey string, status bgtask.Status) error {
	return a.queueRegistryWrite(writer, "adopt Kimi native task identity", func(writer agent.BackgroundTaskServices) error {
		_, sourceStatus, exists, err := writer.LookupBackgroundTask(oldKey)
		if err != nil {
			return err
		}
		if exists && sourceStatus == bgtask.StatusEndedWithUnknownOutcome && !status.IsFinished() {
			return nil
		}
		return writer.RenameBackgroundTask(oldKey, newKey)
	})
}
