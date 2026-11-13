package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
	utiltest "github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestUserEventsHandler_LogsRegistryFailureWithoutDisclosingItsCause(t *testing.T) {
	// The default logger and the metric are process-global.
	logs := utiltest.CaptureDefaultLogger(t)
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "registry-failure")
	bearer := newDelegationBearer(t, st, user.ID)
	const marker = "injected registry backend detail"
	var calls atomic.Int32
	registry := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("load state: get user_state: " + marker)
		}
		manager := crdt.NewManager(owner, newMemJournal(), allowAllAuth{}, nil, time.Now)
		if err := manager.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return manager, nil
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })
	handler := service.NewUserEventsHandler(st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest()).WithTokenValidator(bearer.tv)
	before := testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("invalid", "invalid"))
	request := httptest.NewRequest(http.MethodGet, "http://example.test/ws/userevents", nil)
	request.Header = bearer.authHeader()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Equal(t, "internal server error\n", recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), marker)
	assert.NotContains(t, recorder.Body.String(), "get user_state")
	assert.Contains(t, logs.String(), marker)
	assert.Contains(t, logs.String(), "level=ERROR")
	assert.Contains(t, logs.String(), user.ID)
	assert.Equal(t, before+1, testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("invalid", "invalid")))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPHeader: bearer.authHeader()})
	require.NoError(t, err)
	defer func() { _ = connection.Close(websocket.StatusNormalClosure, "") }()
	payload, err := channelwire.ReadFramedBytes(ctx, connection)
	require.NoError(t, err)
	var event leapmuxv1.WatchUserEvent
	require.NoError(t, proto.Unmarshal(payload, &event))
	require.NotNil(t, event.GetInitial())
	assert.Equal(t, int32(2), calls.Load())
}
