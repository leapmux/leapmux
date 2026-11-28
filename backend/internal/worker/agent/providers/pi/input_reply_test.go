package pi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiSteerReplyTracksNewInputsAfterAToolUse(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.sessionFile = "session-file"
	first := agent.SteerResolution{InputID: "input-first", AttemptID: "attempt-first", Receipt: agent.NativeInputReceipt{
		SessionID: a.sessionFile, InputID: "native-first",
	}}
	second := agent.SteerResolution{InputID: "input-second", AttemptID: "attempt-second", Receipt: agent.NativeInputReceipt{
		SessionID: a.sessionFile, InputID: "native-second",
	}}
	a.identifiedSteers = map[string]agent.SteerResolution{first.Receipt.InputID: first, second.Receipt.InputID: second}
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	a.HandleOutput(piSteerEchoFrame(t, first.Receipt.InputID))
	a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"The first steer started the tool."}]}}`))
	a.HandleOutput([]byte(`{"type":"turn_end","message":{"role":"assistant","stopReason":"toolUse"}}`))
	a.HandleOutput(piSteerEchoFrame(t, second.Receipt.InputID))
	require.Empty(t, sink.NativeReplyTurns())
	assert.Equal(t, first.Receipt.InputID, a.piReplyControlReceipt().TurnID)
	a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"The tool finished."}]}}`))
	a.HandleOutput([]byte(`{"type":"turn_end","message":{"role":"assistant","stopReason":"stop"}}`))
	a.HandleOutput(piSuccessfulReplyEnd)
	a.HandleOutput([]byte(`{"type":"agent_settled"}`))

	first.Outcome, second.Outcome = agent.SteerOutcomeEchoed, agent.SteerOutcomeEchoed
	first.Receipt.TurnID = first.Receipt.InputID
	second.Receipt.TurnID = first.Receipt.TurnID
	assert.Equal(t, []agent.SteerResolution{first, second}, sink.SteerResolutions())
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: a.sessionFile, TurnID: first.Receipt.TurnID, Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{{InputID: first.Receipt.InputID, AfterOrdinal: 1}, {InputID: second.Receipt.InputID, AfterOrdinal: 3}},
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "pi-reply:native-first:2"}, {Ordinal: 4, MessageKey: "pi-reply:native-first:4"}},
	}}, sink.NativeReplyTurns())
}

func piSteerEchoFrame(t *testing.T, inputID string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type": "message_end", "message": map[string]string{"role": "user", piSteerMessageIDField: inputID},
	})
	require.NoError(t, err)
	return raw
}

func TestPiExactReplyKeepsTheSamePromptAcrossNativeRetry(t *testing.T) {
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.sessionFile = "session-file"
	setPiReplyWriter(t, a, func(id string) {
		a.HandleOutput(piReplyResponse(id, "started"))
		a.HandleOutput([]byte(`{"type":"agent_start"}`))
		a.HandleOutput([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error"}],"willRetry":true}`))
		assert.Empty(t, sink.NativeReplyTurns(), "a retrying run preserves the original input")
		a.HandleOutput([]byte(`{"type":"agent_start"}`))
		a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Recovered reply."}]}}`))
		a.HandleOutput(piSuccessfulReplyEnd)
		assert.Empty(t, sink.NativeReplyTurns(), "the final low-level run still needs native settlement")
		a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	})
	receipt, err := a.SendInput("Original input.", nil)
	require.NoError(t, err)
	require.NotEmpty(t, receipt.InputID)
	require.Equal(t, receipt.InputID, receipt.TurnID)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "session-file", TurnID: receipt.TurnID, Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "pi-reply:" + receipt.TurnID + ":2"}},
	}}, sink.NativeReplyTurns())
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestPiExactReplySkipsNonReplyAssistantRows(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.sessionFile = "session-file"
	setPiReplyWriter(t, a, func(id string) {
		a.HandleOutput(piReplyResponse(id, "started"))
		a.HandleOutput([]byte(`{"type":"agent_start"}`))
		a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Checking."},{"type":"toolCall","id":"call-1","name":"bash","arguments":{}}]}}`))
		a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"   \n\t"}]}}`))
		a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Actual reply."}]}}`))
		a.HandleOutput(piSuccessfulReplyEnd)
		a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	})
	receipt, err := a.SendInput("Check the files.", nil)
	require.NoError(t, err)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "session-file", TurnID: receipt.TurnID, Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "pi-reply:" + receipt.TurnID + ":2"}},
	}}, sink.NativeReplyTurns())
}

func TestPiReplyAdmissionStaysBehindOlderBufferedEvents(t *testing.T) {
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.sessionFile = "session"
	require.True(t, a.interruptOutput.begin(func() bool { return true }))
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Older native reply."}]}}`))
	a.HandleOutput(piSuccessfulReplyEnd)
	a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	setPiReplyWriter(t, a, func(id string) {
		a.HandleOutput(piReplyResponse(id, "started"))
	})
	receipt, err := a.SendInput("New input.", nil)
	require.NoError(t, err)
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"New native reply."}]}}`))
	a.HandleOutput(piSuccessfulReplyEnd)
	a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	assert.Empty(t, sink.NativeReplyTurns())
	a.interruptOutput.finish(func() {}, a.dispatchPiOutput)
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, receipt.TurnID, turns[0].TurnID)
	assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "pi-reply:" + receipt.TurnID + ":2"}}, turns[0].Items)
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestPiExactReplyRequiresNativeAdmissionAndSettlement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response func(string) []byte
		settle   bool
		outcome  leapmuxv1.AgentInputTurnOutcome
	}{
		{
			name: "queued", response: func(id string) []byte { return piReplyResponse(id, "queued") }, settle: true,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
		{
			name: "refused",
			response: func(id string) []byte {
				return []byte(fmt.Sprintf(`{"id":%q,"type":"response","command":"prompt","success":false,"error":"No model."}`, id))
			},
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
		},
		{
			name: "process loss", response: func(id string) []byte { return piReplyResponse(id, "started") },
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			a.sessionFile = "session"
			setPiReplyWriter(t, a, func(id string) {
				a.HandleOutput(tc.response(id))
			})
			receipt, err := a.SendInput("One input.", nil)
			require.NoError(t, err)
			a.HandleOutput([]byte(`{"type":"agent_start"}`))
			a.HandleOutput(piSuccessfulReplyEnd)
			if tc.settle {
				a.HandleOutput([]byte(`{"type":"agent_settled"}`))
			} else {
				a.settleHeldPiTurn(agent.MessageCompletionInterrupted)
			}
			assert.Empty(t, sink.NativeReplyTurns())
			assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{receipt: tc.outcome}, sink.NativeReplyRetirements())
		})
	}
}

func TestPiExactReplyReportsNativeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		disposition string
		events      []string
		outcome     leapmuxv1.AgentInputTurnOutcome
		unproved    bool
	}{
		{
			name: "handled without a model run", disposition: "handled",
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		},
		{
			name: "native interruption", disposition: "started",
			events: []string{
				`{"type":"agent_start"}`,
				`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"aborted"}]}`,
				`{"type":"agent_settled"}`,
			},
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED,
		},
		{
			name: "uncommitted text", disposition: "started",
			events: []string{
				`{"type":"agent_start"}`,
				`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"An uncommitted prefix."}}`,
				string(piSuccessfulReplyEnd),
				`{"type":"agent_settled"}`,
			},
			outcome:  leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			unproved: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			setPiReplyWriter(t, a, func(id string) { a.HandleOutput(piReplyResponse(id, tc.disposition)) })
			receipt, err := a.SendInput("One input.", nil)
			require.NoError(t, err)
			for _, event := range tc.events {
				a.HandleOutput([]byte(event))
			}
			if tc.unproved {
				assert.Empty(t, sink.NativeReplyTurns())
				assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{receipt: tc.outcome}, sink.NativeReplyRetirements())
				return
			}
			assert.Equal(t, []agent.NativeReplyTurn{{
				SessionID: receipt.SessionID, TurnID: receipt.TurnID, Outcome: tc.outcome,
				Inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
			}}, sink.NativeReplyTurns())
			assert.Empty(t, sink.NativeReplyRetirements())
		})
	}
}

var piSuccessfulReplyEnd = []byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop"}],"willRetry":false}`)

func setPiReplyWriter(t *testing.T, a *Agent, prompt func(string)) {
	t.Helper()
	setPiInterruptWriter(a, func(data []byte) (int, error) {
		var command struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &command); err != nil {
			return 0, err
		}
		if command.Type == CommandPrompt {
			require.NotEmpty(t, command.ID)
			prompt(command.ID)
		} else {
			a.HandleOutput([]byte(fmt.Sprintf(`{"id":%q,"type":"response","command":%q,"success":true,"data":{}}`, command.ID, command.Type)))
		}
		return len(data), nil
	})
}

func piReplyResponse(id, disposition string) []byte {
	return []byte(fmt.Sprintf(`{"id":%q,"type":"response","command":"prompt","success":true,"data":{"disposition":%q}}`, id, disposition))
}

func TestPiDropAndExitResolveIdentifiedSteersWithEmptyReceipt(t *testing.T) {
	t.Parallel()
	t.Run("clear context clears receipt on dropped steer", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.identifiedSteers = map[string]agent.SteerResolution{
			"native-1": {
				InputID: "input-1", AttemptID: "attempt-1",
				Receipt: agent.NativeInputReceipt{SessionID: "session-1", InputID: "native-1"},
			},
		}
		a.dropIdentifiedPiSteers()
		assert.Equal(t, []agent.SteerResolution{{
			InputID: "input-1", AttemptID: "attempt-1", Outcome: agent.SteerOutcomeDropped,
		}}, sink.SteerResolutions())
	})
	t.Run("process exit reports uncertain with empty receipt", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.identifiedSteers = map[string]agent.SteerResolution{
			"native-2": {
				InputID: "input-2", AttemptID: "attempt-2",
				Receipt: agent.NativeInputReceipt{SessionID: "session-1", InputID: "native-2"},
			},
		}
		a.finishPiProcessExit()
		assert.Equal(t, []agent.SteerResolution{{
			InputID: "input-2", AttemptID: "attempt-2", Outcome: agent.SteerOutcomeUncertain,
		}}, sink.SteerResolutions())
	})
}

func TestPiQueuedReplyDispositionDoesNotPublishIdleTurn(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.sessionFile = "session"
	setPiReplyWriter(t, a, func(id string) {
		a.HandleOutput(piReplyResponse(id, "queued"))
	})
	receipt, err := a.SendInput("Queued input.", nil)
	require.NoError(t, err)
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.NativeReplyRetirements())
	assert.Empty(t, sink.TurnActives(), "queued disposition must not publish an idle turn before agent_start")
}

type piNoQueueSink struct {
	*agenttest.Sink
}

func (*piNoQueueSink) OutputQueue() *agent.OutputQueue { return nil }

func TestPiSteerResolutionOrdersBehindGenerationQueue(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	// A sink without an outer OutputQueue forces orderedSink() to use generationBuffer.OutputQueue().
	a := &Agent{
		Process:     providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "pi-order-test"}),
		sink:        agent.NewProviderServices(&piNoQueueSink{Sink: sink}),
		sessionFile: "session",
	}
	var order []string
	a.generationBuffer.OutputQueue().Append(agent.OutputWrite{
		Run: func() error {
			order = append(order, "queued-write")
			return nil
		},
	})
	require.NoError(t, a.generationBuffer.OutputQueue().Flush())
	// Block the queue with a pending failing write so retainPiSteerResolution must enqueue behind it.
	blocked := true
	a.generationBuffer.OutputQueue().Append(agent.OutputWrite{
		Run: func() error {
			if blocked {
				return fmt.Errorf("transient")
			}
			order = append(order, "second-write")
			return nil
		},
	})
	require.Error(t, a.generationBuffer.OutputQueue().Flush())
	a.retainPiSteerResolution(agent.SteerResolution{
		InputID: "in-1", AttemptID: "att-1", Outcome: agent.SteerOutcomeDropped,
	})
	assert.Empty(t, sink.SteerResolutions(), "steer resolution must wait behind queued generation writes")
	blocked = false
	require.NoError(t, a.generationBuffer.OutputQueue().Flush())
	assert.Equal(t, []agent.SteerResolution{{
		InputID: "in-1", AttemptID: "att-1", Outcome: agent.SteerOutcomeDropped,
	}}, sink.SteerResolutions())
	assert.Equal(t, []string{"queued-write", "second-write"}, order)
}

func TestPiIdentifiedSteerExcludesExtensionCommands(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	rig.agent.sessionFile = "session"
	rig.agent.currentTurnActive = true
	rig.agent.steerBridgeReady = true
	rig.agent.steerBridge = &piSteerBridge{Prefix: "leapmux-steer:nonce:", IDLength: 48}
	rig.agent.extensionCommands = map[string]bool{"local": true}
	rig.setResponder(func(piRecordedRequest) (json.RawMessage, bool, string) {
		return json.RawMessage(`{"disposition":"handled"}`), true, ""
	})
	_, err := rig.agent.SteerInput(agent.SteerRequest{
		Content: "/local", InputID: "in-cmd", AttemptID: "att-cmd",
	})
	require.NoError(t, err)
	reqs := rig.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, CommandPrompt, reqs[0].Type)
	assert.Equal(t, "/local", reqs[0].Payload["message"], "extension commands must be sent verbatim without steerBridge.Prefix")
	rig.agent.Mu.Lock()
	assert.Empty(t, rig.agent.identifiedSteers)
	rig.agent.Mu.Unlock()
	assert.Empty(t, sink.NativeReplyTurns(), "extension commands handled during steer must not publish an empty NativeReplyTurn")
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestPiHandleReplyResponseEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("unmatched response returns false", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.sessionFile = "session"
		assert.False(t, a.handlePiReplyResponse(providerkit.ParseLine(piReplyResponse("missing-id", "started"))))

		a.replyPending = agent.NativeInputReceipt{SessionID: "session", InputID: "req-1", TurnID: "req-1"}
		assert.False(t, a.handlePiReplyResponse(providerkit.ParseLine(piReplyResponse("req-other", "started"))))
		assert.Empty(t, sink.NativeReplyRetirements())
		assert.Empty(t, sink.TurnActives())
	})

	t.Run("unknown and empty dispositions retire UNPROVED and publish turn active", func(t *testing.T) {
		t.Parallel()
		for name, raw := range map[string]string{
			"unknown": `{"id":"req-1","type":"response","command":"prompt","success":true,"data":{"disposition":"unknown"}}`,
			"empty":   `{"id":"req-1","type":"response","command":"prompt","success":true,"data":{}}`,
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				sink := &agenttest.Sink{}
				a := newPiAgentWithSink(agent.NewProviderServices(sink))
				a.sessionFile = "session"
				receipt := agent.NativeInputReceipt{SessionID: "session", InputID: "req-1", TurnID: "req-1"}
				a.replyPending = receipt
				assert.True(t, a.handlePiReplyResponse(providerkit.ParseLine([]byte(raw))))
				assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
					receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
				}, sink.NativeReplyRetirements())
				assert.Equal(t, []bool{false}, sink.TurnActives())
			})
		}
	})

	t.Run("admission-denied started retires UNPROVED and publishes turn active", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name            string
			setup           func(*Agent)
			wantActive      bool
			wantRetainedRun bool
		}{
			{
				name:       "turn already active",
				setup:      func(a *Agent) { a.currentTurnActive = true },
				wantActive: true,
			},
			{
				name: "replyRun already present",
				setup: func(a *Agent) {
					a.replyRun = newPiReplyRun(agent.NativeInputReceipt{SessionID: "session", InputID: "existing", TurnID: "existing"}, true)
				},
				wantActive:      false,
				wantRetainedRun: true,
			},
			{
				name:       "session mismatch",
				setup:      func(a *Agent) { a.sessionFile = "other-session" },
				wantActive: false,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				sink := &agenttest.Sink{}
				a := newPiAgentWithSink(agent.NewProviderServices(sink))
				a.sessionFile = "session"
				receipt := agent.NativeInputReceipt{SessionID: "session", InputID: "req-1", TurnID: "req-1"}
				a.replyPending = receipt
				tc.setup(a)
				assert.True(t, a.handlePiReplyResponse(providerkit.ParseLine(piReplyResponse("req-1", "started"))))
				assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
					receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
				}, sink.NativeReplyRetirements())
				assert.Equal(t, []bool{tc.wantActive}, sink.TurnActives())
				if tc.wantRetainedRun {
					require.NotNil(t, a.replyRun)
					assert.Equal(t, "existing", a.replyRun.receipt.InputID)
				} else {
					assert.Nil(t, a.replyRun)
				}
			})
		}
	})

	t.Run("non-refusal parse error retires UNPROVED and calls handlePiPromptFailure", func(t *testing.T) {
		t.Parallel()
		for name, raw := range map[string]string{
			"command mismatch":     `{"id":"req-1","type":"response","command":"compact","success":true,"data":{}}`,
			"missing success flag": `{"id":"req-1","type":"response","command":"prompt","data":{}}`,
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				sink := &agenttest.Sink{}
				a := newPiAgentWithSink(agent.NewProviderServices(sink))
				a.sessionFile = "session"
				receipt := agent.NativeInputReceipt{SessionID: "session", InputID: "req-1", TurnID: "req-1"}
				a.replyPending = receipt
				assert.True(t, a.handlePiReplyResponse(providerkit.ParseLine([]byte(raw))))
				assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
					receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
				}, sink.NativeReplyRetirements())
				require.Len(t, sink.LeapMuxNotifications(), 1)
				assert.Equal(t, []bool{false}, sink.TurnActives())
			})
		}
	})
}

func TestPiPrepareReplyRowSkipsInactiveOrMismatchedRun(t *testing.T) {
	t.Parallel()

	replyBytes := []byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Reply"}]}}`)

	for _, tc := range []struct {
		name  string
		setup func(*Agent)
	}{
		{
			name:  "nil replyRun",
			setup: func(*Agent) {},
		},
		{
			name: "replyRun not started",
			setup: func(a *Agent) {
				a.replyRun = newPiReplyRun(agent.NativeInputReceipt{SessionID: "session", InputID: "in-1", TurnID: "in-1"}, false)
			},
		},
		{
			name: "session mismatch",
			setup: func(a *Agent) {
				a.replyRun = newPiReplyRun(agent.NativeInputReceipt{SessionID: "old-session", InputID: "in-1", TurnID: "in-1"}, true)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.Sink{}))
			a.sessionFile = "session"
			tc.setup(a)
			content := agent.MessageContent{Original: replyBytes}
			run, item := a.preparePiReplyRow(&content)
			assert.Nil(t, run)
			assert.Empty(t, item)
			assert.Empty(t, content.AgentSessionID)
			assert.Empty(t, content.NativeTurnID)
			assert.Empty(t, content.IdempotencyKey)
		})
	}
}

func TestPiFlushGenerationJSWhitespaceAndThinking(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		deltaEvent   string
		wantUnproved bool
	}{
		{
			name:         "BOM-only text_delta does not mark reply unproved",
			deltaEvent:   `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"\uFEFF"}}`,
			wantUnproved: false,
		},
		{
			name:         "NEL text_delta marks reply unproved",
			deltaEvent:   `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"\u0085"}}`,
			wantUnproved: true,
		},
		{
			name:         "thinking_delta does not mark reply unproved",
			deltaEvent:   `{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"reasoning text"}}`,
			wantUnproved: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			a.sessionFile = "session"
			setPiReplyWriter(t, a, func(id string) { a.HandleOutput(piReplyResponse(id, "started")) })
			receipt, err := a.SendInput("prompt", nil)
			require.NoError(t, err)
			a.HandleOutput([]byte(`{"type":"agent_start"}`))
			a.HandleOutput([]byte(tc.deltaEvent))
			a.HandleOutput(piSuccessfulReplyEnd)
			a.HandleOutput([]byte(`{"type":"agent_settled"}`))
			if tc.wantUnproved {
				assert.Empty(t, sink.NativeReplyTurns())
				assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
					receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
				}, sink.NativeReplyRetirements())
			} else {
				assert.Equal(t, []agent.NativeReplyTurn{{
					SessionID: receipt.SessionID, TurnID: receipt.TurnID, Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
					Inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
				}}, sink.NativeReplyTurns())
				assert.Empty(t, sink.NativeReplyRetirements())
			}
		})
	}
}

type piFailingPlanEndSink struct {
	*agenttest.Sink
	failNextReservation bool
}

func (s *piFailingPlanEndSink) ReserveTranscriptOutput(queue *agent.OutputQueue, writes ...agent.OutputWrite) ([]*agent.OutputTicket, error) {
	if s.failNextReservation {
		s.failNextReservation = false
		return nil, errors.New("storage reservation refused")
	}
	return s.Sink.ReserveTranscriptOutput(queue, writes...)
}

func TestPiToolExecutionPlanCompleteGating(t *testing.T) {
	t.Parallel()

	t.Run("non-plan tool excluded and plan_mode_complete end included while start excluded", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.sessionFile = "session"
		setPiReplyWriter(t, a, func(id string) { a.HandleOutput(piReplyResponse(id, "started")) })
		receipt, err := a.SendInput("Make a plan", nil)
		require.NoError(t, err)
		a.HandleOutput([]byte(`{"type":"agent_start"}`))
		// Non-plan tool: start and end must both be excluded from NativeReplyTurn.Items.
		a.HandleOutput([]byte(`{"type":"tool_execution_start","toolCallId":"bash-1","toolName":"bash","args":{"command":"ls"}}`))
		a.HandleOutput([]byte(`{"type":"tool_execution_end","toolCallId":"bash-1","toolName":"bash","result":{"content":[{"type":"text","text":"file.go"}]}}`))
		// plan_mode_complete start carries the plan itself and is excluded from ReplyText / NativeReplyTurn.Items.
		a.HandleOutput([]byte(`{"type":"tool_execution_start","toolCallId":"plan-1","toolName":"plan_mode_complete","args":{"plan":"Step 1"}}`))
		// plan_mode_complete end carries a statement result (without args.plan) and is stamped and included.
		a.HandleOutput([]byte(`{"type":"tool_execution_end","toolCallId":"plan-1","toolName":"plan_mode_complete","result":{"content":[{"type":"text","text":"Plan ready"}],"details":{"plan":"Step 1"}}}`))
		a.HandleOutput(piSuccessfulReplyEnd)
		a.HandleOutput([]byte(`{"type":"agent_settled"}`))

		wantKey := "pi-reply:" + receipt.TurnID + ":2"
		assert.Equal(t, []agent.NativeReplyTurn{{
			SessionID: receipt.SessionID, TurnID: receipt.TurnID, Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			Inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
			Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: wantKey}},
		}}, sink.NativeReplyTurns())

		var planEndCount int
		for _, msg := range sink.Messages() {
			if strings.Contains(string(msg.Content), `"toolCallId":"plan-1"`) && msg.Closing {
				planEndCount++
				assert.Equal(t, wantKey, msg.IdempotencyKey)
				assert.Equal(t, receipt.SessionID, msg.AgentSessionID)
				assert.Equal(t, receipt.TurnID, msg.NativeTurnID)
			} else {
				assert.Empty(t, msg.IdempotencyKey)
			}
		}
		assert.Equal(t, 1, planEndCount)
	})

	t.Run("non-retained persist failure on plan_mode_complete end marks run unproved", func(t *testing.T) {
		t.Parallel()
		base := &agenttest.Sink{}
		failing := &piFailingPlanEndSink{Sink: base}
		a := newPiAgentWithSink(agent.NewProviderServices(failing))
		a.sessionFile = "session"
		setPiReplyWriter(t, a, func(id string) { a.HandleOutput(piReplyResponse(id, "started")) })
		receipt, err := a.SendInput("Make a plan", nil)
		require.NoError(t, err)
		a.HandleOutput([]byte(`{"type":"agent_start"}`))
		a.HandleOutput([]byte(`{"type":"tool_execution_start","toolCallId":"plan-1","toolName":"plan_mode_complete","args":{"plan":"Step 1"}}`))
		failing.failNextReservation = true
		a.HandleOutput([]byte(`{"type":"tool_execution_end","toolCallId":"plan-1","toolName":"plan_mode_complete","result":{"content":[{"type":"text","text":"Plan ready"}],"details":{"plan":"Step 1"}}}`))

		a.Mu.Lock()
		require.NotNil(t, a.replyRun)
		assert.True(t, a.replyRun.unproved)
		a.Mu.Unlock()

		a.HandleOutput(piSuccessfulReplyEnd)
		a.HandleOutput([]byte(`{"type":"agent_settled"}`))
		assert.Empty(t, base.NativeReplyTurns())
		assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
			receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}, base.NativeReplyRetirements())
	})
}
