package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// dialUserEvents opens the userevents WebSocket. A package var (mirroring
// App.startSolo and TunnelManager.openCh) so tests can hold a dial open and drive
// the concurrent-open fence below deterministically, which is otherwise a race no
// test could pin down.
var dialUserEvents = func(ctx context.Context, proxy *HubProxy, workspaceIDs []string, cursor *leapmuxv1.HLC, epoch int64) (*websocket.Conn, error) {
	// Fail closed on a missing WS client (see HubProxy.requireWSClient): a nil
	// wsClient makes OpenUserEventsWSWithHeader fall back to http.DefaultClient,
	// which carries neither the cookie jar nor pinRedirectsToOrigin and would
	// reopen the hub-side off-origin 3xx redirect-escape the pin closes.
	if err := proxy.requireWSClient("userevents relay"); err != nil {
		return nil, err
	}
	return channelwire.OpenUserEventsWSWithHeader(
		ctx, proxy.wsClient, proxy.baseURL, proxy.cookieHeader(), workspaceIDs, cursor, epoch,
	)
}

// UserEventsRelay bridges the per-user `/ws/userevents` WebSocket between
// the sidecar's HubProxy (which can dial the unix-socket hub) and the
// Tauri shell. The webview cannot open a native WebSocket to a unix
// socket; without this relay the frontend's `useUserEvents` hook sees
// zero frames in desktop solo mode and `seedTabIntoNewWorkspace` /
// `awaitWorkspaceBootstrap` time out indefinitely on every workspace
// the user creates after the session bootstrap.
//
// Mirrors `ChannelRelay` for /ws/channel but is read-only — the
// frontend never writes to /ws/userevents, so there's no Send method.
type UserEventsRelay struct {
	wsRelay
}

// OpenUserEventsRelay dials the per-user WebSocket through the
// sidecar's HubProxy and starts a read loop that forwards every
// inbound frame to the Tauri shell as an UserEventsMessageEvent.
// `workspaceIds` is forwarded verbatim — empty means "every workspace
// I can read", non-empty narrows the filter at the hub.
//
// Always closes any existing relay first and opens a fresh one. The
// hub sends `UserMaterialized` only at subscribe time — if we reused
// a still-live relay across a webview refresh, the freshly-loaded
// page's event listeners would never see the initial bootstrap and
// `awaitWorkspaceBootstrap` would hang for 30s before timing out.
// (`channel_relay.go` keeps its relay across refreshes because its
// subscribers are addressed by channel_id and the hub re-sends per
// channel_id; UserEvents has a single per-user subscription with a
// one-shot initial frame, so the same trick doesn't apply.)
//
// `relayID` names the frontend wrapper opening the relay (see
// wsRelay.owner). Because this open force-restarts rather than
// adopts, the id also has to ORDER the opens: RPCSession runs every
// request on its own goroutine, so an open dispatched earlier can
// execute later, and restarting over a newer relay would tear down
// the one the page is actually listening on -- silently, since the
// teardown cancels the relay context before its read loop can emit
// an userevents:close. So a stale open abandons itself instead:
// last open dispatched wins, whatever order the sidecar runs them in.
func (a *App) OpenUserEventsRelay(requestCtx context.Context, relayID uint64, workspaceIDs []string, cursor *leapmuxv1.HLC, epoch int64) error {
	return a.openRelay(requestCtx, relayOpenSpec{
		// Unlike the channel relay's adopt policy, a stale open here refuses
		// itself outright -- before the dial (an open that ran entirely late
		// must not even tear the successor down) and again at install (a newer
		// open may have installed its relay while we dialed) -- see
		// rejectIfSuperseded.
		policy: func(connection *desktopConnection) (handled bool, err error) {
			return false, a.rejectIfSuperseded(connection, relayID)
		},
		// Force-restart: any prior relay is detached (without draining under the
		// lock) before we dial again.
		closePrior: func() { _ = a.closeUserEventsRelay() },
		dial: func(dialCtx context.Context, proxy *HubProxy) (*websocket.Conn, error) {
			ws, err := dialUserEvents(dialCtx, proxy, workspaceIDs, cursor, epoch)
			if err != nil {
				return nil, fmt.Errorf("connect to userevents relay: %w", err)
			}
			return ws, nil
		},
		commit: func(connection *desktopConnection, ws *websocket.Conn, ctx context.Context, cancel context.CancelFunc) {
			relay := &UserEventsRelay{
				wsRelay: newWSRelay(ws, ctx, cancel, a.EmitEvent),
			}
			// Stamped before the relay is installed, so no close can ever observe it unowned.
			relay.stampOwner(relayID)
			// Route the read loop's emits through the relay-aware sink so an
			// undeliverable frame carries this relay's owner id forward to the close
			// path (mirrors the channel relay's commit closure).
			relay.emit = a.emitForOwner(&relay.wsRelay)
			go relay.runReadLoop()
			connection.userEventsRelay = relay
		},
	})
}

// rejectIfSuperseded reports an error when a NEWER open (a larger relay id, since the
// frontend hands ids out in dispatch order) already owns the userevents relay, so a
// stale open abandons itself instead of restarting over its successor. Checked both
// before the dial -- an open that ran entirely late must not even tear the successor
// down -- and again at install, where the successor may have landed while we dialed.
// Caller holds lifecycleMu.
func (a *App) rejectIfSuperseded(connection *desktopConnection, relayID uint64) error {
	current := connection.userEventsRelay
	if current == nil || current.ownerNow() <= relayID {
		return nil
	}
	return fmt.Errorf("userevents relay superseded by a newer open")
}

// CloseUserEventsRelay tears down the relay IF relayID still owns it. A stale close
// must not tear down its successor: the frontend's tearDown/open pair dispatches the
// close first, but the sidecar may run it second, and the resulting teardown is
// SILENT (the relay context is cancelled before the read loop can emit an
// userevents:close), so the page would sit bootstrapped on a dead relay -- with the
// hub's one-shot UserMaterialized never re-sent -- until a reload.
func (a *App) CloseUserEventsRelay(relayID uint64) error {
	return a.closeRelayIfOwner(relayID, func(c *desktopConnection) *wsRelay {
		if c.userEventsRelay == nil {
			return nil
		}
		return &c.userEventsRelay.wsRelay
	}, a.closeUserEventsRelay)
}

// closeUserEventsRelay detaches the current userevents relay and clears the slot,
// returning the done channel for the caller to drainRelay after releasing
// lifecycleMu (nil if none was installed). Mirrors closeChannelRelay.
func (a *App) closeUserEventsRelay() <-chan struct{} {
	// Caller holds a.lifecycleMu for writing.
	connection := a.connection
	if connection == nil || connection.userEventsRelay == nil {
		return nil
	}
	done := connection.userEventsRelay.detach()
	connection.userEventsRelay = nil
	return done
}

func (r *UserEventsRelay) runReadLoop() { r.run(r.readLoop) }

func (r *UserEventsRelay) readLoop() {
	defer r.cancel()
	// stripPrefix=false: the hub frames each message as length-prefixed
	// WatchUserEvent proto bytes; we forward the raw WS frame verbatim
	// so the frontend's existing length-prefix parser stays unchanged.
	err := channelwire.ReadUserEventsFrames(r.ctx, r.ws, false, func(data []byte) error {
		r.emit(&desktoppb.Event{
			Payload: &desktoppb.Event_UserEventsMessage{
				UserEventsMessage: &desktoppb.UserEventsMessageEvent{
					Data: data,
				},
			},
		})
		return nil
	})
	if r.ctx.Err() != nil {
		return
	}
	if err != nil && r.ctx.Err() == nil {
		slog.Debug("userevents relay read error", "error", err)
	}
	// emitClose cancels before emitting, the shared terminal sequence both read
	// loops route through (see wsRelay.emitClose). No userevents adopt path gates
	// on ctx.Err()==nil today (OpenUserEventsRelay supersedes by owner id, not by
	// ctx), so the cancel-before-emit is defense in depth here -- but sharing the
	// sequence keeps the two loops' order identical, so a future shared
	// adopt-on-ctx path cannot adopt a relay whose read loop has already failed.
	r.emitClose(err, func(code uint32, reason string, wasClean bool) *desktoppb.Event {
		return &desktoppb.Event{
			Payload: &desktoppb.Event_UserEventsClose{
				UserEventsClose: &desktoppb.UserEventsCloseEvent{
					Code:     code,
					Reason:   reason,
					WasClean: wasClean,
				},
			},
		}
	})
}
