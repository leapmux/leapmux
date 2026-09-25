package kimi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// connectOffline connects a fresh agent to the fake server at url and returns
// the error of the connect. The agent's stream, when the connect started one,
// closes with the test.
func connectOffline(t *testing.T, url, token string) (*Agent, error) {
	t.Helper()
	a := newOfflineKimiAgent(t, &agenttest.Sink{})
	t.Cleanup(a.closeStream)
	opts := agent.Options{AgentID: "test-agent", APITimeout: 30 * time.Second}
	return a, a.connect(a.Context(), url, token, opts, 30*time.Second)
}

func TestKimiConnect(t *testing.T) {
	t.Parallel()

	t.Run("refuses a server that stated no token", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		_, err := connectOffline(t, server.URL, "")
		require.ErrorContains(t, err, "stated no access token")
		assert.Empty(t, fake.routes(), "no request goes out without the token")
	})

	t.Run("reports a server description it cannot read", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.reply("GET "+kimiRouteMeta, fakeKapReply{HTTPStatus: 500, Code: 50000, Msg: "meta down"})
		_, err := connectOffline(t, server.URL, fakeKapToken)
		require.ErrorContains(t, err, "read the server description")
		assert.Contains(t, err.Error(), "meta down")
		assert.Zero(t, fake.socketDialCount(), "the stream opens only after the server is known")
	})

	t.Run("refuses a server of the legacy CLI", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.mu.Lock()
		fake.version = "1.9.9"
		fake.mu.Unlock()
		_, err := connectOffline(t, server.URL, fakeKapToken)
		require.ErrorIs(t, err, errKimiLegacyCLI)
		assert.Contains(t, err.Error(), "1.9.9")
	})

	t.Run("reports a model catalog it cannot read", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.reply("GET "+kimiRouteModels, fakeKapReply{Code: 50000, Msg: "catalog down"})
		_, err := connectOffline(t, server.URL, fakeKapToken)
		require.ErrorContains(t, err, "read the model catalog")
		assert.Contains(t, err.Error(), "catalog down")
	})

	t.Run("reads the active features, whatever their case", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.mu.Lock()
		fake.features = []map[string]string{
			{"name": kimiFeatureGoal, "state": "ACTIVE"},
			{"name": "swarm", "state": "disabled"},
			{"name": "cron"},
		}
		fake.mu.Unlock()
		a, err := connectOffline(t, server.URL, fakeKapToken)
		require.NoError(t, err)
		assert.Equal(t, map[string]bool{kimiFeatureGoal: true}, a.features, "a feature in any other state is not active")
		assert.NotEmpty(t, a.SupportedGoalActions())
	})
}

func TestKimiReadyReader(t *testing.T) {
	t.Parallel()
	reader := newKimiReadyReader("test-agent")

	// A log line that quotes the ready line is not the ready line.
	reader.observe([]byte(`{"level":40,"msg":"Kimi server: http://127.0.0.1:1/#token=quoted"}`))
	assert.Empty(t, reader.token())

	reader.observe([]byte("Kimi server: http://127.0.0.1:62730/#token=first"))
	reader.observe([]byte("Kimi server: http://127.0.0.1:1/#token=second"))
	assert.Equal(t, "first", reader.token(), "the first ready line states the token")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	address, err := reader.waiter.Wait(ctx, nil, 30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:62730", address, "the address and the token come from the same line")
}

// The startup stamps its attach to the session it opens from the agent's clock.
func TestKimiOpenStartupSessionStampsTheAttachFromTheClock(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	a, sink, opts := newConnectedKimiAgent(t, server.URL, agent.Options{})
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	attach := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	clock.Set(attach).MustWait(ctx)
	a.clock = clock

	require.NoError(t, a.openStartupSession(opts, 30*time.Second))
	assertAttachSplitsTheTaskHistory(t, &kimiTestRig{agent: a, fake: fake, sink: sink}, attach)
}
