package service

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/nilcheck"
)

// workerConnRegistry is the narrow live-conn lookup the close dispatcher
// needs. ConnForTrustedPath performs no authorization, which is correct here:
// worker ids arrive from channelmgr teardown (a trusted server flow), never
// from a user-supplied id.
// Holding this interface instead of *workermgr.Manager keeps registration
// and registry mutation out of reach.
type workerConnRegistry interface {
	ConnForTrustedPath(string) *workermgr.Conn
}

type workerCloseDispatcher struct {
	workerMgr workerConnRegistry
}

// newWorkerCloseDispatcher rejects nil, including a typed nil registry.
// Otherwise the first teardown would panic inside the registry lookup.
func newWorkerCloseDispatcher(workerMgr workerConnRegistry) *workerCloseDispatcher {
	if nilcheck.IsNilDependency(workerMgr) {
		panic("service: worker close dispatcher requires a worker registry")
	}
	return &workerCloseDispatcher{workerMgr: workerMgr}
}

// enqueueChannelCloses admits each close directly to its current connection's
// control queue. Transport writes remain on the connection's exclusive pump.
// A saturated connection fences itself; later workers still receive their closes.
func (d *workerCloseDispatcher) enqueueChannelCloses(closed []channelmgr.ClosedChannel) {
	for _, cc := range closed {
		if cc.WorkerID == "" || cc.ChannelID == "" {
			continue
		}
		conn := d.workerMgr.ConnForTrustedPath(cc.WorkerID)
		if conn != nil {
			sendChannelCloseNotification(conn, cc.ChannelID)
		}
	}
}

// sendChannelCloseNotification enqueues one channel-close control frame.
// The real write runs on the worker session's pump; this call is a pure
// hub-code enqueue that cannot panic through third-party transport code.
func sendChannelCloseNotification(conn *workermgr.Conn, channelID string) {
	if err := conn.SendControl(newChannelCloseResponse(channelID)); err != nil {
		slog.Warn("failed to enqueue channel close control frame",
			"channel_id", channelID, "worker_id", conn.WorkerID, "error", err)
	}
}

// newChannelCloseResponse builds the worker ConnectResponse that notifies a
// worker one channel is closed. Shared by direct close admission and the
// synchronous worker-relay teardown (closeWorkerChannel) so the close-envelope
// shape has a single source of truth.
func newChannelCloseResponse(channelID string) *leapmuxv1.ConnectResponse {
	return &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_ChannelClose{
			ChannelClose: &leapmuxv1.ChannelCloseNotification{ChannelId: channelID},
		},
	}
}
