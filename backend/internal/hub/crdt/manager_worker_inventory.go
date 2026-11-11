package crdt

import (
	"context"
	"errors"
	"fmt"
	"sort"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"google.golang.org/protobuf/proto"
)

// WorkerTabInventorySnapshot contains immutable admission-time candidates.
// Only its connection's receive loop calls RecordHostedTabs or Finish.
type WorkerTabInventorySnapshot struct {
	owner      userid.UserID
	workerID   string
	candidates map[string]*leapmuxv1.TabRecord
	finished   bool
}

// BeginWorkerTabInventory captures candidates before publishing a connection.
// A tab added after admission cannot become a deletion candidate.
func (m *Manager) BeginWorkerTabInventory(ctx context.Context, workerID string) (*WorkerTabInventorySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if workerID == "" {
		return nil, errors.New("worker inventory requires a worker id")
	}
	state := m.capturedState()
	snapshot := &WorkerTabInventorySnapshot{owner: m.owner, workerID: workerID, candidates: make(map[string]*leapmuxv1.TabRecord)}
	for _, tab := range Project(state).OwnedTabs {
		if tab.WorkerID == workerID {
			snapshot.candidates[tab.TabID] = state.GetTabs()[tab.TabID]
		}
	}
	return snapshot, nil
}

// RecordHostedTabs excludes reported identities from deletion. The caller
// validates each segment before changing the snapshot.
func (s *WorkerTabInventorySnapshot) RecordHostedTabs(tabs []*leapmuxv1.TabIdent) {
	for _, tab := range tabs {
		if candidate := s.candidates[tab.GetTabId()]; candidate != nil && candidate.GetTabType() == tab.GetTabType() {
			delete(s.candidates, tab.GetTabId())
		}
	}
}

// FinishWorkerTabInventory rechecks candidates and commits on the sole writer.
// A changed, revived, reassigned, or newly created tab survives reconciliation.
func (m *Manager) FinishWorkerTabInventory(ctx context.Context, snapshot *WorkerTabInventorySnapshot) error {
	if snapshot == nil || snapshot.owner.IsZero() || !snapshot.owner.Matches(m.owner.String()) {
		return errors.New("worker inventory belongs to another owner")
	}
	if snapshot.finished {
		return errors.New("worker inventory already finished")
	}
	select {
	case <-m.startedChan:
	default:
		return errors.New("worker inventory manager has not started")
	}
	snapshot.finished = true
	var result error
	done := make(chan struct{})
	task := managerTask{done: done, run: func() {
		if err := ctx.Err(); err != nil {
			result = err
			return
		}
		current := Project(m.state)
		var stale []string
		for _, tab := range current.OwnedTabs {
			candidate := snapshot.candidates[tab.TabID]
			if candidate != nil && tab.WorkerID == snapshot.workerID && proto.Equal(candidate, m.state.GetTabs()[tab.TabID]) {
				stale = append(stale, tab.TabID)
			}
		}
		if len(stale) == 0 {
			return
		}
		sort.Strings(stale)
		ops := make([]*leapmuxv1.CrdtOp, 0, len(stale))
		for _, tabID := range stale {
			ops = append(ops, &leapmuxv1.CrdtOp{OpId: id.Generate(), Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{Tab: &leapmuxv1.TabIdent{TabType: snapshot.candidates[tabID].GetTabType(), TabId: tabID}}}})
		}
		// SubmitInternal would send back to this goroutine and deadlock it.
		response := m.processSubmit(ctx, submitJob{internal: true, input: SubmitInput{Internal: true, PrincipalID: HubReservedPrincipal, OriginClient: m.hubClientID, Batches: []*leapmuxv1.OpBatch{{BatchId: "worker-sync-" + snapshot.workerID + "-" + id.Generate(), Ops: ops}}}})
		if response.err != nil {
			result = response.err
			return
		}
		if len(response.results) != 1 || response.results[0].GetCommitted() == nil {
			result = fmt.Errorf("worker inventory tombstones were not committed: %v", response.results)
		}
	}}
	m.touchActivity()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.done:
		return errors.New("worker inventory manager stopped")
	case m.taskCh <- task:
	}
	// Once admitted, join the task even when ctx ends. Its database work uses
	// ctx, and the caller must not race a still-running completion operation.
	<-done
	return result
}
