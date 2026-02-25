package service_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/msgcodec"
	"github.com/leapmux/leapmux/internal/hub/service"
)

// newFastAutoContinueBackoff creates a fast backoff for testing (1ms → 10ms).
func newFastAutoContinueBackoff() *backoff.ExponentialBackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 1 * time.Millisecond
	b.MaxInterval = 10 * time.Millisecond
	b.Multiplier = 2.0
	b.RandomizationFactor = 0
	b.Reset()
	return b
}

// syntheticAPIError500 returns JSON matching a synthetic 500 API error.
func syntheticAPIError500() []byte {
	return []byte(`{"type":"assistant","error":"unknown","message":{"id":"msg-1","model":"<synthetic>","role":"assistant","stop_reason":"stop_sequence","content":[{"type":"text","text":"API Error: 500 {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"Internal server error\"}}"}],"usage":{"input_tokens":0,"output_tokens":0}}}`)
}

// syntheticAPIError529 returns JSON matching a synthetic 529 overloaded error.
func syntheticAPIError529() []byte {
	return []byte(`{"type":"assistant","error":"unknown","message":{"id":"msg-2","model":"<synthetic>","role":"assistant","stop_reason":"stop_sequence","content":[{"type":"text","text":"API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}"}],"usage":{"input_tokens":0,"output_tokens":0}}}`)
}

func TestIsSyntheticAPIError(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    bool
	}{
		{
			name:    "valid 500 error",
			content: syntheticAPIError500(),
			want:    true,
		},
		{
			name:    "valid 529 overloaded error",
			content: syntheticAPIError529(),
			want:    true,
		},
		{
			name:    "normal assistant message",
			content: []byte(`{"type":"assistant","message":{"id":"msg-1","model":"claude-sonnet-4-20250514","role":"assistant","content":[{"type":"text","text":"Hello!"}]}}`),
			want:    false,
		},
		{
			name:    "client error 400",
			content: []byte(`{"type":"assistant","error":"unknown","message":{"id":"msg-1","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"API Error: 400 Bad Request"}]}}`),
			want:    false,
		},
		{
			name:    "client error 401",
			content: []byte(`{"type":"assistant","error":"unknown","message":{"id":"msg-1","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"API Error: 401 Unauthorized"}]}}`),
			want:    false,
		},
		{
			name:    "client error 429",
			content: []byte(`{"type":"assistant","error":"unknown","message":{"id":"msg-1","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"API Error: 429 Too Many Requests"}]}}`),
			want:    false,
		},
		{
			name:    "non-synthetic model",
			content: []byte(`{"type":"assistant","error":"unknown","message":{"id":"msg-1","model":"claude-sonnet-4-20250514","role":"assistant","content":[{"type":"text","text":"API Error: 500 Internal Server Error"}]}}`),
			want:    false,
		},
		{
			name:    "empty error field",
			content: []byte(`{"type":"assistant","error":"","message":{"id":"msg-1","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"API Error: 500 Internal Server Error"}]}}`),
			want:    false,
		},
		{
			name:    "missing error field",
			content: []byte(`{"type":"assistant","message":{"id":"msg-1","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"API Error: 500 Internal Server Error"}]}}`),
			want:    false,
		},
		{
			name:    "missing message field",
			content: []byte(`{"type":"assistant","error":"unknown"}`),
			want:    false,
		},
		{
			name:    "empty content array",
			content: []byte(`{"type":"assistant","error":"unknown","message":{"id":"msg-1","model":"<synthetic>","role":"assistant","content":[]}}`),
			want:    false,
		},
		{
			name:    "invalid JSON",
			content: []byte(`not json`),
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, service.IsSyntheticAPIError(tt.content))
		})
	}
}

// waitForUserMessages polls the DB until at least minCount USER messages exist
// for the given agent, or the timeout expires.
func waitForUserMessages(t *testing.T, queries *gendb.Queries, agentID string, minCount int, timeout time.Duration) []gendb.Message {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		msgs, err := queries.ListMessagesByAgentID(context.Background(), gendb.ListMessagesByAgentIDParams{
			AgentID: agentID,
			Seq:     0,
			Limit:   50,
		})
		require.NoError(t, err)
		var userMsgs []gendb.Message
		for _, m := range msgs {
			if m.Role == leapmuxv1.MessageRole_MESSAGE_ROLE_USER {
				userMsgs = append(userMsgs, m)
			}
		}
		if len(userMsgs) >= minCount {
			return userMsgs
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d user messages, got %d", minCount, len(userMsgs))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// countUserMessages returns the number of USER messages for the agent.
func countUserMessages(t *testing.T, queries *gendb.Queries, agentID string) int {
	t.Helper()
	msgs, err := queries.ListMessagesByAgentID(context.Background(), gendb.ListMessagesByAgentIDParams{
		AgentID: agentID,
		Seq:     0,
		Limit:   50,
	})
	require.NoError(t, err)
	count := 0
	for _, m := range msgs {
		if m.Role == leapmuxv1.MessageRole_MESSAGE_ROLE_USER {
			count++
		}
	}
	return count
}

// extractContent extracts the "content" field from a stored user message.
func extractContent(t *testing.T, msg gendb.Message) string {
	t.Helper()
	data, err := msgcodec.Decompress(msg.Content, msg.ContentCompression)
	require.NoError(t, err)
	// The stored format is a threadWrapper: {"old_seqs":[],"messages":[<raw>]}
	var wrapper struct {
		Messages []json.RawMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(data, &wrapper))
	require.NotEmpty(t, wrapper.Messages)
	var inner struct {
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(wrapper.Messages[0], &inner))
	return inner.Content
}

func TestScheduleAutoContinue_SendsContinueMessage(t *testing.T) {
	restore := service.OverrideAutoContinueBackoff(newFastAutoContinueBackoff)
	defer restore()

	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Auto-Continue Test")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	// Emit a synthetic API error.
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: syntheticAPIError500(),
	})

	// Wait for the auto-continue "Continue." message.
	userMsgs := waitForUserMessages(t, env.queries, agentID, 1, 2*time.Second)
	assert.Equal(t, "Continue.", extractContent(t, userMsgs[0]))
}

func TestScheduleAutoContinue_CancelledByUserMessage(t *testing.T) {
	// Use a slower backoff so we have time to cancel before it fires.
	restore := service.OverrideAutoContinueBackoff(func() *backoff.ExponentialBackOff {
		b := backoff.NewExponentialBackOff()
		b.InitialInterval = 500 * time.Millisecond
		b.MaxInterval = 500 * time.Millisecond
		b.Multiplier = 1.0
		b.RandomizationFactor = 0
		b.Reset()
		return b
	})
	defer restore()

	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Cancel Test")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	// Emit a synthetic API error to schedule auto-continue.
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: syntheticAPIError500(),
	})

	// Immediately send a user message to cancel the pending auto-continue.
	_, err := env.client.SendAgentMessage(context.Background(), authedReq(&leapmuxv1.SendAgentMessageRequest{
		AgentId: agentID,
		Content: "User message",
	}, env.token))
	require.NoError(t, err)

	// Wait longer than the backoff interval and verify no "Continue." message appeared.
	time.Sleep(800 * time.Millisecond)
	userMsgs := waitForUserMessages(t, env.queries, agentID, 1, 100*time.Millisecond)
	for _, msg := range userMsgs {
		content := extractContent(t, msg)
		assert.NotEqual(t, "Continue.", content, "auto-continue should have been cancelled")
	}
}

func TestScheduleAutoContinue_CancelledByClose(t *testing.T) {
	restore := service.OverrideAutoContinueBackoff(func() *backoff.ExponentialBackOff {
		b := backoff.NewExponentialBackOff()
		b.InitialInterval = 500 * time.Millisecond
		b.MaxInterval = 500 * time.Millisecond
		b.Multiplier = 1.0
		b.RandomizationFactor = 0
		b.Reset()
		return b
	})
	defer restore()

	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Close Cancel Test")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	// Emit a synthetic API error.
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: syntheticAPIError500(),
	})

	// Close the agent to cancel auto-continue.
	_, err := env.client.CloseAgent(context.Background(), authedReq(&leapmuxv1.CloseAgentRequest{
		AgentId: agentID,
	}, env.token))
	require.NoError(t, err)

	// Wait longer than the backoff interval.
	time.Sleep(800 * time.Millisecond)
	assert.Equal(t, 0, countUserMessages(t, env.queries, agentID),
		"no auto-continue message should appear after agent close")
}

func TestScheduleAutoContinue_ResetOnNormalMessage(t *testing.T) {
	restore := service.OverrideAutoContinueBackoff(newFastAutoContinueBackoff)
	defer restore()

	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Reset Test")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	// Emit synthetic error to advance backoff.
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: syntheticAPIError500(),
	})

	// Wait for the first "Continue." message.
	waitForUserMessages(t, env.queries, agentID, 1, 2*time.Second)

	// Now emit a normal assistant message — this resets the backoff.
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"OK, continuing."}]}}`),
	})

	// Emit another error — should use the initial backoff (1ms, not escalated).
	before := time.Now()
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: syntheticAPIError500(),
	})

	// Wait for the second "Continue." message.
	waitForUserMessages(t, env.queries, agentID, 2, 2*time.Second)
	elapsed := time.Since(before)

	// With 1ms initial backoff and no jitter, the second message should arrive
	// very quickly (well under 100ms), confirming backoff was reset.
	assert.Less(t, elapsed, 100*time.Millisecond,
		"second auto-continue should fire quickly after backoff reset")
}

func TestScheduleAutoContinue_SkipsInactiveAgent(t *testing.T) {
	restore := service.OverrideAutoContinueBackoff(newFastAutoContinueBackoff)
	defer restore()

	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Inactive Test")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	// Close the agent first so it's INACTIVE.
	_, err := env.client.CloseAgent(context.Background(), authedReq(&leapmuxv1.CloseAgentRequest{
		AgentId: agentID,
	}, env.token))
	require.NoError(t, err)

	// Manually emit a synthetic error (as if it arrived before the close was processed).
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: syntheticAPIError500(),
	})

	// Wait a bit — the auto-continue goroutine should see the agent is inactive and skip.
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 0, countUserMessages(t, env.queries, agentID),
		"no auto-continue for inactive agent")
}

func TestScheduleAutoContinue_ReplacesExistingPending(t *testing.T) {
	restore := service.OverrideAutoContinueBackoff(newFastAutoContinueBackoff)
	defer restore()

	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Replace Test")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	// Emit two synthetic errors rapidly — the second should cancel the first
	// pending auto-continue, so we should end up with exactly one "Continue." message
	// (from the second schedule), not two.
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: syntheticAPIError500(),
	})
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: syntheticAPIError529(),
	})

	// Wait for a "Continue." to appear.
	waitForUserMessages(t, env.queries, agentID, 1, 2*time.Second)

	// Give a bit more time to see if a second one arrives (it shouldn't).
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, countUserMessages(t, env.queries, agentID),
		"only one auto-continue should fire when errors arrive in rapid succession")
}
