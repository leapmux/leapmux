package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/service"
)

func setupShutdownTestServer(t *testing.T, shutdownCh chan struct{}) leapmuxv1connect.AuthServiceClient {
	t.Helper()

	q := setupDB(t)
	err := bootstrap.Run(context.Background(), q)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(
		auth.NewShutdownInterceptor(shutdownCh),
	)
	authSvc := service.NewAuthService(q)
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
}

func TestShutdownInterceptor_AllowsBeforeShutdown(t *testing.T) {
	shutdownCh := make(chan struct{})
	client := setupShutdownTestServer(t, shutdownCh)

	// Before shutdown, RPCs should succeed normally.
	resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.NotNil(t, resp.Msg)
}

func TestShutdownInterceptor_RejectsAfterShutdown(t *testing.T) {
	shutdownCh := make(chan struct{})
	client := setupShutdownTestServer(t, shutdownCh)

	// Close the channel to signal shutdown.
	close(shutdownCh)

	// After shutdown, RPCs should be rejected with CodeUnavailable.
	_, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestShutdownInterceptor_TransitionDuringOperation(t *testing.T) {
	shutdownCh := make(chan struct{})
	client := setupShutdownTestServer(t, shutdownCh)

	// First call succeeds.
	_, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)

	// Trigger shutdown.
	close(shutdownCh)

	// Subsequent call is rejected.
	_, err = client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}
