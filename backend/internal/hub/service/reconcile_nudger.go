package service

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

// ReconcileNudger tells a worker to run its orphan reconciler now, over the
// Connect stream it already holds. It implements crdt.ReconcileNudger.
//
// Why this exists: closing a tab is a CRDT op, and the worker learns of it
// either through the E2EE close RPC or -- when that never arrives -- through its
// reconciler, which otherwise runs hourly and is triggered only by the worker's
// own reconnect handshake. That leaves a close whose RPC failed against a
// still-connected worker, and any CRDT-only close (a peer client, `leapmux
// remote tab close`), converging up to an hour later, with the agent subprocess
// still running the whole time.
type ReconcileNudger struct {
	workers *workermgr.Manager
	logger  *slog.Logger
}

// NewReconcileNudger returns a nudger bound to the worker-connection manager.
func NewReconcileNudger(workers *workermgr.Manager, logger *slog.Logger) *ReconcileNudger {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReconcileNudger{workers: workers, logger: logger}
}

// NudgeReconcile sends an unsolicited ReconcileNudge to workerID, if it is
// connected. Best-effort and non-blocking by contract: an offline worker, a
// closed stream, or a send error all resolve to "not nudged", and the worker's
// own tick or reconnect still converges it.
//
// Uses ConnForTrustedPath, not ConnForUser: workerID came from the CRDT
// projection of the batch's own owner, not from a user request, so there is no
// user-supplied id to authorize and no liveness oracle to leak. The signature
// carries no principal for the same reason -- see crdt.ReconcileNudger.
func (n *ReconcileNudger) NudgeReconcile(workerID string) {
	if n == nil || n.workers == nil || workerID == "" {
		return
	}
	conn := n.workers.ConnForTrustedPath(workerID)
	if conn == nil {
		return
	}
	err := conn.SendControl(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_ReconcileNudge{
			ReconcileNudge: &leapmuxv1.ReconcileNudge{},
		},
	})
	if err != nil {
		// Debug, not Warn: a racing disconnect is the ordinary case here, and
		// nothing is lost -- the reconciler converges regardless.
		n.logger.Debug("reconcile nudge not delivered", "worker_id", workerID, "error", err)
	}
}
