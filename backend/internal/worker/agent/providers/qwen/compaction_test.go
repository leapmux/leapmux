package qwen

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This error is the native reply in qwen/logs/r2-compress-1.log.
const qwenTestCompressRefusal = `{"code":-32603,"message":"Internal error","data":{"details":"Compression was not beneficial for this history size."}}`

type qwenCompactionFailureCase struct {
	manual       bool
	tryWhileBusy bool
	stopped      bool
	reply        json.RawMessage
}

func runQwenCompactionFailure(t *testing.T, tc qwenCompactionFailureCase) *agenttest.ControlSink {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	a, sink, _ := newQwenAgent(t, nil, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == acp.MethodSessionPrompt {
			<-release
			return agenttest.RPCReply{Error: tc.reply}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	if tc.manual {
		require.NoError(t, a.CompactContext())
	} else {
		require.NoError(t, a.SendInput("ordinary prompt", nil))
	}
	if tc.tryWhileBusy {
		require.ErrorIs(t, a.CompactContext(), agent.ErrAgentBusy)
	}
	a.HandleOutput(metaChunk(t, "Compressing context...", map[string]any{"contextCompression": map[string]any{"phase": "progress"}}))
	if tc.stopped {
		a.SetStoppedForTest(true)
	}
	once.Do(func() { close(release) })
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })
	live, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	require.True(t, ok)
	assert.Nil(t, live, "every failed or stopped compaction ends its live state")
	return sink
}

func TestQwenCompressRefusalIsAStatus(t *testing.T) {
	t.Parallel()
	for _, busy := range []bool{false, true} {
		sink := runQwenCompactionFailure(t, qwenCompactionFailureCase{manual: true, tryWhileBusy: busy, reply: json.RawMessage(qwenTestCompressRefusal)})
		require.Equal(t, []map[string]any{{
			"type": contracts.NotificationTypeAgentStatus,
			"text": "Qwen Code did not compact the context: the compression was not beneficial for this history size.",
		}}, sink.Notifications())
	}
}

func TestQwenOtherCompressErrorStillFails(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{
		`{"code":-32603,"message":"Internal error","data":{"details":"Another failure."}}`,
		`{"code":-32000,"message":"Internal error","data":{"details":"Compression was not beneficial for this history size."}}`,
	} {
		sink := runQwenCompactionFailure(t, qwenCompactionFailureCase{manual: true, reply: json.RawMessage(reply)})
		require.Len(t, sink.Notifications(), 1)
		assert.Equal(t, contracts.NotificationTypeAgentError, sink.Notifications()[0]["type"])
	}
}

func TestQwenCompressRefusalDoesNotChangeAnOrdinaryPrompt(t *testing.T) {
	t.Parallel()
	for _, busy := range []bool{false, true} {
		sink := runQwenCompactionFailure(t, qwenCompactionFailureCase{tryWhileBusy: busy, reply: json.RawMessage(qwenTestCompressRefusal)})
		require.Len(t, sink.Notifications(), 1)
		assert.Equal(t, contracts.NotificationTypeAgentError, sink.Notifications()[0]["type"])
	}
}

func TestQwenStoppedCompactionEndsLiveStateWithoutAFailureNote(t *testing.T) {
	t.Parallel()
	sink := runQwenCompactionFailure(t, qwenCompactionFailureCase{manual: true, stopped: true, reply: json.RawMessage(qwenTestCompressRefusal)})
	assert.Empty(t, sink.Notifications())
}

func TestQwenCompactionWriteFailureClearsItsPendingRequest(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	a.SetStdinForTest(agenttest.FailingStdin{})
	require.Error(t, a.CompactContext())
	assert.False(t, a.PromptActive())
	a.stateMu.Lock()
	pending := a.compressPending
	a.stateMu.Unlock()
	assert.Nil(t, pending)
}
