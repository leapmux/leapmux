//go:build unix

package opencode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestACPConfigOption_StartupEnvEffortNotDoubledOntoCoincidentalAxis is the S1 regression: a daemon
// advertising BOTH a real "effort" axis and a coincidental, category-less "thinking_effort" axis
// must receive the env-effort override on "effort" ONLY. A live scan for any well-known effort id
// would mis-claim "thinking_effort" as the effort axis and double-push the override onto it. With
// provider declaration, OpenCode wires
// no effortConfigID (its axis IS "effort"), and the category-only fallback ignores the bare
// "thinking_effort", so the override lands on "effort" exactly once.
func TestACPConfigOption_StartupEnvEffortNotDoubledOntoCoincidentalAxis(t *testing.T) {
	ag, requests := newOpenCodeAgentForRPCWithRequestResponder(t, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	require.Empty(t, ag.HooksForTest().EffortConfigID, "OpenCode's effort axis IS \"effort\"; it declares no convention id")
	// The daemon advertised both axes at handshake (known), neither yet surfaced with a value.
	ag.Mu.Lock()
	ag.OptionsForTest().MarkKnownForTest(acp.ConfigOption{ID: agent.OptionIDEffort})
	ag.OptionsForTest().MarkKnownForTest(acp.ConfigOption{ID: "thinking_effort"})
	ag.Mu.Unlock()

	ag.ApplyStartupOptionsForTest(agent.Options{Options: map[string]string{agent.OptionIDEffort: "high"}})

	var writes []agenttest.RecordedRequest
	for _, r := range requests() {
		if r.Method == acp.MethodSessionSetConfigOption {
			writes = append(writes, r)
		}
	}
	require.Len(t, writes, 1, "the env-effort override is pushed once, not doubled onto the coincidental axis")
	assert.Equal(t, agent.OptionIDEffort, writes[0].Params["configId"])
	assert.Equal(t, "high", writes[0].Params["value"])
}
