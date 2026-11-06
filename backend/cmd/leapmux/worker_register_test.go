package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/config"
)

// registerEnv is a REAL connector service over a real store, plus the
// store handle the assertions read and the failure knobs a deregister
// test injects BETWEEN the command and the real handlers.
type registerEnv struct {
	url   string
	key   string
	store store.Store

	// Register and DeregisterSelf knobs: the middleware below applies
	// them before the real handler runs.
	registerCalls        atomic.Int32
	deregisterCalls      atomic.Int32
	refuseDeregisterWith connect.Code
	transportFailure     bool
}

// The connect routes the knobs gate.
const (
	registerPath   = "/leapmux.v1.WorkerConnectorService/Register"
	deregisterPath = "/leapmux.v1.WorkerConnectorService/DeregisterSelf"
)

// startRegisterEnv wires the REAL WorkerConnectorService -- the
// register command's contract is with the real handlers: the durable
// attempt, the consumed key, and the recovery protocol -- and mints one
// registration key.
func startRegisterEnv(t *testing.T) *registerEnv {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	wMgr := workermgr.New(service.NewWorkerReachAuthorizer(st))
	cMgr := channelmgr.New(0)
	pendingReqs := workermgr.NewPendingRequests(func() time.Duration { return 30 * time.Second })
	notif := notifier.New(st, wMgr, pendingReqs, func() time.Duration { return 30 * time.Second }, nil)
	connector := service.NewWorkerConnectorService(st, wMgr, cMgr, pendingReqs, notif, nil, sendq.NewMaxBytesPoolForTest(), quartz.NewReal())

	server := connect.NewServer()
	leapmuxv1connect.RegisterWorkerConnectorServiceHandler(server, connector)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	env := &registerEnv{store: st}
	gated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == registerPath {
			env.registerCalls.Add(1)
		}
		if r.URL.Path == deregisterPath {
			env.deregisterCalls.Add(1)
			if env.transportFailure {
				// A connection that dies mid-request is the transport
				// failure the credentials must survive.
				hj, ok := w.(http.Hijacker)
				if ok {
					if conn, _, hijackErr := hj.Hijack(); hijackErr == nil {
						_ = conn.Close()
						return
					}
				}
				return
			}
			if env.refuseDeregisterWith != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(int(httpStatusOf(env.refuseDeregisterWith)))
				_, _ = w.Write([]byte(`{"code":"` + env.refuseDeregisterWith.String() + `","message":"injected refusal"}`))
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
	httpServer := httptest.NewUnstartedServer(gated)
	httpServer.Config.Protocols = &http.Protocols{}
	httpServer.Config.Protocols.SetHTTP1(true)
	httpServer.Config.Protocols.SetUnencryptedHTTP2(true)
	httpServer.Start()
	t.Cleanup(httpServer.Close)

	admin, adminErr := st.Users().GetFirstAdmin(context.Background())
	require.NoError(t, adminErr)
	key := id.Generate()
	require.NoError(t, st.RegistrationKeys().Create(context.Background(), store.CreateRegistrationKeyParams{
		ID: key, CreatedBy: userid.MustNew(admin.ID), ExpiresAt: time.Now().Add(time.Hour),
	}))
	env.url, env.key = httpServer.URL, key
	return env
}

// httpStatusOf maps the refusal codes this test injects onto their
// connect HTTP spellings.
func httpStatusOf(code connect.Code) int {
	switch code {
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized
	default:
		return http.StatusForbidden
	}
}

// workerConfigOf loads the worker configuration of a data directory the
// way the command does.
func workerConfigOf(t *testing.T, dataDir string) *config.Config {
	t.Helper()
	cfg, _, err := config.Load([]string{"--data-dir", dataDir})
	require.NoError(t, err)
	return cfg
}

// The register command saves complete credentials against the endpoint
// it registered with, cleans the pending record, and refuses a second
// registration of an already-registered machine.
func TestWorkerRegisterSavesCredentials(t *testing.T) {
	env := startRegisterEnv(t)
	dataDir := t.TempDir()

	args := []string{"--hub", env.url, "--registration-key", env.key, "--data-dir", dataDir}
	require.NoError(t, runWorkerRegister(args))

	cfg := workerConfigOf(t, dataDir)
	state, err := cfg.LoadState()
	require.NoError(t, err)
	require.True(t, state.Registered(), "register leaves complete credentials")
	assert.Equal(t, env.url, state.HubURL, "the saved endpoint is the one registered against")
	assert.NoFileExists(t, cfg.PendingPath(), "the pending record is cleaned up")
	assert.True(t, state.HasKeypair(), "the pinned keypair travels into the credentials")

	// The machine is registered; a second register is refused.
	require.Error(t, runWorkerRegister(args))
}

// A missing key or a missing endpoint is a refusal before anything is
// written.
func TestWorkerRegisterRequiresItsInputs(t *testing.T) {
	env := startRegisterEnv(t)
	hubURL, key := env.url, env.key
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no key", []string{"--hub", hubURL, "--data-dir", t.TempDir()}, "--registration-key is required"},
		{"no hub", []string{"--registration-key", key, "--data-dir", t.TempDir()}, "--hub is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runWorkerRegister(tc.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// A durable pending record left by a crashed run is REUSED, never
// silently replaced: rerunning the same command recovers the committed
// attempt, and the same worker and token come back with exactly one
// row.
func TestWorkerRegisterRecoversAfterAnInterruptedRun(t *testing.T) {
	env := startRegisterEnv(t)
	hubURL, key := env.url, env.key
	dataDir := t.TempDir()
	cfg := workerConfigOf(t, dataDir)

	// An interrupted run left a pending record but no credentials.
	pending := &config.PendingRegistration{
		AttemptID: "a9a0425886d148f68a8f94716ed47a78",
		HubURL:    hubURL,
		RegKey:    key,
	}
	_, err := pending.GenerateKeypair()
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())
	require.NoError(t, cfg.SavePendingRegistration(pending))

	require.NoError(t, runWorkerRegister([]string{"--hub", hubURL, "--registration-key", key, "--data-dir", dataDir}))

	state, err := cfg.LoadState()
	require.NoError(t, err)
	require.True(t, state.Registered())
	assert.NoFileExists(t, cfg.PendingPath())

	// The hub holds exactly one worker, pinned to the REUSED attempt.
	ctx := context.Background()
	row, err := env.store.Workers().GetByRegistrationAttemptID(ctx, pending.AttemptID)
	require.NoError(t, err)
	assert.Equal(t, state.WorkerID, row.ID)
	assert.Equal(t, state.AuthToken, row.AuthToken, "the original token came back")
}

// A pending record from a DIFFERENT hub or key is never silently
// replaced.
func TestWorkerRegisterRefusesAConflictingPendingRecord(t *testing.T) {
	env := startRegisterEnv(t)
	hubURL, key := env.url, env.key
	dataDir := t.TempDir()
	cfg := workerConfigOf(t, dataDir)

	pending := &config.PendingRegistration{
		AttemptID: "b9a0425886d148f68a8f94716ed47a78",
		HubURL:    "http://127.0.0.1:1",
		RegKey:    "an-earlier-key",
	}
	_, err := pending.GenerateKeypair()
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())
	require.NoError(t, cfg.SavePendingRegistration(pending))

	err = runWorkerRegister([]string{"--hub", hubURL, "--registration-key", key, "--data-dir", dataDir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pending registration")
	assert.FileExists(t, cfg.PendingPath(), "the conflicting record stays")
}

// A pending-publication failure causes ZERO registration requests: the
// durable record exists before the first network call or not at all,
// and a hub that is never asked never consumes the key.
func TestWorkerRegisterMakesNoRequestWhenPublicationFails(t *testing.T) {
	env := startRegisterEnv(t)
	dataDir := t.TempDir()

	// The data directory refuses the pending write.
	require.NoError(t, os.MkdirAll(dataDir, 0o700))
	require.NoError(t, os.Chmod(dataDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dataDir, 0o700) })

	// cfg.Validate inside the command would fail on the unwritable
	// directory only at the pending write, so the command reaches the
	// publication and fails there.
	err := runWorkerRegister([]string{"--hub", env.url, "--registration-key", env.key, "--data-dir", dataDir})
	require.Error(t, err)
	assert.Zero(t, env.registerCalls.Load(), "a failed pending publication makes zero network requests")
}
