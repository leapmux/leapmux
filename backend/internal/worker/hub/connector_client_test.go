package hub

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// proxyBehavior states what the test proxy does with the Hub's response.
type proxyBehavior int

const (
	// forwardOverHTTP1 copies the status, the headers, and the body, but not
	// the trailers, as nginx proxy_pass does.
	forwardOverHTTP1 proxyBehavior = iota
	// truncateBody ends the body halfway, as a dropped connection does.
	truncateBody
	// badGateway answers 502 without contacting the Hub, as a proxy does when
	// the Hub is down.
	badGateway
)

// connectorHub serves the Register and DeregisterSelf procedures and counts
// each registration it commits.
type connectorHub struct {
	leapmuxv1connect.UnimplementedWorkerConnectorServiceHandler

	refuse    bool
	committed atomic.Int32
}

func (h *connectorHub) Register(_ context.Context, _ *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
	if h.refuse {
		return nil, connect.NewError(connect.CodeUnauthenticated, "registration key invalid or already consumed")
	}
	h.committed.Add(1)
	return &leapmuxv1.RegisterResponse{WorkerId: "worker-1", AuthToken: "token-1"}, nil
}

func (h *connectorHub) DeregisterSelf(_ context.Context, _ *leapmuxv1.DeregisterSelfRequest) (*leapmuxv1.DeregisterSelfResponse, error) {
	return &leapmuxv1.DeregisterSelfResponse{}, nil
}

// proxiedHub is a Hub behind a test reverse proxy, and the content types of
// the requests that reached the Hub.
type proxiedHub struct {
	endpoint *hubtransport.Endpoint

	mu           sync.Mutex
	contentTypes []string
}

func (p *proxiedHub) seenContentTypes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.contentTypes...)
}

// startProxiedHub serves hub behind a test reverse proxy.
//
// The proxy accepts the Worker's HTTP/2 request and forwards it to the Hub
// over a real HTTP/1.1 connection, which is what nginx proxy_pass does.
// behavior decides what the proxy copies back.
func startProxiedHub(t *testing.T, hub leapmuxv1connect.WorkerConnectorServiceHandler, behavior proxyBehavior) *proxiedHub {
	t.Helper()
	proxied := &proxiedHub{}
	connectServer := connect.NewServer()
	leapmuxv1connect.RegisterWorkerConnectorServiceHandler(connectServer, hub)
	hubMux := http.NewServeMux()
	connecthttp.Mount(hubMux, connectServer, connecthttp.WithReadMaxBytes(0))
	upstream := hubtransporttest.NewHTTP1Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.mu.Lock()
		proxied.contentTypes = append(proxied.contentTypes, r.Header.Get("Content-Type"))
		proxied.mu.Unlock()
		hubMux.ServeHTTP(w, r)
	}))
	upstreamProtocols := &http.Protocols{}
	upstreamProtocols.SetHTTP1(true)
	upstreamClient := &http.Client{Transport: &http.Transport{Protocols: upstreamProtocols}}
	t.Cleanup(upstreamClient.CloseIdleConnections)

	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if behavior == badGateway {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		requestBody, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		upstreamRequest, err := http.NewRequestWithContext(r.Context(), r.Method, upstream.URL+r.URL.RequestURI(), bytes.NewReader(requestBody))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		upstreamRequest.Header = r.Header.Clone()
		response, err := upstreamClient.Do(upstreamRequest)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = response.Body.Close() }()
		responseBody, err := io.ReadAll(response.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// assert, not require: this runs on a server goroutine, where
		// FailNow must not be called.
		assert.Equal(t, 1, response.ProtoMajor, "the proxy must reach the Hub over HTTP/1.x")

		for name, values := range response.Header {
			w.Header()[name] = values
		}
		w.WriteHeader(response.StatusCode)
		if behavior == truncateBody {
			responseBody = responseBody[:len(responseBody)/2]
		}
		_, _ = w.Write(responseBody)
	})
	srv := hubtransporttest.NewServer(t, proxy)
	endpoint, err := hubtransport.New(srv.URL)
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	proxied.endpoint = endpoint
	return proxied
}

func registerOnceThrough(t *testing.T, endpoint *hubtransport.Endpoint) (*leapmuxv1.RegisterResponse, error) {
	t.Helper()
	ctx, info := connect.NewClientContext(testutil.DeadlineContext(t))
	info.RequestHeader().Set("Authorization", "Bearer registration-key")
	return newConnectorClient(endpoint).Register(ctx, &leapmuxv1.RegisterRequest{})
}

// The Worker speaks the Connect protocol, which reports a call's result in
// the HTTP status and the body. A proxy that forwards over HTTP/1.1 and drops
// every trailer, as nginx proxy_pass does, therefore cannot lose a result
// that the Hub committed. Under the gRPC protocol, this registration reached
// the Worker as "no Grpc-Status trailer", although the Hub had created the
// Worker.
func TestConnectorClient_RegistersThroughAProxyThatForwardsOverHTTP1(t *testing.T) {
	hub := &connectorHub{}
	proxied := startProxiedHub(t, hub, forwardOverHTTP1)

	resp, err := registerOnceThrough(t, proxied.endpoint)

	require.NoError(t, err)
	assert.Equal(t, "worker-1", resp.GetWorkerId())
	assert.Equal(t, "token-1", resp.GetAuthToken())
	assert.Equal(t, int32(1), hub.committed.Load())
	assert.Equal(t, []string{"application/proto"}, proxied.seenContentTypes(),
		"the Worker must speak the Connect protocol, whose result needs no trailer")
}

// The Hub's refusal travels in the HTTP status and the body as well, so the
// proxy keeps its code.
func TestConnectorClient_ReportsARefusalThroughAProxyThatForwardsOverHTTP1(t *testing.T) {
	_, err := registerOnceThrough(t, startProxiedHub(t, &connectorHub{refuse: true}, forwardOverHTTP1).endpoint)

	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.ErrorContains(t, err, "registration key invalid or already consumed")
}

// A truncated body is a failure, never a success with partial credentials.
func TestConnectorClient_FailsOnATruncatedResponse(t *testing.T) {
	_, err := registerOnceThrough(t, startProxiedHub(t, &connectorHub{}, truncateBody).endpoint)

	require.Error(t, err)
	var connectErr *connect.Error
	assert.ErrorAs(t, err, &connectErr)
}

func TestDeregister_SucceedsThroughAProxyThatForwardsOverHTTP1(t *testing.T) {
	proxied := startProxiedHub(t, &connectorHub{}, forwardOverHTTP1)

	require.NoError(t, Deregister(testutil.DeadlineContext(t), proxied.endpoint, "worker-token"))
	assert.Equal(t, []string{"application/proto"}, proxied.seenContentTypes())
}

// A registration whose every attempt fails ends at the caller's deadline. The
// error states that deadline and the reason the attempts failed, because the
// deadline alone tells an operator nothing.
func TestRegisterWithClient_ReportsTheLastFailureWhenItEnds(t *testing.T) {
	client := newConnectorClient(startProxiedHub(t, &connectorHub{}, badGateway).endpoint)
	clock := testutil.NewQuartzMock(t)
	testCtx := testutil.DeadlineContext(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, registerRetryTimerTag)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() {
		_, err := registerWithClient(ctx, client, "k", fastRegisterRetry(clock), testRegistrationOptions("k"))
		errCh <- err
	}()
	_ = testutil.WaitForTimer(t, testCtx, newTimer)
	cancel()
	stopTimer.MustWait(testCtx).MustRelease(testCtx)

	var err error
	select {
	case err = <-errCh:
	case <-testCtx.Done():
		require.FailNow(t, "registration ignored the cancelled context")
	}
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, "the last attempt failed")
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "the proxy's 502 survives the cancellation as the last failure")
}

// A run that ends before any attempt failed has no last failure to report.
func TestWithLastFailure(t *testing.T) {
	ended := context.DeadlineExceeded
	assert.Equal(t, ended, withLastFailure(ended, nil))

	lastFailure := connect.NewError(connect.CodeUnavailable, "hub down")
	got := withLastFailure(ended, lastFailure)
	assert.ErrorIs(t, got, context.DeadlineExceeded)
	assert.ErrorIs(t, got, lastFailure)
	assert.EqualError(t, got, "context deadline exceeded; the last attempt failed: unavailable: hub down")
}
