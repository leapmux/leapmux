package claude

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestElicitationCompletionRequiresTheServerAndID(t *testing.T) {
	t.Parallel()
	for _, server := range []string{`"gitlab"`, `""`, `null`, `7`, `{}`, `[]`} {
		t.Run(server, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a, stdin := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
			a.HandleOutput([]byte(`{"type":"control_request","request_id":"r1","request":{"subtype":"elicitation","mcp_server_name":"github","message":"Sign in","mode":"url","elicitation_id":"e1"}}`))
			require.Len(t, sink.PublishedControls(), 1)

			a.HandleOutput([]byte(fmt.Sprintf(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":%s,"elicitation_id":"e1"}`, server)))
			require.Empty(t, stdin.String(), "another server or an invalid identity must not accept the request")
			require.Empty(t, sink.CanceledControls())
			a.HandleOutput([]byte(`{"type":"system","subtype":"elicitation_complete","elicitation_id":"e1"}`))
			require.Empty(t, stdin.String(), "an absent server must not accept the request")

			a.HandleOutput([]byte(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":"github","elicitation_id":"e1"}`))
			assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"action":"accept"}}}`, stdin.String())
			assert.Equal(t, []string{"r1"}, sink.CanceledControls())
		})
	}
}

func TestElicitationCompletionSeparatesServersWithTheSameID(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a, stdin := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	for _, server := range []string{"github", "gitlab"} {
		a.HandleOutput([]byte(fmt.Sprintf(`{"type":"control_request","request_id":%q,"request":{"subtype":"elicitation","mcp_server_name":%q,"message":"Sign in","mode":"url","elicitation_id":"shared"}}`, server, server)))
	}
	require.Len(t, sink.PublishedControls(), 2)
	for _, server := range []string{"gitlab", "github"} {
		stdin.Reset()
		a.HandleOutput([]byte(fmt.Sprintf(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":%q,"elicitation_id":"shared"}`, server)))
		assert.JSONEq(t, fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q,"response":{"action":"accept"}}}`, server), stdin.String())
	}
	assert.Equal(t, []string{"gitlab", "github"}, sink.CanceledControls())
}

func TestElicitationWithoutAServerStillPublishesButCannotCompleteAutomatically(t *testing.T) {
	t.Parallel()
	for _, server := range []string{`""`, `null`, `7`, `{}`, `[]`} {
		t.Run(server, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a, stdin := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
			a.HandleOutput([]byte(fmt.Sprintf(`{"type":"control_request","request_id":"r1","request":{"subtype":"elicitation","mcp_server_name":%s,"message":"Sign in","elicitation_id":"e1"}}`, server)))
			require.Len(t, sink.PublishedControls(), 1, "a malformed optional field must not hide the control")
			a.HandleOutput([]byte(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":"github","elicitation_id":"e1"}`))
			assert.Empty(t, stdin.String())
			assert.Empty(t, sink.CanceledControls())
		})
	}
}
