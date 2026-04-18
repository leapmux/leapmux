// Package locallisten hides the difference between Unix domain sockets and
// Windows named pipes behind a small URL-based API. A local-listen URL has
// the form "unix:<path>" or "npipe:<name>"; callers pass the string through
// without caring which platform they're on, and per-scheme implementations
// below do the right thing or return a clear ErrUnsupportedScheme error.
package locallisten

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/net/http2"
)

// Scheme identifies a local IPC transport.
type Scheme string

// Known schemes.
const (
	SchemeUnix  Scheme = "unix"
	SchemeNpipe Scheme = "npipe"
)

// ErrUnsupportedScheme is returned when the URL's scheme is unknown or not
// implemented on the current platform (e.g. npipe on Unix or unix on Windows).
var ErrUnsupportedScheme = errors.New("unsupported local-listen scheme")

// ErrMissingTarget is returned when the URL has a known scheme but the
// trailing path/name is empty (e.g. "unix:", "npipe:").
var ErrMissingTarget = errors.New("missing target after scheme")

// EnvLocalListen is the env-var form of the hub's --local-listen flag.
const EnvLocalListen = "LEAPMUX_HUB_LOCAL_LISTEN"

// Parse splits a local-listen URL into its scheme and target components.
// Accepted forms: "unix:<path>", "npipe:<name>", "npipe:<full-nt-path>".
// The target is returned verbatim.
func Parse(url string) (Scheme, string, error) {
	if url == "" {
		return "", "", fmt.Errorf("%w: empty url", ErrUnsupportedScheme)
	}
	for _, scheme := range []Scheme{SchemeUnix, SchemeNpipe} {
		prefix := string(scheme) + ":"
		if target, ok := strings.CutPrefix(url, prefix); ok {
			if target == "" {
				return "", "", fmt.Errorf("%w: %s", ErrMissingTarget, prefix)
			}
			return scheme, target, nil
		}
	}
	return "", "", fmt.Errorf("%w: %q (expected unix:<path> or npipe:<name>)", ErrUnsupportedScheme, url)
}

// IsLocal reports whether url uses a local-IPC scheme (unix: or npipe:).
func IsLocal(url string) bool {
	_, _, err := Parse(url)
	return err == nil
}

// Dialer returns a function that opens a fresh connection to the endpoint
// encoded by url. Schemes the current platform can't implement return a
// dialer that always fails with ErrUnsupportedScheme.
func Dialer(url string) (func(ctx context.Context) (net.Conn, error), error) {
	scheme, target, err := Parse(url)
	if err != nil {
		return nil, err
	}
	switch scheme {
	case SchemeUnix:
		return unixDialer(target), nil
	case SchemeNpipe:
		return npipeDialer(target), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedScheme, scheme)
	}
}

// NewLocalH2CTransport wraps a pre-bound local-IPC dialer (unix:/npipe:) in
// an HTTP/2-cleartext transport suitable for gRPC bidi streaming.
func NewLocalH2CTransport(dial func(ctx context.Context) (net.Conn, error)) *http2.Transport {
	return &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
			return dial(ctx)
		},
	}
}

// HTTPDialContext adapts a pre-bound dialer to net/http's DialContext
// signature. The network/addr args are ignored.
func HTTPDialContext(dial func(ctx context.Context) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dial(ctx)
	}
}

// Listen opens a listener for the URL. Unsupported schemes return
// ErrUnsupportedScheme wrapped with a descriptive message.
func Listen(url string) (net.Listener, error) {
	scheme, target, err := Parse(url)
	if err != nil {
		return nil, err
	}
	switch scheme {
	case SchemeUnix:
		return listenUnix(target)
	case SchemeNpipe:
		return listenNpipe(target)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedScheme, scheme)
	}
}

// WaitReady polls until a client can dial the listener at url, or ctx is
// cancelled.
func WaitReady(ctx context.Context, url string) error {
	dial, err := Dialer(url)
	if err != nil {
		return err
	}
	const (
		overallTimeout = 5 * time.Second
		backoff        = 100 * time.Millisecond
		probeTimeout   = 100 * time.Millisecond
	)
	waitCtx, cancel := context.WithTimeout(ctx, overallTimeout)
	defer cancel()
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	for {
		probeCtx, probeCancel := context.WithTimeout(waitCtx, probeTimeout)
		conn, err := dial(probeCtx)
		probeCancel()
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%s not ready after %s", url, overallTimeout)
		case <-timer.C:
			timer.Reset(backoff)
		}
	}
}
