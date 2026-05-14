package locallisten

import (
	"net/http"
	"strings"
	"time"
)

// LocalConnectURL is the placeholder base URL ConnectRPC and net/http
// see when targeting a local-IPC hub. Both http2.Transport and
// http.Transport reject any URL whose scheme isn't http(s); the dialer
// is wired into the transport, so the host portion is purely cosmetic.
const LocalConnectURL = "http://localhost"

// LocalH2CClient returns an HTTP/2 cleartext client and the
// placeholder base URL for the local-IPC hubURL. Caller must verify
// IsLocal(hubURL) first; passing a remote URL returns
// ErrUnsupportedScheme via Parse.
//
// timeout caps individual requests (0 = no timeout). The h2c
// transport is appropriate for ConnectRPC streaming and unary calls
// over the per-agent socket.
func LocalH2CClient(hubURL string, timeout time.Duration) (*http.Client, string, error) {
	dial, err := Dialer(hubURL)
	if err != nil {
		return nil, "", err
	}
	return &http.Client{
		Transport: NewLocalH2CTransport(dial),
		Timeout:   timeout,
	}, LocalConnectURL, nil
}

// LocalHTTPClient returns an HTTP/1.1 client and the placeholder base
// URL for the local-IPC hubURL. Caller must verify IsLocal(hubURL)
// first; passing a remote URL returns ErrUnsupportedScheme via Parse.
//
// timeout caps individual requests (0 = no timeout). The HTTP/1.1
// transport is appropriate for plain REST endpoints and for
// WebSocket upgrade (which doesn't ride on HTTP/2 streams).
func LocalHTTPClient(hubURL string, timeout time.Duration) (*http.Client, string, error) {
	dial, err := Dialer(hubURL)
	if err != nil {
		return nil, "", err
	}
	return &http.Client{
		Transport: &http.Transport{DialContext: HTTPDialContext(dial)},
		Timeout:   timeout,
	}, LocalConnectURL, nil
}

// JoinPath joins a path onto a hub base URL, normalising any
// trailing slash on the base. Callers compose endpoint URLs from a
// user-supplied --hub flag whose value may or may not end in "/".
func JoinPath(hubURL, path string) string {
	return strings.TrimRight(hubURL, "/") + path
}

// SelectClient picks between a local-IPC client and a remote-transport
// client for hubURL. When hubURL is local-IPC and `localBuild`
// succeeds, returns its (client, base) pair (base is the placeholder
// URL the local transport dials internally); otherwise falls through
// to `remoteBuild` which returns the verbatim-URL pair. Centralises
// the if-IsLocal/try-local/fallback dance every per-transport factory
// across worker/hub/delegation had open-coded.
func SelectClient(
	hubURL string,
	localBuild func() (*http.Client, string, error),
	remoteBuild func() (*http.Client, string),
) (*http.Client, string) {
	if IsLocal(hubURL) {
		if c, base, err := localBuild(); err == nil {
			return c, base
		}
	}
	return remoteBuild()
}
