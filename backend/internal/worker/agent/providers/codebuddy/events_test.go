package codebuddy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func codebuddyProgressFrame(t *testing.T, event any, overrides map[string]any) []byte {
	t.Helper()
	frame := map[string]any{
		"type": "stream_event", "session_id": "session-1", "parent_tool_use_id": nil,
		"_requestId": "request-1", "_messageId": "model-1", "event": event,
	}
	for key, value := range overrides {
		if value == nil {
			delete(frame, key)
		} else {
			frame[key] = value
		}
	}
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	return raw
}

func beginCodebuddyModelStream(t *testing.T, a *Agent, overrides map[string]any) {
	t.Helper()
	messageID := any("model-1")
	if value, ok := overrides["_messageId"]; ok {
		messageID = value
	}
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "message_start", "message": map[string]any{"id": messageID}}, overrides))
}

func beginCodebuddyModelBlock(t *testing.T, a *Agent, index int, kind string, overrides map[string]any) {
	t.Helper()
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{
		"type": "content_block_start", "index": index, "content_block": map[string]any{"type": kind, kind: ""},
	}, overrides))
}

func TestCodebuddyStreamedToolOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		index     any
		id        any
		toolName  any
		overrides map[string]any
		ended     bool
		admitted  bool
	}{
		{name: "root tool", index: 0, id: "tool-1", toolName: "AskUserQuestion", admitted: true},
		{name: "large index", index: 1 << 30, id: "tool-1", toolName: "Bash", admitted: true},
		{name: "absent index", id: "tool-1", toolName: "Bash"},
		{name: "negative index", index: -1, id: "tool-1", toolName: "Bash"},
		{name: "fractional index", index: 0.5, id: "tool-1", toolName: "Bash"},
		{name: "absent id", index: 0, toolName: "Bash"},
		{name: "empty id", index: 0, id: "", toolName: "Bash"},
		{name: "blank id", index: 0, id: " ", toolName: "Bash"},
		{name: "nul id", index: 0, id: "tool\x00", toolName: "Bash"},
		{name: "numeric id", index: 0, id: 0, toolName: "Bash"},
		{name: "absent name", index: 0, id: "tool-1"},
		{name: "blank name", index: 0, id: "tool-1", toolName: " "},
		{name: "nul name", index: 0, id: "tool-1", toolName: "Bash\x00"},
		{name: "foreign session", index: 0, id: "tool-1", toolName: "Bash", overrides: map[string]any{"session_id": "foreign"}},
		{name: "foreign response", index: 0, id: "tool-1", toolName: "Bash", overrides: map[string]any{"_messageId": "foreign"}},
		{name: "known child", index: 0, id: "tool-1", toolName: "Bash", overrides: map[string]any{"parent_tool_use_id": "spawn-1"}},
		{name: "unknown child", index: 0, id: "tool-1", toolName: "Bash", overrides: map[string]any{"parent_tool_use_id": "unknown"}},
		{name: "completed stream", index: 0, id: "tool-1", toolName: "Bash", ended: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			openCodebuddyProgressChild(t, a)
			beginCodebuddyModelStream(t, a, nil)
			beginCodebuddyModelStream(t, a, map[string]any{"parent_tool_use_id": "spawn-1"})
			if tc.ended {
				a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "message_stop"}, nil))
			}
			a.HandleOutput(codebuddyProgressFrame(t, map[string]any{
				"type": "content_block_start", "index": tc.index,
				"content_block": map[string]any{"type": "tool_use", "id": tc.id, "name": tc.toolName},
			}, tc.overrides))
			_, admitted := a.turnToolCalls["tool-1"]
			assert.Equal(t, tc.admitted, admitted)
			assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
		})
	}
}

func TestCodebuddyStreamedToolsKeepBlockIdentityAndFinalCounts(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	beginCodebuddyModelStream(t, a, nil)
	tool := func(id string) []byte {
		return codebuddyProgressFrame(t, map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "tool_use", "id": id, "name": "AskUserQuestion"},
		}, nil)
	}
	a.HandleOutput(tool("tool-1"))
	a.HandleOutput(tool("duplicate"))
	beginCodebuddyModelBlock(t, a, 0, "text", nil)
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "invalid tool text"}}, nil))
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "content_block_stop", "index": 0}, nil))
	a.HandleOutput(tool("after-block-stop"))
	assert.Equal(t, map[string]struct{}{"tool-1": {}}, a.turnToolCalls)
	assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","_requestId":"request-1","_messageId":"model-1","message":{"content":[{"type":"tool_use","id":"tool-1","name":"AskUserQuestion"}]}}`))
	a.HandleOutput([]byte(codebuddyResultFinished))
	assert.Equal(t, []int{1}, agenttest.TurnToolUseCounts(t, sink.Messages()))
	a.HandleOutput(tool("after-turn"))
	assert.Empty(t, a.turnToolCalls, "a late tool block cannot restore a completed turn's ownership")
}

func TestCodebuddyModelProgress(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"text", "thinking"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			beginCodebuddyModelStream(t, a, nil)
			a.HandleOutput(codebuddyProgressFrame(t, map[string]any{
				"type": "content_block_start", "index": 1 << 30,
				"content_block": map[string]any{"type": kind, kind: "你好世界"},
			}, nil))
			assert.EqualValues(t, 1, sink.ProgressSnapshot().ThinkingTokens, "count Unicode characters, not UTF-8 bytes")
			a.HandleOutput(codebuddyProgressFrame(t, map[string]any{
				"type": "content_block_delta", "index": 1 << 30,
				"delta": map[string]any{"type": kind + "_delta", kind: "abcdefgh"},
			}, nil))
			assert.EqualValues(t, 3, sink.ProgressSnapshot().ThinkingTokens)
			assert.Zero(t, sink.ProgressSnapshot().OutputBytes)
			assert.Zero(t, len(sink.Messages()), "partial frames supply no transcript rows")
			assert.True(t, a.active, "a native model start begins the root turn")
		})
	}
}

func TestCodebuddyModelProgressRejectsUnscopedEvents(t *testing.T) {
	t.Parallel()
	delta := map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "abcdefgh"}}
	cases := []struct {
		name      string
		event     any
		overrides map[string]any
	}{
		{name: "foreign session", event: delta, overrides: map[string]any{"session_id": "other"}},
		{name: "missing session", event: delta, overrides: map[string]any{"session_id": nil}},
		{name: "missing request", event: delta, overrides: map[string]any{"_requestId": nil}},
		{name: "blank request", event: delta, overrides: map[string]any{"_requestId": " "}},
		{name: "null message", event: delta, overrides: map[string]any{"_messageId": json.RawMessage(`null`)}},
		{name: "numeric message", event: delta, overrides: map[string]any{"_messageId": 7}},
		{name: "another response", event: delta, overrides: map[string]any{"_messageId": "other"}},
		{name: "blank parent", event: delta, overrides: map[string]any{"parent_tool_use_id": " "}},
		{name: "unknown child", event: delta, overrides: map[string]any{"parent_tool_use_id": "unknown"}},
		{name: "missing event", overrides: map[string]any{"event": nil}},
		{name: "invalid event shape", event: "not an event"},
		{name: "missing index", event: map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "abcdefgh"}}},
		{name: "negative index", event: map[string]any{"type": "content_block_delta", "index": -1, "delta": map[string]any{"type": "text_delta", "text": "abcdefgh"}}},
		{name: "fractional index", event: map[string]any{"type": "content_block_delta", "index": 0.5, "delta": map[string]any{"type": "text_delta", "text": "abcdefgh"}}},
		{name: "unstarted block", event: map[string]any{"type": "content_block_delta", "index": 99, "delta": map[string]any{"type": "text_delta", "text": "abcdefgh"}}},
		{name: "wrong delta kind", event: map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "abcdefgh"}}},
		{name: "signature", event: map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "signature_delta", "signature": "abcdefgh"}}},
		{name: "tool arguments", event: map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": "abcdefgh"}}},
		{name: "invalid text type", event: map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": 5}}},
		{name: "mismatched start identity", event: map[string]any{"type": "message_start", "message": map[string]any{"id": "different"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			beginCodebuddyModelStream(t, a, nil)
			beginCodebuddyModelBlock(t, a, 0, "text", nil)
			a.HandleOutput(codebuddyProgressFrame(t, delta, nil))
			require.EqualValues(t, 2, sink.ProgressSnapshot().ThinkingTokens)
			a.HandleOutput(codebuddyProgressFrame(t, tc.event, tc.overrides))
			assert.EqualValues(t, 2, sink.ProgressSnapshot().ThinkingTokens)
			a.HandleOutput(codebuddyProgressFrame(t, delta, nil))
			assert.EqualValues(t, 4, sink.ProgressSnapshot().ThinkingTokens, "a refused frame preserves the valid stream")
			assert.Zero(t, len(sink.Messages()))
		})
	}
}

func TestCodebuddyModelProgressCompletesScopes(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	beginCodebuddyModelStream(t, a, nil)
	for index, kind := range []string{"thinking", "text"} {
		beginCodebuddyModelBlock(t, a, index, kind, nil)
		a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": kind + "_delta", kind: "abcdefgh"}}, nil))
	}
	require.EqualValues(t, 4, sink.ProgressSnapshot().ThinkingTokens)
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "content_block_stop", "index": 0}, nil))
	beginCodebuddyModelBlock(t, a, 0, "thinking", nil)
	beginCodebuddyModelStream(t, a, nil)
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "late text"}}, nil))
	assert.EqualValues(t, 4, sink.ProgressSnapshot().ThinkingTokens, "completed blocks and duplicate starts cannot reopen or reset progress")
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": "abcdefgh"}}, nil))
	assert.EqualValues(t, 6, sink.ProgressSnapshot().ThinkingTokens)
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "message_stop"}, nil))
	assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
	beginCodebuddyModelStream(t, a, nil)
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": "late text"}}, nil))
	assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
	a.HandleOutput([]byte(`{"type":"result","session_id":"session-1","result":"","is_error":false}`))
	beginCodebuddyModelStream(t, a, nil)
	assert.False(t, a.active, "a duplicate start cannot reopen the completed turn")
}

func TestCodebuddyModelProgressResetsNewResponse(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	delta := map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "abcdefgh"}}
	beginCodebuddyModelStream(t, a, nil)
	beginCodebuddyModelBlock(t, a, 0, "text", nil)
	a.HandleOutput(codebuddyProgressFrame(t, delta, nil))
	require.EqualValues(t, 2, sink.ProgressSnapshot().ThinkingTokens)
	second := map[string]any{"_messageId": "model-2"}
	beginCodebuddyModelStream(t, a, second)
	assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
	beginCodebuddyModelBlock(t, a, 0, "text", second)
	a.HandleOutput(codebuddyProgressFrame(t, delta, second))
	assert.EqualValues(t, 2, sink.ProgressSnapshot().ThinkingTokens)
	a.HandleOutput(codebuddyProgressFrame(t, delta, nil))
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "message_stop"}, nil))
	a.HandleOutput(codebuddyProgressFrame(t, delta, second))
	assert.EqualValues(t, 4, sink.ProgressSnapshot().ThinkingTokens)
	assert.Zero(t, len(sink.Messages()))
}

func TestCodebuddyModelProgressClosesAnEmptyBlock(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	beginCodebuddyModelStream(t, a, nil)
	beginCodebuddyModelBlock(t, a, 0, "text", nil)
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "content_block_stop", "index": 0}, nil))
	beginCodebuddyModelBlock(t, a, 0, "text", nil)
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "late text"}}, nil))
	assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
	assert.Zero(t, len(sink.Messages()))
}

func TestCodebuddyModelProgressIgnoresNonTextBlocks(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	beginCodebuddyModelStream(t, a, nil)
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "text": "abcdefgh"}}, nil))
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "abcdefgh"}}, nil))
	assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
	assert.Zero(t, len(sink.Messages()))
}

func TestCodebuddyModelProgressPreservesCompletedAnswers(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"text", "thinking"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			beginCodebuddyModelStream(t, a, nil)
			beginCodebuddyModelBlock(t, a, 0, "text", nil)
			delta := map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Final answer."}}
			a.HandleOutput(codebuddyProgressFrame(t, delta, nil))
			require.Positive(t, sink.ProgressSnapshot().ThinkingTokens)
			completed, err := json.Marshal(map[string]any{"type": "assistant", "session_id": "session-1", "_requestId": "request-1", "_messageId": "model-1", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": kind, kind: "Final answer."}}}})
			require.NoError(t, err)
			a.HandleOutput(completed)
			a.HandleOutput(codebuddyProgressFrame(t, delta, nil))
			assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens, "late deltas cannot reopen a persisted answer")
			a.HandleOutput([]byte(`{"type":"result","session_id":"session-1","result":"Final answer.","is_error":false}`))
			var answers []string
			for _, row := range sink.Messages() {
				var frame struct {
					Type    string
					Message struct {
						Content []struct {
							Type string
							Text string
						}
					}
				}
				require.NoError(t, json.Unmarshal(row.Content, &frame))
				if frame.Type == "assistant" {
					for _, block := range frame.Message.Content {
						if block.Type == "text" {
							answers = append(answers, block.Text)
						}
					}
				}
			}
			assert.Equal(t, []string{"Final answer."}, answers)
			wantRows := 2
			if kind == "thinking" {
				wantRows = 3
			}
			assert.Len(t, sink.Messages(), wantRows)
		})
	}
}

func openCodebuddyProgressChild(t *testing.T, a *Agent) string {
	t.Helper()
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","message":{"content":[{"type":"tool_use","id":"spawn-1","name":"Agent","input":{"prompt":"Reply.","description":"Progress child","run_in_background":true}}]}}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"background-1","tool_use_id":"spawn-1","task_type":"local_agent"}`))
	_, childID := a.tasks.child("spawn-1")
	require.NotEmpty(t, childID)
	return childID
}

func TestCodebuddyModelProgressIsolatesChild(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	childID := openCodebuddyProgressChild(t, a)
	delta := map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "abcdefgh"}}
	beginCodebuddyModelStream(t, a, nil)
	beginCodebuddyModelBlock(t, a, 0, "text", nil)
	a.HandleOutput(codebuddyProgressFrame(t, delta, nil))
	child := map[string]any{"parent_tool_use_id": "spawn-1", "_requestId": "child-request", "_messageId": "child-model"}
	beginCodebuddyModelStream(t, a, child)
	beginCodebuddyModelBlock(t, a, 0, "text", child)
	a.HandleOutput(codebuddyProgressFrame(t, delta, child))
	assert.EqualValues(t, 2, sink.ProgressSnapshot().ThinkingTokens)
	assert.EqualValues(t, 2, sink.Child(childID).ProgressSnapshot().ThinkingTokens)
	a.HandleOutput([]byte(`{"type":"result","session_id":"session-1","result":"","is_error":false}`))
	a.HandleOutput(codebuddyProgressFrame(t, delta, child))
	assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
	assert.EqualValues(t, 4, sink.Child(childID).ProgressSnapshot().ThinkingTokens)
	assert.False(t, a.active, "a background child's stream cannot restart the root turn")
	a.HandleOutput([]byte(`{"type":"result","session_id":"session-1","parent_tool_use_id":"spawn-1","result":"child finished"}`))
	a.HandleOutput(codebuddyProgressFrame(t, delta, child))
	assert.Zero(t, sink.Child(childID).ProgressSnapshot().ThinkingTokens)
}

func TestCodebuddyModelProgressIgnoresUnknownOrFinishedChild(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	child := map[string]any{"parent_tool_use_id": "spawn-1"}
	delta := map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "abcdefgh"}}
	beginCodebuddyModelStream(t, a, child)
	beginCodebuddyModelBlock(t, a, 0, "text", child)
	a.HandleOutput(codebuddyProgressFrame(t, delta, child))
	assert.Empty(t, sink.BackgroundTasks())
	assert.Zero(t, len(sink.Messages()))
	assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
	childID := openCodebuddyProgressChild(t, a)
	beginCodebuddyModelStream(t, a, child)
	beginCodebuddyModelBlock(t, a, 0, "text", child)
	a.HandleOutput(codebuddyProgressFrame(t, delta, child))
	require.Positive(t, sink.Child(childID).ProgressSnapshot().ThinkingTokens)
	a.finishTask("background-1", bgtask.StatusSucceeded, "done")
	updates := len(sink.Child(childID).ProgressUpdates())
	beginCodebuddyModelStream(t, a, child)
	a.HandleOutput(codebuddyProgressFrame(t, delta, child))
	assert.Len(t, sink.Child(childID).ProgressUpdates(), updates)
	row, exists := sink.BackgroundTask("background-1")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
}

type codebuddyRetiredProgressSink struct {
	*agenttest.Sink
	retired  map[string]bool
	reopened bool
}

func (s *codebuddyRetiredProgressSink) ChildSink(childID string) agent.ProviderServices {
	if s.retired[childID] {
		s.reopened = true
	}
	return s.Sink.ChildSink(childID)
}

func (s *codebuddyRetiredProgressSink) CleanupChildAgent(childID string) {
	s.retired[childID] = true
	s.Sink.CleanupChildAgent(childID)
}

func TestCodebuddyModelProgressDoesNotReopenRetiredChild(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	recorder := &codebuddyRetiredProgressSink{Sink: sink, retired: make(map[string]bool)}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(recorder))
	childID := openCodebuddyProgressChild(t, a)
	child := map[string]any{"parent_tool_use_id": "spawn-1"}
	delta := map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "abcdefgh"}}
	beginCodebuddyModelStream(t, a, child)
	beginCodebuddyModelBlock(t, a, 0, "text", child)
	a.HandleOutput(codebuddyProgressFrame(t, delta, child))
	require.Positive(t, sink.Child(childID).ProgressSnapshot().ThinkingTokens)
	a.closeWorkflowChild(codebuddyWorkflowChild{rowKey: "background-1", status: bgtask.StatusSucceeded}, bgtask.StatusSucceeded)
	require.True(t, recorder.retired[childID])
	_, linked := a.tasks.child("spawn-1")
	require.Equal(t, childID, linked, "workflow completion retains its native mapping")
	a.HandleOutput(codebuddyProgressFrame(t, delta, child))
	assert.False(t, recorder.reopened, "late telemetry must not request a new sink for a retired child")
}

type codebuddyProgressLockSink struct {
	*agenttest.Sink
	a      *Agent
	denied bool
}

func (s *codebuddyProgressLockSink) ReportProgress(update agent.ProgressUpdate) {
	if s.a.mu.TryLock() {
		s.a.mu.Unlock()
	} else {
		s.denied = true
	}
	s.Sink.ReportProgress(update)
}

func TestCodebuddyModelProgressReleasesStateLock(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	recorder := &codebuddyProgressLockSink{Sink: sink, a: a}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(recorder))
	beginCodebuddyModelStream(t, a, nil)
	beginCodebuddyModelBlock(t, a, 0, "text", nil)
	a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "abcdefgh"}}, nil))
	require.Positive(t, sink.ProgressSnapshot().ThinkingTokens)
	assert.False(t, recorder.denied)
}

func TestCodebuddyModelProgressIgnoresCompactionAndStoppedProcess(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"compaction", "manual compaction", "stopped"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			switch state {
			case "compaction":
				a.compactionLive = true
			case "manual compaction":
				a.manualCompaction = true
			case "stopped":
				a.SetStoppedForTest(true)
			}
			beginCodebuddyModelStream(t, a, nil)
			beginCodebuddyModelBlock(t, a, 0, "text", nil)
			a.HandleOutput(codebuddyProgressFrame(t, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "abcdefgh"}}, nil))
			assert.Zero(t, sink.ProgressSnapshot().ThinkingTokens)
			assert.False(t, a.active)
			assert.Zero(t, len(sink.Messages()))
		})
	}
}
