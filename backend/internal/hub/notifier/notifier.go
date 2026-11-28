// Package notifier delivers the DEREGISTER instruction to a connected worker.
//
// It keeps no queue and writes nothing to the store. Every removal surface
// commits workers.deleted_at before calling SendDeregister, revoking the
// worker's bearer token immediately across every Hub endpoint. This package
// only contains and stops an already-connected stream; an offline worker (or
// one that misses an in-flight instruction) is refused on reconnect with
// WORKER_DELETED during the soft-delete retention window and clears its local
// state then.
package notifier

import (
	"context"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
}

// requestSender sends one request to a connected worker and waits for the
// response that carries the same request id. *workermgr.PendingRequests is the
// production implementation; a test substitutes its own.
type requestSender interface {
	SendAndWait(ctx context.Context, conn *workermgr.Conn, msg *leapmuxv1.ConnectResponse) (*leapmuxv1.ConnectRequest, error)
}

// Notifier tells a connected worker to stop after its deletion has committed.
type Notifier struct {
	workerMgr  workerRegistry
	sender     requestSender
	apiTimeout func() time.Duration
}

// New creates a new Notifier. wMgr is taken as the narrow workerRegistry
// interface rather than *workermgr.Manager -- matching newWorkerCloseDispatcher
// -- so the narrowing constrains callers too, not just this package's body: a
// caller cannot hand the notifier Register / WaitFor* / the liveness probes,
// and a test can substitute a fake registry through the public constructor.
//
// nilcheck catches a typed nil pointer wrapped in an interface so wiring
// mistakes fail at construction instead of panicking on a delivery goroutine.
func New(wMgr workerRegistry, sender requestSender, apiTimeout func() time.Duration) *Notifier {
	if nilcheck.IsNilDependency(wMgr) {
		panic("notifier: New requires a worker registry")
	}
	if nilcheck.IsNilDependency(sender) {
		panic("notifier: New requires a request sender")
	}
	return &Notifier{
		workerMgr:  wMgr,
		sender:     sender,
		apiTimeout: apiTimeout,
	}
}

// SendDeregister contains a connected worker and tells it to stop. Callers
// MUST invoke it after workers.deleted_at has committed (via
// WorkerDeregisterEffects.Apply). An offline worker needs no live marker: its
// next reconnect receives WORKER_DELETED from /ws/worker.
func (n *Notifier) SendDeregister(ctx context.Context, workerID string) {
	n.workerMgr.MarkDeregistering(workerID)
	conn := n.workerMgr.ConnForTrustedPath(workerID)
	if conn == nil {
		return
	}
	n.deliver(ctx, workerID, conn)
}

// DeliverOnConnect tells a just-connected worker to stop when its row was
// deleted between bearer authentication and stream registration. The caller
// reads the worker after registering conn and passes that same conn so
// delivery never selects a replacement connection belonging to another handler.
func (n *Notifier) DeliverOnConnect(ctx context.Context, workerID string, conn *workermgr.Conn) {
	if conn == nil {
		return
	}
	n.workerMgr.MarkDeregistering(workerID)
	n.deliver(ctx, workerID, conn)
}

// deliver sends the DEREGISTER instruction to a connected worker, waits up to
// apiTimeout for its acknowledgement, and fences the connection.
func (n *Notifier) deliver(ctx context.Context, workerID string, conn *workermgr.Conn) {
	defer conn.Fence()
	sendCtx, cancel := context.WithTimeout(ctx, n.apiTimeout())
	resp, err := n.sender.SendAndWait(sendCtx, conn, deregisterMessage())
	cancel()

	// SendAndWait matches a response by request id alone, so a response with
	// a different payload is not an acknowledgement.
	if err != nil || resp.GetDeregisterAck() == nil {
		slog.Warn("worker did not acknowledge DEREGISTER; the hub will close the stream and refuse reconnects with WORKER_DELETED",
			"worker_id", workerID, "error", err)
		return
	}
	slog.Info("worker acknowledged DEREGISTER", "worker_id", workerID)
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
