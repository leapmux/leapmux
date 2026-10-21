package acp

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// An unknown notification of the current session persists byte for byte; one
// of another session reaches nothing. The base serves this rule for every ACP
// provider that claims no ExtraMethod of its own.
func TestACPUnknownFramePersists(t *testing.T) {
	t.Parallel()
	line := func(t *testing.T, sessionID string) []byte {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": "future/method",
			"params": map[string]any{"sessionId": sessionID, "value": 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "current session", Frame: line(t, "session-1")},
		{Name: "other session", Frame: line(t, "session-other"), OtherSession: true},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		sink := &agenttest.Sink{}
		b := &Base{sink: agent.NewProviderServices(sink), sessionID: "session-1"}
		b.HandleOutput(tc.Frame)
		return sink
	})
}
