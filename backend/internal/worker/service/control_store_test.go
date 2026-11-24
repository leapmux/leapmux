package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestControlRequestStore_BlockingLifecycleAndUnifiedListing(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	var store controlRequestStore
	assert.False(t, store.hasBlocking("agent-1"))
	assert.False(t, store.hasBlockingInstance("agent-1", "req-1", "claim-1"))
	_, found := store.getBlocking("agent-1", "req-1")
	assert.False(t, found)

	t1 := time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)
	pubA, pubB := new(int), new(int)

	// Upsert clones payload and preserves CreatedAt & SourceSeq on identical replay.
	payload := []byte(`{"tool":"bash"}`)
	stored := store.upsertBlocking(blockingControlRecord{
		AgentID: "agent-1", AgentSessionID: "sess-1", RequestID: "req-b",
		Payload: payload, SourceSeq: 7, ClaimToken: "claim-b", CreatedAt: t2, publisher: pubA,
	})
	payload[0] = 'X'
	assert.Equal(t, []byte(`{"tool":"bash"}`), stored.Payload, "upsert must clone caller payload")

	replayed := store.upsertBlocking(blockingControlRecord{
		AgentID: "agent-1", AgentSessionID: "sess-1", RequestID: "req-b",
		Payload: []byte(`{"tool":"bash"}`), SourceSeq: 0, ClaimToken: "claim-b", CreatedAt: t2.Add(time.Minute), publisher: pubA,
	})
	assert.Equal(t, int64(7), replayed.SourceSeq)
	assert.Equal(t, t2, replayed.CreatedAt)
	assert.True(t, store.hasBlockingInstance("agent-1", "req-b", "claim-b"))
	assert.False(t, store.hasBlockingInstance("agent-1", "req-b", "wrong-claim"))

	// Insert a second blocking request under pubB and a non-blocking request in SQLite.
	store.upsertBlocking(blockingControlRecord{
		AgentID: "agent-1", AgentSessionID: "sess-1", RequestID: "req-a",
		Payload: []byte(`{"tool":"read"}`), ClaimToken: "claim-a", CreatedAt: t1, publisher: pubB,
	})
	_, err := svc.Queries.StoreControlRequest(ctx, db.StoreControlRequestParams{
		AgentID: "agent-1", AgentSessionID: "sess-1", RequestID: "req-sql",
		Payload: []byte(`{"question":"ok?"}`), ClaimToken: "claim-sql", SourceSeq: 3,
	})
	require.NoError(t, err)

	// Unified lookup resolves both stores.
	gotBlocking, ok, err := store.lookup(ctx, svc.Queries, "agent-1", "req-a")
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, gotBlocking.NonBlocking)

	gotSQL, ok, err := store.lookup(ctx, svc.Queries, "agent-1", "req-sql")
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, gotSQL.NonBlocking)

	// Unified list merges both stores ordered by CreatedAt then RequestID.
	listed, err := store.list(ctx, svc.Queries, "agent-1")
	require.NoError(t, err)
	require.Len(t, listed, 3)
	assert.Equal(t, "req-a", listed[0].RequestID)
	assert.Equal(t, "req-b", listed[1].RequestID)
	assert.Equal(t, "req-sql", listed[2].RequestID)

	// Delete rejects mismatched claim token and removes on matching token.
	_, ok = store.deleteBlocking("agent-1", "req-a", "wrong")
	assert.False(t, ok)
	removed, ok := store.deleteBlocking("agent-1", "req-a", "claim-a")
	assert.True(t, ok)
	assert.Equal(t, "req-a", removed.RequestID)

	// Publisher-scoped drain removes only records published by pubA.
	store.upsertBlocking(blockingControlRecord{
		AgentID: "agent-1", RequestID: "req-c", ClaimToken: "claim-c", CreatedAt: t1, publisher: pubB,
	})
	drainedA := store.drainBlockingForPublisher("agent-1", pubA)
	require.Len(t, drainedA, 1)
	assert.Equal(t, "req-b", drainedA[0].RequestID)
	assert.True(t, store.hasBlockingInstance("agent-1", "req-c", "claim-c"))

	drainedRest := store.drainBlocking("agent-1")
	require.Len(t, drainedRest, 1)
	assert.Equal(t, "req-c", drainedRest[0].RequestID)
	assert.False(t, store.hasBlocking("agent-1"))
}

type failingControlDBTX struct {
	db.DBTX
}

func (f failingControlDBTX) QueryRowContext(ctx context.Context, _ string, _ ...any) *sql.Row {
	return f.DBTX.QueryRowContext(ctx, "SELECT missing_control_store_column")
}

func (f failingControlDBTX) QueryContext(ctx context.Context, _ string, _ ...any) (*sql.Rows, error) {
	return f.DBTX.QueryContext(ctx, "SELECT missing_control_store_column")
}

func TestControlRequestStore_EdgeCasesAndErrorPropagation(t *testing.T) {
	t.Parallel()

	t.Run("upsertBlocking non-identical replay drops old SourceSeq while keeping CreatedAt", func(t *testing.T) {
		t.Parallel()
		t1 := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
		t2 := t1.Add(time.Minute)

		for _, tc := range []struct {
			name   string
			mutate func(*blockingControlRecord)
		}{
			{name: "changed Payload", mutate: func(r *blockingControlRecord) { r.Payload = []byte(`{"tool":"edit"}`) }},
			{name: "changed ClaimToken", mutate: func(r *blockingControlRecord) { r.ClaimToken = "claim-new" }},
			{name: "changed AgentSessionID", mutate: func(r *blockingControlRecord) { r.AgentSessionID = "sess-new" }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				var store controlRequestStore
				store.upsertBlocking(blockingControlRecord{
					AgentID: "agent-1", AgentSessionID: "sess-1", RequestID: "req-1",
					Payload: []byte(`{"tool":"bash"}`), SourceSeq: 42, ClaimToken: "claim-1", CreatedAt: t1,
				})
				next := blockingControlRecord{
					AgentID: "agent-1", AgentSessionID: "sess-1", RequestID: "req-1",
					Payload: []byte(`{"tool":"bash"}`), SourceSeq: 0, ClaimToken: "claim-1", CreatedAt: t2,
				}
				tc.mutate(&next)
				got := store.upsertBlocking(next)
				assert.Zero(t, got.SourceSeq, "non-identical replay must not preserve old SourceSeq")
				assert.Equal(t, t1, got.CreatedAt, "replacement must preserve existing CreatedAt")
			})
		}

		var zeroTimeStore controlRequestStore
		populated := zeroTimeStore.upsertBlocking(blockingControlRecord{
			AgentID: "agent-1", RequestID: "req-zero", ClaimToken: "claim-zero",
		})
		assert.False(t, populated.CreatedAt.IsZero(), "zero CreatedAt must populate a non-zero timestamp")
	})

	t.Run("returned payload clones isolate stored bytes", func(t *testing.T) {
		t.Parallel()
		var store controlRequestStore
		pubA, pubB := new(int), new(int)
		store.upsertBlocking(blockingControlRecord{
			AgentID: "agent-1", RequestID: "req-get", Payload: []byte(`{"k":"v1"}`), ClaimToken: "c1", publisher: pubA,
		})
		got, ok := store.getBlocking("agent-1", "req-get")
		require.True(t, ok)
		got.Payload[0] = '!'
		again, ok := store.getBlocking("agent-1", "req-get")
		require.True(t, ok)
		assert.Equal(t, []byte(`{"k":"v1"}`), again.Payload, "mutating getBlocking output must not mutate stored bytes")

		store.mu.RLock()
		storedBeforePubDrain := store.blocking["agent-1"]["req-get"].Payload
		store.mu.RUnlock()
		drainedPub := store.drainBlockingForPublisher("agent-1", pubA)
		require.Len(t, drainedPub, 1)
		drainedPub[0].Payload[0] = '?'
		assert.Equal(t, []byte(`{"k":"v1"}`), storedBeforePubDrain, "mutating drainBlockingForPublisher output must not mutate stored bytes")

		store.upsertBlocking(blockingControlRecord{
			AgentID: "agent-1", RequestID: "req-drain", Payload: []byte(`{"k":"v2"}`), ClaimToken: "c2", publisher: pubB,
		})
		store.mu.RLock()
		storedBeforeDrain := store.blocking["agent-1"]["req-drain"].Payload
		store.mu.RUnlock()
		drainedAll := store.drainBlocking("agent-1")
		require.Len(t, drainedAll, 1)
		drainedAll[0].Payload[0] = '@'
		assert.Equal(t, []byte(`{"k":"v2"}`), storedBeforeDrain, "mutating drainBlocking output must not mutate stored bytes")
	})

	t.Run("deleteBlocking wildcard and map pruning with rangeBlockingAgentIDs", func(t *testing.T) {
		t.Parallel()
		var store controlRequestStore
		pubA, pubB := new(int), new(int)

		collectAgents := func() []string {
			var ids []string
			store.rangeBlockingAgentIDs(func(agentID string) {
				ids = append(ids, agentID)
			})
			return ids
		}

		assert.Empty(t, collectAgents())

		store.upsertBlocking(blockingControlRecord{AgentID: "agent-del", RequestID: "req-1", ClaimToken: "secret-token", publisher: pubA})
		store.upsertBlocking(blockingControlRecord{AgentID: "agent-drain", RequestID: "req-2", ClaimToken: "c2", publisher: pubA})
		store.upsertBlocking(blockingControlRecord{AgentID: "agent-pub", RequestID: "req-3", ClaimToken: "c3", publisher: pubB})
		assert.ElementsMatch(t, []string{"agent-del", "agent-drain", "agent-pub"}, collectAgents())

		// Empty claimToken acts as a wildcard delete regardless of stored ClaimToken.
		removed, ok := store.deleteBlocking("agent-del", "req-1", "")
		require.True(t, ok)
		assert.Equal(t, "secret-token", removed.ClaimToken)
		assert.ElementsMatch(t, []string{"agent-drain", "agent-pub"}, collectAgents(), "deleting last record must prune outer map key")

		// drainBlocking empties agent-drain and prunes it.
		require.Len(t, store.drainBlocking("agent-drain"), 1)
		assert.ElementsMatch(t, []string{"agent-pub"}, collectAgents())

		// drainBlockingForPublisher removing all records for agent-pub prunes it too.
		require.Len(t, store.drainBlockingForPublisher("agent-pub", pubB), 1)
		assert.Empty(t, collectAgents())
	})

	t.Run("drainBlocking and drainBlockingForPublisher tie-break and empty paths", func(t *testing.T) {
		t.Parallel()
		var store controlRequestStore
		pubA, pubB := new(int), new(int)
		sameTime := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)

		assert.Nil(t, store.drainBlocking("missing-agent"))
		assert.Nil(t, store.drainBlockingForPublisher("missing-agent", pubA))

		for _, reqID := range []string{"req-z", "req-a", "req-m"} {
			store.upsertBlocking(blockingControlRecord{
				AgentID: "agent-1", RequestID: reqID, ClaimToken: "c-" + reqID, CreatedAt: sameTime, publisher: pubA,
			})
		}
		assert.Nil(t, store.drainBlockingForPublisher("agent-1", pubB), "non-matching publisher must return nil")
		assert.True(t, store.hasBlocking("agent-1"))

		drainedPub := store.drainBlockingForPublisher("agent-1", pubA)
		require.Len(t, drainedPub, 3)
		assert.Equal(t, []string{"req-a", "req-m", "req-z"}, []string{drainedPub[0].RequestID, drainedPub[1].RequestID, drainedPub[2].RequestID})

		for _, reqID := range []string{"req-c", "req-a", "req-b"} {
			store.upsertBlocking(blockingControlRecord{
				AgentID: "agent-2", RequestID: reqID, ClaimToken: "c-" + reqID, CreatedAt: sameTime, publisher: pubB,
			})
		}
		drainedAll := store.drainBlocking("agent-2")
		require.Len(t, drainedAll, 3)
		assert.Equal(t, []string{"req-a", "req-b", "req-c"}, []string{drainedAll[0].RequestID, drainedAll[1].RequestID, drainedAll[2].RequestID})
	})

	t.Run("lookup and list miss, empty, tie-break, and DB error propagation", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		svc, _, _ := setupTestService(t)
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID:            "agent-1",
			WorkingDir:    t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}))

		var store controlRequestStore

		got, found, err := store.lookup(ctx, svc.Queries, "agent-1", "absent")
		require.NoError(t, err)
		assert.False(t, found)
		assert.Zero(t, got)

		emptyList, err := store.list(ctx, svc.Queries, "agent-1")
		require.NoError(t, err)
		assert.Empty(t, emptyList)

		// Tie-break equal CreatedAt by RequestID ascending across blocking and SQLite stores.
		sameTime := time.Date(2025, 7, 1, 8, 0, 0, 0, time.UTC)
		_, err = svc.Queries.StoreControlRequest(ctx, db.StoreControlRequestParams{
			AgentID: "agent-1", RequestID: "req-b", Payload: []byte(`{"q":"b"}`), ClaimToken: "claim-b",
		})
		require.NoError(t, err)
		_, err = svc.DB.ExecContext(ctx, "UPDATE control_requests SET created_at = ? WHERE agent_id = ? AND request_id = ?",
			sqltime.NewSQLiteTime(sameTime), "agent-1", "req-b")
		require.NoError(t, err)
		store.upsertBlocking(blockingControlRecord{
			AgentID: "agent-1", RequestID: "req-c", Payload: []byte(`{"m":"c"}`), ClaimToken: "claim-c", CreatedAt: sameTime,
		})
		store.upsertBlocking(blockingControlRecord{
			AgentID: "agent-1", RequestID: "req-a", Payload: []byte(`{"m":"a"}`), ClaimToken: "claim-a", CreatedAt: sameTime,
		})

		listed, err := store.list(ctx, svc.Queries, "agent-1")
		require.NoError(t, err)
		require.Len(t, listed, 3)
		assert.Equal(t, []string{"req-a", "req-b", "req-c"}, []string{listed[0].RequestID, listed[1].RequestID, listed[2].RequestID})

		// DB query error propagates from both lookup (on blocking miss) and list.
		badQueries := db.New(failingControlDBTX{DBTX: svc.DB})
		_, found, err = store.lookup(ctx, badQueries, "agent-1", "not-in-memory")
		require.Error(t, err)
		assert.False(t, found)

		_, err = store.list(ctx, badQueries, "agent-1")
		require.Error(t, err)
	})
}
