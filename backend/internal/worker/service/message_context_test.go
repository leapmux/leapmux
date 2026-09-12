package service

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/require"
)

func TestToolRequestLookupSeparatesProviderSessions(t *testing.T) {
	t.Parallel()
	sink, _ := newGitStatusFixture(t)
	span := agent.SpanInfo{SpanID: "reused-tool", SpanType: "Read"}
	oldRequest := []byte(`{"sessionUpdate":"tool_call","toolCallId":"reused-tool","kind":"read","rawInput":{"path":"old.txt"}}`)
	newRequest := []byte(`{"sessionUpdate":"tool_call","toolCallId":"reused-tool","kind":"read","rawInput":{"path":"new.txt"}}`)
	sink.UpdateSessionID("old-session")
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: oldRequest}, span))
	sink.UpdateSessionID("new-session")
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: newRequest}, span))
	request, err := sink.ReadToolRequest(span.SpanID)
	require.NoError(t, err)
	require.NotNil(t, request)
	require.Equal(t, newRequest, request.Content.Original)
	require.Equal(t, "new-session", request.Content.AgentSessionID)
	late := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"reused-tool","status":"completed","rawOutput":"Late old output"}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: late, AgentSessionID: "old-session"}, span))
	request, err = sink.ReadToolRequest(span.SpanID)
	require.NoError(t, err)
	require.Equal(t, newRequest, request.Content.Original)
	reloaded := sink.h.NewSink(sink.agentID, sink.agentProvider)
	request, err = reloaded.ReadToolRequest(span.SpanID)
	require.NoError(t, err)
	require.Equal(t, newRequest, request.Content.Original)
}
