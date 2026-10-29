package letta

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
)

// compactionVerdictOps is the retry operation that the end of one manual
// compaction applies: CompactionEnd's cancellation of the server-error retry.
func compactionVerdictOps() []agenttest.TimelineEntry {
	reference := &agenttest.Sink{}
	providerkit.ApplyTurnFailure(reference, providerkit.CompactionEnd(), nil)
	return reference.Timeline()
}

func newLettaCompactionTestAgent(t *testing.T) (*fakeAppServer, *Agent, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	fake, a := newLettaCompactionTestAgentWithSink(t, sink)
	return fake, a, sink
}

func newLettaCompactionTestAgentWithSink(t *testing.T, sink *agenttest.Sink) (*fakeAppServer, *Agent) {
	t.Helper()
	fake, a := newFakeAppServerWithSink(t, sink)
	a.Mu.Lock()
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	a.Mu.Unlock()
	a.sink = agent.NewModelProgressResetSink(a.sink)
	return fake, a
}

func compactDelta(t *testing.T, kind, input string, success *bool) []byte {
	t.Helper()
	delta := map[string]any{"message_type": kind, "command_id": "compact", "input": input, "id": kind + "-probe"}
	if success != nil {
		delta["success"] = *success
		delta["output"] = "Native compaction output."
	}
	raw, err := json.Marshal(map[string]any{
		"type": "stream_delta", "runtime": map[string]any{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"}, "delta": delta,
	})
	require.NoError(t, err)
	return raw
}

func compactResponse(t *testing.T, id any, success any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": "execute_command_response", "request_id": id, "success": success, "output": "Native command outcome."})
	require.NoError(t, err)
	return raw
}

func assertLettaActive(t *testing.T, sink *agenttest.Sink, expected bool) {
	t.Helper()
	active, published := sink.LastTurnActive()
	require.True(t, published)
	assert.Equal(t, expected, active)
}

func TestLettaManualCompactionCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ input, args string }{
		{input: "/compact"},
		{input: "/compact all", args: "all"},
		{input: " \t/compact\t sliding_window  ", args: "sliding_window"},
		{input: "/compact invalid-native-mode", args: "invalid-native-mode"},
		{input: "/compact help", args: "help"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			fake, a, sink := newLettaCompactionTestAgent(t)
			require.NoError(t, a.SendInput(tc.input, nil))
			command := fake.nextCommand(t)
			assert.Equal(t, "execute_command", command["type"])
			assert.Equal(t, "compact", command["command_id"])
			if tc.args == "" {
				assert.NotContains(t, command, "args")
			} else {
				assert.Equal(t, tc.args, command["args"])
			}
			assert.NotContains(t, command, "payload")
			assert.Equal(t, map[string]any{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"}, command["runtime"])
			assert.NotEmpty(t, command["request_id"])
			assert.NotEmpty(t, command["idempotency_key"])
			assert.NotEmpty(t, command["emitted_at"])
			assert.Positive(t, command["event_seq"])
			assertLettaActive(t, sink, true)
		})
	}
}

func TestLettaManualCompactionOrdinaryInputs(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "Explain /compact", "/compactly", "/compact", "/compact help"} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			fake, a, _ := newLettaCompactionTestAgent(t)
			var attachments []*leapmuxv1.Attachment
			if text == "/compact" || text == "/compact help" {
				attachments = []*leapmuxv1.Attachment{{Filename: "note.txt", MimeType: "text/plain", Data: []byte("Attached context")}}
			}
			require.NoError(t, a.SendInput(text, attachments))
			command := fake.nextCommand(t)
			assert.Equal(t, "input", command["type"])
			payload, ok := command["payload"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "create_message", payload["kind"])
			assert.NotContains(t, command, "command_id")
		})
	}
}

type lettaCompactionReleaseSink struct {
	*agenttest.Sink
	noticesAtRelease []agenttest.Message
}

func (s *lettaCompactionReleaseSink) SetTurnState(state agent.TurnState, seq uint64) {
	if !state.Active {
		s.noticesAtRelease = s.PersistedNotifications()
	}
	s.Sink.SetTurnState(state, seq)
}

func TestLettaManualCompactionLifecycle(t *testing.T) {
	t.Parallel()
	fake, a, sink := newLettaCompactionTestAgent(t)
	observed := &lettaCompactionReleaseSink{Sink: sink}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(observed))
	require.NoError(t, a.SendInput("/compact all", nil))
	command := fake.nextCommand(t)
	a.HandleOutput(compactDelta(t, "slash_command_start", "/compact all", nil))
	assert.Equal(t, []any{map[string]any{"trigger": "manual"}}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	success := true
	end := compactDelta(t, "slash_command_end", "/compact all", &success)
	a.HandleOutput(end)
	assertLettaActive(t, sink, true)
	assert.Equal(t, []any{map[string]any{"trigger": "manual"}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	for _, unknown := range []any{"other-request", "", nil, 0} {
		a.HandleOutput(compactResponse(t, unknown, true))
		assertLettaActive(t, sink, true)
	}
	a.HandleOutput([]byte(lettaLiveTurnFinished))
	assertLettaActive(t, sink, true)
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()), "a running compaction applies no verdict")
	a.HandleOutput(compactResponse(t, command["request_id"], true))
	assertLettaActive(t, sink, false)
	assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	require.Len(t, observed.noticesAtRelease, 1)
	var envelope struct {
		Delta json.RawMessage `json:"delta"`
	}
	require.NoError(t, json.Unmarshal(end, &envelope))
	assert.JSONEq(t, string(envelope.Delta), string(observed.noticesAtRelease[0].Content))

	require.NoError(t, a.SendInput("Continue after compaction.", nil))
	assert.Equal(t, "input", fake.nextCommand(t)["type"])
	a.HandleOutput(compactResponse(t, command["request_id"], true))
	assertLettaActive(t, sink, true)
	assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()), "a replayed reply applies no second verdict")
}

func TestLettaManualCompactionFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		success    any
		nativeEnd  bool
		help       bool
		processEnd bool
	}{
		{name: "failed native end", success: false, nativeEnd: true},
		{name: "failed reply without end", success: false},
		{name: "success without end", success: true},
		{name: "missing success", success: nil},
		{name: "invalid success", success: "true"},
		{name: "help", success: true, nativeEnd: true, help: true},
		{name: "process exit", processEnd: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake, a, sink := newLettaCompactionTestAgent(t)
			input := "/compact"
			if tc.help {
				input += " help"
			}
			require.NoError(t, a.SendInput(input, nil))
			command := fake.nextCommand(t)
			a.HandleOutput(compactDelta(t, "slash_command_start", input, nil))
			if tc.nativeEnd {
				success, ok := tc.success.(bool)
				require.True(t, ok)
				a.HandleOutput(compactDelta(t, "slash_command_end", input, &success))
			}
			if tc.processEnd {
				a.SetStoppedForTest(true)
				a.finishOutputAfterExit(agent.MessageCompletionError)
			} else {
				a.HandleOutput(compactResponse(t, command["request_id"], tc.success))
			}
			assertLettaActive(t, sink, false)
			assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			if tc.help {
				assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
				assert.Empty(t, sink.LeapMuxNotifications())
			} else {
				states := sink.SessionInfoValues(contracts.SessionInfoKeyCompaction)
				require.NotEmpty(t, states)
				assert.Nil(t, states[len(states)-1])
				if !tc.nativeEnd {
					notices := sink.LeapMuxNotifications()
					require.Len(t, notices, 1)
					assert.Equal(t, contracts.NotificationTypeCompactionEnded, notices[0][contracts.NotificationFieldType])
					if tc.success != true {
						assert.Equal(t, contracts.CompactionEndOutcomeFailed, notices[0][contracts.NotificationFieldOutcome])
					}
				}
			}
		})
	}
}

func TestLettaManualCompactionInterrupt(t *testing.T) {
	t.Parallel()
	fake, a, sink := newLettaCompactionTestAgent(t)
	require.NoError(t, a.SendInput("/compact", nil))
	command := fake.nextCommand(t)
	interruptErr := a.Interrupt(agent.StopContext{})
	require.ErrorContains(t, interruptErr, "cannot interrupt compaction")
	var unavailable *agent.InterruptUnavailableError
	require.ErrorAs(t, interruptErr, &unavailable)
	assertLettaActive(t, sink, true)
	// A later socket write proves that no ignored abort was sent before this barrier.
	require.NoError(t, a.sendCommand(newLettaCommand("app_server_info", "barrier")))
	assert.Equal(t, "app_server_info", fake.nextCommand(t)["type"])
	a.HandleOutput(compactResponse(t, command["request_id"], true))
	assertLettaActive(t, sink, false)
	assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

func TestLettaManualCompactionAdmission(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing runtime", "stopped", "busy", "stale session"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fake, a, _ := newLettaCompactionTestAgent(t)
			switch mode {
			case "missing runtime":
				a.Mu.Lock()
				a.agentID = ""
				a.Mu.Unlock()
			case "stopped":
				a.SetStoppedForTest(true)
			case "busy":
				a.setTurnActive(true)
			}
			var err error
			if mode == "stale session" {
				err = a.SendInputForSession("other", "/compact", nil)
			} else {
				err = a.SendInput("/compact", nil)
			}
			require.Error(t, err)
			require.NoError(t, a.sendCommand(newLettaCommand("app_server_info", "barrier")))
			assert.Equal(t, "app_server_info", fake.nextCommand(t)["type"])
		})
	}
}

func TestLettaManualCompactionConcurrentAdmission(t *testing.T) {
	t.Parallel()
	fake, a, _ := newLettaCompactionTestAgent(t)
	results := make(chan error, 16)
	var senders sync.WaitGroup
	for i := range 16 {
		text := "/compact"
		if i%2 == 0 {
			text = "Ordinary input."
		}
		senders.Go(func() { results <- a.SendInput(text, nil) })
	}
	senders.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else {
			assert.ErrorIs(t, err, agent.ErrAgentBusy)
		}
	}
	assert.Equal(t, 1, accepted)
	fake.nextCommand(t)
	require.NoError(t, a.sendCommand(newLettaCommand("app_server_info", "barrier")))
	assert.Equal(t, "app_server_info", fake.nextCommand(t)["type"])
}

func TestLettaManualCompactionWriteFailure(t *testing.T) {
	t.Parallel()
	fake, a, sink := newLettaCompactionTestAgent(t)
	a.Mu.Lock()
	connection := a.ws
	a.ws = nil
	a.Mu.Unlock()
	require.Error(t, a.SendInput("/compact", nil))
	assertLettaActive(t, sink, false)
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()), "a compaction that never reached Letta applies no verdict")
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.Mu.Lock()
	a.ws = connection
	a.Mu.Unlock()
	require.NoError(t, a.SendInput("/compact all", nil))
	command := fake.nextCommand(t)
	assert.Equal(t, "execute_command", command["type"])
	a.HandleOutput(compactResponse(t, command["request_id"], true))
	assertLettaActive(t, sink, false)
	assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

func TestLettaManualCompactionPersistenceFailureStillSettles(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	fake, a := newLettaCompactionTestAgentWithSink(t, sink)
	require.NoError(t, a.SendInput("/compact", nil))
	command := fake.nextCommand(t)
	a.HandleOutput(compactDelta(t, "slash_command_start", "/compact", nil))
	success := true
	a.HandleOutput(compactDelta(t, "slash_command_end", "/compact", &success))
	a.HandleOutput(compactResponse(t, command["request_id"], true))
	assertLettaActive(t, sink, false)
	states := sink.SessionInfoValues(contracts.SessionInfoKeyCompaction)
	require.NotEmpty(t, states)
	assert.Nil(t, states[len(states)-1])
}

func TestLettaManualCompactionIgnoresUnownedDeltas(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"runtime", "subagent_id"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			fake, a, sink := newLettaCompactionTestAgent(t)
			require.NoError(t, a.SendInput("/compact", nil))
			fake.nextCommand(t)
			var frame map[string]any
			require.NoError(t, json.Unmarshal(compactDelta(t, "slash_command_start", "/compact", nil), &frame))
			if field == "runtime" {
				frame[field] = map[string]any{"agent_id": "other", "conversation_id": "other"}
			} else {
				frame[field] = "unknown-child"
			}
			raw, err := json.Marshal(frame)
			require.NoError(t, err)
			a.HandleOutput(raw)
			assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			assert.Empty(t, sink.PersistedNotifications())
			assertLettaActive(t, sink, true)
		})
	}
}

// A manual compaction whose model request fails with an error that Letta marks
// as retryable still ends with the compaction's verdict alone, once. The turn
// end that Letta sends during the command decides nothing.
func TestLettaFailedManualCompactionNeverSchedulesARetry(t *testing.T) {
	t.Parallel()
	fake, a, sink := newLettaCompactionTestAgent(t)
	require.NoError(t, a.SendInput("/compact", nil))
	command := fake.nextCommand(t)
	a.HandleOutput(compactDelta(t, "slash_command_start", "/compact", nil))
	a.HandleOutput(lettaErrorMessageDelta(t, "", true))
	a.HandleOutput([]byte(`{"type":"turn_finished","turn_id":"turn-compact","stop_reason":"llm_api_error","error":"503: Service Unavailable","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"}}`))
	failed := false
	a.HandleOutput(compactDelta(t, "slash_command_end", "/compact", &failed))
	a.HandleOutput(compactResponse(t, command["request_id"], false))
	assertLettaActive(t, sink, false)
	assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}
