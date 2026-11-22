package antigravity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAntigravityQuotaPublishesNativeGroupsAndWindows(t *testing.T) {
	a, sink := testAgent(t)
	var mu sync.Mutex
	var requests []string
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case languageServerService + "GetCascadeModelConfigData":
			_, _ = w.Write([]byte(`{"clientModelConfigs":[
                {"modelId":"gemini-4-neon-high","modelOrAlias":{"model":"NEONH"},"quotaInfo":{"remainingFraction":0.8719}},
                {"modelId":"gemini-4-neon-low","modelOrAlias":{"model":"NEONL"},"quotaInfo":{"remainingFraction":0.8719}},
                {"modelId":"claude-opus-5.5-high","modelOrAlias":{"model":"OPUS"},"quotaInfo":{"remainingFraction":1}}
            ]}`))
		case languageServerService + "RetrieveUserQuotaSummary":
			var request json.RawMessage
			if assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
				assert.JSONEq(t, `{"request":{}}`, string(request))
			}
			_, _ = w.Write([]byte(`{"response":{"groups":[
                {"displayName":"Gemini Models","buckets":[
                    {"bucketId":"gemini-weekly","displayName":"Weekly Limit Remaining","window":"weekly","remainingFraction":0.8719,"resetTime":"2026-11-28T12:01:30Z"},
                    {"bucketId":"gemini-5h","displayName":"Five Hour Limit Remaining","window":"5h","remainingFraction":0.25}
                ]},
                {"displayName":"Claude and GPT models","buckets":[
                    {"bucketId":"3p-weekly","displayName":"Weekly Limit Remaining","window":"weekly","remainingFraction":1},
                    {"bucketId":"3p-5h","displayName":"Five Hour Limit Remaining","window":"5h","remainingFraction":0}
                ]}
            ],"buckets":[
                {"bucketId":"gemini-weekly","remainingFraction":0},
                {"bucketId":"credits","displayName":"AI credits","remainingAmount":"42"}
            ]}}`))
		default:
			http.NotFound(w, r)
		}
	})
	a.refreshAccountQuota()
	a.refreshAccountCatalog()
	mu.Lock()
	assert.ElementsMatch(t, []string{
		languageServerService + "GetCascadeModelConfigData",
		languageServerService + "RetrieveUserQuotaSummary",
	}, requests)
	mu.Unlock()
	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRateLimits)
	require.True(t, ok)
	update := value.(map[string]any)
	assert.Equal(t, contracts.RateLimitUpdateModeReplace, update[contracts.RateLimitUpdateFieldMode])
	values := update[contracts.RateLimitUpdateFieldValues].(map[string]any)
	require.Len(t, values, 4)
	weekly, ok := values["gemini-weekly"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Gemini Models · Weekly", weekly[contracts.RateLimitFieldLabel])
	assert.Equal(t, contracts.RateLimitStatusAllowed, weekly[contracts.RateLimitFieldStatus])
	assert.InDelta(t, 0.1281, weekly[contracts.RateLimitFieldUtilization], 1e-9)
	assert.Equal(t, time.Date(2026, 11, 28, 12, 1, 30, 0, time.UTC).Unix(), weekly[contracts.RateLimitFieldResetsAt])
	assert.Equal(t, map[string]any{
		contracts.RateLimitFieldLabel:       "Gemini Models · 5-hour",
		contracts.RateLimitFieldStatus:      contracts.RateLimitStatusAllowed,
		contracts.RateLimitFieldUtilization: 0.75,
	}, values["gemini-5h"])
	assert.Equal(t, map[string]any{
		contracts.RateLimitFieldLabel:       "Claude and GPT models · Weekly",
		contracts.RateLimitFieldStatus:      contracts.RateLimitStatusAllowed,
		contracts.RateLimitFieldUtilization: float64(0),
	}, values["3p-weekly"])
	assert.Equal(t, map[string]any{
		contracts.RateLimitFieldLabel:       "Claude and GPT models · 5-hour",
		contracts.RateLimitFieldStatus:      contracts.RateLimitStatusExceeded,
		contracts.RateLimitFieldUtilization: float64(1),
	}, values["3p-5h"])
}

func TestAntigravityQuotaFractionLimitsAndEmptyReplacement(t *testing.T) {
	for _, tc := range []struct {
		name, bucket string
		count        int
		utilization  float64
		status       string
	}{
		{"below zero", `{"bucketId":"pool","remainingFraction":-0.2}`, 1, 1, contracts.RateLimitStatusExceeded},
		{"above one", `{"bucketId":"pool","remainingFraction":1.2}`, 1, 0, contracts.RateLimitStatusAllowed},
		{"disabled", `{"bucketId":"pool","remainingFraction":0,"disabled":true}`, 0, 0, ""},
		{"no fraction", `{"bucketId":"pool","resetTime":"2026-11-22T12:00:00Z"}`, 0, 0, ""},
		{"no ID", `{"displayName":"Unnamed limit","remainingFraction":0.5}`, 0, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, sink := testAgent(t)
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != languageServerService+"RetrieveUserQuotaSummary" {
					_, _ = w.Write([]byte(`{}`))
					return
				}
				_, _ = fmt.Fprintf(w, `{"response":{"buckets":[%s]}}`, tc.bucket)
			})
			a.refreshAccountQuota()
			value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRateLimits)
			require.True(t, ok)
			values := value.(map[string]any)[contracts.RateLimitUpdateFieldValues].(map[string]any)
			require.Len(t, values, tc.count)
			if tc.count > 0 {
				pool := values["pool"].(map[string]any)
				assert.Equal(t, tc.utilization, pool[contracts.RateLimitFieldUtilization])
				assert.Equal(t, tc.status, pool[contracts.RateLimitFieldStatus])
			}

			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"response":{}}`)) })
			a.refreshAccountQuota()
			value, ok = sink.LastSessionInfoValue(contracts.SessionInfoKeyRateLimits)
			require.True(t, ok)
			assert.Empty(t, value.(map[string]any)[contracts.RateLimitUpdateFieldValues])
		})
	}
}

func TestAntigravityQuotaFailureKeepsLastSnapshotAndRefreshesCatalog(t *testing.T) {
	a, sink := testAgent(t)
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == languageServerService+"RetrieveUserQuotaSummary" {
			_, _ = w.Write([]byte(`{"response":{"buckets":[{"bucketId":"gemini-weekly","remainingFraction":0.8}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	a.refreshAccountQuota()
	before, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRateLimits)
	require.True(t, ok)

	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == languageServerService+"RetrieveUserQuotaSummary" {
			http.Error(w, "The quota service is unavailable.", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"clientModelConfigs":[{"modelId":"gemini-updated","modelOrAlias":{"model":"UPDATED"}}]}`))
	})
	a.refreshAccountQuota()
	a.refreshAccountCatalog()
	after, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRateLimits)
	require.True(t, ok)
	assert.Equal(t, before, after)
	assert.Contains(t, a.catalog.variants, "gemini-updated")
}

func TestAntigravityKeepsQuotaWhenResponseIsMissing(t *testing.T) {
	a, sink := testAgent(t)
	for _, body := range []string{`{"response":{"buckets":[{"bucketId":"pool","remainingFraction":0.8}]}}`, `{}`, `{"response":null}`, ""} {
		a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == languageServerService+"RetrieveUserQuotaSummary" {
				_, _ = w.Write([]byte(body))
			} else {
				_, _ = w.Write([]byte(`{}`))
			}
		})
		a.refreshAccountQuota()
		value, present := sink.LastSessionInfoValue(contracts.SessionInfoKeyRateLimits)
		require.True(t, present)
		assert.Contains(t, value.(map[string]any)[contracts.RateLimitUpdateFieldValues], "pool", "reply %q", body)
	}
}

func TestAntigravityQuotaRefreshContinuesWhileCatalogIsHeld(t *testing.T) {
	a, _ := testAgent(t)
	catalog := make(chan struct{}, 2)
	quota := make(chan struct{}, 2)
	release, unblock := newTestGate(t)
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case languageServerService + "GetCascadeModelConfigData":
			catalog <- struct{}{}
			<-release
			_, _ = w.Write([]byte(`{}`))
		case languageServerService + "RetrieveUserQuotaSummary":
			quota <- struct{}{}
			_, _ = w.Write([]byte(`{"response":{}}`))
		default:
			http.NotFound(w, r)
		}
	})
	defer func() { unblock(); a.accountRefreshWork.CloseAndWait() }()
	a.beginTurn(a.root)
	require.NoError(t, a.endTurn(a.root, agent.MessageCompletionComplete))
	receiveTestValue(t, catalog)
	receiveTestValue(t, quota)
	a.beginTurn(a.root)
	require.NoError(t, a.endTurn(a.root, agent.MessageCompletionComplete))
	receiveTestValue(t, quota)
}
