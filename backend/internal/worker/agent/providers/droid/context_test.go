package droid

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func newDroidContextTestAgent(t *testing.T) (*Agent, *agenttest.Sink, *droidRequestWriter) {
	t.Helper()
	writer := &droidRequestWriter{requests: make(chan droidEnvelope, 2)}
	sink := &agenttest.Sink{}
	processDone := make(chan struct{})
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "droid-context-test", Stdin: writer, Ctx: context.Background(), ProcessDone: processDone,
		}),
		sink: agent.NewProviderServices(sink), sessionID: "native-session",
	}
	t.Cleanup(func() {
		close(processDone)
		a.Process.Stop()
	})
	return a, sink, writer
}

func TestTurnEndReadsNativeContextStats(t *testing.T) {
	t.Parallel()
	a, sink, writer := newDroidContextTestAgent(t)
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()

	notification := newDroidEnvelope(droidTypeNotification)
	notification.Method = droidMethodSessionNotif
	var err error
	notification.Params, err = json.Marshal(map[string]any{
		"sessionId":    "native-session",
		"notification": map[string]any{"type": contracts.DroidNotificationAgentTurnCompleted},
	})
	require.NoError(t, err)
	line, err := notification.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)

	var request droidEnvelope
	select {
	case request = <-writer.requests:
	case <-deadline.C:
		t.Fatal("Droid did not ask for native context stats after the turn")
	}
	assert.Equal(t, droidMethodGetContextStats, request.Method)
	assert.JSONEq(t, `{}`, string(request.Params))
	reply := newDroidEnvelope(droidTypeResponse)
	reply.ID = request.ID
	reply.Result = json.RawMessage(`{"used":200,"remaining":800,"limit":1000,"accuracy":"estimated"}`)
	line, err = reply.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
	require.Eventually(t, func() bool {
		_, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
		return ok
	}, 30*time.Second, 10*time.Millisecond)
	value, _ := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	assert.Equal(t, map[string]any{
		contracts.ContextUsageFieldContextTokens: int64(200),
		contracts.ContextUsageFieldContextWindow: int64(1000),
		contracts.ContextUsageFieldUsagePercent:  float64(20),
	}, value)
}

func TestContextRefreshRepeatsAfterAnOverlappingTurn(t *testing.T) {
	t.Parallel()
	a, sink, writer := newDroidContextTestAgent(t)
	a.refreshContextUsage()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	var first droidEnvelope
	select {
	case first = <-writer.requests:
	case <-deadline.C:
		t.Fatal("the first native context request did not arrive")
	}
	assert.Equal(t, droidMethodGetContextStats, first.Method)

	// A second completed turn needs a fresh report even while the first reply waits.
	a.refreshContextUsage()
	firstReply := newDroidEnvelope(droidTypeResponse)
	firstReply.ID = first.ID
	firstReply.Result = json.RawMessage(`{"used":100,"remaining":900,"limit":1000}`)
	line, err := firstReply.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)

	var second droidEnvelope
	select {
	case second = <-writer.requests:
	case <-deadline.C:
		t.Fatal("Droid dropped the context refresh for the overlapping turn")
	}
	assert.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, droidMethodGetContextStats, second.Method)
	secondReply := newDroidEnvelope(droidTypeResponse)
	secondReply.ID = second.ID
	secondReply.Result = json.RawMessage(`{"used":200,"remaining":800,"limit":1000}`)
	line, err = secondReply.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
	require.Eventually(t, func() bool {
		value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
		if !ok {
			return false
		}
		usage, ok := value.(map[string]any)
		return ok && usage[contracts.ContextUsageFieldContextTokens] == int64(200)
	}, 30*time.Second, 10*time.Millisecond)
}

func TestContextStatsRequireAValidNativeWindow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		result  string
		used    int64
		window  int64
		percent float64
		valid   bool
	}{
		{name: "zero used", result: `{"used":0,"remaining":1000,"limit":1000}`, used: 0, window: 1000, percent: 0, valid: true},
		{name: "overfull window", result: `{"used":2000,"remaining":0,"limit":1000}`, used: 2000, window: 1000, percent: 100, valid: true},
		{name: "missing used", result: `{"limit":1000}`},
		{name: "missing limit", result: `{"used":20}`},
		{name: "zero limit", result: `{"used":20,"limit":0}`},
		{name: "negative used", result: `{"used":-1,"limit":1000}`},
		{name: "negative remaining", result: `{"used":20,"remaining":-1,"limit":1000}`},
		{name: "invalid field", result: `{"used":"many","limit":1000}`},
		{name: "malformed result", result: `{bad`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := &Agent{sink: agent.NewProviderServices(sink)}
			a.applyContextStats(json.RawMessage(tc.result))
			value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
			assert.Equal(t, tc.valid, ok)
			if tc.valid {
				assert.Equal(t, map[string]any{
					contracts.ContextUsageFieldContextTokens: tc.used,
					contracts.ContextUsageFieldContextWindow: tc.window,
					contracts.ContextUsageFieldUsagePercent:  tc.percent,
				}, value)
			}
		})
	}
}
