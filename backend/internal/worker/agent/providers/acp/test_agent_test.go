package acp

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

// testAgent is an ACP agent that no provider owns: the bare base, on the
// permission-mode channel, with no provider hook. A test whose subject is the
// base drives it, so the test depends on no provider's hooks and needs no
// provider's package.
type testAgent struct{ Base }

// The ids that the peer of the test agent offers: two permission modes, and a reasoning
// axis under a convention id rather than the well-known `effort` id. That axis
// is the case that the config-option paths exist for.
const (
	testModeAuto       = "auto"
	testModeApprove    = "approve"
	testThinkingEffort = "thinking_effort"
)

func newTestAgent() *testAgent {
	a := &testAgent{}
	a.hooks.ModeChannel = ModeChannelPermissionMode
	return a
}

// newTestAgentForRPC returns the test agent attached to a fake peer that answers
// `{}` to every request, and a function that returns the requests it recorded.
func newTestAgentForRPC(t *testing.T) (*testAgent, func() []agenttest.RecordedRequest) {
	return acptest.NewAgentForRPC(t, newTestAgent, func(a *testAgent) *Base { return &a.Base })
}

// newTestAgentForRPCWithResponder is newTestAgentForRPC with a peer that
// answers each request with what respond returns for its method.
func newTestAgentForRPCWithResponder(t *testing.T, respond func(method string) agenttest.RPCReply) (*testAgent, func() []agenttest.RecordedRequest) {
	return acptest.NewAgentForRPCWithResponder(t, newTestAgent, func(a *testAgent) *Base { return &a.Base }, respond)
}
