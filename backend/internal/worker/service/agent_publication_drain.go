package service

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

var errAgentPublicationClosed = errors.New("the agent tab is closed")

// agentPublicationDrain tracks work from one physical root independently of
// other agents. The caller stops further admission before waiting.
type agentPublicationDrain struct {
	mu    sync.Mutex
	roots map[string]*agentPublicationBatch
}

type agentPublicationBatch struct {
	pending int
	done    chan struct{}
}

func (d *agentPublicationDrain) begin(rootID string) func() {
	d.mu.Lock()
	if d.roots == nil {
		d.roots = make(map[string]*agentPublicationBatch)
	}
	batch := d.roots[rootID]
	if batch == nil {
		batch = &agentPublicationBatch{done: make(chan struct{})}
		d.roots[rootID] = batch
	}
	batch.pending++
	d.mu.Unlock()
	return func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		batch.pending--
		if batch.pending == 0 {
			delete(d.roots, rootID)
			close(batch.done)
		}
	}
}

func (d *agentPublicationDrain) wait(ctx context.Context, rootID string) error {
	d.mu.Lock()
	batch := d.roots[rootID]
	d.mu.Unlock()
	if batch == nil {
		return nil
	}
	select {
	case <-batch.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *agentPublicationDrain) waitAll(ctx context.Context) error {
	d.mu.Lock()
	batches := make([]*agentPublicationBatch, 0, len(d.roots))
	for _, batch := range d.roots {
		batches = append(batches, batch)
	}
	d.mu.Unlock()
	for _, batch := range batches {
		select {
		case <-batch.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// admitAgentMutation counts the whole operation before reading the live rows.
// After the root closes, a waiter can therefore join every operation that read
// it while open. Later callers can read retained state without mutating it.
func (svc *Service) admitAgentMutation(ctx context.Context, agentRow db.Agent) (func(), error) {
	rootID := agentRow.ID
	if agentRow.ParentAgentID.Valid {
		var err error
		rootID, err = svc.Queries.GetRootAgentID(ctx, agentRow.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errAgentPublicationClosed
		}
		if err != nil {
			return nil, err
		}
	}
	finish := svc.semanticEvents.agentMutations.begin(rootID)
	live, err := svc.Queries.CanWatchAgentHistory(ctx, rootID)
	if err == nil && live && agentRow.ID != rootID {
		live, err = svc.Queries.CanWatchAgentHistory(ctx, agentRow.ID)
	}
	if err != nil {
		finish()
		return nil, err
	}
	if !live {
		finish()
		return nil, errAgentPublicationClosed
	}
	return finish, nil
}

func (svc *Service) requireAgentMutation(sender channel.ResponseWriter, row db.Agent) func() {
	finish, err := svc.admitAgentMutation(bgCtx(), row)
	if errors.Is(err, errAgentPublicationClosed) {
		sendNotFoundError(sender, err.Error())
	} else if err != nil {
		sendInternalError(sender, err.Error())
	}
	return finish
}
