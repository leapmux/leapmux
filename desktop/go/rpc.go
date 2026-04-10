package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"sync"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

type RPCServer struct {
	app    *App
	reader *bufio.Reader
	writer io.Writer
	mu     sync.Mutex
}

func NewRPCServer(reader io.Reader, writer io.Writer) *RPCServer {
	s := &RPCServer{
		reader: bufio.NewReader(reader),
		writer: writer,
	}
	s.app = NewApp(s.emitEvent)
	return s
}

func (s *RPCServer) Run() error {
	defer s.app.shutdown()

	for {
		frame, err := ReadFrame(s.reader)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return fmt.Errorf("read frame: %w", err)
		}

		req := frame.GetRequest()
		if req == nil {
			continue
		}
		// Dispatch in a goroutine so long-running handlers (ConnectSolo,
		// ProxyHTTP, etc.) don't block the read loop. writeResponse is
		// already mutex-protected.
		go s.handleRequest(req)
	}
}

func (s *RPCServer) emitEvent(event *desktoppb.Event) {
	s.writeFrame(&desktoppb.Frame{
		Message: &desktoppb.Frame_Event{Event: event},
	})
}

func (s *RPCServer) writeFrame(frame *desktoppb.Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := WriteFrame(s.writer, frame); err != nil {
		slog.Error("failed to write frame", "error", err)
	}
}

func (s *RPCServer) writeResponse(resp *desktoppb.Response) {
	s.writeFrame(&desktoppb.Frame{
		Message: &desktoppb.Frame_Response{Response: resp},
	})
}

func (s *RPCServer) writeError(id uint64, err error) {
	s.writeResponse(&desktoppb.Response{
		Id:    id,
		Error: err.Error(),
	})
}

func (s *RPCServer) writeOK(id uint64) {
	s.writeResponse(&desktoppb.Response{
		Id:     id,
		Result: &desktoppb.Response_BoolValue{BoolValue: &desktoppb.BoolValue{Value: true}},
	})
}

func (s *RPCServer) handleRequest(req *desktoppb.Request) {
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
		s.writeOK(id)

	case *desktoppb.Request_ConnectDistributed:
		if err := s.app.ConnectDistributed(m.ConnectDistributed.HubUrl); err != nil {
			s.writeError(id, err)
			return
		}
		s.writeResponse(&desktoppb.Response{
			Id: id,
			Result: &desktoppb.Response_ConnectDistributed{
				ConnectDistributed: &desktoppb.ConnectDistributedResponse{
					HubUrl: s.app.GetHubURL(),
				},
			},
		})

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
		s.writeOK(id)

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
