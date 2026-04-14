//go:build unix

package main

import (
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"
)

const sidecarIdleTimeout = 15 * time.Second

func RunSocketServer(socketPath string, binaryHash string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	_ = os.Chmod(socketPath, 0o600)

	app := NewApp(binaryHash)
	defer app.Shutdown()

	go func() {
		<-app.ctx.Done()
		_ = listener.Close()
	}()

	idleTimer := time.NewTimer(sidecarIdleTimeout)
	defer idleTimer.Stop()

	go func() {
		<-idleTimer.C
		slog.Info("desktop sidecar idle timeout reached; shutting down")
		app.Shutdown()
	}()

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

		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}

		session := NewRPCSession(app, conn, conn, func() {
			app.Shutdown()
			_ = conn.Close()
		})
		runErr := session.Run()
		_ = conn.Close()
		if runErr != nil {
			return runErr
		}

		idleTimer.Reset(sidecarIdleTimeout)
	}
}
