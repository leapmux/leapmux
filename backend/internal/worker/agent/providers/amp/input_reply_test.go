package amp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestAmpNativeInputID(t *testing.T) {
	t.Parallel()
	// The actor received these IDs from Amp 0.0.1795176040 on new and resumed threads.
	assert.Equal(t, "M-6GUcx20ixJvAspYo5joWXW", ampNativeInputID("probe-amp-repeat"))
	assert.Equal(t, "M-7dujcYidHgHW5FncmFWMWF", ampNativeInputID("probe-amp-distinct"))
}

func TestAmpReplyHistoryUsesTheExactInputAndNativeEnd(t *testing.T) {
	t.Parallel()
	exported, err := parseThreadExport([]byte(`{"id":"T-owned","messages":[
		{"protocolMessageID":"other-input","role":"user","content":[{"type":"text","text":"Same prompt"}]},
		{"protocolMessageID":"other-answer","role":"assistant","content":[{"type":"text","text":"Foreign"}],"state":{"type":"complete","stopReason":"end_turn"}},
		{"protocolMessageID":"my-input","role":"user","content":[{"type":"text","text":"Same prompt"}]},
		{"protocolMessageID":"my-tool","role":"assistant","content":[{"type":"tool_use","id":"TU-1","name":"read","input":{"z":2,"a":1}}],"state":{"type":"complete","stopReason":"tool_use"}},
		{"protocolMessageID":"tool-result","role":"user","content":[{"type":"tool_result"}]},
		{"protocolMessageID":"steer","role":"user","meta":{"steer":true},"content":[{"type":"text","text":"Continue"}]},
		{"protocolMessageID":"my-answer","role":"assistant","content":[{"type":"text","text":"Owned"}],"state":{"type":"complete","stopReason":"end_turn"}},
		{"protocolMessageID":"later-answer","role":"assistant","content":[{"type":"tool_use","id":"TU-later"}],"state":{"type":"complete"}}
	]}`), "T-owned")
	require.NoError(t, err)
	history := exported.replyHistory("my-input")
	require.True(t, history.ended)
	_, hasOwnedTool := history.toolFrame("TU-1")
	assert.True(t, hasOwnedTool)
	_, hasLaterTool := history.toolFrame("TU-later")
	assert.False(t, hasLaterTool)
	toolDigest, err := ampReplyFrameDigest([]ampBlock{{Type: "tool_use", ID: "TU-1", Name: "read", Input: json.RawMessage(`{"a":1,"z":2}`)}})
	require.NoError(t, err)
	answerDigest, err := ampReplyFrameDigest([]ampBlock{{Type: "text", Text: "Owned"}})
	require.NoError(t, err)
	reply := ampInputReply{
		receipt: agent.NativeInputReceipt{SessionID: "T-owned", InputID: "my-input"},
		frames:  []ampReplyFrame{{digest: toolDigest}, {digest: answerDigest, textKeys: []string{"stored-owned-text"}}},
	}
	turn, proved := reply.project(history)
	require.True(t, proved)
	assert.Equal(t, agent.NativeReplyTurn{
		SessionID: "T-owned", TurnID: "my-input", Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{{InputID: "my-input", AfterOrdinal: 1}},
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "stored-owned-text"}},
	}, turn)

	for _, caseName := range []string{"missing input", "other input before settlement", "unfinished native frame", "different assistant frame", "lost row"} {
		t.Run(caseName, func(t *testing.T) {
			candidate := reply
			inputID := reply.receipt.InputID
			messages := *exported
			switch caseName {
			case "missing input":
				inputID = "absent"
			case "other input before settlement":
				foreign := exported.Messages[0]
				foreign.ID = "racing-new-input"
				messages.Messages = append(append([]ampExportMessage{}, exported.Messages[:6]...), foreign, exported.Messages[6])
			case "unfinished native frame":
				messages.Messages = append([]ampExportMessage{}, exported.Messages...)
				messages.Messages[3].State.Type = "in-progress"
			case "different assistant frame":
				candidate.frames = []ampReplyFrame{{digest: toolDigest}, {digest: toolDigest, textKeys: []string{"stored-foreign-text"}}}
			case "lost row":
				candidate.unproved = true
			}
			_, proved := candidate.project(messages.replyHistory(inputID))
			assert.False(t, proved)
		})
	}
}

type replyRecordingSink struct {
	agent.ProviderServices
	ready chan agent.NativeReplyTurn
}

func (sink *replyRecordingSink) PublishNativeReplyTurn(turn agent.NativeReplyTurn) error {
	if err := sink.ProviderServices.PublishNativeReplyTurn(turn); err != nil {
		return err
	}
	sink.ready <- turn
	return nil
}

func TestAmpPublishesExactRepliesEvenWhenAnEarlierExportWaits(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const threadID = "T-b0ed606d-a7d4-44b0-a078-2ff4ff611c5e"
	h.agent.createThreadFn = func(_ context.Context, config threadConfiguration) (string, error) {
		assert.Equal(t, h.agent.agentMode, config.mode)
		return threadID, nil
	}
	sink := &replyRecordingSink{ProviderServices: h.agent.sink, ready: make(chan agent.NativeReplyTurn, 2)}
	h.agent.sink = sink
	first, err := h.agent.SendInput("Identical prompt", nil)
	require.NoError(t, err)
	proc := h.nextProc()
	assert.Equal(t, []string{threadID}, h.startedThreads())
	assert.Equal(t, threadID, first.SessionID)
	key, ok := proc.lines()[0]["request_id"].(string)
	require.True(t, ok)
	assert.Len(t, key, id.GeneratedLength)
	assert.Equal(t, ampNativeInputID(key), first.InputID)
	assert.False(t, h.agent.PublishTurnActive().Steerable)
	h.feed(proc, fmt.Sprintf(`{"type":"system","subtype":"init","session_id":%q}`, threadID))
	assert.True(t, h.agent.PublishTurnActive().Steerable)

	var second agent.NativeInputReceipt
	var exportCalls atomic.Int32
	firstStarted, releaseFirst := make(chan struct{}), make(chan struct{})
	h.agent.exportFn = func(ctx context.Context, exportedThread string) ([]byte, error) {
		assert.Equal(t, threadID, exportedThread)
		if exportCalls.Add(1) == 1 {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []byte(fmt.Sprintf(`{"id":%q,"messages":[
			{"protocolMessageID":%q,"role":"user"},
			{"protocolMessageID":"answer-1","role":"assistant","content":[{"type":"text","text":"First reply."}],"state":{"type":"complete","stopReason":"end_turn"}},
			{"protocolMessageID":%q,"role":"user"},
			{"protocolMessageID":"answer-2","role":"assistant","content":[{"type":"text","text":"Second reply."}],"state":{"type":"complete","stopReason":"end_turn"}}
		]}`, threadID, first.InputID, second.InputID)), nil
	}
	feedReply := func(text string) {
		h.feed(proc, fmt.Sprintf(`{"type":"assistant","session_id":%q,"message":{"content":[{"type":"text","text":%q}],"stop_reason":"end_turn"}}`, threadID, text))
	}
	ctx := testutil.DeadlineContext(t)
	feedReply("First reply.")
	select {
	case <-firstStarted:
	case <-ctx.Done():
		t.Fatal("the first reply did not begin its export")
	}
	second, err = h.agent.SendInput("Identical prompt", nil)
	require.NoError(t, err)
	assert.NotEqual(t, first.InputID, second.InputID)
	feedReply("Second reply.")

	func() {
		defer close(releaseFirst)
		select {
		case projected := <-sink.ready:
			assert.Equal(t, second.InputID, projected.Inputs[0].InputID)
			assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: replyTextMessageKey(second, 0, 0)}}, projected.Items)
		case <-ctx.Done():
			t.Fatal("the second native reply waited for the first export")
		}
	}()
	select {
	case projected := <-sink.ready:
		assert.Equal(t, first.InputID, projected.Inputs[0].InputID)
		assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: replyTextMessageKey(first, 0, 0)}}, projected.Items)
	case <-ctx.Done():
		t.Fatal("the first export did not publish its reply")
	}
	assert.Len(t, h.sink.NativeReplyTurns(), 2)
	rows := h.rows()
	require.Len(t, rows, 2)
	for i, want := range []string{"First reply.", "Second reply."} {
		text, kept := (ampProvider{}).ReplyText(agent.MessageContent{Original: rows[i].Content})
		require.True(t, kept)
		assert.Equal(t, want, text)
		assert.Equal(t, threadID, rows[i].AgentSessionID)
	}
	assert.Equal(t, replyTextMessageKey(first, 0, 0), rows[0].IdempotencyKey)
	assert.Equal(t, replyTextMessageKey(second, 0, 0), rows[1].IdempotencyKey)
}

func TestAmpHostExitRetiresAcceptedInputWithoutCompletion(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	receipt, err := h.agent.SendInput("prompt", nil)
	require.NoError(t, err)
	proc := h.nextProc()
	proc.exit()
	proc.awaitHandled(t)
	assert.Empty(t, h.sink.NativeReplyTurns())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, h.sink.NativeReplyRetirements())
}

func TestAmpPermissionOwnershipEndsBeforeTheNextInput(t *testing.T) {
	t.Parallel()
	exported, err := parseThreadExport([]byte(`{"id":"T-1","messages":[
		{"protocolMessageID":"owned","role":"user"},
		{"protocolMessageID":"later","role":"user"},
		{"protocolMessageID":"tool","role":"assistant","content":[{"type":"tool_use","id":"TU-later"}]}
	]}`), "T-1")
	require.NoError(t, err)
	_, ownedHasTool := exported.replyHistory("owned").toolFrame("TU-later")
	assert.False(t, ownedHasTool)
	_, laterHasTool := exported.replyHistory("later").toolFrame("TU-later")
	assert.True(t, laterHasTool)
}

func TestAmpReplyReaderRejectsAnotherThread(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.agent.exportFn = func(context.Context, string) ([]byte, error) {
		return []byte(`{"id":"T-2","messages":[]}`), nil
	}
	_, err := h.agent.readReplyHistory(t.Context(), agent.NativeInputReceipt{SessionID: "T-1", InputID: "owned"})
	require.ErrorContains(t, err, "does not identify the requested thread")
}

func TestAmpThreadCreationOutput(t *testing.T) {
	t.Parallel()
	const threadID = "T-b0ed606d-a7d4-44b0-a078-2ff4ff611c5e"
	actual, err := threadIDFromCreateOutput([]byte("https://ampcode.com/threads/" + threadID + "\n"))
	require.NoError(t, err)
	assert.Equal(t, threadID, actual)
	for _, invalid := range []string{
		"", "https://ampcode.com/threads/T-1", "https://ampcode.com/threads/T-00000000-0000-0000-0000-000000000000",
		"https://ampcode.com/other/" + threadID, "https://ampcode.com/threads/" + threadID + "?bad=%zz",
	} {
		_, err := threadIDFromCreateOutput([]byte(invalid))
		assert.Error(t, err, invalid)
	}
}

func TestAmpJavaScriptWhitespaceAgreement(t *testing.T) {
	t.Parallel()
	nelRow := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"\u0085"}]}}`)
	bomRow := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"\uFEFF"}]}}`)
	got, ok := agent.ReplyText(ampProvider{}, agent.MessageContent{Original: nelRow})
	assert.True(t, ok, "U+0085 is visible in JavaScript trim")
	assert.Equal(t, "\u0085", got)
	_, ok = agent.ReplyText(ampProvider{}, agent.MessageContent{Original: bomRow})
	assert.False(t, ok, "U+FEFF is blank in JavaScript trim")

	assert.True(t, assistantBlockKept(ampBlock{Type: contracts.AmpBlockTypeText, Text: "\u0085"}))
	assert.False(t, assistantBlockKept(ampBlock{Type: contracts.AmpBlockTypeText, Text: "\uFEFF"}))
	assert.True(t, assistantBlockKept(ampBlock{Type: contracts.AmpBlockTypeThinking, Thinking: "\u0085"}))
	assert.False(t, assistantBlockKept(ampBlock{Type: contracts.AmpBlockTypeThinking, Thinking: "\uFEFF"}))

	h := newHarness(t, withResume("T-1"))
	ok = h.agent.persistExportMessage("T-1", ampExportMessage{ID: "M-ws"}, []ampBlock{
		{Type: contracts.AmpBlockTypeThinking, Thinking: "\uFEFF"},
		{Type: contracts.AmpBlockTypeThinking, Thinking: "\u0085"},
		{Type: contracts.AmpBlockTypeText, Text: "\uFEFF"},
		{Type: contracts.AmpBlockTypeText, Text: "\u0085"},
	})
	require.True(t, ok)
	rows := h.rows()
	require.Len(t, rows, 2, "persistExportMessage must drop U+FEFF blocks and keep U+0085 blocks")
	first := decodeRow(t, rows[0].Content)
	second := decodeRow(t, rows[1].Content)
	assert.Equal(t, string(agent.AssembledMessageKindReasoning), first["kind"])
	assert.Equal(t, "\u0085", first["text"])
	assert.Equal(t, string(agent.AssembledMessageKindText), second["kind"])
	assert.Equal(t, "\u0085", second["text"])
}

func TestAmpUnknownPermissionWrapsErrControlRequestNotPending(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	err := h.agent.answerPermission("missing-req", "", "allow", "")
	assert.ErrorIs(t, err, agent.ErrControlRequestNotPending)
}

func TestAmpBindReplyPermissionIncludesLateSteerAddedDuringPolling(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	lead, err := h.agent.SendInput("lead", nil)
	require.NoError(t, err)
	lateSteer := agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-late-steer"}

	var attempt atomic.Int32
	firstDone := make(chan struct{})
	releaseSecond := make(chan struct{})
	h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
		if attempt.Add(1) == 1 {
			close(firstDone)
			<-releaseSecond
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[{"protocolMessageID":%q,"role":"user"}]}`, threadID, lead.InputID)), nil
		}
		return []byte(fmt.Sprintf(`{"id":%q,"messages":[
			{"protocolMessageID":%q,"role":"user"},
			{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
			{"protocolMessageID":"asst-1","role":"assistant","content":[{"type":"tool_use","id":"TU-1","name":"Bash"}],"state":{"type":"in-progress"}}
		]}`, threadID, lead.InputID, lateSteer.InputID)), nil
	}
	trap := h.clock.Trap().NewTimer(AmpExportIntervalTag)
	defer trap.Close()

	h.agent.bindReplyPermission(t.Context(), agent.ControlRequest{
		AgentSessionID: "T-1", RequestID: "perm-1", InstanceID: "inst-1",
	}, "TU-1")
	ctx := testutil.DeadlineContext(t)
	select {
	case <-firstDone:
	case <-ctx.Done():
		t.Fatal("bindReplyPermission did not start first export")
	}
	// Late steer echoes while bindReplyPermission is polling.
	h.agent.mu.Lock()
	h.agent.turn.reply.steers = append(h.agent.turn.reply.steers, lateSteer)
	h.agent.mu.Unlock()
	close(releaseSecond)
	testutil.WaitForTimer(t, ctx, trap)
	h.clock.Advance(ampExportInterval).MustWait(ctx)
	testutil.RequireEventually(t, func() bool {
		return len(h.sink.NativeControlInputBindings()) > 0
	}, "permission binding must complete")
	bindings := h.sink.NativeControlInputBindings()
	assert.Equal(t, []string{lead.InputID, lateSteer.InputID}, bindings[len(bindings)-1].InputIDs)
}

func TestAmpStopWaitsForBackgroundReplyPublicationGoroutines(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	lead, err := h.agent.SendInput("lead", nil)
	require.NoError(t, err)
	proc := h.nextProc()

	entered := make(chan struct{})
	release := make(chan struct{})
	h.agent.exportFn = func(_ context.Context, _ string) ([]byte, error) {
		close(entered)
		<-release
		return nil, context.Canceled
	}
	h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn"}}`)
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("publishInputReply did not start")
	}
	stopped := make(chan struct{})
	go func() {
		h.agent.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		close(release)
		t.Fatal("Stop returned while publishInputReply goroutine was still running")
	case <-proc.handled:
	case <-ctx.Done():
		close(release)
		t.Fatal("Stop did not handle process exit")
	}
	select {
	case <-stopped:
		close(release)
		t.Fatal("Stop returned before publishInputReply goroutine exited")
	default:
	}
	close(release)
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("Stop did not finish after publishInputReply exited")
	}
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		lead: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, h.sink.NativeReplyRetirements())
}

func TestAmpReplyFrameDigest(t *testing.T) {
	t.Parallel()

	t.Run("nil and empty blocks produce the same digest", func(t *testing.T) {
		nilDigest, err := ampReplyFrameDigest(nil)
		require.NoError(t, err)
		emptyDigest, err := ampReplyFrameDigest([]ampBlock{})
		require.NoError(t, err)
		assert.Equal(t, nilDigest, emptyDigest)
	})

	t.Run("invalid JSON in Input or Content returns error", func(t *testing.T) {
		_, err := ampReplyFrameDigest([]ampBlock{{Type: contracts.AmpBlockTypeToolUse, Input: json.RawMessage(`{invalid`)}})
		assert.Error(t, err)
		_, err = ampReplyFrameDigest([]ampBlock{{Type: contracts.AmpBlockTypeToolResult, Content: json.RawMessage(`[unclosed`)}})
		assert.Error(t, err)
	})

	t.Run("length prefixes prevent field and block boundary collisions", func(t *testing.T) {
		a, err := ampReplyFrameDigest([]ampBlock{{Type: "text", Text: "ab", Thinking: "c"}})
		require.NoError(t, err)
		b, err := ampReplyFrameDigest([]ampBlock{{Type: "text", Text: "a", Thinking: "bc"}})
		require.NoError(t, err)
		assert.NotEqual(t, a, b)

		twoA, err := ampReplyFrameDigest([]ampBlock{{Type: "text", Text: "a"}, {Type: "text", Text: "bc"}})
		require.NoError(t, err)
		twoB, err := ampReplyFrameDigest([]ampBlock{{Type: "text", Text: "ab"}, {Type: "text", Text: "c"}})
		require.NoError(t, err)
		assert.NotEqual(t, twoA, twoB)
	})

	t.Run("Input nil vs empty object vs null are distinct", func(t *testing.T) {
		nilInput, err := ampReplyFrameDigest([]ampBlock{{Type: contracts.AmpBlockTypeToolUse, ID: "1", Name: "bash"}})
		require.NoError(t, err)
		emptyObj, err := ampReplyFrameDigest([]ampBlock{{Type: contracts.AmpBlockTypeToolUse, ID: "1", Name: "bash", Input: json.RawMessage(`{}`)}})
		require.NoError(t, err)
		nullInput, err := ampReplyFrameDigest([]ampBlock{{Type: contracts.AmpBlockTypeToolUse, ID: "1", Name: "bash", Input: json.RawMessage(`null`)}})
		require.NoError(t, err)
		assert.NotEqual(t, nilInput, emptyObj)
		assert.NotEqual(t, nilInput, nullInput)
		assert.NotEqual(t, emptyObj, nullInput)
	})

	t.Run("canonical JSON equivalence for Input and Content", func(t *testing.T) {
		first, err := ampReplyFrameDigest([]ampBlock{{
			Type:    contracts.AmpBlockTypeToolUse,
			Input:   json.RawMessage(`{"z":2,"a":{"y":true,"x":1}}`),
			Content: json.RawMessage(`[{"b":2,"a":1}]`),
		}})
		require.NoError(t, err)
		second, err := ampReplyFrameDigest([]ampBlock{{
			Type:    contracts.AmpBlockTypeToolUse,
			Input:   json.RawMessage(`{ "a": { "x": 1, "y": true }, "z": 2 }`),
			Content: json.RawMessage(`[ { "a": 1, "b": 2 } ]`),
		}})
		require.NoError(t, err)
		assert.Equal(t, first, second)
	})

	t.Run("IsError flag affects digest", func(t *testing.T) {
		okDigest, err := ampReplyFrameDigest([]ampBlock{{Type: contracts.AmpBlockTypeToolResult, IsError: false}})
		require.NoError(t, err)
		errDigest, err := ampReplyFrameDigest([]ampBlock{{Type: contracts.AmpBlockTypeToolResult, IsError: true}})
		require.NoError(t, err)
		assert.NotEqual(t, okDigest, errDigest)
	})
}

func TestAmpMatchesHistoryPrefixLen(t *testing.T) {
	t.Parallel()
	d0, err := ampReplyFrameDigest([]ampBlock{{Type: contracts.AmpBlockTypeText, Text: "frame-0"}})
	require.NoError(t, err)
	d1, err := ampReplyFrameDigest([]ampBlock{{Type: contracts.AmpBlockTypeText, Text: "frame-1"}})
	require.NoError(t, err)

	completeMsg := func(text string) ampExportMessage {
		msg := ampExportMessage{
			ID:      "M-" + text,
			Role:    contracts.AmpLineTypeAssistant,
			Content: []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"type":"text","text":%q}`, text))},
		}
		msg.State.Type = "complete"
		return msg
	}
	inProgressMsg := ampExportMessage{
		ID:      "M-ip",
		Role:    contracts.AmpLineTypeAssistant,
		Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"frame-1"}`)},
	}
	inProgressMsg.State.Type = "in-progress"

	badJSONMsg := ampExportMessage{
		ID:      "M-bad",
		Role:    contracts.AmpLineTypeAssistant,
		Content: []json.RawMessage{json.RawMessage(`{broken`)},
	}
	badJSONMsg.State.Type = "complete"

	reply := ampInputReply{
		receipt: agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-in"},
		frames:  []ampReplyFrame{{digest: d0}, {digest: d1}},
	}
	history := ampReplyHistory{
		present:   true,
		assistant: []ampExportMessage{completeMsg("frame-0"), completeMsg("frame-1")},
	}

	assert.True(t, reply.matchesHistoryPrefixLen(history, 0))
	assert.True(t, reply.matchesHistoryPrefixLen(history, 1))
	assert.True(t, reply.matchesHistoryPrefixLen(history, 2))
	assert.False(t, reply.matchesHistoryPrefixLen(history, 3), "frameCount exceeding reply and history length must fail")

	shortHistory := ampReplyHistory{present: true, assistant: []ampExportMessage{completeMsg("frame-0")}}
	assert.False(t, reply.matchesHistoryPrefixLen(shortHistory, 2), "frameCount exceeding history length must fail")

	shortReply := ampInputReply{receipt: reply.receipt, frames: []ampReplyFrame{{digest: d0}}}
	assert.False(t, shortReply.matchesHistoryPrefixLen(history, 2), "frameCount exceeding reply length must fail")

	laterInProgress := ampReplyHistory{present: true, assistant: []ampExportMessage{completeMsg("frame-0"), inProgressMsg}}
	assert.True(t, reply.matchesHistoryPrefixLen(laterInProgress, 1), "prefix length 1 ignores incomplete frame 1")
	assert.False(t, reply.matchesHistoryPrefixLen(laterInProgress, 2), "incomplete frame inside prefix fails")

	unproved := reply
	unproved.unproved = true
	assert.False(t, unproved.matchesHistoryPrefixLen(history, 0))

	absent := history
	absent.present = false
	assert.False(t, reply.matchesHistoryPrefixLen(absent, 0))

	badHistory := ampReplyHistory{present: true, assistant: []ampExportMessage{badJSONMsg}}
	assert.False(t, reply.matchesHistoryPrefixLen(badHistory, 1))

	mismatchHistory := ampReplyHistory{present: true, assistant: []ampExportMessage{completeMsg("other")}}
	assert.False(t, reply.matchesHistoryPrefixLen(mismatchHistory, 1))
}

func TestAmpCompleteInputReplyPaths(t *testing.T) {
	t.Parallel()

	t.Run("unproved reply retires lead and steers synchronously without export", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		var exportCalls atomic.Int32
		h.agent.exportFn = func(context.Context, string) ([]byte, error) {
			exportCalls.Add(1)
			return nil, errors.New("unexpected export")
		}
		lead := agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-lead"}
		steer := agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-steer"}
		h.agent.completeInputReply(&ampInputReply{
			receipt:  lead,
			steers:   []agent.NativeInputReceipt{steer},
			unproved: true,
		}, agent.MessageCompletionComplete)
		assert.Zero(t, exportCalls.Load())
		assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
			lead:  leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			steer: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}, h.sink.NativeReplyRetirements())
	})

	t.Run("stopped agent retires reply synchronously without export", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		var exportCalls atomic.Int32
		h.agent.exportFn = func(context.Context, string) ([]byte, error) {
			exportCalls.Add(1)
			return nil, errors.New("unexpected export")
		}
		h.agent.Stop()
		lead := agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-lead"}
		h.agent.completeInputReply(&ampInputReply{receipt: lead}, agent.MessageCompletionComplete)
		assert.Zero(t, exportCalls.Load())
		assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
			lead: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}, h.sink.NativeReplyRetirements())
	})

	t.Run("non-complete startsWithSteer defers retirement to recovery", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		steer := agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-steer"}
		h.agent.completeInputReply(&ampInputReply{
			receipt:         steer,
			startsWithSteer: true,
		}, agent.MessageCompletionInterrupted)
		assert.Empty(t, h.sink.NativeReplyRetirements())
	})
}

func TestAmpBindReplyPermissionPaths(t *testing.T) {
	t.Parallel()

	t.Run("excludes steers after the tool frame and foreign steers", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		lead, err := h.agent.SendInput("lead", nil)
		require.NoError(t, err)
		beforeSteer := agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-before"}
		afterSteer := agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-after"}
		h.agent.mu.Lock()
		h.agent.turn.reply.steers = []agent.NativeInputReceipt{beforeSteer, afterSteer}
		h.agent.mu.Unlock()

		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user"},
				{"protocolMessageID":"M-foreign","role":"user","meta":{"steer":true}},
				{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
				{"protocolMessageID":"asst-0","role":"assistant","content":[{"type":"tool_use","id":"TU-0","name":"Bash"}],"state":{"type":"in-progress"}},
				{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}
			]}`, threadID, lead.InputID, beforeSteer.InputID, afterSteer.InputID)), nil
		}

		h.agent.bindReplyPermission(t.Context(), agent.ControlRequest{
			AgentSessionID: "T-1", RequestID: "perm-1", InstanceID: "inst-1",
		}, "TU-0")
		h.agent.replyWorkWG.Wait()
		bindings := h.sink.NativeControlInputBindings()
		require.Len(t, bindings, 1)
		assert.Equal(t, []string{lead.InputID, beforeSteer.InputID}, bindings[0].InputIDs)
	})

	for _, tc := range []struct {
		name     string
		response func(leadID string) ([]byte, error)
	}{
		{
			name: "early exit on ended history",
			response: func(leadID string) ([]byte, error) {
				return []byte(fmt.Sprintf(`{"id":"T-1","messages":[
					{"protocolMessageID":%q,"role":"user"},
					{"protocolMessageID":"asst-0","role":"assistant","content":[{"type":"text","text":"done"}],"state":{"type":"complete","stopReason":"end_turn"}}
				]}`, leadID)), nil
			},
		},
		{
			name: "early exit on history conflict",
			response: func(leadID string) ([]byte, error) {
				return []byte(fmt.Sprintf(`{"id":"T-1","messages":[
					{"protocolMessageID":%q,"role":"user"},
					{"protocolMessageID":"M-other","role":"user"}
				]}`, leadID)), nil
			},
		},
		{
			name: "early exit on export error",
			response: func(string) ([]byte, error) {
				return nil, errors.New("export failed")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, withResume("T-1"))
			lead, err := h.agent.SendInput("lead", nil)
			require.NoError(t, err)
			var calls atomic.Int32
			h.agent.exportFn = func(context.Context, string) ([]byte, error) {
				calls.Add(1)
				return tc.response(lead.InputID)
			}
			h.agent.bindReplyPermission(t.Context(), agent.ControlRequest{
				AgentSessionID: "T-1", RequestID: "perm-1", InstanceID: "inst-1",
			}, "TU-missing")
			h.agent.replyWorkWG.Wait()
			assert.Equal(t, int32(1), calls.Load())
			assert.Empty(t, h.sink.NativeControlInputBindings())
		})
	}

	t.Run("Stop waits for in-flight bindReplyPermission via replyWorkWG", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		_, err := h.agent.SendInput("lead", nil)
		require.NoError(t, err)
		proc := h.nextProc()
		entered := make(chan struct{})
		release := make(chan struct{})
		h.agent.exportFn = func(context.Context, string) ([]byte, error) {
			close(entered)
			<-release
			return nil, context.Canceled
		}
		h.agent.bindReplyPermission(t.Context(), agent.ControlRequest{
			AgentSessionID: "T-1", RequestID: "perm-1", InstanceID: "inst-1",
		}, "TU-1")
		ctx := testutil.DeadlineContext(t)
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("bindReplyPermission did not start export")
		}
		stopped := make(chan struct{})
		go func() {
			h.agent.Stop()
			close(stopped)
		}()
		select {
		case <-stopped:
			close(release)
			t.Fatal("Stop returned while bindReplyPermission was still running")
		case <-proc.handled:
		case <-ctx.Done():
			close(release)
			t.Fatal("Stop did not handle process exit")
		}
		select {
		case <-stopped:
			close(release)
			t.Fatal("Stop returned before bindReplyPermission exited")
		default:
		}
		close(release)
		select {
		case <-stopped:
		case <-ctx.Done():
			t.Fatal("Stop did not finish after bindReplyPermission exited")
		}
	})
}
