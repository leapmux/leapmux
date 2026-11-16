package opencode

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeToolIdentityCannotCollideAtAComponentDelimiter(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	streams := newOpenCodeToolStreams("ses_1", agent.NewProviderServices(sink))
	firstID, secondID := "c", "b:c"
	first, err := streams.tool("msg_a:b", &firstID)
	require.NoError(t, err)
	second, err := streams.tool("msg_a", &secondID)
	require.NoError(t, err)
	assert.NotEqual(t, first.spanID, second.spanID)
	firstContent, err := streams.content(first, []byte(`{}`), "request", "")
	require.NoError(t, err)
	secondContent, err := streams.content(second, []byte(`{}`), "request", "")
	require.NoError(t, err)
	assert.NotEqual(t, firstContent.IdempotencyKey, secondContent.IdempotencyKey)
}

func TestOpenCodeToolSpanIDPreservesEmptyAndOpaqueComponents(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "opencode:tool:bQ.Yw", openCodeToolSpanID("m", "c"))
	assert.Equal(t, "opencode:tool:bQ.", openCodeToolSpanID("m", ""))
	assert.NotEqual(t, openCodeToolSpanID("m", "工具"), openCodeToolSpanID("m", "工具:"))
}

func TestOpenCodeToolStreamRetainsNativeOpeningAndResult(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	tools := newOpenCodeToolStreams("ses_1", agent.NewProviderServices(sink))
	start := openCodeSessionEventForTest(t, "tool.input.started", `"assistantMessageID":"msg_1","id":"call_1","name":"shell"`)
	called := openCodeSessionEventForTest(t, "tool.called", `"assistantMessageID":"msg_1","id":"call_1","input":{"command":"echo text"},"executed":false`)
	done := openCodeSessionEventForTest(t, "tool.success", `"assistantMessageID":"msg_1","id":"call_1","content":[{"type":"text","text":"text"}],"executed":false`)
	for _, event := range []openCodeEvent{start, called, done, done} {
		accepted, err := tools.observe(event)
		require.True(t, accepted)
		require.NoError(t, err)
	}
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, string(called.Raw), string(rows[0].Content))
	assert.Equal(t, string(done.Raw), string(rows[1].Content))
	assert.False(t, rows[0].Closing)
	assert.True(t, rows[1].Closing)
	assert.Equal(t, agent.MessageCompletionComplete, rows[1].Completion)
	assert.Contains(t, string(rows[1].SupplementalContent), "session.tool.input.started")
	assert.Contains(t, string(rows[1].SupplementalContent), "session.tool.called")
	assert.Equal(t, 1, tools.count)
}

func TestOpenCodeToolProgressEnrichesTheRetainedOpening(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	tools := newOpenCodeToolStreams("ses_1", agent.NewProviderServices(sink))
	_, err := tools.observe(openCodeSessionEventForTest(t, "tool.input.started", `"assistantMessageID":"msg_1","id":"call_1","name":"shell"`))
	require.NoError(t, err)
	called := openCodeSessionEventForTest(t, "tool.called", `"assistantMessageID":"msg_1","id":"call_1","input":{"command":"printf output"},"executed":false`)
	_, err = tools.observe(called)
	require.NoError(t, err)
	progress := openCodeSessionEventForTest(t, "tool.progress", `"assistantMessageID":"msg_1","id":"call_1","metadata":{"output":"partial output","exit":0}`)
	_, err = tools.observe(progress)
	require.NoError(t, err)
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, string(called.Raw), string(rows[0].Content))
	assert.Contains(t, string(rows[0].SupplementalContent), "partial output")
	assert.Positive(t, rows[0].SupplementalRevision)
}

func TestOpenCodeToolRecoveryAndLiveResultDeduplicate(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	tools := newOpenCodeToolStreams("ses_1", agent.NewProviderServices(sink))
	message := storedAssistant(t, `{"id":"msg_1","type":"assistant","content":[{"type":"tool","id":"call_1","name":"read","state":{"status":"completed","input":{"path":"a.txt"},"content":[{"type":"text","text":"file body"}]}}],"time":{"created":1,"completed":2}}`)
	require.NoError(t, tools.recover(message))
	require.NoError(t, tools.recover(message))
	_, err := tools.observe(openCodeSessionEventForTest(t, "tool.success", `"assistantMessageID":"msg_1","id":"call_1","content":[{"type":"text","text":"file body"}],"executed":false`))
	require.NoError(t, err)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Contains(t, string(rows[1].Content), "file body")
	assert.Equal(t, 1, tools.count)
}

func TestOpenCodeToolStreamPreservesFailedWriteAndClosesOnce(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	tools := newOpenCodeToolStreams("ses_1", agent.NewProviderServices(sink))
	_, err := tools.observe(openCodeSessionEventForTest(t, "tool.input.started", `"assistantMessageID":"msg_1","id":"call_1","name":"shell"`))
	require.NoError(t, err)
	_, err = tools.observe(openCodeSessionEventForTest(t, "tool.called", `"assistantMessageID":"msg_1","id":"call_1","input":{"command":"echo text"},"executed":false`))
	require.NoError(t, err)
	sink.PersistErr = assert.AnError
	done := openCodeSessionEventForTest(t, "tool.failed", `"assistantMessageID":"msg_1","id":"call_1","error":{"type":"unknown","message":"failed"},"executed":false`)
	_, err = tools.observe(done)
	require.ErrorIs(t, err, assert.AnError)
	sink.PersistErr = nil
	_, err = tools.observe(done)
	require.NoError(t, err)
	rows := sink.Messages()
	require.Len(t, rows, 3)
	assert.Equal(t, string(rows[1].Content), string(rows[2].Content))
	assert.Equal(t, 1, tools.count)
}

func TestOpenCodeToolStreamNativeLaunchHasNoSpanAndKeepsMetadata(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	tools := newOpenCodeToolStreams("ses_1", agent.NewProviderServices(sink))
	_, err := tools.observe(openCodeSessionEventForTest(t, "tool.input.started", `"assistantMessageID":"msg_1","id":"call_1","name":"subagent"`))
	require.NoError(t, err)
	_, err = tools.observe(openCodeSessionEventForTest(t, "tool.called", `"assistantMessageID":"msg_1","id":"call_1","input":{"agent":"general","prompt":"task","description":"Child"},"executed":false`))
	require.NoError(t, err)
	_, err = tools.observe(openCodeSessionEventForTest(t, "tool.success", `"assistantMessageID":"msg_1","id":"call_1","content":[{"type":"text","text":"started"}],"metadata":{"sessionID":"ses_child","status":"running"},"executed":false`))
	require.NoError(t, err)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.True(t, rows[0].NoSpan)
	assert.True(t, rows[1].NoSpan)
	assert.Contains(t, string(rows[1].Content), "ses_child")
}

func TestOpenCodeToolStreamInterruptedCallKeepsItsLastNativeRecord(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	tools := newOpenCodeToolStreams("ses_1", agent.NewProviderServices(sink))
	_, err := tools.observe(openCodeSessionEventForTest(t, "tool.input.started", `"assistantMessageID":"msg_1","id":"call_1","name":"shell"`))
	require.NoError(t, err)
	called := openCodeSessionEventForTest(t, "tool.called", `"assistantMessageID":"msg_1","id":"call_1","input":{"command":"held"},"executed":false`)
	_, err = tools.observe(called)
	require.NoError(t, err)
	require.NoError(t, tools.finish(agent.MessageCompletionInterrupted))
	require.NoError(t, tools.finish(agent.MessageCompletionInterrupted))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, string(called.Raw), string(rows[1].Content))
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
}

func TestOpenCodeToolStopPreservesFirstSeenOrder(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	tools := newOpenCodeToolStreams("ses_1", agent.NewProviderServices(sink))
	const count = 32
	for i := range count {
		identity := fmt.Sprintf(`"assistantMessageID":"msg_1","id":"call_%02d"`, i)
		_, err := tools.observe(openCodeSessionEventForTest(t, "tool.input.started", identity+`,"name":"shell"`))
		require.NoError(t, err)
		_, err = tools.observe(openCodeSessionEventForTest(t, "tool.called", identity+`,"input":{"command":"held"},"executed":false`))
		require.NoError(t, err)
	}
	require.NoError(t, tools.finish(agent.MessageCompletionInterrupted))
	rows := sink.Messages()
	require.Len(t, rows, 2*count)
	for i := range count {
		assert.Equal(t, openCodeToolSpanID("msg_1", fmt.Sprintf("call_%02d", i)), rows[count+i].SpanID)
		assert.True(t, rows[count+i].Closing)
	}
}
