package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

func setupShutdownTestServer(t *testing.T, shutdownCh chan struct{}) leapmuxv1connect.AuthServiceClient {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	mux := http.NewServeMux()
	connectServer := connect.NewServer(auth.NewShutdownInterceptor(shutdownCh))
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, &config.Config{}, servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil)))
	leapmuxv1connect.RegisterAuthServiceHandler(connectServer, authSvc)
	connecthttp.Mount(mux, connectServer)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return leapmuxv1connect.NewAuthServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0))))
}

func TestShutdownInterceptor_AllowsBeforeShutdown(t *testing.T) {
	shutdownCh := make(chan struct{})
	client := setupShutdownTestServer(t, shutdownCh)

	// Before shutdown, RPCs should succeed normally.
	resp, err := client.GetOAuthProviders(context.Background(), &leapmuxv1.GetOAuthProvidersRequest{})
	require.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestShutdownInterceptor_RejectsAfterShutdown(t *testing.T) {
	shutdownCh := make(chan struct{})
	client := setupShutdownTestServer(t, shutdownCh)

	// Close the channel to signal shutdown.
	close(shutdownCh)

	// After shutdown, RPCs should be rejected with CodeUnavailable.
	_, err := client.GetOAuthProviders(context.Background(), &leapmuxv1.GetOAuthProvidersRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestShutdownInterceptor_TransitionDuringOperation(t *testing.T) {
	shutdownCh := make(chan struct{})
	client := setupShutdownTestServer(t, shutdownCh)

	// First call succeeds.
	_, err := client.GetOAuthProviders(context.Background(), &leapmuxv1.GetOAuthProvidersRequest{})
	require.NoError(t, err)

	// Trigger shutdown.
	close(shutdownCh)

	// Subsequent call is rejected.
	_, err = client.GetOAuthProviders(context.Background(), &leapmuxv1.GetOAuthProvidersRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}
