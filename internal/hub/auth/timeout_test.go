package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/service"
)

// timeoutCapture is a wrapper around AuthService that captures the context
// deadline observed inside a handler invocation.
type timeoutCapture struct {
	leapmuxv1connect.UnimplementedAuthServiceHandler
	inner       *service.AuthService
	deadline    time.Time
	hasDeadline bool
}

func (c *timeoutCapture) GetSystemInfo(ctx context.Context, req *connect.Request[leapmuxv1.GetSystemInfoRequest]) (*connect.Response[leapmuxv1.GetSystemInfoResponse], error) {
	c.deadline, c.hasDeadline = ctx.Deadline()
	return c.inner.GetSystemInfo(ctx, req)
}

func setupTimeoutTestServer(t *testing.T, timeout time.Duration) (leapmuxv1connect.AuthServiceClient, *timeoutCapture) {
	t.Helper()

	q := setupDB(t)
	err := bootstrap.Run(context.Background(), q)
	require.NoError(t, err)

	capture := &timeoutCapture{inner: service.NewAuthService(q)}

	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(auth.NewTimeoutInterceptor(func() time.Duration { return timeout }))
	path, handler := leapmuxv1connect.NewAuthServiceHandler(capture, interceptors)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
	return client, capture
}

func TestTimeoutInterceptor_AppliesDefaultTimeout(t *testing.T) {
	defaultTimeout := 5 * time.Second
	client, capture := setupTimeoutTestServer(t, defaultTimeout)

	before := time.Now()

	// Call without any client-side deadline. The interceptor should apply the
	// default timeout so the handler sees a deadline.
	_, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)

	assert.True(t, capture.hasDeadline, "expected context to have a deadline")

	// The deadline should be approximately now + defaultTimeout. Allow some
	// tolerance for test execution time.
	expectedDeadline := before.Add(defaultTimeout)
	assert.WithinDuration(t, expectedDeadline, capture.deadline, 2*time.Second,
		"deadline should be approximately now + default timeout")
}

func TestTimeoutInterceptor_PreservesExistingDeadline(t *testing.T) {
	defaultTimeout := 5 * time.Second
	client, capture := setupTimeoutTestServer(t, defaultTimeout)

	// Set a custom deadline that is further out than the default timeout.
	customDeadline := time.Now().Add(30 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), customDeadline)
	defer cancel()

	_, err := client.GetSystemInfo(ctx, connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)

	assert.True(t, capture.hasDeadline, "expected context to have a deadline")

	// The handler should see the original (longer) deadline, not one overwritten
	// by the interceptor's shorter default.
	assert.WithinDuration(t, customDeadline, capture.deadline, 2*time.Second,
		"original deadline should be preserved, not replaced by default timeout")

	// Make sure the deadline was NOT shortened to the default timeout.
	// The captured deadline should be at least 25 seconds from now, which is
	// well beyond the 5s default timeout the interceptor would apply.
	assert.True(t, capture.deadline.After(time.Now().Add(defaultTimeout)),
		"deadline should be further out than the default timeout")
}
