package acp

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

var errRegistryPublicationLocked = errors.New("an ACP publication lock still owns the registry write")

// ApplyRegistryObservation retains a registry-only observation in the root output queue.
// A nil error transfers publication ownership to the queue, including after storage refusal.
// A caller reading a native snapshot must keep its owning WithSessionID lease through admission.
func (b *Base) ApplyRegistryObservation(obs *SubagentObservation) error {
	b.sessionUpdates.mu.Lock()
	defer b.sessionUpdates.mu.Unlock()
	return b.main().applyRegistryObservation(obs)
}

func registryObservationUpsert(obs SubagentObservation, rowKey, childID, parentID string) bgtask.Upsert {
	kind := obs.Kind
	if kind == bgtask.KindUnspecified {
		kind = bgtask.KindSubagent
	}
	return bgtask.Upsert{
		RowKey: rowKey, Kind: kind, ChildAgentID: childID, ParentAgentID: parentID,
		GroupKey: obs.GroupKey, GroupLabel: obs.GroupLabel, Title: obs.Title,
		TitleIsCommand: obs.TitleIsCommand, ActiveForm: obs.Activity, Status: obs.Status,
	}
}

func validateRegistryObservation(obs *SubagentObservation) error {
	if obs == nil || strings.TrimSpace(obs.RowKey) == "" {
		return errors.New("the registry observation has no row key")
	}
	if obs.RenameFrom != "" && strings.TrimSpace(obs.RenameFrom) == "" {
		return errors.New("the registry observation has an empty rename source")
	}
	if obs.Mode != ModeUpsert && obs.Mode != ModeCloseOnly {
		return errors.New("the registry observation has an unknown mode")
	}
	if obs.Mode == ModeCloseOnly && !obs.CloseRow {
		return errors.New("a close-only registry observation must close its row")
	}
	if obs.CloseRow && !obs.Status.IsFinished() {
		return errors.New("a registry close requires a final status")
	}
	if obs.ChildAgentKey != "" || obs.ChildSpawnSpanID != "" || obs.ChildAgentSessionID != "" ||
		obs.Prompt != "" || obs.ChildTranscriptPayload != nil || obs.ChildUpdate != nil || obs.ChildUpdateFlush ||
		obs.ReportID != "" || obs.Report != (agent.SubagentReport{}) {
		return errors.New("a registry-only observation cannot carry child conversation data")
	}
	return nil
}

// applyRegistryObservation runs under sessionUpdates.mu, which excludes session replacement.
// The complete batch enters the queue before its admitted intent becomes visible.
func (c *conversation) applyRegistryObservation(observation *SubagentObservation) error {
	if err := validateRegistryObservation(observation); err != nil {
		return err
	}
	if c.b.CurrentSessionID() == "" {
		return errors.New("the registry observation has no owning session")
	}
	obs := *observation
	if c.b.childIdentityRefused(obs.RowKey) || c.b.childIdentityRefused(obs.RenameFrom) {
		return agent.ErrChildIdentityRefused
	}
	sink := c.sink()
	queue := agent.OutputQueueFor(c.b.sink)
	if sink == nil || queue == nil {
		return errors.New("the registry observation has no output queue")
	}
	writes := make([]agent.OutputWrite, 0, 3)
	if from, to := obs.RenameFrom, obs.RowKey; from != "" && from != to {
		writes = append(writes, agent.OutputWrite{Label: "rename ACP registry row", Run: func() error {
			return sink.RenameBackgroundTask(from, to)
		}})
	}
	if obs.Mode == ModeUpsert {
		upsert := registryObservationUpsert(obs, obs.RowKey, "", c.agentID())
		writes = append(writes, agent.OutputWrite{Label: "upsert ACP registry row", Run: func() error {
			return sink.UpsertBackgroundTask(upsert)
		}})
	}
	if obs.CloseRow {
		key, status := obs.RowKey, obs.Status
		writes = append(writes, agent.OutputWrite{Label: "close ACP registry row", Run: func() error {
			return sink.CloseBackgroundTask(key, status)
		}})
	}
	c.appendRegistryWrites(queue, writes, func() {
		if obs.RenameFrom != "" {
			c.b.openRows.renamed(obs.RenameFrom, obs.RowKey)
		}
		if obs.CloseRow || obs.Status.IsFinished() {
			c.b.openRows.closed(obs.RowKey)
		} else {
			c.b.openRows.opened(obs.RowKey)
		}
	})
	return nil
}

// appendRegistryWrites prevents incidental transcript flushes and automatic retries
// from publishing a partly admitted batch or calling observers under source locks.
func (c *conversation) appendRegistryWrites(queue *agent.OutputQueue, writes []agent.OutputWrite, intent func()) {
	ready := &atomic.Bool{}
	b, output, sourceMutex := c.b, c.out, c.publicationMutex
	for i := range writes {
		run := writes[i].Run
		writes[i].Run = func() error {
			if !ready.Load() {
				return errRegistryPublicationLocked
			}
			if err := b.checkRegistryPublicationLocks(); err != nil {
				return err
			}
			if output != nil && output != &b.acpTurnOutput {
				if !output.turnMu.TryLock() {
					return errRegistryPublicationLocked
				}
				output.turnMu.Unlock()
			}
			if sourceMutex != nil {
				if !sourceMutex.TryLock() {
					return errRegistryPublicationLocked
				}
				sourceMutex.Unlock()
			}
			return run()
		}
	}
	queue.Append(writes...)
	if intent != nil {
		intent()
	}
	ready.Store(true)
}

// checkRegistryPublicationLocks checks readiness, not native session authority.
// Admission fixes the destination. A later session replacement cannot revoke an accepted write.
func (b *Base) checkRegistryPublicationLocks() error {
	for _, lock := range []interface {
		TryLock() bool
		Unlock()
	}{&b.optionWriteMu, &b.sessionMu, &b.sessionUpdates.mu, &b.lifecycleMu, &b.turnMu, &b.Mu, &b.children.mu} {
		if !lock.TryLock() {
			return errRegistryPublicationLocked
		}
		lock.Unlock()
	}
	if !b.childDispatch.mu.TryLock() {
		return errRegistryPublicationLocked
	}
	paused := b.childDispatch.paused != nil
	b.childDispatch.mu.Unlock()
	if paused {
		return errRegistryPublicationLocked
	}
	return nil
}

// FlushRegistryObservations attempts accepted writes after the caller releases its provider locks.
// A refusal remains owned by the queue and its Worker's retry clock.
func (b *Base) FlushRegistryObservations() {
	if b.sink == nil {
		return
	}
	if err := agent.FlushPendingOutput(b.sink); err != nil && !errors.Is(err, errRegistryPublicationLocked) {
		slog.Warn("publish retained ACP registry output", "provider", b.ProviderName(), "error", err)
	}
}

// closeRegistryRow captures the original cleanup delegate before the row loses its route.
// A delayed close never looks up a replacement child under a reused row key.
func (c *conversation) closeRegistryRow(rowKey string, status bgtask.Status) error {
	if !status.IsFinished() {
		return fmt.Errorf("close registry row %q: status is not final", rowKey)
	}
	c.b.children.mu.Lock()
	cleanup := c.b.children.agents[rowKey]
	c.b.children.mu.Unlock()
	return c.appendFinalRegistryObservation(rowKey, SubagentObservation{Mode: ModeCloseOnly, CloseRow: true, Status: status}, "", cleanup)
}

// appendFinalRegistryObservation retains final registry effects on the physical
// root queue. Opening writes and native child adoption remain synchronous.
// A span-only opening must create its row before synchronous native adoption.
// Otherwise Rename can miss the source, and the delayed opening recreates it.
func (c *conversation) appendFinalRegistryObservation(rowKey string, observation SubagentObservation, childID string, cleanup childAgentRef) error {
	if rowKey == "" {
		return errors.New("the final ACP registry observation has no row key")
	}
	if !observation.Status.IsFinished() {
		return fmt.Errorf("finish registry row %q: status is not final", rowKey)
	}
	sink := c.sink()
	queue := agent.OutputQueueFor(c.b.sink)
	if sink == nil || queue == nil {
		return fmt.Errorf("finish registry row %q: no root output queue", rowKey)
	}
	writes := make([]agent.OutputWrite, 0, 3)
	if observation.Mode != ModeCloseOnly && observation.RenameFrom == "" {
		upsert := registryObservationUpsert(observation, rowKey, childID, c.agentID())
		writes = append(writes, agent.OutputWrite{Label: "upsert final ACP registry row", Run: func() error {
			return sink.UpsertBackgroundTask(upsert)
		}})
	}
	if observation.CloseRow {
		status := observation.Status
		writes = append(writes, agent.OutputWrite{Label: "close ACP registry row", Run: func() error {
			return sink.CloseBackgroundTask(rowKey, status)
		}})
	}
	if observation.CloseRow && cleanup.parent != nil && cleanup.id != "" {
		writes = append(writes, agent.OutputWrite{Label: "clean up ACP child services", Run: func() error {
			cleanup.parent.CleanupChildAgent(cleanup.id)
			return nil
		}})
	}
	if len(writes) == 0 {
		return nil
	}
	c.appendRegistryWrites(queue, writes, func() { c.b.openRows.closed(rowKey) })
	return nil
}
