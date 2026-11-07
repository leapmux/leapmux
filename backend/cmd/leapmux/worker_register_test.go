package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/config"
)

// registerEnv is a REAL connector service over a real store, plus the
// store handle the assertions read and the failure controls tests apply
// around the real handlers.
type registerEnv struct {
	url                   string
	key                   string
	store                 store.Store
	server                *httptest.Server
	dropRegisterResponse  atomic.Bool
	registrationCommitted chan struct{}
	releaseResponse       chan struct{}
	releaseOnce           sync.Once
	holdDeregister        atomic.Bool
	deregisterReceived    chan struct{}

	// Middleware can refuse a request or hold its response after the real
	// registration transaction commits.
	registerCalls        atomic.Int32
	deregisterCalls      atomic.Int32
	refuseDeregisterWith connect.Code
	transportFailure     bool
}

// These routes select the protocol operations the fixture controls.
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
	env := serveRegisterEnv(t, st, "")
	env.key = mintRegistrationFixtureKey(t, st)
	return env
}

// serveRegisterEnv creates fresh protocol state over the supplied store. An
// address preserves the Hub endpoint across an actual listener/store restart.
func serveRegisterEnv(t *testing.T, st store.Store, address string) *registerEnv {
	t.Helper()

	wMgr := workermgr.New(service.NewWorkerReachAuthorizer(st))
	cMgr := channelmgr.New(0)
	pendingReqs := workermgr.NewPendingRequests(func() time.Duration { return 30 * time.Second })
	notif := notifier.New(st, wMgr, pendingReqs, func() time.Duration { return 30 * time.Second }, nil)
	connector := service.NewWorkerConnectorService(st, wMgr, cMgr, pendingReqs, notif, nil, sendq.NewMaxBytesPoolForTest(), quartz.NewReal())

	server := connect.NewServer()
	leapmuxv1connect.RegisterWorkerConnectorServiceHandler(server, connector)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	env := &registerEnv{store: st, registrationCommitted: make(chan struct{}, 1), releaseResponse: make(chan struct{}), deregisterReceived: make(chan struct{}, 1)}
	gated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == registerPath {
			env.registerCalls.Add(1)
			if env.dropRegisterResponse.Load() {
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, r)
				select {
				case env.registrationCommitted <- struct{}{}:
				default:
				}
				<-env.releaseResponse
				return
			}
		}
		if r.URL.Path == deregisterPath {
			env.deregisterCalls.Add(1)
			if env.holdDeregister.Load() {
				env.deregisterReceived <- struct{}{}
				select {
				case <-r.Context().Done():
				case <-env.releaseResponse:
				}
				return
			}
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
	if address != "" {
		require.NoError(t, httpServer.Listener.Close())
		listener, err := net.Listen("tcp", address)
		require.NoError(t, err)
		httpServer.Listener = listener
	}
	httpServer.Config.Protocols = &http.Protocols{}
	httpServer.Config.Protocols.SetHTTP1(true)
	httpServer.Config.Protocols.SetUnencryptedHTTP2(true)
	httpServer.Start()
	env.url, env.server = httpServer.URL, httpServer
	t.Cleanup(env.stop)
	return env
}

func (e *registerEnv) stop() {
	e.releaseOnce.Do(func() { close(e.releaseResponse) })
	e.server.Close()
}

func mintRegistrationFixtureKey(t *testing.T, st store.Store) string {
	t.Helper()
	admin, adminErr := st.Users().GetFirstAdmin(context.Background())
	require.NoError(t, adminErr)
	key := id.Generate()
	require.NoError(t, st.RegistrationKeys().Create(context.Background(), store.CreateRegistrationKeyParams{
		ID: key, CreatedBy: userid.MustNew(admin.ID), ExpiresAt: time.Now().Add(time.Hour),
	}))
	return key
}

func TestWorkerRegisterRecoversAfterClientAndHubRestart(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	path := filepath.Join(t.TempDir(), "hub.db")
	st, err := sqlite.OpenTestable(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	hubtestutil.CreateTestAdmin(t, st)
	env := serveRegisterEnv(t, st, "")
	key := mintRegistrationFixtureKey(t, st)
	env.dropRegisterResponse.Store(true)
	dataDir := t.TempDir()
	cfg := workerConfigOf(t, dataDir)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcessLifecycleCLI$", "--",
		"worker", "register", "--hub", env.url, "--registration-key", key, "--data-dir", dataDir)
	command.Env = lifecycleCLIEnvironment(t.TempDir())
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	require.NoError(t, command.Start())
	done := make(chan struct{})
	var commandErr error
	go func() {
		commandErr = command.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		<-done
		if t.Failed() {
			t.Log(output.String())
		}
	})
	select {
	case <-env.registrationCommitted:
	case <-done:
		t.Fatalf("the registering client ended before its commit: %v\n%s", commandErr, output.String())
	case <-ctx.Done():
		t.Fatal("registration never reached the committed-response gate")
	}
	require.NoError(t, command.Process.Kill())
	select {
	case <-done:
		require.Error(t, commandErr)
	case <-ctx.Done():
		t.Fatal("the interrupted registering client did not exit")
	}
	pending, err := cfg.LoadPendingRegistration()
	require.NoError(t, err)
	require.NotNil(t, pending, "the child must publish recovery material before registering")
	assert.NoFileExists(t, cfg.StatePath(), "the withheld response must not reach local credentials")
	committed, err := st.Workers().GetByRegistrationAttemptID(ctx, pending.AttemptID)
	require.NoError(t, err)
	address, hubURL := env.server.Listener.Addr().String(), env.url
	env.stop()
	require.NoError(t, st.Close())
	reopened, err := sqlite.OpenTestable(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := serveRegisterEnv(t, reopened, address)
	require.Equal(t, hubURL, restarted.url)
	result := invokeLifecycleCLI(t, t.TempDir(), "worker", "register", "--hub", hubURL,
		"--registration-key", key, "--data-dir", dataDir)
	require.Equal(t, 0, result.exitCode, result.stdout+result.stderr)
	state, err := cfg.LoadState()
	require.NoError(t, err)
	require.True(t, state.Registered())
	assert.Equal(t, committed.ID, state.WorkerID)
	assert.Equal(t, committed.AuthToken, state.AuthToken)
	assert.Equal(t, pending.KeyMaterial, state.KeyMaterial)
	assert.NoFileExists(t, cfg.PendingPath())
	assert.NoFileExists(t, filepath.Join(dataDir, "worker.db"), "register must exit without running the worker")
	page, err := reopened.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
		RegisteredBy: userid.MustNew(committed.RegisteredBy), PageParams: store.PageParams{Limit: 10},
	})
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	assert.Equal(t, int32(1), env.registerCalls.Load())
	assert.Equal(t, int32(1), restarted.registerCalls.Load(), "the new client must recover the one committed attempt")
}

func TestWorkerRegisterFinishesInterruptedCleanup(t *testing.T) {
	for _, pendingPresent := range []bool{false, true} {
		t.Run(map[bool]string{false: "temporary copies only", true: "pending destination and copies"}[pendingPresent], func(t *testing.T) {
			env, cfg := registerThroughEnv(t)
			state, err := cfg.LoadState()
			require.NoError(t, err)
			if pendingPresent {
				require.NoError(t, cfg.SavePendingRegistration(&config.PendingRegistration{
					AttemptID: "a9a0425886d148f68a8f94716ed47a78", HubURL: env.url, RegKey: env.key, KeyMaterial: state.KeyMaterial,
				}))
			}
			copies := []string{cfg.PendingPath() + ".tmp-a", cfg.PendingPath() + ".tmp-b"}
			for _, path := range copies {
				require.NoError(t, os.WriteFile(path, []byte("private interrupted copy"), 0o600))
			}
			calls := env.registerCalls.Load()
			require.NoError(t, runWorkerRegister([]string{"--hub", env.url, "--registration-key", env.key, "--data-dir", cfg.DataDir}))
			assert.Equal(t, calls, env.registerCalls.Load(), "cleanup must make no registration request")
			assert.NoFileExists(t, cfg.PendingPath())
			for _, path := range copies {
				assert.NoFileExists(t, path)
			}
			current, err := cfg.LoadState()
			require.NoError(t, err)
			assert.Equal(t, state, current)
		})
	}
}

func TestWorkerRegisterReportsMalformedPendingData(t *testing.T) {
	env, cfg := registerThroughEnv(t)
	data := []byte("{invalid pending JSON")
	require.NoError(t, os.WriteFile(cfg.PendingPath(), data, 0o600))
	calls := env.registerCalls.Load()
	err := runWorkerRegister([]string{"--hub", env.url, "--registration-key", env.key, "--data-dir", cfg.DataDir})
	require.ErrorContains(t, err, "parse the pending registration")
	assert.Equal(t, calls, env.registerCalls.Load())
	preserved, err := os.ReadFile(cfg.PendingPath())
	require.NoError(t, err)
	assert.Equal(t, data, preserved)
}

func TestWorkerRegistrationVersionQueriesHaveNoSideEffects(t *testing.T) {
	for _, args := range [][]string{
		{"register", "--version"},
		{"register", "--hub", "http://127.0.0.1:1", "--registration-key", "probe", "--version"},
		{"deregister", "--version"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "must-not-exist")
			command := append([]string{"worker"}, args...)
			command = append(command, "--data-dir", dataDir)
			result := invokeLifecycleCLI(t, t.TempDir(), command...)
			require.Equal(t, 0, result.exitCode, result.stderr)
			assert.NotEmpty(t, result.stdout)
			assert.NotContains(t, result.stderr, "panic:")
			assert.NoDirExists(t, dataDir)
		})
	}
}

func TestWorkerRegisterTimeoutValidation(t *testing.T) {
	for _, value := range []string{"0", "-1s", "999999999999999999999h"} {
		t.Run(value, func(t *testing.T) {
			env := startRegisterEnv(t)
			dataDir := filepath.Join(t.TempDir(), "must-not-exist")
			err := runWorkerRegister([]string{"--hub", env.url, "--registration-key", env.key, "--data-dir", dataDir, "--timeout", value})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "timeout")
			assert.NotContains(t, err.Error(), "flag provided but not defined")
			assert.NoDirExists(t, dataDir)
			assert.Zero(t, env.registerCalls.Load())
		})
	}
	t.Run("tiny positive timeout keeps pending recovery material", func(t *testing.T) {
		env := startRegisterEnv(t)
		dataDir := t.TempDir()
		err := runWorkerRegister([]string{"--hub", env.url, "--registration-key", env.key, "--data-dir", dataDir, "--timeout=1ns"})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		pending, err := workerConfigOf(t, dataDir).LoadPendingRegistration()
		require.NoError(t, err)
		require.NotNil(t, pending)
		assert.Zero(t, env.registerCalls.Load())
	})
	t.Run("a large representable timeout accepts a successful registration", func(t *testing.T) {
		env := startRegisterEnv(t)
		dataDir := t.TempDir()
		require.NoError(t, runWorkerRegister([]string{"--hub", env.url, "--registration-key", env.key, "--data-dir", dataDir, "--timeout=2500000h"}))
		state, err := workerConfigOf(t, dataDir).LoadState()
		require.NoError(t, err)
		require.True(t, state.Registered())
	})
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
