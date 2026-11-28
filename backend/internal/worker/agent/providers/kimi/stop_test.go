package kimi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestKimiProcessExitReleasesRootCompaction(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.HandleOutput(kimiCompactingStateFrame(t, kimiMainAgentID, true))
	active, published := rig.sink.LastTurnActive()
	require.True(t, published)
	require.True(t, active, "manual compaction holds the queue before process exit")

	rig.agent.SimulateExitForTest()
	require.NoError(t, rig.agent.Wait())
	active, published = rig.sink.LastTurnActive()
	require.True(t, published)
	assert.False(t, active, "a dead process cannot own a compaction turn")
	rig.agent.Mu.Lock()
	compacting := rig.agent.runs[kimiMainAgentID].compacting
	rig.agent.Mu.Unlock()
	assert.False(t, compacting, "process exit clears the native compaction state")
}

// exitOnShutdown makes the rig's process exit when the fake receives
// POST /shutdown, as the real server does. The rig runs no process, so nothing
// else ends it.
func exitOnShutdown(rig *kimiTestRig) {
	rig.fake.mu.Lock()
	defer rig.fake.mu.Unlock()
	rig.fake.onShutdown = rig.agent.SimulateExitForTest
}

func TestKimiClosesTheEventStreamBeforeNativeShutdown(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	closedAtShutdown := make(chan bool, 1)
	rig.fake.reply("POST "+kimiRouteShutdown, fakeKapReply{BeforeReply: func() {
		rig.agent.stream.mu.Lock()
		closedAtShutdown <- rig.agent.stream.closed
		rig.agent.stream.mu.Unlock()
		rig.agent.SimulateExitForTest()
	}})
	rig.agent.Stop()
	require.Len(t, closedAtShutdown, 1)
	assert.True(t, <-closedAtShutdown, "the stream must not start another connection during shutdown")
}

func TestKimiStop(t *testing.T) {
	t.Parallel()

	t.Run("an idle agent aborts nothing and shuts the server down once", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		exitOnShutdown(rig)
		rig.agent.Stop()
		assert.Len(t, rig.fake.requestsTo("POST "+kimiRouteShutdown), 1)
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath("session_1", kimiActionAbort)), "no turn runs")
		assert.True(t, rig.agent.IsStopped())
		require.Error(t, rig.agent.stream.write(map[string]any{"type": "noop"}), "the stream closes with the stop")

		before := len(rig.fake.routes())
		rig.agent.Stop()
		assert.Len(t, rig.fake.routes(), before, "a second stop sends nothing")
	})

	t.Run("an abort that fails does not keep the server running", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		exitOnShutdown(rig)
		rig.startPublicMainTurn(t, "t0", 0, `{"kind":"user"}`)
		rig.fake.reply("POST "+kimiSessionPath("session_1", kimiActionAbort), fakeKapReply{Code: 50000, Msg: "abort failed"})
		rig.agent.Stop()
		assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath("session_1", kimiActionAbort)), 1)
		assert.Len(t, rig.fake.requestsTo("POST "+kimiRouteShutdown), 1)
	})

	t.Run("an agent that never connected stops", func(t *testing.T) {
		t.Parallel()
		a := newOfflineKimiAgent(t, &agenttest.Sink{})
		a.turnActive = true
		a.SimulateExitForTest()
		assert.NotPanics(t, a.Stop, "a stop after a failed start has no client, stream or endpoint")
		assert.True(t, a.IsStopped())
	})
}

func TestKimiInterrupt(t *testing.T) {
	t.Parallel()

	t.Run("reports an abort the server refuses", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.startPublicMainTurn(t, "t0", 0, `{"kind":"user"}`)
		rig.fake.reply("POST "+kimiSessionPath("session_1", kimiActionAbort), fakeKapReply{Code: 40401, Msg: "no running turn"})
		require.ErrorContains(t, rig.agent.Interrupt(agent.StopContext{}), "no running turn")
	})

	t.Run("refuses a session id that Kimi Code does not issue", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.startPublicMainTurn(t, "t0", 0, `{"kind":"user"}`)
		rig.agent.Mu.Lock()
		rig.agent.sessionID = "../other"
		rig.agent.Mu.Unlock()
		before := len(rig.fake.routes())
		require.ErrorContains(t, rig.agent.Interrupt(agent.StopContext{}), "session id")
		assert.Len(t, rig.fake.routes(), before)
	})
}

func TestKimiStopAndWaitWithdrawPendingControlsByInstanceID(t *testing.T) {
	t.Parallel()

	t.Run("Stop withdraws pending controls by instance ID and Wait stays idempotent", func(t *testing.T) {
		t.Parallel()
		sink := &kimiInstanceCancelSink{}
		a := newOfflineKimiAgent(t, sink)
		sink.UpdateSessionID("session_1")
		a.HandleOutput(kimiEventFrame(t, "session_1", approvalEvent("approval_b", map[string]any{"kind": "command", "command": "ls"})))
		instB := sink.LastPublishedControl().InstanceID
		a.HandleOutput(kimiEventFrame(t, "session_1", questionEvent("question_a")))
		instA := sink.LastPublishedControl().InstanceID
		// Answered controls must not be withdrawn.
		a.Mu.Lock()
		a.controls["approval_answered"] = publishedKimiControl(kimiControlApproval, "session_1", true)
		a.Mu.Unlock()

		a.SimulateExitForTest()
		a.Stop()
		unscoped, scoped := sink.cancelSnapshots()
		assert.Empty(t, unscoped)
		assert.Equal(t, []kimiInstanceCancelCall{
			{requestID: "approval_b", instanceID: instB},
			{requestID: "question_a", instanceID: instA},
		}, scoped)

		require.NoError(t, a.Wait())
		unscoped, scoped = sink.cancelSnapshots()
		assert.Empty(t, unscoped)
		assert.Len(t, scoped, 2, "Wait after Stop must not withdraw controls a second time")
	})

	t.Run("Wait withdraws pending controls after unexpected exit", func(t *testing.T) {
		t.Parallel()
		sink := &kimiInstanceCancelSink{}
		a := newOfflineKimiAgent(t, sink)
		sink.UpdateSessionID("session_1")
		a.HandleOutput(kimiEventFrame(t, "session_1", approvalEvent("approval_1", map[string]any{"kind": contracts.KimiDisplayGoalStart})))
		inst := sink.LastPublishedControl().InstanceID

		a.SimulateExitForTest()
		require.NoError(t, a.Wait())
		unscoped, scoped := sink.cancelSnapshots()
		assert.Empty(t, unscoped)
		assert.Equal(t, []kimiInstanceCancelCall{{requestID: "approval_1", instanceID: inst}}, scoped)
	})
}
