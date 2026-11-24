package kilo

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
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
	parts, err := kiloPreparedParts(json.RawMessage(`{"sessionId":"session","prompt":[{"type":"text","text":""},{"type":"image","data":"AAE=","mimeType":"image/png"}]}`), "session")
	require.NoError(t, err)
	raw, err := json.Marshal(parts)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"type":"text","text":""},{"type":"file","url":"data:image/png;base64,AAE=","mime":"image/png","filename":"image"}]`, string(raw))
	_, err = kiloPreparedParts(json.RawMessage(`{"sessionId":"different","prompt":[{"type":"text","text":"input"}]}`), "session")
	assert.ErrorIs(t, err, agent.ErrInputSessionChanged)
	slashParts, err := kiloPreparedParts(json.RawMessage(`{"sessionId":"session","prompt":[{"type":"text","text":" /compact"}]}`), "session")
	require.NoError(t, err)
	assert.Empty(t, slashParts)
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
