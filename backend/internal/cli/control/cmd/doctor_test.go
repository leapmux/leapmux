package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/util/version"
)

type doctorTestHub struct {
	leapmuxv1connect.UnimplementedAuthServiceHandler
	leapmuxv1connect.UnimplementedAdminNetworkServiceHandler
	leapmuxv1connect.UnimplementedSystemServiceHandler
	leapmuxv1connect.UnimplementedWorkerManagementServiceHandler

	isAdmin          bool
	authErr          error
	listenResp       *leapmuxv1.GetListenStatusResponse
	listenErr        error
	listenStatusHits atomic.Int32
	buildInfo        *leapmuxv1.BuildInfo
	workers          []*leapmuxv1.Worker

	onGetCurrentUser  func(ctx context.Context)
	onGetListenStatus func(ctx context.Context)
	onGetSystemInfo   func(ctx context.Context)
	onListWorkers     func(ctx context.Context)
}

func (h *doctorTestHub) GetCurrentUser(ctx context.Context, _ *leapmuxv1.GetCurrentUserRequest) (*leapmuxv1.GetCurrentUserResponse, error) {
	if h.onGetCurrentUser != nil {
		h.onGetCurrentUser(ctx)
	}
	if h.authErr != nil {
		return nil, h.authErr
	}
	return &leapmuxv1.GetCurrentUserResponse{
		SessionReport: &leapmuxv1.SessionReport{
			User: &leapmuxv1.User{Id: "u-admin", Username: "admin", IsAdmin: h.isAdmin},
		},
	}, nil
}

func (h *doctorTestHub) GetListenStatus(ctx context.Context, _ *leapmuxv1.GetListenStatusRequest) (*leapmuxv1.GetListenStatusResponse, error) {
	h.listenStatusHits.Add(1)
	if h.onGetListenStatus != nil {
		h.onGetListenStatus(ctx)
	}
	if h.listenErr != nil {
		return nil, h.listenErr
	}
	if h.listenResp != nil {
		return h.listenResp, nil
	}
	return &leapmuxv1.GetListenStatusResponse{}, nil
}

func (h *doctorTestHub) GetSystemInfo(ctx context.Context, _ *leapmuxv1.GetSystemInfoRequest) (*leapmuxv1.GetSystemInfoResponse, error) {
	if h.onGetSystemInfo != nil {
		h.onGetSystemInfo(ctx)
	}
	b := h.buildInfo
	if b == nil {
		b = version.BuildInfoProto()
	}
	return &leapmuxv1.GetSystemInfoResponse{Build: b}, nil
}

func (h *doctorTestHub) ListWorkers(ctx context.Context, _ *leapmuxv1.ListWorkersRequest) (*leapmuxv1.ListWorkersResponse, error) {
	if h.onListWorkers != nil {
		h.onListWorkers(ctx)
	}
	return &leapmuxv1.ListWorkersResponse{Workers: h.workers}, nil
}

func startDoctorHubServer(t *testing.T, h *doctorTestHub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	cs := connect.NewServer()
	leapmuxv1connect.RegisterAuthServiceHandler(cs, h)
	leapmuxv1connect.RegisterAdminNetworkServiceHandler(cs, h)
	leapmuxv1connect.RegisterSystemServiceHandler(cs, h)
	leapmuxv1connect.RegisterWorkerManagementServiceHandler(cs, h)
	connecthttp.Mount(mux, cs, connecthttp.WithReadMaxBytes(0))
	srv := hubtransporttest.NewServer(t, mux)
	t.Cleanup(srv.Close)
	return srv
}

func hasDoctorCheck(t *testing.T, data map[string]any, name string) bool {
	t.Helper()
	checks, ok := data["checks"].([]any)
	require.True(t, ok, "checks must be an array")
	for _, raw := range checks {
		chk, ok := raw.(map[string]any)
		require.True(t, ok)
		if chk["name"] == name {
			return true
		}
	}
	return false
}

func doctorCheckByName(t *testing.T, data map[string]any, name string) map[string]any {
	t.Helper()
	checks, ok := data["checks"].([]any)
	require.True(t, ok, "checks must be an array")
	for _, raw := range checks {
		chk, ok := raw.(map[string]any)
		require.True(t, ok)
		if chk["name"] == name {
			return chk
		}
	}
	t.Fatalf("doctor report has no %q check: %v", name, checks)
	return nil
}

func TestDoctor_AdminNetworkCallsGetListenStatusAndReportsBindErrors(t *testing.T) {
	t.Run("all listeners bound reports ok", func(t *testing.T) {
		isolateCLIEnv(t)
		h := &doctorTestHub{
			isAdmin: true,
			listenResp: &leapmuxv1.GetListenStatusResponse{
				Bound:             []*leapmuxv1.BoundAddress{{Address: "127.0.0.1:4327"}},
				LocalIpcAddresses: []string{"unix:/tmp/hub.sock"},
			},
		}
		srv := startDoctorHubServer(t, h)
		liveCredentialFor(t, srv.URL)

		data := runControl(t, "doctor", "--hub", srv.URL).Data(t)
		assert.Equal(t, true, data["healthy"])
		assert.Equal(t, int32(1), h.listenStatusHits.Load(), "doctor must call AdminNetworkService.GetListenStatus")
		chk := doctorCheckByName(t, data, "admin-network")
		assert.Equal(t, "ok", chk["status"])
		assert.Contains(t, chk["detail"], "127.0.0.1:4327")
		assert.Contains(t, chk["detail"], "unix:/tmp/hub.sock")
	})

	t.Run("partial listener bind error reports warn", func(t *testing.T) {
		isolateCLIEnv(t)
		h := &doctorTestHub{
			isAdmin: true,
			listenResp: &leapmuxv1.GetListenStatusResponse{
				Bound: []*leapmuxv1.BoundAddress{
					{Address: "127.0.0.1:4327"},
					{Address: "100.64.0.1:4327", Error: "address already in use"},
				},
			},
		}
		srv := startDoctorHubServer(t, h)
		liveCredentialFor(t, srv.URL)

		data := runControl(t, "doctor", "--hub", srv.URL).Data(t)
		assert.Equal(t, true, data["healthy"])
		chk := doctorCheckByName(t, data, "admin-network")
		assert.Equal(t, "warn", chk["status"])
		assert.Contains(t, chk["detail"], "100.64.0.1:4327")
		assert.Contains(t, chk["detail"], "address already in use")
	})

	t.Run("all listeners failed reports fail and unhealthy exit", func(t *testing.T) {
		isolateCLIEnv(t)
		h := &doctorTestHub{
			isAdmin: true,
			listenResp: &leapmuxv1.GetListenStatusResponse{
				Bound: []*leapmuxv1.BoundAddress{
					{Address: "127.0.0.1:4327", Error: "permission denied"},
				},
			},
		}
		srv := startDoctorHubServer(t, h)
		liveCredentialFor(t, srv.URL)

		out := runControl(t, "doctor", "--hub", srv.URL)
		require.Equal(t, cli.ExitFailure, out.Code)
		data := out.envelope(t).Data
		assert.Equal(t, false, data["healthy"])
		chk := doctorCheckByName(t, data, "admin-network")
		assert.Equal(t, "fail", chk["status"])
		assert.Contains(t, chk["detail"], "127.0.0.1:4327")
		assert.Contains(t, chk["detail"], "permission denied")
	})

	t.Run("non-admin skips GetListenStatus and omits admin-network", func(t *testing.T) {
		isolateCLIEnv(t)
		h := &doctorTestHub{isAdmin: false}
		srv := startDoctorHubServer(t, h)
		liveCredentialFor(t, srv.URL)

		data := runControl(t, "doctor", "--hub", srv.URL).Data(t)
		assert.Equal(t, true, data["healthy"])
		assert.Zero(t, h.listenStatusHits.Load(), "non-admin doctor must not call GetListenStatus")
		assert.False(t, hasDoctorCheck(t, data, "admin-network"))
	})

	t.Run("admin permission denied on GetListenStatus omits admin-network and stays healthy", func(t *testing.T) {
		isolateCLIEnv(t)
		h := &doctorTestHub{
			isAdmin:   true,
			listenErr: connect.NewError(connect.CodePermissionDenied, "missing admin:read"),
		}
		srv := startDoctorHubServer(t, h)
		liveCredentialFor(t, srv.URL)

		data := runControl(t, "doctor", "--hub", srv.URL).Data(t)
		assert.Equal(t, true, data["healthy"])
		assert.Equal(t, int32(1), h.listenStatusHits.Load())
		assert.False(t, hasDoctorCheck(t, data, "admin-network"))
	})
}

func TestDoctor_OnlineWorkerZeroMetricsEmittedInJSONAndText(t *testing.T) {
	hub := &recordingHub{
		systemBuild: version.BuildInfoProto(),
		workers: []*leapmuxv1.Worker{
			{Id: "worker-A", Online: true},
			{Id: "worker-off", Online: false},
		},
	}
	worker := &phase2bWorkerDispatcher{
		workerName: "emptybox",
		providers:  nil,
		shells:     nil,
	}
	startSpawnIPC(t, hub, worker)

	data := runControl(t, "doctor").Data(t)
	assert.Equal(t, true, data["healthy"])
	workers, ok := data["workers"].([]any)
	require.True(t, ok)
	require.Len(t, workers, 2)

	online := workers[0].(map[string]any)
	assert.Equal(t, "emptybox", online["name"])
	assert.Contains(t, online, "ping_ms", "online worker must emit ping_ms even when 0")
	assert.Contains(t, online, "providers", "online worker must emit providers even when 0")
	assert.Contains(t, online, "shells", "online worker must emit shells even when 0")
	assert.EqualValues(t, 0, online["providers"])
	assert.EqualValues(t, 0, online["shells"])

	offline := workers[1].(map[string]any)
	assert.NotContains(t, offline, "ping_ms", "offline worker must omit ping_ms")
	assert.NotContains(t, offline, "providers", "offline worker must omit providers")
	assert.NotContains(t, offline, "shells", "offline worker must omit shells")

	textOut := runControlText(t, "doctor")
	require.Equal(t, cli.ExitOK, textOut.Code, textOut.Stderr)
	assert.Contains(t, textOut.Stdout, "providers=0")
	assert.Contains(t, textOut.Stdout, "shells=0")
	assert.Contains(t, textOut.Stdout, "offline")
}

type doctorWorkerDispatcher struct {
	workerName   string
	build        *leapmuxv1.BuildInfo
	sysInfoErr   string
	providersErr string
	shellsErr    string
}

func (d *doctorWorkerDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch {
	case strings.HasSuffix(req.GetMethod(), "/Ping"):
		writeProtoResponse(rw, &leapmuxv1.PingResponse{})
	case strings.HasSuffix(req.GetMethod(), "/GetWorkerSystemInfo"):
		if d.sysInfoErr != "" {
			_ = rw.SendError(13, d.sysInfoErr)
			return
		}
		b := d.build
		if b == nil {
			b = version.BuildInfoProto()
		}
		writeProtoResponse(rw, &leapmuxv1.GetWorkerSystemInfoResponse{
			Name:  d.workerName,
			Build: b,
		})
	case strings.HasSuffix(req.GetMethod(), "/ListAvailableProviders"):
		if d.providersErr != "" {
			_ = rw.SendError(13, d.providersErr)
			return
		}
		writeProtoResponse(rw, &leapmuxv1.ListAvailableProvidersResponse{})
	case strings.HasSuffix(req.GetMethod(), "/ListAvailableShells"):
		if d.shellsErr != "" {
			_ = rw.SendError(13, d.shellsErr)
			return
		}
		writeProtoResponse(rw, &leapmuxv1.ListAvailableShellsResponse{})
	default:
		_ = rw.SendError(12, "unimplemented: "+req.GetMethod())
	}
}

func TestDoctor_OnlineWorkerProbeErrorPaths(t *testing.T) {
	t.Run("provider and shell errors keep worker ok and omit optional metrics", func(t *testing.T) {
		hub := &recordingHub{
			systemBuild: version.BuildInfoProto(),
			workers: []*leapmuxv1.Worker{
				{Id: "worker-A", Online: true},
			},
		}
		worker := &doctorWorkerDispatcher{
			workerName:   "partialbox",
			providersErr: "provider scan failed",
			shellsErr:    "shell scan failed",
		}
		startSpawnIPC(t, hub, worker)

		data := runControl(t, "doctor").Data(t)
		assert.Equal(t, true, data["healthy"])
		workers, ok := data["workers"].([]any)
		require.True(t, ok)
		require.Len(t, workers, 1)

		wk := workers[0].(map[string]any)
		assert.Equal(t, "ok", wk["status"])
		assert.Equal(t, "partialbox", wk["name"])
		assert.Contains(t, wk, "ping_ms")
		assert.NotContains(t, wk, "providers")
		assert.NotContains(t, wk, "shells")

		textOut := runControlText(t, "doctor")
		require.Equal(t, cli.ExitOK, textOut.Code, textOut.Stderr)
		assert.Contains(t, textOut.Stdout, "partialbox")
		assert.Contains(t, textOut.Stdout, "ping=")
		assert.NotContains(t, textOut.Stdout, "providers=")
		assert.NotContains(t, textOut.Stdout, "shells=")
	})

	t.Run("system info failure after ping fails worker and doctor exit", func(t *testing.T) {
		hub := &recordingHub{
			systemBuild: version.BuildInfoProto(),
			workers: []*leapmuxv1.Worker{
				{Id: "worker-A", Online: true},
			},
		}
		worker := &doctorWorkerDispatcher{
			workerName: "brokenbox",
			sysInfoErr: "sysinfo broken",
		}
		startSpawnIPC(t, hub, worker)

		out := runControl(t, "doctor")
		require.Equal(t, cli.ExitFailure, out.Code)
		data := out.envelope(t).Data
		assert.Equal(t, false, data["healthy"])

		workersChk := doctorCheckByName(t, data, "workers")
		assert.Equal(t, "fail", workersChk["status"])

		workers, ok := data["workers"].([]any)
		require.True(t, ok)
		require.Len(t, workers, 1)

		wk := workers[0].(map[string]any)
		assert.Equal(t, "fail", wk["status"])
		assert.Contains(t, wk["error"], "system info failed")
		assert.Contains(t, wk["error"], "sysinfo broken")
	})
}

func TestDoctor_LocalSocketAuthHintsIncludeDeviceCode(t *testing.T) {
	t.Run("refused local socket hints --device-code", func(t *testing.T) {
		isolateCLIEnv(t)
		h := &doctorTestHub{
			authErr: connect.NewError(connect.CodeUnauthenticated, "rejected"),
		}
		mux := http.NewServeMux()
		cs := connect.NewServer()
		leapmuxv1connect.RegisterAuthServiceHandler(cs, h)
		leapmuxv1connect.RegisterSystemServiceHandler(cs, h)
		leapmuxv1connect.RegisterWorkerManagementServiceHandler(cs, h)
		connecthttp.Mount(mux, cs, connecthttp.WithReadMaxBytes(0))
		sockURL := hubtransporttest.NewSocketServer(t, "doc-refused", mux)

		out := runControl(t, "doctor", "--hub", sockURL)
		require.Equal(t, cli.ExitFailure, out.Code)
		data := out.envelope(t).Data
		chk := doctorCheckByName(t, data, "auth")
		assert.Equal(t, "fail", chk["status"])
		assert.Contains(t, chk["fix"], "leapmux auth login --hub "+sockURL+" --device-code")
	})

	t.Run("expiring refresh token on local socket hints --device-code", func(t *testing.T) {
		isolateCLIEnv(t)
		h := &doctorTestHub{isAdmin: false}
		mux := http.NewServeMux()
		cs := connect.NewServer()
		leapmuxv1connect.RegisterAuthServiceHandler(cs, h)
		leapmuxv1connect.RegisterSystemServiceHandler(cs, h)
		leapmuxv1connect.RegisterWorkerManagementServiceHandler(cs, h)
		connecthttp.Mount(mux, cs, connecthttp.WithReadMaxBytes(0))
		sockURL := hubtransporttest.NewSocketServer(t, "doc-expiring", mux)

		require.NoError(t, control.SaveCredentials(sockURL, control.CredentialFile{
			HubURL:           sockURL,
			AccessToken:      "lmx_a_local",
			RefreshToken:     "lmx_r_local",
			Username:         "alice",
			UserID:           "u-1",
			ExpiresAt:        time.Now().Add(time.Hour),
			RefreshExpiresAt: time.Now().Add(time.Hour),
		}))

		data := runControl(t, "doctor", "--hub", sockURL).Data(t)
		chk := doctorCheckByName(t, data, "auth")
		assert.Equal(t, "warn", chk["status"])
		assert.Contains(t, chk["fix"], "leapmux auth login --hub "+sockURL+" --device-code")
	})
}

func TestDoctor_PostAuthChecksGetFreshDeadlinesAndRunConcurrently(t *testing.T) {
	isolateCLIEnv(t)

	var (
		mu              sync.Mutex
		authDeadline    time.Time
		netDeadline     time.Time
		sysDeadline     time.Time
		workersDeadline time.Time
	)
	netArrived := make(chan struct{})
	sysArrived := make(chan struct{})
	workersArrived := make(chan struct{})

	h := &doctorTestHub{
		isAdmin: true,
		listenResp: &leapmuxv1.GetListenStatusResponse{
			Bound: []*leapmuxv1.BoundAddress{{Address: "127.0.0.1:4327"}},
		},
		onGetCurrentUser: func(ctx context.Context) {
			if d, ok := ctx.Deadline(); ok {
				mu.Lock()
				authDeadline = d
				mu.Unlock()
			}
			// Advance past Connect-Timeout-Ms (1ms) truncation and Windows's
			// ~15.6ms clock tick so fresh post-auth deadlines are strictly
			// later than authDeadline; a shared deadline would not advance.
			timer := time.NewTimer(25 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
			}
		},
		onGetListenStatus: func(ctx context.Context) {
			if d, ok := ctx.Deadline(); ok {
				mu.Lock()
				netDeadline = d
				mu.Unlock()
			}
			close(netArrived)
			select {
			case <-sysArrived:
			case <-ctx.Done():
			}
			select {
			case <-workersArrived:
			case <-ctx.Done():
			}
		},
		onGetSystemInfo: func(ctx context.Context) {
			if d, ok := ctx.Deadline(); ok {
				mu.Lock()
				sysDeadline = d
				mu.Unlock()
			}
			close(sysArrived)
			select {
			case <-netArrived:
			case <-ctx.Done():
			}
			select {
			case <-workersArrived:
			case <-ctx.Done():
			}
		},
		onListWorkers: func(ctx context.Context) {
			if d, ok := ctx.Deadline(); ok {
				mu.Lock()
				workersDeadline = d
				mu.Unlock()
			}
			close(workersArrived)
			select {
			case <-netArrived:
			case <-ctx.Done():
			}
			select {
			case <-sysArrived:
			case <-ctx.Done():
			}
		},
	}
	srv := startDoctorHubServer(t, h)
	liveCredentialFor(t, srv.URL)

	data := runControl(t, "doctor", "--hub", srv.URL).Data(t)
	assert.Equal(t, true, data["healthy"])

	// Verify deterministic check ordering in report.Checks.
	checks := data["checks"].([]any)
	names := make([]string, 0, len(checks))
	for _, raw := range checks {
		names = append(names, raw.(map[string]any)["name"].(string))
	}
	assert.Equal(t, []string{"local-daemons", "connection", "auth", "admin-network", "hub", "worker-pins", "workers"}, names)
	assert.Equal(t, "0 pinned workers", doctorCheckByName(t, data, "worker-pins")["detail"])

	mu.Lock()
	defer mu.Unlock()
	require.False(t, authDeadline.IsZero(), "auth check must carry a deadline")
	require.False(t, netDeadline.IsZero(), "admin-network check must carry a deadline")
	require.False(t, sysDeadline.IsZero(), "hub check must carry a deadline")
	require.False(t, workersDeadline.IsZero(), "workers check must carry a deadline")
	assert.True(t, netDeadline.After(authDeadline), "admin-network must derive a fresh deadline after auth (%v vs %v)", netDeadline, authDeadline)
	assert.True(t, sysDeadline.After(authDeadline), "hub version must derive a fresh deadline after auth (%v vs %v)", sysDeadline, authDeadline)
	assert.True(t, workersDeadline.After(authDeadline), "workers must derive a fresh deadline after auth (%v vs %v)", workersDeadline, authDeadline)
	assert.True(t, strings.Contains(doctorCheckByName(t, data, "admin-network")["detail"].(string), "127.0.0.1:4327"))
}

func TestDoctor_InsideTabReportsDirectHubAuth(t *testing.T) {
	t.Run("no direct hub configured reports info and stays healthy", func(t *testing.T) {
		isolateCLIEnv(t)
		hub := &recordingHub{systemBuild: version.BuildInfoProto()}
		startSpawnIPC(t, hub, nil)

		data := runControl(t, "doctor").Data(t)
		assert.Equal(t, true, data["healthy"])
		chk := doctorCheckByName(t, data, "direct_hub_auth")
		assert.Equal(t, "info", chk["status"])
		assert.Contains(t, chk["detail"], "no direct hub configured")
	})

	t.Run("stored credential for configured default hub reports ok", func(t *testing.T) {
		isolateCLIEnv(t)
		const hubURL = "https://hub.example.test"
		require.NoError(t, control.SaveCredentials(hubURL, control.CredentialFile{
			HubURL:      hubURL,
			AccessToken: "tok",
			UserID:      "u-1",
			Username:    "alice",
			ExpiresAt:   time.Now().Add(time.Hour),
		}))
		hub := &recordingHub{systemBuild: version.BuildInfoProto()}
		startSpawnIPC(t, hub, nil)

		data := runControl(t, "doctor").Data(t)
		assert.Equal(t, true, data["healthy"])
		chk := doctorCheckByName(t, data, "direct_hub_auth")
		assert.Equal(t, "ok", chk["status"])
		assert.Contains(t, chk["detail"], hubURL)
		assert.Contains(t, chk["detail"], "alice")
	})

	t.Run("LEAPMUX_HUB without stored credential warns and explains socket precedence without failing doctor", func(t *testing.T) {
		isolateCLIEnv(t)
		hub := &recordingHub{systemBuild: version.BuildInfoProto()}
		startSpawnIPC(t, hub, nil)
		t.Setenv("LEAPMUX_HUB", "https://unauthed.example.test")

		data := runControl(t, "doctor").Data(t)
		assert.Equal(t, true, data["healthy"])
		conn := doctorCheckByName(t, data, "connection")
		assert.Contains(t, conn["detail"], "$LEAPMUX_HUB=https://unauthed.example.test")
		assert.Contains(t, conn["detail"], "$LEAPMUX_CONTROL_SOCK")
		chk := doctorCheckByName(t, data, "direct_hub_auth")
		assert.Equal(t, "warn", chk["status"])
		assert.Contains(t, chk["detail"], "no stored credential")
		assert.Contains(t, chk["detail"], "$LEAPMUX_CONTROL_SOCK takes precedence")
		assert.Contains(t, chk["fix"], "leapmux auth login --hub https://unauthed.example.test")
	})

	t.Run("multiple stored credentials with no default hub warns and suggests auth use", func(t *testing.T) {
		isolateCLIEnv(t)
		for _, hubURL := range []string{"https://hub-a.example.test", "https://hub-b.example.test"} {
			require.NoError(t, control.SaveCredentials(hubURL, control.CredentialFile{
				HubURL:      hubURL,
				AccessToken: "tok",
				UserID:      "u-1",
				Username:    "alice",
				ExpiresAt:   time.Now().Add(time.Hour),
			}))
		}
		hub := &recordingHub{systemBuild: version.BuildInfoProto()}
		startSpawnIPC(t, hub, nil)

		data := runControl(t, "doctor").Data(t)
		assert.Equal(t, true, data["healthy"])
		chk := doctorCheckByName(t, data, "direct_hub_auth")
		assert.Equal(t, "warn", chk["status"])
		assert.Contains(t, chk["detail"], "2 stored credentials")
		assert.Contains(t, chk["fix"], "leapmux auth use <hub>")
	})

	t.Run("expired refresh token warns with login fix", func(t *testing.T) {
		isolateCLIEnv(t)
		const hubURL = "https://expired-refresh.example.test"
		require.NoError(t, control.SaveCredentials(hubURL, control.CredentialFile{
			HubURL:           hubURL,
			AccessToken:      "tok",
			RefreshToken:     "ref",
			UserID:           "u-1",
			Username:         "alice",
			ExpiresAt:        time.Now().Add(-2 * time.Hour),
			RefreshExpiresAt: time.Now().Add(-time.Hour),
		}))
		hub := &recordingHub{systemBuild: version.BuildInfoProto()}
		startSpawnIPC(t, hub, nil)

		data := runControl(t, "doctor").Data(t)
		assert.Equal(t, true, data["healthy"])
		chk := doctorCheckByName(t, data, "direct_hub_auth")
		assert.Equal(t, "warn", chk["status"])
		assert.Contains(t, chk["detail"], "refresh token expired")
		assert.Contains(t, chk["detail"], "alice")
		assert.Contains(t, chk["fix"], "leapmux auth login --hub "+hubURL)
	})

	t.Run("expired access token with valid refresh token reports ok and notes refresh", func(t *testing.T) {
		isolateCLIEnv(t)
		const hubURL = "https://expired-access.example.test"
		require.NoError(t, control.SaveCredentials(hubURL, control.CredentialFile{
			HubURL:           hubURL,
			AccessToken:      "tok",
			RefreshToken:     "ref",
			UserID:           "u-1",
			Username:         "alice",
			ExpiresAt:        time.Now().Add(-time.Hour),
			RefreshExpiresAt: time.Now().Add(24 * time.Hour),
		}))
		hub := &recordingHub{systemBuild: version.BuildInfoProto()}
		startSpawnIPC(t, hub, nil)

		data := runControl(t, "doctor").Data(t)
		assert.Equal(t, true, data["healthy"])
		chk := doctorCheckByName(t, data, "direct_hub_auth")
		assert.Equal(t, "ok", chk["status"])
		assert.Contains(t, chk["detail"], "access token expired; will refresh")
		assert.Empty(t, chk["fix"])
	})

	t.Run("local IPC hub without stored credentials reports ok", func(t *testing.T) {
		isolateCLIEnv(t)
		const localHub = "unix:/tmp/leapmux-local-hub.sock"
		require.NoError(t, control.SetDefaultHub(localHub))
		hub := &recordingHub{systemBuild: version.BuildInfoProto()}
		startSpawnIPC(t, hub, nil)

		data := runControl(t, "doctor").Data(t)
		assert.Equal(t, true, data["healthy"])
		chk := doctorCheckByName(t, data, "direct_hub_auth")
		assert.Equal(t, "ok", chk["status"])
		assert.Contains(t, chk["detail"], "no stored credential required")
		assert.Empty(t, chk["fix"])
	})

	t.Run("corrupt credential file warns with login fix", func(t *testing.T) {
		isolateCLIEnv(t)
		const hubURL = "https://corrupt.example.test"
		require.NoError(t, control.SetDefaultHub(hubURL))
		credPath, err := control.CredentialsPath(hubURL)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(credPath), 0o700))
		require.NoError(t, os.WriteFile(credPath, []byte("{corrupt-json"), 0o600))
		hub := &recordingHub{systemBuild: version.BuildInfoProto()}
		startSpawnIPC(t, hub, nil)

		data := runControl(t, "doctor").Data(t)
		assert.Equal(t, true, data["healthy"])
		chk := doctorCheckByName(t, data, "direct_hub_auth")
		assert.Equal(t, "warn", chk["status"])
		assert.Contains(t, chk["detail"], "stored credential unreadable")
		assert.Contains(t, chk["fix"], "leapmux auth login --hub "+hubURL)
	})
}
