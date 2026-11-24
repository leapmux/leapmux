package service

import (
	"github.com/leapmux/leapmux/internal/worker/bgtask"

	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestMessageSessionIDMatchesTheRowsEachSinkWrites pins the contract a
// session-info broadcast relies on: the session a sink reports is the session
// its own rows carry. A child sink reports its own session, never its parent's,
// so a broadcast a provider makes for a child identifies the child's transcript.
func TestMessageSessionIDMatchesTheRowsEachSinkWrites(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink := setupRootSink(t, "root-1")

	sink.UpdateSessionID("sess-root")
	assert.Equal(t, "sess-root", sink.MessageSessionID())
	require.NoError(t, sink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"assistant"}`)},
		agent.SpanInfo{SpanID: "root-span-1", SpanType: "text"}))

	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "span-1", ProviderChildKey: "task-1", Title: "child"})
	require.NoError(t, err)
	childSink := sink.ChildSink(childID)

	childSink.UpdateSessionID("sess-child")
	assert.Equal(t, "sess-child", childSink.MessageSessionID())
	require.NoError(t, childSink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"assistant"}`)},
		agent.SpanInfo{SpanID: "child-span-1", SpanType: "text"}))

	rootMsgs, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "root-1", Limit: 100})
	require.NoError(t, err)
	require.Len(t, rootMsgs, 1)
	assert.Equal(t, "sess-root", rootMsgs[0].AgentSessionID, "the root rows carry the session the root sink reports")
	assert.Equal(t, "sess-root", sink.MessageSessionID(), "the child session does not replace the root's")

	childMsgs, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: childID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, childMsgs, 1)
	assert.Equal(t, "sess-child", childMsgs[0].AgentSessionID, "the child rows carry the session the child sink reports")
}

// TestMessageSessionIDStartsEmptyAndFollowsEachSessionTransition pins the
// value the sink reports at every point of a session change. It is empty until
// the first UpdateSessionID, and it identifies the newest session after each one.
// Each row carries the value the sink reported when the row was written.
func TestMessageSessionIDStartsEmptyAndFollowsEachSessionTransition(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink := setupRootSink(t, "root-1")

	assert.Empty(t, sink.MessageSessionID(), "no session is known before the first UpdateSessionID")
	require.NoError(t, sink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"assistant"}`)},
		agent.SpanInfo{SpanID: "span-1", SpanType: "text"}))

	sink.UpdateSessionID("sess-1")
	assert.Equal(t, "sess-1", sink.MessageSessionID(), "the first session is reported at once")
	require.NoError(t, sink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"assistant"}`)},
		agent.SpanInfo{SpanID: "span-2", SpanType: "text"}))

	sink.UpdateSessionID("sess-2")
	assert.Equal(t, "sess-2", sink.MessageSessionID(), "the newest session wins the transition")
	require.NoError(t, sink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"assistant"}`)},
		agent.SpanInfo{SpanID: "span-3", SpanType: "text"}))

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "root-1", Limit: 100})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Empty(t, rows[0].AgentSessionID, "the rows of the empty session carry no session")
	assert.Equal(t, "sess-1", rows[1].AgentSessionID, "each row carries the session the sink reported when it wrote")
	assert.Equal(t, "sess-2", rows[2].AgentSessionID, "each row carries the session the sink reported when it wrote")
}

// TestChildMessageSessionIDIsNotInheritedFromItsParent pins the child sink's
// own session state. A child sink reports no session while its parent holds
// one, and a parent transition never moves it. Only the child's own
// UpdateSessionID gives it a session.
func TestChildMessageSessionIDIsNotInheritedFromItsParent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink := setupRootSink(t, "root-1")
	sink.UpdateSessionID("sess-root")

	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "span-1", ProviderChildKey: "task-1", Title: "child"})
	require.NoError(t, err)
	childSink := sink.ChildSink(childID)

	assert.Empty(t, childSink.MessageSessionID(), "the child starts with no session of its own")
	require.NoError(t, childSink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"assistant"}`)},
		agent.SpanInfo{SpanID: "child-span-1", SpanType: "text"}))

	sink.UpdateSessionID("sess-root-2")
	assert.Equal(t, "sess-root-2", sink.MessageSessionID())
	assert.Empty(t, childSink.MessageSessionID(), "the parent's session change does not move the child")

	childSink.UpdateSessionID("sess-child")
	assert.Equal(t, "sess-child", childSink.MessageSessionID())
	require.NoError(t, childSink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"assistant"}`)},
		agent.SpanInfo{SpanID: "child-span-2", SpanType: "text"}))

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: childID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Empty(t, rows[0].AgentSessionID, "the rows of the empty session carry no session")
	assert.Equal(t, "sess-child", rows[1].AgentSessionID, "each row carries the session the child sink reported when it wrote")
}

func TestBindChildSessionIDRecordsTheFirstNativeSession(t *testing.T) {
	t.Parallel()
	svc, root := setupRootSink(t, "root-1")
	root.UpdateSessionID("native-root")
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "spawn", Title: "Child",
	})
	require.NoError(t, err)
	child := root.ChildSink(childID)

	require.ErrorIs(t, root.BindChildSessionID("native-child"), agent.ErrChildIdentityRefused)
	require.ErrorIs(t, child.BindChildSessionID(""), agent.ErrChildIdentityRefused)
	require.NoError(t, child.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		agent.MessageContent{Original: []byte(`{"content":"The provisional child prompt"}`)}, agent.SpanInfo{}))
	_, err = svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_child_session BEFORE UPDATE OF agent_session_id ON agents
WHEN NEW.agent_session_id = 'native-child' BEGIN SELECT RAISE(ABORT, 'child session write refused'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, child.BindChildSessionID("native-child"), "child session write refused")
	assert.Empty(t, child.MessageSessionID())
	_, err = svc.DB.ExecContext(t.Context(), `DROP TRIGGER refuse_child_session`)
	require.NoError(t, err)

	require.NoError(t, child.BindChildSessionID("native-child"))
	require.NoError(t, child.BindChildSessionID("native-child"))
	require.ErrorIs(t, child.BindChildSessionID("other-child"), agent.ErrChildIdentityRefused)
	assert.Equal(t, "native-child", child.MessageSessionID())
	assert.Equal(t, "native-root", root.MessageSessionID())
	stored, err := svc.Queries.GetAgentByID(t.Context(), childID)
	require.NoError(t, err)
	assert.Equal(t, "native-child", stored.AgentSessionID)
	assert.Zero(t, stored.SessionStartSeq, "learning this session's ID preserves the original session boundary")
	require.NoError(t, child.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"assistant"}`)}, agent.SpanInfo{}))
	rows, err := svc.Queries.ListMessagesByAgentID(t.Context(), db.ListMessagesByAgentIDParams{AgentID: childID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Empty(t, rows[0].AgentSessionID)
	assert.Equal(t, "native-child", rows[1].AgentSessionID)
}
