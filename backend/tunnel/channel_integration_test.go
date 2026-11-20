package tunnel_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/tofupins"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
	"github.com/leapmux/leapmux/solo"
	"github.com/leapmux/leapmux/tunnel"
)

func uniqueTestListenURL(t *testing.T) string {
	return locallistentest.UniqueListenURL(t, "leapmux-test")
}

// soloStopBudget is the ceiling on solo teardown.
//
// A healthy stop measures ~1.1s, and almost none of that is our work: it is
// net/http's fixed 1s HTTP/2 GOAWAY grace (http2goAwayTimeout), which
// Server.Shutdown waits out on the worker's h2c connection. Only ~50ms is
// listener and store close, and that did not move under 4x CPU
// oversubscription. So the discriminating band is [1.1s, 10s] -- the hub's own
// HTTP drain timeout -- and 8s sits far enough above the floor to survive a
// loaded runner while still failing a stop that is waiting out the drain.
//
// This is the only solo bootstrap whose worker is fully CONNECTED by the time
// Stop runs; backend/solo's own tests tear down before the worker's h2c
// connection exists, so the same budget there would assert nothing.
const soloStopBudget = 8 * time.Second

// startTestSolo starts a solo Hub+Worker instance for integration testing.
// Returns the hub addresses, account and worker IDs, and the authenticated
// HTTP client that completed first-password setup.
func startTestSolo(t *testing.T) (hubURL, localListenURL, userID, workerID string, httpClient *http.Client) {
	t.Helper()

	// Use a short path under /tmp to stay within the 104-byte macOS Unix
	// socket path limit. The solo instance creates hub.sock inside dataDir.
	dataDir, err := os.MkdirTemp("", "lm")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Find a free port.
	ln, listenErr := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, listenErr)
	addr := ln.Addr().String()
	_ = ln.Close()

	// Both entries in the bind set: the TCP address the tests dial over HTTP,
	// and a short-path local socket (the deep default path would exceed
	// macOS's 104-byte sun_path limit). The list is the bind set, so naming
	// only one of them would drop the other.
	t.Setenv("LEAPMUX_HUB_LISTEN", addr+","+uniqueTestListenURL(t))

	inst, err := solo.Start(ctx, solo.Config{
		Listen:     addr,
		ConfigDir:  dataDir,
		ConfigFile: dataDir + "/solo.yaml",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		// The budget detects a full Hub HTTP drain when an idle worker leaves
		// Connect open. Worker work can also exceed it. Report the delay, then
		// join Stop before later cleanup removes the data directory.
		stopped := make(chan error, 1)
		go func() { stopped <- inst.Stop() }()
		select {
		case err := <-stopped:
			require.NoError(t, err)
		case <-time.After(soloStopBudget):
			t.Errorf("solo shutdown exceeded its %s budget", soloStopBudget)
			require.NoError(t, <-stopped, "the timed-out shutdown must still finish before fixture cleanup returns")
		}
	})

	hubURL = "http://" + addr
	localListenURL = inst.LocalListenURL()

	// Wait for Hub to be ready.
	require.NoError(t, waitForHTTP(hubURL, 30*time.Second))

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	apiClient := &http.Client{Timeout: 10 * time.Second, Jar: jar}
	authClient := leapmuxv1connect.NewAuthServiceClient(connect.NewClient(connecthttp.NewTransport(apiClient, hubURL, connecthttp.WithReadMaxBytes(0))))
	ctx, info := connect.NewClientContext(ctx)
	_, err = authClient.SetInitialSoloPassword(ctx, &leapmuxv1.SetInitialSoloPasswordRequest{Password: "correct-horse-battery-staple"})
	require.NoError(t, err)
	require.NotEmpty(t, info.ResponseHeader().Get("Set-Cookie"))

	meResp, err := authClient.GetCurrentUser(ctx, &leapmuxv1.GetCurrentUserRequest{})
	require.NoError(t, err)
	userID = meResp.GetSessionReport().GetUser().GetId()
	require.NotEmpty(t, userID)

	// Get worker ID by listing workers (poll until online).
	//
	// The 30s budget below is the ceiling for a wedged worker; the tick is
	// what every healthy run actually pays. A locally spawned worker connects
	// in tens of milliseconds, so a 500ms tick spent nearly all of its time
	// asleep after the worker was already up -- twice, once here and once for
	// the handshake params -- on every test in this file.
	mgmtClient := leapmuxv1connect.NewWorkerManagementServiceClient(connect.NewClient(connecthttp.NewTransport(apiClient, hubURL, connecthttp.WithReadMaxBytes(0))))
	var wID string
	require.Eventually(t, func() bool {
		listResp, listErr := mgmtClient.ListWorkers(ctx, &leapmuxv1.ListWorkersRequest{})
		if listErr != nil {
			return false
		}
		for _, w := range listResp.GetWorkers() {
			if w.GetOnline() {
				wID = w.GetId()
				return true
			}
		}
		return false
	}, 30*time.Second, 10*time.Millisecond, "worker did not come online in time")

	// The hub lists a worker online only after its hello, so an online
	// worker's handshake parameters carry its key.
	channelClient := leapmuxv1connect.NewChannelServiceClient(connect.NewClient(connecthttp.NewTransport(apiClient, hubURL, connecthttp.WithReadMaxBytes(0))))
	resp, keyErr := channelClient.GetWorkerHandshakeParams(ctx, &leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: wID})
	require.NoError(t, keyErr)
	require.NotEmpty(t, resp.GetKeys().GetX25519(), "an online worker's handshake parameters carry its key")

	return hubURL, localListenURL, userID, wID, &http.Client{Jar: jar}
}

func waitForHTTP(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("server at %s not ready after %v", url, timeout)
}

func TestChannelOpenAndCallRPC(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	hubURL, _, _, workerID, httpClient := startTestSolo(t)
	ctx := context.Background()

	// Open an E2EE channel.
	pins, path := openChannelPinStore(t)
	relay := observeChannelRegistration(httpClient)
	ping := newHeldPingReply()
	ctxOpen := channelOpenContext(t)
	results := openChannelWithPin(ctxOpen, ctx, hubURL, workerID, relay.client, ping.client(ctxOpen, httpClient), pins)
	awaitPinSignal(t, ctxOpen, ping.received)
	assert.NoFileExists(t, path, "the encrypted Ping reply has not reached the client")
	ping.release()

	result := awaitPinOpen(t, ctxOpen, results)
	require.NoError(t, result.err)
	ch := result.channel
	require.NotNil(t, ch)
	assertPublishedWorkerKeys(t, hubURL, httpClient, workerID, pins)
	assert.Zero(t, relay.closeCalls.Load(), "a channel that was handed out needs no rollback")

	// Call GetWorkerSystemInfo to verify the channel works.
	payload, err := proto.Marshal(&leapmuxv1.GetWorkerSystemInfoRequest{})
	require.NoError(t, err)

	resp, err := ch.CallRPC(ctx, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, payload)
	require.NoError(t, err)

	var sysInfo leapmuxv1.GetWorkerSystemInfoResponse
	require.NoError(t, proto.Unmarshal(resp.GetPayload(), &sysInfo))
	assert.NotEmpty(t, sysInfo.GetOs())
	assert.NotEmpty(t, sysInfo.GetArch())
	// The worker's build arrives in its own message; the version is never
	// empty, because an unstamped binary reports "dev".
	require.NotNil(t, sysInfo.GetBuild())
	assert.NotEmpty(t, sysInfo.GetBuild().GetVersion())
}

// pinTestTransport lets these tests observe or hold real relay traffic.
type pinTestTransport func(*http.Request) (*http.Response, error)

func (transport pinTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func withPinTestTransport(client *http.Client, transport pinTestTransport) *http.Client {
	copy := *client
	copy.Transport = transport
	return &copy
}

type channelRegistrationObserver struct {
	client     *http.Client
	registered chan string
	closeCalls atomic.Int32
}

func observeChannelRegistration(client *http.Client) *channelRegistrationObserver {
	result := &channelRegistrationObserver{registered: make(chan string, 1)}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	result.client = withPinTestTransport(client, func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case leapmuxv1connect.ChannelServiceCloseChannelProcedure:
			result.closeCalls.Add(1)
		case leapmuxv1connect.ChannelServiceOpenChannelProcedure:
			request = request.Clone(request.Context())
			request.Header.Set("Accept-Encoding", "identity")
		}

		response, err := transport.RoundTrip(request)
		if err != nil || request.URL.Path != leapmuxv1connect.ChannelServiceOpenChannelProcedure || response.StatusCode != http.StatusOK {
			return response, err
		}
		// Connect sends the unary OpenChannelResponse as a protobuf body.
		wire, readErr := io.ReadAll(response.Body)
		if err := errors.Join(readErr, response.Body.Close()); err != nil {
			return nil, err
		}
		var opened leapmuxv1.OpenChannelResponse
		if err := proto.Unmarshal(wire, &opened); err != nil {
			return nil, fmt.Errorf("read test channel registration: %w", err)
		}
		result.registered <- opened.GetChannelId()
		response.Body = io.NopCloser(bytes.NewReader(wire))
		return response, nil
	})
	return result
}

func (observer *channelRegistrationObserver) channelID(t *testing.T, ctx context.Context) string {
	t.Helper()
	select {
	case id := <-observer.registered:
		require.NotEmpty(t, id)
		return id
	case <-ctx.Done():
		t.Fatal("the Hub did not register a channel")
		return ""
	}
}

type heldPingReply struct {
	received chan struct{}
	resume   chan struct{}
	failure  error
}

func newHeldPingReply() *heldPingReply {
	return &heldPingReply{received: make(chan struct{}), resume: make(chan struct{})}
}

func (ping *heldPingReply) release() { close(ping.resume) }

func (ping *heldPingReply) client(ctx context.Context, client *http.Client) *http.Client {
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return withPinTestTransport(client, func(request *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(request)
		if err != nil || request.URL.Path != contracts.WSRouteChannel || response.StatusCode != http.StatusSwitchingProtocols {
			return response, err
		}
		body, ok := response.Body.(io.ReadWriteCloser)
		if !ok {
			return nil, errors.Join(errors.New("test relay response is not a WebSocket"), response.Body.Close())
		}
		response.Body = &heldPingReplyBody{ReadWriteCloser: body, buffered: bufio.NewReader(body), ctx: ctx, ping: ping}
		return response, nil
	})
}

type heldPingReplyBody struct {
	io.ReadWriteCloser
	buffered          *bufio.Reader
	ctx               context.Context
	ping              *heldPingReply
	controlBytesLeft  int
	releasedPingReply bool
	pingReadErr       error
}

func (body *heldPingReplyBody) Read(dst []byte) (int, error) {
	if body.pingReadErr != nil {
		return 0, body.pingReadErr
	}
	if body.releasedPingReply || len(dst) == 0 {
		return body.buffered.Read(dst)
	}
	if body.controlBytesLeft == 0 {
		header, err := body.buffered.Peek(2)
		if err != nil {
			return 0, err
		}
		const binaryOpcode, firstControlOpcode, opcodeMask = 2, 8, 0x0f
		opcode := header[0] & opcodeMask
		if opcode == binaryOpcode {
			// Before handout the only application reply is the encrypted Ping.
			close(body.ping.received)
			select {
			case <-body.ping.resume:
				body.pingReadErr = body.ping.failure
			case <-body.ctx.Done():
				body.pingReadErr = body.ctx.Err()
			}
			if body.pingReadErr != nil {
				return 0, body.pingReadErr
			}
			body.releasedPingReply = true
			return body.buffered.Read(dst)
		}
		// Pass through server keepalives and other control frames. They
		// have an unmasked payload of at most 125 bytes.
		if opcode < firstControlOpcode || header[1] > 125 {
			return 0, fmt.Errorf("unexpected WebSocket frame before the Ping reply: %x", header)
		}
		body.controlBytesLeft = len(header) + int(header[1])
	}
	n, err := body.buffered.Read(dst[:min(len(dst), body.controlBytesLeft)])
	body.controlBytesLeft -= n
	return n, err
}

type pinCommitDecorator struct {
	store  *tofupins.Store
	commit func(context.Context, func(context.Context) error) error
}

func (policy pinCommitDecorator) Resolve(ctx context.Context, workerID string, x, m, sl []byte) (func(context.Context) error, error) {
	commit, err := policy.store.Resolve(ctx, workerID, x, m, sl)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error { return policy.commit(ctx, commit) }, nil
}

type pinOpenResult struct {
	channel *tunnel.Channel
	err     error
}

func openChannelWithPin(ctx, lifetime context.Context, hubURL, workerID string, httpClient, wsClient *http.Client, pins tunnel.KeyPinStore) <-chan pinOpenResult {
	result := make(chan pinOpenResult, 1)
	go func() {
		channel, err := tunnel.OpenChannel(ctx, hubURL, workerID, &tunnel.OpenChannelOptions{
			LifetimeContext:     lifetime,
			HTTPClient:          httpClient,
			WebSocketHTTPClient: wsClient,
			KeyPin:              pins,
		})
		result <- pinOpenResult{channel, err}
	}()
	return result
}

func awaitPinOpen(t *testing.T, waitCtx context.Context, results <-chan pinOpenResult) pinOpenResult {
	t.Helper()
	select {
	case result := <-results:
		if result.channel != nil {
			t.Cleanup(result.channel.Close)
		}
		return result
	case <-waitCtx.Done():
		t.Fatal("the channel open did not finish")
		return pinOpenResult{}
	}
}

func awaitPinSignal(t *testing.T, waitCtx context.Context, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-waitCtx.Done():
		t.Fatal("the channel open did not reach the synchronization point")
	}
}

func openChannelPinStore(t *testing.T) (*tofupins.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pins.json")
	pins, err := tofupins.Open(tofupins.Options{Path: path, DirMode: 0o700, FileMode: 0o600})
	require.NoError(t, err)
	return pins, path
}

func assertPublishedWorkerKeys(t *testing.T, hubURL string, httpClient *http.Client, workerID string, pins *tofupins.Store) {
	t.Helper()
	client := leapmuxv1connect.NewChannelServiceClient(connect.NewClient(connecthttp.NewTransport(httpClient, hubURL, connecthttp.WithReadMaxBytes(0))))
	params, err := client.GetWorkerHandshakeParams(t.Context(), &leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID})
	require.NoError(t, err)
	snapshot, err := pins.Snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, snapshot, 1)
	require.Contains(t, snapshot, workerID)
	entry := snapshot[workerID]
	assert.Equal(t, base64.StdEncoding.EncodeToString(params.GetKeys().GetX25519()), entry.X25519PublicKey)
	assert.Equal(t, base64.StdEncoding.EncodeToString(params.GetKeys().GetMlkem()), entry.MlkemPublicKey)
	assert.Equal(t, base64.StdEncoding.EncodeToString(params.GetKeys().GetSlhdsa()), entry.SlhdsaPublicKey)
}

func assertPinOpenRolledBack(t *testing.T, ctx context.Context, relay *channelRegistrationObserver, service leapmuxv1connect.ChannelServiceClient, channelID string) {
	t.Helper()
	assert.EqualValues(t, 1, relay.closeCalls.Load())
	_, err := service.CloseChannel(ctx, &leapmuxv1.CloseChannelRequest{ChannelId: channelID})
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "the Hub no longer has the channel")
}

func TestChannelPinCommitRollbackOnTheSoloRelay(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	hubURL, _, _, workerID, httpClient := startTestSolo(t)
	service := leapmuxv1connect.NewChannelServiceClient(connect.NewClient(connecthttp.NewTransport(httpClient, hubURL, connecthttp.WithReadMaxBytes(0))))

	t.Run("failed verification records no pin", func(t *testing.T) {
		ctx := channelOpenContext(t)
		pins, path := openChannelPinStore(t)
		relay := observeChannelRegistration(httpClient)
		ping := newHeldPingReply()
		ping.failure = errors.New("could not receive the verification reply")
		results := openChannelWithPin(ctx, t.Context(), hubURL, workerID, relay.client, ping.client(ctx, httpClient), pins)

		id := relay.channelID(t, ctx)
		awaitPinSignal(t, ctx, ping.received)
		assert.NoFileExists(t, path)
		ping.release()

		result := awaitPinOpen(t, ctx, results)
		require.ErrorContains(t, result.err, "verify channel session")
		assert.Nil(t, result.channel)
		assert.NoFileExists(t, path)
		assertPinOpenRolledBack(t, ctx, relay, service, id)
	})

	t.Run("commit file error refuses handout", func(t *testing.T) {
		ctx := channelOpenContext(t)
		pins, path := openChannelPinStore(t)
		relay := observeChannelRegistration(httpClient)
		committing := make(chan struct{})
		proceed := make(chan struct{})
		policy := pinCommitDecorator{store: pins, commit: func(ctx context.Context, commit func(context.Context) error) error {
			close(committing)
			select {
			case <-proceed:
				return commit(ctx)
			case <-ctx.Done():
				return ctx.Err()
			}
		}}
		results := openChannelWithPin(ctx, t.Context(), hubURL, workerID, relay.client, httpClient, policy)
		id := relay.channelID(t, ctx)
		awaitPinSignal(t, ctx, committing)
		assert.NoFileExists(t, path)
		before := []byte("{invalid")
		require.NoError(t, os.WriteFile(path, before, 0o600))
		close(proceed)

		result := awaitPinOpen(t, ctx, results)
		require.ErrorContains(t, result.err, "commit worker key pin: parse pins")
		assert.Nil(t, result.channel)
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, before, after)
		assertPinOpenRolledBack(t, ctx, relay, service, id)
	})

	for _, test := range []struct {
		name                       string
		cancelOpen, cancelLifetime bool
	}{
		{name: "operation canceled before commit", cancelOpen: true},
		{name: "lifetime canceled before commit", cancelLifetime: true},
		{name: "both contexts canceled before commit", cancelOpen: true, cancelLifetime: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			waitCtx := channelOpenContext(t)
			ctx, cancelOpen := context.WithCancel(waitCtx)
			defer cancelOpen()
			lifetime, cancelLifetime := context.WithCancel(t.Context())
			defer cancelLifetime()
			pins, path := openChannelPinStore(t)
			relay := observeChannelRegistration(httpClient)
			committing := make(chan struct{})
			policy := pinCommitDecorator{store: pins, commit: func(ctx context.Context, commit func(context.Context) error) error {
				close(committing)
				<-ctx.Done()
				return commit(ctx)
			}}
			results := openChannelWithPin(ctx, lifetime, hubURL, workerID, relay.client, httpClient, policy)
			id := relay.channelID(t, waitCtx)
			awaitPinSignal(t, waitCtx, committing)
			assert.NoFileExists(t, path)
			if test.cancelOpen {
				cancelOpen()
			}
			if test.cancelLifetime {
				cancelLifetime()
			}

			result := awaitPinOpen(t, waitCtx, results)
			require.ErrorIs(t, result.err, context.Canceled)
			assert.Nil(t, result.channel)
			assert.NoFileExists(t, path)
			assertPinOpenRolledBack(t, waitCtx, relay, service, id)
		})
	}

	for _, peerClose := range []bool{false, true} {
		name := "operation cancellation preserves the published pin"
		if peerClose {
			name = "peer closure preserves the published pin"
		}
		t.Run(name, func(t *testing.T) {
			waitCtx := channelOpenContext(t)
			openCtx, cancelOpen := context.WithCancel(waitCtx)
			defer cancelOpen()
			lifetimeCtx := t.Context()
			pins, path := openChannelPinStore(t)
			relay := observeChannelRegistration(httpClient)
			published := make(chan struct{})
			policy := pinCommitDecorator{store: pins, commit: func(ctx context.Context, commit func(context.Context) error) error {
				if err := commit(ctx); err != nil {
					return err
				}
				close(published)
				<-ctx.Done()
				return nil
			}}
			results := openChannelWithPin(openCtx, lifetimeCtx, hubURL, workerID, relay.client, httpClient, policy)
			id := relay.channelID(t, waitCtx)
			awaitPinSignal(t, waitCtx, published)
			before, err := os.ReadFile(path)
			require.NoError(t, err)

			if peerClose {
				_, err := service.CloseChannel(waitCtx, &leapmuxv1.CloseChannelRequest{ChannelId: id})
				require.NoError(t, err)
			} else {
				cancelOpen()
			}

			result := awaitPinOpen(t, waitCtx, results)
			assert.Nil(t, result.channel)
			if peerClose {
				require.ErrorContains(t, result.err, "channel closed before handout")
				assert.NoError(t, openCtx.Err())
				assert.NoError(t, lifetimeCtx.Err())
			} else {
				require.ErrorIs(t, result.err, context.Canceled)
			}
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assertPublishedWorkerKeys(t, hubURL, httpClient, workerID, pins)
			assertPinOpenRolledBack(t, waitCtx, relay, service, id)
		})
	}
}

func TestChannelLifetimeCanOutliveHandshakeContext(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	hubURL, _, _, workerID, httpClient := startTestSolo(t)
	operationCtx, cancelOperation := context.WithCancel(channelOpenContext(t))
	t.Cleanup(cancelOperation)
	lifetimeCtx, cancelLifetime := context.WithCancel(context.Background())
	t.Cleanup(cancelLifetime)

	ch, err := tunnel.OpenChannel(operationCtx, hubURL, workerID, &tunnel.OpenChannelOptions{
		LifetimeContext:     lifetimeCtx,
		HTTPClient:          httpClient,
		WebSocketHTTPClient: httpClient,
	})
	require.NoError(t, err)
	t.Cleanup(ch.Close)
	cancelOperation()
	require.NoError(t, ch.Context().Err(), "completed handshake context must not own the established channel")

	payload, err := proto.Marshal(&leapmuxv1.GetWorkerSystemInfoRequest{})
	require.NoError(t, err)
	_, err = ch.CallRPC(lifetimeCtx, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, payload)
	require.NoError(t, err)

	cancelLifetime()
	select {
	case <-ch.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("channel did not stop with its lifetime context")
	}
}

func TestChannelTunnelEchoFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	echoAddr := testutil.StartEchoServer(t)
	echoHost, echoPort := testutil.ParseAddr(echoAddr)

	hubURL, _, _, workerID, httpClient := startTestSolo(t)
	ctx := context.Background()

	ch, err := tunnel.OpenChannel(channelOpenContext(t), hubURL, workerID, &tunnel.OpenChannelOptions{
		LifetimeContext:     ctx,
		HTTPClient:          httpClient,
		WebSocketHTTPClient: httpClient,
	})
	require.NoError(t, err)
	t.Cleanup(ch.Close)

	// Open a tunnel connection to the echo server.
	openPayload, err := proto.Marshal(&leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "echo-flow-conn",
		TargetAddr: echoHost,
		TargetPort: echoPort,
	})
	require.NoError(t, err)

	const connID = "echo-flow-conn"
	openedCh := make(chan error, 1)
	dataCh := make(chan []byte, 16)
	eofCh := make(chan struct{}, 1)
	reqID, err := ch.SendRPCNoWait(ctx, leapmuxv1connect.WorkerTunnelServiceOpenTunnelConnProcedure, openPayload, tunnel.RPCHandlers{
		Stream: func(msg *leapmuxv1.InnerStreamMessage) {
			if msg.GetError() != nil {
				openedCh <- fmt.Errorf("open failed: %s", msg.GetError().GetMessage())
				return
			}
			var event leapmuxv1.TunnelConnEvent
			if err := proto.Unmarshal(msg.GetPayload(), &event); err != nil {
				return
			}
			switch event.GetEvent().(type) {
			case *leapmuxv1.TunnelConnEvent_Opened:
				openedCh <- nil
			case *leapmuxv1.TunnelConnEvent_Data:
				dataCh <- event.GetData()
			case *leapmuxv1.TunnelConnEvent_Eof:
				select {
				case eofCh <- struct{}{}:
				default:
				}
			}
		},
		StreamRejected: func(err error) {
			openedCh <- fmt.Errorf("the channel rejected a frame: %w", err)
		},
	})
	require.NoError(t, err)

	// The first stream frame is the `opened` event.
	select {
	case openErr := <-openedCh:
		require.NoError(t, openErr)

		defer ch.UnregisterStream(reqID)

		// Send data through the tunnel.
		sendPayload, _ := proto.Marshal(&leapmuxv1.SendTunnelDataRequest{
			ConnId: connID,
			Data:   []byte("hello tunnel"),
		})
		_, err := ch.SendRPCNoWait(ctx, leapmuxv1connect.WorkerTunnelServiceSendTunnelDataProcedure, sendPayload, tunnel.RPCHandlers{})
		require.NoError(t, err)

		// Wait for echo data from the stream.
		select {
		case data := <-dataCh:
			assert.Equal(t, "hello tunnel", string(data))
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for echo data")
		}

		// Close the tunnel connection.
		closePayload, _ := proto.Marshal(&leapmuxv1.CloseTunnelConnRequest{ConnId: connID})
		_, err = ch.SendRPCNoWait(ctx, leapmuxv1connect.WorkerTunnelServiceCloseTunnelConnProcedure, closePayload, tunnel.RPCHandlers{})
		require.NoError(t, err)

	case <-time.After(30 * time.Second):
		ch.UnregisterStream(reqID)
		t.Fatal("timeout waiting for the OpenTunnelConn opened event")
	}
}

func TestChannelSocks5EchoFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	echoAddr := testutil.StartEchoServer(t)
	echoHost, echoPort := testutil.ParseAddr(echoAddr)

	hubURL, _, _, workerID, httpClient := startTestSolo(t)
	ctx := context.Background()

	ch, err := tunnel.OpenChannel(channelOpenContext(t), hubURL, workerID, &tunnel.OpenChannelOptions{
		LifetimeContext:     ctx,
		HTTPClient:          httpClient,
		WebSocketHTTPClient: httpClient,
	})
	require.NoError(t, err)
	t.Cleanup(ch.Close)

	// Create a local SOCKS5 proxy listener that forwards via the E2EE channel,
	// simulating what the desktop TunnelManager does for SOCKS5 tunnels.
	socks5Ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = socks5Ln.Close() })

	// Handle one SOCKS5 connection in a goroutine.
	proxyReady := make(chan struct{})
	proxyDone := make(chan error, 1)
	go func() {
		close(proxyReady)
		conn, acceptErr := socks5Ln.Accept()
		if acceptErr != nil {
			proxyDone <- acceptErr
			return
		}
		defer func() { _ = conn.Close() }()

		// Perform SOCKS5 handshake.
		// Phase 1: Greeting.
		header := make([]byte, 2)
		if _, readErr := io.ReadFull(conn, header); readErr != nil {
			proxyDone <- readErr
			return
		}
		nMethods := int(header[1])
		methods := make([]byte, nMethods)
		if _, readErr := io.ReadFull(conn, methods); readErr != nil {
			proxyDone <- readErr
			return
		}
		if _, writeErr := conn.Write([]byte{0x05, 0x00}); writeErr != nil {
			proxyDone <- writeErr
			return
		}

		// Phase 2: CONNECT request.
		reqHeader := make([]byte, 4)
		if _, readErr := io.ReadFull(conn, reqHeader); readErr != nil {
			proxyDone <- readErr
			return
		}
		atyp := reqHeader[3]
		var targetAddrStr string
		switch atyp {
		case 0x01: // IPv4
			ip := make([]byte, 4)
			_, _ = io.ReadFull(conn, ip)
			targetAddrStr = net.IP(ip).String()
		case 0x03: // Domain
			lenBuf := make([]byte, 1)
			_, _ = io.ReadFull(conn, lenBuf)
			domain := make([]byte, lenBuf[0])
			_, _ = io.ReadFull(conn, domain)
			targetAddrStr = string(domain)
		}
		portBuf := make([]byte, 2)
		_, _ = io.ReadFull(conn, portBuf)
		targetPortVal := uint32(portBuf[0])<<8 | uint32(portBuf[1])

		// Open tunnel connection via E2EE channel.
		openPayload, _ := proto.Marshal(&leapmuxv1.OpenTunnelConnRequest{
			ConnId:     "socks-flow-conn",
			TargetAddr: targetAddrStr,
			TargetPort: targetPortVal,
		})

		const connID = "socks-flow-conn"
		openedCh := make(chan error, 1)
		dataCh := make(chan []byte, 16)
		reqID, sendErr := ch.SendRPCNoWait(ctx, leapmuxv1connect.WorkerTunnelServiceOpenTunnelConnProcedure, openPayload, tunnel.RPCHandlers{
			Stream: func(msg *leapmuxv1.InnerStreamMessage) {
				if msg.GetError() != nil {
					openedCh <- fmt.Errorf("open tunnel failed: %s", msg.GetError().GetMessage())
					return
				}
				var event leapmuxv1.TunnelConnEvent
				if proto.Unmarshal(msg.GetPayload(), &event) != nil {
					return
				}
				switch event.GetEvent().(type) {
				case *leapmuxv1.TunnelConnEvent_Opened:
					openedCh <- nil
				case *leapmuxv1.TunnelConnEvent_Data:
					dataCh <- event.GetData()
				}
			},
			StreamRejected: func(err error) {
				openedCh <- fmt.Errorf("the channel rejected a frame: %w", err)
			},
		})
		if sendErr != nil {
			proxyDone <- sendErr
			return
		}

		if openErr := <-openedCh; openErr != nil {
			// Send SOCKS5 failure.
			_, _ = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			proxyDone <- openErr
			return
		}

		// Send SOCKS5 success reply.
		_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

		defer ch.UnregisterStream(reqID)

		// Bidirectional forwarding.
		done := make(chan struct{}, 2)

		// Worker -> client.
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				select {
				case data := <-dataCh:
					if _, err := conn.Write(data); err != nil {
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()

		// Client -> Worker. Assign the per-conn monotonic write sequence the
		// worker applies in order (as tunnel.Conn.Write does), so chunked SOCKS
		// traffic is not reordered or wedged by the worker's ordered write gate.
		var writeSeq atomic.Uint64
		go func() {
			defer func() { done <- struct{}{} }()
			buf := make([]byte, 32*1024)
			for {
				n, readErr := conn.Read(buf)
				if n > 0 {
					payload, _ := proto.Marshal(&leapmuxv1.SendTunnelDataRequest{
						ConnId: connID,
						Data:   buf[:n],
						Seq:    writeSeq.Add(1) - 1,
					})
					if _, err := ch.SendRPCNoWait(ctx, leapmuxv1connect.WorkerTunnelServiceSendTunnelDataProcedure, payload, tunnel.RPCHandlers{}); err != nil {
						return
					}
				}
				if readErr != nil {
					return
				}
			}
		}()

		<-done
		closePayload, _ := proto.Marshal(&leapmuxv1.CloseTunnelConnRequest{ConnId: connID, Seq: writeSeq.Load()})
		_, _ = ch.SendRPCNoWait(ctx, leapmuxv1connect.WorkerTunnelServiceCloseTunnelConnProcedure, closePayload, tunnel.RPCHandlers{})
		proxyDone <- nil
	}()

	<-proxyReady

	// Now act as a SOCKS5 client: connect to the proxy and request the echo server.
	client, err := net.Dial("tcp", socks5Ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	// SOCKS5 greeting.
	_, err = client.Write([]byte{0x05, 0x01, 0x00})
	require.NoError(t, err)
	greetReply := make([]byte, 2)
	_, err = io.ReadFull(client, greetReply)
	require.NoError(t, err)
	assert.Equal(t, byte(0x05), greetReply[0])
	assert.Equal(t, byte(0x00), greetReply[1])

	// SOCKS5 CONNECT to echo server (IPv4).
	ip := net.ParseIP(echoHost).To4()
	require.NotNil(t, ip)
	connectReq := []byte{0x05, 0x01, 0x00, 0x01}
	connectReq = append(connectReq, ip...)
	connectReq = append(connectReq, byte(echoPort>>8), byte(echoPort))
	_, err = client.Write(connectReq)
	require.NoError(t, err)

	connectReply := make([]byte, 10)
	_, err = io.ReadFull(client, connectReply)
	require.NoError(t, err)
	assert.Equal(t, byte(0x05), connectReply[0])
	assert.Equal(t, byte(0x00), connectReply[1], "expected SOCKS5 success reply")

	// Now the SOCKS5 tunnel is established. Send data and verify echo.
	testData := []byte("hello via socks5")
	_, err = client.Write(testData)
	require.NoError(t, err)

	echoed := make([]byte, len(testData))
	_, err = io.ReadFull(client, echoed)
	require.NoError(t, err)
	assert.Equal(t, string(testData), string(echoed), "expected data echoed through SOCKS5 tunnel")
}

// The legacy Unix-socket auto-approval path is gone. In the new flow,
// solo mode registers workers through Server.RegisterWorker (in-process,
// no RPC) — covered by solo's own tests — and external workers must
// present a registration key minted via WorkerManagementService. The
// service-level coverage of that flow lives in
// internal/hub/service/worker_registration_test.go.

func TestChannelMultipleRPCs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	hubURL, _, _, workerID, httpClient := startTestSolo(t)
	ctx := context.Background()

	ch, err := tunnel.OpenChannel(channelOpenContext(t), hubURL, workerID, &tunnel.OpenChannelOptions{
		LifetimeContext:     ctx,
		HTTPClient:          httpClient,
		WebSocketHTTPClient: httpClient,
	})
	require.NoError(t, err)
	t.Cleanup(ch.Close)

	// Send multiple RPCs to verify the channel handles sequential calls.
	for i := 0; i < 5; i++ {
		payload, _ := proto.Marshal(&leapmuxv1.GetWorkerSystemInfoRequest{})
		resp, err := ch.CallRPC(ctx, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, payload)
		require.NoError(t, err, "RPC %d failed", i)

		var sysInfo leapmuxv1.GetWorkerSystemInfoResponse
		require.NoError(t, proto.Unmarshal(resp.GetPayload(), &sysInfo))
		assert.NotEmpty(t, sysInfo.GetOs())
	}
}

func TestChannelConcurrentRPCs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	hubURL, _, _, workerID, httpClient := startTestSolo(t)
	ctx := context.Background()
	ch, err := tunnel.OpenChannel(channelOpenContext(t), hubURL, workerID, &tunnel.OpenChannelOptions{
		LifetimeContext:     ctx,
		HTTPClient:          httpClient,
		WebSocketHTTPClient: httpClient,
	})
	require.NoError(t, err)
	t.Cleanup(ch.Close)

	const calls = 32
	start := make(chan struct{})
	errCh := make(chan error, calls)
	var wg sync.WaitGroup
	for range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			payload, marshalErr := proto.Marshal(&leapmuxv1.GetWorkerSystemInfoRequest{})
			if marshalErr != nil {
				errCh <- marshalErr
				return
			}
			_, callErr := ch.CallRPC(ctx, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, payload)
			errCh <- callErr
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for callErr := range errCh {
		require.NoError(t, callErr)
	}
}

// A large multi-chunk CallRPC must not stall multiplexed tunnel traffic: the
// SendGate lets single-chunk tunnel frames overtake between chunks, and the
// Hub's one-in-flight chunked-sequence tracker accepts that interleaving. A
// violation tears the channel down, so this fails loudly rather than subtly.
func TestChannelLargeRPCDoesNotStallTunnelTraffic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	echoAddr := testutil.StartEchoServer(t)
	echoHost, echoPort := testutil.ParseAddr(echoAddr)

	hubURL, _, _, workerID, httpClient := startTestSolo(t)
	ctx := context.Background()

	ch, err := tunnel.OpenChannel(channelOpenContext(t), hubURL, workerID, &tunnel.OpenChannelOptions{
		LifetimeContext:     ctx,
		HTTPClient:          httpClient,
		WebSocketHTTPClient: httpClient,
	})
	require.NoError(t, err)
	t.Cleanup(ch.Close)

	conn, err := tunnel.DialTunnel(ch, echoHost, echoPort)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// A payload larger than one Noise transport chunk forces a multi-chunk
	// CallRPC. Ping ignores the body and returns empty — we only need the
	// multi-chunk send on the wire.
	big := make([]byte, 100*1024)
	for i := range big {
		big[i] = byte(i)
	}
	rpcDone := make(chan error, 1)
	go func() {
		_, callErr := ch.CallRPC(ctx, leapmuxv1connect.WorkerSystemServicePingProcedure, big)
		rpcDone <- callErr
	}()

	// Drive echo traffic while the large RPC is in flight. If the RPC
	// head-of-line-blocked the channel, these would stall until it finished.
	const rounds = 16
	for i := range rounds {
		msg := []byte(fmt.Sprintf("echo-%d", i))
		n, writeErr := conn.Write(msg)
		require.NoError(t, writeErr)
		require.Equal(t, len(msg), n)

		buf := make([]byte, len(msg))
		_, readErr := io.ReadFull(conn, buf)
		require.NoError(t, readErr, "tunnel frame %d must keep flowing during the large RPC", i)
		assert.Equal(t, msg, buf)
	}

	select {
	case callErr := <-rpcDone:
		require.NoError(t, callErr, "large multi-chunk Ping must complete")
	case <-time.After(60 * time.Second):
		t.Fatal("large CallRPC did not complete")
	}
}

// channelOpenContext uses the public budget for each real handshake. A background
// context would select the Hub's shorter generic send timeout instead.
// Each caller keeps the established channel's lifetime context separate.
func channelOpenContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), tunnel.DefaultChannelOpenTimeout)
	t.Cleanup(cancel)
	return ctx
}
