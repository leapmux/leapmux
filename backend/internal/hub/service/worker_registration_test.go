package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// recordingSender captures every Send for assertions about what content
// reached the mail layer. Using a real Sender (rather than a nil stub)
// lets the EmailRegistrationInstructions test verify body composition.
type recordingSender struct {
	mu       sync.Mutex
	messages []mail.Message
}

func (r *recordingSender) Send(_ context.Context, msg mail.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, msg)
	return nil
}
func (r *recordingSender) last() *mail.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.messages) == 0 {
		return nil
	}
	out := r.messages[len(r.messages)-1]
	return &out
}

type regKeyEnv struct {
	mgmtClient      leapmuxv1connect.WorkerManagementServiceClient
	connectorClient leapmuxv1connect.WorkerConnectorServiceClient
	authClient      leapmuxv1connect.AuthServiceClient
	store           store.Store
	mailer          *recordingSender
	server          *httptest.Server
	mux             *http.ServeMux
	wMgr            *workermgr.Manager
}

func setupRegKeyEnv(t *testing.T) *regKeyEnv {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	cfg := testConfig()
	wMgr := workermgr.New()
	cMgr := channelmgr.New()
	pendingReqs := workermgr.NewPendingRequests(cfg.APITimeout)

	mux := http.NewServeMux()
	interceptor, sc := auth.NewInterceptor(st, nil, false, false)
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)

	mailer := &recordingSender{}
	authSvc := service.NewAuthService(st, cfg, sc, nil, mail.NewStubSender())
	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(authPath, authHandler)

	connectorSvc := service.NewWorkerConnectorService(st, wMgr, cMgr, service.NewHubEventBroadcaster(cMgr), pendingReqs, nil, nil)
	connectorPath, connectorHandler := leapmuxv1connect.NewWorkerConnectorServiceHandler(connectorSvc, opts)
	mux.Handle(connectorPath, connectorHandler)

	notif := notifier.New(st, wMgr, pendingReqs, cfg)
	mgmtSvc := service.NewWorkerManagementService(st, wMgr, service.NewHubEventBroadcaster(cMgr), notif, mailer)
	mgmtPath, mgmtHandler := leapmuxv1connect.NewWorkerManagementServiceHandler(mgmtSvc, opts)
	mux.Handle(mgmtPath, mgmtHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &regKeyEnv{
		mgmtClient:      leapmuxv1connect.NewWorkerManagementServiceClient(server.Client(), server.URL),
		connectorClient: leapmuxv1connect.NewWorkerConnectorServiceClient(server.Client(), server.URL),
		authClient:      leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL),
		store:           st,
		mailer:          mailer,
		server:          server,
		mux:             mux,
		wMgr:            wMgr,
	}
}

func (e *regKeyEnv) login(t *testing.T, username, password string) string {
	t.Helper()
	resp, err := e.authClient.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: username, Password: password,
	}))
	require.NoError(t, err)
	return sessionFromCookie(t, resp.Header().Get("Set-Cookie"))
}

func (e *regKeyEnv) adminID(t *testing.T) string {
	t.Helper()
	admin, err := e.store.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	return admin.ID
}

func (e *regKeyEnv) registerWithKey(t *testing.T, key string) (*connect.Response[leapmuxv1.RegisterResponse], error) {
	t.Helper()
	req := connect.NewRequest(&leapmuxv1.RegisterRequest{Version: "v"})
	if key != "" {
		req.Header().Set("Authorization", "Bearer "+key)
	}
	return e.connectorClient.Register(context.Background(), req)
}

func TestCreateRegistrationKey_RequiresAuth(t *testing.T) {
	env := setupRegKeyEnv(t)

	_, err := env.mgmtClient.CreateRegistrationKey(context.Background(), connect.NewRequest(&leapmuxv1.CreateRegistrationKeyRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestCreateRegistrationKey_ReturnsKeyAndExpiry(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	resp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.GetRegistrationKey())
	require.NotNil(t, resp.Msg.GetExpiresAt())

	expiresAt := resp.Msg.GetExpiresAt().AsTime()
	delta := time.Until(expiresAt)
	// Should be ~5 min from now; allow generous slack for clock + RPC.
	assert.Greater(t, delta, 4*time.Minute)
	assert.Less(t, delta, 6*time.Minute)
}

func TestRegister_RejectsMissingBearer(t *testing.T) {
	env := setupRegKeyEnv(t)

	_, err := env.registerWithKey(t, "")
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestRegister_HappyPath_ReturnsCredentialsAndConsumesKey(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.Msg.GetRegistrationKey()

	regResp, err := env.registerWithKey(t, key)
	require.NoError(t, err)
	assert.NotEmpty(t, regResp.Msg.GetWorkerId())
	assert.NotEmpty(t, regResp.Msg.GetAuthToken())
	assert.NotEmpty(t, regResp.Msg.GetRegisteredBy())

	// A second Register with the same key must fail — the key was
	// soft-deleted as part of the consume txn.
	_, err = env.registerWithKey(t, key)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	// Worker row exists and has registered_by = key.created_by.
	w, err := env.store.Workers().GetByID(context.Background(), regResp.Msg.GetWorkerId())
	require.NoError(t, err)
	assert.Equal(t, regResp.Msg.GetRegisteredBy(), w.RegisteredBy)
}

func TestRegister_AtomicConsume_RaceProducesOneWinner(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.Msg.GetRegistrationKey()

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
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.Msg.GetRegistrationKey()

	// Force-expire by soft-deleting the row directly via the store.
	_, err = env.store.RegistrationKeys().SoftDelete(context.Background(), store.SoftDeleteRegistrationKeyParams{
		ID:        key,
		CreatedBy: env.adminID(t),
	})
	require.NoError(t, err)

	_, err = env.mgmtClient.ExtendRegistrationKey(context.Background(), authedReq(&leapmuxv1.ExtendRegistrationKeyRequest{
		RegistrationKey: key,
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"a dead key must never be revivable; clients must mint a new one")
}

func TestExtendRegistrationKey_RejectsTooEarly(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.Msg.GetRegistrationKey()

	// Just-minted key has ~5min remaining, well above the 2-min buffer.
	_, err = env.mgmtClient.ExtendRegistrationKey(context.Background(), authedReq(&leapmuxv1.ExtendRegistrationKeyRequest{
		RegistrationKey: key,
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "extension not allowed yet")
}

func TestExtendRegistrationKey_AcceptsInsideBuffer(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.Msg.GetRegistrationKey()

	// Push the key inside the extend window: 90s remaining < 2min buffer.
	rows, err := env.store.RegistrationKeys().Extend(context.Background(), store.ExtendRegistrationKeyParams{
		ID:        key,
		CreatedBy: env.adminID(t),
		ExpiresAt: time.Now().Add(90 * time.Second),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	resp, err := env.mgmtClient.ExtendRegistrationKey(context.Background(), authedReq(&leapmuxv1.ExtendRegistrationKeyRequest{
		RegistrationKey: key,
	}, token))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.GetExpiresAt())
	delta := time.Until(resp.Msg.GetExpiresAt().AsTime())
	assert.Greater(t, delta, 4*time.Minute, "extension should restore ~5min TTL")
}

func TestExtendRegistrationKey_RejectsOtherUsersKey(t *testing.T) {
	env := setupRegKeyEnv(t)
	adminToken := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, adminToken))
	require.NoError(t, err)
	key := createResp.Msg.GetRegistrationKey()

	// Bring up a second user. We seed directly via the store helper —
	// SignUp is disabled in this test config, and that's fine; the
	// registration-key flow has its own auth concerns we want to isolate.
	hubtestutil.CreateTestUser(t, env.store, "other", "secret-password")
	otherToken := env.login(t, "other", "secret-password")

	// Ownership is enforced inside the SQL WHERE clause via
	// RegistrationKeys().GetOwned: cross-user access is indistinguishable
	// from "no such key", which deliberately closes the oracle on whether
	// a key id corresponds to *some* other user's row.
	_, err = env.mgmtClient.ExtendRegistrationKey(context.Background(), authedReq(&leapmuxv1.ExtendRegistrationKeyRequest{
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
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.Msg.GetRegistrationKey()

	// Push the key inside the extend window so the service-level
	// buffer check passes.
	rows, err := env.store.RegistrationKeys().Extend(context.Background(), store.ExtendRegistrationKeyParams{
		ID:        key,
		CreatedBy: env.adminID(t),
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
	_, err = env.mgmtClient.ExtendRegistrationKey(context.Background(), authedReq(&leapmuxv1.ExtendRegistrationKeyRequest{
		RegistrationKey: key,
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"a Consumed key must never be resurrectable via Extend")

	// And a fresh Consume must still fail — the row is dead.
	_, err = env.store.RegistrationKeys().Consume(context.Background(), key)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestDeleteRegistrationKey_SoftDeletes(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.Msg.GetRegistrationKey()

	_, err = env.mgmtClient.DeleteRegistrationKey(context.Background(), authedReq(&leapmuxv1.DeleteRegistrationKeyRequest{
		RegistrationKey: key,
	}, token))
	require.NoError(t, err)

	// Subsequent register must be rejected.
	_, err = env.registerWithKey(t, key)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestEmailRegistrationInstructions_RequiresVerifiedEmail(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)

	// admin user is created without a verified email by CreateTestAdmin,
	// so the precondition check should reject.
	_, err = env.mgmtClient.EmailRegistrationInstructions(context.Background(), authedReq(&leapmuxv1.EmailRegistrationInstructionsRequest{
		RegistrationKey: createResp.Msg.GetRegistrationKey(),
		Command:         "leapmux worker --hub http://x --registration-key abc",
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Nil(t, env.mailer.last(), "no mail should be sent without a verified email")
}

func TestEmailRegistrationInstructions_SendsToVerifiedAddress(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	// Mark the admin's email verified.
	require.NoError(t, env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email:         "admin@example.com",
		EmailVerified: true,
		ID:            env.adminID(t),
	}))

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)

	cmd := "leapmux worker --hub http://x --registration-key abc"
	_, err = env.mgmtClient.EmailRegistrationInstructions(context.Background(), authedReq(&leapmuxv1.EmailRegistrationInstructionsRequest{
		RegistrationKey: createResp.Msg.GetRegistrationKey(),
		Command:         cmd,
	}, token))
	require.NoError(t, err)

	last := env.mailer.last()
	require.NotNil(t, last)
	assert.Equal(t, "admin@example.com", last.To)
	assert.True(t, strings.Contains(last.Body, cmd), "email body should contain the rendered command")
}

func TestDeregisterWorker_AllowsManuallyRegisteredWorker(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	// Mint a key, then consume it via Register — the resulting worker
	// row has auto_registered=false.
	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.Msg.GetRegistrationKey())
	require.NoError(t, err)
	workerID := regResp.Msg.GetWorkerId()

	_, err = env.mgmtClient.DeregisterWorker(context.Background(), authedReq(&leapmuxv1.DeregisterWorkerRequest{
		WorkerId: workerID,
	}, token))
	require.NoError(t, err)

	worker, err := env.store.Workers().GetByIDIncludeDeleted(context.Background(), workerID)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, worker.Status)
}

// TestDeregisterWorker_RefusesAutoRegisteredWorker locks in the
// defense-in-depth guard for the solo launcher's bundled worker. The
// row is created via Server.RegisterWorker (auto_registered=true) and
// would just be re-created on next launch if deregistered, so the
// handler must refuse rather than producing a transient outage and a
// reappearing row.
func TestDeregisterWorker_RefusesAutoRegisteredWorker(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	workerID := "auto-worker-1"
	require.NoError(t, env.store.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       "auto-token-1",
		RegisteredBy:    env.adminID(t),
		PublicKey:       []byte{},
		MlkemPublicKey:  []byte{},
		SlhdsaPublicKey: []byte{},
		AutoRegistered:  true,
	}))

	_, err := env.mgmtClient.DeregisterWorker(context.Background(), authedReq(&leapmuxv1.DeregisterWorkerRequest{
		WorkerId: workerID,
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// Row still active — the precondition fired before any UPDATE.
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
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on Windows; named-pipe coverage lives in integration tests")
	}

	env := setupRegKeyEnv(t)

	// Bind the Connect handlers to a unix-socket listener wrapped in h2c
	// so the gRPC-over-h2c client used by `leapmux worker --hub unix:...`
	// can dial it the same way it would the production hub.
	// UniqueListenURL keeps the socket path under AF_UNIX's 104-byte
	// sun_path limit, which t.TempDir() blows past on macOS runners.
	socketURL := locallistentest.UniqueListenURL(t, "hub")
	ln, err := locallisten.Listen(socketURL)
	require.NoError(t, err)
	srv := &http.Server{
		Handler:           h2c.NewHandler(env.mux, &http2.Server{}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	t.Cleanup(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(ln) }()
	require.NoError(t, locallisten.WaitReady(context.Background(), socketURL))

	dial, err := locallisten.Dialer(socketURL)
	require.NoError(t, err)
	httpClient := &http.Client{Transport: locallisten.NewLocalH2CTransport(dial)}
	t.Cleanup(httpClient.CloseIdleConnections)

	connectorClient := leapmuxv1connect.NewWorkerConnectorServiceClient(httpClient, "http://localhost", connect.WithGRPC())
	authClient := leapmuxv1connect.NewAuthServiceClient(httpClient, "http://localhost")
	mgmtClient := leapmuxv1connect.NewWorkerManagementServiceClient(httpClient, "http://localhost")

	register := func(t *testing.T, key string) error {
		t.Helper()
		req := connect.NewRequest(&leapmuxv1.RegisterRequest{Version: "v"})
		if key != "" {
			req.Header().Set("Authorization", "Bearer "+key)
		}
		_, err := connectorClient.Register(context.Background(), req)
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
		loginResp, err := authClient.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
			Username: "admin", Password: "admin123",
		}))
		require.NoError(t, err)
		token := sessionFromCookie(t, loginResp.Header().Get("Set-Cookie"))

		createResp, err := mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
		require.NoError(t, err)
		key := createResp.Msg.GetRegistrationKey()

		err = register(t, key)
		require.NoError(t, err, "valid key over unix socket must register the worker")

		// Replay rejected: registration keys are single-use regardless
		// of transport.
		err = register(t, key)
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})
}
