//go:build windows

package locallisten

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/user"
	"strings"
	"sync"

	"github.com/Microsoft/go-winio"
)

const pipePrefix = `\\.\pipe\`

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
	return &npipeListener{
		Listener: listener,
		conns:    make(map[*trackedPipeConn]struct{}),
	}, nil
}

// npipeListener wraps winio.Listener so callers can uniformly check for
// "listener closed" via errors.Is(err, net.ErrClosed) regardless of the
// underlying transport. It also tracks accepted connections so they can be
// force-closed via CloseAccepted on shutdown — net/http hands h2c-upgraded
// connections to http2.Server via Hijack, which removes them from
// http.Server.activeConn and puts them out of reach of http.Server.Close().
// On Windows that matters because each surviving accepted pipe handle is a
// "pipe instance" that blocks the next ListenPipe(FIRST_PIPE_INSTANCE).
type npipeListener struct {
	net.Listener
	mu    sync.Mutex
	conns map[*trackedPipeConn]struct{}
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
