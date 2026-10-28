package muse

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildNativeHeadAcceptsItsCutFromAnOwnedControlRequest(t *testing.T) {
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	state.childViewPrefix = childViewPrefixReady
	state.nativeHead = &childNativeHead{cursor: "control-cut", covered: make(map[[32]byte]struct{}), observed: make(map[[32]byte]struct{})}
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 17, "method": contracts.MuseMethodUserInputRequest,
		"params": map[string]any{
			"sessionId": "child", "viewCursor": "control-cut", "userInputId": "question",
			"itemId": "question-item", "toolCallId": "question-tool", "toolName": "request_user_input", "turnId": "head-turn",
			"questions": []any{museNativeQuestion("question", "single")},
		},
	})
	require.NoError(t, err)
	a.HandleOutput(request)
	assert.True(t, state.nativeHead.passed, "a native control request can identify the coherent view cut")
	a.HandleOutput(childLifecycleFrame(t, methodTurnStarted, "later-live", "later-turn"))
	assert.True(t, a.ActiveChildTurnState(state.childKey).Active)
	assert.Empty(t, child.Messages(), "crossing the activity cut does not fabricate a proved control transcript")
}
