package muse

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// registryPublication owns receipts independently of native transcript state.
// Retained registry writes keep neither the Agent nor its session history.
type registryPublication struct {
	mu                         sync.Mutex
	registryRevision           int64
	registryHeadRevision       int64
	registryReconciledRevision int64
	queuedRevision             int64
	queuedHeadRevision         int64
	workflowPublications       map[string]workflowChildPublication
}

type nativeRegistryRow struct {
	upsert     bgtask.Upsert
	revive     bool
	revived    bool
	closeKey   string
	childID    string
	attempt    int64
	hasAttempt bool
}

type nativeRegistryPlan struct {
	rows     []nativeRegistryRow
	setupErr error
}

type nativeRegistryBatch struct {
	writer      agent.ProviderServices
	dispatcher  *outputDispatcher
	publication *registryPublication
	revision    int64
	head        bool
	rows        []nativeRegistryRow
	next        int
}

// run calls direct registry methods. Each successful row and revival stays
// complete when a later operation needs another attempt.
func (batch *nativeRegistryBatch) run() error {
	if err := batch.dispatcher.checkPublicationLocks(); err != nil {
		return err
	}
	for batch.next < len(batch.rows) {
		row := &batch.rows[batch.next]
		if row.closeKey != "" {
			if err := batch.writer.CloseBackgroundTask(row.closeKey, bgtask.StatusStopped); err != nil {
				return err
			}
		} else {
			if row.revive && !row.revived {
				if err := batch.writer.ReviveBackgroundTask(row.upsert.RowKey); err != nil {
					return err
				}
				row.revived = true
			}
			if err := batch.writer.UpsertBackgroundTask(row.upsert); err != nil {
				return err
			}
		}
		if row.hasAttempt {
			publication := batch.publication
			publication.mu.Lock()
			receipt := publication.workflowPublications[row.childID]
			if !receipt.hasAcceptedAttempt || row.attempt > receipt.acceptedAttempt {
				receipt.acceptedAttempt, receipt.hasAcceptedAttempt = row.attempt, true
			}
			if receipt.hasPendingRestart && receipt.pendingRestartAttempt <= row.attempt {
				receipt.hasPendingRestart = false
			}
			publication.workflowPublications[row.childID] = receipt
			publication.mu.Unlock()
		}
		batch.next++
	}
	if publication := batch.publication; publication != nil {
		publication.mu.Lock()
		publication.registryRevision = max(publication.registryRevision, batch.revision)
		if publication.queuedRevision == batch.revision {
			publication.queuedRevision = 0
		}
		if batch.head {
			publication.registryReconciledRevision = max(publication.registryReconciledRevision, batch.revision)
			if publication.registryHeadRevision == batch.revision {
				publication.registryHeadRevision = 0
			}
			if publication.queuedHeadRevision == batch.revision {
				publication.queuedHeadRevision = 0
			}
		}
		publication.mu.Unlock()
	}
	return nil
}

// The caller holds stateMu. Receipt locks never acquire provider locks.
func (item *itemState) registryState() *registryPublication {
	if item.registry == nil {
		item.registry = &registryPublication{workflowPublications: make(map[string]workflowChildPublication)}
	}
	return item.registry
}

// The caller holds dispatcher.mu. Only completely prepared rows enter the
// queue, after child creation and opening-prompt admission have finished.
func (a *Agent) queueRegistryBatchLocked(batch *nativeRegistryBatch) error {
	batch.dispatcher = a.dispatcher
	queue := agent.OutputQueueFor(a.sink)
	if queue == nil {
		return fmt.Errorf("the Muse registry publication has no output queue")
	}
	if publication := batch.publication; publication != nil {
		publication.mu.Lock()
		headPending := batch.head && publication.registryReconciledRevision < batch.revision && publication.queuedHeadRevision < batch.revision
		if (publication.registryRevision >= batch.revision || publication.queuedRevision >= batch.revision) && !headPending {
			publication.mu.Unlock()
			return nil
		}
		publication.queuedRevision = max(publication.queuedRevision, batch.revision)
		if headPending {
			publication.queuedHeadRevision = batch.revision
		}
		publication.mu.Unlock()
	}
	a.deferOutputAction(func() {
		queue.Append(agent.OutputWrite{ScopeID: "muse-registry", Label: "publish Muse registry state", Run: batch.run})
		if err := queue.Flush(); err != nil {
			slog.Debug("retain Muse registry publication", "error", err)
		}
	})
	return nil
}
