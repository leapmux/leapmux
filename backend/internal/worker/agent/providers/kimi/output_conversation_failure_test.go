package kimi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiConversationFailureCancelsTheOlderPageWithoutReleasingAdmission(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	entered, cancelled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(entered)
		select {
		case <-request.Context().Done():
			close(cancelled)
		case <-ctx.Done():
		}
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	rig := newKimiConversationRig(t)
	rig.agent.api = &kimiClient{endpoint: endpoint, timeout: 30 * time.Second}
	rig.conversationFrame(t, `{"type":"turns","has_more":true,"turns":[{"turn_id":"recent","ordinal":2,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[{"type":"assistant_message","item_id":"recent-answer","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Uncommitted history"}]}]}`)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the older conversation request did not start")
	}
	rig.conversationFrame(t, `{"type":"turns.failed","code":0,"msg":"Native history unavailable"}`)
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("the subscription failure did not cancel its older history request")
	}
	rig.agent.transcriptRecoveryWork.CloseAndWait()
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Empty(t, rig.sink.Messages(), "a retired page cannot commit after the native failure")
	assert.Equal(t, 1, rig.sink.NotificationCount())
	assert.True(t, rig.agent.PublishTurnActive().Active, "missing replacement history still holds input admission")
	assert.Zero(t, rig.agent.pendingNativeBytes)
	rig.conversationFrame(t, `{"type":"turns","has_more":false,"turns":[]}`)
	assert.False(t, rig.agent.PublishTurnActive().Active)
}

func TestKimiConversationFailureRemainsVisibleUntilAReplacementSnapshot(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	run := rig.agent.run(kimiMainAgentID)
	rig.agent.awaitConversationSnapshot(run)
	rig.conversationFrame(t, `{"type":"turns.failed","code":0,"msg":"Native conversation storage unavailable"}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Equal(t, 1, rig.sink.NotificationCount(), "a subscription failure must not wait behind its own missing history")
	message := rig.sink.LastNotification()
	var record struct {
		Type      string          `json:"type"`
		SessionID string          `json:"session_id"`
		AgentID   string          `json:"agent_id"`
		Failure   json.RawMessage `json:"failure"`
	}
	require.NoError(t, json.Unmarshal(message.Content, &record))
	assert.Equal(t, "leapmux.kimi.failure", record.Type)
	assert.Equal(t, "session_1", record.SessionID)
	assert.Equal(t, "main", record.AgentID)
	var failure struct {
		Type string `json:"type"`
		Code *int   `json:"code"`
		Msg  string `json:"msg"`
	}
	require.NoError(t, json.Unmarshal(record.Failure, &failure))
	assert.Equal(t, "turns.failed", failure.Type)
	require.NotNil(t, failure.Code)
	assert.Zero(t, *failure.Code)
	assert.Equal(t, "Native conversation storage unavailable", failure.Msg)
	assert.False(t, message.TurnEnd)
	assert.NotNil(t, run.conversationRecovery.Load())
	rig.conversationFrame(t, `{"type":"turns","turns":[],"has_more":false}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Nil(t, run.conversationRecovery.Load(), "the native replacement snapshot releases admission")
	assert.Equal(t, 1, rig.sink.NotificationCount())
}
