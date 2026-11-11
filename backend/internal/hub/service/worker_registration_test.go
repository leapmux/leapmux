package service_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	workerhub "github.com/leapmux/leapmux/internal/worker/hub"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/coder/quartz"
	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

type regKeyEnv struct {
	mgmtClient      leapmuxv1connect.WorkerManagementServiceClient
	connectorClient leapmuxv1connect.WorkerConnectorServiceClient
	authClient      leapmuxv1connect.AuthServiceClient
	store           store.Store
	mailer          *mailSenderDouble
	server          *httptest.Server
	mux             *http.ServeMux
	wMgr            *workermgr.Manager
	notifier        *notifier.Notifier
	channelMgr      *channelmgr.Manager
	// registry holds one in-memory CRDT manager per user, as hub/server.go
	// wires it, so a test can subscribe to a user's /ws/userevents frames.
	registry *crdt.Registry
	// connector is the WorkerConnectorService behind connectorClient, for a
	// test that drives one of its Hub-internal entry points.
	connector *service.WorkerConnectorService
	probes    chan time.Time
}

// regKeySeed configures the settings a registration-key env runs under;
// nil leaves every setting at its default.
type regKeySeed func(t *testing.T, set *settings.Manager)

// maxWorkersPerUser seeds the per-user worker cap the way the old config
// field did.
func maxWorkersPerUser(n int64) regKeySeed {
	return func(t *testing.T, set *settings.Manager) {
		t.Helper()
		require.NoError(t, settings.KeyLimits.Set(context.Background(), set, settings.LimitsValue{
			MaxConnectionsPerUser: 32,
			MaxWorkersPerUser:     n,
		}))
	}
}

func setupRegKeyEnv(t *testing.T) *regKeyEnv {
	t.Helper()
	return setupRegKeyEnvWithCfg(t, testConfig(), seedSMTP)
}

// setupRegKeyEnvWithCfg builds a registration-key test env with a
// caller-supplied config and settings seed. Use this when the test cares
// about email_enabled gating; setupRegKeyEnv (the default) configures
// SMTP so EmailRegistrationInstructions can run.
func setupRegKeyEnvWithCfg(t *testing.T, cfg *config.Config, seed regKeySeed) *regKeyEnv {
	t.Helper()
	return newRegKeyEnv(t, cfg, seed, regKeyEnvOptions{})
}

// newRegKeyEnv builds a registration-key test env. Its connectorStore option
// wraps the store that the worker connector and stream handlers read, so a
// test can act inside their store calls. Other services read the store
// unwrapped.
type regKeyEnvOptions struct {
	workerProbeTimeout time.Duration
	connectorStore     func(store.Store) store.Store
	deregisterEffects  func(*notifier.Notifier, *crdt.Registry) *service.WorkerDeregisterEffects
}

func newRegKeyEnv(t *testing.T, cfg *config.Config, seed regKeySeed, options regKeyEnvOptions) *regKeyEnv {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	return regKeyEnvFromStore(t, st, cfg, seed, options, nil)
}

// regKeyEnvFromStore exercises registration through the same services on a
// caller-supplied backend, including databases with concurrent writers.
// clock drives the connector's deadlines; nil gives the real clock.
func regKeyEnvFromStore(t *testing.T, st store.Store, cfg *config.Config, seed regKeySeed, options regKeyEnvOptions, clock quartz.Clock) *regKeyEnv {
	t.Helper()
	require.GreaterOrEqual(t, options.workerProbeTimeout, time.Duration(0), "worker probe timeout must not be negative")
	probeTimeout := 30 * time.Second
	if options.workerProbeTimeout != 0 {
		probeTimeout = options.workerProbeTimeout
	}
	if clock == nil {
		clock = quartz.NewReal()
	}
	hubtestutil.CreateTestAdmin(t, st)
	set := servicetest.NewSettingsManager(t, st, nil)
	if seed != nil {
		seed(t, set)
	}

	wMgr := workermgr.New(service.NewWorkerReachAuthorizer(st))
	cMgr := channelmgr.New(0)
	pendingReqs := workermgr.NewPendingRequests(func() time.Duration { return settings.DefaultTimeouts.APITimeout() })

	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(sc.Stop)
	connectServer := connect.NewServer(interceptor)

	mailer := &mailSenderDouble{}
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, cfg, set, auth.NewCredentialLifecycleEffects(sc, nil, nil)))
	leapmuxv1connect.RegisterAuthServiceHandler(connectServer, authSvc)

	registry := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(owner, newMemJournal(), allowAllAuth{}, nil, time.Now)
		if err := mgr.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })
	// One notifier for both services, as hub/server.go wires it: the
	// connector delivers DEREGISTER to a deregistering worker that connects,
	// and the management service sends it to a connected one. Every
	// workers-changed notification goes through the CRDT registry, as there.
	notif := notifier.New(st, wMgr, pendingReqs, func() time.Duration { return settings.DefaultTimeouts.APITimeout() }, registry.BroadcastWorkersChanged)
	connectorSt := st
	if options.connectorStore != nil {
		connectorSt = options.connectorStore(st)
	}
	effects := service.NewWorkerDeregisterEffects(auth.NewDelegationScopeCache(st), notif, registry.BroadcastWorkersChanged)
	if options.deregisterEffects != nil {
		effects = options.deregisterEffects(notif, registry)
	}
	connectorSvc := service.NewWorkerConnectorService(service.WorkerConnectorServiceDeps{
		Store:             connectorSt,
		WorkerManager:     wMgr,
		ChannelManager:    cMgr,
		PendingRequests:   pendingReqs,
		Notifier:          notif,
		Registry:          registry,
		QueuePool:         sendq.NewMaxBytesPoolForTest(),
		Clock:             clock,
		DeregisterEffects: effects,
	})
	// Both halves of max_workers_per_user, wired from one value exactly as
	// hub/server.go does: the service caps registered ROWS, the registry caps
	// LIVE CONNECTIONS, and a fixture that wired only the first would let a test
	// pass against the row cap alone -- which is the bug these two exist to
	// close between them.
	limits := settings.KeyLimits.Of(set.Snapshot(context.Background()))
	connectorSvc.SetMaxWorkersPerUser(limits.MaxWorkersPerUser)
	wMgr.SetMaxWorkersPerUser(limits.MaxWorkersPerUser)
	leapmuxv1connect.RegisterWorkerConnectorServiceHandler(connectServer, connectorSvc)
	probes := make(chan time.Time)
	mux.Handle(channelwire.WSRouteWorker, service.NewWorkerStreamHandler(connectorSvc).WithForcedKeepaliveProbesForTest(probes, probeTimeout))

	mgmtSvc := service.NewWorkerManagementService(st, wMgr, service.NewWorkerDeregisterEffects(nil, notif, registry.BroadcastWorkersChanged), mailer, mail.Renderer{}, set)
	leapmuxv1connect.RegisterWorkerManagementServiceHandler(connectServer, mgmtSvc)
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &regKeyEnv{
		mgmtClient:      leapmuxv1connect.NewWorkerManagementServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0)))),
		connectorClient: workerhub.NewConnectorClient(server.Client(), server.URL),
		authClient:      leapmuxv1connect.NewAuthServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0)))),
		store:           st,
		mailer:          mailer,
		server:          server,
		mux:             mux,
		wMgr:            wMgr,
		notifier:        notif,
		channelMgr:      cMgr,
		registry:        registry,
		connector:       connectorSvc,
		probes:          probes,
	}
}

func (e *regKeyEnv) login(t *testing.T, username, password string) string {
	t.Helper()
	ctx, info := connect.NewClientContext(context.Background())
	_, err := e.authClient.Login(ctx, &leapmuxv1.LoginRequest{
		Username: username, Password: password,
	})
	require.NoError(t, err)
	return sessionFromCookie(t, info.ResponseHeader().Get("Set-Cookie"))
}

func (e *regKeyEnv) adminID(t *testing.T) string {
	t.Helper()
	admin, err := e.store.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	return admin.ID
}

// workerTestPeer carries the context used by one test's stream operations.
type workerTestPeer struct {
	stream *workerhub.WorkerStream
	ctx    context.Context
}

func (p *workerTestPeer) Send(msg *leapmuxv1.ConnectRequest) error { return p.stream.Send(p.ctx, msg) }
func (p *workerTestPeer) Receive() (*leapmuxv1.ConnectResponse, error) {
	return p.stream.Receive(p.ctx)
}
func (p *workerTestPeer) Close() error { return p.stream.Close() }

func dialWorker(t *testing.T, ctx context.Context, server *httptest.Server, token string) *workerTestPeer {
	t.Helper()
	stream, err := workerhub.DialWorkerStream(ctx, server.Client(), server.URL, token)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.CloseNow() })
	return &workerTestPeer{stream: stream, ctx: ctx}
}

func dialWorkerOverUnixSocket(t *testing.T, ctx context.Context, mux *http.ServeMux, token string) *workerTestPeer {
	t.Helper()
	endpoint, err := hubtransport.New(hubtransporttest.NewSocketServer(t, "worker-identity", mux))
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	stream, err := workerhub.DialWorkerStream(ctx, endpoint.WebSocketClient(), endpoint.BaseURL(), token)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.CloseNow() })
	return &workerTestPeer{stream: stream, ctx: ctx}
}

func requireWorkerClose(t *testing.T, err error, status websocket.StatusCode, reason string) {
	t.Helper()
	var closed websocket.CloseError
	require.ErrorAs(t, err, &closed)
	assert.Equal(t, status, closed.Code)
	assert.Equal(t, reason, closed.Reason)
}

func requireUpgradeRefused(t *testing.T, err error, status int) {
	t.Helper()
	var refused *workerhub.UpgradeRefusedError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, status, refused.StatusCode)
}

func (e *regKeyEnv) registerWithKey(t *testing.T, key string) (*leapmuxv1.RegisterResponse, error) {
	t.Helper()
	return e.registerWithKeys(t, key, workerkeystest.Keys(0x31))
}

// registerWithKeys registers with an explicit key set, for the tests that must
// present a malformed one or a second set. Each call mints a fresh attempt
// identity, the way the worker's register command does per invocation.
func (e *regKeyEnv) registerWithKeys(t *testing.T, key string, keys *leapmuxv1.WorkerPublicKeys) (*leapmuxv1.RegisterResponse, error) {
	t.Helper()
	ctx, info := connect.NewClientContext(context.Background())
	if key != "" {
		info.RequestHeader().Set("Authorization", "Bearer "+key)
	}
	return e.connectorClient.Register(ctx, &leapmuxv1.RegisterRequest{Keys: keys, AttemptId: testAttemptID(t)})
}

// testAttemptID mints one valid durable attempt identity.
func testAttemptID(t *testing.T) string {
	t.Helper()
	var buf [16]byte
	_, err := rand.Read(buf[:])
	require.NoError(t, err)
	return hex.EncodeToString(buf[:])
}

func TestCreateRegistrationKey_RequiresAuth(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)

	_, err := env.mgmtClient.CreateRegistrationKey(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestCreateRegistrationKey_ReturnsKeyAndExpiry(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	resp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetRegistrationKey())
	require.NotNil(t, resp.GetExpiresAt())

	expiresAt := resp.GetExpiresAt().AsTime()
	delta := time.Until(expiresAt)
	// Should be ~5 min from now; allow generous slack for clock + RPC.
	assert.Greater(t, delta, 4*time.Minute)
	assert.Less(t, delta, 6*time.Minute)
}

func TestRegister_RejectsMissingBearer(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)

	_, err := env.registerWithKey(t, "")
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestRegister_HappyPath_ReturnsCredentialsAndConsumesKey(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.GetRegistrationKey()

	regResp, err := env.registerWithKey(t, key)
	require.NoError(t, err)
	assert.NotEmpty(t, regResp.GetWorkerId())
	assert.NotEmpty(t, regResp.GetAuthToken())

	// A second Register with the same key must fail — the key was
	// soft-deleted as part of the consume txn.
	_, err = env.registerWithKey(t, key)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	// Worker row exists and has registered_by = key.created_by. The response does
	// not carry the owner -- the Hub delivers it on connect (WorkerIdentity) instead
	// of at registration, so the DB row is the only place to assert it.
	w, err := env.store.Workers().GetByID(context.Background(), regResp.GetWorkerId())
	require.NoError(t, err)
	assert.NotEmpty(t, w.RegisteredBy, "the worker row must record the key creator as its owner")
}

// The worker pool's count term, end to end.
//
// A Worker's stream is a member of the worker pool, guaranteed the same
// working set the pool may not reclaim -- but it takes no lease, so the per-user
// CONNECTION cap cannot see it, and registration keys have no quota. Without
// this bound, N keys mint N members and the floors the worker pool promises sum
// without limit, on the pool whose eviction costs the most.
func TestRegister_RefusesBeyondThePerUserWorkerCap(t *testing.T) {
	// Exact stage assertions read process-global counters. Keep this test
	// sequential so other admission tests cannot increment the connect stage.

	env := setupRegKeyEnvWithCfg(t, testConfig(), maxWorkersPerUser(2))
	token := env.login(t, "admin", "admin123")

	register := func(t *testing.T) error {
		t.Helper()
		createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
			&leapmuxv1.CreateRegistrationKeyRequest{}, token))
		require.NoError(t, err)
		_, err = env.registerWithKey(t, createResp.GetRegistrationKey())
		return err
	}

	registrationsBefore := workerAdmissionsRefused(metrics.WorkerStageRegister)
	require.NoError(t, register(t), "the first worker must register")
	require.NoError(t, register(t), "and so must the second, up to the cap")
	assert.Equal(t, registrationsBefore, workerAdmissionsRefused(metrics.WorkerStageRegister),
		"successful registrations must not count as refusals")

	// One cap, two stages, one counter. The `stage` label is what an operator
	// reads to tell "deregister something" from "an existing stream is still
	// holding the slot"; the counter itself is the number that answers "is this
	// cap biting at all", which is why the two stages are not two series.
	before := workerAdmissionsRefused(metrics.WorkerStageRegister)
	connectBefore := workerAdmissionsRefused(metrics.WorkerStageConnect)

	err := register(t)
	require.Error(t, err, "the third must be refused")
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err),
		"a cap is exhausted resources, not a bad request: the caller did nothing wrong")
	assert.Contains(t, err.Error(), "max_workers_per_user",
		"the operator-facing key has to be in the message, or nobody knows what to raise")

	assert.Equal(t, before+1, workerAdmissionsRefused(metrics.WorkerStageRegister),
		"the refusal must increment the registration stage exactly once")
	assert.Equal(t, connectBefore, workerAdmissionsRefused(metrics.WorkerStageConnect),
		"...under the stage that actually refused it, not the other one")
}

// workerAdmissionsRefused reads one stage of leapmux_worker_admissions_refused_total.
func workerAdmissionsRefused(stage string) float64 {
	return promtestutil.ToFloat64(metrics.WorkerAdmissionsRefusedTotal.WithLabelValues(stage))
}

// Deregistering frees a slot, or the cap is a lifetime quota rather than a bound
// on what is registered at once.
func TestRegister_WorkerCapCountsOnlyLiveWorkers(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnvWithCfg(t, testConfig(), maxWorkersPerUser(1))
	token := env.login(t, "admin", "admin123")

	register := func(t *testing.T) (string, error) {
		t.Helper()
		createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
			&leapmuxv1.CreateRegistrationKeyRequest{}, token))
		require.NoError(t, err)
		resp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
		if err != nil {
			return "", err
		}
		return resp.GetWorkerId(), nil
	}

	first, err := register(t)
	require.NoError(t, err)
	_, err = register(t)
	require.Error(t, err, "at a cap of one, the second must be refused")

	require.NoError(t, env.store.Workers().MarkDeleted(context.Background(), first))

	_, err = register(t)
	assert.NoError(t, err, "a deregistered worker must give its slot back")
}

func TestRegister_AtomicConsume_RaceProducesOneWinner(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.GetRegistrationKey()

	// Two goroutines race to consume the same key. Exactly one must win.
	const racers = 4
	var wg sync.WaitGroup
	wg.Add(racers)
	results := make(chan error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, err := env.registerWithKey(t, key)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	failures := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			failures++
			assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		}
	}
	assert.Equal(t, 1, successes, "exactly one racer must consume the key")
	assert.Equal(t, racers-1, failures)
}

func TestExtendRegistrationKey_RejectsExpired(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.GetRegistrationKey()

	// Force-expire by soft-deleting the row directly via the store.
	_, err = env.store.RegistrationKeys().SoftDelete(context.Background(), store.SoftDeleteRegistrationKeyParams{
		ID:        key,
		CreatedBy: userid.MustNew(env.adminID(t)),
	})
	require.NoError(t, err)

	_, err = env.mgmtClient.ExtendRegistrationKey(authedReq(context.Background(), &leapmuxv1.ExtendRegistrationKeyRequest{
		RegistrationKey: key,
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"a dead key must never be revivable; clients must mint a new one")
}

func TestExtendRegistrationKey_RejectsTooEarly(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.GetRegistrationKey()

	// Just-minted key has ~5min remaining, well above the 2-min buffer.
	_, err = env.mgmtClient.ExtendRegistrationKey(authedReq(context.Background(), &leapmuxv1.ExtendRegistrationKeyRequest{
		RegistrationKey: key,
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "extension not allowed yet")
}

func TestExtendRegistrationKey_AcceptsInsideBuffer(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.GetRegistrationKey()

	// Push the key inside the extend window: 90s remaining < 2min buffer.
	rows, err := env.store.RegistrationKeys().Extend(context.Background(), store.ExtendRegistrationKeyParams{
		ID:        key,
		CreatedBy: userid.MustNew(env.adminID(t)),
		ExpiresAt: time.Now().Add(90 * time.Second),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	resp, err := env.mgmtClient.ExtendRegistrationKey(authedReq(context.Background(), &leapmuxv1.ExtendRegistrationKeyRequest{
		RegistrationKey: key,
	}, token))
	require.NoError(t, err)
	require.NotNil(t, resp.GetExpiresAt())
	delta := time.Until(resp.GetExpiresAt().AsTime())
	assert.Greater(t, delta, 4*time.Minute, "extension should restore ~5min TTL")
}

func TestExtendRegistrationKey_RejectsOtherUsersKey(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	adminToken := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, adminToken))
	require.NoError(t, err)
	key := createResp.GetRegistrationKey()

	// Bring up a second user. We seed directly via the store helper —
	// SignUp is disabled in this test config, and that's fine; the
	// registration-key flow has its own auth concerns we want to isolate.
	hubtestutil.CreateTestUser(t, env.store, "other", "secret-password")
	otherToken := env.login(t, "other", "secret-password")

	// Ownership is enforced inside the SQL WHERE clause via
	// RegistrationKeys().GetOwned: cross-user access is indistinguishable
	// from "no such key", which deliberately closes the oracle on whether
	// a key id corresponds to *some* other user's row.
	_, err = env.mgmtClient.ExtendRegistrationKey(authedReq(context.Background(), &leapmuxv1.ExtendRegistrationKeyRequest{
		RegistrationKey: key,
	}, otherToken))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestExtendRegistrationKey_DoesNotResurrectConsumedKey is the
// regression for the TOCTOU window in Extend. The service-level flow is
// SELECT (for the buffer-check error message) followed by UPDATE; if a
// concurrent Consume burns the row between them, the UPDATE must refuse
// to revive the dead row. The atomic guard lives in the SQL WHERE
// clause (`expires_at > now`).
func TestExtendRegistrationKey_DoesNotResurrectConsumedKey(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.GetRegistrationKey()

	// Push the key inside the extend window so the service-level
	// buffer check passes.
	rows, err := env.store.RegistrationKeys().Extend(context.Background(), store.ExtendRegistrationKeyParams{
		ID:        key,
		CreatedBy: userid.MustNew(env.adminID(t)),
		ExpiresAt: time.Now().Add(90 * time.Second),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	// Simulate a concurrent Consume that lands between the service's
	// SELECT and UPDATE. Calling Consume directly is functionally
	// equivalent to a worker presenting the key.
	consumed, err := env.store.RegistrationKeys().Consume(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, key, consumed.ID)

	// Extend now races a dead row. The handler's SELECT would have
	// observed the live state pre-Consume; the UPDATE must refuse to
	// revive it.
	_, err = env.mgmtClient.ExtendRegistrationKey(authedReq(context.Background(), &leapmuxv1.ExtendRegistrationKeyRequest{
		RegistrationKey: key,
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"a Consumed key must never be resurrectable via Extend")

	// And a fresh Consume must still fail — the row is dead.
	_, err = env.store.RegistrationKeys().Consume(context.Background(), key)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestRevokeRegistrationKey_SoftDeletes(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.GetRegistrationKey()

	_, err = env.mgmtClient.RevokeRegistrationKey(authedReq(context.Background(), &leapmuxv1.RevokeRegistrationKeyRequest{
		RegistrationKey: key,
	}, token))
	require.NoError(t, err)

	// Subsequent register must be rejected.
	_, err = env.registerWithKey(t, key)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestRevokeRegistrationKey_RefusesAMissingOrUnknownKey pins the two
// refusals: an empty key is the caller's malformed request, and a key the
// caller does not own reads exactly as a key that does not exist.
func TestRevokeRegistrationKey_RefusesAMissingOrUnknownKey(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	_, err := env.mgmtClient.RevokeRegistrationKey(authedReq(context.Background(), &leapmuxv1.RevokeRegistrationKeyRequest{}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = env.mgmtClient.RevokeRegistrationKey(authedReq(context.Background(), &leapmuxv1.RevokeRegistrationKeyRequest{
		RegistrationKey: "no-such-key",
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// A revoked key has nothing left to revoke, so a second revoke answers
// NotFound, as Extend does for a dead key. Rewriting the expiry again would move
// it forward and delay the purge.
func TestRevokeRegistrationKey_RefusesARevokedKey(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	revoke := &leapmuxv1.RevokeRegistrationKeyRequest{RegistrationKey: createResp.GetRegistrationKey()}

	_, err = env.mgmtClient.RevokeRegistrationKey(authedReq(context.Background(), revoke, token))
	require.NoError(t, err)

	_, err = env.mgmtClient.RevokeRegistrationKey(authedReq(context.Background(), revoke, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestEmailRegistrationInstructions_RequiresVerifiedEmail(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)

	// admin user is created without a verified email by CreateTestAdmin,
	// so the precondition check should reject.
	_, err = env.mgmtClient.EmailRegistrationInstructions(authedReq(context.Background(), &leapmuxv1.EmailRegistrationInstructionsRequest{
		RegistrationKey: createResp.GetRegistrationKey(),
		Command:         "leapmux worker --hub http://x --registration-key abc",
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Nil(t, env.mailer.last(), "no mail should be sent without a verified email")
}

// TestEmailRegistrationInstructions_RejectsWhenSMTPUnconfigured locks
// in the defense-in-depth precondition: even if a client bypasses the
// frontend gate (which hides the button when email_enabled=false), the
// RPC refuses to call the disabled mail backend.
func TestEmailRegistrationInstructions_RejectsWhenSMTPUnconfigured(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnvWithCfg(t, testConfig(), nil) // no SMTP host set
	token := env.login(t, "admin", "admin123")

	// Mark the admin's email verified — otherwise the
	// verified-email check fires first and we couldn't tell which
	// precondition rejected us.
	require.NoError(t, env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email:         "admin@example.com",
		EmailVerified: true,
		ID:            env.adminID(t),
	}))

	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)

	_, err = env.mgmtClient.EmailRegistrationInstructions(authedReq(context.Background(), &leapmuxv1.EmailRegistrationInstructionsRequest{
		RegistrationKey: createResp.GetRegistrationKey(),
		Command:         "leapmux worker --hub http://x --registration-key abc",
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "not configured to send email")
	assert.Nil(t, env.mailer.last(), "no mail should be sent when SMTP is unconfigured")
}

func TestEmailRegistrationInstructions_SendsToVerifiedAddress(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	// Mark the admin's email verified.
	require.NoError(t, env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email:         "admin@example.com",
		EmailVerified: true,
		ID:            env.adminID(t),
	}))

	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)

	cmd := "leapmux worker --hub http://x --registration-key abc"
	_, err = env.mgmtClient.EmailRegistrationInstructions(authedReq(context.Background(), &leapmuxv1.EmailRegistrationInstructionsRequest{
		RegistrationKey: createResp.GetRegistrationKey(),
		Command:         cmd,
	}, token))
	require.NoError(t, err)

	last := env.mailer.last()
	require.NotNil(t, last)
	assert.Equal(t, "admin@example.com", last.To)
	assert.True(t, strings.Contains(last.Body, cmd), "email body should contain the rendered command")
}

func TestDeregisterWorker_AllowsManuallyRegisteredWorker(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	// Mint a key, then consume it via Register — the resulting worker
	// row has auto_registered=false.
	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
	require.NoError(t, err)
	workerID := regResp.GetWorkerId()

	_, err = env.mgmtClient.DeregisterWorker(authedReq(context.Background(), &leapmuxv1.DeregisterWorkerRequest{
		WorkerId: workerID,
	}, token))
	require.NoError(t, err)

	worker, err := env.store.Workers().GetByIDIncludeDeleted(context.Background(), workerID)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, worker.Status)
}

// createBundledWorker adds an auto-registered worker row for the admin, as
// Server.RegisterWorker does, and returns its id.
func createBundledWorker(t *testing.T, env *regKeyEnv) string {
	t.Helper()
	workerID := id.Generate()
	require.NoError(t, env.store.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:             workerID,
		AuthToken:      id.Generate(),
		RegisteredBy:   userid.MustNew(env.adminID(t)),
		AutoRegistered: true,
		PublicKeys:     storetest.WorkerKeys(workerkeystest.SeededWorkerFill),
	}))
	return workerID
}

// The owner may remove an offline bundled worker: that is the only cleanup in
// the browser for the row that a launch leaves behind when the app's state file
// was lost.
func TestDeregisterWorker_AllowsAnOfflineBundledWorker(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")
	workerID := createBundledWorker(t, env)

	_, err := env.mgmtClient.DeregisterWorker(authedReq(context.Background(), &leapmuxv1.DeregisterWorkerRequest{
		WorkerId: workerID,
	}, token))
	require.NoError(t, err)

	worker, err := env.store.Workers().GetByIDIncludeDeleted(context.Background(), workerID)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, worker.Status)
}

// The owner cannot stop a connected bundled worker: the app would run with no
// local worker until its next launch. The refusal is the admin path's, because
// one rule governs both paths.
func TestDeregisterWorker_RefusesAConnectedBundledWorker(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")
	workerID := createBundledWorker(t, env)
	conn := workermgrtest.NewConnWithWrite(t, workerID, func(*leapmuxv1.ConnectResponse) error { return nil })
	_, err := env.wMgr.Register(conn)
	require.NoError(t, err)

	_, err = env.mgmtClient.DeregisterWorker(authedReq(context.Background(), &leapmuxv1.DeregisterWorkerRequest{
		WorkerId: workerID,
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "the bundled local worker is connected; deregister it only while it is offline")

	// Row still active: the precondition fired before any UPDATE.
	worker, err := env.store.Workers().GetByID(context.Background(), workerID)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, worker.Status)
}

// TestRegister_OverUnixSocket_StillRequiresValidKey is the regression
// guard for the desktop / solo-mode unix-socket path. Worker
// registration must require a valid registration key on every transport
// — the only sanctioned bypass is the in-process Server.RegisterWorker
// call used to bootstrap the co-located solo worker, which never
// touches the RPC layer. An external worker that connects over the
// hub's unix socket gets the same auth treatment as one over TCP:
// missing or invalid bearer → Unauthenticated.
func TestRegister_OverUnixSocket_StillRequiresValidKey(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on Windows; named-pipe coverage lives in integration tests")
	}

	env := setupRegKeyEnv(t)

	// The h2c client that `leapmux worker --hub unix:...` uses dials the
	// hub the same way it would in production.
	endpoint, err := hubtransport.New(hubtransporttest.NewSocketServer(t, "registration", env.mux))
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	httpClient := endpoint.UnaryClient(hubtransport.DefaultUnaryTimeout)
	connectorClient := workerhub.NewConnectorClient(httpClient, "http://localhost")
	authClient := leapmuxv1connect.NewAuthServiceClient(connect.NewClient(connecthttp.NewTransport(httpClient, "http://localhost", connecthttp.WithReadMaxBytes(0))))
	mgmtClient := leapmuxv1connect.NewWorkerManagementServiceClient(connect.NewClient(connecthttp.NewTransport(httpClient, "http://localhost", connecthttp.WithReadMaxBytes(0))))

	register := func(t *testing.T, key string) error {
		t.Helper()
		ctx, info := connect.NewClientContext(context.Background())
		if key != "" {
			info.RequestHeader().Set("Authorization", "Bearer "+key)
		}
		_, err := connectorClient.Register(ctx, &leapmuxv1.RegisterRequest{
			Keys:      workerkeystest.Keys(0x31),
			AttemptId: testAttemptID(t),
		})
		return err
	}

	t.Run("missing bearer is rejected", func(t *testing.T) {
		err := register(t, "")
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("invalid bearer is rejected", func(t *testing.T) {
		err := register(t, "not-a-real-key")
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("valid bearer registers a worker", func(t *testing.T) {
		loginCtx, loginInfo := connect.NewClientContext(context.Background())
		_, err := authClient.Login(loginCtx, &leapmuxv1.LoginRequest{
			Username: "admin", Password: "admin123",
		})
		require.NoError(t, err)
		token := sessionFromCookie(t, loginInfo.ResponseHeader().Get("Set-Cookie"))

		createResp, err := mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
		require.NoError(t, err)
		key := createResp.GetRegistrationKey()

		err = register(t, key)
		require.NoError(t, err, "valid key over unix socket must register the worker")

		// Replay rejected: registration keys are single-use regardless
		// of transport.
		err = register(t, key)
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})
}

// The Hub must send WorkerIdentity as the FIRST message on every worker stream.
//
// It is the worker's only source for its own owner: the worker's owner gate
// guards every machine-scoped family (file, git, sysinfo, tunnel) on it, and the
// worker keeps no local copy -- a cached copy is what previously went missing and
// left the worker permanently denying its own legitimate user. Identity must also
// PRECEDE the connection being published to the worker manager, so it cannot be
// overtaken by a frontend-driven ChannelOpen on the same stream; asserting it is
// the first frame the worker receives is how that ordering stays true.
func TestConnect_SendsWorkerIdentityFirst(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on Windows")
	}
	env := setupRegKeyEnv(t)

	// Register a worker so it has an auth token and a recorded owner.
	token := env.login(t, "admin", "admin123")
	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
		&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
	require.NoError(t, err)

	worker, err := env.store.Workers().GetByID(context.Background(), regResp.GetWorkerId())
	require.NoError(t, err)
	require.NotEmpty(t, worker.RegisteredBy)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := dialWorkerOverUnixSocket(t, ctx, env.mux, worker.AuthToken)
	// The worker sends its hello before the Hub greets it.
	require.NoError(t, stream.Send(helloRequest(classicHello(0x31))))

	first, err := stream.Receive()
	require.NoError(t, err)
	require.NotNil(t, first.GetWorkerIdentity(),
		"the FIRST message on the stream must be WorkerIdentity, before any other payload")
	assert.Equal(t, worker.RegisteredBy, first.GetWorkerIdentity().GetRegisteredBy(),
		"the Hub must name the worker's recorded owner")
	// The greeting also carries the owner's username, which the worker reports
	// to `leapmux control whoami` and could not learn any other way.
	assert.Equal(t, "admin", first.GetWorkerIdentity().GetRegisteredByUsername(),
		"the Hub must give the owner's username")
	require.NoError(t, stream.Close())
}

// connectAdminWorker registers a worker as the admin and opens its WebSocket
// stream over HTTP/1.1. It returns the stream, the worker's owner id, and
// the greeting the Hub sent first.
func connectAdminWorker(t *testing.T, env *regKeyEnv) (*workerTestPeer, string, *leapmuxv1.WorkerIdentity) {
	t.Helper()
	stream, worker := openAdminWorkerStream(t, env)
	first, err := stream.Receive()
	require.NoError(t, err)
	require.NotNil(t, first.GetWorkerIdentity(), "the first frame is the greeting")
	return stream, worker.RegisteredBy, first.GetWorkerIdentity()
}

// openAdminWorkerStream registers a worker as the admin, opens its WebSocket
// stream over HTTP/1.1 and sends the hello without waiting for the greeting.
// Tests can use store hooks to pause setup before the Hub writes that greeting.
func openAdminWorkerStream(t *testing.T, env *regKeyEnv) (*workerTestPeer, *store.Worker) {
	t.Helper()
	token := env.login(t, "admin", "admin123")
	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
		&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
	require.NoError(t, err)
	worker, err := env.store.Workers().GetByID(context.Background(), regResp.GetWorkerId())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	stream := dialWorker(t, ctx, env.server, worker.AuthToken)
	t.Cleanup(func() { _ = stream.Close() })
	require.NoError(t, stream.Send(helloRequest(classicHello(0x31))))
	return stream, worker
}

// nextWorkerIdentity reads frames until the next WorkerIdentity frame.
func nextWorkerIdentity(t *testing.T, stream *workerTestPeer) *leapmuxv1.WorkerIdentity {
	t.Helper()
	for {
		frame, err := stream.Receive()
		require.NoError(t, err)
		if identity := frame.GetWorkerIdentity(); identity != nil {
			return identity
		}
	}
}

// renameUser changes a user's username in the store, as UserService.UpdateProfile
// does.
func renameUser(t *testing.T, st store.Store, userID, username string) {
	t.Helper()
	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	require.NoError(t, st.Users().UpdateProfile(context.Background(), store.UpdateUserProfileParams{
		ID: userID, Username: username, DisplayName: user.DisplayName,
	}))
}

// A rename reaches a connected worker without a reconnect: the Hub re-delivers
// the identity to every worker of the owner, which is what the user-info
// observer in hub/server.go calls for each user_info event.
func TestRedeliverWorkerIdentity_FollowsARename(t *testing.T) {
	t.Parallel()
	env := setupRegKeyEnv(t)
	stream, owner, greeting := connectAdminWorker(t, env)
	require.Equal(t, "admin", greeting.GetRegisteredByUsername())

	renameUser(t, env.store, owner, "admin-renamed")
	env.connector.RedeliverWorkerIdentity(owner)

	identity := nextWorkerIdentity(t, stream)
	assert.Equal(t, owner, identity.GetRegisteredBy(), "a rename keeps the owner")
	assert.Equal(t, "admin-renamed", identity.GetRegisteredByUsername(), "the worker learns the new username")

	// Another user's change reaches none of this owner's workers: the next
	// identity frame the worker reads is the owner's own later re-delivery.
	env.connector.RedeliverWorkerIdentity("someone-else")
	env.connector.RedeliverWorkerIdentity("")
	renameUser(t, env.store, owner, "admin-final")
	env.connector.RedeliverWorkerIdentity(owner)
	identity = nextWorkerIdentity(t, stream)
	assert.Equal(t, owner, identity.GetRegisteredBy(), "no other owner's identity reaches this worker")
	assert.Equal(t, "admin-final", identity.GetRegisteredByUsername())
}

// A rename can commit after serveWorkerStream's greeting read returned the
// old name and before the registration publishes the connection. Its
// re-delivery then starts while the registry does not hold the connection
// yet. serveWorkerStream holds the owner's delivery lock across both steps,
// so the re-delivery waits, takes its snapshot after the registration, and
// the worker never keeps the old name.
func TestConnect_SendsARenameThatRacedTheRegistration(t *testing.T) {
	t.Parallel()
	var renamed atomic.Bool
	var connector atomic.Pointer[service.WorkerConnectorService]
	redelivered := make(chan struct{})
	env := newRegKeyEnv(t, testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
		return renameAfterFirstReadStore{
			Store: st, renamed: &renamed, username: "admin-raced",
			// The rename's user-info observer, which runs on another goroutine
			// while serveWorkerStream still holds the lock.
			afterRename: func(owner string) {
				go func() {
					defer close(redelivered)
					connector.Load().RedeliverWorkerIdentity(owner)
				}()
			},
		}
	}})
	connector.Store(env.connector)
	stream, owner, greeting := connectAdminWorker(t, env)
	require.Equal(t, "admin", greeting.GetRegisteredByUsername(), "the greeting holds the name read before the rename")
	require.True(t, renamed.Load(), "precondition: the rename landed during Connect")

	identity := nextWorkerIdentity(t, stream)
	assert.Equal(t, owner, identity.GetRegisteredBy())
	assert.Equal(t, "admin-raced", identity.GetRegisteredByUsername(),
		"the re-delivery finds the connection that its rename raced")
	<-redelivered
}

// A connection reads the owner's username once, for its greeting. The count is
// taken after a re-delivery, whose frame the test reads, so it holds every read
// that serveWorkerStream made.
func TestConnect_ReadsTheUsernameOnce(t *testing.T) {
	t.Parallel()
	var reads atomic.Int64
	env := newRegKeyEnv(t, testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
		return userReadCountingStore{Store: st, reads: &reads}
	}})
	stream, owner, _ := connectAdminWorker(t, env)

	renameUser(t, env.store, owner, "admin-counted-once")
	env.connector.RedeliverWorkerIdentity(owner)
	assert.Equal(t, "admin-counted-once", nextWorkerIdentity(t, stream).GetRegisteredByUsername())
	assert.Equal(t, int64(2), reads.Load(), "the greeting's read, then the re-delivery's read")
}

// A failed username read keeps the name each worker holds: the re-delivery
// sends nothing rather than an empty name.
func TestRedeliverWorkerIdentity_SendsNothingWhenTheAccountCannotBeRead(t *testing.T) {
	t.Parallel()
	var failReads atomic.Bool
	env := newRegKeyEnv(t, testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
		return userReadFailStore{Store: st, fail: &failReads}
	}})
	stream, owner, _ := connectAdminWorker(t, env)

	failReads.Store(true)
	env.connector.RedeliverWorkerIdentity(owner)
	failReads.Store(false)
	renameUser(t, env.store, owner, "admin-after")
	env.connector.RedeliverWorkerIdentity(owner)

	// The first identity frame after the greeting is the second re-delivery:
	// the failed one sent nothing.
	identity := nextWorkerIdentity(t, stream)
	assert.Equal(t, "admin-after", identity.GetRegisteredByUsername())
}

// The username is display only, so a failed read never refuses a connection:
// the greeting identifies the owner with no username, and no second frame repeats
// the empty name. The connection stays live, so a later re-delivery reaches it.
func TestConnect_GreetsWithNoUsernameWhenTheAccountCannotBeRead(t *testing.T) {
	t.Parallel()
	var failReads atomic.Bool
	failReads.Store(true)
	env := newRegKeyEnv(t, testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
		return userReadFailStore{Store: st, fail: &failReads}
	}})
	stream, owner, greeting := connectAdminWorker(t, env)
	assert.Equal(t, owner, greeting.GetRegisteredBy(), "the greeting still identifies the owner")
	assert.Empty(t, greeting.GetRegisteredByUsername(), "an unread username is sent as none")

	failReads.Store(false)
	renameUser(t, env.store, owner, "admin-later")
	env.connector.RedeliverWorkerIdentity(owner)

	// The first identity frame after the greeting is the re-delivery:
	// serveWorkerStream sent no second frame for the read that failed.
	identity := nextWorkerIdentity(t, stream)
	assert.Equal(t, "admin-later", identity.GetRegisteredByUsername())
}

// A user-info event fires for every account change, so a re-delivery for an
// owner with no worker connected to this Hub must cost nothing: no store read,
// no frame. The owner with a connected worker is the control that shows the
// counter sees the read.
func TestRedeliverWorkerIdentity_ReadsNothingForAnOwnerWithNoConnectedWorker(t *testing.T) {
	t.Parallel()
	var reads atomic.Int64
	env := newRegKeyEnv(t, testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
		return userReadCountingStore{Store: st, reads: &reads}
	}})
	stream, owner, _ := connectAdminWorker(t, env)

	reads.Store(0)
	env.connector.RedeliverWorkerIdentity("someone-else")
	env.connector.RedeliverWorkerIdentity("")
	assert.Zero(t, reads.Load(), "an owner with no connected worker costs no read")

	renameUser(t, env.store, owner, "admin-counted")
	env.connector.RedeliverWorkerIdentity(owner)
	assert.Equal(t, int64(1), reads.Load(), "an owner with a connected worker costs one read")
	assert.Equal(t, "admin-counted", nextWorkerIdentity(t, stream).GetRegisteredByUsername())
}

// Every username read runs under the owner's delivery lock: the greeting's read
// and each re-delivery's read. The lock is what keeps a read that started before
// a rename from enqueuing after the rename's own re-delivery, so the worker can
// never end up with the older name.
func TestWorkerIdentityReadsHoldTheOwnersDeliveryLock(t *testing.T) {
	t.Parallel()
	var connector atomic.Pointer[service.WorkerConnectorService]
	var held lockObservations
	env := newRegKeyEnv(t, testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
		return lockObservingStore{Store: st, connector: &connector, held: &held}
	}})
	connector.Store(env.connector)
	stream, owner, _ := connectAdminWorker(t, env)

	renameUser(t, env.store, owner, "admin-locked")
	env.connector.RedeliverWorkerIdentity(owner)
	require.Equal(t, "admin-locked", nextWorkerIdentity(t, stream).GetRegisteredByUsername())

	assert.Equal(t, []bool{true, true}, held.snapshot(), "the greeting's read, then the re-delivery's read")
	assert.False(t, env.connector.IdentityDeliveryLockHeldForTest(owner), "each critical section ended")
}

// serveWorkerStream reads the owner's username under the owner's delivery
// lock. A read that stalls holds the lock for at most the identity read
// timeout, so a re-delivery for the same owner still completes. The user-info
// observers and the revocation watcher run that re-delivery, so a stall that
// held the lock for ever would stall them too.
func TestConnect_AStalledGreetingReadReleasesTheOwnersDeliveryLock(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	stalled := make(chan struct{})
	env := regKeyEnvFromStore(t, hubtestutil.OpenTestStore(t), testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
		return stallUserReadStore{Store: st, reads: new(atomic.Int64), stallRead: 1, stalled: stalled}
	}}, clock)
	stream, worker := openAdminWorkerStream(t, env)
	owner := worker.RegisteredBy

	select {
	case <-stalled:
	case <-ctx.Done():
		require.FailNow(t, "Connect never read the username for its greeting")
	}
	require.True(t, env.connector.IdentityDeliveryLockHeldForTest(owner),
		"precondition: the stalled read holds the owner's delivery lock")

	renameUser(t, env.store, owner, "admin-after-stall")
	redelivered := make(chan struct{})
	go func() {
		defer close(redelivered)
		env.connector.RedeliverWorkerIdentity(owner)
	}()
	// The re-delivery takes its snapshot inside the lock, so it waits here. One
	// that took the snapshot first would find no connection and return, and the
	// worker would keep the old name. A slow run can only miss that mistake, so
	// this window cannot fail a correct build.
	assert.Never(t, func() bool {
		select {
		case <-redelivered:
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, 5*time.Millisecond, "the re-delivery must wait for the greeting's critical section")
	clock.Advance(service.WorkerIdentityReadTimeoutForTest).MustWait(ctx)
	select {
	case <-redelivered:
	case <-ctx.Done():
		require.FailNow(t, "the re-delivery still waits on the owner's delivery lock after the read timeout")
	}

	assert.Empty(t, nextWorkerIdentity(t, stream).GetRegisteredByUsername(),
		"the stalled read greets with no username")
	assert.Equal(t, "admin-after-stall", nextWorkerIdentity(t, stream).GetRegisteredByUsername(),
		"the re-delivery waited for the registration, so it found the connection")
}

// A re-delivery reads the username under the owner's delivery lock too, and it
// runs inside a user-info observer with no request context of its own. A read
// that stalls must end at the identity read timeout, so the observer, and the
// revocation watcher that runs it, does not wait for ever.
func TestRedeliverWorkerIdentity_AStalledReadEndsAtTheReadTimeout(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	stalled := make(chan struct{})
	env := regKeyEnvFromStore(t, hubtestutil.OpenTestStore(t), testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
		// The first read is serveWorkerStream's; the second is the re-delivery's.
		return stallUserReadStore{Store: st, reads: new(atomic.Int64), stallRead: 2, stalled: stalled}
	}}, clock)
	stream, owner, _ := connectAdminWorker(t, env)

	redelivered := make(chan struct{})
	go func() {
		defer close(redelivered)
		env.connector.RedeliverWorkerIdentity(owner)
	}()
	select {
	case <-stalled:
	case <-ctx.Done():
		require.FailNow(t, "the re-delivery never read the username")
	}
	clock.Advance(service.WorkerIdentityReadTimeoutForTest).MustWait(ctx)
	select {
	case <-redelivered:
	case <-ctx.Done():
		require.FailNow(t, "the re-delivery still waits on its read after the read timeout")
	}
	assert.False(t, env.connector.IdentityDeliveryLockHeldForTest(owner), "the re-delivery released the owner's delivery lock")

	// The stalled read sent nothing, and the next re-delivery still works.
	renameUser(t, env.store, owner, "admin-after-redeliver-stall")
	env.connector.RedeliverWorkerIdentity(owner)
	assert.Equal(t, "admin-after-redeliver-stall", nextWorkerIdentity(t, stream).GetRegisteredByUsername())
}

// A panic in serveWorkerStream's greeting read must release the owner's
// delivery lock. Otherwise every later delivery for the owners on that lock's
// stripe blocks for ever. Nothing registered, so the worker stays offline.
func TestConnect_APanicInTheGreetingReadUnlocks(t *testing.T) {
	t.Parallel()
	env := newRegKeyEnv(t, testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
		return panicUserReadStore{Store: st, reads: new(atomic.Int64), panicRead: 1}
	}})
	stream, worker := openAdminWorkerStream(t, env)

	_, err := stream.Receive()
	require.Error(t, err, "the panic ends the stream before any frame")
	assert.Nil(t, env.wMgr.ConnForTrustedPath(worker.ID), "nothing registered the connection")
	assert.False(t, env.connector.IdentityDeliveryLockHeldForTest(worker.RegisteredBy),
		"the panic must release the owner's delivery lock")
}

// A panic in serveWorkerStream after the registration must undo the
// registration. Otherwise the worker reads online through a connection that
// nobody drains.
func TestConnect_APanicAfterTheRegistrationUnregisters(t *testing.T) {
	t.Parallel()
	env := newRegKeyEnv(t, testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
		return panicWorkerReadStore{Store: st}
	}})
	stream, worker := openAdminWorkerStream(t, env)

	// The teardown can still write the queued greeting before the stream
	// ends, so read until the stream fails.
	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}
	assert.Nil(t, env.wMgr.ConnForTrustedPath(worker.ID), "the panic must unregister the connection")
	assert.False(t, env.connector.IdentityDeliveryLockHeldForTest(worker.RegisteredBy),
		"the critical section ended with the registration")
}

// panicWorkerReadStore panics in the worker read that serveWorkerStream makes
// after the registration.
type panicWorkerReadStore struct {
	store.Store
}

func (s panicWorkerReadStore) Workers() store.WorkerStore {
	return panicWorkerReadWorkers{WorkerStore: s.Store.Workers()}
}

type panicWorkerReadWorkers struct {
	store.WorkerStore
}

func (panicWorkerReadWorkers) GetByIDIncludeDeleted(context.Context, string) (*store.Worker, error) {
	panic("worker read panicked")
}

// stallUserReadStore blocks the connector's user read number stallRead by id
// until that read's context ends, and closes stalled when the read starts.
// Every other read passes through.
type stallUserReadStore struct {
	store.Store
	reads     *atomic.Int64
	stallRead int64
	stalled   chan struct{}
}

func (s stallUserReadStore) Users() store.UserStore {
	return stallUserReadUsers{UserStore: s.Store.Users(), stall: s}
}

type stallUserReadUsers struct {
	store.UserStore
	stall stallUserReadStore
}

func (u stallUserReadUsers) GetByID(ctx context.Context, id string) (*store.User, error) {
	if u.stall.reads.Add(1) != u.stall.stallRead {
		return u.UserStore.GetByID(ctx, id)
	}
	close(u.stall.stalled)
	<-ctx.Done()
	return nil, ctx.Err()
}

// panicUserReadStore panics in the connector's user read number panicRead by
// id. Every other read passes through.
type panicUserReadStore struct {
	store.Store
	reads     *atomic.Int64
	panicRead int64
}

func (s panicUserReadStore) Users() store.UserStore {
	return panicUserReadUsers{UserStore: s.Store.Users(), reads: s.reads, panicRead: s.panicRead}
}

type panicUserReadUsers struct {
	store.UserStore
	reads     *atomic.Int64
	panicRead int64
}

func (u panicUserReadUsers) GetByID(ctx context.Context, id string) (*store.User, error) {
	if u.reads.Add(1) == u.panicRead {
		panic("user read panicked")
	}
	return u.UserStore.GetByID(ctx, id)
}

// lockObservations records, for each user read, whether the reader held the
// owner's identity delivery lock.
type lockObservations struct {
	mu   sync.Mutex
	held []bool
}

func (o *lockObservations) record(held bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.held = append(o.held, held)
}

func (o *lockObservations) snapshot() []bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.held)
}

// lockObservingStore records, on each user read by id, whether the
// connector's identity delivery lock for that user is held.
type lockObservingStore struct {
	store.Store
	connector *atomic.Pointer[service.WorkerConnectorService]
	held      *lockObservations
}

func (s lockObservingStore) Users() store.UserStore {
	return lockObservingUsers{UserStore: s.Store.Users(), connector: s.connector, held: s.held}
}

type lockObservingUsers struct {
	store.UserStore
	connector *atomic.Pointer[service.WorkerConnectorService]
	held      *lockObservations
}

func (u lockObservingUsers) GetByID(ctx context.Context, id string) (*store.User, error) {
	if c := u.connector.Load(); c != nil {
		u.held.record(c.IdentityDeliveryLockHeldForTest(id))
	}
	return u.UserStore.GetByID(ctx, id)
}

// userReadCountingStore counts the connector's user reads by id.
type userReadCountingStore struct {
	store.Store
	reads *atomic.Int64
}

func (s userReadCountingStore) Users() store.UserStore {
	return userReadCountingUsers{UserStore: s.Store.Users(), reads: s.reads}
}

type userReadCountingUsers struct {
	store.UserStore
	reads *atomic.Int64
}

func (u userReadCountingUsers) GetByID(ctx context.Context, id string) (*store.User, error) {
	u.reads.Add(1)
	return u.UserStore.GetByID(ctx, id)
}

// renameAfterFirstReadStore renames a user right after the connector's first
// read of that user, which is the greeting read in serveWorkerStream.
type renameAfterFirstReadStore struct {
	store.Store
	renamed  *atomic.Bool
	username string
	// afterRename runs once, on the reader's goroutine, right after the rename
	// commits.
	afterRename func(owner string)
}

func (s renameAfterFirstReadStore) Users() store.UserStore {
	return renameAfterFirstReadUsers{UserStore: s.Store.Users(), stub: s}
}

type renameAfterFirstReadUsers struct {
	store.UserStore
	stub renameAfterFirstReadStore
}

func (u renameAfterFirstReadUsers) GetByID(ctx context.Context, id string) (*store.User, error) {
	user, err := u.UserStore.GetByID(ctx, id)
	if err != nil || !u.stub.renamed.CompareAndSwap(false, true) {
		return user, err
	}
	if err := u.UpdateProfile(ctx, store.UpdateUserProfileParams{
		ID: id, Username: u.stub.username, DisplayName: user.DisplayName,
	}); err != nil {
		return nil, err
	}
	u.stub.afterRename(id)
	return user, nil
}

// userReadFailStore fails the connector's user reads while fail is set.
type userReadFailStore struct {
	store.Store
	fail *atomic.Bool
}

func (s userReadFailStore) Users() store.UserStore {
	return userReadFailUsers{UserStore: s.Store.Users(), fail: s.fail}
}

type userReadFailUsers struct {
	store.UserStore
	fail *atomic.Bool
}

func (u userReadFailUsers) GetByID(ctx context.Context, id string) (*store.User, error) {
	if u.fail.Load() {
		return nil, errors.New("user read failed")
	}
	return u.UserStore.GetByID(ctx, id)
}

// TestConnect_TellsADeregisteringWorkerToStopAndDeletesItOnAck pins the
// recovery for a worker deregistered while it was offline. deregistering_at is
// the persisted intent, so its next connect must carry the DEREGISTER
// instruction, and the worker's acknowledgement deletes it.
func TestConnect_TellsADeregisteringWorkerToStopAndDeletesItOnAck(t *testing.T) {
	t.Parallel()
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")
	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
		&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
	require.NoError(t, err)
	workerID := regResp.GetWorkerId()
	deregistered, err := env.store.Workers().ForceDeregister(context.Background(), workerID)
	require.NoError(t, err)
	require.Equal(t, int64(1), deregistered, "the worker is deregistered while no connection exists")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := dialWorker(t, ctx, env.server, regResp.GetAuthToken())
	require.NoError(t, stream.Send(helloRequest(postQuantumHello(0x31))))

	var instruction *leapmuxv1.ConnectResponse
	for instruction == nil {
		frame, err := stream.Receive()
		require.NoError(t, err, "the Hub must send DEREGISTER before it ends the stream")
		if frame.GetDeregister() != nil {
			instruction = frame
		}
	}
	require.NoError(t, stream.Send(&leapmuxv1.ConnectRequest{
		RequestId: instruction.GetRequestId(),
		Payload:   &leapmuxv1.ConnectRequest_DeregisterAck{DeregisterAck: &leapmuxv1.DeregisterAck{}},
	}))

	testutil.AssertEventually(t, func() bool {
		row, err := env.store.Workers().GetByIDIncludeDeleted(context.Background(), workerID)
		return err == nil && row.Status == leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED
	})
	testutil.AssertEventually(t, func() bool {
		return !env.wMgr.OnlineForTrustedPath(workerID)
	})
	require.NoError(t, stream.Close())
}

func TestFailedStopDeliveryEndsTheActiveStreamAndRecoversDurableState(t *testing.T) {
	t.Parallel()
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "deregistering on reconnect", true: "deleted on reconnect"}[deleted], func(t *testing.T) {
			env := setupRegKeyEnv(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			key := id.Generate()
			require.NoError(t, env.store.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
				ID: key, CreatedBy: userid.MustNew(env.adminID(t)), ExpiresAt: time.Now().Add(time.Hour),
			}))
			registered, err := env.registerWithKey(t, key)
			require.NoError(t, err)

			first := dialWorker(t, ctx, env.server, registered.GetAuthToken())
			require.NoError(t, first.Send(helloRequest(postQuantumHello(0x31))))
			// The identity frame proves the Hub registered the connection.
			for {
				frame, err := first.Receive()
				require.NoError(t, err)
				if frame.GetWorkerIdentity() != nil {
					break
				}
			}
			if deleted {
				require.NoError(t, env.store.Workers().MarkDeleted(ctx, registered.GetWorkerId()))
			} else {
				_, err := env.store.Workers().ForceDeregister(ctx, registered.GetWorkerId())
				require.NoError(t, err)
			}
			failed := notifier.New(env.store, env.wMgr, failedStopSender{}, func() time.Duration { return time.Second }, nil)
			require.NoError(t, failed.SendDeregister(ctx, registered.GetWorkerId()))
			for {
				_, err := first.Receive()
				if err != nil {
					break
				}
			}
			require.NoError(t, ctx.Err(), "the stream must end because delivery forced reconnect, not because the test expired")
			testutil.AssertEventually(t, func() bool { return !env.wMgr.OnlineForTrustedPath(registered.GetWorkerId()) })
			next := dialWorker(t, ctx, env.server, registered.GetAuthToken())
			if deleted {
				_, err := next.Receive()
				require.Error(t, err)
				requireWorkerClose(t, err, websocket.StatusPolicyViolation, channelwire.WorkerCloseReasonDeleted)
				return
			}
			require.NoError(t, next.Send(helloRequest(postQuantumHello(0x31))))
			for {
				frame, err := next.Receive()
				require.NoError(t, err)
				if frame.GetDeregister() != nil {
					require.NoError(t, next.Send(&leapmuxv1.ConnectRequest{
						RequestId: frame.GetRequestId(), Payload: &leapmuxv1.ConnectRequest_DeregisterAck{DeregisterAck: &leapmuxv1.DeregisterAck{}},
					}))
					break
				}
			}
			testutil.AssertEventually(t, func() bool {
				worker, err := env.store.Workers().GetByIDIncludeDeleted(ctx, registered.GetWorkerId())
				return err == nil && worker.DeletedAt != nil
			})
		})
	}
}

type failedStopSender struct{}

func (failedStopSender) SendAndWait(context.Context, *workermgr.Conn, *leapmuxv1.ConnectResponse) (*leapmuxv1.ConnectRequest, error) {
	return nil, errors.New("the stop instruction was not delivered")
}

// authReadHookStore runs afterAuthRead after each successful
// GetByAuthTokenIncludeDeleted call. This is the window between the stream
// handler's authentication read and connection registration.
type authReadHookStore struct {
	store.Store
	afterAuthRead func(*store.Worker)
}

func (s authReadHookStore) Workers() store.WorkerStore {
	return authReadHookWorkers{WorkerStore: s.Store.Workers(), afterAuthRead: s.afterAuthRead}
}

type authReadHookWorkers struct {
	store.WorkerStore
	afterAuthRead func(*store.Worker)
}

func (w authReadHookWorkers) GetByAuthTokenIncludeDeleted(ctx context.Context, token string) (*store.Worker, error) {
	worker, err := w.WorkerStore.GetByAuthTokenIncludeDeleted(ctx, token)
	if err == nil {
		w.afterAuthRead(worker)
	}
	return worker, err
}

// A deregistration, or a deletion with the worker's user, can commit after
// WorkerStreamHandler authenticates the worker and before the session
// registers its connection. SendDeregister then finds no connection and tells
// nobody, so serveWorkerStream must notice the change and tell the worker to
// stop. Otherwise the worker stays connected, and keeps its agents running,
// until it happens to reconnect.
func TestConnect_TellsAWorkerChangedWhileItConnectsToStop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(ctx context.Context, st store.Store, workerID string) error
	}{
		{
			name: "deregistered",
			change: func(ctx context.Context, st store.Store, workerID string) error {
				_, err := st.Workers().ForceDeregister(ctx, workerID)
				return err
			},
		},
		{
			name: "deleted with its user",
			change: func(ctx context.Context, st store.Store, workerID string) error {
				return st.Workers().MarkDeleted(ctx, workerID)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var env *regKeyEnv
			var once sync.Once
			env = newRegKeyEnv(t, testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
				return authReadHookStore{Store: st, afterAuthRead: func(worker *store.Worker) {
					once.Do(func() {
						ctx := context.Background()
						assert.False(t, env.wMgr.OnlineForTrustedPath(worker.ID),
							"the change must land before Connect registers the connection")
						// The durable change commits first, and the notifier
						// runs after it, exactly as the services call it.
						assert.NoError(t, tt.change(ctx, env.store, worker.ID))
						assert.NoError(t, env.notifier.SendDeregister(ctx, worker.ID))
					})
				}}
			}})
			token := env.login(t, "admin", "admin123")
			createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
				&leapmuxv1.CreateRegistrationKeyRequest{}, token))
			require.NoError(t, err)
			regResp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
			require.NoError(t, err)
			workerID := regResp.GetWorkerId()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			stream := dialWorker(t, ctx, env.server, regResp.GetAuthToken())
			require.NoError(t, stream.Send(helloRequest(postQuantumHello(0x31))))

			var instruction *leapmuxv1.ConnectResponse
			for instruction == nil {
				frame, err := stream.Receive()
				require.NoError(t, err, "the Hub must send DEREGISTER before it ends the stream")
				if frame.GetDeregister() != nil {
					instruction = frame
				}
			}
			require.NoError(t, stream.Send(&leapmuxv1.ConnectRequest{
				RequestId: instruction.GetRequestId(),
				Payload:   &leapmuxv1.ConnectRequest_DeregisterAck{DeregisterAck: &leapmuxv1.DeregisterAck{}},
			}))

			testutil.AssertEventually(t, func() bool {
				row, err := env.store.Workers().GetByIDIncludeDeleted(context.Background(), workerID)
				return err == nil && row.Status == leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED
			})
			testutil.AssertEventually(t, func() bool {
				return !env.wMgr.OnlineForTrustedPath(workerID) && !env.wMgr.IsDeregistering(workerID)
			})
			require.NoError(t, stream.Close())
		})
	}
}

// workerReadFailStore fails every GetByIDIncludeDeleted, which is the read that
// serveWorkerStream makes after it registers the connection.
type workerReadFailStore struct {
	store.Store
}

func (s workerReadFailStore) Workers() store.WorkerStore {
	return workerReadFailWorkers{WorkerStore: s.Store.Workers()}
}

type workerReadFailWorkers struct {
	store.WorkerStore
}

func (workerReadFailWorkers) GetByIDIncludeDeleted(context.Context, string) (*store.Worker, error) {
	return nil, errors.New("worker read failed")
}

// serveWorkerStream cannot know whether the worker must be told to stop when
// it cannot read the worker again after the registration. It must refuse the
// stream rather than serve it as ACTIVE, and the refused connection must
// leave the registry. The worker reconnects, and serveWorkerStream decides
// again on the next connection.
func TestConnect_RefusesTheStreamWhenTheWorkerCannotBeReadAgain(t *testing.T) {
	t.Parallel()
	env := newRegKeyEnv(t, testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
		return workerReadFailStore{Store: st}
	}})
	token := env.login(t, "admin", "admin123")
	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
		&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
	require.NoError(t, err)
	workerID := regResp.GetWorkerId()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream := dialWorker(t, ctx, env.server, regResp.GetAuthToken())
	require.NoError(t, stream.Send(helloRequest(postQuantumHello(0x31))))

	for {
		frame, err := stream.Receive()
		if err != nil {
			requireWorkerClose(t, err, websocket.StatusInternalError, channelwire.WorkerCloseReasonHubInternalError)
			break
		}
		assert.Nil(t, frame.GetDeregister(), "a worker whose state is unknown must not be told to stop")
	}
	testutil.AssertEventually(t, func() bool {
		return !env.wMgr.OnlineForTrustedPath(workerID)
	})
	row, err := env.store.Workers().GetByID(context.Background(), workerID)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, row.Status)
}

// A worker stream can pass the WebSocket admission check before shutdown,
// then spend time reading its hello, comparing keys, and reading the store
// before Register sees the fenced registry. The worker must receive close
// 1012 with hub_shutting_down. The manager tests prove the refusal; this test
// proves that a real stream receives the shutdown close rather than close
// 1011 with hub_internal_error.
func TestConnect_FencedRegistryRefusesWithServiceRestart(t *testing.T) {
	t.Parallel()
	env := setupRegKeyEnv(t)

	token := env.login(t, "admin", "admin123")
	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
		&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
	require.NoError(t, err)

	worker, err := env.store.Workers().GetByID(context.Background(), regResp.GetWorkerId())
	require.NoError(t, err)

	// The fixture omits the WebSocket admission check, so the request reaches
	// the registry after shutdown fences it.
	env.wMgr.FenceAll()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := dialWorker(t, ctx, env.server, worker.AuthToken)
	// The registry is fenced before this stream opens, and the Hub reads the
	// hello before Register refuses the connection, so this Send normally
	// succeeds. The close frame carries the refusal.
	require.NoError(t, stream.Send(helloRequest(classicHello(0x31))))

	_, err = stream.Receive()
	require.Error(t, err, "a fenced registry must refuse the connection, not hold the stream open")
	requireWorkerClose(t, err, websocket.StatusServiceRestart, channelwire.WorkerCloseReasonHubShuttingDown)
	assert.Nil(t, env.wMgr.ConnForTrustedPath(worker.ID),
		"the refused connection must never be published")
	assert.False(t, env.connector.IdentityDeliveryLockHeldForTest(worker.RegisteredBy),
		"a refused registration ends the critical section too")
	_ = stream.Close()
}

// The regression this cap exists to close, end to end.
//
// max_workers_per_user is meant to bound how many of one account's Workers sit in
// the worker send-queue pool, each holding a floor the pool may not reclaim.
// Counting ROWS cannot do that. The pool member is created per worker stream,
// behind an auth-token lookup that -- correctly -- still admits a DEREGISTERING
// worker, because that stream is how the worker is told to tear itself down. The
// row count stops seeing it the moment the row deregisters, and nothing
// server-side ends the stream: SendDeregister only marks and notifies,
// MarkDeleted waits for the worker's own acknowledgement. Repeated
// registration and deregistration therefore added a member every time
// without exceeding the registration cap.
func TestConnect_RefusesWhenTheAccountIsAtItsLiveWorkerCap(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnvWithCfg(t, testConfig(), maxWorkersPerUser(1))
	token := env.login(t, "admin", "admin123")

	register := func(t *testing.T) *leapmuxv1.RegisterResponse {
		t.Helper()
		createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
			&leapmuxv1.CreateRegistrationKeyRequest{}, token))
		require.NoError(t, err)
		resp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
		require.NoError(t, err)
		return resp
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hello := helloRequest(postQuantumHello(0x31))
	inventory := &leapmuxv1.ConnectRequest{RequestId: "inventory", Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: &leapmuxv1.WorkerTabInventory{Complete: true}}}
	openStream := func(t *testing.T, authToken string) (*workerTestPeer, *leapmuxv1.ConnectResponse, error) {
		t.Helper()
		stream := dialWorker(t, ctx, env.server, authToken)
		if err := stream.Send(hello); err != nil {
			return stream, nil, err
		}
		first, err := stream.Receive()
		return stream, first, err
	}
	awaitInventory := func(t *testing.T, stream *workerTestPeer) {
		t.Helper()
		for {
			frame, err := stream.Receive()
			require.NoError(t, err)
			if frame.GetWorkerTabInventoryResp() != nil {
				return
			}
		}
	}

	first := register(t)
	firstStream, greeting, err := openStream(t, first.GetAuthToken())
	require.NoError(t, err)
	require.NotNil(t, greeting.GetWorkerIdentity(),
		"the greeting is enqueued before the conn is published, so receiving it proves membership")
	require.NoError(t, firstStream.Send(inventory))
	awaitInventory(t, firstStream)

	live := env.wMgr.ConnForTrustedPath(first.GetWorkerId())
	require.NotNil(t, live)
	assert.Equal(t, greeting.GetWorkerIdentity().GetRegisteredBy(), live.Owner(),
		"the connection must be counted against the account that registered the worker")

	// Deregister it. This is the row transition DeregisterWorker performs, driven
	// through the store because its notifier round trip is not what is under
	// test: SendDeregister parks in SendAndWait until the worker acks or
	// api_timeout expires, and a bare test stream acks nothing. The row state is what the row
	// cap reads, and the registry flag is what the RPC sets alongside it.
	rows, err := env.store.Workers().Deregister(context.Background(), store.DeregisterWorkerParams{
		ID:           first.GetWorkerId(),
		RegisteredBy: userid.MustNew(env.adminID(t)),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	env.wMgr.MarkDeregistering(first.GetWorkerId())

	deregistered, err := env.store.Workers().GetByIDIncludeDeleted(context.Background(), first.GetWorkerId())
	require.NoError(t, err)
	require.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, deregistered.Status)

	// The deregistering row still holds a writable connection and queue membership.
	env.connector.RedeliverWorkerIdentity(env.adminID(t))
	require.NotNil(t, nextWorkerIdentity(t, firstStream))
	require.NotNil(t, env.wMgr.ConnForTrustedPath(first.GetWorkerId()),
		"a deregistering worker keeps its connection, and therefore its pool membership")

	// The row cap now sees an account with no ACTIVE workers, so a replacement
	// registers. That much is intended -- a machine being torn down should not
	// keep its owner from bringing up its successor -- and it is also exactly why
	// the row cap cannot be the membership bound.
	second := register(t)

	// Its CONNECTION is what must be refused, because the first worker is still
	// holding a member of the pool.
	//
	// Counted on the same series as a refused registration, under the stage that
	// says WHERE it bit -- an operator asking "is this cap biting?" gets one
	// number, and a refusal here means an existing stream is holding the slot
	// rather than that a row has to be deregistered.
	refusalsBefore := workerAdmissionsRefused(metrics.WorkerStageConnect)
	secondStream, _, err := openStream(t, second.GetAuthToken())
	require.Error(t, err, "cycling registrations must not grow live worker-pool membership")
	requireWorkerClose(t, err, websocket.StatusTryAgainLater, channelwire.WorkerCloseReasonTooManyWorkers)
	assert.GreaterOrEqual(t, workerAdmissionsRefused(metrics.WorkerStageConnect), refusalsBefore+1,
		"a Worker refused at Connect must be counted, under the connect stage")
	assert.Contains(t, err.Error(), "max_workers_per_user",
		"the operator-facing key has to be in the message, or nobody knows what to raise")
	assert.Nil(t, env.wMgr.ConnForTrustedPath(second.GetWorkerId()),
		"the refused connection must never be published")
	_ = secondStream.Close()

	// The disconnect -- not the deregistration -- is what gives the slot back.
	require.NoError(t, firstStream.Close())
	testutil.AssertEventually(t, func() bool {
		return !env.wMgr.OnlineForTrustedPath(first.GetWorkerId())
	})

	thirdStream, greeting, err := openStream(t, second.GetAuthToken())
	require.NoError(t, err, "a freed slot must be usable")
	assert.NotNil(t, greeting.GetWorkerIdentity())
	require.NoError(t, thirdStream.Close())
}

// subscribeUserEvents subscribes to userID's CRDT manager the way a
// /ws/userevents connection whose credential holds worker:read does, with a
// filter that admits no workspace, and returns the events it receives.
func (e *regKeyEnv) subscribeUserEvents(t *testing.T, userID string) <-chan *leapmuxv1.WatchUserEvent {
	t.Helper()
	mgr, err := e.registry.Get(context.Background(), userID)
	require.NoError(t, err)
	events := make(chan *leapmuxv1.WatchUserEvent, 64)
	_, unsub := mgr.Subscribe(&crdt.Subscriber{
		UserID:         userID,
		Filter:         crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{}},
		MayReadWorkers: true,
		Send: func(evt *crdt.MarshaledEvent) error {
			events <- evt.Event
			return nil
		},
	})
	t.Cleanup(unsub)
	return events
}

// requireWorkersChanged waits for the next workers_changed event on events,
// skipping any other event, and fails with failMsg at the test deadline.
func requireWorkersChanged(t *testing.T, events <-chan *leapmuxv1.WatchUserEvent, failMsg string) {
	t.Helper()
	deadline := testutil.DeadlineContext(t)
	for {
		select {
		case evt := <-events:
			if evt.GetWorkersChanged() != nil {
				return
			}
		case <-deadline.Done():
			t.Fatal(failMsg)
		}
	}
}

// registerWorker mints a registration key as the admin and registers one
// worker with it, returning the new worker's ID.
func (e *regKeyEnv) registerWorker(t *testing.T, token string) string {
	t.Helper()
	createResp, err := e.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
		&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	resp, err := e.registerWithKey(t, createResp.GetRegistrationKey())
	require.NoError(t, err)
	return resp.GetWorkerId()
}

// A user whose only connection is /ws/userevents -- no channel socket open,
// because no worker is online yet -- must still learn that a worker joined.
func TestRegisterNotifiesAUserWithNoChannelSocket(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	events := env.subscribeUserEvents(t, env.adminID(t))
	env.registerWorker(t, env.login(t, "admin", "admin123"))

	requireWorkersChanged(t, events, "the user's /ws/userevents subscriber heard nothing about the new worker")
}

// The owner's own deregistration announces the change on the same stream.
func TestDeregisterNotifiesTheOwnersUserEvents(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")
	workerID := env.registerWorker(t, token)
	// Subscribe only now, so the registration's own event cannot satisfy the wait.
	events := env.subscribeUserEvents(t, env.adminID(t))

	_, err := env.mgmtClient.DeregisterWorker(authedReq(context.Background(),
		&leapmuxv1.DeregisterWorkerRequest{WorkerId: workerID}, token))
	require.NoError(t, err)

	requireWorkersChanged(t, events, "the owner's /ws/userevents subscriber heard nothing about the deregistration")
}

// workersChangedSoFar counts the workers_changed events already delivered to
// events, without waiting for more.
func workersChangedSoFar(events <-chan *leapmuxv1.WatchUserEvent) int {
	n := 0
	for {
		select {
		case evt := <-events:
			if evt.GetWorkersChanged() != nil {
				n++
			}
		default:
			return n
		}
	}
}

// The client loads a worker's name only while the list reads the worker online,
// and a worker registers before it connects. So the list must be re-fetched when
// the worker comes online and when it goes offline, not only when it registers.
// A connection that replaces a live one leaves the worker online, so it
// announces nothing.
func TestConnectAndDisconnectNotifyTheOwnersUserEvents(t *testing.T) {
	t.Parallel()
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")
	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
		&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
	require.NoError(t, err)
	workerID := regResp.GetWorkerId()
	// Subscribe only now, so the registration's own event cannot satisfy a wait.
	events := env.subscribeUserEvents(t, env.adminID(t))

	ctx := testutil.DeadlineContext(t)
	connectStream := func(hello *leapmuxv1.WorkerHello) *workerTestPeer {
		stream := dialWorker(t, ctx, env.server, regResp.GetAuthToken())
		t.Cleanup(func() { _ = stream.Close() })
		require.NoError(t, stream.Send(helloRequest(hello)))
		identity, err := stream.Receive()
		require.NoError(t, err)
		require.NotNil(t, identity.GetWorkerIdentity(), "the first frame after a valid hello is WorkerIdentity")
		return stream
	}

	// Both connections present the ONE key set the worker registered, in two
	// modes: the pin is about the keys, not the cipher choice.
	first := connectStream(classicHello(0x31))
	requireWorkersChanged(t, events, "the owner heard nothing when the worker came online")
	// The hub announces after Register published the connection.
	assert.True(t, env.wMgr.OnlineForTrustedPath(workerID), "a list read after the announcement shows the worker online")

	// The hub drains the greeting only after Register returned, so once the
	// replacement's identity arrived, any announcement of its Register was
	// already delivered. The superseded handler announces from its teardown,
	// which ends before its stream does.
	second := connectStream(postQuantumHello(0x31))
	for {
		if _, err := first.Receive(); err != nil {
			break
		}
	}
	assert.Zero(t, workersChangedSoFar(events), "a replacement leaves the worker online and announces nothing")

	require.NoError(t, second.Close())
	requireWorkersChanged(t, events, "the owner heard nothing when the worker went offline")
	assert.False(t, env.wMgr.OnlineForTrustedPath(workerID), "a list read after the announcement shows the worker offline")
}

// A worker-list change must not reach a channel socket: the Go tunnel client
// tries to decrypt every frame on it, and a plaintext frame closes its channel.
func TestAWorkerListChangeSendsNothingOnChannelSockets(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	adminID := env.adminID(t)
	events := env.subscribeUserEvents(t, adminID)
	channelFrames := make(chan *leapmuxv1.ChannelMessage, 8)
	env.channelMgr.BindUser(adminID, "recording-socket", func(msg *leapmuxv1.ChannelMessage) error {
		channelFrames <- msg
		return nil
	}, func() {})

	env.registerWorker(t, env.login(t, "admin", "admin123"))

	// Register delivers workers_changed to the subscriber before it answers,
	// so once the event arrived, any frame it sent to a channel socket arrived
	// too.
	requireWorkersChanged(t, events, "the user's /ws/userevents subscriber heard nothing about the new worker")
	assert.Empty(t, channelFrames, "no channel socket may receive a worker-list change")
}

// The registration pins the keys, which is the whole point: a stolen auth token
// can no longer attach other keys to this worker.
func TestRegisterPinsTheWorkersPublicKeys(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")
	keys := workerkeystest.Keys(0x31)

	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
		&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKeys(t, createResp.GetRegistrationKey(), keys)
	require.NoError(t, err)

	row, err := env.store.Workers().GetByID(context.Background(), regResp.GetWorkerId())
	require.NoError(t, err)
	assert.Equal(t, store.WorkerPublicKeysFromProto(keys), row.PublicKeys,
		"the new row must carry the keys the worker registered with, not the ones a later hello presents")
}

// A malformed key set is refused WITHOUT consuming the registration key: the
// keys are checked before the key is read, so the operator can fix the worker and
// retry with the same key.
func TestRegisterRefusesAnIncompleteKeySetWithoutConsumingTheKey(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")
	complete := workerkeystest.Keys(0x31)
	cloneWith := func(mutate func(*leapmuxv1.WorkerPublicKeys)) *leapmuxv1.WorkerPublicKeys {
		out := proto.Clone(complete).(*leapmuxv1.WorkerPublicKeys)
		mutate(out)
		return out
	}

	for name, keys := range map[string]*leapmuxv1.WorkerPublicKeys{
		"keys absent":        nil,
		"a short x25519 key": cloneWith(func(k *leapmuxv1.WorkerPublicKeys) { k.X25519 = k.X25519[:16] }),
		// The right size, but a low-order point: every handshake fails at its
		// DH, so the pin would leave the worker unreachable.
		"a low-order x25519 key": cloneWith(func(k *leapmuxv1.WorkerPublicKeys) {
			k.X25519 = make([]byte, contracts.X25519PublicKeySize)
		}),
		"ML-KEM absent":  cloneWith(func(k *leapmuxv1.WorkerPublicKeys) { k.Mlkem = nil }),
		"SLH-DSA absent": cloneWith(func(k *leapmuxv1.WorkerPublicKeys) { k.Slhdsa = nil }),
		"an oversized SLH-DSA key": cloneWith(func(k *leapmuxv1.WorkerPublicKeys) {
			k.Slhdsa = append(k.Slhdsa, 0)
		}),
	} {
		t.Run(name, func(t *testing.T) {
			createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
				&leapmuxv1.CreateRegistrationKeyRequest{}, token))
			require.NoError(t, err)

			_, err = env.registerWithKeys(t, createResp.GetRegistrationKey(), keys)
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

			// The same key still registers a complete set.
			resp, err := env.registerWithKeys(t, createResp.GetRegistrationKey(), complete)
			require.NoError(t, err, "a refused registration must not consume the key")
			assert.NotEmpty(t, resp.GetWorkerId())
		})
	}
}

// A hello whose keys differ from the pinned ones is refused before the
// connection is published, so it can never replace the live one; the registered
// keys then connect.
func TestConnectRefusesKeysThatDifferFromTheRegisteredKeys(t *testing.T) {
	t.Parallel()
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")
	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
		&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKeys(t, createResp.GetRegistrationKey(), workerkeystest.Keys(0x31))
	require.NoError(t, err)

	ctx := testutil.DeadlineContext(t)

	openStream := func(t *testing.T) *workerTestPeer {
		t.Helper()
		stream := dialWorker(t, ctx, env.server, regResp.GetAuthToken())
		t.Cleanup(func() { _ = stream.Close() })
		return stream
	}

	impostor := openStream(t)
	require.NoError(t, impostor.Send(helloRequest(postQuantumHello(0x77))))
	_, err = impostor.Receive()
	require.Error(t, err)
	requireWorkerClose(t, err, websocket.StatusPolicyViolation, channelwire.WorkerCloseReasonKeysDiffer)
	assert.Nil(t, env.wMgr.ConnForTrustedPath(regResp.GetWorkerId()),
		"a refused connection must never be published")

	real := openStream(t)
	require.NoError(t, real.Send(helloRequest(postQuantumHello(0x31))))
	identity, err := real.Receive()
	require.NoError(t, err)
	require.NotNil(t, identity.GetWorkerIdentity(), "the registered keys still connect")
}
