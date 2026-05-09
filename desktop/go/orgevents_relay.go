package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

// OrgEventsRelay bridges the per-org `/ws/orgevents` WebSocket between
// the sidecar's HubProxy (which can dial the unix-socket hub) and the
// Tauri shell. The webview cannot open a native WebSocket to a unix
// socket; without this relay the frontend's `useOrgEvents` hook sees
// zero frames in desktop solo mode and `seedTabIntoNewWorkspace` /
// `awaitWorkspaceBootstrap` time out indefinitely on every workspace
// the user creates after the session bootstrap.
//
// Mirrors `ChannelRelay` for /ws/channel but is read-only — the
// frontend never writes to /ws/orgevents, so there's no Send method.
type OrgEventsRelay struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	emit   func(*desktoppb.Event)
}

// OpenOrgEventsRelay dials the per-org WebSocket through the
// sidecar's HubProxy and starts a read loop that forwards every
// inbound frame to the Tauri shell as an OrgEventsMessageEvent.
// `workspaceIds` is forwarded verbatim — empty means "every workspace
// I can read", non-empty narrows the filter at the hub.
//
// Always closes any existing relay first and opens a fresh one. The
// hub sends `OrgMaterialized` only at subscribe time — if we reused
// a still-live relay across a webview refresh, the freshly-loaded
// page's event listeners would never see the initial bootstrap and
// `awaitWorkspaceBootstrap` would hang for 30s before timing out.
// (`channel_relay.go` keeps its relay across refreshes because its
// subscribers are addressed by channel_id and the hub re-sends per
// channel_id; OrgEvents has a single per-org subscription with a
// one-shot initial frame, so the same trick doesn't apply.)
func (a *App) OpenOrgEventsRelay(orgID string, workspaceIDs []string) error {
	if a.proxy == nil {
		return fmt.Errorf("not connected")
	}
	if orgID == "" {
		return fmt.Errorf("orgID required")
	}

	// Force-restart: any prior relay tears down before we dial again.
	a.closeOrgEventsRelay()

	wsURL := channelwire.OrgEventsURL(a.proxy.baseURL, orgID, workspaceIDs)
	opts := &websocket.DialOptions{
		Subprotocols: []string{"orgevents-relay"},
		HTTPHeader:   a.proxy.cookieHeader(),
	}
	if a.proxy.wsClient != nil {
		opts.HTTPClient = a.proxy.wsClient
	}

	ctx, cancel := context.WithCancel(a.ctx)
	ws, _, err := websocket.Dial(ctx, wsURL, opts)
	if err != nil {
		cancel()
		return fmt.Errorf("connect to org events relay: %w", err)
	}
	// Per-message read limit mirrors the hub-side `SetReadLimit(16 MiB)`
	// in ws_orgevents.go; bootstrap frames carrying the full
	// OrgMaterialized can be large.
	ws.SetReadLimit(16 * 1024 * 1024)
	relay := &OrgEventsRelay{
		ws:     ws,
		ctx:    ctx,
		cancel: cancel,
		emit:   a.EmitEvent,
	}
	go relay.readLoop()

	a.orgEventsRelay = relay
	return nil
}

func (a *App) CloseOrgEventsRelay() error {
	a.closeOrgEventsRelay()
	return nil
}

func (a *App) closeOrgEventsRelay() {
	if a.orgEventsRelay == nil {
		return
	}
	a.orgEventsRelay.cancel()
	_ = a.orgEventsRelay.ws.Close(websocket.StatusNormalClosure, "")
	a.orgEventsRelay = nil
}

func (r *OrgEventsRelay) readLoop() {
	defer r.cancel()
	// stripPrefix=false: the hub frames each message as length-prefixed
	// WatchOrgEvent proto bytes; we forward the raw WS frame verbatim
	// so the frontend's existing length-prefix parser stays unchanged.
	err := channelwire.RunOrgEventsReadLoop(r.ctx, r.ws, false, func(data []byte) error {
		r.emit(&desktoppb.Event{
			Payload: &desktoppb.Event_OrgEventsMessage{
				OrgEventsMessage: &desktoppb.OrgEventsMessageEvent{
					Data: data,
				},
			},
		})
		return nil
	})
	if err != nil && r.ctx.Err() == nil {
		slog.Debug("org events relay read error", "error", err)
	}
	r.emit(&desktoppb.Event{
		Payload: &desktoppb.Event_OrgEventsClose{
			OrgEventsClose: &desktoppb.OrgEventsCloseEvent{},
		},
	})
}
