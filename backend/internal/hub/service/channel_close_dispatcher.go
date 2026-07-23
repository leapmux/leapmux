package service

import (
	"log/slog"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/nilcheck"
)

const (
	channelCloseSenderLimit = 4
)

// workerConnRegistry is the narrow live-conn lookup the close dispatcher
// needs. ConnForTrustedPath performs no authorization, which is correct here:
// worker ids arrive from channelmgr teardown (a trusted server flow), never
// from a user-supplied id.
// Holding this interface instead of *workermgr.Manager keeps Register /
// the liveness probes / WaitFor* out of reach.
type workerConnRegistry interface {
	ConnForTrustedPath(string) *workermgr.Conn
}

type workerCloseDispatcher struct {
	workerMgr workerConnRegistry
	ready     []string

	mu      sync.Mutex
	pending map[string]*pendingWorkerCloses
	workers int
}

type pendingWorkerCloses struct {
	channelIDs []string
}

// newWorkerCloseDispatcher requires a registry. The narrow interface above
// cannot be nil-checked by its holder -- a nil *workermgr.Manager converted to
// it is a NON-nil interface value -- so an unchecked nil would surface as a nil
// receiver panic inside enqueueChannelCloses, on the caller's goroutine and
// outside runWorker's recover. Refusing at construction turns a wiring mistake
// into a startup failure that names it, rather than a teardown that dies on the
// first revocation.
func newWorkerCloseDispatcher(workerMgr workerConnRegistry) *workerCloseDispatcher {
	if nilcheck.IsNilDependency(workerMgr) {
		panic("service: worker close dispatcher requires a worker registry")
	}
	return &workerCloseDispatcher{
		workerMgr: workerMgr,
		pending:   make(map[string]*pendingWorkerCloses),
	}
}

// enqueueChannelCloses accepts close notifications without waiting for a worker
// stream. Pending closes are grouped by worker so one blocked stream consumes at
// most one sender. The ready queue is intentionally unbounded: revocation is a
// security boundary, so a burst must not silently leave worker-side channels
// alive. Only the sender goroutine count is bounded. The method name matches the
// channelCloseEnqueuer interface so this dispatcher satisfies it directly.
func (d *workerCloseDispatcher) enqueueChannelCloses(closed []channelmgr.ClosedChannel) {
	d.mu.Lock()
	// defer, not a trailing Unlock: this body calls out to the registry, and a
	// panic there with the lock held explicitly would wedge every subsequent
	// channel teardown rather than failing the one call.
	defer d.mu.Unlock()
	for _, cc := range closed {
		if cc.WorkerID == "" || cc.ChannelID == "" || d.workerMgr.ConnForTrustedPath(cc.WorkerID) == nil {
			continue
		}
		workerPending := d.pending[cc.WorkerID]
		if workerPending == nil {
			workerPending = &pendingWorkerCloses{}
			d.pending[cc.WorkerID] = workerPending
			d.ready = append(d.ready, cc.WorkerID)
		}
		workerPending.channelIDs = append(workerPending.channelIDs, cc.ChannelID)
	}
	d.startWorkersLocked()
}

func (d *workerCloseDispatcher) startWorkersLocked() {
	toStart := min(channelCloseSenderLimit-d.workers, len(d.ready))
	d.workers += toStart
	for range toStart {
		go d.runWorker()
	}
}

func (d *workerCloseDispatcher) runWorker() {
	for {
		d.mu.Lock()
		if len(d.ready) == 0 {
			d.workers--
			d.mu.Unlock()
			return
		}
		workerID := d.ready[0]
		if len(d.ready) == 1 {
			d.ready = nil
		} else {
			d.ready[0] = ""
			d.ready = d.ready[1:]
		}
		d.mu.Unlock()
		// runWorker is a detached goroutine (go d.runWorker()) with no caller to
		// propagate to: recover a panic in delivery so one bad close drops a single
		// notification instead of crashing the whole Hub, matching the sibling
		// channelmgr.fanOutTeardown defense. d.mu is released above and
		// deliverWorkerCloses's own locked sections are panic-free map/slice ops, so
		// recovery can never resume holding the lock; the loop then continues,
		// keeping this worker and its slot accounting intact. (The innermost
		// sendChannelCloseNotification also recovers conn.Send; this widens the
		// guard from that one call to the whole delivery body.)
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("recovered from panic in worker-close dispatch",
						"worker_id", workerID, "panic", r)
				}
			}()
			d.deliverWorkerCloses(workerID)
		}()
	}
}

func (d *workerCloseDispatcher) deliverWorkerCloses(workerID string) {
	d.mu.Lock()
	workerPending := d.pending[workerID]
	if workerPending == nil || len(workerPending.channelIDs) == 0 {
		d.mu.Unlock()
		return
	}
	channelIDs := workerPending.channelIDs
	workerPending.channelIDs = nil
	d.mu.Unlock()

	for _, channelID := range channelIDs {
		conn := d.workerMgr.ConnForTrustedPath(workerID)
		if conn == nil {
			continue
		}
		sendChannelCloseNotification(conn, channelID)
	}

	d.mu.Lock()
	if len(workerPending.channelIDs) == 0 {
		delete(d.pending, workerID)
	} else {
		d.ready = append(d.ready, workerID)
	}
	d.startWorkersLocked()
	d.mu.Unlock()
}

// sendChannelCloseNotification writes one channel-close notification to a
// worker, recovering from any panic. Conn.Send already fences on a closed flag
// -- a send racing worker disconnect returns ErrConnectionClosed rather than
// writing through a finished HTTP/2 response writer -- so the panic the old
// inline recover guarded against is structurally prevented. The recover remains
// as defense in depth because this runs on a detached dispatcher goroutine
// (runWorker) with no caller to propagate an error to: an unrecovered panic
// here would crash the whole Hub process instead of dropping one notification.
func sendChannelCloseNotification(conn *workermgr.Conn, channelID string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("recovered from panic sending channel close",
				"channel_id", channelID, "worker_id", conn.WorkerID, "panic", r)
		}
	}()
	_ = conn.Send(newChannelCloseResponse(channelID))
}

// newChannelCloseResponse builds the worker ConnectResponse that notifies a
// worker one channel is closed. Shared by the async dispatcher and the
// synchronous worker-relay teardown (closeWorkerChannel) so the close-envelope
// shape has a single source of truth.
func newChannelCloseResponse(channelID string) *leapmuxv1.ConnectResponse {
	return &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_ChannelClose{
			ChannelClose: &leapmuxv1.ChannelCloseNotification{ChannelId: channelID},
		},
	}
}
