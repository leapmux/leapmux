// Package notifier delivers the DEREGISTER instruction to a worker.
//
// It keeps no queue. The persisted deregistration intent is the
// workers.deregistering_at column, written by the deregister transaction
// before the notifier runs; the notifier only tells a CONNECTED worker to stop
// and, on its acknowledgement, deletes the worker. A worker that is offline is
// told at its next connect (DeliverOnConnect), and the cleanup loop deletes a
// worker still deregistering after its grace period
// (store.CleanupStore.CompleteStaleDeregistrations), so every failure of this
// package's delivery is recovered from the persisted state.
package notifier

import (
	"context"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/nilcheck"
)

// workerRegistry is the narrow surface Notifier needs from the live-worker
// map. ConnForTrustedPath performs no authorization, which is correct here:
// every worker id the notifier passes came from an authorized store row or a
// trusted server flow (deregister, connect), never from a user request.
// Holding this interface instead of *workermgr.Manager structurally prevents
// reaching Register / WaitFor* / the liveness probes.
type workerRegistry interface {
	ConnForTrustedPath(string) *workermgr.Conn
	MarkDeregistering(string)
	ClearDeregistering(string)
}

// requestSender sends one request to a connected worker and waits for the
// response that carries the same request id. *workermgr.PendingRequests is the
// production implementation; a test substitutes its own.
type requestSender interface {
	SendAndWait(ctx context.Context, conn *workermgr.Conn, msg *leapmuxv1.ConnectResponse) (*leapmuxv1.ConnectRequest, error)
}

// Notifier tells a deregistering worker to stop, and deletes it once the
// worker acknowledges. See the package doc for how a failed delivery is
// recovered.
type Notifier struct {
	store          store.Store
	workerMgr      workerRegistry
	sender         requestSender
	apiTimeout     func() time.Duration
	workersChanged func(ownerID string)
}

// New creates a new Notifier. wMgr is taken as the narrow workerRegistry
// interface rather than *workermgr.Manager -- matching newWorkerCloseDispatcher
// -- so the narrowing constrains callers too, not just this package's body: a
// caller cannot hand the notifier Register / WaitFor* / the liveness probes,
// and a test can substitute a fake registry through the public constructor.
//
// The narrowing also destroys the caller's ability to spot a nil registry: a
// nil *workermgr.Manager converted to this interface is a NON-nil interface
// holding a nil pointer, so `wMgr != nil` would not catch it. Left unchecked,
// the first ConnForTrustedPath call panics on a nil receiver -- and it happens
// on the delivery goroutine, which has no recover, so a wiring mistake takes
// the hub process down long after startup instead of at it. nilcheck sees
// through the conversion, and the sender gets the same check for the same
// reason.
//
// workersChanged is called with the worker's owner after a deletion is
// recorded, so the owner's clients re-read their worker list. A nil
// workersChanged is a no-op.
func New(st store.Store, wMgr workerRegistry, sender requestSender, apiTimeout func() time.Duration, workersChanged func(ownerID string)) *Notifier {
	if nilcheck.IsNilDependency(wMgr) {
		panic("notifier: New requires a worker registry")
	}
	if nilcheck.IsNilDependency(sender) {
		panic("notifier: New requires a request sender")
	}
	return &Notifier{
		store:          st,
		workerMgr:      wMgr,
		sender:         sender,
		apiTimeout:     apiTimeout,
		workersChanged: workersChanged,
	}
}

// SendDeregister tells a worker to stop. The caller MUST call it after the
// durable change committed: deregistering_at for a deregistration, or
// deleted_at for a worker deleted with its user.
//
// It marks the worker deregistering in the live registry, so its user can no
// longer reach it, and then:
//   - an offline worker that is already deleted needs nothing more, so its
//     mark is cleared;
//   - an offline deregistering worker keeps its mark, and is told at its next
//     connect;
//   - a connected worker is told now, and deleted on its acknowledgement (see
//     deliver).
//
// A failed read of the worker is returned. The durable change already
// committed, so the next connect or the cleanup sweep still recovers it.
func (n *Notifier) SendDeregister(ctx context.Context, workerID string) error {
	n.workerMgr.MarkDeregistering(workerID)
	worker, err := n.store.Workers().GetByIDIncludeDeleted(ctx, workerID)
	if err != nil {
		return err
	}
	conn := n.workerMgr.ConnForTrustedPath(workerID)
	if conn == nil {
		if worker.DeletedAt != nil {
			n.workerMgr.ClearDeregistering(workerID)
		}
		return nil
	}
	n.deliver(ctx, worker, conn)
	return nil
}

// DeliverOnConnect tells a just-connected worker to stop when it is
// DEREGISTERING, or when it was deleted with its user while it connected. The
// caller reads the worker after it registers the connection. It also
// re-establishes the in-memory deregistering mark, which a hub restart loses
// while deregistering_at survives.
func (n *Notifier) DeliverOnConnect(ctx context.Context, worker *store.Worker) {
	n.workerMgr.MarkDeregistering(worker.ID)
	conn := n.workerMgr.ConnForTrustedPath(worker.ID)
	if conn == nil {
		// The connection ended before delivery started; the next connect or
		// the cleanup sweep recovers it.
		slog.Warn("deregistering worker disconnected before it was told to stop", "worker_id", worker.ID)
		return
	}
	n.deliver(ctx, worker, conn)
}

// deliver is the one delivery routine: send the DEREGISTER instruction to a
// connected worker, wait for its acknowledgement, and record the deletion.
func (n *Notifier) deliver(ctx context.Context, worker *store.Worker, conn *workermgr.Conn) {
	sendCtx, cancel := context.WithTimeout(ctx, n.apiTimeout())
	resp, err := n.sender.SendAndWait(sendCtx, conn, deregisterMessage())
	cancel()

	if worker.DeletedAt != nil {
		// The worker was deleted with its user: there is no deletion left to
		// record, and nothing to wait for whatever the send's result was.
		n.workerMgr.ClearDeregistering(worker.ID)
		return
	}
	// SendAndWait matches a response by request id alone, so a response with
	// a different payload is not an acknowledgement.
	if err != nil || resp.GetDeregisterAck() == nil {
		slog.Warn("worker did not acknowledge DEREGISTER; it is told again at its next connect, and deleted by the cleanup sweep after the grace period",
			"worker_id", worker.ID, "error", err)
		return
	}

	// The worker deletes its own registration and exits right after it
	// acknowledges, so its connection context ends at once. The deletion is
	// recorded in a hub-owned context, so that cannot cancel the only attempt.
	completeCtx, cancelComplete := context.WithTimeout(context.WithoutCancel(ctx), n.apiTimeout())
	defer cancelComplete()
	if err := n.store.Workers().MarkDeleted(completeCtx, worker.ID); err != nil {
		slog.Warn("worker acknowledged DEREGISTER but its deletion failed; the cleanup sweep deletes it after the grace period",
			"worker_id", worker.ID, "error", err)
		return
	}
	n.workerMgr.ClearDeregistering(worker.ID)
	if n.workersChanged != nil {
		n.workersChanged(worker.RegisteredBy)
	}
	slog.Info("worker deregistration complete", "worker_id", worker.ID)
}

// deregisterMessage is the fixed DEREGISTER instruction.
func deregisterMessage() *leapmuxv1.ConnectResponse {
	return &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_Deregister{
			Deregister: &leapmuxv1.DeregisterNotification{
				Reason: "worker deregistered by owner",
			},
		},
	}
}
