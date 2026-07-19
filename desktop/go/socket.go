package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
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
func RunSocketServer(endpoint string, app *App) error {
	listenURL, err := prepareEndpoint(endpoint)
	if err != nil {
		return err
	}
	listener, err := locallisten.Listen(listenURL)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listenURL, err)
	}
	relisten := func() (net.Listener, error) {
		replacement, listenErr := locallisten.Listen(listenURL)
		if listenErr != nil {
			return nil, fmt.Errorf("relisten %s: %w", listenURL, listenErr)
		}
		return replacement, nil
	}
	return runSocketServer(listener, app, relisten, nil)
}

func runSocketServer(
	listener net.Listener,
	app *App,
	relisten func() (net.Listener, error),
	afterAccept func(),
) error {
	stopWatchingShutdown := closeListenerOnShutdown(app.ctx.Done(), listener)
	// The defer re-reads `listener` rather than capturing it: the idle-deadline
	// path below REBINDS the variable via relisten, so the value to close is
	// whatever the loop last installed. Only a successful relisten is stored, so
	// this can never be the nil listener a failed one returns -- calling Close on
	// a nil net.Listener interface would panic the sidecar, and a panic skips
	// main.run's deferred app.Shutdown, orphaning the Hub's revocation runtime
	// lease until its TTL fences the next launch.
	defer func() {
		stopWatchingShutdown()
		_ = listener.Close()
	}()

	for {
		conn, idleDeadlineReached, err := acceptBeforeIdleDeadline(
			listener,
			app.ctx.Done(),
			sidecarIdleTimeout,
			afterAccept,
		)
		if idleDeadlineReached && conn == nil {
			// The idle timer closing the listener turns the pending Accept into
			// net.ErrClosed -- the expected companion of an idle shutdown. Any OTHER
			// error means a real listener fault happened to land at the same instant;
			// the outcome is the same (shut down), but surface it so a control-socket
			// fault is not silently logged as a routine idle timeout.
			if err != nil && !errors.Is(err, net.ErrClosed) {
				slog.Warn("desktop sidecar idle timeout reached after a control socket accept error; shutting down",
					"error", err)
			} else {
				slog.Info("desktop sidecar idle timeout reached; shutting down")
			}
			_ = app.Shutdown()
			return nil
		}
		if err != nil {
			if app.ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if app.ctx.Err() != nil {
			// acceptBeforeIdleDeadline's shutdown case closes any connection it
			// accepted mid-shutdown and returns a nil conn; guard the Close so an
			// external shutdown (Shutdown RPC, SIGTERM) while idle-blocked does
			// not dereference a nil net.Conn.
			if conn != nil {
				_ = conn.Close()
			}
			return nil
		}

		session := NewRPCSession(app, conn, conn, func() {
			_ = app.Shutdown()
			_ = conn.Close()
		})
		runErr := session.Run()
		_ = conn.Close()
		if runErr != nil {
			return runErr
		}

		if idleDeadlineReached {
			stopWatchingShutdown()
			if app.ctx.Err() != nil {
				return nil
			}
			if relisten == nil {
				return fmt.Errorf("socket listener closed while accepting a connection at the idle deadline")
			}
			// Assign only on success: relisten returns (nil, err) when the socket
			// path is gone/taken or fds are exhausted, and clobbering `listener`
			// with that nil would make the deferred Close above nil-deref.
			replacement, relistenErr := relisten()
			if relistenErr != nil {
				return relistenErr
			}
			listener = replacement
			stopWatchingShutdown = closeListenerOnShutdown(app.ctx.Done(), listener)
		}
	}
}

type socketAcceptResult struct {
	conn net.Conn
	err  error
}

func acceptBeforeIdleDeadline(
	listener net.Listener,
	shutdown <-chan struct{},
	timeout time.Duration,
	afterAccept func(),
) (net.Conn, bool, error) {
	result := make(chan socketAcceptResult, 1)
	go func() {
		retryDelay := acceptRetryDelayMin
		for {
			conn, err := listener.Accept()
			if err != nil && isTemporaryAcceptError(err) {
				// A transient fd/buffer exhaustion (EMFILE/ENOBUFS -- e.g. a tunnel
				// workload saturating the process's fd table) must not kill the
				// control socket the shell depends on: back off and re-accept, the
				// same policy the tunnel accept loops apply via acceptWithRetry.
				// The idle-timer and shutdown arms of the select below close the
				// listener, which turns the NEXT Accept into net.ErrClosed (not
				// temporary), so this loop cannot outlive them by more than one
				// backoff step.
				slog.Warn("desktop control socket accept failed; retrying",
					"error", err, "retry_in", retryDelay)
				var cancelled bool
				if retryDelay, cancelled = backoffAfterTemporaryAccept(retryDelay, shutdown); cancelled {
					result <- socketAcceptResult{err: err}
					return
				}
				continue
			}
			if conn != nil && afterAccept != nil {
				afterAccept()
			}
			result <- socketAcceptResult{conn: conn, err: err}
			return
		}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case accepted := <-result:
		return accepted.conn, false, accepted.err
	case <-timer.C:
		_ = listener.Close()
		accepted := <-result
		return accepted.conn, true, accepted.err
	case <-shutdown:
		_ = listener.Close()
		accepted := <-result
		if accepted.conn != nil {
			_ = accepted.conn.Close()
		}
		return nil, false, nil
	}
}

func closeListenerOnShutdown(shutdown <-chan struct{}, listener net.Listener) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-shutdown:
			_ = listener.Close()
		case <-stop:
		}
	}()
	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() { close(stop) })
		<-done
	}
}
