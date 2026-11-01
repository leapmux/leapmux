package service_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/sendq"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/mail"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type channelTestEnv struct {
	channelClient   leapmuxv1connect.ChannelServiceClient
	connectorClient leapmuxv1connect.WorkerConnectorServiceClient
	mgmtClient      leapmuxv1connect.WorkerManagementServiceClient
	authClient      leapmuxv1connect.AuthServiceClient
	store           store.Store
	workerMgr       *workermgr.Manager
	channelMgr      *channelmgr.Manager
	pending         *workermgr.PendingRequests
	mux             *http.ServeMux
	// clock drives the connector's hello and idle deadlines.
	clock *quartz.Mock
}

func setupChannelTestServer(t *testing.T) *channelTestEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	hubtestutil.CreateTestAdmin(t, st)

	cfg := testConfig()
	set := servicetest.NewSettingsManager(t, st, nil)
	clock := testutil.NewQuartzMock(t)
	wMgr := workermgr.New(service.NewWorkerReachAuthorizer(st))
	cMgr := channelmgr.New(0)
	pendingReqs := workermgr.NewPendingRequests(func() time.Duration { return settings.DefaultTimeouts.APITimeout() })

	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(sc.Stop)
	connectServer := connect.NewServer(interceptor)

	leapmuxv1connect.RegisterAuthServiceHandler(connectServer, service.NewAuthService(servicetest.AuthServiceDeps(st, cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil))))

	leapmuxv1connect.RegisterWorkerConnectorServiceHandler(connectServer,
		service.NewWorkerConnectorService(st, wMgr, nil, nil, nil, nil, sendq.NewMaxBytesPoolForTest(), clock))

	leapmuxv1connect.RegisterWorkerManagementServiceHandler(connectServer,
		service.NewWorkerManagementService(st, wMgr, nil, nil, mail.NewStubSender(), mail.Renderer{}, set, nil))

	channelSvc := service.NewChannelService(st, wMgr, cMgr, pendingReqs, sc)
	leapmuxv1connect.RegisterChannelServiceHandler(connectServer, channelSvc)
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &channelTestEnv{
		channelClient:   leapmuxv1connect.NewChannelServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0)))),
		connectorClient: leapmuxv1connect.NewWorkerConnectorServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0)))),
		mgmtClient:      leapmuxv1connect.NewWorkerManagementServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0)))),
		authClient:      leapmuxv1connect.NewAuthServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0)))),
		store:           st,
		workerMgr:       wMgr,
		channelMgr:      cMgr,
		pending:         pendingReqs,
		mux:             mux,
		clock:           clock,
	}
}

// classicHello is a valid classic-mode WorkerHello whose X25519 key is filled
// with fill, so two connections' keys are told apart.
func classicHello(fill byte) *leapmuxv1.WorkerHello {
	return &leapmuxv1.WorkerHello{
		Keys:           &leapmuxv1.WorkerPublicKeys{X25519: bytes.Repeat([]byte{fill}, noise.EphemeralPublicKeySize)},
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
	}
}

// postQuantumHello is a valid post-quantum WorkerHello whose keys are filled
// with fill.
func postQuantumHello(fill byte) *leapmuxv1.WorkerHello {
	return &leapmuxv1.WorkerHello{
		Keys: &leapmuxv1.WorkerPublicKeys{
			X25519: bytes.Repeat([]byte{fill}, noise.EphemeralPublicKeySize),
			Mlkem:  bytes.Repeat([]byte{fill}, noise.MlkemPublicKeySize),
			Slhdsa: bytes.Repeat([]byte{fill}, noise.SlhdsaPublicKeySize),
		},
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
	}
}

func helloRequest(hello *leapmuxv1.WorkerHello) *leapmuxv1.ConnectRequest {
	return &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_Hello{Hello: hello}}
}

// requireHandshake asserts that GetWorkerHandshakeParams answers with exactly
// the keys and the mode of want.
func (e *channelTestEnv) requireHandshake(t *testing.T, ctx context.Context, token, workerID string, want *leapmuxv1.WorkerHello) {
	t.Helper()
	resp, err := e.channelClient.GetWorkerHandshakeParams(authedReq(ctx,
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID}, token))
	require.NoError(t, err)
	assert.True(t, proto.Equal(want.GetKeys(), resp.GetKeys()), "the keys must be the selected connection's")
	assert.Equal(t, want.GetEncryptionMode(), resp.GetEncryptionMode(), "the mode must be the selected connection's")
}

// streamConnector returns a WorkerConnectorService client served over h2c on a
// unix socket. Connect is a bidi stream, which the env's HTTP/1 httptest server
// cannot carry.
func (e *channelTestEnv) streamConnector(t *testing.T, name string) leapmuxv1connect.WorkerConnectorServiceClient {
	t.Helper()
	socketURL := hubtransporttest.NewSocketServer(t, name, e.mux)
	endpoint, err := hubtransport.New(socketURL)
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	return leapmuxv1connect.NewWorkerConnectorServiceClient(connect.NewClient(connecthttp.NewTransport(
		endpoint.StreamClient(), "http://localhost", connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
}

// openWorkerStream opens a Connect stream authenticated as the worker.
func openWorkerStream(t *testing.T, ctx context.Context, client leapmuxv1connect.WorkerConnectorServiceClient, authToken string) leapmuxv1connect.WorkerConnectorServiceConnectClientStream {
	t.Helper()
	streamCtx, streamInfo := connect.NewClientContext(ctx)
	streamInfo.RequestHeader().Set("Authorization", "Bearer "+authToken)
	stream, err := client.Connect(streamCtx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

func (e *channelTestEnv) adminToken(t *testing.T) string {
	t.Helper()
	ctx, info := connect.NewClientContext(context.Background())
	_, err := e.authClient.Login(ctx, &leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin123",
	})
	require.NoError(t, err)
	return sessionFromCookie(t, info.ResponseHeader().Get("Set-Cookie"))
}

func (e *channelTestEnv) createWorker(t *testing.T, token string) string {
	t.Helper()
	ctx := context.Background()

	// An authenticated user mints a registration key, then the worker presents
	// it as a bearer credential. We exercise the actual RPC path here so the
	// auth interceptor allowlist and the consume transaction are covered
	// alongside the channel tests.
	createCtx, createReq := authedReq(ctx, &leapmuxv1.CreateRegistrationKeyRequest{}, token)
	createResp, err := e.mgmtClient.CreateRegistrationKey(createCtx, createReq)
	require.NoError(t, err)

	ctx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("Authorization", "Bearer "+createResp.GetRegistrationKey())
	regResp, err := e.connectorClient.Register(ctx, &leapmuxv1.RegisterRequest{})
	require.NoError(t, err)

	return regResp.GetWorkerId()
}

func registerOnlineWorker(t *testing.T, env *channelTestEnv, workerID string, hello *leapmuxv1.WorkerHello) {
	t.Helper()
	conn := workermgrtest.NewConnWithHello(t, workerID, hello, func(*leapmuxv1.ConnectResponse) error { return nil })
	_, err := env.workerMgr.Register(conn)
	require.NoError(t, err)
}

func TestGetWorkerHandshakeParams(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorker(t, token)
	hello := postQuantumHello(0x5a)
	registerOnlineWorker(t, env, workerID, hello)

	env.requireHandshake(t, ctx, token, workerID, hello)
}

// workerStreams sets up a registered worker and a stream client for the tests
// that drive the real Connect handler.
func workerStreams(t *testing.T, name string) (env *channelTestEnv, ctx context.Context, token, workerID, authToken string, client leapmuxv1connect.WorkerConnectorServiceClient) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on Windows")
	}
	env = setupChannelTestServer(t)
	ctx = testutil.DeadlineContext(t)
	token = env.adminToken(t)
	workerID = env.createWorker(t, token)
	worker, err := env.store.Workers().GetByID(ctx, workerID)
	require.NoError(t, err)
	return env, ctx, token, workerID, worker.AuthToken, env.streamConnector(t, name)
}

// connectWorker opens a stream, sends hello, and waits for the identity frame
// that proves the Hub registered the connection.
func connectWorker(t *testing.T, ctx context.Context, client leapmuxv1connect.WorkerConnectorServiceClient, authToken string, hello *leapmuxv1.WorkerHello) leapmuxv1connect.WorkerConnectorServiceConnectClientStream {
	t.Helper()
	stream := openWorkerStream(t, ctx, client, authToken)
	require.NoError(t, stream.Send(helloRequest(hello)))
	identity, err := stream.Receive()
	require.NoError(t, err)
	require.NotNil(t, identity.GetWorkerIdentity(), "the first frame after a valid hello is WorkerIdentity")
	return stream
}

// The keys and the encryption mode a client hands to the Noise handshake must
// come from ONE worker connection. A connection whose hello has not arrived is
// not registered, so the answer stays the previous connection's whole pair.
func TestGetWorkerHandshakeParamsNeverPairsAModeWithAnotherConnectionsKeys(t *testing.T) {
	t.Parallel()
	env, ctx, token, workerID, authToken, client := workerStreams(t, "hub-handshake-pair")

	helloA := classicHello(0xa1)
	connectWorker(t, ctx, client, authToken, helloA)
	env.requireHandshake(t, ctx, token, workerID, helloA)

	// Connection B opens and withholds its hello. The hello timer proves B's
	// handler is running and waiting for it.
	newTimer, stopTimer := testutil.NewTimerTraps(t, env.clock, "worker-hello")
	streamB := openWorkerStream(t, ctx, client, authToken)
	require.NoError(t, streamB.SendHeaders())
	testutil.WaitForTimer(t, ctx, newTimer)
	env.requireHandshake(t, ctx, token, workerID, helloA)

	// After B's hello, B is registered and the answer is B's whole pair. The
	// trap also holds the hello timer's stop, which B reaches as it leaves the
	// wait; release it.
	helloB := postQuantumHello(0xb2)
	require.NoError(t, streamB.Send(helloRequest(helloB)))
	stopTimer.MustWait(ctx).MustRelease(ctx)
	identityB, err := streamB.Receive()
	require.NoError(t, err)
	require.NotNil(t, identityB.GetWorkerIdentity())
	env.requireHandshake(t, ctx, token, workerID, helloB)
}

// A replacement connection is invisible to GetWorkerHandshakeParams until the
// moment the registry selects it: after its hello is read and before it is
// registered, and after a refusal, the answer is still the current
// connection's.
func TestGetWorkerHandshakeParamsAnswersFromTheSelectedConnection(t *testing.T) {
	t.Parallel()
	env, ctx, token, workerID, authToken, client := workerStreams(t, "hub-handshake-selected")

	helloA := classicHello(0xa1)
	connectWorker(t, ctx, client, authToken, helloA)

	// Gate B between its hello and its registration: receiveHello stops its
	// hello timer as it returns, and the trap parks it there.
	newTimer, stopTimer := testutil.NewTimerTraps(t, env.clock, "worker-hello")
	helloB := postQuantumHello(0xb2)
	streamB := openWorkerStream(t, ctx, client, authToken)
	require.NoError(t, streamB.Send(helloRequest(helloB)))
	testutil.WaitForTimer(t, ctx, newTimer)
	stop := stopTimer.MustWait(ctx)
	env.requireHandshake(t, ctx, token, workerID, helloA)
	stop.MustRelease(ctx)
	identityB, err := streamB.Receive()
	require.NoError(t, err)
	require.NotNil(t, identityB.GetWorkerIdentity())
	env.requireHandshake(t, ctx, token, workerID, helloB)
}

// A refused replacement leaves the registered connection's answer unchanged.
func TestGetWorkerHandshakeParamsIgnoresARefusedReplacement(t *testing.T) {
	t.Parallel()
	env, ctx, token, workerID, authToken, client := workerStreams(t, "hub-handshake-refused")

	helloA := classicHello(0xa1)
	connectWorker(t, ctx, client, authToken, helloA)

	bad := classicHello(0xc3)
	bad.Keys.X25519 = bad.Keys.X25519[:16]
	streamC := openWorkerStream(t, ctx, client, authToken)
	require.NoError(t, streamC.Send(helloRequest(bad)))
	_, err := streamC.Receive()
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	env.requireHandshake(t, ctx, token, workerID, helloA)
}

// Two replacements racing each other: every answer is the whole snapshot of
// one connection, never the keys of one and the mode of another.
func TestGetWorkerHandshakeParamsNeverMixesConcurrentReplacements(t *testing.T) {
	t.Parallel()
	env, ctx, token, workerID, authToken, client := workerStreams(t, "hub-handshake-race")

	helloA := classicHello(0xa1)
	connectWorker(t, ctx, client, authToken, helloA)
	helloB := postQuantumHello(0xb2)
	helloC := classicHello(0xc3)

	var wg sync.WaitGroup
	for _, hello := range []*leapmuxv1.WorkerHello{helloB, helloC} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream := openWorkerStream(t, ctx, client, authToken)
			_ = stream.Send(helloRequest(hello))
			// Hold the stream until the test ends; a replaced stream ends
			// on its own.
			for {
				if _, err := stream.Receive(); err != nil {
					return
				}
			}
		}()
	}

	known := []*leapmuxv1.WorkerHello{helloA, helloB, helloC}
	for range 50 {
		resp, err := env.channelClient.GetWorkerHandshakeParams(authedReq(ctx,
			&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID}, token))
		if err != nil {
			// The worker is briefly offline between a replacement's fence
			// and its registration.
			continue
		}
		matched := false
		for _, hello := range known {
			if proto.Equal(hello.GetKeys(), resp.GetKeys()) {
				assert.Equal(t, hello.GetEncryptionMode(), resp.GetEncryptionMode(),
					"the mode must belong to the connection whose keys were returned")
				matched = true
			}
		}
		assert.True(t, matched, "the keys must belong to one of the connections")
	}
	// End the surviving stream, so both stream goroutines return.
	env.workerMgr.FenceAll()
	wg.Wait()
}

// Connect refuses every first message that is not a usable hello, before it
// registers anything.
func TestConnectRefusesAnUnusableFirstMessage(t *testing.T) {
	t.Parallel()
	env, ctx, token, workerID, authToken, client := workerStreams(t, "hub-hello-refusals")

	short := classicHello(0x01)
	short.Keys.X25519 = short.Keys.X25519[:31]
	unspecified := classicHello(0x02)
	unspecified.EncryptionMode = leapmuxv1.EncryptionMode_ENCRYPTION_MODE_UNSPECIFIED
	pqNoMlkem := postQuantumHello(0x03)
	pqNoMlkem.Keys.Mlkem = nil
	pqNoSlhdsa := postQuantumHello(0x04)
	pqNoSlhdsa.Keys.Slhdsa = nil
	classicBadMlkem := classicHello(0x05)
	classicBadMlkem.Keys.Mlkem = bytes.Repeat([]byte{0x05}, 100)

	for name, first := range map[string]*leapmuxv1.ConnectRequest{
		"a heartbeat":                         {Payload: &leapmuxv1.ConnectRequest_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}}},
		"a hello with no keys":                helloRequest(&leapmuxv1.WorkerHello{EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC}),
		"an unspecified mode":                 helloRequest(unspecified),
		"a short x25519 key":                  helloRequest(short),
		"a post-quantum hello with no mlkem":  helloRequest(pqNoMlkem),
		"a post-quantum hello with no slhdsa": helloRequest(pqNoSlhdsa),
		"a classic hello with a short mlkem":  helloRequest(classicBadMlkem),
	} {
		t.Run(name, func(t *testing.T) {
			stream := openWorkerStream(t, ctx, client, authToken)
			require.NoError(t, stream.Send(first))
			_, err := stream.Receive()
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
	_, err := env.channelClient.GetWorkerHandshakeParams(authedReq(ctx,
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID}, token))
	require.Error(t, err, "no refused connection may be listed online")
}

// A hello that never arrives ends the stream at the hello deadline.
func TestConnectEndsAStreamWhoseHelloNeverArrives(t *testing.T) {
	t.Parallel()
	env, ctx, _, _, authToken, client := workerStreams(t, "hub-hello-deadline")

	newTimer, stopTimer := testutil.NewTimerTraps(t, env.clock, "worker-hello")
	stream := openWorkerStream(t, ctx, client, authToken)
	require.NoError(t, stream.SendHeaders())
	delay := testutil.WaitForTimer(t, ctx, newTimer)
	testutil.AdvanceAndAwaitStop(t, ctx, env.clock, delay, stopTimer)

	_, err := stream.Receive()
	require.Error(t, err)
	assert.Equal(t, connect.CodeDeadlineExceeded, connect.CodeOf(err))
}

// A worker that hangs up before its hello ends the stream cleanly: Connect
// returns no error, and the worker is never listed online.
func TestConnectEndsQuietlyWhenTheWorkerClosesBeforeItsHello(t *testing.T) {
	t.Parallel()
	env, ctx, token, workerID, authToken, client := workerStreams(t, "hub-hello-eof")

	stream := openWorkerStream(t, ctx, client, authToken)
	require.NoError(t, stream.SendHeaders())
	require.NoError(t, stream.CloseSend())
	_, err := stream.Receive()
	require.ErrorIs(t, err, io.EOF, "a hang-up before the hello is no protocol error")

	_, err = env.channelClient.GetWorkerHandshakeParams(authedReq(ctx,
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "a connection with no hello is never registered")
}

// connectWorkerUnderIdleTrap is connectWorker for a test that traps the idle
// timer. The handler arms that timer before its loop writes the greeting, so
// the trap must release the timer before the identity frame can arrive. It
// returns the stream and the delay the timer asked for.
func connectWorkerUnderIdleTrap(
	t *testing.T, ctx context.Context, client leapmuxv1connect.WorkerConnectorServiceClient,
	authToken string, hello *leapmuxv1.WorkerHello, newIdleTimer *quartz.Trap,
) (leapmuxv1connect.WorkerConnectorServiceConnectClientStream, time.Duration) {
	t.Helper()
	stream := openWorkerStream(t, ctx, client, authToken)
	require.NoError(t, stream.Send(helloRequest(hello)))
	delay := testutil.WaitForTimer(t, ctx, newIdleTimer)
	identity, err := stream.Receive()
	require.NoError(t, err)
	require.NotNil(t, identity.GetWorkerIdentity(), "the first frame after a valid hello is WorkerIdentity")
	return stream, delay
}

// A worker that goes silent after its hello is assumed dead at the receive
// idle deadline: the Hub ends the stream and lists the worker offline.
func TestConnectEndsAStreamThatFallsSilentAfterItsHello(t *testing.T) {
	t.Parallel()
	env, ctx, token, workerID, authToken, client := workerStreams(t, "hub-idle-deadline")

	newTimer, stopTimer := testutil.NewTimerTraps(t, env.clock, "worker-idle")
	hello := classicHello(0x51)
	stream, delay := connectWorkerUnderIdleTrap(t, ctx, client, authToken, hello, newTimer)
	assert.Equal(t, 10*time.Second, delay, "the idle deadline is the one the worker's heartbeat interval is sized for")
	env.requireHandshake(t, ctx, token, workerID, hello)

	testutil.AdvanceAndAwaitStop(t, ctx, env.clock, delay, stopTimer)
	_, err := stream.Receive()
	require.ErrorIs(t, err, io.EOF, "the idle deadline ends the stream without an error code")

	_, err = env.channelClient.GetWorkerHandshakeParams(authedReq(ctx,
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "a worker that timed out is offline")
}

// Every message from the worker re-arms the idle deadline for its full length,
// so a worker that heartbeats inside the deadline stays connected past it.
func TestConnectHeartbeatRearmsTheIdleDeadline(t *testing.T) {
	t.Parallel()
	env, ctx, token, workerID, authToken, client := workerStreams(t, "hub-idle-rearm")

	newTimer, stopTimer := testutil.NewTimerTraps(t, env.clock, "worker-idle")
	resetTimer := env.clock.Trap().TimerReset("worker-idle")
	t.Cleanup(resetTimer.Close)
	hello := classicHello(0x61)
	stream, delay := connectWorkerUnderIdleTrap(t, ctx, client, authToken, hello, newTimer)

	heartbeat := func() {
		t.Helper()
		require.NoError(t, stream.Send(&leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}}}))
		stopTimer.MustWait(ctx).MustRelease(ctx)
		reset := resetTimer.MustWait(ctx)
		assert.Equal(t, delay, reset.Duration, "a message re-arms the deadline for its full length")
		reset.MustRelease(ctx)
		for {
			frame, err := stream.Receive()
			require.NoError(t, err)
			if frame.GetHeartbeat() != nil {
				return
			}
		}
	}

	// Two heartbeats, each one second before the deadline it re-arms. Past
	// the first deadline the worker is still connected.
	env.clock.Advance(delay - time.Second).MustWait(ctx)
	heartbeat()
	env.clock.Advance(delay - time.Second).MustWait(ctx)
	heartbeat()
	env.requireHandshake(t, ctx, token, workerID, hello)

	// Silence for the full deadline after the last heartbeat ends the stream.
	testutil.AdvanceAndAwaitStop(t, ctx, env.clock, delay, stopTimer)
	_, err := stream.Receive()
	require.ErrorIs(t, err, io.EOF)
}

// An empty heartbeat keeps the stream alive and updates last_seen.
func TestConnectHeartbeatUpdatesLastSeen(t *testing.T) {
	t.Parallel()
	env, ctx, _, workerID, authToken, client := workerStreams(t, "hub-heartbeat")

	stream := connectWorker(t, ctx, client, authToken, classicHello(0x11))
	before, err := env.store.Workers().GetByID(ctx, workerID)
	require.NoError(t, err)
	require.NotNil(t, before.LastSeenAt)

	require.NoError(t, stream.Send(&leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}}}))
	for {
		frame, err := stream.Receive()
		require.NoError(t, err)
		if frame.GetHeartbeat() != nil {
			break
		}
	}
	after, err := env.store.Workers().GetByID(ctx, workerID)
	require.NoError(t, err)
	require.NotNil(t, after.LastSeenAt)
	assert.False(t, after.LastSeenAt.Before(*before.LastSeenAt), "a heartbeat moves last_seen forward or keeps it")
}

// A second hello on one stream is a protocol error.
func TestConnectRefusesASecondHello(t *testing.T) {
	t.Parallel()
	_, ctx, _, _, authToken, client := workerStreams(t, "hub-second-hello")

	stream := connectWorker(t, ctx, client, authToken, classicHello(0x21))
	require.NoError(t, stream.Send(helloRequest(classicHello(0x21))))
	for {
		_, err := stream.Receive()
		if err != nil {
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			return
		}
	}
}

func TestGetWorkerHandshakeParams_NotFound(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.GetWorkerHandshakeParams(authedReq(ctx,
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: "nonexistent"}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_EmptyWorkerID(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.GetWorkerHandshakeParams(authedReq(ctx,
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: ""}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_WorkerOffline(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Worker exists with a public key but is not registered as online.
	workerID := env.createWorker(t, token)

	_, err := env.channelClient.GetWorkerHandshakeParams(authedReq(ctx,
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_RejectsStaleAuthGeneration(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	checker := &freshnessAfterNCalls{staleAfter: 0}
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, checker)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: userid.MustNew(env.user.ID), Username: env.user.Username, AuthGeneration: 0,
	})

	_, err := channelSvc.GetWorkerHandshakeParams(ctx, &leapmuxv1.GetWorkerHandshakeParamsRequest{
		WorkerId: env.workerID,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Equal(t, int32(1), checker.calls.Load(), "handshake params should reject stale auth before worker lookup")
}

func TestOpenChannel_WorkerOffline(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorker(t, token)

	_, err := env.channelClient.OpenChannel(authedReq(ctx,
		&leapmuxv1.OpenChannelRequest{
			WorkerId:         workerID,
			HandshakePayload: []byte("handshake-msg-1"),
		}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestOpenChannel_EmptyFields(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Empty worker_id.
	_, err := env.channelClient.OpenChannel(authedReq(ctx,
		&leapmuxv1.OpenChannelRequest{
			WorkerId:         "",
			HandshakePayload: []byte("data"),
		}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestOpenChannel_WithMockWorker(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorker(t, token)

	// Simulate worker online by registering a mock connection.
	sentCh := make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, workerID, func(msg *leapmuxv1.ConnectResponse) error {
		sentCh <- msg
		return nil

	})

	_, _ = env.workerMgr.Register(conn)

	// OpenChannel should fail with timeout because mock worker doesn't respond.
	// Use a short timeout context.
	shortCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err := env.channelClient.OpenChannel(authedReq(shortCtx,
		&leapmuxv1.OpenChannelRequest{
			WorkerId:         workerID,
			HandshakePayload: []byte("handshake-msg-1"),
		}, token))
	require.Error(t, err)
	// The sent message should have been a ChannelOpenRequest.
	select {
	case sentMsg := <-sentCh:
		assert.NotNil(t, sentMsg.GetChannelOpen())
		assert.Equal(t, []byte("handshake-msg-1"), sentMsg.GetChannelOpen().GetHandshakePayload())
		assert.Equal(t, uint64(contracts.MaxMessageSize), sentMsg.GetChannelOpen().GetMaxMessageSize(),
			"hub must announce its resolved max_message_size on ChannelOpenRequest")
	case <-time.After(time.Second):
		require.Fail(t, "expected a message to be sent to worker")
	}
}

type freshnessAfterNCalls struct {
	calls      atomic.Int32
	staleAfter int32
}

type allowAllAuthFreshness struct{}

func (allowAllAuthFreshness) IsAuthContextCurrent(*auth.UserInfo) bool { return true }

func (allowAllAuthFreshness) CurrentCredentialExpiry(_ context.Context, u *auth.UserInfo) auth.CredentialDeadline {
	if u == nil {
		return auth.UnsetDeadline()
	}
	return u.CredentialExpiresAt
}

func TestNewChannelServiceRequiresAuthFreshnessChecker(t *testing.T) {
	t.Parallel()

	require.Panics(t, func() {
		service.NewChannelService(nil, nil, nil, nil, nil)
	})
	var typedNil *auth.AuthContextRegistry
	require.Panics(t, func() {
		service.NewChannelService(nil, nil, nil, nil, typedNil)
	})
}

func (c *freshnessAfterNCalls) IsAuthContextCurrent(*auth.UserInfo) bool {
	return c.calls.Add(1) <= c.staleAfter
}

func (c *freshnessAfterNCalls) CurrentCredentialExpiry(_ context.Context, u *auth.UserInfo) auth.CredentialDeadline {
	if u == nil {
		return auth.UnsetDeadline()
	}
	return u.CredentialExpiresAt
}

type directOpenChannelEnv struct {
	store       store.Store
	user        *store.User
	workerID    string
	workspaceID string
	worker      *workermgr.Manager
	channels    *channelmgr.Manager
	pending     *workermgr.PendingRequests
	sent        chan *leapmuxv1.ConnectResponse
}

func setupDirectOpenChannelEnv(t *testing.T) *directOpenChannelEnv {
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	user, err := st.Users().GetByUsername(context.Background(), hubtestutil.TestAdminUsername)
	require.NoError(t, err)

	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:           workerID,
		AuthToken:    id.Generate(),
		RegisteredBy: userid.MustNew(user.ID),
	}))
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID: workspaceID, OwnerUserID: userid.MustNew(user.ID), Title: "ws",
	}))

	wMgr := workermgr.New(service.NewWorkerReachAuthorizer(st))
	cMgr := channelmgr.New(0)
	// Generous, because no test on this env asserts the deadline: every one of
	// them either completes the request from the conn's write callback or fails
	// before the request is ever issued. A short default is then a wall-clock
	// race the whole file runs against -- at 100ms, a `-race` run of the package
	// crossed it often enough to fail an assertion with CodeUnavailable, blaming
	// the error-code mapping for a timeout. Matches workermgr's own pending
	// tests. A test that WANTS the deadline should build its own env with a
	// short one, the way TestOpenChannel_WithMockWorker bounds its context.
	pendingReqs := workermgr.NewPendingRequests(func() time.Duration { return 30 * time.Second })
	sent := make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, workerID, func(msg *leapmuxv1.ConnectResponse) error {
		sent <- msg
		return nil

	})
	_, _ = wMgr.Register(conn)
	return &directOpenChannelEnv{
		store:       st,
		user:        user,
		workerID:    workerID,
		workspaceID: workspaceID,
		worker:      wMgr,
		channels:    cMgr,
		pending:     pendingReqs,
		sent:        sent,
	}
}

func TestOpenChannel_UnregistersWhenAuthRevokedDuringRegistration(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)

	checker := &freshnessAfterNCalls{staleAfter: 1}
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, checker)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: userid.MustNew(env.user.ID), Username: env.user.Username, AuthGeneration: 0,
	})

	_, err := channelSvc.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Empty(t, env.channels.CloseByUserRevocation(env.user.ID, 1), "stale auth must not leave a registered channel")
	assert.Equal(t, int32(2), checker.calls.Load(), "OpenChannel should check before and after registration")
	select {
	case <-env.sent:
		require.Fail(t, "stale auth must be rejected before the worker handshake is sent")
	default:
	}
}

func TestOpenChannel_ClosesWorkerChannelWhenAuthRevokedDuringHandshake(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 2)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if msg.GetChannelOpen() != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        msg.GetChannelOpen().GetChannelId(),
						HandshakePayload: []byte("worker-handshake"),
						MaxMessageSize:   msg.GetChannelOpen().GetMaxMessageSize(),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	checker := &freshnessAfterNCalls{staleAfter: 3}
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, checker)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: userid.MustNew(env.user.ID), Username: env.user.Username, AuthGeneration: 0,
	})

	_, err := channelSvc.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Empty(t, env.channels.CloseByUserRevocation(env.user.ID, 1), "stale auth must not leave a registered channel")
	assert.Equal(t, int32(4), checker.calls.Load(), "OpenChannel should re-check after the worker opens the channel")

	var openedID string
	select {
	case sentMsg := <-env.sent:
		open := sentMsg.GetChannelOpen()
		require.NotNil(t, open, "first worker message should open the channel")
		openedID = open.GetChannelId()
	default:
		require.Fail(t, "expected ChannelOpen to be sent to worker")
	}
	select {
	case sentMsg := <-env.sent:
		closeMsg := sentMsg.GetChannelClose()
		require.NotNil(t, closeMsg, "revoked auth after worker open must send ChannelClose")
		assert.Equal(t, openedID, closeMsg.GetChannelId())
	case <-time.After(time.Second):
		require.Fail(t, "expected ChannelClose to compensate for worker-side open")
	}
}

func TestOpenChannel_ClosesLocalChannelWhenOpenWriteFails(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 2)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if msg.GetChannelOpen() != nil {
			return errors.New("worker stream reset")
		}
		return nil
	})
	_, _ = env.worker.Register(conn)
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: userid.MustNew(env.user.ID), Username: env.user.Username,
	})

	_, err := channelSvc.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	open := <-env.sent
	require.NotNil(t, open.GetChannelOpen())
	// A write failure fences the conn, so ChannelClose compensation cannot be
	// delivered on the dead stream. The local channel must still be torn down.
	assert.False(t, env.channels.Exists(open.GetChannelOpen().GetChannelId()))
	select {
	case msg := <-env.sent:
		t.Fatalf("fenced conn must not deliver further frames, got %T", msg.GetPayload())
	case <-time.After(50 * time.Millisecond):
	}
}

func TestOpenChannel_MapsWorkerErrorCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		code leapmuxv1.ChannelOpenErrorCode
		want connect.Code
	}{
		{
			name: "invalid max message size",
			code: leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_INVALID_MAX_MESSAGE_SIZE,
			want: connect.CodeInvalidArgument,
		},
		{
			name: "channel already active",
			code: leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_CHANNEL_ALREADY_ACTIVE,
			want: connect.CodeFailedPrecondition,
		},
		{
			name: "no authenticated user",
			code: leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_NO_AUTHENTICATED_USER,
			want: connect.CodeInternal,
		},
		{
			name: "handshake failed",
			code: leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_HANDSHAKE_FAILED,
			want: connect.CodeInvalidArgument,
		},
		{
			name: "unspecified stays internal",
			code: leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_UNSPECIFIED,
			want: connect.CodeInternal,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupDirectOpenChannelEnv(t)
			env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
			conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
				env.sent <- msg
				if open := msg.GetChannelOpen(); open != nil {
					env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
						Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
							ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
								ChannelId: open.GetChannelId(),
								Error:     "worker reject: " + tc.name,
								ErrorCode: tc.code,
							},
						},
					})
				}
				return nil

			})
			_, _ = env.worker.Register(conn)

			channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
			ctx := auth.WithUser(context.Background(), &auth.UserInfo{
				ID:       userid.MustNew(env.user.ID),
				Username: env.user.Username,
			})
			_, err := channelSvc.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{
				WorkerId:         env.workerID,
				HandshakePayload: []byte("handshake"),
			})
			require.Error(t, err)
			assert.Equal(t, tc.want, connect.CodeOf(err), "code %v must map via channelwire.ConnectCodeFromChannelOpenError", tc.code)
		})
	}
}

func TestOpenChannel_RejectsErrorCodeOnlyWithoutErrorString(t *testing.T) {
	t.Parallel()

	// Hub must fail closed when ErrorCode is set even if Error is empty, so a
	// buggy/hostile worker cannot fall through to the success path.
	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        open.GetChannelId(),
						ErrorCode:        leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_CHANNEL_ALREADY_ACTIVE,
						MaxMessageSize:   uint64(contracts.MaxMessageSize),
						HandshakePayload: []byte("should-not-matter"),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	_, err := channelSvc.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "worker rejected channel")
	assert.Contains(t, err.Error(), leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_CHANNEL_ALREADY_ACTIVE.String(),
		"empty Error must fall back to the ErrorCode name so the reject is still attributable")
}

func TestOpenChannel_MapsInvalidMaxMessageSizeErrorCode(t *testing.T) {
	t.Parallel()

	// Kept as a focused alias of the table above for the original skew case.
	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId: open.GetChannelId(),
						Error:     "invalid hub max_message_size: below floor",
						ErrorCode: leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_INVALID_MAX_MESSAGE_SIZE,
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	_, err := channelSvc.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid hub max_message_size")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err),
		"structured INVALID_MAX_MESSAGE_SIZE must map to InvalidArgument, not Internal")
}

func TestOpenChannel_UnspecifiedWorkerErrorStaysInternal(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId: open.GetChannelId(),
						Error:     "handshake failed: corrupt",
						// ErrorCode unspecified → Internal (retryable-looking
						// worker fault, not a client config mistake).
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	_, err := channelSvc.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

func TestOpenChannel_RejectsWorkerEchoAboveHubMax(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	hubMax := 1 << 20 // 1 MiB — below the protocol default so an oversize echo is visible
	env.channels = channelmgr.New(hubMax)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        open.GetChannelId(),
						HandshakePayload: []byte("worker-handshake"),
						// Valid by itself, but above what this hub announced.
						MaxMessageSize: uint64(hubMax * 2),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	_, err := channelSvc.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "above hub max")
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err),
		"peer echo above hub max is a Hub↔Worker protocol fault, not a client InvalidArgument")
}

func TestOpenChannel_RejectsWorkerEchoInvalidMaxMessageSize(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		echo uint64
	}{
		{name: "zero", echo: 0},
		{name: "below floor", echo: 1},
		{name: "above ceiling", echo: uint64(contracts.MaxConfigurableMessageSize + 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupDirectOpenChannelEnv(t)
			env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
			conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
				env.sent <- msg
				if open := msg.GetChannelOpen(); open != nil {
					env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
						Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
							ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
								ChannelId:        open.GetChannelId(),
								HandshakePayload: []byte("worker-handshake"),
								MaxMessageSize:   tc.echo,
							},
						},
					})
				}
				return nil

			})
			_, _ = env.worker.Register(conn)

			channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
			ctx := auth.WithUser(context.Background(), &auth.UserInfo{
				ID:       userid.MustNew(env.user.ID),
				Username: env.user.Username,
			})
			_, err := channelSvc.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{
				WorkerId:         env.workerID,
				HandshakePayload: []byte("handshake"),
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "max_message_size")
			assert.Equal(t, connect.CodeInternal, connect.CodeOf(err),
				"bad/missing worker echo is a peer/protocol fault, not a client InvalidArgument")
			if tc.echo == 0 {
				assert.Contains(t, err.Error(), "version skew")
			}
		})
	}
}

func TestOpenChannel_AdoptsWorkerLoweredMaxMessageSize(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	hubMax := 200_000
	lowered := 100_000
	env.channels = channelmgr.New(hubMax)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			assert.Equal(t, uint64(hubMax), open.GetMaxMessageSize(),
				"hub must announce its configured payload budget")
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        open.GetChannelId(),
						HandshakePayload: []byte("worker-handshake"),
						MaxMessageSize:   uint64(lowered),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	resp, err := channelSvc.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(lowered), resp.GetMaxMessageSize())
	require.True(t, env.channels.Exists(resp.GetChannelId()))

	// Prove the per-channel tracker ceiling is MaxReassembledMessageSize(lowered):
	// past the bare payload must still fit (headroom), past the derived ceiling must not
	// (and must not silently keep the larger hub default).
	channelID := resp.GetChannelId()
	ceiling := channelwire.MaxReassembledMessageSize(lowered)
	chunkCiphertext := func(plaintext int) int {
		return plaintext + contracts.NoiseAEADAuthTagSize
	}
	const dir = "fe2w"
	const piece = 40_000

	trackAccum := func(corr uint64, target int, finalMore bool) error {
		total := 0
		for total+piece < target {
			if err := env.channels.ChunkTracker.Track(channelID, dir, corr, chunkCiphertext(piece), true); err != nil {
				return err
			}
			total += piece
		}
		remain := target - total
		return env.channels.ChunkTracker.Track(channelID, dir, corr, chunkCiphertext(remain), finalMore)
	}

	require.NoError(t, trackAccum(1, lowered+1, false),
		"reassembled ceiling must include envelope headroom past the bare payload")
	err = trackAccum(2, ceiling+1, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max size")
}

func TestOpenChannel_ReturnsNegotiatedMaxMessageSize(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        open.GetChannelId(),
						HandshakePayload: []byte("worker-handshake"),
						MaxMessageSize:   open.GetMaxMessageSize(),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	resp, err := channelSvc.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(contracts.MaxMessageSize), resp.GetMaxMessageSize(),
		"OpenChannelResponse must return the effective payload budget")
	assert.True(t, env.channels.Exists(resp.GetChannelId()))
}

// TestOpenChannel_PropagatesAuthenticatedUserId pins the user-owned model:
// OpenChannel must announce the authenticated principal both to the worker
// (ChannelOpenRequest.user_id) and back to the client (OpenChannelResponse.user_id).
func TestOpenChannel_PropagatesAuthenticatedUserId(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	var announcedToWorker string
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			announcedToWorker = open.GetUserId()
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        open.GetChannelId(),
						HandshakePayload: []byte("worker-handshake"),
						MaxMessageSize:   open.GetMaxMessageSize(),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	resp, err := channelSvc.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	})
	require.NoError(t, err)
	assert.Equal(t, env.user.ID, announcedToWorker,
		"ChannelOpenRequest must carry the authenticated user id to the worker")
	assert.Equal(t, env.user.ID, resp.GetUserId(),
		"OpenChannelResponse must echo the authenticated user id to the client")
}

func TestOpenChannel_ClosesWhenCredentialExpires(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 2)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        open.GetChannelId(),
						HandshakePayload: []byte("worker-handshake"),
						MaxMessageSize:   open.GetMaxMessageSize(),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	// The deadline has to clear the OpenChannel round trip below (a worker
	// registration, a pending ChannelOpen request/response, and a store read)
	// and still land well inside the one-second Eventually that follows. 50ms
	// cleared neither reliably: OpenChannel itself returned "authentication
	// expired" once this package ran under -race, where every one of those steps
	// is several times slower, and the test then failed at its FIRST require --
	// reporting a broken expiry path for what was really a stopwatch. 300ms
	// keeps roughly a 3x margin on both sides.
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:                  userid.MustNew(env.user.ID),
		Username:            env.user.Username,
		CredentialExpiresAt: auth.DeadlineAt(time.Now().Add(300 * time.Millisecond)),
	})
	resp, err := channelSvc.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	})
	require.NoError(t, err)
	require.True(t, env.channels.Exists(resp.GetChannelId()))

	require.Eventually(t, func() bool {
		return !env.channels.Exists(resp.GetChannelId())
	}, time.Second, 10*time.Millisecond, "channel must close when its authenticating credential expires")

	var closeSeen bool
	for !closeSeen {
		select {
		case msg := <-env.sent:
			if closeMsg := msg.GetChannelClose(); closeMsg != nil {
				assert.Equal(t, resp.GetChannelId(), closeMsg.GetChannelId())
				closeSeen = true
			}
		case <-time.After(time.Second):
			require.Fail(t, "credential expiry must notify the worker")
		}
	}
}

func TestCloseChannelsByBearer_DoesNotBlockOnWorkerSend(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	blocked := make(chan struct{})
	started := make(chan struct{})
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		close(started)
		<-blocked
		return nil

	})
	_, _ = env.worker.Register(conn)
	t.Cleanup(func() { close(blocked) })

	env.channels.RegisterWithAuthInfo("blocked-close", env.workerID, env.user.ID, channelmgr.AuthInfo{
		Credential: auth.APICredential("token-1"),
	}, nil)
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})

	returned := make(chan int, 1)
	go func() {
		returned <- channelSvc.CloseChannelsByBearer(auth.NewBearerRef(auth.BearerKindAPI, "token-1"))
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		require.Fail(t, "worker close send did not start")
	}
	select {
	case count := <-returned:
		assert.Equal(t, 1, count)
	case <-time.After(100 * time.Millisecond):
		require.Fail(t, "local channel teardown must not wait for a blocked worker stream")
	}
}

func TestCloseChannelsByUserRevocation_BlockedWorkerDoesNotStarveHealthyWorker(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	blocked := make(chan struct{})
	blockedStarted := make(chan struct{}, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		select {
		case blockedStarted <- struct{}{}:
		default:
		}
		<-blocked
		return nil

	})
	_, _ = env.worker.Register(conn)
	t.Cleanup(func() { close(blocked) })

	healthyWorkerID := id.Generate()
	healthySent := make(chan *leapmuxv1.ConnectResponse, 1)
	healthyConn := workermgrtest.NewConnWithWrite(t, healthyWorkerID, func(msg *leapmuxv1.ConnectResponse) error {
		healthySent <- msg
		return nil

	})
	_, _ = env.worker.Register(healthyConn)

	for i := 0; i < 8; i++ {
		env.channels.RegisterWithAuthInfo(id.Generate(), env.workerID, env.user.ID, channelmgr.AuthInfo{}, nil)
	}
	healthyChannelID := id.Generate()
	env.channels.RegisterWithAuthInfo(healthyChannelID, healthyWorkerID, env.user.ID, channelmgr.AuthInfo{}, nil)
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})

	assert.Equal(t, 9, channelSvc.CloseChannelsByUserRevocation(env.user.ID, 1))
	assert.Equal(t, 0, channelSvc.CloseChannelsByUserRevocation(env.user.ID, 1), "local teardown must finish before worker delivery")

	select {
	case <-blockedStarted:
	case <-time.After(time.Second):
		require.Fail(t, "blocked worker send did not start")
	}
	select {
	case msg := <-healthySent:
		require.NotNil(t, msg.GetChannelClose())
		assert.Equal(t, healthyChannelID, msg.GetChannelClose().GetChannelId())
	case <-time.After(time.Second):
		require.Fail(t, "blocked worker must not starve close delivery to a healthy worker")
	}
}

func TestCloseChannelsByUserRevocation_DoesNotBlockOnParkedWorkerWrites(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	blocked := make(chan struct{})
	var started atomic.Int32

	const workerCount = 20
	for i := 0; i < workerCount; i++ {
		workerID := id.Generate()
		conn := workermgrtest.NewConnWithWrite(t, workerID, func(*leapmuxv1.ConnectResponse) error {
			started.Add(1)
			<-blocked
			return nil
		})
		_, _ = env.worker.Register(conn)
		env.channels.RegisterWithAuthInfo(id.Generate(), workerID, env.user.ID, channelmgr.AuthInfo{}, nil)
	}
	t.Cleanup(func() { close(blocked) })

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	returned := make(chan int, 1)
	go func() {
		returned <- channelSvc.CloseChannelsByUserRevocation(env.user.ID, 1)
	}()
	select {
	case count := <-returned:
		assert.Equal(t, workerCount, count, "local teardown must finish without waiting for drained writes")
	case <-time.After(time.Second):
		require.Fail(t, "revocation teardown blocked behind parked worker writes")
	}
	require.Eventually(t, func() bool { return started.Load() > 0 }, time.Second, time.Millisecond,
		"at least one close frame must still reach a worker drain")
}

func TestCloseChannel_NotFound(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.CloseChannel(authedReq(ctx,
		&leapmuxv1.CloseChannelRequest{ChannelId: "nonexistent"}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCloseChannel_EmptyChannelID(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.CloseChannel(authedReq(ctx,
		&leapmuxv1.CloseChannelRequest{ChannelId: ""}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCloseChannel_Success(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Get admin user info.
	adminUser, err := env.store.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)

	workerID := env.createWorker(t, token)

	// Register a channel in the channel manager.
	channelID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(channelID, workerID, adminUser.ID, channelmgr.AuthInfo{
		Credential: auth.SessionCredential(token),
	}, nil)

	// Close the channel.
	_, err = env.channelClient.CloseChannel(authedReq(ctx,
		&leapmuxv1.CloseChannelRequest{ChannelId: channelID}, token))
	require.NoError(t, err)

	// Verify channel is removed.
	assert.False(t, env.channelMgr.Exists(channelID))
}

func TestCloseChannel_WrongUser(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	adminToken := env.adminToken(t)

	workerID := env.createWorker(t, adminToken)

	// Get admin user info.
	adminUser, err := env.store.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)

	// Register a channel owned by admin in channel manager.
	channelID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(channelID, workerID, adminUser.ID, channelmgr.AuthInfo{}, nil)

	// Create a second user.
	_, user2Token := env.createSecondUser(t)

	// user2 should not be able to close admin's channel.
	_, err = env.channelClient.CloseChannel(authedReq(ctx,
		&leapmuxv1.CloseChannelRequest{ChannelId: channelID}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_Unauthenticated(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()

	_, err := env.channelClient.GetWorkerHandshakeParams(ctx, &leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: "any"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_Classic(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorker(t, token)
	hello := classicHello(0x6b)
	registerOnlineWorker(t, env, workerID, hello)

	env.requireHandshake(t, ctx, token, workerID, hello)
}

// --- Handshake scenario tests with different encryption modes ---

func TestOpenChannel_PostQuantumHandshake(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorker(t, token)

	sentCh := make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithHello(t, workerID, postQuantumHello(0x7c), func(msg *leapmuxv1.ConnectResponse) error {
		sentCh <- msg
		return nil
	})

	_, _ = env.workerMgr.Register(conn)

	shortCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err := env.channelClient.OpenChannel(authedReq(shortCtx,
		&leapmuxv1.OpenChannelRequest{
			WorkerId:         workerID,
			HandshakePayload: []byte("pq-handshake-msg1"),
		}, token))
	// Times out because mock worker doesn't respond.
	require.Error(t, err)

	// A WAIT, not a non-blocking read: the send to the worker happens on the
	// handler goroutine, and the RPC above returns on its own 100ms deadline
	// without waiting for it. A read that gives up at once therefore races the
	// send and fails under load, although the claim here -- that the hub handed
	// the ChannelOpen to the worker -- says nothing about which happens first.
	// The limit is a deadlock guard, matching the sibling case above.
	select {
	case sentMsg := <-sentCh:
		assert.NotNil(t, sentMsg.GetChannelOpen())
		assert.Equal(t, []byte("pq-handshake-msg1"), sentMsg.GetChannelOpen().GetHandshakePayload())
	case <-time.After(time.Second):
		require.Fail(t, "expected a message to be sent to worker")
	}
}

func TestOpenChannel_ClassicHandshake(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorker(t, token)

	sentCh := make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithHello(t, workerID, classicHello(0x7d), func(msg *leapmuxv1.ConnectResponse) error {
		sentCh <- msg
		return nil
	})

	_, _ = env.workerMgr.Register(conn)

	shortCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err := env.channelClient.OpenChannel(authedReq(shortCtx,
		&leapmuxv1.OpenChannelRequest{
			WorkerId:         workerID,
			HandshakePayload: []byte("classic-handshake-msg1"),
		}, token))
	require.Error(t, err)

	// A WAIT rather than a non-blocking read, for the reason given in
	// TestOpenChannel_PostQuantumHandshake.
	select {
	case sentMsg := <-sentCh:
		assert.NotNil(t, sentMsg.GetChannelOpen())
		assert.Equal(t, []byte("classic-handshake-msg1"), sentMsg.GetChannelOpen().GetHandshakePayload())
	case <-time.After(time.Second):
		require.Fail(t, "expected a message to be sent to worker")
	}
}

func (e *channelTestEnv) createSecondUser(t *testing.T) (userID, token string) {
	t.Helper()
	ctx := context.Background()

	userID = id.Generate()
	hash, _ := password.Hash("testpass2")
	_ = e.store.Users().Create(ctx, store.CreateUserParams{
		ID:                    userID,
		Username:              "user2",
		PasswordHash:          hash,
		DisplayName:           "User 2",
		FirstCredentialExempt: true,
		IsAdmin:               false,
	})
	token, _, _, loginErr := auth.Login(ctx, e.store, "user2", "testpass2", auth.DefaultSessionDuration)
	require.NoError(t, loginErr)
	return
}
