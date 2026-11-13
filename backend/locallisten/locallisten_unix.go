//go:build unix

package locallisten

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"syscall"
	"time"
)

func listenUnix(path string) (net.Listener, error) {
	return listenUnixWithPermissions(path, os.Chmod)
}

// The permission operation is a private seam for checking connections queued
// between bind and chmod. Production always supplies os.Chmod.
func listenUnixWithPermissions(path string, chmod func(string, os.FileMode) error) (net.Listener, error) {
	if err := unixPeerCredentialsAvailable(); err != nil {
		return nil, err
	}
	if limit := MaxUnixSocketPathBytes(); len(path) > limit {
		return nil, fmt.Errorf("%w: %q is %d bytes; the limit is %d", ErrSocketPathTooLong, path, len(path), limit)
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, fmt.Errorf("unix listen: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("unix listen %s: %w", path, err)
	}
	if err := chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("unix listen: chmod %s: %w", path, err)
	}
	return &unixListener{listener: listener, readPeerUID: peerUID}, nil
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
		conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return fmt.Errorf("%s is already in use", path)
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return fmt.Errorf("probe existing socket %s: %w", path, dialErr)
		}
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

// unixListener admits only the account that runs this process. Its native
// listener remains the sole owner of socket unlinking. Do not expose AcceptUnix:
// every returned connection must pass the same credential check.
type unixListener struct {
	listener    *net.UnixListener
	readPeerUID func(*net.UnixConn) (uint32, error)
}

func (l *unixListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.listener.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, err := l.readPeerUID(connection)
		if err != nil {
			return nil, fmt.Errorf("unix accept: read peer credentials: %w", errors.Join(err, connection.Close()))
		}
		if uid != uint32(os.Geteuid()) {
			if err := connection.Close(); err != nil {
				return nil, fmt.Errorf("unix accept: close refused peer: %w", err)
			}
			continue
		}
		return connection, nil
	}
}

func (l *unixListener) Addr() net.Addr { return l.listener.Addr() }

// Close delegates unlinking to the native listener. Repeated closes cannot
// remove a replacement socket that now occupies the same pathname.
func (l *unixListener) Close() error { return l.listener.Close() }

func peerUID(connection *net.UnixConn) (uint32, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var credentialErr error
	if err := raw.Control(func(fd uintptr) { uid, credentialErr = socketPeerUID(fd) }); err != nil {
		return 0, err
	}
	if credentialErr != nil {
		return 0, credentialErr
	}
	return uid, nil
}
