package kimi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestKimiWaitSettlesWhatTheProcessLeftOpen(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		stopped    bool
		completion agent.MessageCompletion
	}{
		"a server that died":    {stopped: false, completion: agent.MessageCompletionError},
		"a server that stopped": {stopped: true, completion: agent.MessageCompletionInterrupted},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.transcriptOps(t, 1,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`,
				`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"call_1","toolCallId":"call_1","name":"Bash","state":"running","input":{"command":"sleep 60"}}}`,
				`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"call_child","name":"Agent","state":"running","input":{"prompt":"Review."},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
			rig.transcriptAgentOps(t, "agent-0", 1,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"other"},"prompt":"Review."}}`,
				`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"read","toolCallId":"read","name":"Read","state":"running","input":{"path":"a.go"}}}`,
				`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"Found it."}}`)
			rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"Half an answer"}}`)
			_, child := childOf(t, rig.sink, "session_1/agent-0")
			childBefore := len(child.Messages())

			rig.agent.SetStoppedForTest(tc.stopped)
			rig.agent.SimulateExitForTest()
			require.NoError(t, rig.agent.Wait())

			var text, call bool
			for _, message := range rig.sink.Messages() {
				if message.SpanID == "session_1/"+kimiTranscriptScope("main", "t0", "t0.1", "call_1") && message.Closing {
					call = true
					assert.Equal(t, tc.completion, message.Completion, "the open call states how the process ended")
				}
				var row map[string]string
				if json.Unmarshal(message.Content, &row) == nil && row[contracts.AssembledMessageFieldType] == contracts.AssembledMessageType &&
					row[contracts.AssembledMessageFieldKind] == string(agent.AssembledMessageKindText) {
					text = true
					assert.Equal(t, "Half an answer", row[contracts.AssembledMessageFieldText])
					assert.Equal(t, string(tc.completion), row[contracts.AssembledMessageFieldCompletion])
				}
			}
			assert.True(t, text, "the text the model streamed is not lost")
			assert.True(t, call, "the open call does not stay running")
			assert.Greater(t, len(child.Messages()), childBefore, "the subagent's streamed text reaches its own transcript")
			assert.Equal(t, []bool{true, false}, child.TurnActives(), "the subagent's tab stops showing a turn")
			last, published := rig.sink.LastTurnActive()
			require.True(t, published)
			assert.False(t, last, "the main turn ends with the process")

			// The manager can call Wait a second time. It settles nothing twice.
			count := rig.sink.MessageCount()
			require.NoError(t, rig.agent.Wait())
			assert.Equal(t, count, rig.sink.MessageCount())
		})
	}
}

func TestKimiProcessExitReleasesManualCompaction(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{"trigger": "manual", "phase": "started"})
	active, published := rig.sink.LastTurnActive()
	require.True(t, published)
	require.True(t, active, "manual compaction holds the queue before process exit")

	rig.agent.SimulateExitForTest()
	require.NoError(t, rig.agent.Wait())
	active, published = rig.sink.LastTurnActive()
	require.True(t, published)
	assert.False(t, active, "a dead process cannot own a compaction turn")
	rig.agent.Mu.Lock()
	manual := rig.agent.manualCompactionActive
	rig.agent.Mu.Unlock()
	assert.False(t, manual, "process exit clears the native compaction state")
}

// exitOnShutdown makes the rig's process exit when the fake receives
// POST /shutdown, as the real server does. The rig runs no process, so nothing
// else ends it.
func exitOnShutdown(rig *kimiTestRig) {
	rig.fake.mu.Lock()
	defer rig.fake.mu.Unlock()
	rig.fake.onShutdown = rig.agent.SimulateExitForTest
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
		rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user","payload":{"kind":"user"}}}}`)
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
		rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user","payload":{"kind":"user"}}}}`)
		rig.fake.reply("POST "+kimiSessionPath("session_1", kimiActionAbort), fakeKapReply{Code: 40401, Msg: "no running turn"})
		require.ErrorContains(t, rig.agent.Interrupt(agent.StopContext{}), "no running turn")
	})

	t.Run("refuses a session id that Kimi Code does not issue", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user","payload":{"kind":"user"}}}}`)
		rig.agent.Mu.Lock()
		rig.agent.sessionID = "../other"
		rig.agent.Mu.Unlock()
		before := len(rig.fake.routes())
		require.ErrorContains(t, rig.agent.Interrupt(agent.StopContext{}), "session id")
		assert.Len(t, rig.fake.routes(), before)
	})
}
