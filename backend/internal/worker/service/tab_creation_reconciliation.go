package service

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TabReconciliationPass owns the publication generation captured before a Hub read.
// ReapIf checks creation before consulting the absence clock or closing a tab.
type TabReconciliationPass interface {
	ApplyArchiveState(context.Context, leapmuxv1.WorkspaceArchiveState, userid.UserID, []*leapmuxv1.TabIdent) ([]string, error)
	ReapIf(leapmuxv1.TabType, string, string, func() bool) bool
	Done()
}

type tabReconciliationPass struct {
	coordinator *tabCreationCoordinator
	generation  uint64
	archive     *archiveObservation
}

func (svc *Service) BeginTabReconciliation() TabReconciliationPass {
	c := svc.creations
	c.mu.Lock()
	defer c.mu.Unlock()
	pass := &tabReconciliationPass{coordinator: c, generation: c.generation, archive: svc.beginArchiveObservation()}
	c.reads[pass] = pass.generation
	return pass
}
func (p *tabReconciliationPass) Done() {
	defer p.archive.done()
	c := p.coordinator
	c.mu.Lock()
	delete(c.reads, p)
	c.pruneVersionsLocked()
	c.mu.Unlock()
}
func (c *tabCreationCoordinator) pruneVersionsLocked() {
	oldest := c.generation
	for _, generation := range c.reads {
		if generation < oldest {
			oldest = generation
		}
	}
	for key, generation := range c.versions {
		if generation <= oldest {
			delete(c.versions, key)
		}
	}
}
func (p *tabReconciliationPass) ReapIf(kind leapmuxv1.TabType, owner, tabID string, due func() bool) bool {
	c := p.coordinator
	svc := c.svc
	keyOwner := owner
	if keyOwner == "" {
		keyOwner = svc.RegisteredBy().String()
	}
	ownerID, ownerOK := userid.New(keyOwner)
	if !ownerOK {
		return false
	}
	unlock := c.lock(creationKey{ownerID.String(), tabID})
	defer unlock()
	c.mu.Lock()
	newer := c.versions[newOwnedTabKey(kind, tabID, owner)] > p.generation
	c.mu.Unlock()
	if newer {
		return false
	}
	pending, err := svc.Queries.HasPendingTabCreation(c.ctx, db.HasPendingTabCreationParams{UserID: ownerID.String(), WorkerID: svc.WorkerID, TabType: int64(kind), TabID: tabID, State: creationPending, State_2: creationCancelRequested})
	if err != nil || pending {
		return false
	}
	if due() {
		svc.CloseTabForReconcile(kind, owner, tabID)
	}
	return true
}

func (p *tabReconciliationPass) ApplyArchiveState(ctx context.Context, state leapmuxv1.WorkspaceArchiveState, owner userid.UserID, tabs []*leapmuxv1.TabIdent) ([]string, error) {
	return p.coordinator.svc.applyObservedTabArchiveState(ctx, state, owner, tabs, p.archive)
}
