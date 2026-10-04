package providerkit

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ErrServerExited reports that the agent's server process ended before it
// stated the address it listens on.
var ErrServerExited = errors.New("the agent server exited before it started to listen")

// ListenWaiter waits for the stdout line with which an agent CLI states the
// address of the local server it started.
//
// The provider reads stdout with Process.ReadLines and hands every line to
// Observe, and its start path calls Wait. The first matching line wins; a
// later match -- a server that logs its address again -- changes nothing.
type ListenWaiter struct {
	pattern *regexp.Regexp

	once    sync.Once
	address string
	found   chan struct{}
}

// NewListenWaiter returns a waiter for lines that pattern matches. pattern must
// hold exactly one capture group: the address, as an http URL or as a bare
// host:port, which Wait reads as http.
func NewListenWaiter(pattern *regexp.Regexp) *ListenWaiter {
	if pattern.NumSubexp() != 1 {
		panic(fmt.Sprintf("providerkit.NewListenWaiter: pattern %q must hold exactly one capture group", pattern))
	}
	return &ListenWaiter{pattern: pattern, found: make(chan struct{})}
}

// Observe checks one stdout line. It reports whether this line stated the
// address for the first time.
func (w *ListenWaiter) Observe(line []byte) bool {
	match := w.pattern.FindSubmatch(line)
	if match == nil {
		return false
	}
	address := strings.TrimSpace(string(match[1]))
	if address == "" {
		return false
	}
	first := false
	w.once.Do(func() {
		if !strings.Contains(address, "://") {
			address = "http://" + address
		}
		w.address = address
		close(w.found)
		first = true
	})
	return first
}

// Wait returns the stated address. It fails when exited closes first (the
// process ended), when the timeout elapses, or when ctx ends.
//
// A caller passes the address to NewHTTPEndpoint, which refuses anything but a
// loopback http address.
func (w *ListenWaiter) Wait(ctx context.Context, exited <-chan struct{}, timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	// The address wins a tie with the exit: a server that printed its address and
	// then died is reported by the first request, with the reason that request
	// gets, rather than as a server that never started.
	select {
	case <-w.found:
		return w.address, nil
	default:
	}
	select {
	case <-w.found:
		return w.address, nil
	case <-exited:
		select {
		case <-w.found:
			return w.address, nil
		default:
		}
		return "", ErrServerExited
	case <-timer.C:
		return "", fmt.Errorf("the agent server stated no address within %s", timeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// ReserveLoopbackPort returns a TCP port on 127.0.0.1 that was free a moment
// ago, for a CLI that refuses port 0 and must be told its port.
//
// Another process can take the port between this call and the CLI's bind. The
// CLI then fails to start, and the start reports that failure; a retry picks a
// new port. A CLI that accepts port 0 and states the port it bound needs none of
// this -- use ListenWaiter instead.
func ReserveLoopbackPort() (int, error) {
	return reserveLoopbackPort(net.Listen)
}

// reserveLoopbackPort is ReserveLoopbackPort with the listen function as a
// parameter. A test states a fake listener, because no bind of the real port can
// prove the release: another process can take the port first.
func reserveLoopbackPort(listen func(network, address string) (net.Listener, error)) (int, error) {
	listener, err := listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve a loopback port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release the reserved loopback port: %w", err)
	}
	return port, nil
}

// NewServerSecret returns a random credential for one agent's local server:
// 32 bytes from the system's secure source, as unpadded base64url text.
//
// A fresh secret for each agent keeps another local process from driving the
// server. The secret reaches the server through its environment, never through
// argv, where any local user could read it from the process list.
func NewServerSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate a server secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
