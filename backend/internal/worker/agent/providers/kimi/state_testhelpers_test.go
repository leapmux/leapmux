package kimi

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/worker/agent"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/service"
	"github.com/stretchr/testify/require"
)

type kimiServiceTestRig struct {
	agent    *Agent
	fake     *fakeKap
	output   *service.OutputHandler
	queries  *db.Queries
	sink     agent.ProviderServices
	database *sql.DB
}

func newKimiServiceTestRig(t *testing.T) *kimiServiceTestRig {
	t.Helper()
	database, err := workerdb.Open(filepath.Join(t.TempDir(), "worker.sqlite"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), database))
	queries := db.New(database)
	require.NoError(t, queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: "test-agent", WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE}))
	registry, err := agent.NewRegistry(Registration())
	require.NoError(t, err)
	handler := service.NewOutputHandler(database, queries, service.NewWatcherManager(), agent.NewManager(registry, nil), nil, nil)
	handler.DataDir = t.TempDir()
	t.Cleanup(handler.CancelBackgroundCtx)
	root, err := handler.NewRootSink("test-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE)
	require.NoError(t, err)
	fake, server := newFakeKap(t)
	provider, opts := newUnconnectedKimiAgent(t, agent.Options{}, root)
	require.NoError(t, provider.connect(provider.Context(), server.URL, fakeKapToken, opts, 30*time.Second))
	t.Cleanup(provider.closeStream)
	require.NoError(t, provider.openStartupSession(opts, 30*time.Second))
	main := provider.run(kimiMainAgentID)
	waitFor(t, func() bool {
		return main.conversationRecovery.Load() == nil && !agent.OutputQueueFor(root).HasPending()
	}, "the service rig applies its initial conversation page and output")
	return &kimiServiceTestRig{agent: provider, fake: fake, output: handler, queries: queries, sink: root, database: database}
}
