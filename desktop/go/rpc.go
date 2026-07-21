package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

type RPCSession struct {
	app        *App
	reader     *bufio.Reader
	writer     io.Writer
	writeClose io.Closer
	mu         sync.Mutex
	closeOnce  sync.Once
	onShutdown func()
}

type frameReadResult struct {
	frame *desktoppb.Frame
	err   error
}

// The desktop RPC session is local and interactive: the only client is the
// Tauri shell on the same machine, cooperating over a single connection. There
// is deliberately NO admission control, concurrency cap, or byte budget --
// every request is accepted and dispatched to its own handler goroutine, so a
// burst of concurrent calls (or one large proxy upload) never drops an
// in-flight response or forces a reconnect. Teardown stays bounded: handlers
// are tracked by a waitCounter and drained (interruptably) in Run's defer. Do not
// reintroduce an admission/budget gate without reconsidering that contract.

// handlerDrainTimeout is the hard cap on waiting for in-flight handlers, both
// before the writer is interrupted (so a handler can flush its final response)
// and after (so teardown unwinds). It bounds teardown against a handler that
// ignores sessionCtx (a non-cancellable exec or filesystem scan); stragglers are
// abandoned and reclaimed at process exit. A var so tests can shorten it.
//
// It matches the shell's own patience -- desktop/rust/src/main.rs's
// request_shutdown_async waits 5s for the Shutdown reply -- so a response that
// would arrive later is one no caller is still listening for.
var handlerDrainTimeout time.Duration = 5 * time.Second

// interruptGrace is the drain's own budget for the phase AFTER the writer is
// interrupted, on top of whatever is left of handlerDrainTimeout.
//
// It has to be its own budget, because reaching that phase on the clean path
// means the shared budget is already spent by construction: waitBounded only
// returns false once its timer has fired. Without a floor the post-interrupt wait
// would be a single non-blocking look at the counter, which a handler that the
// interrupt just unblocked cannot win in the microseconds it needs to unwind --
// so every clean drain past the flush window would warn and abandon handlers that
// were about to finish. A small fixed grace is the right shape here: interrupting
// the writer unblocks a pipe-blocked handler immediately, so anything that has
// not returned within it is a genuine straggler. A var so tests can shorten it.
var interruptGrace time.Duration = 250 * time.Millisecond

func NewRPCSession(app *App, reader io.Reader, writer io.Writer, onShutdown func()) *RPCSession {
	writeClose, _ := writer.(io.Closer)
	return &RPCSession{
		app:        app,
		reader:     bufio.NewReader(reader),
		writer:     writer,
		writeClose: writeClose,
		onShutdown: onShutdown,
	}
}

func (s *RPCSession) Run() error {
	s.app.SetEventSink(s.emitEvent)
	s.app.SetEventSinkForRelay(s.emitEventForRelay)

	sessionCtx, cancelSession := context.WithCancelCause(s.app.ctx)
	readResults := make(chan frameReadResult, 1)
	go s.readFrames(readResults, cancelSession)
	var handlers waitCounter
	defer func() {
		cancelSession(nil)
		s.app.SetEventSink(nil)
		s.app.SetEventSinkForRelay(nil)
		s.drainHandlers(&handlers, context.Cause(sessionCtx))
	}()
	for {
		select {
		case <-s.app.ctx.Done():
			return nil
		case result := <-readResults:
			if s.app.ctx.Err() != nil {
				return nil
			}
			if result.err != nil {
				return terminalSessionError(result.err)
			}

			frame := result.frame
			req := frame.GetRequest()
			if req == nil {
				continue
			}
			handlers.add()
			go func(req *desktoppb.Request) {
				defer handlers.done()
				s.dispatch(sessionCtx, req)
			}(req)
		}
	}
}

// dispatch answers req, which was dequeued for its own handler goroutine.
//
// Every Request frame gets exactly one Response carrying its Id -- a request dropped
// in silence would leave the shell awaiting a reply it never gets, since it has no
// per-request timeout. A session cancelled between the read and this dispatch is
// reachable two ways (readFrames cancels BEFORE pushing the read error, so the last
// good frame is dequeued under an already-cancelled session; and App.Shutdown cancels
// app.ctx underneath a request that already passed Run's check), so a cancelled
// session is answered explicitly rather than skipped. The writer is still live here --
// interruptWriter only runs in the drain's second phase -- so on the shutdown path the
// error actually reaches the shell, and it matches what beginOperation would have
// returned for a request arriving a moment later; on the read-error path the peer is
// gone and the write fails harmlessly into failFrameForRelay's log.
func (s *RPCSession) dispatch(sessionCtx context.Context, req *desktoppb.Request) {
	if err := sessionCtx.Err(); err != nil {
		s.writeError(req.Id, fmt.Errorf("desktop sidecar is shutting down: %w", err))
		return
	}
	s.handleRequest(sessionCtx, req)
}

func (s *RPCSession) drainHandlers(handlers *waitCounter, cause error) {
	// On a clean shutdown (no cause, or a plain Canceled) let handlers flush their
	// final responses BEFORE the writer is interrupted; on an error teardown the
	// peer is already gone, so there is nothing to flush and we interrupt at once.
	//
	// The clean-path wait must cover a legitimately slow handler, because the
	// Shutdown RPC's own handler is one of them: it calls App.Shutdown, whose first
	// act is cancelling app.ctx -- which is exactly what ends Run and brings us
	// here. In solo mode that handler is still inside the hub teardown (operation
	// drain, then server/registry/watcher shutdown) for well over a second, so a
	// short grace interrupted the writer out from under it and dropped the
	// LifecycleResult -- the cleanup_errors report the RPC exists to deliver -- and
	// left the shell burning its full timeout waiting for a reply that never came.
	// The wait ends the moment handlers finish, so a fast teardown is not slowed.
	//
	// Either way the wait is bounded so a handler that ignores sessionCtx (a
	// non-cancellable exec or filesystem scan) cannot hang teardown, which would
	// block the socket accept loop or process exit. Stragglers are abandoned; the
	// session is ending and they are reclaimed at process exit.
	//
	// handlerDrainTimeout is the budget for the WHOLE drain, not per phase (plus
	// interruptGrace, which the post-interrupt phase needs as a floor). The two
	// phases share a deadline because they are sequential: giving each the full
	// timeout would let teardown run to 2x it, and the timeout is sized against the
	// shell's own patience (request_shutdown_async waits handlerDrainTimeout for the
	// Shutdown reply), so a per-phase budget would routinely drain past the point
	// where any caller is still listening.
	//
	// It bounds the HANDLER DRAIN only -- NOT the sidecar's total shutdown, which is
	// not bounded by the shell's window at all. App.Shutdown's own drainOperations
	// can spend operationDrainTimeout before its handler even reaches this point, and
	// the stopSolo teardown that follows runs unbounded; main.go also calls
	// App.Shutdown (through the same shutdownOnce) on its way out. So this budget
	// makes the drain give up promptly; it does not promise the process is gone.
	//
	// waitCounter's no-add-after-sample contract holds here by ordering: only
	// Run's loop calls handlers.add(), and that loop has exited before this
	// deferred drain runs, so the counter only decreases from here on and each
	// phase's wait may safely re-sample it.
	deadline := time.Now().Add(handlerDrainTimeout)
	if cause == nil || errors.Is(cause, context.Canceled) {
		// No warning on this phase: exceeding the flush window is not itself a
		// failure, it just means the writer is interrupted and the wait below
		// reports any straggler.
		if handlers.wait(time.Until(deadline), "") {
			return
		}
	}
	s.interruptWriter()
	// Whatever is left of the shared budget, but never less than interruptGrace: the
	// clean path only gets here once the budget is spent, so without the floor this
	// phase would never actually wait on the interrupt it just issued.
	handlers.wait(max(time.Until(deadline), interruptGrace),
		"rpc session: handler drain timed out; abandoning in-flight handlers")
}

func (s *RPCSession) interruptWriter() {
	if s.writeClose == nil {
		return
	}
	s.closeOnce.Do(func() { _ = s.writeClose.Close() })
}

func (s *RPCSession) readFrames(results chan<- frameReadResult, cancelSession context.CancelCauseFunc) {
	for {
		frame, err := ReadFrame(s.reader)
		if err != nil {
			cancelSession(err)
		}
		select {
		case results <- frameReadResult{frame: frame, err: err}:
		case <-s.app.ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func terminalSessionError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || isBenignSessionReadError(err) {
		return nil
	}
	return fmt.Errorf("read frame: %w", err)
}

func isBenignSessionReadError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	if isPipeClosed(err) {
		return true
	}
	// Fallback for errors that don't implement Unwrap (net/net.OpError
	// occasionally wraps the bare "use of closed network connection" string).
	return strings.Contains(err.Error(), "use of closed network connection")
}

func (s *RPCSession) emitEvent(event *desktoppb.Event) {
	s.emitEventForRelay(0, event)
}

// emitEventForRelay is the relay-aware emit: it writes the event frame to the
// shell pipe, and on a delivery failure (oversize frame, broken pipe) tells
// CloseRelayForUndeliverableEvent WHICH relay emitted it so the close can gate on
// ownership. owner is 0 for non-relay events (the generic emitEvent path), which
// CloseRelayForUndeliverableEvent treats as "no relay to close" (only *Message
// payloads name a relay, and 0 matches no installed owner).
func (s *RPCSession) emitEventForRelay(owner uint64, event *desktoppb.Event) {
	frame := &desktoppb.Frame{
		Message: &desktoppb.Frame_Event{Event: event},
	}
	// Validate once and route past WriteFrame's re-validation: event frames are
	// the relay hot path (every ChannelMessage and OrgEventsMessage rides one),
	// and WriteFrame would walk the frame with proto.Size a second time before
	// the marshal sizes it again -- the same double walk writeResponse already
	// avoids via writeFrameUnchecked. An event that busts the budget takes the
	// same failFrameForRelay path a failed write would, so an oversized relay
	// frame still tears its relay down instead of being silently skipped.
	if err := validateFrameSize(frame); err != nil {
		s.failFrameForRelay(owner, frame, err)
		return
	}
	// Inline the write rather than routing through writeFrameUnchecked: a write
	// failure on a relay event frame must close THAT relay (the stream is
	// desynced), and writeFrameUnchecked's failure path is the log-only
	// failFrameForRelay(0, ...) used for non-relay response frames. Doing the
	// write here keeps both the budget and the write failure paths on
	// failFrameForRelay.
	s.mu.Lock()
	werr := writeFrameTo(s.writer, frame)
	s.mu.Unlock()
	if werr != nil {
		s.failFrameForRelay(owner, frame, werr)
	}
}

// failFrameForRelay reports a frame that could not be delivered and, when it
// belonged to an ordered relay stream, tears that relay down so the frontend
// resynchronizes.
//
// A relay stream is ORDERED and its consumer cannot detect, let alone tolerate, a
// gap: `channel:message` carries Noise ciphertext whose implicit nonce counter
// advances per message, so ONE dropped frame permanently desyncs every subsequent
// decrypt, and `orgevents:message` carries CRDT ops with no gap detection. Logging
// and carrying on converts a delivery failure into silent, unbounded corruption on a
// relay that still reports healthy -- leaving the frame budget's headroom as the only
// thing between the app and that corruption.
//
// Three failures reach here, and a relay-scoped recovery is coherent for each:
//
//   - The frame exceeded the budget. validateFrameSize rejects before writeFrameTo
//     runs, so nothing was written. This is the case this function exists for, and
//     the stream is intact -- which is what lets the close event emitted below
//     actually arrive.
//   - proto.Marshal failed (a nil or invalid message). protodelim.MarshalTo returns
//     (0, err) before its first Write, so again nothing was written.
//   - A Write failed. This one CAN leave bytes on the wire: MarshalTo writes the
//     varint prefix and the body as two calls, and even one call can return short.
//     But s.writer is a raw net.Conn with no write deadline anywhere in this sidecar,
//     so Write only returns short on a broken pipe -- a peer that is alive but not
//     reading makes it block, not truncate. By the time a partial write happens the
//     reader that would misparse the tail is already gone, and the next write fails
//     too, so no one ever observes desynced framing. The read loop tears the session
//     down on its own.
//
// So do NOT escalate this to a session-wide teardown to "protect" the framing: in the
// only case where framing could be damaged, there is no reader left to damage it for.
//
// The teardown is scoped to the offending RELAY rather than the whole RPC
// session, because that is the level at which recovery already exists: the
// frontend reconnects on a relay close and re-handshakes, whereas killing the
// session would strand the Tauri shell, which has no sidecar respawn and awaits
// every request without a timeout -- turning one bad frame into a permanently
// wedged UI. Anything else (a response, a non-relay event) is order-independent,
// so logging it is the whole remedy.
//
// failFrameForRelay threads the emitting relay's owner id into
// CloseRelayForUndeliverableEvent so the close gates on ownership: without it,
// the close goroutine (spawned here) could run after a successor's open
// superseded the emitter and tear down the successor's relay for the emitter's
// fault. owner is 0 for non-relay frames, which no installed relay matches.
func (s *RPCSession) failFrameForRelay(owner uint64, frame *desktoppb.Frame, err error) {
	slog.Error("failed to write frame", "error", err)
	event := frame.GetEvent()
	if event == nil {
		return
	}
	// Off this goroutine: the emit that failed may BE the relay's read loop, and
	// tearing the relay down joins that loop.
	go s.app.CloseRelayForUndeliverableEvent(owner, event)
}

// writeFrameUnchecked writes frame under the session write mutex WITHOUT
// re-validating the frame budget. writeResponse has already validated (or
// substituted an in-budget error response), so routing through this avoids a
// second proto.Size walk on every response -- meaningful for ProxyHTTP
// responses whose body can be several MB.
func (s *RPCSession) writeFrameUnchecked(frame *desktoppb.Frame) {
	s.mu.Lock()
	err := writeFrameTo(s.writer, frame)
	s.mu.Unlock()
	if err != nil {
		// owner 0 matches no installed relay, so this logs and returns for the
		// response frames writeFrameUnchecked carries -- the same log-only remedy
		// the removed failFrame wrapper gave, without a second failure path to
		// keep in sync.
		s.failFrameForRelay(0, frame, err)
	}
}

func (s *RPCSession) writeResponse(resp *desktoppb.Response) {
	frame := &desktoppb.Frame{
		Message: &desktoppb.Frame_Response{Response: resp},
	}
	if err := validateFrameSize(frame); err != nil {
		frame = &desktoppb.Frame{
			Message: &desktoppb.Frame_Response{Response: &desktoppb.Response{
				Id:    resp.GetId(),
				Error: fmt.Sprintf("response exceeds frame budget: %v", err),
			}},
		}
	}
	s.writeFrameUnchecked(frame)
}

func (s *RPCSession) writeError(id uint64, err error) {
	s.writeResponse(&desktoppb.Response{
		Id:    id,
		Error: err.Error(),
	})
}

// writeErrOrOK is the whole reply of a void method: err's message when it failed,
// the boolean ack otherwise. Every void method routes through here so the ack shape
// is defined once -- a new one cannot ship acking with a different result type.
func (s *RPCSession) writeErrOrOK(id uint64, err error) {
	if err != nil {
		s.writeError(id, err)
		return
	}
	s.writeOK(id)
}

func (s *RPCSession) writeOK(id uint64) {
	s.writeResponse(&desktoppb.Response{
		Id:     id,
		Result: &desktoppb.Response_BoolValue{BoolValue: &desktoppb.BoolValue{Value: true}},
	})
}

func (s *RPCSession) writeSidecarInfo(id uint64) {
	s.writeResponse(&desktoppb.Response{
		Id:     id,
		Result: &desktoppb.Response_SidecarInfo{SidecarInfo: s.app.SidecarInfo()},
	})
}

func (s *RPCSession) handleRequest(ctx context.Context, req *desktoppb.Request) {
	id := req.Id

	switch m := req.Method.(type) {
	case *desktoppb.Request_GetConfig:
		s.writeResponse(&desktoppb.Response{
			Id: id,
			Result: &desktoppb.Response_Config{
				Config: configToProto(s.app.GetConfig()),
			},
		})

	case *desktoppb.Request_SetWindowSize:
		err := s.app.SetWindowSize(int(m.SetWindowSize.Width), int(m.SetWindowSize.Height), windowModeFromProto(m.SetWindowSize.Mode))
		if err != nil {
			s.writeError(id, err)
			return
		}
		s.writeResponse(&desktoppb.Response{
			Id:     id,
			Result: &desktoppb.Response_SetWindowSize{SetWindowSize: &desktoppb.SetWindowSizeResponse{}},
		})

	case *desktoppb.Request_GetBuildInfo:
		s.writeResponse(&desktoppb.Response{
			Id: id,
			Result: &desktoppb.Response_BuildInfo{
				BuildInfo: buildInfoToProto(s.app.GetBuildInfo()),
			},
		})

	case *desktoppb.Request_GetSidecarInfo:
		s.writeSidecarInfo(id)

	case *desktoppb.Request_GetStartupInfo:
		s.writeResponse(&desktoppb.Response{
			Id: id,
			Result: &desktoppb.Response_StartupInfo{
				StartupInfo: &desktoppb.StartupInfo{
					Config:    configToProto(s.app.GetConfig()),
					BuildInfo: buildInfoToProto(s.app.GetBuildInfo()),
				},
			},
		})

	case *desktoppb.Request_CheckFullDiskAccess:
		s.writeResponse(&desktoppb.Response{
			Id:     id,
			Result: &desktoppb.Response_BoolValue{BoolValue: &desktoppb.BoolValue{Value: s.app.CheckFullDiskAccess()}},
		})

	case *desktoppb.Request_OpenFullDiskAccessSettings:
		s.writeErrOrOK(id, s.app.OpenFullDiskAccessSettings())

	case *desktoppb.Request_ConnectSolo:
		if err := s.app.ConnectSolo(ctx); err != nil {
			s.writeError(id, err)
			return
		}
		s.writeSidecarInfo(id)

	case *desktoppb.Request_ConnectDistributed:
		if err := s.app.ConnectDistributed(ctx, m.ConnectDistributed.HubUrl); err != nil {
			s.writeError(id, err)
			return
		}
		s.writeSidecarInfo(id)

	case *desktoppb.Request_ProxyHttp:
		resp, body, err := s.app.ProxyHTTP(ctx, m.ProxyHttp.Method, m.ProxyHttp.Path, m.ProxyHttp.Headers, m.ProxyHttp.Body)
		if err != nil {
			s.writeError(id, err)
			return
		}
		s.writeResponse(&desktoppb.Response{
			Id: id,
			Result: &desktoppb.Response_ProxyHttp{
				ProxyHttp: &desktoppb.ProxyHttpResponse{
					Status:  int32(resp.Status),
					Headers: headerValuesToProto(resp.Headers),
					Body:    body,
				},
			},
		})

	case *desktoppb.Request_OpenChannelRelay:
		s.writeErrOrOK(id, s.app.OpenChannelRelay(ctx, m.OpenChannelRelay.GetRelayId()))

	case *desktoppb.Request_SendChannelMessage:
		s.writeErrOrOK(id, s.app.SendChannelMessage(ctx, m.SendChannelMessage.Data))

	case *desktoppb.Request_CloseChannelRelay:
		s.writeErrOrOK(id, s.app.CloseChannelRelay(m.CloseChannelRelay.GetRelayId()))

	case *desktoppb.Request_OpenOrgEventsRelay:
		s.writeErrOrOK(id, s.app.OpenOrgEventsRelay(
			ctx,
			m.OpenOrgEventsRelay.GetRelayId(),
			m.OpenOrgEventsRelay.GetOrgId(),
			m.OpenOrgEventsRelay.GetWorkspaceIds(),
		))

	case *desktoppb.Request_CloseOrgEventsRelay:
		s.writeErrOrOK(id, s.app.CloseOrgEventsRelay(m.CloseOrgEventsRelay.GetRelayId()))

	case *desktoppb.Request_SwitchMode:
		outcome, err := s.app.SwitchMode()
		if err != nil {
			s.writeError(id, err)
			return
		}
		s.writeLifecycleResult(id, outcome)

	case *desktoppb.Request_CreateTunnel:
		cfg := m.CreateTunnel.Config
		if cfg == nil {
			s.writeError(id, fmt.Errorf("tunnel config is required"))
			return
		}
		info, err := s.app.CreateTunnel(ctx, TunnelConfig{
			WorkerID:   cfg.WorkerId,
			Type:       cfg.Type,
			TargetAddr: cfg.TargetAddr,
			TargetPort: int(cfg.TargetPort),
			BindAddr:   cfg.BindAddr,
			BindPort:   int(cfg.BindPort),
		})
		if err != nil {
			s.writeError(id, err)
			return
		}
		s.writeResponse(&desktoppb.Response{
			Id: id,
			Result: &desktoppb.Response_CreateTunnel{
				CreateTunnel: &desktoppb.CreateTunnelResponse{
					Info: tunnelInfoToProto(info),
				},
			},
		})

	case *desktoppb.Request_DeleteTunnel:
		s.writeErrOrOK(id, s.app.DeleteTunnel(m.DeleteTunnel.TunnelId))

	case *desktoppb.Request_ResetTunnels:
		s.writeErrOrOK(id, s.app.ResetTunnels())

	case *desktoppb.Request_ListTunnels:
		s.writeResponse(&desktoppb.Response{
			Id: id,
			Result: &desktoppb.Response_ListTunnels{
				ListTunnels: &desktoppb.ListTunnelsResponse{
					Tunnels: tunnelInfosToProto(s.app.ListTunnels()),
				},
			},
		})

	case *desktoppb.Request_ListEditors:
		editors, err := s.app.ListEditors(m.ListEditors.Refresh)
		if err != nil {
			s.writeError(id, err)
			return
		}
		s.writeResponse(&desktoppb.Response{
			Id: id,
			Result: &desktoppb.Response_ListEditors{
				ListEditors: &desktoppb.ListEditorsResponse{
					Editors: detectedEditorsToProto(editors),
				},
			},
		})

	case *desktoppb.Request_OpenInEditor:
		s.writeErrOrOK(id, s.app.OpenInEditor(m.OpenInEditor.EditorId, m.OpenInEditor.Path))

	case *desktoppb.Request_Shutdown:
		outcome := lifecycleOutcome{}
		if err := s.app.Shutdown(); err != nil {
			outcome.cleanupErrors = append(outcome.cleanupErrors, err)
		}
		s.writeLifecycleResult(id, outcome)
		if s.onShutdown != nil {
			go s.onShutdown()
		}

	case *desktoppb.Request_CliPathStatus:
		s.writeResponse(&desktoppb.Response{
			Id:     id,
			Result: &desktoppb.Response_CliPathStatus{CliPathStatus: s.app.CliPathStatus()},
		})

	case *desktoppb.Request_CliInstallSymlink:
		result, err := s.app.CliInstallSymlink(m.CliInstallSymlink.Force)
		if err != nil {
			s.writeError(id, err)
			return
		}
		s.writeResponse(&desktoppb.Response{
			Id:     id,
			Result: &desktoppb.Response_CliInstallSymlink{CliInstallSymlink: result},
		})

	default:
		s.writeError(id, fmt.Errorf("unknown method: %T", req.Method))
	}
}

func (s *RPCSession) writeLifecycleResult(id uint64, outcome lifecycleOutcome) {
	cleanupErrors := make([]string, len(outcome.cleanupErrors))
	for i, err := range outcome.cleanupErrors {
		cleanupErrors[i] = err.Error()
	}
	s.writeResponse(&desktoppb.Response{
		Id: id,
		Result: &desktoppb.Response_Lifecycle{Lifecycle: &desktoppb.LifecycleResult{
			SidecarInfo:   s.app.SidecarInfo(),
			CleanupErrors: cleanupErrors,
		}},
	})
}

func configToProto(cfg *DesktopConfig) *desktoppb.DesktopConfig {
	return &desktoppb.DesktopConfig{
		Mode:         cfg.Mode,
		HubUrl:       cfg.HubURL,
		WindowWidth:  int32(cfg.WindowWidth),
		WindowHeight: int32(cfg.WindowHeight),
		WindowMode:   windowModeToProto(cfg.WindowMode),
	}
}

// windowModeToProto maps the config's string window mode onto the wire enum.
// Empty/unknown becomes NORMAL (the fresh-config default).
func windowModeToProto(mode string) desktoppb.WindowMode {
	switch mode {
	case WindowModeMaximized:
		return desktoppb.WindowMode_WINDOW_MODE_MAXIMIZED
	case WindowModeFullscreen:
		return desktoppb.WindowMode_WINDOW_MODE_FULLSCREEN
	default:
		return desktoppb.WindowMode_WINDOW_MODE_NORMAL
	}
}

// windowModeFromProto maps the wire enum back to the config's string mode.
// UNSPECIFIED/unknown becomes "normal".
func windowModeFromProto(mode desktoppb.WindowMode) string {
	switch mode {
	case desktoppb.WindowMode_WINDOW_MODE_MAXIMIZED:
		return WindowModeMaximized
	case desktoppb.WindowMode_WINDOW_MODE_FULLSCREEN:
		return WindowModeFullscreen
	default:
		return WindowModeNormal
	}
}

func buildInfoToProto(info BuildInfo) *desktoppb.BuildInfo {
	return &desktoppb.BuildInfo{
		Version:    info.Version,
		CommitHash: info.CommitHash,
		CommitTime: info.CommitTime,
		BuildTime:  info.BuildTime,
		Branch:     info.Branch,
	}
}

// tunnelInfosToProto maps a tunnel listing for the wire; the plural sibling of
// tunnelInfoToProto, kept beside the other converters so the dispatch switch
// stays a thin router.
func tunnelInfosToProto(tunnels []TunnelInfo) []*desktoppb.TunnelInfo {
	out := make([]*desktoppb.TunnelInfo, len(tunnels))
	for i := range tunnels {
		out[i] = tunnelInfoToProto(&tunnels[i])
	}
	return out
}

// detectedEditorsToProto maps an editor listing for the wire.
func detectedEditorsToProto(editors []DetectedEditor) []*desktoppb.DetectedEditor {
	out := make([]*desktoppb.DetectedEditor, len(editors))
	for i := range editors {
		out[i] = &desktoppb.DetectedEditor{
			Id:          editors[i].ID,
			DisplayName: editors[i].DisplayName,
		}
	}
	return out
}

func tunnelInfoToProto(info *TunnelInfo) *desktoppb.TunnelInfo {
	return &desktoppb.TunnelInfo{
		Id:         info.ID,
		WorkerId:   info.WorkerID,
		Type:       info.Type,
		BindAddr:   info.BindAddr,
		BindPort:   int32(info.BindPort),
		TargetAddr: info.TargetAddr,
		TargetPort: int32(info.TargetPort),
	}
}
