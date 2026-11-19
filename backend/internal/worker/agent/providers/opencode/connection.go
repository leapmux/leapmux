package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const (
	openCodeReconnectFirst    = 100 * time.Millisecond
	openCodeReconnectMax      = 5 * time.Second
	openCodeStableConnection  = 30 * time.Second
	openCodeReconnectTimerTag = "opencode-stream-reconnect"
	openCodeConnectTimerTag   = "opencode-stream-connect"
)

// openCodeStream has one reader and dispatcher. Recovery runs on that same
// goroutine before it consumes events buffered by the replacement connection.
type openCodeStream struct {
	rpc             openCodeRPC
	clock           quartz.Clock
	agentID         string
	processDone     <-chan struct{}
	restoreSessions func(context.Context) error
	dispatch        func(context.Context, json.RawMessage) error
	cancel          context.CancelFunc
	done            chan struct{}
	connected       chan struct{}
	connectedOnce   sync.Once
	mu              sync.Mutex
	lastError       error
	readCancel      context.CancelFunc
}

type openCodeStreamConfig struct {
	rpc             openCodeRPC
	clock           quartz.Clock
	agentID         string
	processDone     <-chan struct{}
	restoreSessions func(context.Context) error
	dispatch        func(context.Context, json.RawMessage) error
}

func startOpenCodeStream(ctx context.Context, config openCodeStreamConfig) (*openCodeStream, error) {
	if config.rpc.endpoint == nil || config.clock == nil || config.dispatch == nil {
		return nil, errors.New("an OpenCode event stream requires an endpoint, clock, and event dispatcher")
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &openCodeStream{
		rpc: config.rpc, clock: config.clock, agentID: config.agentID, processDone: config.processDone,
		restoreSessions: config.restoreSessions, dispatch: config.dispatch, cancel: cancel,
		done: make(chan struct{}), connected: make(chan struct{}),
	}
	go s.run(ctx)
	return s, nil
}

func (s *openCodeStream) stop() { s.cancel() }

// requestRecovery interrupts the current read so its replacement restores
// native snapshots. It preserves the server process and the stream lifetime.
func (s *openCodeStream) requestRecovery() {
	s.mu.Lock()
	cancel := s.readCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *openCodeStream) wait() { <-s.done }

func (s *openCodeStream) awaitConnected(ctx context.Context, timeout time.Duration) error {
	timer := s.clock.NewTimer(timeout, openCodeConnectTimerTag)
	defer timer.Stop(openCodeConnectTimerTag)
	select {
	case <-s.connected:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.processDone:
		return errors.New("OpenCode exited before its event stream connected")
	case <-s.done:
		return s.connectionError("the OpenCode event stream ended before connecting")
	case <-timer.C:
		return s.connectionError("the OpenCode event stream did not connect before the startup deadline")
	}
}

func (s *openCodeStream) connectionError(message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastError != nil {
		return fmt.Errorf("%s: %w", message, s.lastError)
	}
	return errors.New(message)
}

func (s *openCodeStream) run(ctx context.Context) {
	defer close(s.done)
	delay := openCodeReconnectFirst
	for {
		started := s.clock.Now()
		err := s.consume(ctx)
		if ctx.Err() != nil {
			return
		}
		select {
		case <-s.processDone:
			return
		default:
		}
		s.mu.Lock()
		s.lastError = err
		s.mu.Unlock()
		// Authentication and an absent route cannot improve on this server.
		if providerkit.IsHTTPStatus(err, http.StatusUnauthorized) || providerkit.IsHTTPStatus(err, http.StatusForbidden) || providerkit.IsHTTPStatus(err, http.StatusNotFound) {
			return
		}
		if s.clock.Now().Sub(started) >= openCodeStableConnection {
			delay = openCodeReconnectFirst
		}
		slog.Debug("OpenCode event stream ended; reconnecting", "agent_id", s.agentID, "error", err, "delay", delay)
		timer := s.clock.NewTimer(delay, openCodeReconnectTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop(openCodeReconnectTimerTag)
			return
		case <-s.processDone:
			timer.Stop(openCodeReconnectTimerTag)
			return
		case <-timer.C:
		}
		delay = min(delay*2, openCodeReconnectMax)
	}
}

func (s *openCodeStream) consume(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.readCancel = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.readCancel = nil
		s.mu.Unlock()
	}()
	response, err := s.rpc.endpoint.OpenStream(ctx, http.MethodGet, "/api/event", http.Header{"Accept": {"text/event-stream"}})
	if err != nil {
		return err
	}
	connectionReady := false
	err = readOpenCodeSSE(response.Body, func(event providerkit.SSEEvent) error {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(event.Data, &header); err != nil {
			return fmt.Errorf("decode an OpenCode event: %w", err)
		}
		if header.Type == "server.connected" {
			if connectionReady {
				return errors.New("OpenCode repeated the event-stream greeting")
			}
			connectionReady = true
			first := false
			s.connectedOnce.Do(func() { first = true; close(s.connected) })
			if !first && s.restoreSessions != nil {
				return s.restoreSessions(ctx)
			}
			return nil
		}
		if !connectionReady {
			return errors.New("OpenCode sent an event before its stream greeting")
		}
		if header.Type == "" {
			return errors.New("the OpenCode event has no type")
		}
		return s.dispatch(ctx, append(json.RawMessage(nil), event.Data...))
	})
	if err != nil {
		return err
	}
	return io.EOF
}
