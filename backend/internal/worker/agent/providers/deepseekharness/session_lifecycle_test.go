package deepseekharness

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootSnapshotDoesNotBecomeReadyWhenItsNativeChildCannotBeFollowed(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	raw := []byte(`{"type":"item","streamId":"root","value":{"type":"snapshot","cursor":12,"records":[],"hasMore":false,"projections":{"values":{"subagentCatalog":[{"id":"native-child","createdAt":1000,"mode":"continuable","label":"The native child"}]}}}}`)
	require.ErrorContains(t, a.handleFrame(raw), "stream connection")
	assert.Empty(t, a.childCatalog)
	assert.Empty(t, a.children)
	assert.Equal(t, int64(-1), a.streams["root"].lastSeq)
	select {
	case <-a.streams["root"].ready:
		t.Fatal("the failed native child restoration published root readiness")
	default:
	}
}

func TestChildSnapshotExcludesItsInheritedParentPrefix(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.streams["child"] = &sessionStream{address: sessionAddress{Kind: "subagent", ParentSessionID: "native-root", ChildSessionID: "native-child", Mode: "one-shot"}, sessionID: "native-child", parentSessionID: "native-root", childAgentID: "stored-child", lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
	raw := []byte(`{"type":"item","streamId":"child","value":{"type":"snapshot","cursor":5,"records":[{"event":{"type":"tool/call","seq":1,"time":1000,"data":{"turn":1,"callId":"parent-call","name":"bash","arguments":"{}"}}},{"event":{"type":"user/message","seq":2,"time":1000,"data":{"role":"user","content":[{"type":"text","text":"Inherited parent input."}]}}},{"event":{"type":"subagent/descriptor","seq":3,"time":1000,"data":{"version":0,"mode":"one-shot","label":"The native child"}}},{"event":{"type":"user/message","seq":4,"time":1001,"data":{"role":"user","content":[{"type":"text","text":"The exact child input."}]}}},{"event":{"type":"assistant/message","seq":5,"time":1002,"data":{"message":{"role":"assistant","content":[{"type":"text","text":"The exact child answer."}]}}}}],"hasMore":false,"projections":{"values":{"subagent":{"mode":"one-shot","label":"The native child","seq":3}}}}}`)
	require.NoError(t, a.handleFrame(raw))
	messages := sink.Child("stored-child").Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[0].Content), "The exact child input.")
	assert.Contains(t, string(messages[1].Content), "The exact child answer.")
	assert.Empty(t, a.streams["child"].pending)
}
