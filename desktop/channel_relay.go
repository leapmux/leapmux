package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"sync"

	"github.com/coder/websocket"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ChannelRelay bridges WebSocket channel relay traffic between the Wails
// frontend and the Hub. Binary data is base64-encoded at the boundary.
type ChannelRelay struct {
	ws       *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	wailsCtx context.Context
	mu       sync.Mutex
}

// OpenChannelRelay connects to the Hub's /ws/channel endpoint and starts
// relaying messages to the frontend via Wails events.
func (a *App) OpenChannelRelay(token string) error {
	if a.proxy == nil {
		return fmt.Errorf("not connected")
	}

	a.closeChannelRelay()

	ctx, cancel := context.WithCancel(a.ctx)
	relay := &ChannelRelay{
		ctx:      ctx,
		cancel:   cancel,
		wailsCtx: a.ctx,
	}

	// Build WebSocket URL and options.
	wsURL := a.proxy.baseURL + "/ws/channel"
	// Switch scheme for WebSocket.
	wsURL = httpToWS(wsURL)

	opts := &websocket.DialOptions{
		Subprotocols: []string{"channel-relay"},
	}

	// Add auth token for distributed mode.
	if token != "" {
		opts.Subprotocols = append(opts.Subprotocols, "auth.token."+token)
	}

	// Use the WebSocket-compatible HTTP client (HTTP/1.1 for upgrade).
	if a.proxy.wsClient != nil {
		opts.HTTPClient = a.proxy.wsClient
	}

	ws, _, err := websocket.Dial(ctx, wsURL, opts)
	if err != nil {
		cancel()
		return fmt.Errorf("connect to channel relay: %w", err)
	}
	ws.SetReadLimit(65535 + 4096) // Same as channelproto.WSReadLimit.
	relay.ws = ws

	// Start goroutine to read from WS and emit Wails events.
	go relay.readLoop()

	a.relay = relay
	return nil
}

// SendChannelMessage receives base64-encoded binary data from the frontend
// and forwards it to the Hub via the WebSocket.
func (a *App) SendChannelMessage(b64Data string) error {
	if a.relay == nil {
		return fmt.Errorf("channel relay not open")
	}

	data, err := base64.StdEncoding.DecodeString(b64Data)
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}

	a.relay.mu.Lock()
	defer a.relay.mu.Unlock()
	return a.relay.ws.Write(a.relay.ctx, websocket.MessageBinary, data)
}

// CloseChannelRelay tears down the channel relay.
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

// readLoop reads binary messages from the Hub WebSocket and emits them
// to the frontend as base64-encoded Wails events.
func (r *ChannelRelay) readLoop() {
	defer r.cancel()
	for {
		_, data, err := r.ws.Read(r.ctx)
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			slog.Debug("channel relay read error", "error", err)
			// Emit close event so frontend can reconnect.
			wailsRuntime.EventsEmit(r.wailsCtx, "channel:close")
			return
		}
		b64 := base64.StdEncoding.EncodeToString(data)
		wailsRuntime.EventsEmit(r.wailsCtx, "channel:message", b64)
	}
}

// httpToWS converts an http(s) URL to ws(s).
func httpToWS(url string) string {
	if len(url) >= 8 && url[:8] == "https://" {
		return "wss://" + url[8:]
	}
	if len(url) >= 7 && url[:7] == "http://" {
		return "ws://" + url[7:]
	}
	return url
}

