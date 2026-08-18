package service

import (
	"context"
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/notifier"
)

// WorkerDeregisterEffects holds the three out-of-database effects that
// MUST follow every worker deregistration, whichever surface performed it.
//
// The store verb only flips the row's status. Without these effects the
// worker is never told to stop, so it keeps its Connect stream and its
// leases; its memoized delegation scope keeps outstanding tokens reaching
// across workers until a cache TTL expires; and no owner's client learns
// the list changed. The row also STAYS at DEREGISTERING forever, because
// only the notifier's acknowledgement path calls MarkDeleted.
//
// The admin surfaces used to run none of the three, so a deregister typed
// by an administrator did strictly less than the same deregister typed by
// the worker's owner. One type carried by every caller is what removes
// that difference.
type WorkerDeregisterEffects struct {
	scopeCache  *auth.DelegationScopeCache
	notifier    *notifier.Notifier
	broadcaster *HubEventBroadcaster
}

// NewWorkerDeregisterEffects binds the three collaborators. Production
// passes all three. Each one is nil-tolerant so a test that exercises only
// the store half can pass nil, the same rule
// auth.CredentialLifecycleEffects holds.
func NewWorkerDeregisterEffects(scopeCache *auth.DelegationScopeCache, n *notifier.Notifier, broadcaster *HubEventBroadcaster) *WorkerDeregisterEffects {
	return &WorkerDeregisterEffects{scopeCache: scopeCache, notifier: n, broadcaster: broadcaster}
}

// Apply runs the three effects for one deregistered worker. ownerID is the
// worker's registrant, because the workers-changed notification is
// addressed to the OWNER, not to the administrator who typed the command.
//
// Callers MUST run it AFTER the deregistering transaction commits.
// SendDeregister persists a notification row and moves worker-manager
// state, so a rollback after it would leave a worker told it was
// deregistered while its row says otherwise.
func (e *WorkerDeregisterEffects) Apply(ctx context.Context, workerID, ownerID string) error {
	if e == nil {
		return nil
	}
	// Deregistration is the operator's containment action against a
	// compromised worker: evict its memoized delegation scope so
	// outstanding tokens minted on it lose their cross-worker reach on the
	// next SubmitOps, not a cache TTL later.
	if e.scopeCache != nil {
		e.scopeCache.EvictWorker(workerID)
	}

	if e.notifier != nil {
		if err := e.notifier.SendDeregister(ctx, workerID); err != nil {
			return fmt.Errorf("send deregister: %w", err)
		}
	}

	if e.broadcaster != nil {
		e.broadcaster.NotifyWorkersChanged(ownerID)
	}
	return nil
}
