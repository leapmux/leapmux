package hub

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/sendq"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// inventoryUpload belongs to one connection and captures its writer.
// Neither a reconnect nor a shutdown can redirect its reports to a new writer.
type inventoryUpload struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (u *inventoryUpload) stop() { u.cancel(); <-u.done }

// StopTabSync closes upload admission and joins the current producer before
// shutdown flushes the outbound queue. It is safe to call more than once.
func (c *Client) StopTabSync() {
	c.mu.Lock()
	c.tabSyncStopped = true
	u := c.tabSync
	c.mu.Unlock()
	if u != nil {
		u.stop()
	}
}

func (c *Client) startTabSync(parent context.Context, writer *sendq.Writer[*leapmuxv1.ConnectRequest]) *inventoryUpload {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tabSyncStopped || c.TabSyncProvider == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	u := &inventoryUpload{cancel: cancel, done: make(chan struct{})}
	c.tabSync = u
	go func() {
		defer close(u.done)
		inventory, err := c.TabSyncProvider(ctx)
		if err == nil && inventory != nil {
			var segments []*leapmuxv1.ConnectRequest
			segments, err = inventorySegments(ctx, inventory)
			if err == nil {
				for _, segment := range segments {
					if ctx.Err() != nil {
						err = ctx.Err()
						break
					}
					if err = writer.EnqueueWait(ctx, segment); err != nil {
						break
					}
				}
			}
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("failed to send worker tab inventory; Hub reconciliation remains incomplete", "error", err)
		}
	}()
	return u
}

// inventorySegments checks every entry before enqueueing any report. Each
// request owns its entries and slice, so queue consumers see immutable objects.
// Reserve the completion field in every size calculation; only the final
// request sets it. Include the ConnectRequest envelope in the wire limit.
func inventorySegments(ctx context.Context, inventory *leapmuxv1.WorkerTabInventory) ([]*leapmuxv1.ConnectRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot := &leapmuxv1.WorkerTabInventory{}

	size := func(entries int) int { return protowire.SizeTag(12) + protowire.SizeBytes(entries+2) }
	for _, tab := range inventory.GetTabs() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if tab == nil || tab.GetTabId() == "" || tab.GetTabType() < leapmuxv1.TabType_TAB_TYPE_AGENT || tab.GetTabType() > leapmuxv1.TabType_TAB_TYPE_IMAGE {
			return nil, fmt.Errorf("invalid worker tab inventory entry")
		}
		if size(protowire.SizeTag(1)+protowire.SizeBytes(proto.Size(tab))) > channelwire.WorkerStreamMaxMessageBytes {
			return nil, fmt.Errorf("worker tab inventory entry exceeds the %d-byte message limit", channelwire.WorkerStreamMaxMessageBytes)
		}
		snapshot.Tabs = append(snapshot.Tabs, proto.CloneOf(tab))
	}
	var reports []*leapmuxv1.ConnectRequest
	var tabs []*leapmuxv1.TabIdent
	entries := 0
	appendReport := func(complete bool) {
		reports = append(reports, &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: &leapmuxv1.WorkerTabInventory{Tabs: tabs, Complete: complete}}})
		tabs = nil
		entries = 0
	}
	for _, tab := range snapshot.GetTabs() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := protowire.SizeTag(1) + protowire.SizeBytes(proto.Size(tab))
		if size(entries+n) > channelwire.WorkerStreamMaxMessageBytes {
			appendReport(false)
		}
		tabs = append(tabs, tab)
		entries += n
	}
	appendReport(true)
	return reports, nil
}
