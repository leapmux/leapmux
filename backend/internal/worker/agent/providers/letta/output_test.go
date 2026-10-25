package letta

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestLettaRetryDiscardsThePartial(t *testing.T) {
	t.Parallel()
	// Source: .tmp/probe/letta/logs/frames.jsonl, frames 15-31.
	agenttest.RunRetryAttemptCase(t, agenttest.RetryAttemptCase{
		Name:       "native stream retry",
		FramesPath: "testdata/retry_partial.jsonl",
		NewAgent: func(t *testing.T, sink *agenttest.Sink) func([]byte) {
			a := &Agent{sink: agent.NewModelProgressResetSink(agent.NewProviderServices(sink)), agentID: "agent-local-1", conversationID: "local-conv-1"}
			return a.HandleOutput
		},
		Want:      []agenttest.RetryAttemptText{{Text: "FULL-ANSWER-TWO", Completion: agent.MessageCompletionComplete}},
		Discarded: []string{"PARTIAL-ATTEMPT-ONE"},
	})
}

func TestLettaRetryDiscardsThoughtAndTextTogether(t *testing.T) {
	t.Parallel()
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprint(child), func(t *testing.T) {
			a, sink := newLettaChildTestAgent(t)
			a.sink = agent.NewModelProgressResetSink(a.sink)
			target := sink
			feed := func(delta map[string]any) {
				if child {
					a.HandleOutput(lettaChildDeltaFrame(t, delta))
					return
				}
				raw, err := json.Marshal(map[string]any{"type": "stream_delta", "delta": delta})
				require.NoError(t, err)
				a.HandleOutput(raw)
			}
			if child {
				a.HandleOutput(lettaChildStateFrame(t, "running"))
				row, ok := sink.BackgroundTask(lettaTestChildID)
				require.True(t, ok)
				target = sink.Child(row.ChildAgentID)
			}
			feed(map[string]any{"message_type": "reasoning_message", "reasoning": "FAILED-THOUGHT"})
			feed(map[string]any{"message_type": "assistant_message", "content": "FAILED-TEXT"})
			assert.Positive(t, target.ProgressSnapshot().ThinkingTokens)
			feed(map[string]any{"message_type": "retry", "attempt": 1})
			assert.Zero(t, target.ProgressSnapshot().ThinkingTokens)
			feed(map[string]any{"message_type": "assistant_message", "content": "RETRIED-ANSWER"})
			assert.Positive(t, target.ProgressSnapshot().ThinkingTokens)
			if child {
				a.HandleOutput(lettaChildStateFrame(t, "completed"))
			} else {
				a.HandleOutput([]byte(lettaLiveTurnFinished))
			}
			assert.Zero(t, target.ProgressSnapshot().ThinkingTokens)
			for _, row := range target.Messages() {
				assert.NotContains(t, string(row.Content), "FAILED-THOUGHT")
				assert.NotContains(t, string(row.Content), "FAILED-TEXT")
			}
			require.Len(t, target.PersistedNotifications(), 1)
			assert.Equal(t, []map[string]any{{"type": "api_retry_resolved"}}, target.LeapMuxNotifications())
		})
	}
}

func TestLettaEventMessageRetryPersists(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	delta := `{"message_type":"event_message","event_type":"retry","event_data":{"attempt":1,"max_attempts":3,"delay_ms":1000,"message":"Retry the request."}}`
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":` + delta + `}`))
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, delta, string(sink.PersistedNotifications()[0].Content))
}

func TestLettaAbortKeepsTheTurnUntilItsTurnFinished(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)
	a.HandleOutput([]byte(lettaLiveRuntimeStartResponse))
	a.HandleOutput([]byte(lettaLiveLoopStatus))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Equal(t, "abort_message", fake.nextCommand(t)["type"])
	// Source: .tmp/probe/letta/logs/frames2.jsonl, frames 19-24.
	a.HandleOutput([]byte(`{"type":"update_loop_status","loop_status":{"status":"WAITING_ON_INPUT"}}`))
	require.ErrorIs(t, a.SendInput("Wait for the cancelled turn.", nil), agent.ErrAgentBusy)
	a.HandleOutput([]byte(lettaLiveTurnFinishedCancelled))
	require.NoError(t, a.SendInput("Continue after the cancelled turn.", nil))
	assert.Equal(t, "input", fake.nextCommand(t)["type"])
}

func TestLettaNormalIdleDoesNotReleaseInputBeforeTheFinalFrame(t *testing.T) {
	t.Parallel()
	_, a := newFakeAppServer(t)
	a.HandleOutput([]byte(lettaLiveRuntimeStartResponse))
	a.HandleOutput([]byte(lettaLiveLoopStatus))
	a.HandleOutput([]byte(`{"type":"update_loop_status","loop_status":{"status":"WAITING_ON_INPUT"}}`))
	require.ErrorIs(t, a.SendInput("Do not overtake the prior final row.", nil), agent.ErrAgentBusy)
	a.HandleOutput([]byte(lettaLiveTurnFinished))
	require.NoError(t, a.SendInput("The prior final row now exists.", nil))
}

func TestLettaRepeatedTurnFinishedIsIgnored(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput([]byte(lettaLiveTurnFinished))
	a.HandleOutput([]byte(lettaLiveLoopStatus))
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"assistant_message","content":"The next answer."}}`))
	a.HandleOutput([]byte(lettaLiveTurnFinished))
	assert.Len(t, sink.Messages(), 1)
	assert.Equal(t, []bool{true}, sink.TurnActives())
	a.HandleOutput([]byte(`{"type":"turn_finished","turn_id":"next-turn","stop_reason":"end_turn"}`))
	require.Len(t, sink.Messages(), 3)
	assert.Contains(t, string(sink.Messages()[1].Content), "The next answer.")
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestLettaRepeatedTurnFinishedHistoryHasALimit(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	frame := func(id int) []byte {
		return []byte(fmt.Sprintf(`{"type":"turn_finished","turn_id":"turn-%d","stop_reason":"end_turn"}`, id))
	}
	for id := 0; id < 17; id++ {
		a.HandleOutput(frame(id))
	}
	a.HandleOutput(frame(0))
	assert.Len(t, sink.Messages(), 18)
	a.HandleOutput(frame(16))
	assert.Len(t, sink.Messages(), 18)
}

func TestLettaNormalTurnEndKeepsTheFinalOutcome(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput([]byte(lettaLiveLoopStatus))
	a.HandleOutput([]byte(`{"type":"update_loop_status","loop_status":{"status":"WAITING_ON_INPUT"}}`))
	a.HandleOutput([]byte(lettaLiveTurnFinished))
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	require.Len(t, sink.Messages(), 1)
	assert.True(t, sink.Messages()[0].TurnEnd)
	assert.Equal(t, agent.MessageCompletionComplete, sink.Messages()[0].Completion)
}

func TestLettaWarningStatusPersists(t *testing.T) {
	t.Parallel()
	for _, level := range []string{"warning", "error"} {
		t.Run(level, func(t *testing.T) {
			a, sink := newLettaChildTestAgent(t)
			payload := fmt.Sprintf(`{"message_type":"status","level":%q,"message":"The stream recovered."}`, level)
			a.HandleOutput([]byte(`{"type":"stream_delta","delta":` + payload + `}`))
			require.Len(t, sink.PersistedNotifications(), 1)
			assert.JSONEq(t, payload, string(sink.PersistedNotifications()[0].Content))
		})
	}
}

func TestLettaInterruptedAndModelUpdateEchoesAreDropped(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`{"message_type":"status","level":"warning","message":"Interrupted"}`,
		`{"message_type":"status","level":"info","message":"Model updated to native-model"}`,
		`{"message_type":"status","level":"warning","message":" "}`,
	} {
		a, sink := newLettaChildTestAgent(t)
		a.HandleOutput([]byte(`{"type":"stream_delta","delta":` + payload + `}`))
		assert.Empty(t, sink.PersistedNotifications())
		assert.Empty(t, sink.Messages())
	}
}

func TestLettaCompactionIsLiveThenOneBoundary(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	// Source: .tmp/probe/letta/logs/frames.jsonl, frames 43-44.
	start := []byte(`{"type":"stream_delta","delta":{"message_type":"event_message","event_type":"compaction","event_data":{"trigger":"context_window_limit"}}}`)
	a.HandleOutput(start)
	a.HandleOutput(start)
	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	require.True(t, ok)
	assert.Equal(t, map[string]any{"trigger": "context_window_limit"}, value)
	assert.Empty(t, sink.PersistedNotifications())
	end := `{"message_type":"summary_message","summary":"Retained context.","compaction_stats":{"trigger":"context_window_limit","context_tokens_before":759,"context_tokens_after":396}}`
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":` + end + `}`))
	value, ok = sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	require.True(t, ok)
	assert.Nil(t, value)
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, end, string(sink.PersistedNotifications()[0].Content))
}

func TestLettaCompactionStoresThePrecedingAnswerFirst(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"assistant_message","content":"The answer before compaction."}}`))
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"event_message","event_type":"compaction"}}`))
	require.Len(t, sink.Messages(), 1)
	assert.Contains(t, string(sink.Messages()[0].Content), "The answer before compaction.")
	assert.Empty(t, sink.PersistedNotifications())
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"summary_message"}}`))
	assert.Len(t, sink.PersistedNotifications(), 1)
	assert.Len(t, sink.Messages(), 1)
}

func TestLettaTurnEndWithALiveCompactionWritesAnUnsuccessfulEnd(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"error", "cancelled"} {
		t.Run(reason, func(t *testing.T) {
			a, sink := newLettaChildTestAgent(t)
			a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"event_message","event_type":"compaction"}}`))
			a.HandleOutput([]byte(`{"type":"turn_finished","stop_reason":"` + reason + `"}`))
			value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
			require.True(t, ok)
			assert.Nil(t, value)
			outcome := "failed"
			if reason == "cancelled" {
				outcome = "aborted"
			}
			assert.Equal(t, []map[string]any{{"type": "compaction_ended", "outcome": outcome}}, sink.LeapMuxNotifications())
		})
	}
}

func TestLettaChildCompactionEndsOnItsOwnSink(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	child := sink.Child(row.ChildAgentID)
	a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{"message_type": "event_message", "event_type": "compaction"}))
	a.HandleOutput(lettaChildStateFrame(t, "error"))
	_, rootLive := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	assert.False(t, rootLive)
	value, exists := child.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	assert.True(t, exists)
	assert.Nil(t, value)
	assert.Equal(t, []map[string]any{{"type": "compaction_ended", "outcome": "failed"}}, child.LeapMuxNotifications())
}

func TestUsageStatisticsReportsTheContextTokens(t *testing.T) {
	t.Parallel()
	for _, count := range []int64{0, 4000, -1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			a, sink := newLettaChildTestAgent(t)
			a.HandleOutput([]byte(fmt.Sprintf(`{"type":"stream_delta","delta":{"message_type":"usage_statistics","prompt_tokens":8000,"completion_tokens":20,"context_tokens":%d}}`, count)))
			value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
			require.True(t, ok)
			usage := value.(map[string]any)
			if count < 0 {
				assert.NotContains(t, usage, contracts.ContextUsageFieldContextTokens)
			} else {
				assert.Equal(t, count, usage[contracts.ContextUsageFieldContextTokens])
			}
		})
	}
}

func TestUsageStatisticsIgnoresAnInvalidOptionalContextCount(t *testing.T) {
	t.Parallel()
	for _, count := range []any{nil, "4", true, []any{}, 1.5, json.Number("9223372036854775808")} {
		a, sink := newLettaChildTestAgent(t)
		raw, err := json.Marshal(map[string]any{"type": "stream_delta", "delta": map[string]any{"message_type": "usage_statistics", "prompt_tokens": 4, "completion_tokens": 2, "context_tokens": count}})
		require.NoError(t, err)
		a.HandleOutput(raw)
		value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
		require.True(t, ok, "invalid optional count: %v", count)
		usage := value.(map[string]any)
		assert.Equal(t, int64(4), usage[contracts.ContextUsageFieldInputTokens])
		assert.NotContains(t, usage, contracts.ContextUsageFieldContextTokens)
	}
}

// The App Server puts each body in its own field, rather than `payload`.
// `stream_delta` carries `delta`.
// `control_request` carries `request`.
// `update_loop_status` carries `loop_status`.
// `update_subagent_state` carries `subagents`.
// The frames below come from a live `letta server --listen` run.
// A handler that reads `payload` silently drops these frames.

const lettaLiveStreamDelta = `{"type":"stream_delta","delta":{"id":"ui-msg-2","date":"2026-09-25T21:18:53.273Z","agent_id":"agent-local-1","conversation_id":"local-conv-1","message_type":"assistant_message","content":[{"type":"text","text":"MOCK-REPLY-OK"}],"run_id":"local-run-1","seq_id":1,"type":"message"},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":11,"emitted_at":"2026-09-25T21:18:53.273Z","idempotency_key":"stream_delta:11:x"}`

const lettaLiveTurnFinished = `{"type":"turn_finished","turn_id":"batch-direct-0ac84fc0","stop_reason":"end_turn","run_id":"local-run-1","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":16,"emitted_at":"2026-09-25T21:18:53.282Z","idempotency_key":"turn_finished:16:x"}`

const lettaLiveLoopStatus = `{"type":"update_loop_status","loop_status":{"status":"SENDING_API_REQUEST","active_run_ids":[],"executing_tool_call_ids":[]},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":5,"emitted_at":"2026-09-25T21:18:53.170Z","idempotency_key":"update_loop_status:5:x"}`

const lettaLiveSubagents = `{"type":"update_subagent_state","subagents":[],"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":6,"emitted_at":"2026-09-25T21:18:53.186Z","idempotency_key":"update_subagent_state:6:x"}`

const lettaLiveControlRequest = `{"type":"control_request","request_id":"perm-call_ask_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which color do you prefer?","header":"Color","options":[{"label":"Red","description":"Warm"},{"label":"Blue","description":"Cool"}]}]},"tool_call_id":"call_ask_1","permission_suggestions":[{"id":"save-default","text":"Yes, allow AskUserQuestion operations during this session"}],"blocked_path":null},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":15,"emitted_at":"2026-09-25T21:25:23.430Z","idempotency_key":"control_request:15:x"}`

// Protocol state and acknowledgements. Each `update_device_status` alone is
// about 3 KB. A transcript that stored them pushed the reader's own answer out
// of the virtualized chat, so the reader saw no answer at all.
const lettaLiveDeviceStatus = `{"type":"update_device_status","device_status":{"current_connection_id":"app-server","connection_name":"trustin-imac.local","is_online":true,"is_processing":true,"current_permission_mode":"unrestricted","current_working_directory":"/tmp/wd","git_context":null,"letta_code_version":"0.0.1"},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":8,"emitted_at":"2026-09-25T21:18:53.186Z","idempotency_key":"update_device_status:8:x"}`

const lettaLiveUpdateQueue = `{"type":"update_queue","queue":[],"removed":[],"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":7,"emitted_at":"2026-09-25T21:18:53.186Z","idempotency_key":"update_queue:7:x"}`

const lettaLiveInputAccepted = `{"type":"input_accepted","request_id":"in-1","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"accepted":true,"disposition":"started"}`

const lettaLiveListModelsResponse = `{"type":"list_models_response","request_id":"lm-1","success":true,"models":[]}`

const lettaLiveUsageStatistics = `{"type":"stream_delta","delta":{"message_type":"usage_statistics","prompt_tokens":10,"completion_tokens":2,"total_tokens":12},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":13,"emitted_at":"2026-09-25T21:18:53.280Z","idempotency_key":"stream_delta:13:x"}`

func TestUsageStatisticsReportsNativeContextCounts(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"usage_statistics","prompt_tokens":12000,"completion_tokens":40,"total_tokens":12040}}`))

	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	usage, ok := value.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(12000), usage[contracts.ContextUsageFieldInputTokens])
	assert.Equal(t, int64(40), usage[contracts.ContextUsageFieldOutputTokens])
	assert.NotContains(t, usage, contracts.ContextUsageFieldContextWindow)
}

func TestUsageStatisticsKeepsZeroAndRejectsInvalidCounts(t *testing.T) {
	t.Parallel()
	zeroSink := &agenttest.Sink{}
	zeroAgent := &Agent{sink: agent.NewProviderServices(zeroSink)}
	zeroAgent.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"usage_statistics","prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`))
	value, ok := zeroSink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	usage, ok := value.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(0), usage[contracts.ContextUsageFieldInputTokens])
	assert.Equal(t, int64(0), usage[contracts.ContextUsageFieldOutputTokens])

	for _, frame := range []string{
		`{"type":"stream_delta","delta":{"message_type":"usage_statistics","prompt_tokens":-1,"completion_tokens":4}}`,
		`{"type":"stream_delta","delta":{"message_type":"usage_statistics","prompt_tokens":4}}`,
		`{"type":"stream_delta","delta":{"message_type":"usage_statistics","prompt_tokens":"4","completion_tokens":2}}`,
	} {
		sink := &agenttest.Sink{}
		a := &Agent{sink: agent.NewProviderServices(sink)}
		a.HandleOutput([]byte(frame))
		_, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
		assert.False(t, ok, frame)
	}
}

const lettaLiveStopReasonDelta = `{"type":"stream_delta","delta":{"message_type":"stop_reason","stop_reason":"end_turn"},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":14,"emitted_at":"2026-09-25T21:18:53.281Z","idempotency_key":"stream_delta:14:x"}`

// The worker flushes the buffered assistant text at the native turn end.
func TestStreamDeltaCarriesItsBodyInTheDeltaField(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.HandleOutput([]byte(lettaLiveStreamDelta))
	a.HandleOutput([]byte(lettaLiveTurnFinished))

	var found []string
	for _, m := range sink.Messages() {
		found = append(found, string(m.Content))
	}
	require.NotEmpty(t, found, "the turn produces a transcript row")
	assert.True(t, strings.Contains(strings.Join(found, "\n"), "MOCK-REPLY-OK"),
		"the assistant text reaches the transcript, rows: %v", found)
}

// The local App Server emits a reasoning string beside content. The worker
// must assemble that field before it emits the turn end.
func TestLocalReasoningDeltaUsesReasoningField(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	for _, frame := range []string{
		`{"type":"stream_delta","delta":{"message_type":"reasoning_message","reasoning":"Compare "}}`,
		`{"type":"stream_delta","delta":{"message_type":"reasoning_message","reasoning":"the values."}}`,
		lettaLiveTurnFinished,
	} {
		a.HandleOutput([]byte(frame))
	}

	var reasoningRows []map[string]any
	for _, row := range sink.Messages() {
		var payload map[string]any
		if err := json.Unmarshal(row.Content, &payload); err != nil {
			continue
		}
		if payload[contracts.AssembledMessageFieldKind] == contracts.AssembledMessageKindReasoning {
			reasoningRows = append(reasoningRows, payload)
		}
	}
	require.Len(t, reasoningRows, 1, "the native reasoning chunks create one row")
	assert.Equal(t, "Compare the values.", reasoningRows[0][contracts.AssembledMessageFieldText])
}

func TestReasoningDeltaStillReadsContentBlocks(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"reasoning_message","content":[{"type":"text","text":"Older format."}]}}`))
	a.HandleOutput([]byte(lettaLiveTurnFinished))

	var found bool
	for _, row := range sink.Messages() {
		var payload map[string]any
		if json.Unmarshal(row.Content, &payload) == nil && payload[contracts.AssembledMessageFieldKind] == contracts.AssembledMessageKindReasoning {
			found = payload[contracts.AssembledMessageFieldText] == "Older format."
		}
	}
	assert.True(t, found, "the prior content-block shape still creates a reasoning row")
}

// A live update_loop_status starts the turn. Its status is in `loop_status`.
func TestLoopStatusBodyLivesInTheLoopStatusField(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.HandleOutput([]byte(lettaLiveLoopStatus))
	actives := sink.TurnActives()
	require.NotEmpty(t, actives, "the loop status moves the turn flag")
	assert.True(t, actives[len(actives)-1], "SENDING_API_REQUEST starts the turn")
}

// A native child snapshot updates registry state without a transcript row.
func TestSubagentStateStoresNoRow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.HandleOutput([]byte(lettaLiveSubagents))

	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.Messages())
}

// A live control_request opens a permission control. Its discriminator is
// `request.subtype`, its body sits in `request`, and the id sits at the frame
// root. The frame asks whether AskUserQuestion may run, which Strict mode asks
// since Letta Code 0.34: the tool no longer waits for an answer.
func TestControlRequestBodyLivesInTheRequestField(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.Mu.Lock()
	a.conversationID = "local-conv-1"
	a.Mu.Unlock()
	a.HandleOutput([]byte(lettaLiveControlRequest))

	published := sink.PublishedControls()
	require.Len(t, published, 1, "the request is published once")
	assert.Equal(t, "perm-call_ask_1", published[0].RequestID, "the id sits at the frame root")
	var payload struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(published[0].Payload, &payload))
	assert.Equal(t, string(lettaControlPermission), payload.Type, "a request for the question tool asks whether the tool may run")
}

// The control payload uses the contract's tool fields that the browser reads.
// A payload with `toolName`, `toolCallId`, and `input` drew an empty tool banner.
// Its question options did not appear.
func TestControlPayloadUsesTheContractFieldNames(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.Mu.Lock()
	a.conversationID = "local-conv-1"
	a.Mu.Unlock()
	a.HandleOutput([]byte(lettaLiveControlRequest))

	requests := sink.PublishedControls()
	require.NotEmpty(t, requests, "the control request is published")
	var published map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Payload, &published))
	assert.Equal(t, "permission", published["type"])
	assert.Equal(t, "AskUserQuestion", published[contracts.LettaDeltaFieldToolName], "the tool name is under tool_name")
	assert.Equal(t, "call_ask_1", published[contracts.LettaDeltaFieldToolCallID], "the call id is under tool_call_id")
	assert.NotNil(t, published[contracts.LettaDeltaFieldToolInput], "the tool input is under tool_input")
	assert.NotContains(t, published, "toolName", "no camelCase twin of tool_name")
	assert.NotContains(t, published, "input", "no bare `input` twin of tool_input")
}

// Protocol state and command acknowledgements must create no transcript rows.
// Stored protocol rows displaced the assistant answer in the virtualized chat.
func TestProtocolStateAndAcksMoveNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	for _, frame := range []string{
		lettaLiveDeviceStatus,
		lettaLiveUpdateQueue,
		lettaLiveInputAccepted,
		lettaLiveListModelsResponse,
		lettaLiveUsageStatistics,
		lettaLiveStopReasonDelta,
	} {
		a.HandleOutput([]byte(frame))
	}
	assert.Empty(t, sink.Messages(), "no protocol state becomes a transcript row")
	assert.Empty(t, sink.PersistedNotifications(), "no protocol state becomes a notification row")
	assert.Empty(t, sink.TurnActives(), "no protocol state moves the turn flag")
}

// The turn end still reaches the transcript: it is the result divider the
// reader sees after every turn.
func TestTurnFinishedStillReachesTheTranscript(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.HandleOutput([]byte(lettaLiveTurnFinished))
	rows := sink.Messages()
	require.NotEmpty(t, rows, "the turn end is persisted")
	assert.True(t, rows[len(rows)-1].TurnEnd, "the row is the turn end")
}

func TestTurnFinishedReportsZeroToolUses(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.HandleOutput([]byte(lettaLiveStreamDelta))
	a.HandleOutput([]byte(lettaLiveTurnFinished))

	assert.Equal(t, []int{0}, agenttest.TurnToolUseCounts(t, sink.Messages()))
	rows := sink.Messages()
	assert.JSONEq(t, lettaLiveTurnFinished, string(rows[len(rows)-1].Content), "tool metadata preserves the original native event")
}

func TestTurnFinishedCountsUniqueRootToolCalls(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	for _, frame := range []string{
		`{"type":"stream_delta","delta":{"message_type":"tool_call_message","tool_calls":[{"tool_call_id":"read-1","name":"Read","arguments":"{\"file_path\":\"one.txt\"}"},{"tool_call_id":"read-2","name":"Read","arguments":"{\"file_path\":\"two.txt\"}"}]}}`,
		`{"type":"stream_delta","delta":{"message_type":"client_tool_start","tool_call_id":"read-1","tool_name":"Read","tool_input":{"file_path":"one.txt"}}}`,
		`{"type":"stream_delta","delta":{"message_type":"tool_return_message","tool_call_id":"read-1","tool_return":"first result"}}`,
		`{"type":"stream_delta","delta":{"message_type":"tool_return_message","tool_call_id":"read-1","tool_return":"first result"}}`,
		`{"type":"stream_delta","delta":{"message_type":"tool_return_message","tool_call_id":"read-2","tool_return":"second result"}}`,
		lettaLiveTurnFinished,
	} {
		a.HandleOutput([]byte(frame))
	}

	assert.Equal(t, []int{2}, agenttest.TurnToolUseCounts(t, sink.Messages()), "native call IDs count once despite multiple start and result events")
}

func TestTurnFinishedToolCountResetsAndExcludesChildCalls(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{
		"message_type": "client_tool_start", "tool_call_id": "reused-read", "tool_name": "Read",
		"tool_input": map[string]string{"file_path": "child.txt"},
	}))
	a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{
		"message_type": "tool_return_message", "tool_call_id": "reused-read", "tool_return": "child result",
	}))
	rootStart := []byte(`{"type":"stream_delta","delta":{"message_type":"client_tool_start","tool_call_id":"reused-read","tool_name":"Read","tool_input":{"file_path":"root.txt"}}}`)
	rootResult := []byte(`{"type":"stream_delta","delta":{"message_type":"tool_return_message","tool_call_id":"reused-read","tool_return":"root result"}}`)
	a.HandleOutput(rootStart)
	a.HandleOutput(rootResult)
	a.HandleOutput([]byte(`{"type":"turn_finished","turn_id":"tool-turn-1","stop_reason":"end_turn"}`))
	a.HandleOutput([]byte(lettaLiveStreamDelta))
	a.HandleOutput([]byte(`{"type":"turn_finished","turn_id":"tool-turn-2","stop_reason":"end_turn"}`))
	a.HandleOutput(rootStart)
	a.HandleOutput(rootResult)
	a.HandleOutput([]byte(`{"type":"turn_finished","turn_id":"tool-turn-3","stop_reason":"end_turn"}`))

	assert.Equal(t, []int{1, 0, 1}, agenttest.TurnToolUseCounts(t, sink.Messages()), "the root count excludes children and resets between turns")
}

func TestToolReturnPersistsBeforeItsSpanCloses(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"client_tool_start","run_id":"rail-run","tool_call_id":"shell-rail","tool_name":"Bash","tool_input":{"command":"printf result"}}}`))
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"tool_return_message","run_id":"rail-run","tool_call_id":"shell-rail","tool_return":"result"}}`))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.True(t, rows[1].Closing)
	assert.Contains(t, rows[1].SpansOpenAtPersist, agenttest.SpanOpen{SpanID: "letta-tool-shell-rail"})
	assert.Equal(t, []string{"letta-tool-shell-rail"}, sink.ClosedSpans())
}

func TestTurnFinishedRejectsIncompleteToolStarts(t *testing.T) {
	t.Parallel()
	for _, frame := range []string{
		`{"type":"stream_delta","delta":{"message_type":"client_tool_start","tool_name":"Read"}}`,
		`{"type":"stream_delta","delta":{"message_type":"client_tool_start","tool_call_id":"invalid-read"}}`,
	} {
		sink := &agenttest.Sink{}
		a := &Agent{sink: agent.NewProviderServices(sink)}
		a.HandleOutput([]byte(frame))
		a.HandleOutput([]byte(lettaLiveTurnFinished))
		assert.Equal(t, []int{0}, agenttest.TurnToolUseCounts(t, sink.Messages()), "an incomplete native tool call must not count as tool activity")
	}
}

func TestLettaNativeProgressKeepsTheToolOpenAndReplacesItsLiveWindow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	start := `{"id":"native-start","message_type":"client_tool_start","run_id":"run-1","tool_call_id":"call-1","tool_name":"Bash","tool_args":"{\"command\":\"node actual-script.js\"}"}`
	first := `{"type":"message","id":"synthetic-tool-return-stream-call-1","message_type":"tool_return_message","run_id":"run-1","status":"success","tool_call_id":"call-1","tool_return":"first window","tool_returns":[{"tool_call_id":"call-1","status":"success","tool_return":"first window","stdout":["first window"]}]}`
	second := `{"type":"message","id":"synthetic-tool-return-stream-call-1","message_type":"tool_return_message","run_id":"run-1","status":"success","tool_call_id":"call-1","tool_return":"second window","tool_returns":[{"tool_call_id":"call-1","status":"success","tool_return":"second window","stdout":["second window"]}]}`
	for _, payload := range []string{start, first, second} {
		a.HandleOutput([]byte(`{"type":"stream_delta","delta":` + payload + `}`))
	}
	assert.Empty(t, sink.ClosedSpans(), "native progress must not close the tool")
	rows := sink.Messages()
	require.Len(t, rows, 3, "each native payload must remain in Worker history")
	for i, payload := range []string{start, first, second} {
		assert.Equal(t, []byte(payload), rows[i].Content, "the original native bytes must remain intact")
	}
	assert.False(t, rows[1].Closing)
	assert.False(t, rows[2].Closing)
	var tails []string
	for _, update := range sink.ProgressUpdates() {
		if update.Operation == agent.ProgressOutputTail {
			assert.Equal(t, "letta-tool-call-1", update.ScopeID)
			tails = append(tails, update.Text)
		}
	}
	assert.Equal(t, []string{"first window", "second window"}, tails, "current windows must replace live output without concatenation")
}

func TestLettaEmptyClientEndRetainsLifecycleBeforeTheActualSyntheticFinalReturn(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	start := `{"id":"native-start","message_type":"client_tool_start","run_id":"run-1","tool_call_id":"call-1","tool_name":"Bash","tool_args":"{\"command\":\"node actual-script.js\"}"}`
	end := `{"id":"native-start","message_type":"client_tool_end","run_id":"run-1","tool_call_id":"call-1","status":"success"}`
	final := `{"type":"message","id":"synthetic-tool-return-native-final","message_type":"tool_return_message","run_id":"run-1","status":"success","tool_call_id":"call-1","tool_return":"actual final native result","tool_returns":[{"tool_call_id":"call-1","status":"success","tool_return":"actual final native result"}]}`
	for _, payload := range []string{start, end, final} {
		a.HandleOutput([]byte(`{"type":"stream_delta","delta":` + payload + `}`))
	}
	rows := sink.Messages()
	require.Len(t, rows, 3)
	for i, payload := range []string{start, end, final} {
		assert.Equal(t, []byte(payload), rows[i].Content, "the original native bytes must remain intact")
	}
	assert.Equal(t, "letta-tool-call-1", rows[1].SpanID)
	assert.False(t, rows[1].Closing, "an absent result must remain lifecycle data")
	assert.True(t, rows[2].Closing)
	assert.Contains(t, string(rows[2].Content), "synthetic-tool-return-native-final", "the actual synthetic final message must remain intact")
}

// The two live `turn_finished` frames below come from Letta Code 0.34.2.
// `abort_message` ends a turn with `stop_reason: cancelled`, once the turn stops.
// A model request that fails ends its turn with `stop_reason: error`, and the
// frame carries the error text.
const (
	lettaLiveTurnFinishedCancelled = `{"type":"turn_finished","turn_id":"batch-direct-ed91f9f8-7480-429e-b8eb-978f8ed667a8","stop_reason":"cancelled","run_id":"local-run-2","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":26,"emitted_at":"2026-10-04T21:29:06.785Z","idempotency_key":"turn_finished:26:50f04e1f-9bc5-4c26-9716-37538c613b5c"}`
	lettaLiveTurnFinishedError     = `{"type":"turn_finished","turn_id":"batch-direct-8c27ef5b-2295-4c29-845e-e1b1ff7fc1bc","stop_reason":"error","run_id":"local-run-1","error":"{\n  \"error\": {\n    \"error\": {\n      \"type\": \"local_backend_error\",\n      \"message\": \"400: {\\\"message\\\":\\\"NATIVEERRORMARKER\\\"}\"\n    },\n    \"run_id\": \"local-run-1\"\n  }\n}","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":17,"emitted_at":"2026-10-04T21:41:00.680Z","idempotency_key":"turn_finished:17:2f3e0a18-6118-4be9-9d5f-364278e7366c"}`
)

// These frames come from a native Letta Code 0.34.2 request that returned HTTP 400.
// The server sends them in this order:
//   - The child snapshot.
//   - A non-final loop_error.
//   - An error_message.
//   - A stop_reason.
//   - A final loop_error.
//   - The turn_finished frame with the same error.
var lettaLiveModelErrorFrames = []string{
	// snapshot
	`{"type":"update_subagent_state","subagents":[],"runtime":{"agent_id":"agent-local-9145b410-391b-46fd-8739-f16733a5894f","conversation_id":"local-conv-1"},"event_seq":6,"emitted_at":"2026-10-04T23:54:34.700Z","idempotency_key":"update_subagent_state:6:dfc2de1b-5808-472d-a664-d3a5e2f4f67d"}`,
	// open loop error
	`{"type":"stream_delta","delta":{"id":"lifecycle-db3808fb-7b10-4c86-bb1b-f6a5c4f9f980","date":"2026-10-04T23:54:35.359Z","message_type":"loop_error","run_id":"local-run-1","message":"{\n  \"error\": {\n    \"error\": {\n      \"type\": \"local_backend_error\",\n      \"message\": \"400: {\\\"type\\\":\\\"invalid_request_error\\\",\\\"code\\\":\\\"invalid_request_error\\\",\\\"message\\\":\\\"NATIVEERRORMARKER\\\"}\",\n      \"detail\": \"400: {\\\"type\\\":\\\"invalid_request_error\\\",\\\"code\\\":\\\"invalid_request_error\\\",\\\"message\\\":\\\"NATIVEERRORMARKER\\\"}\"\n    },\n    \"run_id\": \"local-run-1\"\n  }\n}","stop_reason":"error","is_terminal":false,"api_error":{"message_type":"error_message","message":"400: {\"type\":\"invalid_request_error\",\"code\":\"invalid_request_error\",\"message\":\"NATIVEERRORMARKER\"}","error_type":"local_backend_error","run_id":"local-run-1","detail":"400: {\"type\":\"invalid_request_error\",\"code\":\"invalid_request_error\",\"message\":\"NATIVEERRORMARKER\"}"}},"runtime":{"agent_id":"agent-local-9145b410-391b-46fd-8739-f16733a5894f","conversation_id":"local-conv-1"},"event_seq":11,"emitted_at":"2026-10-04T23:54:35.359Z","idempotency_key":"stream_delta:11:a1047cab-5d50-4698-a33e-13fafa92797e"}`,
	// error message
	`{"type":"stream_delta","delta":{"id":"letta-msg-1","date":"2026-10-04T23:54:35.354Z","agent_id":"agent-local-9145b410-391b-46fd-8739-f16733a5894f","conversation_id":"local-conv-1","message_type":"error_message","message":"400: {\"type\":\"invalid_request_error\",\"code\":\"invalid_request_error\",\"message\":\"NATIVEERRORMARKER\"}","detail":"400: {\"type\":\"invalid_request_error\",\"code\":\"invalid_request_error\",\"message\":\"NATIVEERRORMARKER\"}","error_type":"local_backend_error","retryable":false,"run_id":"local-run-1","seq_id":1,"type":"message"},"runtime":{"agent_id":"agent-local-9145b410-391b-46fd-8739-f16733a5894f","conversation_id":"local-conv-1"},"event_seq":12,"emitted_at":"2026-10-04T23:54:35.359Z","idempotency_key":"stream_delta:12:4ce3f538-ae02-48bb-8dad-3772b090da33"}`,
	// stop reason
	`{"type":"stream_delta","delta":{"message_type":"stop_reason","stop_reason":"error","run_id":"local-run-1","seq_id":2,"type":"message"},"runtime":{"agent_id":"agent-local-9145b410-391b-46fd-8739-f16733a5894f","conversation_id":"local-conv-1"},"event_seq":13,"emitted_at":"2026-10-04T23:54:35.362Z","idempotency_key":"stream_delta:13:73abed5f-79bf-4a27-b048-447a8af85a5d"}`,
	// final loop error
	`{"type":"stream_delta","delta":{"id":"lifecycle-71f78419-3baa-451d-b74d-eada6d09b888","date":"2026-10-04T23:54:35.364Z","message_type":"loop_error","run_id":"local-run-1","message":"{\n  \"error\": {\n    \"error\": {\n      \"type\": \"local_backend_error\",\n      \"message\": \"400: {\\\"type\\\":\\\"invalid_request_error\\\",\\\"code\\\":\\\"invalid_request_error\\\",\\\"message\\\":\\\"NATIVEERRORMARKER\\\"}\",\n      \"detail\": \"400: {\\\"type\\\":\\\"invalid_request_error\\\",\\\"code\\\":\\\"invalid_request_error\\\",\\\"message\\\":\\\"NATIVEERRORMARKER\\\"}\"\n    },\n    \"run_id\": \"local-run-1\"\n  }\n}","stop_reason":"error","is_terminal":true,"client_message_ids":["leapmux-message-d41dcd3f-f2ce-4bce-b60e-0eb8c550068b"],"api_error":{"message_type":"error_message","message":"400: {\"type\":\"invalid_request_error\",\"code\":\"invalid_request_error\",\"message\":\"NATIVEERRORMARKER\"}","error_type":"local_backend_error","run_id":"local-run-1","detail":"400: {\"type\":\"invalid_request_error\",\"code\":\"invalid_request_error\",\"message\":\"NATIVEERRORMARKER\"}"}},"runtime":{"agent_id":"agent-local-9145b410-391b-46fd-8739-f16733a5894f","conversation_id":"local-conv-1"},"event_seq":14,"emitted_at":"2026-10-04T23:54:35.364Z","idempotency_key":"stream_delta:14:ee060298-d235-4591-9ca9-3977e0e9b21b"}`,
	// turn end
	`{"type":"turn_finished","turn_id":"batch-direct-5b7597fe-01cf-4e2d-9313-92a2b013aa03","stop_reason":"error","run_id":"local-run-1","error":"{\n  \"error\": {\n    \"error\": {\n      \"type\": \"local_backend_error\",\n      \"message\": \"400: {\\\"type\\\":\\\"invalid_request_error\\\",\\\"code\\\":\\\"invalid_request_error\\\",\\\"message\\\":\\\"NATIVEERRORMARKER\\\"}\",\n      \"detail\": \"400: {\\\"type\\\":\\\"invalid_request_error\\\",\\\"code\\\":\\\"invalid_request_error\\\",\\\"message\\\":\\\"NATIVEERRORMARKER\\\"}\"\n    },\n    \"run_id\": \"local-run-1\"\n  }\n}","runtime":{"agent_id":"agent-local-9145b410-391b-46fd-8739-f16733a5894f","conversation_id":"local-conv-1"},"event_seq":17,"emitted_at":"2026-10-04T23:54:35.365Z","idempotency_key":"turn_finished:17:bcdb9da3-11dd-4fb7-95bf-941dc1a3de1f"}`,
}

// The stored turn end records how the turn ended. The browser draws the divider
// from that record, so an interrupted turn must not read as an ended one.
func TestTurnFinishedRecordsHowTheTurnEnded(t *testing.T) {
	t.Parallel()
	withReason := func(reason string) string {
		return `{"type":"turn_finished","turn_id":"batch-direct-1","stop_reason":"` + reason + `","run_id":"local-run-1"}`
	}
	cases := []struct {
		name  string
		frame string
		want  agent.MessageCompletion
	}{
		{"the live cancelled stop", lettaLiveTurnFinishedCancelled, agent.MessageCompletionInterrupted},
		{"the live error stop", lettaLiveTurnFinishedError, agent.MessageCompletionError},
		{"the live end_turn stop", lettaLiveTurnFinished, agent.MessageCompletionComplete},
		{"an llm_api_error stop", withReason("llm_api_error"), agent.MessageCompletionError},
		{"a max_steps stop", withReason("max_steps"), agent.MessageCompletionComplete},
		{"a stop that Letta Code adds later", withReason("a_stop_that_does_not_exist_yet"), agent.MessageCompletionComplete},
		{"a turn end with no stop", `{"type":"turn_finished","turn_id":"batch-direct-1"}`, agent.MessageCompletionComplete},
		{"a stop that is not text", `{"type":"turn_finished","turn_id":"batch-direct-1","stop_reason":5}`, agent.MessageCompletionComplete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := &Agent{sink: agent.NewProviderServices(sink)}

			a.HandleOutput([]byte(tc.frame))

			rows := sink.Messages()
			require.Len(t, rows, 1)
			assert.True(t, rows[0].TurnEnd)
			assert.Equal(t, tc.want, rows[0].Completion)
			assert.JSONEq(t, tc.frame, string(rows[0].Content), "the row keeps the native frame whole")
		})
	}
}

// The failed divider owns an error that the native loop repeats before the turn end.
func TestLettaFailedTurnStatesItsErrorOnce(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}

	for _, frame := range lettaLiveModelErrorFrames {
		a.HandleOutput([]byte(frame))
	}

	notices := sink.PersistedNotifications()
	assert.Empty(t, notices, "the native divider states the error once")

	rows := sink.Messages()
	require.Len(t, rows, 1, "no other frame of the failed turn becomes a row")
	assert.True(t, rows[0].TurnEnd)
	assert.Equal(t, agent.MessageCompletionError, rows[0].Completion)
	assert.JSONEq(t, lettaLiveModelErrorFrames[5], string(rows[0].Content), "the turn end keeps the native frame whole")
}

// Letta Code 0.34.2 queues the first message after an interrupt, and every message
// that arrives while a turn runs. It echoes the queued message as a `user_message`
// when the message starts, and it gives the echo the `client_message_id` of the
// message as its `otid`: letta.js `emitDequeuedUserMessage` reads
// `payload.otid ??= payload.client_message_id`. Letta Code's own clients match an
// echo to their own copy of the message by this id. LeapMux stores the message
// when the reader sends it, so the echo must draw no second row.
//
// The frame is verbatim from a live `letta server --listen` run of Letta Code
// 0.34.2 (probe `interrupt-model-id`, PROMPT_C after an interrupt), except for
// the `otid`, which the test sets to the id of the message that it sent.
const lettaLiveQueuedMessageEcho = `{"type":"stream_delta","delta":{"type":"message","id":"user-msg-65066ea3-9a57-4d90-9c45-5720f48167ca","date":"2026-10-04T23:02:55.820Z","message_type":"user_message","content":[{"type":"text","text":"PROMPT_C"}],"otid":%q},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":28,"emitted_at":"2026-10-04T23:02:55.820Z","idempotency_key":"stream_delta:28:9504d989-cddf-456d-b00e-0efc03fa796f"}`

// The verbatim `input_accepted` and `update_queue` frames of the same run. Both
// state the queue, and neither draws a row.
const (
	lettaLiveQueuedInputAccepted = `{"type":"input_accepted","request_id":"in-3","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"accepted":true,"disposition":"queued"}`
	lettaLiveQueuedUpdateQueue   = `{"type":"update_queue","queue":[{"id":"q-1","client_message_id":%q,"kind":"message","source":"user","content":[{"type":"text","text":"PROMPT_C"}],"enqueued_at":"2026-10-04T23:02:55.819Z"}],"removed":[],"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":27,"emitted_at":"2026-10-04T23:02:55.819Z","idempotency_key":"update_queue:27:2dcc5cd6-06b8-4a22-b820-7fac74717634"}`
)

func TestEchoOfAQueuedMessageTheReaderSentIsNotARow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	fake, a := openedAgentWithSink(t, sink)

	require.NoError(t, a.SendInput("PROMPT_C", nil))
	id := sentClientMessageID(t, inputPayload(t, fake))

	// Letta Code queues the message and echoes it when it starts.
	a.HandleOutput([]byte(lettaLiveQueuedInputAccepted))
	a.HandleOutput([]byte(fmt.Sprintf(lettaLiveQueuedUpdateQueue, id)))
	a.HandleOutput([]byte(fmt.Sprintf(lettaLiveQueuedMessageEcho, id)))

	assert.Empty(t, sink.Messages(), "LeapMux stored the message when the reader sent it, so its echo is no second row")
}

// A user message that LeapMux did not send keeps its row: Letta Code gives its
// own messages a random `otid` (the verbatim frame of
// TestEchoOfAnAnswerFromAnotherSenderStaysARow), and a frame may state none.
func TestUserMessageThatLeapMuxDidNotSendStaysARow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	_, a := openedAgentWithSink(t, sink)

	a.HandleOutput([]byte(fmt.Sprintf(lettaLiveQueuedMessageEcho, "aa72b91d-0de8-4bde-bada-4d070e2a13f1")))
	assert.Len(t, sink.Messages(), 1, "a message with an id of Letta Code's own is a row")

	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"user_message","content":[{"type":"text","text":"No id."}]}}`))
	assert.Len(t, sink.Messages(), 2, "a message with no id is a row")

	a.HandleOutput([]byte(fmt.Sprintf(lettaLiveQueuedMessageEcho, "")))
	assert.Len(t, sink.Messages(), 3, "a message with an empty id is a row")
}

func TestLettaModelProgress(t *testing.T) {
	t.Parallel()
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprint(child), func(t *testing.T) {
			t.Parallel()
			a, sink := newLettaChildTestAgent(t)
			a.sink = agent.NewModelProgressResetSink(a.sink)
			target := sink
			scope := "main"
			if child {
				a.HandleOutput(lettaChildStateFrame(t, "running"))
				row, ok := sink.BackgroundTask(lettaTestChildID)
				require.True(t, ok)
				target = sink.Child(row.ChildAgentID)
				scope = "child:" + lettaTestChildID
			}
			feed := func(delta map[string]any) {
				if child {
					a.HandleOutput(lettaChildDeltaFrame(t, delta))
					return
				}
				raw, err := json.Marshal(map[string]any{"type": "stream_delta", "delta": delta})
				require.NoError(t, err)
				a.HandleOutput(raw)
			}
			feed(map[string]any{"message_type": "assistant_message", "content": []any{map[string]any{"type": "text", "text": "First text 한글 😊"}}})
			first := target.ProgressSnapshot().ThinkingTokens
			assert.Positive(t, first)
			feed(map[string]any{"message_type": "reasoning_message", "content": []any{map[string]any{"type": "text", "text": "Fallback reasoning keeps every character."}}})
			assert.Greater(t, target.ProgressSnapshot().ThinkingTokens, first)
			feed(map[string]any{"message_type": "reasoning_message", "reasoning": "Explicit reasoning.", "content": "Do not duplicate fallback."})
			feed(map[string]any{"message_type": "assistant_message", "content": " More final text."})
			var observations []agent.ProgressUpdate
			for _, update := range target.ProgressUpdates() {
				if update.Operation == agent.ProgressModelText {
					observations = append(observations, update)
				}
			}
			assert.Equal(t, []agent.ProgressUpdate{
				agent.ModelTextProgress(scope, "First text 한글 😊"),
				agent.ModelTextProgress(scope+":reasoning", "Fallback reasoning keeps every character."),
				agent.ModelTextProgress(scope+":reasoning", "Explicit reasoning."),
				agent.ModelTextProgress(scope, " More final text."),
			}, observations)
			if child {
				assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
				a.HandleOutput(lettaChildStateFrame(t, "completed"))
			} else {
				a.HandleOutput([]byte(lettaLiveTurnFinished))
			}
			assert.Zero(t, target.ProgressSnapshot().ThinkingTokens)
			var stored strings.Builder
			for _, message := range target.Messages() {
				stored.Write(message.Content)
			}
			assert.Contains(t, stored.String(), "First text 한글 😊 More final text.")
			assert.Contains(t, stored.String(), "Fallback reasoning keeps every character.Explicit reasoning.")
			assert.NotContains(t, stored.String(), "Do not duplicate fallback.")
		})
	}
}

func TestLettaModelProgressIgnoresEmptyMalformedAndRetiredChildren(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.sink = agent.NewModelProgressResetSink(a.sink)
	for _, raw := range []string{
		`{"type":"stream_delta","delta":{"message_type":"assistant_message","content":null}}`,
		`{"type":"stream_delta","delta":{"message_type":"assistant_message","content":{}}}`,
		`{"type":"stream_delta","delta":{"message_type":"assistant_message","content":""}}`,
		`{"type":"stream_delta","delta":{"message_type":"reasoning_message","reasoning":""}}`,
		`{"type":"stream_delta","delta":{"message_type":"reasoning_message","reasoning":12}}`,
	} {
		a.HandleOutput([]byte(raw))
	}
	assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
	delta := map[string]any{"message_type": "assistant_message", "content": "A child must own this text."}
	a.HandleOutput(lettaChildDeltaFrame(t, delta))
	assert.Empty(t, sink.Messages())
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	a.HandleOutput(lettaChildStateFrame(t, "completed"))
	target := sink.Child(row.ChildAgentID)
	before := target.Messages()
	a.HandleOutput(lettaChildDeltaFrame(t, delta))
	assert.Equal(t, before, target.Messages())
	assert.Zero(t, target.ProgressSnapshot().ThinkingTokens)
	assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
}

func TestLettaModelProgressCompletesWhenPersistenceFails(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"tool", "turn", "exit"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{PersistErr: assert.AnError}
			a := &Agent{sink: agent.NewModelProgressResetSink(agent.NewProviderServices(sink))}
			// Seed the progress independently so this regression isolates failed-persistence cleanup.
			a.sink.ReportProgress(agent.ModelTextProgress("main", "Pending text must complete."))
			a.sink.ReportProgress(agent.ModelTextProgress("main:reasoning", "Pending reasoning must complete."))
			a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"assistant_message","content":"Pending text must complete."}}`))
			a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"reasoning_message","reasoning":"Pending reasoning must complete."}}`))
			require.Positive(t, sink.ProgressSnapshot().ThinkingTokens)
			switch boundary {
			case "tool":
				a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"client_tool_start","tool_call_id":"call-1","tool_name":"Bash"}}`))
			case "turn":
				a.HandleOutput([]byte(lettaLiveTurnFinished))
			case "exit":
				a.finishOutputAfterExit(agent.MessageCompletionError)
			}
			assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
		})
	}
}

func TestLettaDeltaText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, raw, want string }{
		{name: "string", raw: `"Plain 한글 😊"`, want: "Plain 한글 😊"},
		{name: "text blocks", raw: `[{"type":"text","text":"first"},{"type":"text","text":" second"}]`, want: "first second"},
		{name: "unsupported block", raw: `[{"type":"image","text":"not model text"},{"type":"text","text":"actual text"}]`, want: "actual text"},
		{name: "empty", raw: `[]`}, {name: "null", raw: `null`}, {name: "malformed", raw: `{"text":`}, {name: "number", raw: `0`},
	} {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); assert.Equal(t, tc.want, deltaText(json.RawMessage(tc.raw))) })
	}
}

func TestLettaModelProgressConcurrentFrames(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.sink = agent.NewModelProgressResetSink(a.sink)
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	child := sink.Child(row.ChildAgentID)
	rootFrame := []byte(`{"type":"stream_delta","delta":{"message_type":"assistant_message","content":"abcdefgh"}}`)
	childFrame := lettaChildDeltaFrame(t, map[string]any{"message_type": "assistant_message", "content": "abcdefghijkl"})
	var writers sync.WaitGroup
	for range 32 {
		writers.Go(func() { a.HandleOutput(rootFrame) })
		writers.Go(func() { a.HandleOutput(childFrame) })
	}
	writers.Wait()
	assert.Equal(t, int64(64), sink.ProgressSnapshot().ThinkingTokens)
	assert.Equal(t, int64(96), child.ProgressSnapshot().ThinkingTokens)
	a.HandleOutput([]byte(lettaLiveTurnFinished))
	assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
	assert.Equal(t, int64(96), child.ProgressSnapshot().ThinkingTokens, "root completion preserves child progress")
	a.HandleOutput(lettaChildStateFrame(t, "completed"))
	assert.Zero(t, child.ProgressSnapshot().ThinkingTokens)
}

func TestLettaNonCommandFieldsStayOpaque(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.sink = agent.NewModelProgressResetSink(a.sink)
	for _, text := range []string{"Keep this model text", " and its final continuation."} {
		raw, err := json.Marshal(map[string]any{
			"type":    "stream_delta",
			"runtime": map[string]any{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"},
			"delta":   map[string]any{"message_type": "assistant_message", "content": text, "input": map[string]any{"opaque": true}, "output": map[string]any{"opaque": true}, "success": "opaque metadata"},
		})
		require.NoError(t, err)
		a.HandleOutput(raw)
	}
	assert.Positive(t, sink.ProgressSnapshot().ThinkingTokens)
	a.HandleOutput([]byte(lettaLiveTurnFinished))
	messages := sink.Messages()
	require.Len(t, messages, 2)
	var content map[string]any
	require.NoError(t, json.Unmarshal(messages[0].Content, &content))
	assert.Equal(t, "Keep this model text and its final continuation.", content[contracts.AssembledMessageFieldText])
}
