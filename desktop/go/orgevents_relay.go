package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

// dialOrgEvents opens the org-events WebSocket. A package var (mirroring
// App.startSolo and TunnelManager.openCh) so tests can hold a dial open and drive
// the concurrent-open fence below deterministically, which is otherwise a race no
// test could pin down.
var dialOrgEvents = func(ctx context.Context, proxy *HubProxy, orgID string, workspaceIDs []string) (*websocket.Conn, error) {
	// Fail closed on a missing WS client (see HubProxy.requireWSClient): a nil
	// wsClient makes OpenOrgEventsWSWithHeader fall back to http.DefaultClient,
	// which carries neither the cookie jar nor pinRedirectsToOrigin and would
	// reopen the hub-side off-origin 3xx redirect-escape the pin closes.
	if err := proxy.requireWSClient("org events relay"); err != nil {
		return nil, err
	}
	return channelwire.OpenOrgEventsWSWithHeader(
		ctx, proxy.wsClient, proxy.baseURL, proxy.cookieHeader(), orgID, workspaceIDs,
	)
}

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
	wsRelay
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
//
// `relayID` names the frontend wrapper opening the relay (see
// wsRelay.owner). Because this open force-restarts rather than
// adopts, the id also has to ORDER the opens: RPCSession runs every
// request on its own goroutine, so an open dispatched earlier can
// execute later, and restarting over a newer relay would tear down
// the one the page is actually listening on -- silently, since the
// teardown cancels the relay context before its read loop can emit
// an orgevents:close. So a stale open abandons itself instead:
// last open dispatched wins, whatever order the sidecar runs them in.
func (a *App) OpenOrgEventsRelay(requestCtx context.Context, relayID uint64, orgID string, workspaceIDs []string) error {
	// A static argument check, so it lives here rather than inside the policy
	// (which runs twice and decides about the INSTALLED relay, not the request).
	if orgID == "" {
		return fmt.Errorf("orgID required")
	}
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
		closePrior: func() { _ = a.closeOrgEventsRelay() },
		dial: func(dialCtx context.Context, proxy *HubProxy) (*websocket.Conn, error) {
			ws, err := dialOrgEvents(dialCtx, proxy, orgID, workspaceIDs)
			if err != nil {
				return nil, fmt.Errorf("connect to org events relay: %w", err)
			}
			return ws, nil
		},
		commit: func(connection *desktopConnection, ws *websocket.Conn, ctx context.Context, cancel context.CancelFunc) {
			relay := &OrgEventsRelay{
				wsRelay: newWSRelay(ws, ctx, cancel, a.EmitEvent),
			}
			// Stamped before the relay is installed, so no close can ever observe it unowned.
			relay.owner = relayID
			// Route the read loop's emits through the relay-aware sink so an
			// undeliverable frame carries this relay's owner id forward to the close
			// path (mirrors the channel relay's commit closure).
			relay.emit = a.emitForOwner(&relay.wsRelay)
			go relay.runReadLoop()
			connection.orgEventsRelay = relay
		},
	})
}

// rejectIfSuperseded reports an error when a NEWER open (a larger relay id, since the
// frontend hands ids out in dispatch order) already owns the org-events relay, so a
// stale open abandons itself instead of restarting over its successor. Checked both
// before the dial -- an open that ran entirely late must not even tear the successor
// down -- and again at install, where the successor may have landed while we dialed.
// Caller holds lifecycleMu.
func (a *App) rejectIfSuperseded(connection *desktopConnection, relayID uint64) error {
	current := connection.orgEventsRelay
	if current == nil || current.owner <= relayID {
		return nil
	}
	return fmt.Errorf("org events relay superseded by a newer open")
}

// CloseOrgEventsRelay tears down the relay IF relayID still owns it. A stale close
// must not tear down its successor: the frontend's tearDown/open pair dispatches the
// close first, but the sidecar may run it second, and the resulting teardown is
// SILENT (the relay context is cancelled before the read loop can emit an
// orgevents:close), so the page would sit bootstrapped on a dead relay -- with the
// hub's one-shot OrgMaterialized never re-sent -- until a reload.
func (a *App) CloseOrgEventsRelay(relayID uint64) error {
	return a.closeRelayIfOwner(relayID, func(c *desktopConnection) *wsRelay {
		if c.orgEventsRelay == nil {
			return nil
		}
		return &c.orgEventsRelay.wsRelay
	}, a.closeOrgEventsRelay)
}

// closeOrgEventsRelay detaches the current org-events relay and clears the slot,
// returning the done channel for the caller to drainRelay after releasing
// lifecycleMu (nil if none was installed). Mirrors closeChannelRelay.
func (a *App) closeOrgEventsRelay() <-chan struct{} {
	// Caller holds a.lifecycleMu for writing.
	connection := a.connection
	if connection == nil || connection.orgEventsRelay == nil {
		return nil
	}
	done := connection.orgEventsRelay.detach()
	connection.orgEventsRelay = nil
	return done
}

func (r *OrgEventsRelay) runReadLoop() { r.run(r.readLoop) }

func (r *OrgEventsRelay) readLoop() {
	defer r.cancel()
	// stripPrefix=false: the hub frames each message as length-prefixed
	// WatchOrgEvent proto bytes; we forward the raw WS frame verbatim
	// so the frontend's existing length-prefix parser stays unchanged.
	err := channelwire.ReadOrgEventsFrames(r.ctx, r.ws, false, func(data []byte) error {
		r.emit(&desktoppb.Event{
			Payload: &desktoppb.Event_OrgEventsMessage{
				OrgEventsMessage: &desktoppb.OrgEventsMessageEvent{
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
		slog.Debug("org events relay read error", "error", err)
	}
	// emitClose cancels before emitting, the shared terminal sequence both read
	// loops route through (see wsRelay.emitClose). No org-events adopt path gates
	// on ctx.Err()==nil today (OpenOrgEventsRelay supersedes by owner id, not by
	// ctx), so the cancel-before-emit is defense in depth here -- but sharing the
	// sequence keeps the two loops' order identical, so a future shared
	// adopt-on-ctx path cannot adopt a relay whose read loop has already failed.
	r.emitClose(err, func(code uint32, reason string, wasClean bool) *desktoppb.Event {
		return &desktoppb.Event{
			Payload: &desktoppb.Event_OrgEventsClose{
				OrgEventsClose: &desktoppb.OrgEventsCloseEvent{
					Code:     code,
					Reason:   reason,
					WasClean: wasClean,
				},
			},
		}
	})
}
