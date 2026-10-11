package mimo

import (
	"net/http"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only a refused credential has a cause that the worker can state. Any other
// failure of the health check keeps its own cause.
func TestCheckCredential(t *testing.T) {
	t.Parallel()

	a, server := newTestAgent(t, nil)
	require.NoError(t, a.checkCredential(a.Context()))

	server.respond("GET "+routeHealth, http.StatusUnauthorized, ``)
	assert.ErrorIs(t, a.checkCredential(a.Context()), errCredentialRefused)

	server.respond("GET "+routeHealth, http.StatusInternalServerError, `{"name":"UnknownError"}`)
	err := a.checkCredential(a.Context())
	require.Error(t, err)
	assert.NotErrorIs(t, err, errCredentialRefused, "a server that fails did not refuse the credential")
	assert.True(t, providerkit.IsHTTPStatus(err, http.StatusInternalServerError))
}

func TestLoadCatalog(t *testing.T) {
	t.Parallel()

	t.Run("the server's answers build the catalog", func(t *testing.T) {
		t.Parallel()
		a, _ := newTestAgent(t, nil)
		a.catalog = mimoCatalog{}

		require.NoError(t, a.loadCatalog(a.Context()))
		assert.Equal(t, []string{"mock/alpha", "mock/beta", "other/old"}, modelIDs(a.catalog.models))
		assert.Equal(t, "mock/beta", a.catalog.defaultModel, "the configured model is the default")
		assert.Equal(t, []string{contracts.MiMoModeBuild, contracts.MiMoModePlan, "max"}, modeIDs(a.catalog.modes))
	})

	t.Run("the configuration and the agent list are optional", func(t *testing.T) {
		t.Parallel()
		a, server := newTestAgent(t, nil)
		a.catalog = mimoCatalog{}
		server.respond("GET "+routeConfig, http.StatusInternalServerError, `{}`)
		server.respond("GET "+routeAgents, http.StatusInternalServerError, `{}`)

		require.NoError(t, a.loadCatalog(a.Context()))
		assert.Len(t, a.catalog.models, 3)
		assert.Equal(t, "mock/alpha", a.catalog.defaultModel, "with no configured model, the first provider's top-ranked model runs")
		assert.Equal(t, mimoStaticModes, a.catalog.modes, "with no agent list, the static modes stay")
	})

	t.Run("the models are required", func(t *testing.T) {
		t.Parallel()
		a, server := newTestAgent(t, nil)
		before := a.catalog
		server.respond("GET "+routeConfigProviders, http.StatusInternalServerError, `{}`)

		err := a.loadCatalog(a.Context())
		assert.True(t, providerkit.IsHTTPStatus(err, http.StatusInternalServerError))
		assert.Equal(t, before, a.catalog, "a failed load keeps the catalog the agent had")
		assert.Empty(t, server.requestsTo("GET "+routeConfig), "the start fails before it reads the rest")
	})
}

// A stream that opens and never states server.connected cannot release the
// start path. The wait ends at the timeout that the caller gives, and nothing
// else ends it: the process runs and the context stays open.
func TestOpenEventStreamTimesOutWithoutAConnection(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	a, server := newTestAgent(t, nil)
	clock := useMockClock(t, a)
	server.handle("GET "+routeEvents, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	})
	connect := clock.Trap().NewTimer(mimoStreamConnectTimerTag)
	defer connect.Close()

	result := make(chan error, 1)
	go func() { result <- a.openEventStream(a.Context(), testTimeout) }()
	assert.Equal(t, testTimeout, testutil.WaitForTimer(t, ctx, connect), "the wait is the timeout that the caller gives")
	clock.Advance(testTimeout - time.Nanosecond).MustWait(ctx)
	select {
	case err := <-result:
		t.Fatalf("the wait ended before its timeout: %v", err)
	default:
	}
	clock.Advance(time.Nanosecond).MustWait(ctx)
	select {
	case err := <-result:
		assert.ErrorContains(t, err, "did not connect within 30s")
	case <-ctx.Done():
		t.Fatal("the wait did not end at its timeout")
	}

	a.streamCancel()
	select {
	case <-a.streamDone:
	case <-ctx.Done():
		t.Fatal("the stream loop ends when its context ends")
	}
}
