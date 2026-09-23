package copilot

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/require"
)

// Copilot names two session events that move the turn flag: assistant.turn_start
// opens a turn, and session.idle ends it. Every other event must move nothing,
// and so must an event that a later runtime adds.
//
// The table is every event that contracts/copilot-protocol.json declares, one
// member of each event family that the contract states by prefix, and one event
// that does not exist yet. The contract lists every type that the installed
// runtime declares, so an event that the runtime adds reaches this table through
// the contract, and an unclassified event cannot move the flag in production.
func TestTurnSignalWhitelist_Copilot(t *testing.T) {
	t.Parallel()

	agenttest.AssertTurnFrames(t, copilotTurnFrameCases(t), func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		a, sink := newNativeCopilotForEvents(t)
		a.HandleOutput(nativeCopilotEvent(t, "", tc.Name, json.RawMessage(tc.Line)))
		return sink.TurnActives()
	})
}

// copilotTurnFrameCases keys each case by the event type. A case gives an empty
// data object: the turn flag must not depend on what the data holds.
func copilotTurnFrameCases(t *testing.T) []agenttest.TurnFrameCase {
	t.Helper()
	raw, err := os.ReadFile(testutil.RepoPath(t, "contracts", "copilot-protocol.json"))
	require.NoError(t, err)
	var contract struct {
		Events        map[string]string `json:"events"`
		EventPrefixes map[string]string `json:"eventPrefixes"`
	}
	require.NoError(t, json.Unmarshal(raw, &contract))
	require.NotEmpty(t, contract.Events, "the sweep proves nothing against an empty contract")

	moves := map[string]bool{
		contracts.CopilotEventAssistantTurnStart: true,
		contracts.CopilotEventSessionIdle:        true,
	}
	var types []string
	for _, eventType := range contract.Events {
		types = append(types, eventType)
	}
	for _, prefix := range contract.EventPrefixes {
		types = append(types, prefix+"future_member")
	}
	sort.Strings(types)
	cases := make([]agenttest.TurnFrameCase, 0, len(types)+1)
	for _, eventType := range types {
		cases = append(cases, agenttest.TurnFrameCase{Name: eventType, Line: `{}`, Moves: moves[eventType]})
	}
	for eventType := range moves {
		require.Containsf(t, types, eventType, "the contract must declare the turn signal %q", eventType)
	}
	return append(cases, agenttest.TurnFrameCase{Name: "assistant.future_signal", Line: `{}`})
}
