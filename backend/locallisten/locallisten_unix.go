//go:build unix

package locallisten

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
)

func listenUnix(path string) (net.Listener, error) {
	if err := removeStaleSocket(path); err != nil {
		return nil, fmt.Errorf("unix listen: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("unix listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("unix listen: chmod %s: %w", path, err)
	}
	return &unixListener{Listener: listener, path: path}, nil
}

// unixListener unlinks the socket file when the listener closes.
type unixListener struct {
	net.Listener
	path string
}

func (l *unixListener) Close() error {
	err := l.Listener.Close()
	_ = os.Remove(l.path)
	return err
}

func listenNpipe(name string) (net.Listener, error) {
	return nil, fmt.Errorf("%w: npipe listener requires Windows", ErrUnsupportedScheme)
}

// removeStaleSocket unlinks a leftover socket file, refusing to touch
// a non-socket path so user data can't be clobbered.
func removeStaleSocket(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode().Type() == fs.ModeSocket {
		return os.Remove(path)
	}
	return fmt.Errorf("%s exists but is not a socket", path)
}

func unixDialer(path string) func(ctx context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}
}

func npipeDialer(string) func(ctx context.Context) (net.Conn, error) {
	return func(context.Context) (net.Conn, error) {
		return nil, fmt.Errorf("%w: npipe transport requires Windows", ErrUnsupportedScheme)
	}
}
