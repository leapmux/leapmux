package service_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"connectrpc.com/connect"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/testutil"
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
	return setupRegKeyEnvWithCfg(t, testConfigWithSMTP())
}

// setupRegKeyEnvWithCfg builds a registration-key test env with a
// caller-supplied config. Use this when the test cares about
// email_enabled gating; setupRegKeyEnv (the default) configures SMTP so
// EmailRegistrationInstructions can run.
func setupRegKeyEnvWithCfg(t *testing.T, cfg *config.Config) *regKeyEnv {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	wMgr := workermgr.New(service.NewWorkerReachAuthorizer(st))
	cMgr := channelmgr.New(0)
	pendingReqs := workermgr.NewPendingRequests(func() time.Duration { return cfg.APITimeout })

	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)

	mailer := &recordingSender{}
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, cfg, auth.NewCredentialLifecycleEffects(sc, nil, nil)))
	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(authPath, authHandler)

	connectorSvc := service.NewWorkerConnectorService(st, wMgr, cMgr, service.NewHubEventBroadcaster(cMgr), pendingReqs, nil, nil, sendq.NewMaxBytesPoolForTest())
	// Both halves of max_workers_per_user, wired from one value exactly as
	// hub/server.go does: the service caps registered ROWS, the registry caps
	// LIVE CONNECTIONS, and a fixture that wired only the first would let a test
	// pass against the row cap alone -- which is the bug these two exist to
	// close between them.
	connectorSvc.SetMaxWorkersPerUser(cfg.MaxWorkersPerUser)
	wMgr.SetMaxWorkersPerUser(cfg.MaxWorkersPerUser)
	connectorPath, connectorHandler := leapmuxv1connect.NewWorkerConnectorServiceHandler(connectorSvc, opts)
	mux.Handle(connectorPath, connectorHandler)

	notif := notifier.New(st, wMgr, pendingReqs, cfg)
	mgmtSvc := service.NewWorkerManagementService(st, wMgr, service.NewHubEventBroadcaster(cMgr), notif, mailer, mail.Renderer{}, cfg, nil)
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

// serveOverUnixSocket binds the env's mux to a unix-socket listener wrapped in
// h2c and returns an HTTP client that dials it. Both are torn down with the test.
//
// Connect is a bidi stream, which httptest's HTTP/1 server (e.server) cannot
// serve, so every test that opens one needs this instead -- and it is also the
// real transport `leapmux worker --hub unix:...` uses, so a registration test
// gets the desktop/solo path for free. `name` only has to be unique within the
// process: UniqueListenURL keeps the socket path under AF_UNIX's 104-byte
// sun_path limit, which t.TempDir() blows past on macOS runners.
func (e *regKeyEnv) serveOverUnixSocket(t *testing.T, name string) *http.Client {
	t.Helper()

	socketURL := locallistentest.UniqueListenURL(t, name)
	ln, err := locallisten.Listen(socketURL)
	require.NoError(t, err)
	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: e.mux, ReadHeaderTimeout: 5 * time.Second, Protocols: protocols}
	t.Cleanup(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(ln) }()
	require.NoError(t, locallisten.WaitReady(context.Background(), socketURL))

	dial, err := locallisten.Dialer(socketURL)
	require.NoError(t, err)
	httpClient := &http.Client{Transport: locallisten.NewLocalH2CTransport(dial)}
	t.Cleanup(httpClient.CloseIdleConnections)
	return httpClient
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
	t.Parallel()

	env := setupRegKeyEnv(t)

	_, err := env.mgmtClient.CreateRegistrationKey(context.Background(), connect.NewRequest(&leapmuxv1.CreateRegistrationKeyRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestCreateRegistrationKey_ReturnsKeyAndExpiry(t *testing.T) {
	t.Parallel()

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

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.Msg.GetRegistrationKey()

	regResp, err := env.registerWithKey(t, key)
	require.NoError(t, err)
	assert.NotEmpty(t, regResp.Msg.GetWorkerId())
	assert.NotEmpty(t, regResp.Msg.GetAuthToken())

	// A second Register with the same key must fail — the key was
	// soft-deleted as part of the consume txn.
	_, err = env.registerWithKey(t, key)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	// Worker row exists and has registered_by = key.created_by. The response does
	// not carry the owner -- the Hub delivers it on connect (WorkerIdentity) instead
	// of at registration, so the DB row is the only place to assert it.
	w, err := env.store.Workers().GetByID(context.Background(), regResp.Msg.GetWorkerId())
	require.NoError(t, err)
	assert.NotEmpty(t, w.RegisteredBy, "the worker row must record the key creator as its owner")
}

// The worker pool's count term, end to end.
//
// A Worker's Connect stream is a member of the worker pool, guaranteed the same
// working set the pool may not reclaim -- but it takes no lease, so the per-user
// CONNECTION cap cannot see it, and registration keys have no quota. Without
// this bound, N keys mint N members and the floors the worker pool promises sum
// without limit, on the pool whose eviction costs the most.
func TestRegister_RefusesBeyondThePerUserWorkerCap(t *testing.T) {
	t.Parallel()

	cfg := testConfigWithSMTP()
	cfg.MaxWorkersPerUser = 2
	env := setupRegKeyEnvWithCfg(t, cfg)
	token := env.login(t, "admin", "admin123")

	register := func(t *testing.T) error {
		t.Helper()
		createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(),
			authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
		require.NoError(t, err)
		_, err = env.registerWithKey(t, createResp.Msg.GetRegistrationKey())
		return err
	}

	require.NoError(t, register(t), "the first worker must register")
	require.NoError(t, register(t), "and so must the second, up to the cap")

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

	// At LEAST one more, not exactly one: the counter is process-global and
	// every test in this binary shares it, so an exact delta would be a race
	// with any other parallel test that trips the same cap.
	assert.GreaterOrEqual(t, workerAdmissionsRefused(metrics.WorkerStageRegister), before+1,
		"a refused registration must be visible from outside")
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

	cfg := testConfigWithSMTP()
	cfg.MaxWorkersPerUser = 1
	env := setupRegKeyEnvWithCfg(t, cfg)
	token := env.login(t, "admin", "admin123")

	register := func(t *testing.T) (string, error) {
		t.Helper()
		createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(),
			authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
		require.NoError(t, err)
		resp, err := env.registerWithKey(t, createResp.Msg.GetRegistrationKey())
		if err != nil {
			return "", err
		}
		return resp.Msg.GetWorkerId(), nil
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
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.Msg.GetRegistrationKey()

	// Force-expire by soft-deleting the row directly via the store.
	_, err = env.store.RegistrationKeys().SoftDelete(context.Background(), store.SoftDeleteRegistrationKeyParams{
		ID:        key,
		CreatedBy: userid.MustNew(env.adminID(t)),
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
	t.Parallel()

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
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.Msg.GetRegistrationKey()

	// Push the key inside the extend window: 90s remaining < 2min buffer.
	rows, err := env.store.RegistrationKeys().Extend(context.Background(), store.ExtendRegistrationKeyParams{
		ID:        key,
		CreatedBy: userid.MustNew(env.adminID(t)),
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
	t.Parallel()

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
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	key := createResp.Msg.GetRegistrationKey()

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
	t.Parallel()

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
	t.Parallel()

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

// TestEmailRegistrationInstructions_RejectsWhenSMTPUnconfigured locks
// in the defense-in-depth precondition: even if a client bypasses the
// frontend gate (which hides the button when email_enabled=false), the
// RPC refuses to call the disabled mail backend.
func TestEmailRegistrationInstructions_RejectsWhenSMTPUnconfigured(t *testing.T) {
	t.Parallel()

	env := setupRegKeyEnvWithCfg(t, testConfig()) // no SmtpHost set
	token := env.login(t, "admin", "admin123")

	// Mark the admin's email verified — otherwise the
	// verified-email check fires first and we couldn't tell which
	// precondition rejected us.
	require.NoError(t, env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email:         "admin@example.com",
		EmailVerified: true,
		ID:            env.adminID(t),
	}))

	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(), authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)

	_, err = env.mgmtClient.EmailRegistrationInstructions(context.Background(), authedReq(&leapmuxv1.EmailRegistrationInstructionsRequest{
		RegistrationKey: createResp.Msg.GetRegistrationKey(),
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
	t.Parallel()

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
	t.Parallel()

	env := setupRegKeyEnv(t)
	token := env.login(t, "admin", "admin123")

	workerID := "auto-worker-1"
	require.NoError(t, env.store.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       "auto-token-1",
		RegisteredBy:    userid.MustNew(env.adminID(t)),
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
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on Windows; named-pipe coverage lives in integration tests")
	}

	env := setupRegKeyEnv(t)

	// The gRPC-over-h2c client used by `leapmux worker --hub unix:...` dials the
	// hub the same way it would in production.
	httpClient := env.serveOverUnixSocket(t, "hub")
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

// The Hub must send WorkerIdentity as the FIRST message on every Connect stream.
//
// It is the worker's only source for its own owner: requireWorkerOwner gates every
// machine-scoped family (file, git, sysinfo, tunnel) on it, and the worker keeps no
// local copy -- a cached copy is what previously went missing and left the worker
// permanently denying its own legitimate user. Identity must also PRECEDE the
// connection being published to the worker manager, so it cannot be overtaken by a
// frontend-driven ChannelOpen on the same stream; asserting it is the first frame the
// worker receives is how that ordering stays true.
func TestConnect_SendsWorkerIdentityFirst(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on Windows")
	}
	env := setupRegKeyEnv(t)

	// Register a worker so it has an auth token and a recorded owner.
	token := env.login(t, "admin", "admin123")
	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(),
		authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.Msg.GetRegistrationKey())
	require.NoError(t, err)

	worker, err := env.store.Workers().GetByID(context.Background(), regResp.Msg.GetWorkerId())
	require.NoError(t, err)
	require.NotEmpty(t, worker.RegisteredBy)

	connectorClient := leapmuxv1connect.NewWorkerConnectorServiceClient(
		env.serveOverUnixSocket(t, "hub-identity"), "http://localhost", connect.WithGRPC())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := connectorClient.Connect(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+worker.AuthToken)
	// ConnectRPC only sends headers on the first Send, so the worker speaks first.
	require.NoError(t, stream.Send(&leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
	}))

	first, err := stream.Receive()
	require.NoError(t, err)
	require.NotNil(t, first.GetWorkerIdentity(),
		"the FIRST message on the stream must be WorkerIdentity, not the heartbeat echo")
	assert.Equal(t, worker.RegisteredBy, first.GetWorkerIdentity().GetRegisteredBy(),
		"the Hub must name the worker's recorded owner")
	require.NoError(t, stream.CloseRequest())
}

// A Connect handler can clear the shutdown interceptor -- a one-shot check --
// microseconds before the Hub starts fencing, then spend a store round trip and
// the greeting write getting to Register. The registry refuses it, and what the
// worker sees has to be Unavailable rather than Internal: the manager-level
// tests pin the refusal, but only this one pins the status code a real worker
// observes, which is the part a later edit folding the branch back into the
// generic CodeInternal return would silently change.
func TestConnect_FencedRegistryRefusesWithUnavailable(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on Windows")
	}
	env := setupRegKeyEnv(t)

	token := env.login(t, "admin", "admin123")
	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(),
		authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.Msg.GetRegistrationKey())
	require.NoError(t, err)

	worker, err := env.store.Workers().GetByID(context.Background(), regResp.Msg.GetWorkerId())
	require.NoError(t, err)

	connectorClient := leapmuxv1connect.NewWorkerConnectorServiceClient(
		env.serveOverUnixSocket(t, "hub-fenced"), "http://localhost", connect.WithGRPC())

	// The Hub has begun shutting down. The interceptor is NOT installed on this
	// fixture, which is exactly the window under test: the request is already
	// past it.
	env.wMgr.FenceAll()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := connectorClient.Connect(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+worker.AuthToken)
	// ConnectRPC documents that when the server returns an error, the client's
	// first Send may wrap io.EOF and Receive carries the real status. Fencing
	// is the cheapest refuse path (no greeting enqueue), so on a fast runner
	// the Hub often closes before the heartbeat envelope finishes writing —
	// requiring Send to succeed flakes as "unknown: write envelope: EOF".
	err = stream.Send(&leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
	})
	if err != nil {
		require.ErrorIs(t, err, io.EOF,
			"server may close before the first Send finishes; got %v", err)
	}

	_, err = stream.Receive()
	require.Error(t, err, "a fenced registry must refuse the connection, not hold the stream open")
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err),
		"a Hub that is going away is Unavailable; Internal would read as a worker-side fault")
	assert.Nil(t, env.wMgr.ConnForTrustedPath(worker.ID),
		"the refused connection must never be published")
	require.NoError(t, stream.CloseRequest())
}

// The regression this cap exists to close, end to end.
//
// max_workers_per_user is meant to bound how many of one account's Workers sit in
// the worker send-queue pool, each holding a floor the pool may not reclaim.
// Counting ROWS cannot do that. The pool member is created per Connect stream,
// behind an auth-token lookup that -- correctly -- still admits a DEREGISTERING
// worker, because that stream is how the worker is told to tear itself down. The
// row count stops seeing it the moment the row deregisters, and nothing
// server-side ends the stream: SendDeregister only marks and notifies,
// MarkDeleted waits for the worker's own ack, and heartbeats defeat the idle
// timer. So register / deregister / register cycles added a member every time
// while never once exceeding the cap.
func TestConnect_RefusesWhenTheAccountIsAtItsLiveWorkerCap(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on Windows")
	}

	cfg := testConfigWithSMTP()
	cfg.MaxWorkersPerUser = 1
	env := setupRegKeyEnvWithCfg(t, cfg)
	token := env.login(t, "admin", "admin123")

	register := func(t *testing.T) *leapmuxv1.RegisterResponse {
		t.Helper()
		createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(),
			authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
		require.NoError(t, err)
		resp, err := env.registerWithKey(t, createResp.Msg.GetRegistrationKey())
		require.NoError(t, err)
		return resp.Msg
	}

	connectorClient := leapmuxv1connect.NewWorkerConnectorServiceClient(
		env.serveOverUnixSocket(t, "hub-worker-cap"), "http://localhost", connect.WithGRPC())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The mode is declared on EVERY heartbeat, as a real worker declares it: the
	// Hub caches the first one and terminates the stream if a later heartbeat
	// arrives UNSPECIFIED, so a bare repeat would end this connection for a
	// reason that has nothing to do with the cap.
	heartbeat := &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{
			EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
		}},
	}
	type workerStream = connect.BidiStreamForClient[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse]

	// openStream starts a Connect stream and returns the Hub's first frame. The
	// worker speaks first because ConnectRPC only sends headers on the first Send.
	// When the Hub refuses immediately, Send may wrap io.EOF; Receive carries
	// the status (see BidiStreamForClient.Send docs).
	openStream := func(t *testing.T, authToken string) (*workerStream, *leapmuxv1.ConnectResponse, error) {
		t.Helper()
		stream := connectorClient.Connect(ctx)
		stream.RequestHeader().Set("Authorization", "Bearer "+authToken)
		if err := stream.Send(heartbeat); err != nil && !errors.Is(err, io.EOF) {
			return stream, nil, err
		}
		first, err := stream.Receive()
		return stream, first, err
	}

	// awaitHeartbeat reads until the Hub echoes one. A real worker heartbeats
	// through its own deregistration, and that is what defeats the 10s
	// receive-idle timer -- the only thing that would otherwise end a
	// deregistering worker's stream. Waiting for the ECHO rather than just
	// sending is what makes this a seam instead of a hope: the Hub replies from
	// the same handler turn that resets the timer, so the test never depends on
	// how long the steps after it take.
	awaitHeartbeat := func(t *testing.T, stream *workerStream) {
		t.Helper()
		for {
			frame, err := stream.Receive()
			require.NoError(t, err)
			if frame.GetHeartbeat() != nil {
				return
			}
		}
	}

	first := register(t)
	firstStream, greeting, err := openStream(t, first.GetAuthToken())
	require.NoError(t, err)
	require.NotNil(t, greeting.GetWorkerIdentity(),
		"the greeting is enqueued before the conn is published, so receiving it proves membership")
	awaitHeartbeat(t, firstStream)

	live := env.wMgr.ConnForTrustedPath(first.GetWorkerId())
	require.NotNil(t, live)
	assert.Equal(t, greeting.GetWorkerIdentity().GetRegisteredBy(), live.Owner(),
		"the connection must be counted against the account that registered the worker")

	// Deregister it. This is the row transition DeregisterWorker performs, driven
	// through the store because its notifier round trip is not what is under
	// test: SendOrQueue parks in SendAndWait until the worker acks or api_timeout
	// expires, and a bare test stream acks nothing. The row state is what the row
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

	// Nothing server-side ends that stream, and the worker's own heartbeats keep
	// the idle timer from doing it either -- that is the whole hazard.
	require.NoError(t, firstStream.Send(heartbeat))
	awaitHeartbeat(t, firstStream)
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
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err),
		"a cap is exhausted resources, not a bad request: the worker's credentials are fine")
	assert.GreaterOrEqual(t, workerAdmissionsRefused(metrics.WorkerStageConnect), refusalsBefore+1,
		"a Worker refused at Connect must be counted, under the connect stage")
	assert.Contains(t, err.Error(), "max_workers_per_user",
		"the operator-facing key has to be in the message, or nobody knows what to raise")
	assert.Nil(t, env.wMgr.ConnForTrustedPath(second.GetWorkerId()),
		"the refused connection must never be published")
	require.NoError(t, secondStream.CloseRequest())

	// The disconnect -- not the deregistration -- is what gives the slot back.
	require.NoError(t, firstStream.CloseRequest())
	testutil.AssertEventually(t, func() bool {
		return !env.wMgr.OnlineForTrustedPath(first.GetWorkerId())
	})

	thirdStream, greeting, err := openStream(t, second.GetAuthToken())
	require.NoError(t, err, "a freed slot must be usable")
	assert.NotNil(t, greeting.GetWorkerIdentity())
	require.NoError(t, thirdStream.CloseRequest())
}
