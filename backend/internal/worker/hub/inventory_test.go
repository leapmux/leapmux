package hub

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/leapmux/leapmux/internal/sendq"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestConnect_InventoryFollowsFirstUsableIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, stream := writerStreamClient(t, nil, nil)
		var owner atomic.Bool
		var calls atomic.Int32
		c.OnWorkerIdentity = func(identity *leapmuxv1.WorkerIdentity) bool {
			if identity.GetRegisteredBy() == "" {
				assert.Zero(t, calls.Load())
				return false
			}
			owner.Store(true)
			return true
		}
		c.TabSyncProvider = func(context.Context) (*leapmuxv1.WorkerTabInventory, error) {
			calls.Add(1)
			if !owner.Load() {
				return nil, nil
			}
			return &leapmuxv1.WorkerTabInventory{}, nil
		}
		stream.messages = []*leapmuxv1.ConnectResponse{{Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{WorkerIdentity: &leapmuxv1.WorkerIdentity{}}}, inventoryIdentity(), inventoryIdentity()}
		close(stream.receiveOK)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- c.Connect(ctx, "token") }()
		synctest.Wait()
		stream.mu.Lock()
		sent := append([]*leapmuxv1.ConnectRequest(nil), stream.sent...)
		stream.mu.Unlock()
		assert.Equal(t, int32(1), calls.Load())
		require.Len(t, sent, 2, "the initial owner must precede exactly one report")
		assert.NotNil(t, sent[1].GetWorkerTabInventory())
		cancel()
		assert.ErrorIs(t, <-result, context.Canceled)
	})
}

func inventoryIdentity() *leapmuxv1.ConnectResponse {
	return &leapmuxv1.ConnectResponse{Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "owner"}}}
}

func TestConnect_InventorySegmentsPreserveEveryTab(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, stream := writerStreamClient(t, nil, nil)
		inventory := &leapmuxv1.WorkerTabInventory{}
		for i := range 850000 {
			inventory.Tabs = append(inventory.Tabs, &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: fmt.Sprintf("file-%032d", i)})
		}
		require.Greater(t, proto.Size(inventory), channelwire.WorkerStreamMaxMessageBytes)
		c.TabSyncProvider = func(context.Context) (*leapmuxv1.WorkerTabInventory, error) { return inventory, nil }
		stream.messages = []*leapmuxv1.ConnectResponse{inventoryIdentity()}
		close(stream.receiveOK)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- c.Connect(ctx, "token") }()
		synctest.Wait()
		stream.mu.Lock()
		sent := append([]*leapmuxv1.ConnectRequest(nil), stream.sent...)
		stream.mu.Unlock()
		count := 0
		for _, msg := range sent {
			if report := msg.GetWorkerTabInventory(); report != nil {
				assert.LessOrEqual(t, proto.Size(msg), channelwire.WorkerStreamMaxMessageBytes)
				assert.Equal(t, count+len(report.GetTabs()) == len(inventory.Tabs), report.GetComplete())
				for _, tab := range report.GetTabs() {
					require.True(t, proto.Equal(inventory.Tabs[count], tab))
					count++
				}
			}
		}
		assert.Equal(t, len(inventory.Tabs), count, "a large report must not be skipped or truncated")
		cancel()
		assert.ErrorIs(t, <-result, context.Canceled)
	})
}

func TestConnect_InventoryProducerStopsBeforeFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, stream := writerStreamClient(t, nil, nil)
		stream.messages = []*leapmuxv1.ConnectResponse{inventoryIdentity()}
		close(stream.receiveOK)
		var calls atomic.Int32
		exited := make(chan struct{})
		c.TabSyncProvider = func(ctx context.Context) (*leapmuxv1.WorkerTabInventory, error) {
			calls.Add(1)
			<-ctx.Done()
			close(exited)
			return &leapmuxv1.WorkerTabInventory{}, nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- c.Connect(ctx, "token") }()
		synctest.Wait()
		require.Equal(t, int32(1), calls.Load())
		c.StopTabSync()
		<-exited
		require.NoError(t, c.FlushSends(ShutdownFlushTimeout))
		stream.mu.Lock()
		assert.Len(t, stream.sent, 1, "no inventory may enter after the shutdown stop")
		stream.mu.Unlock()
		cancel()
		assert.ErrorIs(t, <-result, context.Canceled)
		// A later connection cannot reopen producer admission during shutdown.
		replacement := newFakeHubStream(nil, nil)
		replacement.messages = []*leapmuxv1.ConnectResponse{inventoryIdentity()}
		close(replacement.receiveOK)
		c.dialStream = func(context.Context, string) (hubStream, error) { return replacement, nil }
		next, nextCancel := context.WithCancel(t.Context())
		defer nextCancel()
		go func() { result <- c.Connect(next, "token") }()
		synctest.Wait()
		assert.Equal(t, int32(1), calls.Load())
		nextCancel()
		assert.ErrorIs(t, <-result, context.Canceled)
	})
}

func TestConnect_InventoryDisconnectJoinsProviderAndReconnects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, stream := writerStreamClient(t, nil, nil)
		stream.messages = []*leapmuxv1.ConnectResponse{inventoryIdentity()}
		close(stream.receiveOK)
		exited := make(chan struct{})
		c.TabSyncProvider = func(ctx context.Context) (*leapmuxv1.WorkerTabInventory, error) {
			<-ctx.Done()
			close(exited)
			return nil, ctx.Err()
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- c.Connect(ctx, "token") }()
		synctest.Wait()
		cancel()
		assert.ErrorIs(t, <-result, context.Canceled)
		<-exited
		replacement := newFakeHubStream(nil, nil)
		replacement.messages = []*leapmuxv1.ConnectResponse{inventoryIdentity()}
		close(replacement.receiveOK)
		c.dialStream = func(context.Context, string) (hubStream, error) { return replacement, nil }
		c.TabSyncProvider = func(context.Context) (*leapmuxv1.WorkerTabInventory, error) {
			return &leapmuxv1.WorkerTabInventory{}, nil
		}
		next, nextCancel := context.WithCancel(t.Context())
		defer nextCancel()
		go func() { result <- c.Connect(next, "token") }()
		synctest.Wait()
		replacement.mu.Lock()
		require.Len(t, replacement.sent, 2)
		assert.True(t, replacement.sent[1].GetWorkerTabInventory().GetComplete())
		replacement.mu.Unlock()
		nextCancel()
		assert.ErrorIs(t, <-result, context.Canceled)
	})
}

func TestInventorySegments_ImmutableAndRejectsMalformedEntries(t *testing.T) {
	tab := &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "file"}
	inventory := &leapmuxv1.WorkerTabInventory{Tabs: []*leapmuxv1.TabIdent{tab}}
	segments, err := inventorySegments(t.Context(), inventory)
	require.NoError(t, err)
	tab.TabId = "mutated"
	inventory.Tabs = nil
	assert.Equal(t, "file", segments[0].GetWorkerTabInventory().GetTabs()[0].GetTabId())
	for _, entry := range []*leapmuxv1.TabIdent{nil, {}, {TabId: "file", TabType: leapmuxv1.TabType(-1)}, {TabId: "file", TabType: leapmuxv1.TabType(999)}} {
		reports, err := inventorySegments(t.Context(), &leapmuxv1.WorkerTabInventory{Tabs: []*leapmuxv1.TabIdent{entry}})
		require.Error(t, err)
		assert.Nil(t, reports)
	}
}

func TestInventorySegments_StopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reports, err := inventorySegments(ctx, &leapmuxv1.WorkerTabInventory{Tabs: []*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "file"}}})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, reports)
}

func TestConnect_InventoryProviderDoesNotInventAnEmptyReport(t *testing.T) {
	for _, mode := range []string{"absent", "nil result", "failed"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, stream := writerStreamClient(t, nil, nil)
				if mode != "absent" {
					c.TabSyncProvider = func(context.Context) (*leapmuxv1.WorkerTabInventory, error) {
						if mode == "failed" {
							return nil, errors.New("query failed")
						}
						return nil, nil
					}
				}
				stream.messages = []*leapmuxv1.ConnectResponse{inventoryIdentity()}
				close(stream.receiveOK)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				result := make(chan error, 1)
				go func() { result <- c.Connect(ctx, "token") }()
				synctest.Wait()
				stream.mu.Lock()
				assert.Len(t, stream.sent, 1)
				stream.mu.Unlock()
				cancel()
				assert.ErrorIs(t, <-result, context.Canceled)
			})
		})
	}
}

func TestInventoryUpload_CancellationReleasesParkedQueueAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newBareClient()
		inventory := &leapmuxv1.WorkerTabInventory{}
		for i := range 300000 {
			inventory.Tabs = append(inventory.Tabs, &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: fmt.Sprintf("file-%032d", i)})
		}
		c.TabSyncProvider = func(context.Context) (*leapmuxv1.WorkerTabInventory, error) { return inventory, nil }
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started := make(chan struct{})
		writer := sendq.New(ctx, sendq.Config[*leapmuxv1.ConnectRequest]{Write: func(context.Context, *leapmuxv1.ConnectRequest) error { close(started); <-ctx.Done(); return ctx.Err() }, Size: func(m *leapmuxv1.ConnectRequest) int { return proto.Size(m) }, MaxBytes: channelwire.WorkerStreamMaxMessageBytes})
		defer writer.Close()
		upload := c.startTabSync(ctx, writer)
		require.NotNil(t, upload)
		<-started
		synctest.Wait()
		select {
		case <-upload.done:
			t.Fatal("producer did not wait for the full queue")
		default:
		}
		require.Positive(t, writer.QueuedBytes())
		// Changing the client's writer cannot redirect an admitted producer.
		var redirected atomic.Int32
		replacement := sendq.New(ctx, sendq.Config[*leapmuxv1.ConnectRequest]{Write: func(context.Context, *leapmuxv1.ConnectRequest) error { redirected.Add(1); return nil }, Size: func(m *leapmuxv1.ConnectRequest) int { return proto.Size(m) }, MaxBytes: sendq.DefaultMaxBytes})
		defer replacement.Close()
		c.mu.Lock()
		c.writer = replacement
		c.mu.Unlock()
		c.StopTabSync()
		<-upload.done
		assert.Zero(t, redirected.Load())
		cancel()
	})
}

// Cancellation at a later Err check injects the event during entry preparation.
type inventoryCancelContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *inventoryCancelContext) Err() error {
	c.checks++
	if c.checks == 3 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestInventorySegments_CancelsDuringPreparation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	controlled := &inventoryCancelContext{Context: ctx, cancel: cancel}
	inventory := &leapmuxv1.WorkerTabInventory{Tabs: []*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "a"}, {TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "b"}}}
	reports, err := inventorySegments(controlled, inventory)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, reports)
}
