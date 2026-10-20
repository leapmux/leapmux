package goose

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGooseCompactTurnStoresTheBoundaryRow pins the manual compaction flow:
// the live compaction runs while the /compact prompt runs, and the prompt's
// end stores the one boundary row (decision 21: no native frame exists).
func TestGooseCompactTurnStoresTheBoundaryRow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)
	a.Mu.Lock()
	a.compactPending = true
	a.Mu.Unlock()
	providerkit.PublishCompaction(a.Sink(), "manual")
	a.FinishPromptRequestForTest("s1", []byte(`{"stopReason":"end_turn","usage":{"totalTokens":2}}`), nil)
	ends := sink.LeapMuxNotifications()
	require.Len(t, ends, 1, "the compact turn stores one boundary row")
	assert.Equal(t, contracts.NotificationTypeCompactionEnded, ends[0][contracts.NotificationFieldType])
	assert.NotContains(t, ends[0], contracts.NotificationFieldOutcome)
	info := sink.SessionInfoValues(contracts.SessionInfoKeyCompaction)
	assert.Equal(t, []interface{}{map[string]interface{}{"trigger": "manual"}, nil}, info, "the live compaction withdraws at the end")
}
