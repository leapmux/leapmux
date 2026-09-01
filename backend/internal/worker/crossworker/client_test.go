package crossworker

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/locallisten/locallistentest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDelegationProvider lets tests inject a controlled bearer / error
// without spinning up a real hub mint endpoint. The real
// DelegationStore covers the HTTP path under delegation_test.go;
// here we want to exercise the Client's argument-validation +
// pooling without that machinery.
type stubDelegationProvider struct {
	bearer string
	err    error
	calls  int
}

func (s *stubDelegationProvider) GetBearer(_ context.Context, _ DelegationScope) (string, error) {
	s.calls++
	return s.bearer, s.err
}

// testEndpoint is the hub address these tests point the pool at. Nothing here
// dials it: every case stops before an open, or supplies its own server.
func testEndpoint(t *testing.T) *hubtransport.Endpoint {
	return testEndpointFor(t, "http://hub.test")
}

// testEndpointFor builds the endpoint for url. hubtransport.New opens no
// connection, so a URL that nothing listens on is a valid fixture.
func testEndpointFor(t *testing.T, url string) *hubtransport.Endpoint {
	t.Helper()
	endpoint, err := hubtransport.New(url)
	require.NoError(t, err, "hubtransport.New(%q)", url)
	t.Cleanup(endpoint.CloseIdleConnections)
	return endpoint
}

// TestNew_ConstructsUsableClient pins down the constructor's contract.
// The pool map must be initialized so the first cache lookup doesn't
// nil-deref.
func TestNew_ConstructsUsableClient(t *testing.T) {
	c := New(context.Background(), testEndpoint(t), &PinStore{}, &stubDelegationProvider{})
	require.NotNil(t, c)
	assert.NotNil(t, c.channels, "channels map must be initialized — Client mutex assumes it")

	// Close must be safe even when no channels were ever opened.
	c.Close()
	// Idempotent: a second Close after the first must not panic on a
	// nil-cleared map. (Code path the worker shutdown loop hits.)
	c.Close()
}

func TestNew_BindsPoolToExplicitLifetime(t *testing.T) {
	lifetime, cancel := context.WithCancel(context.Background())
	c := New(lifetime, testEndpoint(t), &PinStore{}, &stubDelegationProvider{})
	cancel()
	select {
	case <-c.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cross-worker pool did not stop with its lifetime context")
	}
	_, err := c.channelFor(context.Background(), "worker-2", DelegationScope{UserID: userid.MustNew("user-1")})
	assert.ErrorIs(t, err, context.Canceled)
}

// TestChannelFor_RejectsEmptyTarget and TestChannelFor_RejectsEmptyUser
// guard the input contract: callers (router.go) supply userID +
// targetWorkerID, but a future refactor that drops one path could
// silently start opening unscoped channels.
func TestChannelFor_RejectsEmptyTarget(t *testing.T) {
	c := New(context.Background(), testEndpoint(t), &PinStore{}, &stubDelegationProvider{bearer: "x"})
	_, err := c.channelFor(context.Background(), "", DelegationScope{UserID: userid.MustNew("user-1")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target_worker_id required")
}

func TestChannelFor_RejectsEmptyUser(t *testing.T) {
	c := New(context.Background(), testEndpoint(t), &PinStore{}, &stubDelegationProvider{bearer: "x"})
	_, err := c.channelFor(context.Background(), "worker-B", DelegationScope{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user_id required")
}

// TestChannelFor_PropagatesDelegationError wires the failure path
// through the DelegationProvider abstraction. Worker A asks for a
// bearer for the user; if the mint endpoint refuses, the channel must
// NOT be opened — otherwise we'd hold an unauthenticated Noise channel
// in the pool.
func TestChannelFor_PropagatesDelegationError(t *testing.T) {
	dp := &stubDelegationProvider{err: errors.New("mint refused: tab gone")}
	c := New(context.Background(), testEndpoint(t), &PinStore{}, dp)

	_, err := c.channelFor(context.Background(), "worker-B", DelegationScope{UserID: userid.MustNew("user-1"), AgentID: "agent-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delegation token")
	assert.Contains(t, err.Error(), "mint refused")
	// Single attempt — channelFor must not retry on delegation failure
	// (retries belong inside the DelegationStore where they can be
	// scoped to the propagation race; everything else is fatal).
	assert.Equal(t, 1, dp.calls)

	// Pool must remain empty after a failed open. Otherwise a later
	// Close would try to close a nil tunnel.Channel.
	c.mu.Lock()
	assert.Empty(t, c.channels)
	c.mu.Unlock()
}

// TestChannelFor_PoolKeyIsTargetWorkerAndUser proves the cache key
// composes both the target worker and the user id. Two users on the
// same worker must receive separate channels (different Noise_NK
// session keys → different identity assertions); the same user
// hitting the same worker twice must hit the existing entry.
//
// Without standing up a real Noise responder we can't drive the open
// happy path here; the assertion is on the argument-validation code
// the pool relies on, which the existing fake covers via
// PropagatesDelegationError.
func TestChannelFor_PoolKeyComposition(t *testing.T) {
	c := New(context.Background(), testEndpoint(t), &PinStore{}, &stubDelegationProvider{bearer: "x"})
	// Manually seed pool entries to verify key independence: worker AND user
	// must each contribute, so two users on one worker (and one user across two
	// workers) never share a Noise session. There is no third axis -- a
	// delegation bearer is scoped to (user, minting worker), so two workspaces
	// of the same user legitimately ride the same channel.
	c.channels[clientKey{WorkerID: "B", UserID: "u-1"}] = nil
	c.channels[clientKey{WorkerID: "B", UserID: "u-2"}] = nil
	c.channels[clientKey{WorkerID: "C", UserID: "u-1"}] = nil
	require.Len(t, c.channels, 3)

	c.Close()
	c.mu.Lock()
	assert.Empty(t, c.channels, "Close must clear the pool")
	c.mu.Unlock()
}

// TestCallInner_DelegationFailureSurfaces is the unary entrypoint
// counterpart to PropagatesDelegationError — proves the error
// reaches the caller intact instead of being swallowed by the
// pool layer.
func TestCallInner_DelegationFailureSurfaces(t *testing.T) {
	c := New(context.Background(), testEndpoint(t), &PinStore{}, &stubDelegationProvider{err: errors.New("mint denied")})
	_, err := c.CallInner(context.Background(), "worker-B", userid.MustNew("user-1"), "OpenAgent", []byte("payload"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint denied")
}

// TestStreamInner_DelegationFailureSurfaces mirrors CallInner for the
// streaming path. Both go through channelFor, but the streaming
// version's onMsg callback shouldn't swallow the upstream error.
func TestStreamInner_DelegationFailureSurfaces(t *testing.T) {
	c := New(context.Background(), testEndpoint(t), &PinStore{}, &stubDelegationProvider{err: errors.New("mint denied")})
	err := c.StreamInner(context.Background(), "worker-B", userid.MustNew("user-1"), "WatchEvents", nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint denied")
}

// TestCallInner_RequiresUserID guards the input contract: a cross-worker call
// with no user is rejected before any network I/O, because the user IS the
// delegation bearer's identity and an unminted one would mint as nobody.
func TestCallInner_RequiresUserID(t *testing.T) {
	c := New(context.Background(), testEndpoint(t), &PinStore{}, &stubDelegationProvider{bearer: "x"})
	_, err := c.CallInner(context.Background(), "worker-B", userid.UserID{}, "OpenAgent", []byte("p"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user_id required")
}

// blockingDelegationProvider blocks the first (and, under single-flight, only)
// GetBearer until released, so a test can register several concurrent callers
// on the same in-flight open before it resolves.
type blockingDelegationProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
	err     error
}

func (b *blockingDelegationProvider) GetBearer(ctx context.Context, _ DelegationScope) (string, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return "", b.err
}

// TestChannelFor_SingleFlightsConcurrentOpens proves the pool dedups a burst of
// concurrent first-contact calls to the same (worker, user, workspace) into a
// single delegation mint + Noise handshake, instead of racing N of them and
// discarding all but one.
func TestChannelFor_SingleFlightsConcurrentOpens(t *testing.T) {
	dp := &blockingDelegationProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
		err:     errors.New("mint done"),
	}
	c := New(context.Background(), testEndpoint(t), &PinStore{}, dp)
	defer c.Close()

	const callers = 8
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := c.channelFor(context.Background(), "worker-B",
				DelegationScope{UserID: userid.MustNew("user-1")})
			errs[idx] = err
		}(i)
	}

	// Wait for the shared open to begin, then give the followers time to attach
	// to it before releasing the mint.
	select {
	case <-dp.started:
	case <-time.After(2 * time.Second):
		t.Fatal("no channelFor started the shared open")
	}
	time.Sleep(100 * time.Millisecond)
	close(dp.release)
	wg.Wait()

	dp.mu.Lock()
	calls := dp.calls
	dp.mu.Unlock()
	assert.Equal(t, 1, calls, "concurrent opens for one key must mint exactly one delegation token")

	for i, err := range errs {
		require.Error(t, err, "caller %d", i)
		assert.Contains(t, err.Error(), "mint done", "caller %d", i)
	}

	// The in-flight marker and the pool must be empty after a failed shared open.
	c.mu.Lock()
	_, stillInflight := c.inflight[clientKey{WorkerID: "worker-B", UserID: "user-1"}]
	poolLen := len(c.channels)
	c.mu.Unlock()
	assert.False(t, stillInflight, "in-flight marker must clear once the open resolves")
	assert.Zero(t, poolLen, "a failed open must not pool a channel")
}

// Compile-time interface assertions: both stubs satisfy the contract.
var (
	_ DelegationProvider = (*stubDelegationProvider)(nil)
	_ DelegationProvider = (*blockingDelegationProvider)(nil)
)

// fakeChannelService records that a cross-worker channel open reached the hub,
// and refuses it with a message the caller can recognise.
type fakeChannelService struct {
	leapmuxv1connect.UnimplementedChannelServiceHandler
	called chan string
}

func (f *fakeChannelService) GetWorkerHandshakeParams(
	_ context.Context,
	req *connect.Request[leapmuxv1.GetWorkerHandshakeParamsRequest],
) (*connect.Response[leapmuxv1.GetWorkerHandshakeParamsResponse], error) {
	select {
	case f.called <- req.Msg.GetWorkerId():
	default:
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("sibling worker is not registered"))
}

// TestOpenChannel_ReachesAHubOverASocket covers a hub addressed by `unix:` or
// `npipe:`, which a cross-worker channel could not reach at all: the open
// passed the raw hub URL and no HTTP clients, so tunnel.OpenChannel fell back
// to http.DefaultClient, which cannot dial a socket.
//
// The open still fails here -- the fake refuses the handshake -- and that is
// the point: the failure comes from the HUB, over the socket, rather than from
// a transport that never left the process.
func TestOpenChannel_ReachesAHubOverASocket(t *testing.T) {
	fake := &fakeChannelService{called: make(chan string, 1)}
	mux := http.NewServeMux()
	path, handler := leapmuxv1connect.NewChannelServiceHandler(fake)
	mux.Handle(path, handler)

	socketURL := locallistentest.UniqueListenURL(t, "crossworker")
	ln, err := locallisten.Listen(socketURL)
	require.NoError(t, err)
	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, Protocols: protocols}
	t.Cleanup(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(ln) }()
	require.NoError(t, locallisten.WaitReady(context.Background(), socketURL))

	c := New(context.Background(), testEndpointFor(t, socketURL), &PinStore{}, &stubDelegationProvider{bearer: "delegation-bearer"})
	t.Cleanup(c.Close)

	_, err = c.CallInner(context.Background(), "worker-B", userid.MustNew("user-1"), "OpenAgent", []byte("payload"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sibling worker is not registered",
		"the refusal must come from the hub, not from a transport that could not dial the socket")

	select {
	case gotWorkerID := <-fake.called:
		assert.Equal(t, "worker-B", gotWorkerID)
	default:
		t.Fatal("the channel open never reached the hub over the socket")
	}
}
