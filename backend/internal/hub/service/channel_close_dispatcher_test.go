package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/sendq"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestWorkerCloseDispatcher_DoesNotDropLargeBatch(t *testing.T) {
	t.Parallel()

	workerMgr := workermgr.New(workermgr.DenyAllReach())
	var mu sync.Mutex
	received := make(map[string]struct{})
	conn := workermgrtest.NewConnWithWrite(t, "worker", func(msg *leapmuxv1.ConnectResponse) error {
		mu.Lock()
		received[msg.GetChannelClose().GetChannelId()] = struct{}{}
		mu.Unlock()
		return nil
	})
	_, _ = workerMgr.Register(conn)
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

type closeRegistryFunc func(string) *workermgr.Conn

func (f closeRegistryFunc) ConnForTrustedPath(workerID string) *workermgr.Conn { return f(workerID) }

func TestWorkerCloseDispatcher_LooksUpEachValidCloseOnce(t *testing.T) {
	t.Parallel()
	delivered := make(chan string, 2)
	first := workermgrtest.NewConnWithWrite(t, "worker", func(m *leapmuxv1.ConnectResponse) error {
		delivered <- "first:" + m.GetChannelClose().GetChannelId()
		return nil
	})
	replacement := workermgrtest.NewConnWithWrite(t, "worker", func(m *leapmuxv1.ConnectResponse) error {
		delivered <- "replacement:" + m.GetChannelClose().GetChannelId()
		return nil
	})
	calls := 0
	dispatcher := newWorkerCloseDispatcher(closeRegistryFunc(func(worker string) *workermgr.Conn {
		calls++
		if worker == "offline" {
			return nil
		}
		if calls == 1 {
			return first
		}
		return replacement
	}))
	dispatcher.enqueueChannelCloses([]channelmgr.ClosedChannel{{WorkerID: "", ChannelID: "ignored"}, {WorkerID: "worker"}, {WorkerID: "worker", ChannelID: "a"}, {WorkerID: "worker", ChannelID: "b"}, {WorkerID: "offline", ChannelID: "c"}})
	got := []string{requireCloseDelivery(t, delivered), requireCloseDelivery(t, delivered)}
	assert.ElementsMatch(t, []string{"first:a", "replacement:b"}, got)
	assert.Equal(t, 3, calls, "blank identities must not look up a connection; each valid identity must look it up once")
}

func requireCloseDelivery(t *testing.T, delivered <-chan string) string {
	t.Helper()
	select {
	case value := <-delivered:
		return value
	case <-time.After(30 * time.Second):
		t.Fatal("close was not delivered")
		return ""
	}
}

func TestWorkerCloseDispatcher_SaturationDoesNotStopHealthyWorkers(t *testing.T) {
	_ = testutil.CaptureDefaultLogger(t)
	const capacity = sendq.DefaultControlReserve + 1024
	pool := sendq.NewPool(sendq.PoolConfig{Capacity: capacity, MinFloor: capacity, MaxFloor: capacity})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	saturated, _ := workermgr.NewConn(ctx, cancel, "saturated", "", pool, func(context.Context, *leapmuxv1.ConnectResponse) error {
		t.Error("an oversized close reached the writer")
		return nil
	}, nil, workermgrtest.TestHello())
	t.Cleanup(saturated.Fence)
	delivered := make(chan string, 1)
	healthy := workermgrtest.NewConnWithWrite(t, "healthy", func(m *leapmuxv1.ConnectResponse) error { delivered <- m.GetChannelClose().GetChannelId(); return nil })
	dispatcher := newWorkerCloseDispatcher(closeRegistryFunc(func(worker string) *workermgr.Conn {
		if worker == "saturated" {
			return saturated
		}
		return healthy
	}))
	dispatcher.enqueueChannelCloses([]channelmgr.ClosedChannel{{WorkerID: "saturated", ChannelID: strings.Repeat("x", int(capacity)+1)}, {WorkerID: "saturated", ChannelID: "already-fenced"}, {WorkerID: "healthy", ChannelID: "survives"}})
	select {
	case <-saturated.Done():
	default:
		t.Fatal("oversized control admission did not fence the connection")
	}
	assert.Equal(t, "survives", requireCloseDelivery(t, delivered))
}

func TestWorkerCloseDispatcher_ConcurrentBatches(t *testing.T) {
	t.Parallel()
	var count atomic.Int32
	conn := workermgrtest.NewConnWithWrite(t, "worker", func(*leapmuxv1.ConnectResponse) error { count.Add(1); return nil })
	dispatcher := newWorkerCloseDispatcher(closeRegistryFunc(func(string) *workermgr.Conn { return conn }))
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			dispatcher.enqueueChannelCloses([]channelmgr.ClosedChannel{{WorkerID: "worker", ChannelID: fmt.Sprint(i)}})
		})
	}
	wg.Wait()
	testutil.AssertEventually(t, func() bool { return count.Load() == 100 })
}
