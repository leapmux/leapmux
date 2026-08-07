//go:build windows

package locallisten

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
)

const pipePrefix = `\\.\pipe\`

// closeRetryInterval is how long Close waits for one winio close before it
// starts another, and closeAttempts is how many closes it starts. Together they
// bound how long Close can hold up the caller. See Close for why a retry is
// necessary and why a bound is.
const (
	closeRetryInterval = 250 * time.Millisecond
	closeAttempts      = 8
)

// errCloseStuck reports a listener routine that did not stop. The routine keeps
// the first pipe instance for the life of the process, so a later listen on the
// SAME name fails -- the desktop sidecar rebinds one fixed name after an idle
// shutdown, and that relisten is what a stuck listener costs. Report it; the
// alternative is a caller that never returns at all, which costs more.
var errCloseStuck = errors.New("npipe listener did not close")

func listenNpipe(name string) (net.Listener, error) {
	pipePath := fullPipePath(name)
	sddl, err := userOnlySDDL()
	if err != nil {
		return nil, fmt.Errorf("npipe listen: build security descriptor: %w", err)
	}
	listener, err := winio.ListenPipe(pipePath, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    65536,
		OutputBufferSize:   65536,
	})
	if err != nil {
		return nil, fmt.Errorf("npipe listen %s: %w", pipePath, err)
	}
	return newNpipeListener(listener), nil
}

// newNpipeListener wraps ln. It is the ONE construction site, so a listener a
// test builds carries the same close policy as a production one.
func newNpipeListener(ln net.Listener) *npipeListener {
	return &npipeListener{
		Listener:      ln,
		retryInterval: closeRetryInterval,
		conns:         make(map[*trackedPipeConn]struct{}),
	}
}

// npipeListener wraps winio.Listener so callers can uniformly check for
// "listener closed" via errors.Is(err, net.ErrClosed) regardless of the
// underlying transport. It also tracks accepted connections so they can be
// force-closed via CloseAccepted on shutdown — a WebSocket upgrade hijacks its
// connection, and net/http drops a hijacked conn from http.Server.activeConn,
// putting it out of reach of http.Server.Close(). (An h2c connection is not
// hijacked and stays in that map, so Close() does reach it.)
// On Windows that matters because each surviving accepted pipe handle is a
// "pipe instance" that blocks the next ListenPipe(FIRST_PIPE_INSTANCE).
type npipeListener struct {
	net.Listener
	// retryInterval paces Close. A field rather than the constant alone, so a
	// test drives the retry without a real wait.
	retryInterval time.Duration
	mu            sync.Mutex
	conns         map[*trackedPipeConn]struct{}
}

// Close stops the listener. It starts another close every retryInterval that
// the close before it does not return.
//
// The retry works around https://github.com/microsoft/go-winio/issues/85, open
// since 2018 and unfixed in v0.6.2, the newest release. The fix for it,
// https://github.com/microsoft/go-winio/pull/369, is open and unmerged. Delete
// this retry once a release carries that fix.
//
// The defect leaves a close blocked forever. winio's Close sends ONE token on
// the listener's private closeCh and then waits for doneCh. The token usually
// reaches the listener routine's own select, which stops the routine and closes
// doneCh. An accept that is already in flight takes the token first, in
// makeConnectedServerPipe, and THAT path rewrites the aborted connect's error to
// ErrPipeListenerClosed only when the connect returned nil or ErrFileClosed. Any
// other result -- the ERROR_NO_DATA that a connect-and-close client leaves
// behind, for example -- goes back to the routine, which computes
// "closed = err == ErrPipeListenerClosed", reads false, and continues to run.
// doneCh stays open, and the token that would have stopped the routine is spent.
// See pipe.go lines 440 to 485 of go-winio v0.6.2.
//
// A later token stops the routine from either state that it parks in. The
// routine's own select receives the token and sets closed. Alternatively
// makeConnectedServerPipe receives it and aborts a connect that no client
// satisfied, which does map to ErrPipeListenerClosed.
//
// Close must RETURN, which is why the attempts are bounded and a stuck listener
// is reported instead of waited out. http.Server.Shutdown closes its listeners
// while it holds srv.mu, so a close that blocks also blocks every Serve that
// returns and every connection that ends, and the whole http.Server deadlocks:
// Shutdown never reaches its own context deadline, because it blocks before it
// reads the context. The bound also covers a hang no token can release --
// https://github.com/microsoft/go-winio/issues/357, where the routine parks on
// the connect result itself rather than on a select. Close reports that listener
// and lets the caller continue.
func (l *npipeListener) Close() error {
	// One slot for each attempt: an attempt that returns late, after the routine
	// finally stops, must not park forever on its send.
	results := make(chan error, closeAttempts)
	timer := time.NewTimer(l.retryInterval)
	defer timer.Stop()
	for attempt := 1; ; attempt++ {
		go func() { results <- l.Listener.Close() }()
		select {
		case err := <-results:
			return err
		case <-timer.C:
			if attempt >= closeAttempts {
				return fmt.Errorf("%w: %s gave no answer to %d closes over %s",
					errCloseStuck, l.Addr(), closeAttempts, time.Duration(closeAttempts)*l.retryInterval)
			}
			slog.Warn("npipe listener close did not return; starting another",
				"pipe", l.Addr().String(), "attempt", attempt)
			timer.Reset(l.retryInterval)
		}
	}
}

func (l *npipeListener) Accept() (net.Conn, error) {
	raw, err := l.Listener.Accept()
	if errors.Is(err, winio.ErrPipeListenerClosed) {
		return nil, fmt.Errorf("%w: %w", net.ErrClosed, err)
	}
	if err != nil {
		return nil, err
	}
	c := &trackedPipeConn{Conn: raw, ln: l}
	l.mu.Lock()
	l.conns[c] = struct{}{}
	l.mu.Unlock()
	return c, nil
}

// CloseAccepted force-closes every accepted connection currently tracked
// by this listener. Safe to call after the listener itself has been closed.
func (l *npipeListener) CloseAccepted() {
	l.mu.Lock()
	conns := l.conns
	l.conns = make(map[*trackedPipeConn]struct{})
	l.mu.Unlock()
	// Close the raw conn directly instead of routing through
	// trackedPipeConn.Close — that path would re-lock l.mu just to delete
	// from a map we've already replaced. trackedPipeConn.closeOnce still
	// guards any later application-level Close from double-closing through
	// our wrapper; an extra raw Close is safely idempotent on winio pipes.
	for c := range conns {
		_ = c.Conn.Close()
	}
}

type trackedPipeConn struct {
	net.Conn
	ln        *npipeListener
	closeOnce sync.Once
}

func (c *trackedPipeConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.ln.mu.Lock()
		delete(c.ln.conns, c)
		c.ln.mu.Unlock()
		err = c.Conn.Close()
	})
	return err
}

func listenUnix(string) (net.Listener, error) {
	return nil, fmt.Errorf("%w: unix listener not supported on Windows", ErrUnsupportedScheme)
}

// fullPipePath accepts both "my-pipe" and a pre-qualified "\\.\pipe\my-pipe".
func fullPipePath(name string) string {
	if strings.HasPrefix(name, pipePrefix) {
		return name
	}
	return pipePrefix + name
}

// userOnlySDDL returns an SDDL granting Generic All only to the current user's SID.
func userOnlySDDL() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("O:%sG:%sD:(A;;GA;;;%s)", u.Uid, u.Uid, u.Uid), nil
}

func unixDialer(string) func(ctx context.Context) (net.Conn, error) {
	return func(context.Context) (net.Conn, error) {
		return nil, fmt.Errorf("%w: unix transport not supported on Windows", ErrUnsupportedScheme)
	}
}

func npipeDialer(name string) func(ctx context.Context) (net.Conn, error) {
	fullPath := fullPipePath(name)
	return func(ctx context.Context) (net.Conn, error) {
		return winio.DialPipeContext(ctx, fullPath)
	}
}
