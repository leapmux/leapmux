//go:build windows

package locallisten

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/user"
	"strings"

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
	return &npipeListener{Listener: listener}, nil
}

// npipeListener wraps winio.Listener so callers can uniformly check for
// "listener closed" via errors.Is(err, net.ErrClosed) regardless of the
// underlying transport.
type npipeListener struct {
	net.Listener
}

func (l *npipeListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if errors.Is(err, winio.ErrPipeListenerClosed) {
		return nil, fmt.Errorf("%w: %w", net.ErrClosed, err)
	}
	return conn, err
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
