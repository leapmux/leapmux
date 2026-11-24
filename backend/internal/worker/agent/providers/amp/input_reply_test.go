package amp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	assert.True(t, history.blocksTool("TU-1"))
	assert.False(t, history.blocksTool("TU-later"))
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
	first, err := h.agent.SendInputWithReceipt("Identical prompt", nil)
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
	second, err = h.agent.SendInputWithReceipt("Identical prompt", nil)
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
	receipt, err := h.agent.SendInputWithReceipt("prompt", nil)
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
	assert.False(t, exported.replyHistory("owned").blocksTool("TU-later"))
	assert.True(t, exported.replyHistory("later").blocksTool("TU-later"))
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
