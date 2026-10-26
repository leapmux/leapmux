//go:build unix

package kiro

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKiroUsageRefreshReadsAfterStartupAndTurnsAndPreservesFailedReads(t *testing.T) {
	a, sink, requests, _ := startFakeKiroWith(t, "auto", []string{kiroFakeUsageReplies + `=[{"success":true,"data":{"planName":"Plan","usageBreakdowns":[{"resourceType":"CREDIT","used":25.5,"limit":100,"hasLimit":true}]}},{"success":false,"message":"Unavailable"},{"success":true,"data":{"usageBreakdowns":"invalid"}},{"success":true,"data":{"usageBreakdowns":[]}}]`}, agent.Options{})
	quotaReads := func() int {
		count := 0
		for _, request := range requests() {
			if request.Method == kiroAccountUsageMethod {
				assert.Empty(t, request.Params, "account usage is process-wide")
				count++
			}
		}
		return count
	}
	require.Eventually(t, func() bool { return len(sink.SessionInfoValues(contracts.SessionInfoKeyRateLimits)) == 1 }, 30*time.Second, time.Millisecond)
	first := sink.SessionInfoValues(contracts.SessionInfoKeyRateLimits)[0]
	for index := range 3 {
		require.NoError(t, a.SendInput("Read the next mocked account quota.", nil))
		require.Eventually(t, func() bool { return !a.PromptActive() && quotaReads() >= index+2 }, 30*time.Second, time.Millisecond)
		// The barrier follows the quota request in the native reader, so its
		// response proves that the fake already sent the quota response.
		_, err := a.SendRequest("leapmux/test/release_usage", json.RawMessage(`{}`), a.APITimeout())
		require.NoError(t, err)
		if index < 2 {
			assert.Equal(t, []any{first}, sink.SessionInfoValues(contracts.SessionInfoKeyRateLimits))
		}
	}
	require.Eventually(t, func() bool { return len(sink.SessionInfoValues(contracts.SessionInfoKeyRateLimits)) == 2 }, 30*time.Second, time.Millisecond)
	assert.Equal(t, map[string]any{"mode": "replace", "values": map[string]any{}}, sink.SessionInfoValues(contracts.SessionInfoKeyRateLimits)[1])
}

func TestKiroUsageRefreshCoalescesAndJoinsAReadThatTheProcessNeverAnswers(t *testing.T) {
	a, sink, requests, _ := startFakeKiroWith(t, "auto", []string{kiroFakeHoldUsage + "=1"}, agent.Options{})
	reads := func() int {
		count := 0
		for _, request := range requests() {
			if request.Method == kiroAccountUsageMethod {
				count++
			}
		}
		return count
	}
	require.Eventually(t, func() bool { return reads() == 1 }, 30*time.Second, time.Millisecond)
	for range 8 {
		a.requestUsageRefresh()
	}
	_, err := a.SendRequest("leapmux/test/release_usage", json.RawMessage(`{}`), a.APITimeout())
	require.NoError(t, err)
	require.Eventually(t, func() bool { return reads() == 2 }, 30*time.Second, time.Millisecond)
	a.Stop()
	_ = a.Wait()
	assert.Equal(t, 2, reads(), "requests during the first read schedule only one additional read")
	select {
	case <-a.usageDone:
	default:
		t.Fatal("Wait returned before the account-usage reader ended")
	}
	assert.Len(t, sink.SessionInfoValues(contracts.SessionInfoKeyRateLimits), 1)
}
