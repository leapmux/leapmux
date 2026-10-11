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

	"github.com/leapmux/leapmux/channelwire"
	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"

	"github.com/leapmux/leapmux/util/drain"
)

type RPCSession struct {
	app             *App
	reader          *bufio.Reader
	writer          io.Writer
	writeClose      io.Closer
	mu              sync.Mutex
	closeOnce       sync.Once
	handlerDrain    rpcHandlerDrain
	shutdownCleanup *drain.Counter
	peerDone        chan struct{}
	onShutdown      func()
}

type frameReadResult struct {
	frame *desktoppb.Frame
	err   error
}

// The Tauri shell sends local requests through one interactive RPC connection.
// RPCSession accepts every request and starts one handler goroutine for it.
// It applies no admission limit, concurrency limit or frame budget beyond frame validation.
// A concurrent request burst or large proxy upload must not discard a response or force a reconnect.
// Run tracks each handler with drain.Counter and limits its wait during teardown.
// Preserve this request-admission contract when changing the drain.

func NewRPCSession(app *App, reader io.Reader, writer io.Writer, onShutdown func()) *RPCSession {
	writeClose, _ := writer.(io.Closer)
	return &RPCSession{
		app:          app,
		reader:       bufio.NewReader(reader),
		writer:       writer,
		writeClose:   writeClose,
		onShutdown:   onShutdown,
		handlerDrain: newRPCHandlerDrain(),
		peerDone:     make(chan struct{}),
	}
}

func (s *RPCSession) Run() error {
	s.app.SetEventSink(s.emitEvent)
	s.app.SetEventSinkForRelay(s.emitEventForRelay)

	sessionCtx, cancelSession := context.WithCancelCause(s.app.ctx)
	readResults := make(chan frameReadResult, 1)
	go s.readFrames(readResults, cancelSession)
	var handlers drain.Counter
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
				return completedSessionError(result.err)
			}

			frame := result.frame
			req := frame.GetRequest()
			if req == nil {
				continue
			}
			handlers.Add()
			var cleanupDone func()
			if _, shutdown := req.Method.(*desktoppb.Request_Shutdown); shutdown {
				if s.shutdownCleanup == nil {
					s.shutdownCleanup = &drain.Counter{}
				}
				s.shutdownCleanup.Add()
				cleanupDone = sync.OnceFunc(s.shutdownCleanup.Done)
			}
			go func(req *desktoppb.Request, cleanupDone func()) {
				defer handlers.Done()
				s.dispatch(sessionCtx, req, cleanupDone)
			}(req, cleanupDone)
		}
	}
}

// dispatch answers the request that Run gave to this handler.
// Every request receives one response with the same ID.
// Without that response, the shell can wait indefinitely because ordinary requests have no deadline.
// readFrames can cancel the session before Run receives its last valid frame.
// App.Shutdown can also cancel app.ctx after Run admits a request.
// Both paths require an explicit error response from the admitted handler.
//
// The response writer remains open until the drain's second phase.
// During shutdown, the error matches the rejection that beginOperation gives to a later request.
// After a transport read error, the peer can no longer receive the response.
// failFrameForRelay logs that failed write without changing the shutdown result.
func (s *RPCSession) dispatch(sessionCtx context.Context, req *desktoppb.Request, cleanupDone func()) {
	if cleanupDone != nil {
		defer cleanupDone()
	}
	if err := sessionCtx.Err(); err != nil {
		if cleanupDone != nil {
			cleanupDone()
		}
		s.writeError(req.Id, fmt.Errorf("desktop sidecar is shutting down: %w", err))
		return
	}
	s.handleRequest(sessionCtx, req, cleanupDone)
}

func (s *RPCSession) drainHandlers(handlers *drain.Counter, cause error) {
	// A clean shutdown lets admitted handlers flush their final responses before writer interruption.
	// A transport error interrupts the writer immediately because the peer cannot receive a response.
	// The Shutdown handler cancels app.ctx before it stops the Worker and Hub.
	// Run therefore starts this drain while that handler still performs cleanup.
	// The first phase retains the writer until the handlers finish or the shared deadline expires.
	//
	// Ordinary handlers share one deadline across both phases.
	// A handler that ignores sessionCtx cannot keep the socket accept loop open indefinitely.
	// Writer interruption gives each handler a minimum grace to finish.
	// An admitted Shutdown retains the writer until its native cleanup ends or the peer disconnects.
	// A separate response grace then limits the wait for a blocked writer.
	//
	// This deadline limits only the handler drain.
	// App.Shutdown can first spend operationDrainTimeout and then wait indefinitely for solo cleanup.
	// main.go also waits on the same shutdownOnce before process exit.
	// A finished handler drain therefore does not prove that the sidecar process ended.
	//
	// Run is the only caller of handlers.Add, and its request loop ended before this deferred drain.
	// No new handler can enter after the completion channel is sampled.
	// Each phase can safely sample the counter again.
	policy := s.handlerDrain
	if s.shutdownCleanup != nil {
		policy.cleanupDone = s.shutdownCleanup.DoneChan()
	}
	policy.peerDone = s.peerDone
	policy.run(handlers, cause, s.interruptWriter)
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
			close(s.peerDone)
			s.interruptWriter()
		}
		select {
		case results <- frameReadResult{frame: frame, err: err}:
		case <-s.app.ctx.Done():
			// Native cleanup can outlive the handler deadline.
			// Continue reading so a peer disconnect still releases that cleanup wait.
		}
		if err != nil {
			return
		}
	}
}

func completedSessionError(err error) error {
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
	// the relay hot path (every ChannelMessage and UserEventsMessage rides one),
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
// decrypt, and `userevents:message` carries CRDT ops with no gap detection. Logging
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

func (s *RPCSession) handleRequest(ctx context.Context, req *desktoppb.Request, cleanupDone func()) {
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
		err := s.app.SetWindowSize(int(m.SetWindowSize.Width), int(m.SetWindowSize.Height), m.SetWindowSize.Mode)
		if err != nil {
			s.writeError(id, err)
			return
		}
		s.writeResponse(&desktoppb.Response{
			Id:     id,
			Result: &desktoppb.Response_SetWindowSize{SetWindowSize: &desktoppb.SetWindowSizeResponse{}},
		})

	case *desktoppb.Request_SetDesktopBehavior:
		err := s.app.SetDesktopBehavior(DesktopBehavior{
			TrayEnabled:    m.SetDesktopBehavior.TrayEnabled,
			TrayOnClose:    TrayOnClose(m.SetDesktopBehavior.TrayOnClose),
			TrayOnMinimize: TrayOnMinimize(m.SetDesktopBehavior.TrayOnMinimize),
			StartMinimized: StartMinimized(m.SetDesktopBehavior.StartMinimized),
		})
		if err != nil {
			s.writeError(id, err)
			return
		}
		s.writeResponse(&desktoppb.Response{
			Id:     id,
			Result: &desktoppb.Response_SetDesktopBehavior{SetDesktopBehavior: &desktoppb.SetDesktopBehaviorResponse{}},
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

	case *desktoppb.Request_OpenUserEventsRelay:
		req := m.OpenUserEventsRelay
		// Validate the resume cursor. The hub's ParseResumeCursor rejects a
		// malformed resume_after_hlc / resume_epoch with HTTP 400 (a malformed
		// cursor is a client bug, not a legacy client). The sidecar is the ONLY
		// path between the desktop frontend and the hub, so it applies the SAME
		// strictness: a malformed field rejects the relay-open RPC (the frontend
		// treats it as a failed open → reconnect, and on the next attempt sends
		// a well-formed cursor or none) rather than silently degrading to a
		// full-snapshot connect. Degrade+log would let a frontend serialization
		// regression limp along on full snapshots with a warn buried in sidecar
		// logs the frontend never surfaces — exactly the silent failure the
		// hub's 400 exists to catch.
		cursor, epoch, err := channelwire.ParseResumeCursor(req.GetResumeHlc(), req.GetResumeEpoch())
		if err != nil {
			slog.Warn("userevents relay: rejected malformed resume cursor",
				"resume_hlc", req.GetResumeHlc(), "resume_epoch", req.GetResumeEpoch(), "error", err)
			s.writeError(id, err)
			return
		}
		s.writeErrOrOK(id, s.app.OpenUserEventsRelay(
			ctx,
			req.GetRelayId(),
			req.GetWorkspaceIds(),
			cursor,
			epoch,
		))

	case *desktoppb.Request_CloseUserEventsRelay:
		s.writeErrOrOK(id, s.app.CloseUserEventsRelay(m.CloseUserEventsRelay.GetRelayId()))

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

	case *desktoppb.Request_ListExternalApps:
		apps, err := s.app.ListExternalApps(m.ListExternalApps.Refresh)
		if err != nil {
			s.writeError(id, err)
			return
		}
		s.writeResponse(&desktoppb.Response{
			Id: id,
			Result: &desktoppb.Response_ListExternalApps{
				ListExternalApps: &desktoppb.ListExternalAppsResponse{
					Apps: externalAppsToProto(apps),
				},
			},
		})

	case *desktoppb.Request_OpenInExternalApp:
		s.writeErrOrOK(id, s.app.OpenInExternalApp(m.OpenInExternalApp.AppId, m.OpenInExternalApp.Path))

	case *desktoppb.Request_Shutdown:
		outcome := lifecycleOutcome{}
		if err := s.app.Shutdown(); err != nil {
			outcome.cleanupErrors = append(outcome.cleanupErrors, err)
		}
		if cleanupDone != nil {
			cleanupDone()
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

// configToProto copies the config onto the wire.
//
// The four token fields go across VERBATIM. They are contract tokens on both
// sides, so there is nothing to translate: the sidecar stores this vocabulary
// and never branches on it, and the Rust shell normalizes an empty or
// unrecognized token once, where the policy that reads it lives.
func configToProto(cfg *DesktopConfig) *desktoppb.DesktopConfig {
	return &desktoppb.DesktopConfig{
		Mode:           cfg.Mode,
		HubUrl:         cfg.HubURL,
		WindowWidth:    int32(cfg.WindowWidth),
		WindowHeight:   int32(cfg.WindowHeight),
		WindowMode:     cfg.WindowMode,
		TrayEnabled:    cfg.TrayEnabled,
		TrayOnClose:    string(cfg.TrayOnClose),
		TrayOnMinimize: string(cfg.TrayOnMinimize),
		StartMinimized: string(cfg.StartMinimized),
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

// externalAppsToProto maps an application listing for the wire.
func externalAppsToProto(apps []ExternalApp) []*desktoppb.ExternalApp {
	out := make([]*desktoppb.ExternalApp, len(apps))
	for i := range apps {
		out[i] = &desktoppb.ExternalApp{
			Id:          apps[i].ID,
			DisplayName: apps[i].DisplayName,
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
