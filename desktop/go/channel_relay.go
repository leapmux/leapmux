package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

// ChannelRelay bridges WebSocket channel relay traffic between the sidecar
// and the Tauri shell.
type ChannelRelay struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	emit   func(*desktoppb.Event)
}

func (a *App) OpenChannelRelay() error {
	if a.proxy == nil {
		return fmt.Errorf("not connected")
	}

	// Reuse an existing healthy relay. The sidecar persists across dev
	// refreshes, so the frontend's reconnect attempt would otherwise tear
	// down the hub-side binding and trigger a cleanup race that wipes
	// channels the freshly-loaded page is about to use.
	if a.relay != nil && a.relay.ctx.Err() == nil {
		return nil
	}

	a.closeChannelRelay()

	ctx, cancel := context.WithCancel(a.ctx)
	relay := &ChannelRelay{
		ctx:    ctx,
		cancel: cancel,
		emit:   a.EmitEvent,
	}

	wsURL := channelwire.HTTPToWS(a.proxy.baseURL) + "/ws/channel"
	opts := &websocket.DialOptions{
		Subprotocols: []string{"channel-relay"},
		HTTPHeader:   a.proxy.cookieHeader(),
	}
	if a.proxy.wsClient != nil {
		opts.HTTPClient = a.proxy.wsClient
	}

	ws, _, err := websocket.Dial(ctx, wsURL, opts)
	if err != nil {
		cancel()
		return fmt.Errorf("connect to channel relay: %w", err)
	}
	ws.SetReadLimit(channelwire.WSReadLimit)
	relay.ws = ws
	go relay.readLoop()

	a.relay = relay
	return nil
}

func (a *App) SendChannelMessage(data []byte) error {
	if a.relay == nil {
		return fmt.Errorf("channel relay not open")
	}

	a.relay.mu.Lock()
	defer a.relay.mu.Unlock()
	return a.relay.ws.Write(a.relay.ctx, websocket.MessageBinary, data)
}

func (a *App) CloseChannelRelay() error {
	a.closeChannelRelay()
	return nil
}

func (a *App) closeChannelRelay() {
	if a.relay == nil {
		return
	}
	a.relay.cancel()
	_ = a.relay.ws.Close(websocket.StatusNormalClosure, "")
	a.relay = nil
}

func (r *ChannelRelay) readLoop() {
	defer r.cancel()
	for {
		_, data, err := r.ws.Read(r.ctx)
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			slog.Debug("channel relay read error", "error", err)
			r.emit(&desktoppb.Event{
				Payload: &desktoppb.Event_ChannelClose{
					ChannelClose: &desktoppb.ChannelCloseEvent{},
				},
			})
			return
		}

		r.emit(&desktoppb.Event{
			Payload: &desktoppb.Event_ChannelMessage{
				ChannelMessage: &desktoppb.ChannelMessageEvent{
					Data: data,
				},
			},
		})
	}
}
