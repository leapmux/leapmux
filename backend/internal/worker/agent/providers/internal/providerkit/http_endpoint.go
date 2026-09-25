package providerkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

// MaxEndpointResponseBytes limits one response body that HTTPEndpoint.Do reads.
// A session snapshot with a long history is the largest reply a local agent
// server sends, and it stays far below this. The limit stops a runaway reply
// from exhausting the worker's memory.
const MaxEndpointResponseBytes = 64 << 20

// maxErrorBodyBytes limits the part of an error reply that HTTPStatusError
// keeps. The reason is at the start of the body, and a log line has no use for
// the rest.
const maxErrorBodyBytes = 4 << 10

// EndpointAuth adds a local server's credential to one request.
type EndpointAuth func(*http.Request)

// BearerAuth sends the token as an `Authorization: Bearer` header.
func BearerAuth(token string) EndpointAuth {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

// BasicAuth sends the user and the password as HTTP basic authentication.
func BasicAuth(user, password string) EndpointAuth {
	return func(r *http.Request) { r.SetBasicAuth(user, password) }
}

// HTTPEndpoint addresses the local HTTP server that an agent CLI runs for the
// life of one agent, for a provider whose protocol runs over HTTP rather than
// over the process's stdio.
//
// It accepts a LOOPBACK http address only. The server holds the agent's
// session and runs tools on the user's machine, and the credential that
// LeapMux sends it would reach any other host as well. Three more rules keep
// every request on the machine:
//
//   - The dedicated transport uses no proxy. A proxy from the environment would
//     see both the credential and the traffic.
//   - The client follows no redirect. A local agent API has no reason to send
//     one, and a redirect is the one way a reply can move a request, its body
//     and its fixed headers to another address. A 3xx reply returns an
//     HTTPStatusError instead.
//   - The dialer refuses a resolved address that is not loopback. The URL check
//     accepts the NAME `localhost`, and the system resolver answers that name.
type HTTPEndpoint struct {
	base *url.URL
	auth EndpointAuth
	// header holds the fixed headers that WithHeader added. Every request carries
	// them. It is never changed after construction, so a copy can share it with
	// no lock.
	header http.Header
	client *http.Client
}

// NewHTTPEndpoint validates base and returns an endpoint for it. auth may be
// nil for a server that takes no credential.
func NewHTTPEndpoint(base string, auth EndpointAuth) (*HTTPEndpoint, error) {
	u, err := ParseLoopbackHTTPURL(base)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = loopbackDialer().DialContext
	return &HTTPEndpoint{
		base: u,
		auth: auth,
		// No client Timeout: a request takes its deadline from its context, and a
		// stream stays open for the whole session.
		client: &http.Client{Transport: transport, CheckRedirect: refuseRedirect},
	}, nil
}

// refuseRedirect makes the client return a 3xx reply as it is, so checkStatus
// reports it and no request follows it. See HTTPEndpoint.
func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// loopbackDialer returns a dialer with the timeouts of http.DefaultTransport
// that checks each address it connects to. See HTTPEndpoint.
func loopbackDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   refuseNonLoopbackAddress,
	}
}

// refuseNonLoopbackAddress runs after the resolver and before the connection
// starts, so address is the IP address that the dialer really connects to.
func refuseNonLoopbackAddress(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("the local server address %s cannot be read: %w", address, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("the local server address %s is not a loopback address", address)
	}
	return nil
}

// ParseLoopbackHTTPURL parses an http URL and refuses anything but a loopback
// host with an explicit port, with no user info, query or fragment. It drops a
// trailing slash from the path, so a caller appends a path that starts with one.
func ParseLoopbackHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("parse the local server address %q: %w", raw, err)
	}
	if u.Scheme != "http" {
		return nil, fmt.Errorf("the local server address %q is not an http URL", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("the local server address %q holds user info, a query or a fragment", raw)
	}
	if u.Port() == "" {
		return nil, fmt.Errorf("the local server address %q has no port", raw)
	}
	if !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("the local server address %q is not a loopback address", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// WithHeader returns an endpoint that sends header name with value on every
// request, for a server that scopes each request by a header, such as the
// directory a session belongs to.
//
// The receiver does not change. The copy shares the receiver's connection pool
// and credential, so Close on either one releases the idle connections of both.
// A per-request header that OpenStream receives with the same name replaces the
// fixed value for that request.
func (e *HTTPEndpoint) WithHeader(name, value string) *HTTPEndpoint {
	copied := *e
	copied.header = e.header.Clone()
	if copied.header == nil {
		copied.header = http.Header{}
	}
	copied.header.Set(name, value)
	return &copied
}

// Close releases the idle connections to the server. Call it when the agent
// stops. A stream that is still open ends with its own context.
func (e *HTTPEndpoint) Close() {
	e.client.CloseIdleConnections()
}

// BaseURL returns the server's base address.
func (e *HTTPEndpoint) BaseURL() string {
	return e.base.String()
}

// URL returns the address of path on the server, with query appended when it
// is non-empty. path starts with a slash.
func (e *HTTPEndpoint) URL(path string, query url.Values) string {
	u := *e.base
	u.Path = e.base.Path + path
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

// HTTPStatusError reports a reply whose status is not 2xx.
type HTTPStatusError struct {
	Method     string
	Path       string
	StatusCode int
	Status     string
	// Body is the start of the reply body, which holds the server's reason.
	Body string
}

func (e *HTTPStatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s %s: %s", e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("%s %s: %s: %s", e.Method, e.Path, e.Status, e.Body)
}

// IsHTTPStatus reports whether err is an HTTPStatusError with the given code.
func IsHTTPStatus(err error, code int) bool {
	var statusErr *HTTPStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode == code
}

// Do sends one JSON request and decodes a JSON reply into out.
//
// body is marshaled as the request body when it is non-nil. out may be nil to
// discard the reply, and an empty reply leaves out unchanged. A non-2xx reply
// returns an HTTPStatusError. The request takes its deadline from ctx.
func (e *HTTPEndpoint) Do(ctx context.Context, method, path string, body, out any) error {
	return e.DoQuery(ctx, method, path, nil, body, out)
}

// DoQuery is Do with query parameters, which URL appends to path when query is
// non-empty. A caller passes the parameters here rather than in path, because
// the path is escaped as a path and a `?` inside it would reach the server as
// part of the path.
func (e *HTTPEndpoint) DoQuery(ctx context.Context, method, path string, query url.Values, body, out any) error {
	response, err := e.send(ctx, method, path, query, nil, body)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if err := checkStatus(method, path, response); err != nil {
		return err
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, MaxEndpointResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%s %s: read the reply: %w", method, path, err)
	}
	if len(payload) > MaxEndpointResponseBytes {
		return fmt.Errorf("%s %s: the reply exceeds %d bytes", method, path, MaxEndpointResponseBytes)
	}
	if out == nil || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("%s %s: decode the reply: %w", method, path, err)
	}
	return nil
}

// OpenStream sends a request whose reply is a long-lived stream, such as an
// event stream, and returns the open reply for a 2xx status. The caller reads
// and closes its body; cancelling ctx ends the stream. header adds headers to
// the request, and may be nil.
func (e *HTTPEndpoint) OpenStream(ctx context.Context, method, path string, header http.Header) (*http.Response, error) {
	return e.OpenStreamQuery(ctx, method, path, nil, header)
}

// OpenStreamQuery is OpenStream with query parameters, for a stream route that
// takes its resume position in the query.
func (e *HTTPEndpoint) OpenStreamQuery(ctx context.Context, method, path string, query url.Values, header http.Header) (*http.Response, error) {
	response, err := e.send(ctx, method, path, query, header, nil)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(method, path, response); err != nil {
		_ = response.Body.Close()
		return nil, err
	}
	return response, nil
}

// OpenWebSocket upgrades a request to path into a WebSocket connection, for a
// server that pushes its events over a WebSocket rather than an event stream.
//
// The upgrade carries the endpoint's credential and the fixed headers of
// WithHeader, and it runs over the same client as every other request, for the
// reasons HTTPEndpoint states. opts may be nil. A header in opts replaces a
// fixed header of the same name, as a per-request header does for OpenStream.
// The HTTPClient and the credential header are the endpoint's own, and a caller
// cannot replace them. A refused upgrade returns an HTTPStatusError with the
// server's reason. The connection lives until the caller closes it or ctx ends
// the dial.
func (e *HTTPEndpoint) OpenWebSocket(ctx context.Context, path string, opts *websocket.DialOptions) (*websocket.Conn, error) {
	dial := websocket.DialOptions{}
	if opts != nil {
		dial = *opts
	}
	header := e.header.Clone()
	if header == nil {
		header = http.Header{}
	}
	for key, values := range dial.HTTPHeader {
		header.Del(key)
		for _, value := range values {
			header.Add(key, value)
		}
	}
	if e.auth != nil {
		// The credential is applied to a request that is never sent, so a
		// scheme that writes more than one header reaches the upgrade whole.
		probe := &http.Request{Header: header}
		e.auth(probe)
	}
	dial.HTTPHeader = header
	dial.HTTPClient = e.client

	target := *e.base
	target.Scheme = "ws"
	target.Path = e.base.Path + path
	conn, response, err := websocket.Dial(ctx, target.String(), &dial)
	if err != nil {
		if response != nil && (response.StatusCode < 200 || response.StatusCode >= 300) && response.StatusCode != http.StatusSwitchingProtocols {
			statusErr := checkStatus(http.MethodGet, path, response)
			if response.Body != nil {
				_ = response.Body.Close()
			}
			if statusErr != nil {
				return nil, statusErr
			}
		}
		return nil, fmt.Errorf("GET %s: open the WebSocket: %w", path, err)
	}
	return conn, nil
}

func (e *HTTPEndpoint) send(ctx context.Context, method, path string, query url.Values, header http.Header, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("%s %s: encode the request: %w", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, e.URL(path, query), reader)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	for key, values := range e.header {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	for key, values := range header {
		request.Header.Del(key)
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", "application/json")
	}
	if e.auth != nil {
		e.auth(request)
	}
	response, err := e.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return response, nil
}

// checkStatus returns an HTTPStatusError for a non-2xx reply. It reads the
// start of the body for the reason and leaves the body to the caller to close.
func checkStatus(method, path string, response *http.Response) error {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	excerpt, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
	return &HTTPStatusError{
		Method:     method,
		Path:       path,
		StatusCode: response.StatusCode,
		Status:     response.Status,
		Body:       strings.TrimSpace(string(excerpt)),
	}
}
