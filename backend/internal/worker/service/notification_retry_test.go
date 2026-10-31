package service

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type retryResolutionProvider struct {
	agent.Provider
	armed   atomic.Bool
	reenter func()
}

func (p *retryResolutionProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	if p.armed.CompareAndSwap(true, false) {
		p.reenter()
	}
	return p.Provider.Classify(raw)
}

func TestRetryResolutionRevalidatesAfterProviderReentry(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"answer", "replace sink", "notice", "resolve"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			providerID := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
			provider := &retryResolutionProvider{Provider: testRegistry.Plugin(providerID)}
			svc, _, _ := setupTestService(t, withRegistry(registryWithPlugin(t, providerID, provider)))
			const agentID = "retry-reentry"
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID: agentID, WorkingDir: t.TempDir(), AgentProvider: providerID,
			}))
			sink := newTestRootOutputSink(t, svc.Output, agentID, providerID)
			persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, []byte(`{"type":"system","subtype":"api_retry","attempt":1}`))
			resolve := func() { sink.PersistLeapMuxNotification(map[string]interface{}{"type": "api_retry_resolved"}) }
			provider.reenter = func() {
				switch operation {
				case "answer":
					assert.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
						agent.MessageContent{Original: []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"answer"}]}}`)}, agent.SpanInfo{}))
				case "replace sink":
					newTestRootOutputSink(t, svc.Output, agentID, providerID)
				case "notice":
					sink.PersistLeapMuxNotification(map[string]interface{}{"type": "agent_status", "message": "separate notice"})
				case "resolve":
					resolve()
				}
			}
			provider.armed.Store(true)
			finished := make(chan struct{})
			go func() { resolve(); close(finished) }()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("the retry resolution held a lock during provider reentry")
			}
			rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
			require.NoError(t, err)
			index := 0
			switch operation {
			case "notice":
				require.Len(t, rows, 2)
				index = 1
			case "answer":
				require.Len(t, rows, 2)
			default:
				require.Len(t, rows, 1)
			}
			wrapper := decodeNotifWrapper(t, rows[index].Content, rows[index].ContentCompression)
			require.Len(t, wrapper.Messages, 1)
			if operation == "answer" || operation == "replace sink" {
				assert.JSONEq(t, `{"type":"system","subtype":"api_retry","attempt":1}`, string(wrapper.Messages[0]))
			} else {
				assert.JSONEq(t, `{"type":"api_retry_resolved"}`, string(wrapper.Messages[0]))
			}
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[index].Source)
		})
	}
}

func TestRetryResolutionCrossesNotificationPagesAndSettlesOnce(t *testing.T) {
	t.Parallel()
	svc, sink := setupRootSink(t, "retry-pages")
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, []byte(`{"type":"system","subtype":"api_retry","attempt":1}`))
	for i := range 65 {
		source := leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX
		if i%2 == 1 {
			source = leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
		}
		persistNotif(t, sink, source, []byte(`{"type":"agent_status","message":"separate notice"}`))
	}
	var writers sync.WaitGroup
	for range 8 {
		writers.Go(func() { sink.PersistLeapMuxNotification(map[string]interface{}{"type": "api_retry_resolved"}) })
	}
	writers.Wait()
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "retry-pages"})
	require.NoError(t, err)
	require.Len(t, rows, 66)
	last := rows[len(rows)-1]
	assert.EqualValues(t, 67, last.Seq, "concurrent resolutions move the row only once")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source)
	wrapper := decodeNotifWrapper(t, last.Content, last.ContentCompression)
	require.Len(t, wrapper.Messages, 1)
	assert.JSONEq(t, `{"type":"api_retry_resolved"}`, string(wrapper.Messages[0]))
}
