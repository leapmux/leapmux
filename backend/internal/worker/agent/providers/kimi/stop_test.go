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
			rig.startTurn(t, 0, contracts.KimiOriginUser)
			rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "call_1",
				"name": contracts.KimiToolBash, "args": map[string]any{"command": "sleep 60"}})
			spawnAgent(t, rig, "agent-0", "call_child", nil)
			// A tool call flushes the text before it, so the text the process leaves
			// behind is what streamed after the last call started.
			rig.feed(t, map[string]any{"type": contracts.KimiEventAssistantDelta, "turnId": 0, "delta": "Half an answer"})
			_, child := childOf(t, rig.sink, "session_1/agent-0")
			childBefore := len(child.Messages())

			rig.agent.SetStoppedForTest(tc.stopped)
			rig.agent.SimulateExitForTest()
			require.NoError(t, rig.agent.Wait())

			var text, call bool
			for _, message := range rig.sink.Messages() {
				if message.SpanID == kimiSpanID("session_1", kimiMainAgentID, 0, "call_1") && message.Closing {
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
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
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
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
		rig.fake.reply("POST "+kimiSessionPath("session_1", kimiActionAbort), fakeKapReply{Code: 40401, Msg: "no running turn"})
		require.ErrorContains(t, rig.agent.Interrupt(), "no running turn")
	})

	t.Run("refuses a session id that Kimi Code does not issue", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
		rig.agent.Mu.Lock()
		rig.agent.sessionID = "../other"
		rig.agent.Mu.Unlock()
		before := len(rig.fake.routes())
		require.ErrorContains(t, rig.agent.Interrupt(), "session id")
		assert.Len(t, rig.fake.routes(), before)
	})
}
