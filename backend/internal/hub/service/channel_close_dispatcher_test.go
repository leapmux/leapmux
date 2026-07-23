package service

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestWorkerCloseDispatcher_DoesNotDropLargeBatch(t *testing.T) {
	workerMgr := workermgr.New(workermgr.DenyAllReach())
	var mu sync.Mutex
	received := make(map[string]struct{})
	_, _ = workerMgr.Register(&workermgr.Conn{
		WorkerID: "worker",
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			mu.Lock()
			received[msg.GetChannelClose().GetChannelId()] = struct{}{}
			mu.Unlock()
			return nil
		},
	})
	dispatcher := newWorkerCloseDispatcher(workerMgr)
	closed := make([]channelmgr.ClosedChannel, 300)
	for i := range closed {
		closed[i] = channelmgr.ClosedChannel{
			ChannelID: fmt.Sprintf("channel-%d", i),
			WorkerID:  "worker",
		}
	}

	dispatcher.enqueueChannelCloses(closed)

	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) == len(closed)
	})
	mu.Lock()
	assert.Len(t, received, len(closed))
	mu.Unlock()
}

// A panic in conn.Send must not propagate: deliverWorkerCloses runs on a
// detached dispatcher goroutine where an unrecovered panic would crash the
// whole Hub process rather than drop one close notification.
func TestSendChannelCloseNotification_RecoversFromPanic(t *testing.T) {
	conn := &workermgr.Conn{
		WorkerID: "worker",
		SendFn: func(*leapmuxv1.ConnectResponse) error {
			panic("worker stream already finished")
		},
	}
	assert.NotPanics(t, func() {
		sendChannelCloseNotification(conn, "ch-1")
	})
}
