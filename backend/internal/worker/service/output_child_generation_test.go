package service

import (
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaleEnsureChildAgentCannotAttachTheReplacementSpan(t *testing.T) {
	for _, cold := range []bool{false, true} {
		t.Run(map[bool]string{false: "warm registry", true: "cold registry"}[cold], func(t *testing.T) {
			t.Parallel()
			const rootID = "stale-child-generation"
			svc, outgoing := setupRootSink(t, rootID)
			replacement := newTestRootOutputSink(t, svc.Output, rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			childID, err := replacement.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "shared-child", Title: "Replacement child"})
			require.NoError(t, err)
			before, err := svc.Queries.GetAgentByID(t.Context(), childID)
			require.NoError(t, err)
			require.Empty(t, before.SpawnSpanID)
			registryBefore := storedRow(t, svc, rootID, "shared-child")
			if cold {
				svc.Output.bgtasks.Delete(rootID)
			}
			resolved, err := outgoing.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "shared-child", SpawnSpanID: "obsolete-span", Title: "Obsolete child"})
			assert.ErrorIs(t, err, agent.ErrChildIdentityRefused, "an older physical source cannot establish a route to replacement work")
			assert.Empty(t, resolved)
			after, err := svc.Queries.GetAgentByID(t.Context(), childID)
			require.NoError(t, err)
			assert.Equal(t, before, after, "a refused source must change no child identity or metadata")
			assert.Equal(t, registryBefore, storedRow(t, svc, rootID, "shared-child"))
			resolved, err = replacement.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "shared-child", SpawnSpanID: "replacement-span", Title: "Later native title"})
			require.NoError(t, err)
			assert.Equal(t, childID, resolved)
			completed, err := svc.Queries.GetAgentByID(t.Context(), childID)
			require.NoError(t, err)
			expected := before
			expected.SpawnSpanID = "replacement-span"
			assert.Equal(t, expected, completed, "the current source attaches its first span without renaming the child")
			assert.Equal(t, registryBefore, storedRow(t, svc, rootID, "shared-child"))
		})
	}
}

func TestReplacementEnsureChildAgentClaimsTheOriginalChildWithoutChangingIdentity(t *testing.T) {
	t.Parallel()
	const rootID = "claimed-child-generation"
	svc, outgoing := setupRootSink(t, rootID)
	spec := agent.ChildAgentSpec{ProviderChildKey: "shared-child", SpawnSpanID: "original-span", AgentSessionID: "original-native-session", Title: "Original native title"}
	childID, err := outgoing.EnsureChildAgent(spec)
	require.NoError(t, err)
	before, err := svc.Queries.GetAgentByID(t.Context(), childID)
	require.NoError(t, err)
	registryBefore := storedRow(t, svc, rootID, "shared-child")
	replacement := newTestRootOutputSink(t, svc.Output, rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	resolved, err := replacement.EnsureChildAgent(spec)
	require.NoError(t, err)
	assert.Equal(t, childID, resolved)
	after, err := svc.Queries.GetAgentByID(t.Context(), childID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	expected := registryBefore
	expected.OutputGeneration = requireRootOutputSink(t, svc.Output, rootID).outputGeneration
	assert.Equal(t, expected, storedRow(t, svc, rootID, "shared-child"), "an unchanged linkage transfers only registry ownership")
	resolved, err = outgoing.EnsureChildAgent(spec)
	assert.ErrorIs(t, err, agent.ErrChildIdentityRefused)
	assert.Empty(t, resolved)
	final, err := svc.Queries.GetAgentByID(t.Context(), childID)
	require.NoError(t, err)
	assert.Equal(t, before, final)
	assert.Equal(t, expected, storedRow(t, svc, rootID, "shared-child"))
}

func TestHistoricalEnsureChildAgentCanFinishAnIndependentChild(t *testing.T) {
	t.Parallel()
	const rootID = "independent-child-generation"
	svc, outgoing := setupRootSink(t, rootID)
	oldGeneration := requireRootOutputSink(t, svc.Output, rootID).outputGeneration
	replacement := newTestRootOutputSink(t, svc.Output, rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	newChildID, err := replacement.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "replacement-child", SpawnSpanID: "replacement-span"})
	require.NoError(t, err)
	newBefore := storedRow(t, svc, rootID, "replacement-child")
	oldChildID, err := outgoing.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "independent-old-child", SpawnSpanID: "independent-old-span"})
	require.NoError(t, err, "physical replacement alone cannot reject independent retained history")
	assert.NotEqual(t, newChildID, oldChildID)
	assert.Equal(t, oldGeneration, storedRow(t, svc, rootID, "independent-old-child").OutputGeneration)
	assert.Equal(t, newBefore, storedRow(t, svc, rootID, "replacement-child"))
}

func TestEnsureChildAgentRechecksTheSourceAfterConcurrentReplacement(t *testing.T) {
	const rootID = "concurrent-child-generation"
	svc, outgoing := setupRootSink(t, rootID)
	key := strings.Repeat("native-child", bgtask.RowKeyByteLimit+1)
	entered, resume := make(chan struct{}), make(chan struct{})
	resumeOnce := sync.OnceFunc(func() { close(resume) })
	var trapped atomic.Bool
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(controlFailureLogObserver{Handler: slog.NewTextHandler(io.Discard, nil), observe: func(record slog.Record) {
		if record.Message != "derived a registry row key from an unusable provider key" {
			return
		}
		belongs := false
		record.Attrs(func(attribute slog.Attr) bool {
			belongs = belongs || attribute.Key == "agent_id" && attribute.Value.String() == rootID
			return true
		})
		if belongs && trapped.CompareAndSwap(false, true) {
			close(entered)
			<-resume
		}
	}}))
	t.Cleanup(func() { resumeOnce(); slog.SetDefault(previousLogger) })
	type result struct {
		id  string
		err error
	}
	done := make(chan result, 1)
	go func() {
		id, err := outgoing.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: key, SpawnSpanID: "obsolete-span", Title: "Obsolete child"})
		done <- result{id: id, err: err}
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the outgoing child ensure did not reach its admission boundary")
	}
	replacement := newTestRootOutputSink(t, svc.Output, rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	childID, err := replacement.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: key, Title: "Replacement child"})
	require.NoError(t, err)
	before, err := svc.Queries.GetAgentByID(t.Context(), childID)
	require.NoError(t, err)
	registryBefore := storedRow(t, svc, rootID, bgtask.NormalizeRowKey(key))
	resumeOnce()
	select {
	case stale := <-done:
		assert.ErrorIs(t, stale.err, agent.ErrChildIdentityRefused)
		assert.Empty(t, stale.id)
	case <-ctx.Done():
		t.Fatal("the outgoing child ensure did not finish after replacement")
	}
	after, err := svc.Queries.GetAgentByID(t.Context(), childID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Equal(t, registryBefore, storedRow(t, svc, rootID, bgtask.NormalizeRowKey(key)))
	resolved, err := replacement.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: key, SpawnSpanID: "replacement-span"})
	require.NoError(t, err)
	assert.Equal(t, childID, resolved)
	final, err := svc.Queries.GetAgentByID(t.Context(), childID)
	require.NoError(t, err)
	assert.Equal(t, "replacement-span", final.SpawnSpanID)
	assert.Equal(t, before.Title, final.Title)
}

func TestEnsureChildAgentLinkFailureRollsBackIdentityAndGeneration(t *testing.T) {
	t.Parallel()
	const rootID = "child-link-rollback-generation"
	svc, original := setupRootSink(t, rootID)
	childID, err := original.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "shared-child", Title: "Original child"})
	require.NoError(t, err)
	// Model the existing partial-linkage failure state while preserving the native child identity.
	_, err = svc.DB.ExecContext(t.Context(), "UPDATE agent_background_tasks SET child_agent_id = '' WHERE owner_agent_id = ? AND row_key = ?", rootID, "shared-child")
	require.NoError(t, err)
	before, err := svc.Queries.GetAgentByID(t.Context(), childID)
	require.NoError(t, err)
	require.Empty(t, before.SpawnSpanID)
	registryBefore := storedRow(t, svc, rootID, "shared-child")
	require.Empty(t, registryBefore.ChildAgentID)
	replacement := newTestRootOutputSink(t, svc.Output, rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	cache := svc.Output.bgTaskCache(rootID)
	cache.Mu.Lock()
	mirror := cache.mirrorLocked()
	cache.Mu.Unlock()
	_, err = svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_child_registry_link BEFORE UPDATE OF child_agent_id ON agent_background_tasks
WHEN NEW.owner_agent_id = 'child-link-rollback-generation' AND NEW.row_key = 'shared-child'
BEGIN SELECT RAISE(ABORT, 'child registry linkage refused'); END`)
	require.NoError(t, err)
	spec := agent.ChildAgentSpec{ProviderChildKey: "shared-child", SpawnSpanID: "replacement-span", Title: "Original child"}
	resolved, err := replacement.EnsureChildAgent(spec)
	require.ErrorContains(t, err, "child registry linkage refused")
	assert.NotErrorIs(t, err, agent.ErrChildIdentityRefused, "a storage refusal is distinct from a superseded native identity")
	assert.Empty(t, resolved)
	after, err := svc.Queries.GetAgentByID(t.Context(), childID)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the failed linkage must roll back the first-span attachment")
	assert.Equal(t, registryBefore, storedRow(t, svc, rootID, "shared-child"), "the failed linkage must roll back the ownership claim")
	cache.Mu.Lock()
	current := cache.mirrorLocked()
	cache.Mu.Unlock()
	assert.Equal(t, mirror, current)
	_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER refuse_child_registry_link")
	require.NoError(t, err)
	resolved, err = replacement.EnsureChildAgent(spec)
	require.NoError(t, err)
	assert.Equal(t, childID, resolved)
	final, err := svc.Queries.GetAgentByID(t.Context(), childID)
	require.NoError(t, err)
	expected := before
	expected.SpawnSpanID = "replacement-span"
	assert.Equal(t, expected, final)
	linked := storedRow(t, svc, rootID, "shared-child")
	assert.Equal(t, requireRootOutputSink(t, svc.Output, rootID).outputGeneration, linked.OutputGeneration)
	assert.Equal(t, childID, linked.ChildAgentID)
	assert.Equal(t, registryBefore.CreatedAt, linked.CreatedAt)
	children, err := svc.Queries.ListAgentDescendantsForResume(t.Context(), sqlString(rootID))
	require.NoError(t, err)
	assert.Len(t, children, 1, "the retry must reuse the original durable child")
}
