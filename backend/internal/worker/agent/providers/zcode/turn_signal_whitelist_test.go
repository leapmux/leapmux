package zcode

import (
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTurnSignalWhitelist_ZCode(t *testing.T) {
	t.Parallel()

	// ZCode is the one provider whose whole event enumeration is a contract this
	// repository owns, so the table is checked for completeness against it (see
	// the test underneath). Adding an event to the contract without an entry
	// there fails that test.
	var seq atomic.Int64
	agenttest.AssertTurnFrames(t, zcodeTurnFrameCases(), func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		sink := &agenttest.Sink{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), &zcodeRecordedStdin{})
		// Each case gets its own agent, so the seq only has to rise within one.
		a.HandleOutput(zcodeEventLine(t, seq.Add(1), tc.Name, tc.Line))
		return sink.TurnActives()
	})
}

// zcodeTurnFrameCases keys each case by the event type, which is what the
// completeness test compares against the contract.
func zcodeTurnFrameCases() []agenttest.TurnFrameCase {
	return []agenttest.TurnFrameCase{
		{Name: contracts.ZCodeEventTurnStarted, Line: `{"turnNumber":1,"input":"hi"}`, Moves: true},
		{Name: contracts.ZCodeEventTurnCompleted, Line: `{"toolCallCount":1}`, Moves: true},
		{Name: contracts.ZCodeEventTurnFailed, Line: `{"error":{"message":"boom","retryable":false}}`, Moves: true},

		{Name: contracts.ZCodeEventTurnSteerQueued, Line: `{"inputId":"i1"}`},
		{Name: contracts.ZCodeEventTurnSteerDrained, Line: `{"inputId":"i1"}`},
		{Name: contracts.ZCodeEventSessionCreated, Line: `{"sessionId":"sess-1"}`},
		{Name: contracts.ZCodeEventSessionResumed, Line: `{"sessionId":"sess-1"}`},
		{Name: contracts.ZCodeEventSessionUpdated, Line: `{"sessionId":"sess-1"}`},
		{Name: contracts.ZCodeEventSessionTitleUpdated, Line: `{"title":"a title"}`},
		{Name: contracts.ZCodeEventSessionClosed, Line: `{"sessionId":"sess-1"}`},
		{Name: contracts.ZCodeEventMessageUpserted, Line: `{"message":{"id":"m1","role":"assistant"}}`},
		{Name: contracts.ZCodeEventMessageRemoved, Line: `{"messageId":"m1"}`},
		{Name: contracts.ZCodeEventPartStarted, Line: `{"part":{"id":"p1","type":"text"}}`},
		{Name: contracts.ZCodeEventPartDelta, Line: `{"partId":"p1","delta":"hi"}`},
		{Name: contracts.ZCodeEventPartUpserted, Line: `{"part":{"id":"p1","type":"text","text":"hi"}}`},
		{Name: contracts.ZCodeEventPartRemoved, Line: `{"partId":"p1"}`},
		{Name: contracts.ZCodeEventModelStreaming, Line: `{"streaming":true}`},
		{Name: contracts.ZCodeEventToolUpdated, Line: `{"kind":"progress","toolCallId":"t1"}`},
		{Name: contracts.ZCodeEventPermissionRequested, Line: `{"requestId":"r1","toolCallId":"t1"}`},
		{Name: contracts.ZCodeEventPermissionResolved, Line: `{"requestId":"r1"}`},
		{Name: contracts.ZCodeEventUserInputRequested, Line: `{"requestId":"r1"}`},
		{Name: contracts.ZCodeEventUserInputResolved, Line: `{"requestId":"r1"}`},
		{Name: contracts.ZCodeEventCheckpointCreated, Line: `{"checkpointId":"c1"}`},
		{Name: contracts.ZCodeEventRewindTriggered, Line: `{"checkpointId":"c1"}`},
		{Name: contracts.ZCodeEventStreamRecoveryUpdated, Line: `{"state":"recovering"}`},

		{Name: "turn.futureSignal", Line: `{"turnNumber":2}`},
	}
}

func TestTurnSignalWhitelist_ZCodeTableCoversTheContract(t *testing.T) {
	t.Parallel()

	// contracts/zcode-protocol.json is the vendor vocabulary this repository
	// tracks. An event listed there is one the app-server can send, so the table
	// must say whether it moves the turn flag -- and a new one arrives as a
	// failure here rather than as an unclassified frame in production.
	raw, err := os.ReadFile(testutil.RepoPath(t, "contracts", "zcode-protocol.json"))
	require.NoError(t, err)
	var contract struct {
		Events map[string]string `json:"events"`
	}
	require.NoError(t, json.Unmarshal(raw, &contract))
	require.NotEmpty(t, contract.Events, "the contract lists the event vocabulary")

	covered := make(map[string]struct{}, len(zcodeTurnFrameCases()))
	for _, tc := range zcodeTurnFrameCases() {
		covered[tc.Name] = struct{}{}
	}
	for name, eventType := range contract.Events {
		assert.Contains(t, covered, eventType,
			"contract event %s (%s) has no turn-signal case", name, eventType)
	}
}
