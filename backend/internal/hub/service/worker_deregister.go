package service

import (
	"context"

	"connectrpc.com/connect/v2"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// refuseConnectedBundledWorker is the one rule that both DeregisterWorker
// handlers apply to a bundled (auto-registered) worker. online tells whether w
// holds a connection; each handler reads it, because the audit classifies the
// registry read by the handler that makes it.
//
// The Hub refuses a bundled worker only while it is connected. Stopping a
// running one would leave the app with no local worker until its next launch,
// and the app registers a fresh worker on that launch, so the action would
// also be undone. An offline one is removable: that is the only cleanup for the
// ACTIVE row that a launch leaves behind when the app's state file was lost
// (loadOrCreateWorkerState logs it as orphaning the previous one), and for a
// worker whose process failed to start. Removing the current worker of an app
// that failed to start makes the next launch register a new worker id, and its
// tabs do not move to that id.
//
// A deleted row is not refused here: the caller's row-count check reports it
// like an unknown id.
func refuseConnectedBundledWorker(w *store.Worker, online bool) error {
	if w.AutoRegistered && w.DeletedAt == nil && online {
		return connect.Errorf(connect.CodeFailedPrecondition,
			"the bundled local worker is connected; deregister it only while it is offline")
	}
	return nil
}

// WorkerDeregisterEffects holds the three out-of-database effects that
// MUST follow every worker deregistration, whichever surface performed it.
//
// The store verb commits deleted_at immediately, revoking the worker's bearer
// token across every Hub endpoint. Without these effects an already-connected
// worker keeps its live stream until it reconnects, its memoized delegation
// scope keeps outstanding tokens reaching across workers until a cache TTL
// expires, and no owner's client learns the list changed. An offline worker
// (or one that misses the live instruction) receives WORKER_DELETED on its
// next reconnect during the soft-delete retention window and clears its local
// state then.
//
// The admin surfaces used to run none of the three, so a deregister typed
// by an administrator did strictly less than the same deregister typed by
// the worker's owner. One type carried by every caller is what removes
// that difference.
type WorkerDeregisterEffects struct {
	scopeCache           *auth.DelegationScopeCache
	notifier             *notifier.Notifier
	notifyWorkersChanged func(userID string)
}

// NewWorkerDeregisterEffects binds the three collaborators. Production
// passes all three; notifyWorkersChanged is the CRDT registry's
// BroadcastWorkersChanged. Each one is nil-tolerant so a test that exercises
// only the store half can pass nil, the same rule
// auth.CredentialLifecycleEffects holds.
func NewWorkerDeregisterEffects(scopeCache *auth.DelegationScopeCache, n *notifier.Notifier, notifyWorkersChanged func(userID string)) *WorkerDeregisterEffects {
	return &WorkerDeregisterEffects{scopeCache: scopeCache, notifier: n, notifyWorkersChanged: notifyWorkersChanged}
}

// Apply runs the three effects for one deregistered worker. ownerID is the
// worker's registrant, because the workers-changed notification is
// addressed to the OWNER, not to the administrator who typed the command.
//
// Callers MUST run it AFTER the deletion transaction commits: the owner's
// WorkerManagementService.DeregisterWorker, AdminWorkerService.DeregisterWorker,
// WorkerConnectorService.DeregisterSelf, the user-deletion cascade, and
// Server.RetireLocalWorker.
func (e *WorkerDeregisterEffects) Apply(ctx context.Context, workerID, ownerID string) {
	if e == nil {
		return
	}
	// Deregistration is the operator's containment action against a
	// compromised worker: evict its memoized delegation scope so
	// outstanding tokens minted on it lose their cross-worker reach on the
	// next SubmitOps, not a cache TTL later.
	if e.scopeCache != nil {
		e.scopeCache.EvictWorker(workerID)
	}

	if e.notifier != nil {
		e.notifier.SendDeregister(ctx, workerID)
	}

	if e.notifyWorkersChanged != nil {
		e.notifyWorkersChanged(ownerID)
	}
}
