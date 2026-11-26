package kilo

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func kiloTestMessage(t *testing.T, raw string) kiloNativeMessage {
	t.Helper()
	var message kiloNativeMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &message))
	return message
}

func TestKiloReplyHistorySelectsOnlyOwnedMessagesThroughTheFinalReply(t *testing.T) {
	t.Parallel()
	input := kiloTestMessage(t, `{"info":{"id":"input","sessionID":"session","role":"user"}}`)
	first := kiloTestMessage(t, `{"info":{"id":"first","sessionID":"session","role":"assistant","parentID":"input"}}`)
	final := kiloTestMessage(t, `{"info":{"id":"final","sessionID":"session","role":"assistant","parentID":"input"}}`)
	other := kiloTestMessage(t, `{"info":{"id":"other","sessionID":"session","role":"assistant","parentID":"other-input"}}`)
	later := kiloTestMessage(t, `{"info":{"id":"later","sessionID":"session","role":"assistant","parentID":"input"}}`)
	var cursors []string
	messages, err := kiloReplyHistory("session", "input", "final", func(cursor string) ([]kiloNativeMessage, string, error) {
		cursors = append(cursors, cursor)
		if cursor == "" {
			return []kiloNativeMessage{final, other, later}, "older", nil
		}
		return []kiloNativeMessage{input, first}, "", nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"", "older"}, cursors)
	assert.Equal(t, []kiloNativeMessage{first, final}, messages)
}

func TestKiloReplyHistoryRequiresTheExactInputAndFinalMessage(t *testing.T) {
	t.Parallel()
	input := kiloTestMessage(t, `{"info":{"id":"input","sessionID":"session","role":"user"}}`)
	wrong := kiloTestMessage(t, `{"info":{"id":"final","sessionID":"session","role":"assistant","parentID":"other-input"}}`)
	missing, err := kiloReplyHistory("session", "input", "final", func(string) ([]kiloNativeMessage, string, error) {
		return []kiloNativeMessage{input, wrong}, "", nil
	})
	require.Error(t, err)
	assert.Empty(t, missing)

	calls := 0
	_, err = kiloReplyHistory("session", "input", "final", func(string) ([]kiloNativeMessage, string, error) {
		calls++
		return nil, "same", nil
	})
	require.Error(t, err)
	assert.Equal(t, 2, calls)
}

func TestKiloReplyRequiresCompleteTranscriptTextForEachNativeMessage(t *testing.T) {
	t.Parallel()
	message := kiloTestMessage(t, `{"info":{"id":"reply","sessionID":"session","role":"assistant","parentID":"input"},"parts":[{"type":"text","text":"The first checklist is ready."},{"type":"text","ignored":true,"text":"Hidden"}]}`)
	var replies kiloNativeReplies
	replies.add("input", agent.NativeInputReceipt{SessionID: "session", InputID: "receipt"})
	replies.recordText("different-reply", "different-key", "The first checklist is ready.")
	replies.recordText("reply", "begin", "The first ")
	items, ready := replies.replyItems([]kiloNativeMessage{message})
	assert.False(t, ready)
	assert.Empty(t, items)
	replies.recordText("reply", "end", "checklist is ready.")
	replies.recordText("reply", "end", "checklist is ready.")
	items, ready = replies.replyItems([]kiloNativeMessage{message})
	assert.True(t, ready)
	assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "begin"}, {Ordinal: 3, MessageKey: "end"}}, items)
}

func TestKiloNativePromptPreservesThePreparedBlocksAndSession(t *testing.T) {
	t.Parallel()
	parts, err := kiloPreparedParts(acp.PreparedPrompt{
		SessionID: "session",
		Blocks: []map[string]any{
			{"type": "text", "text": ""},
			{"type": "image", "data": "AAE=", "mimeType": "image/png"},
		},
	}, "session")
	require.NoError(t, err)
	raw, err := json.Marshal(parts)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"type":"text","text":""},{"type":"file","url":"data:image/png;base64,AAE=","mime":"image/png","filename":"image"}]`, string(raw))
	_, err = kiloPreparedParts(acp.PreparedPrompt{
		SessionID: "different",
		Blocks:    []map[string]any{{"type": "text", "text": "input"}},
	}, "session")
	assert.ErrorIs(t, err, agent.ErrInputSessionChanged)
	slashParts, err := kiloPreparedParts(acp.PreparedPrompt{
		SessionID: "session",
		Blocks:    []map[string]any{{"type": "text", "text": " /compact"}},
	}, "session")
	require.NoError(t, err)
	assert.Empty(t, slashParts)

	resourceParts, err := kiloPreparedParts(acp.PreparedPrompt{
		SessionID: "session",
		Blocks: []map[string]any{
			{"type": "resource", "resource": map[string]any{"uri": "note.txt", "text": "hello"}},
			{"type": "resource", "resource": map[string]any{"uri": "doc.pdf", "blob": "AA==", "mimeType": "application/pdf"}},
		},
	}, "session")
	require.NoError(t, err)
	raw, err = json.Marshal(resourceParts)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"type":"text","text":"[note.txt]\nhello"},{"type":"file","url":"data:application/pdf;base64,AA==","mime":"application/pdf","filename":"file"}]`, string(raw))

	for _, fallbackBlock := range []map[string]any{
		{"type": "resource", "resource": map[string]any{"uri": "file:///etc/hosts", "text": "x"}},
		{"type": "resource", "resource": map[string]any{"uri": "doc.bin", "blob": "AA=="}},
		{"type": "audio", "data": "AA=="},
	} {
		fallback, err := kiloPreparedParts(acp.PreparedPrompt{SessionID: "session", Blocks: []map[string]any{fallbackBlock}}, "session")
		require.NoError(t, err)
		assert.Nil(t, fallback)
	}
}

func TestKiloPermissionResolvesOnlyOneTrackedToolOwner(t *testing.T) {
	t.Parallel()
	var replies kiloNativeReplies
	replies.add("steer", agent.NativeInputReceipt{SessionID: "session", TurnID: "steered-turn"})
	owned := kiloTestMessage(t, `{"info":{"id":"reply","sessionID":"session","role":"assistant","parentID":"steer"},"parts":[{"type":"tool","callID":"call"}]}`)
	other := kiloTestMessage(t, `{"info":{"id":"other","sessionID":"session","role":"assistant","parentID":"older-prompt"},"parts":[{"type":"tool","callID":"call"}]}`)
	assert.Equal(t, "steered-turn", replies.permissionTurnID("session", "call", []kiloNativeMessage{owned}))
	assert.Empty(t, replies.permissionTurnID("session", "call", []kiloNativeMessage{owned, other}))
	assert.Empty(t, replies.permissionTurnID("session", "call", []kiloNativeMessage{other}))
	assert.Empty(t, replies.permissionTurnID("other-session", "call", []kiloNativeMessage{owned}))
}

func TestKiloAssignsSeparateTranscriptKeysToSegmentsOfOneAssistantMessage(t *testing.T) {
	t.Parallel()
	a := newAgent(false)
	hooks := opencode.FamilyHooks(&a.FamilyBase)
	require.NotNil(t, hooks.AssistantTextKey)
	first := hooks.AssistantTextKey("msg_assistant")
	second := hooks.AssistantTextKey("msg_assistant")
	other := hooks.AssistantTextKey("msg_assistant_later")
	assert.Regexp(t, `^kilo-reply:"msg_assistant":[A-Za-z0-9]{48}$`, first)
	assert.Regexp(t, `^kilo-reply:"msg_assistant":[A-Za-z0-9]{48}$`, second)
	assert.Regexp(t, `^kilo-reply:"msg_assistant_later":[A-Za-z0-9]{48}$`, other)
	assert.NotEqual(t, first, second)
}

func TestKiloNativeRepliesPruneConsumedTextAndSignalWaiters(t *testing.T) {
	t.Parallel()
	var replies kiloNativeReplies
	replies.add("input-1", agent.NativeInputReceipt{SessionID: "session", InputID: "r1"})
	replies.add("input-2", agent.NativeInputReceipt{SessionID: "session", InputID: "r2"})

	changed := replies.textSignal()
	replies.recordText("msg-1", "k1", "hello")
	select {
	case <-changed:
	default:
		t.Fatal("recordText must close textChanged signal channel")
	}

	replies.recordText("msg-2", "k2", "world")
	replies.pruneText(map[string]bool{"msg-1": true})

	replies.mu.Lock()
	_, hasMsg1 := replies.text["msg-1"]
	_, hasMsg2 := replies.text["msg-2"]
	replies.mu.Unlock()
	assert.False(t, hasMsg1, "consumed message text must be pruned even while another native input remains active")
	assert.True(t, hasMsg2)
}

func TestKiloConfigureHooksResolvesPermissionTurnOwner(t *testing.T) {
	t.Parallel()
	a := newAgent(false)
	a.SetSessionIDForTest("session-1")
	a.nativeReplies.add("msg-1", agent.NativeInputReceipt{SessionID: "session-1", TurnID: "turn-1"})
	a.nativeReplies.toolOwners = map[kiloToolOwnerKey]string{
		{sessionID: "session-1", toolCallID: "call-1"}: "msg-1",
	}

	hooks := opencode.FamilyHooks(&a.FamilyBase)
	require.NotNil(t, hooks.ControlOwner)

	permLine := providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"session-1","toolCall":{"toolCallId":"call-1"}}}`))
	res, err := hooks.ControlOwner("session-1", permLine, true)
	require.NoError(t, err)
	assert.Equal(t, "turn-1", res.TurnID)

	resNoTurn, err := hooks.ControlOwner("session-1", permLine, false)
	require.NoError(t, err)
	assert.Empty(t, resNoTurn.TurnID)

	otherLine := providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":2,"method":"elicitation/create","params":{"sessionId":"session-1"}}`))
	resOther, err := hooks.ControlOwner("session-1", otherLine, true)
	require.NoError(t, err)
	assert.Empty(t, resOther.TurnID)
}
