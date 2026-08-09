package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// setupChildAgentTest provisions a root + child agent pair for child-routing
// tests. The child is linked to the root via a background-task registry row so
// SendAgentMessage/InterruptAgent can resolve (ownerID, rowKey). Returns the
// service, dispatcher, the child agent id, and the root agent id.
func setupChildAgentTest(t *testing.T) (*Service, *channel.Dispatcher, string, string) {
	t.Helper()
	ctx := context.Background()
	svc, d, _ := setupTestService(t)

	// Root agent (Codex supports child steering).
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	sink := svc.Output.NewSink("root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	// EnsureChildAgent creates the child row + registry row in one idempotent
	// call; the returned id is the virtual child agent id.
	childID, err := sink.EnsureChildAgent("spawn-span-1", "row-key-1", "child task")
	require.NoError(t, err)
	require.NotEmpty(t, childID)

	return svc, d, childID, "root-1"
}

// TestSendAgentMessageToChildOwnerNotRunningPersistsDeliveryError verifies the
// owner-not-running branch of child routing: the user message is persisted into
// the CHILD transcript stamped with a delivery error (not silently dropped).
// This path exercises the shared persistChildUserRowWithDeliveryError helper
// that also backs the SendChildInput-failure branch.
func TestSendAgentMessageToChildOwnerNotRunningPersistsDeliveryError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, childID, _ := setupChildAgentTest(t)

	// Register a watch on the CHILD so the broadcast is captured.
	wWatch := newTestWriter()
	registerAgentWatch(svc, wWatch.channelID, childID, leapmuxv1.WatchMode_WATCH_MODE_FULL, wWatch)

	w := newTestWriter()
	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: childID,
		Content: "hello child",
	}, w)
	require.Empty(t, w.errors, "the handler acks (it persists with a delivery error)")

	// The user row landed in the CHILD transcript with a delivery error.
	msgs, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{
		AgentID: childID, Seq: 0,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 1, "one user row persisted into the child transcript")
	assert.Equal(t, "agent is not running", msgs[0].DeliveryError,
		"delivery error must explain the owner process is not running")

	// A broadcast carried the message with the same delivery error.
	foundDeliveryErr := false
	for _, stream := range wWatch.streamsSnapshot() {
		ev := decodeWatchAgentEvent(t, stream)
		if am := ev.GetAgentMessage(); am != nil && am.GetDeliveryError() == "agent is not running" {
			foundDeliveryErr = true
		}
	}
	assert.True(t, foundDeliveryErr, "the delivery-error broadcast must reach the child watch")
}

// TestSendAgentMessageToChildSlashCommandRejected verifies slash commands are
// rejected for child targets (InvalidArgument) before any persistence.
func TestSendAgentMessageToChildSlashCommandRejected(t *testing.T) {
	t.Parallel()

	_, d, childID, _ := setupChildAgentTest(t)

	w := newTestWriter()
	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: childID,
		Content: "/clear",
	}, w)

	rejs := w.rejections()
	require.Len(t, rejs, 1)
	assert.Contains(t, rejs[0].message, "slash commands")
}

// TestSendAgentMessageToChildMissingRegistryRowRejected verifies a child with no
// registry row linking it to an owner is rejected (FailedPrecondition) -- there
// is no way to steer it.
// TestSendAgentMessageToChildMissingRegistryRowReturnsUnavailable verifies that a
// child send against a transient registry miss (the child agent row exists but
// the registry upsert hasn't replayed yet after a worker restart) returns
// UNAVAILABLE so the frontend re-queues — NOT a hard FailedPrecondition.
func TestSendAgentMessageToChildMissingRegistryRowReturnsUnavailable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, _ := setupTestService(t)

	// A root + a child agent with NO registry row linking them.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-x",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:            "orphan-child",
		ParentAgentID: sql.NullString{String: "root-x", Valid: true},
		SpawnSpanID:   "span-x",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	w := newTestWriter()
	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: "orphan-child",
		Content: "hello",
	}, w)

	rejs := w.rejections()
	require.Len(t, rejs, 1)
	// A transient registry miss is UNAVAILABLE (retry), not FailedPrecondition.
	assert.Equal(t, int32(codes.Unavailable), rejs[0].code)
	assert.Contains(t, rejs[0].message, "not yet loaded")
}

// TestCloseAgentOnChildKeepsRowAndTranscript verifies closing a child tab is
// tab-only: the child row is NOT marked closed_at and the transcript + registry
// row survive.
func TestCloseAgentOnChildKeepsRowAndTranscript(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, childID, _ := setupChildAgentTest(t)

	// Seed a message into the child transcript so we can confirm it survives.
	_, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "cm-1",
		AgentID:       childID,
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte("keep me"),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	})
	require.NoError(t, err)

	w := newTestWriter()
	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{
		AgentId: childID,
	}, w)
	require.Empty(t, w.errors)

	// The child row must NOT be marked closed_at.
	child, err := svc.Queries.GetAgentByID(ctx, childID)
	require.NoError(t, err)
	assert.False(t, child.ClosedAt.Valid, "closing a child tab must not stamp closed_at")

	// The transcript survives.
	msgs, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{
		AgentID: childID, Seq: 0,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 1, "the child transcript survives a tab close")

	// The registry row survives too.
	rows, err := svc.Queries.ListAgentBackgroundTasks(ctx, db.ListAgentBackgroundTasksParams{
		OwnerAgentID: "root-1", Limit: 100,
	})
	require.NoError(t, err)
	require.NotEmpty(t, rows, "the registry row survives a child tab close")
}

// TestListAgentMessagesChildReturnsEmptyTasks is a lightweight cross-check that
// the BackgroundTasksLoaded contract holds end-to-end for a registry-backed
// child. (The dedicated test in list_messages_anchor_test.go covers the
// loaded=true assertion against the root's seeded task in full.)
func TestListAgentMessagesChildReturnsEmptyTasks(t *testing.T) {
	t.Parallel()

	svc, d, childID, _ := setupChildAgentTest(t)

	// Seed a task on the ROOT so a leak would be observable on the child.
	require.NoError(t, svc.Output.NewSink("root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX).
		UpsertBackgroundTask(bgtask.Upsert{
			RowKey: "rtask", Kind: bgtask.KindShell, Title: "r", Status: bgtask.StatusRunning,
		}))

	// The root has a registry row; the child must not inherit it.
	w := newTestWriter()
	dispatch(d, "ListAgentMessages", &leapmuxv1.ListAgentMessagesRequest{
		AgentId: childID,
		Anchor:  leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST,
		Limit:   10,
	}, w)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentMessagesResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	assert.Empty(t, resp.GetBackgroundTasks())
	assert.True(t, resp.GetBackgroundTasksLoaded(),
		"child LATEST page must report loaded=true (empty-but-loaded)")
}

// TestInterruptAgentOnChildRoutesViaChildSteerer verifies InterruptAgent on a
// child agent routes through the child-steering path (Agents.InterruptChild on
// the owner) rather than interrupting the child id directly. The strongest
// feasible service-level assertion uses the rejection disposition unique to
// that path: a RUNNING owner that does NOT implement ChildSteerer (the mock
// owner here is a ClaudeCodeAgent, which never steers) makes InterruptChild
// return ErrChildSteeringUnsupported, which the handler maps to
// FailedPrecondition "this subagent cannot be interrupted". That arm is ONLY
// reachable through InterruptChild -- the direct-interrupt arm (owner not
// running, or a non-child target) produces NotFound instead -- so observing it
// proves the routing went via Agents.InterruptChild(ownerID, rowKey).
//
// A companion subtest covers the owner-not-running arm: with no mock owner
// registered, InterruptChild returns ErrAgentNotFound and the handler reports
// NotFound "agent not found or not running" (still via the child path, not a
// direct Interrupt on the child id).
func TestInterruptAgentOnChildRoutesViaChildSteerer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("runningOwnerWithoutChildSteererReportsFailedPrecondition", func(t *testing.T) {
		t.Parallel()
		svc, d, childID, rootID := setupChildAgentTest(t)

		// Start a mock owner process. MockStartAgent wraps it as a
		// ClaudeCodeAgent, which does NOT implement ChildSteerer -- so
		// InterruptChild returns ErrChildSteeringUnsupported, the unique
		// FailedPrecondition disposition that proves the child-steering path
		// was taken (rather than Agents.Interrupt on the child id).
		_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
			AgentID:    rootID,
			WorkingDir: t.TempDir(),
			HomeDir:    t.TempDir(),
		}, svc.Output.NewSink(rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX))
		require.NoError(t, err)
		defer svc.Agents.StopAgent(rootID)

		w := newTestWriter()
		dispatch(d, "InterruptAgent", &leapmuxv1.InterruptAgentRequest{
			AgentId: childID,
		}, w)

		rejs := w.rejections()
		require.Len(t, rejs, 1, "the child-steering-unsupported path must reject")
		assert.Equal(t, int32(codes.FailedPrecondition), rejs[0].code,
			"ErrChildSteeringUnsupported maps to FailedPrecondition, proving InterruptChild routing")
		assert.Contains(t, rejs[0].message, "cannot be interrupted")
	})

	t.Run("ownerNotRunningReportsNotFoundViaChildPath", func(t *testing.T) {
		t.Parallel()
		// No mock owner is registered, so Agents.InterruptChild(ownerID, rowKey)
		// returns ErrAgentNotFound (the owner has no process); the handler maps
		// that to NotFound. This is the child-routing NotFound arm -- a direct
		// Interrupt on a non-child root id would only run AFTER the
		// ParentAgentID check, which this child never reaches.
		_, d, childID, _ := setupChildAgentTest(t)

		w := newTestWriter()
		dispatch(d, "InterruptAgent", &leapmuxv1.InterruptAgentRequest{
			AgentId: childID,
		}, w)

		rejs := w.rejections()
		require.Len(t, rejs, 1)
		assert.Equal(t, int32(codes.NotFound), rejs[0].code,
			"owner-not-running routes through InterruptChild -> ErrAgentNotFound -> NotFound")
		assert.Contains(t, rejs[0].message, "not found or not running")
	})
}

// TestEnsureAgentRunningRefusesChildAgent verifies that a child agent (a
// subagent transcript) is refused on the raw-control surface. SendAgentRawMessage
// rejects children with FailedPrecondition ("this subagent cannot be messaged")
// before any process spawn -- a child never owns a process. This guards the
// ensureAgentRunning choke point indirectly: SendAgentRawMessage is the
// dispatchable entry that refuses children at the service boundary.
func TestEnsureAgentRunningRefusesChildAgent(t *testing.T) {
	t.Parallel()

	_, d, childID, _ := setupChildAgentTest(t)

	w := newTestWriter()
	dispatch(d, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
		AgentId: childID,
		Content: `{"jsonrpc":"2.0","method":"session/interrupt"}`,
	}, w)

	rejs := w.rejections()
	require.Len(t, rejs, 1, "a child agent must be refused on the raw-control surface")
	assert.Equal(t, int32(codes.FailedPrecondition), rejs[0].code)
	assert.Contains(t, rejs[0].message, "subagent cannot be messaged",
		"the rejection message must explain the child has no process of its own")
}

// TestUpdateAgentSettingsRejectsChildAgent verifies a settings change against a
// virtual child is rejected before the optimistic DB write: a child has no
// process and no settings of its own (it inherits the owner's), so persisting
// divergent options to the child row would store options no live process honors.
func TestUpdateAgentSettingsRejectsChildAgent(t *testing.T) {
	t.Parallel()

	_, d, childID, _ := setupChildAgentTest(t)

	w := newTestWriter()
	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: childID,
		Settings: &leapmuxv1.AgentSettings{
			Options: map[string]string{"model": "gpt-5"},
		},
	}, w)

	rejs := w.rejections()
	require.Len(t, rejs, 1, "a child agent must reject settings changes")
	assert.Equal(t, int32(codes.FailedPrecondition), rejs[0].code)
	assert.Contains(t, rejs[0].message, "subagent",
		"the rejection message must explain a child has no settings of its own")
}

// TestAgentToProto_RootAgentIdResolved verifies agentToProto stamps the
// root_agent_id wire field: a root maps to its own id, a child maps to the
// resolved root owner. The frontend reads the registry owner and the NOTIFY
// subscription from this field instead of walking a client-side chain.
func TestAgentToProto_RootAgentIdResolved(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, childID, rootID := setupChildAgentTest(t)

	rootRow, err := svc.Queries.GetAgentByID(ctx, rootID)
	require.NoError(t, err)
	rootInfo := svc.agentToProto(&rootRow, false, nil)
	assert.Equal(t, rootID, rootInfo.GetRootAgentId(), "a root's root_agent_id is its own id")

	childRow, err := svc.Queries.GetAgentByID(ctx, childID)
	require.NoError(t, err)
	childInfo := svc.agentToProto(&childRow, false, nil)
	assert.Equal(t, rootID, childInfo.GetRootAgentId(),
		"a child's root_agent_id resolves up the parent chain to the root owner")
}

// TestCloseAgentOnRootClosesDescendantsAndMarksTasksStopped verifies closing a
// ROOT agent tears down the whole tree: every descendant child row is stamped
// closed_at (via ListAgentTreeIDs + CloseAgent), each descendant's span tracker
// is cleaned up (CleanupAgent), and every still-active background-task row owned
// by the root is terminalized as 'stopped' (MarkAgentBackgroundTasksExited with
// stopped=true). This mirrors the existing child-close test but exercises the
// ROOT close path in closeAgentTabCommon.rootTeardown/rootClose.
func TestCloseAgentOnRootClosesDescendantsAndMarksTasksStopped(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, childID, rootID := setupChildAgentTest(t)

	// Seed an ACTIVE registry row under the root so the 'stopped' terminalization
	// is observable. setupChildAgentTest already inserted the spawn row via
	// EnsureChildAgent; assert it is present and active, then add a second
	// running shell row to exercise a non-subagent active row too.
	require.NoError(t, svc.Output.NewSink(rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX).
		UpsertBackgroundTask(bgtask.Upsert{
			RowKey: "bg-shell-1", Kind: bgtask.KindShell, Title: "bg shell",
			Status: bgtask.StatusRunning,
		}))

	rowsBefore, err := svc.Queries.ListAgentBackgroundTasks(ctx, db.ListAgentBackgroundTasksParams{
		OwnerAgentID: rootID, Limit: 100,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(rowsBefore), 2, "spawn row + seeded shell row")
	hasActiveBefore := false
	for _, r := range rowsBefore {
		if !bgtask.StatusFromWire(r.Status).IsTerminal() {
			hasActiveBefore = true
		}
	}
	require.True(t, hasActiveBefore, "there must be at least one active row before the close")

	// Precondition: neither row is closed yet.
	rootBefore, err := svc.Queries.GetAgentByID(ctx, rootID)
	require.NoError(t, err)
	require.False(t, rootBefore.ClosedAt.Valid, "root must start open")
	childBefore, err := svc.Queries.GetAgentByID(ctx, childID)
	require.NoError(t, err)
	require.False(t, childBefore.ClosedAt.Valid, "child must start open")

	// Close the ROOT (not the child tab). This drives rootTeardown +
	// rootClose: CloseAgent over the whole tree id list, CleanupAgent per
	// descendant, and MarkAgentBackgroundTasksExited(root, stopped=true).
	w := newTestWriter()
	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{
		AgentId: rootID,
	}, w)
	require.Empty(t, w.errors, "CloseAgent on the root must succeed")

	// Both the root and the descendant child are stamped closed_at.
	rootAfter, err := svc.Queries.GetAgentByID(ctx, rootID)
	require.NoError(t, err)
	assert.True(t, rootAfter.ClosedAt.Valid, "closing the root must stamp closed_at on the root")
	childAfter, err := svc.Queries.GetAgentByID(ctx, childID)
	require.NoError(t, err)
	assert.True(t, childAfter.ClosedAt.Valid,
		"closing the root must stamp closed_at on every descendant child too")

	// Every background-task row owned by the root is now terminal, and the
	// previously-active rows are 'stopped' (the explicit-close disposition).
	rowsAfter, err := svc.Queries.ListAgentBackgroundTasks(ctx, db.ListAgentBackgroundTasksParams{
		OwnerAgentID: rootID, Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, rowsAfter, len(rowsBefore),
		"rows are retained (not deleted) -- only terminalized")
	for _, r := range rowsAfter {
		status := bgtask.StatusFromWire(r.Status)
		assert.True(t, status.IsTerminal(),
			"row %s must be terminal after root close, got %s", r.RowKey, r.Status)
		if r.RowKey == "bg-shell-1" {
			assert.Equal(t, bgtask.StatusStopped, status,
				"the active shell row must be terminalized as 'stopped' (explicit close)")
		}
	}
}
