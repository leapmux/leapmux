package commandcode

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nativeCompactionEndpoint(t *testing.T, a *Agent, outcome string) *providerkit.HTTPEndpoint {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/compact", r.URL.Path)
		a.HandleOutput([]byte(`{"type":"event","event":{"type":"compaction_start","trigger":"manual"}}`))
		if outcome != "" {
			a.HandleOutput([]byte(fmt.Sprintf(`{"type":"event","event":{"type":"compaction_outcome","trigger":"manual","outcome":%q,"tokensBefore":1000,"tokensAfter":1000}}`, outcome)))
		}
		a.HandleOutput([]byte(`{"type":"event","event":{"type":"compaction_done","trigger":"manual","tokensSaved":0}}`))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"completed":true}`))
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	return endpoint
}

func TestFailedNativeManualCompactionDoesNotReportSuccess(t *testing.T) {
	a, sink := testAgent(t)
	a.bridge = nativeCompactionEndpoint(t, a, "failed")
	require.Error(t, a.CompactContext())
	active, known := sink.LastTurnActive()
	require.True(t, known)
	assert.False(t, active)
}

func TestNativeManualCompactionRequiresItsExactOutcome(t *testing.T) {
	for _, outcome := range []string{"summarized", "failed", "too-small", "", "unknown-native-outcome"} {
		t.Run(outcome, func(t *testing.T) {
			a, _ := testAgent(t)
			a.bridge = nativeCompactionEndpoint(t, a, outcome)
			err := a.CompactContext()
			if outcome == "summarized" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			assert.False(t, a.PublishTurnActive().Active)
			assert.Nil(t, a.manualCompaction)
		})
	}
}

func TestNativeManualCompactionBoundaryRequiresASuccessfulOutcome(t *testing.T) {
	p := commandcodeProvider{}
	assert.Empty(t, p.Classify([]byte(`{"type":"event","event":{"type":"compaction_done","trigger":"manual","tokensSaved":0}}`)).Kind)
	assert.Equal(t, agent.NotificationKindCompactionBoundary, p.Classify([]byte(`{"type":"event","event":{"type":"compaction_outcome","trigger":"manual","outcome":"summarized","tokensBefore":1000,"tokensAfter":1000}}`)).Kind)
	assert.Empty(t, p.Classify([]byte(`{"type":"event","event":{"type":"compaction_outcome","trigger":"manual","outcome":"failed"}}`)).Kind)
	assert.Equal(t, agent.NotificationKindCompactionBoundary, p.Classify([]byte(`{"type":"event","event":{"type":"compaction_done","tokensSaved":101}}`)).Kind)
}

func TestNativeCompactionProcessExitPublishesIdle(t *testing.T) {
	a, sink := testAgent(t)
	feedEvent(t, a, map[string]any{"type": "compaction_start", "trigger": "manual"})
	a.finishStream()
	assert.False(t, a.PublishTurnActive().Active)
	active, known := sink.LastTurnActive()
	require.True(t, known)
	assert.False(t, active)
}
