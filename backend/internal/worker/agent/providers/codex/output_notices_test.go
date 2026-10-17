package codex

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	agentapi "github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexBufferingUpdateReplacesTheCompleteOffer(t *testing.T) {
	t.Parallel()
	for _, faster := range []string{`null`, `""`, `"gpt-5.4"`} {
		t.Run(faster, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
			armRetryableTurn(t, a, sink)
			feedCodexLines(a, fmt.Sprintf(`{"method":"model/safetyBuffering/updated","params":{"threadId":"main-thread","turnId":"turn-1","showBufferingUi":true,"fasterModel":%s}}`, faster))
			offer, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyTurnRetryOffer)
			require.True(t, exists)
			assert.Nil(t, offer)
			_, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
			assert.ErrorIs(t, err, agentapi.ErrTurnRetryUnavailable)
		})
	}
}

func TestCodexStaleBufferingUpdateKeepsTheCurrentOffer(t *testing.T) {
	t.Parallel()
	for _, visible := range []bool{false, true} {
		t.Run(fmt.Sprint(visible), func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
			armRetryableTurn(t, a, sink)
			feedCodexLines(a, fmt.Sprintf(`{"method":"model/safetyBuffering/updated","params":{"threadId":"main-thread","turnId":"old-turn","showBufferingUi":%t}}`, visible))
			offer, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyTurnRetryOffer)
			require.True(t, exists)
			require.NotNil(t, offer)
			retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
			require.NoError(t, err)
			retry.Abandon(assert.AnError)
		})
	}
}

func TestCodexNoticeIdentitySeparatesEmbeddedNulls(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]string{
		{`{"method":"configWarning","params":{"summary":"a\u0000b","details":"c"}}`, `{"method":"configWarning","params":{"summary":"a","details":"b\u0000c"}}`},
		{`{"method":"model/verification","params":{"verifications":["a\u0000trustedAccessForCyber"]}}`, `{"method":"model/verification","params":{"verifications":["a","trustedAccessForCyber"]}}`},
	} {
		sink := &agenttest.Sink{}
		a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
		feedCodexLines(a, pair[0], pair[1], pair[0], pair[1])
		assert.Equal(t, 2, sink.NotificationCount(), "distinct notices remain distinct, while identical notices still deduplicate")
	}
}

func TestCodexAnswerReleasesInputThatCannotBeRetried(t *testing.T) {
	t.Parallel()
	a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	armRetryableTurn(t, a, sink)
	feedCodexLines(a, `{"method":"item/agentMessage/delta","params":{"threadId":"main-thread","itemId":"answer","delta":"Hello"}}`)
	a.Mu.Lock()
	retained := a.mainTurn.input
	a.Mu.Unlock()
	require.Empty(t, retained, "the answer permanently ends retry eligibility")
	require.NoError(t, a.SteerInput("another instruction", nil))
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Empty(t, a.mainTurn.input, "a later steer must not restore unusable retry input")
}
