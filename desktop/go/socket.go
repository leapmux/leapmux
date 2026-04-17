package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/leapmux/leapmux/locallisten"
)

// sidecarIdleTimeout is the duration of quiet between sessions after which
// the sidecar shuts itself down. A variable (not const) so tests can shorten it.
var sidecarIdleTimeout = 15 * time.Second

// RunSocketServer listens on endpoint (a raw Unix socket path or Windows pipe
// name; the scheme is prepended automatically) and serves one RPC session at
// a time. Shuts the sidecar down when no client has been attached for
// sidecarIdleTimeout.
func RunSocketServer(endpoint string, binaryHash string) error {
	listenURL, err := prepareEndpoint(endpoint)
	if err != nil {
		return err
	}
	listener, err := locallisten.Listen(listenURL)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listenURL, err)
	}
	defer func() { _ = listener.Close() }()

	app := NewApp(binaryHash)
	defer app.Shutdown()

	go func() {
		<-app.ctx.Done()
		_ = listener.Close()
	}()

	// Single-owner idle timer: the goroutine that reads from timer.C also
	// drives Stop/Reset via busyStart/busyEnd, so the accept loop never
	// races the timer. The timer is stopped for the full duration of a
	// session and re-armed only when the session ends, so long-lived
	// connections don't trip the idle deadline.
	busyStart := make(chan struct{}, 1)
	busyEnd := make(chan struct{}, 1)
	go func() {
		timer := time.NewTimer(sidecarIdleTimeout)
		defer timer.Stop()
		for {
			select {
			case <-busyStart:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				select {
				case <-busyEnd:
					timer.Reset(sidecarIdleTimeout)
				case <-app.ctx.Done():
					return
				}
			case <-timer.C:
				slog.Info("desktop sidecar idle timeout reached; shutting down")
				app.Shutdown()
				return
			case <-app.ctx.Done():
				return
			}
		}
	}()

	signal := func(ch chan<- struct{}) {
		select {
		case ch <- struct{}{}:
		default:
		}
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			if app.ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		signal(busyStart)

		session := NewRPCSession(app, conn, conn, func() {
			app.Shutdown()
			_ = conn.Close()
		})
		runErr := session.Run()
		_ = conn.Close()
		if runErr != nil {
			return runErr
		}

		signal(busyEnd)
	}
}
