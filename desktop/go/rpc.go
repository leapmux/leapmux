package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

type RPCSession struct {
	app        *App
	reader     *bufio.Reader
	writer     io.Writer
	mu         sync.Mutex
	onShutdown func()
}

func NewRPCSession(app *App, reader io.Reader, writer io.Writer, onShutdown func()) *RPCSession {
	return &RPCSession{
		app:        app,
		reader:     bufio.NewReader(reader),
		writer:     writer,
		onShutdown: onShutdown,
	}
}

func (s *RPCSession) Run() error {
	s.app.SetEventSink(s.emitEvent)
	defer s.app.SetEventSink(nil)

	for {
		frame, err := ReadFrame(s.reader)
		if err != nil {
			if isBenignSessionReadError(err) {
				return nil
			}
			return fmt.Errorf("read frame: %w", err)
		}

		req := frame.GetRequest()
		if req == nil {
			continue
		}
		// Unbounded per-request goroutine is intentional: the peer is the
		// trusted Tauri shell (one session at a time, enforced by socket.go),
		// not an untrusted network client.
		go s.handleRequest(req)
	}
}

func isBenignSessionReadError(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF || err == io.ErrUnexpectedEOF {
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
	s.writeFrame(&desktoppb.Frame{
		Message: &desktoppb.Frame_Event{Event: event},
	})
}

func (s *RPCSession) writeFrame(frame *desktoppb.Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := WriteFrame(s.writer, frame); err != nil {
		slog.Error("failed to write frame", "error", err)
	}
}

func (s *RPCSession) writeResponse(resp *desktoppb.Response) {
	s.writeFrame(&desktoppb.Frame{
		Message: &desktoppb.Frame_Response{Response: resp},
	})
}

func (s *RPCSession) writeError(id uint64, err error) {
	s.writeResponse(&desktoppb.Response{
		Id:    id,
		Error: err.Error(),
	})
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

func (s *RPCSession) handleRequest(req *desktoppb.Request) {
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
		err := s.app.SetWindowSize(int(m.SetWindowSize.Width), int(m.SetWindowSize.Height), m.SetWindowSize.Maximized)
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
		s.app.OpenFullDiskAccessSettings()
		s.writeOK(id)

	case *desktoppb.Request_ConnectSolo:
		if err := s.app.ConnectSolo(); err != nil {
			s.writeError(id, err)
			return
		}
		s.writeSidecarInfo(id)

	case *desktoppb.Request_ConnectDistributed:
		if err := s.app.ConnectDistributed(m.ConnectDistributed.HubUrl); err != nil {
			s.writeError(id, err)
			return
		}
		s.writeSidecarInfo(id)

	case *desktoppb.Request_ProxyHttp:
		resp, body, err := s.app.ProxyHTTP(m.ProxyHttp.Method, m.ProxyHttp.Path, m.ProxyHttp.Headers, m.ProxyHttp.Body)
		if err != nil {
			s.writeError(id, err)
			return
		}
		s.writeResponse(&desktoppb.Response{
			Id: id,
			Result: &desktoppb.Response_ProxyHttp{
				ProxyHttp: &desktoppb.ProxyHttpResponse{
					Status:  int32(resp.Status),
					Headers: resp.Headers,
					Body:    body,
				},
			},
		})

	case *desktoppb.Request_OpenChannelRelay:
		if err := s.app.OpenChannelRelay(); err != nil {
			s.writeError(id, err)
			return
		}
		s.writeOK(id)

	case *desktoppb.Request_SendChannelMessage:
		if err := s.app.SendChannelMessage(m.SendChannelMessage.Data); err != nil {
			s.writeError(id, err)
			return
		}
		s.writeOK(id)

	case *desktoppb.Request_CloseChannelRelay:
		if err := s.app.CloseChannelRelay(); err != nil {
			s.writeError(id, err)
			return
		}
		s.writeOK(id)

	case *desktoppb.Request_SwitchMode:
		if err := s.app.SwitchMode(); err != nil {
			s.writeError(id, err)
			return
		}
		s.writeSidecarInfo(id)

	case *desktoppb.Request_CreateTunnel:
		cfg := m.CreateTunnel.Config
		info, err := s.app.CreateTunnel(TunnelConfig{
			WorkerID:   cfg.WorkerId,
			Type:       cfg.Type,
			TargetAddr: cfg.TargetAddr,
			TargetPort: int(cfg.TargetPort),
			BindAddr:   cfg.BindAddr,
			BindPort:   int(cfg.BindPort),
			HubURL:     cfg.HubUrl,
			UserID:     cfg.UserId,
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
		if err := s.app.DeleteTunnel(m.DeleteTunnel.TunnelId); err != nil {
			s.writeError(id, err)
			return
		}
		s.writeOK(id)

	case *desktoppb.Request_ListTunnels:
		tunnels := s.app.ListTunnels()
		pbTunnels := make([]*desktoppb.TunnelInfo, len(tunnels))
		for i := range tunnels {
			pbTunnels[i] = tunnelInfoToProto(&tunnels[i])
		}
		s.writeResponse(&desktoppb.Response{
			Id: id,
			Result: &desktoppb.Response_ListTunnels{
				ListTunnels: &desktoppb.ListTunnelsResponse{
					Tunnels: pbTunnels,
				},
			},
		})

	case *desktoppb.Request_Shutdown:
		s.writeOK(id)
		if s.onShutdown != nil {
			go s.onShutdown()
		}

	default:
		s.writeError(id, fmt.Errorf("unknown method: %T", req.Method))
	}
}

func configToProto(cfg *DesktopConfig) *desktoppb.DesktopConfig {
	return &desktoppb.DesktopConfig{
		Mode:            cfg.Mode,
		HubUrl:          cfg.HubURL,
		WindowWidth:     int32(cfg.WindowWidth),
		WindowHeight:    int32(cfg.WindowHeight),
		WindowMaximized: cfg.WindowMaximized,
	}
}

func buildInfoToProto(info BuildInfo) *desktoppb.BuildInfo {
	return &desktoppb.BuildInfo{
		Version:    info.Version,
		CommitHash: info.CommitHash,
		CommitTime: info.CommitTime,
		BuildTime:  info.BuildTime,
	}
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
