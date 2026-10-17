package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// retryingStub is a running agent with the turn-retry route. It records the
// offer that the manager passes and closes started when the retry's Start
// runs.
type retryingStub struct {
	agenttest.IdleAgent
	turnID  string
	model   string
	started chan struct{}
}

func (s *retryingStub) BeginTurnRetry(turnID, model string) (agent.TurnRetry, error) {
	s.turnID, s.model = turnID, model
	return retryingStubRetry{started: s.started}, nil
}

// retryingStubRetry is the accepted retry of retryingStub.
type retryingStubRetry struct {
	started chan struct{}
}

func (r retryingStubRetry) Replace() error { return nil }
func (r retryingStubRetry) Start() error {
	close(r.started)
	return nil
}
func (r retryingStubRetry) Abandon(error) {}

// RetryAgentTurn refuses a subagent: its turn belongs to the root agent.
func TestRetryAgentTurnRefusesASubagent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "root-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID: "child-1", ParentAgentID: sql.NullString{String: "root-1", Valid: true},
		SpawnSpanID: "spawn-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	writer := newTestWriter()
	dispatch(dispatcher, "RetryAgentTurn", &leapmuxv1.RetryAgentTurnRequest{
		AgentId: "child-1", TurnId: "turn-1", Model: "gpt-6-luna",
	}, writer)
	rejections := writer.rejections()
	require.Len(t, rejections, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), rejections[0].code)
}

// A retry for a turn or model the agent no longer offers is a refused
// precondition, not an internal failure.
func TestRetryAgentTurnMapsAnUnavailableOffer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "root-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	// A running provider with no turn-retry route.
	svc.Agents.PutAgentForTest("root-1", struct{ agenttest.IdleAgent }{})

	writer := newTestWriter()
	dispatch(dispatcher, "RetryAgentTurn", &leapmuxv1.RetryAgentTurnRequest{
		AgentId: "root-1", TurnId: "turn-1", Model: "gpt-6-luna",
	}, writer)
	rejections := writer.rejections()
	require.Len(t, rejections, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), rejections[0].code)
	assert.Contains(t, rejections[0].message, "the agent no longer offers this retry")
}

// The request states the offered turn and model. An empty field is refused
// before anything runs.
func TestRetryAgentTurnValidatesTheOfferedTurnAndModel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "root-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	for _, req := range []*leapmuxv1.RetryAgentTurnRequest{
		{AgentId: "root-1", TurnId: "", Model: "gpt-6-luna"},
		{AgentId: "root-1", TurnId: "turn-1", Model: ""},
		{AgentId: "root-1", TurnId: "  ", Model: "gpt-6-luna"},
		{AgentId: "root-1", TurnId: "turn-1", Model: "  "},
	} {
		writer := newTestWriter()
		dispatch(dispatcher, "RetryAgentTurn", req, writer)
		rejections := writer.rejections()
		require.Len(t, rejections, 1)
		assert.Equal(t, int32(codes.InvalidArgument), rejections[0].code)
	}
}

// RetryAgentTurn answers NotFound when no running agent can take the retry.
// Both an id this worker never held and a stored agent without a running
// provider are refused.
func TestRetryAgentTurnMapsAMissingAgent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "root-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	for _, test := range []struct {
		name    string
		agentID string
	}{
		{"an id this worker never held", "no-such-agent"},
		{"a stored agent with no running provider", "root-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := newTestWriter()
			dispatch(dispatcher, "RetryAgentTurn", &leapmuxv1.RetryAgentTurnRequest{
				AgentId: test.agentID, TurnId: "turn-1", Model: "gpt-6-luna",
			}, writer)
			rejections := writer.rejections()
			require.Len(t, rejections, 1)
			assert.Equal(t, int32(codes.NotFound), rejections[0].code)
		})
	}
}

// The happy path: the handler sends the offered turn and model to the running
// provider and answers the empty success response. Both values arrive trimmed,
// so a padded field cannot miss the offer that identifies them. The response
// states the acceptance: the retry's phases run after it, in their own
// goroutine.
func TestRetryAgentTurnRunsTheOfferedRetry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "root-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	stub := &retryingStub{started: make(chan struct{})}
	svc.Agents.PutAgentForTest("root-1", stub)

	writer := newTestWriter()
	dispatch(dispatcher, "RetryAgentTurn", &leapmuxv1.RetryAgentTurnRequest{
		AgentId: "root-1", TurnId: " turn-1 ", Model: " gpt-6-luna ",
	}, writer)
	assert.Empty(t, writer.rejections())
	require.Len(t, writer.responses, 1)
	assert.Equal(t, "turn-1", stub.turnID, "the offered turn is sent trimmed")
	assert.Equal(t, "gpt-6-luna", stub.model, "the offered model is sent trimmed")
	select {
	case <-stub.started:
	case <-time.After(30 * time.Second):
		t.Fatal("the accepted retry never started its turn")
	}
}

// Each caller of restartAgentPreservingSession states its OWN action, whole.
//
// The parameter used to be a bare sentence FRAGMENT interpolated into four frames.
// It read correctly in some and not in others: "failed to finish input queue a forced
// stop" for both callers, "failed to restart agent after an agent settings restart"
// circularly, and the one notification no frame reached told a reader who pressed Stop
// that the agent had failed to RESTART.
func TestRestartMessagesStateTheirOwnCallersAction(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		messages restartMessages
		// word is what every sentence of this caller must state, so a message
		// copied from the other caller fails here.
		word string
	}{
		{name: "settings", messages: settingsRestartMessages, word: "settings"},
		{name: "forced stop", messages: forcedStopMessages, word: "force"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for label, sentence := range map[string]string{
				"pauseFailedLog":      tc.messages.pauseFailedLog,
				"finishFailedLog":     tc.messages.finishFailedLog,
				"restartFailedLog":    tc.messages.restartFailedLog,
				"pauseFailedNotice":   tc.messages.pauseFailedNotice,
				"restartFailedNotice": tc.messages.restartFailedNotice,
			} {
				assert.NotEmptyf(t, sentence, "%s must be stated", label)
				assert.Containsf(t, strings.ToLower(sentence), tc.word,
					"%s must state this caller's own action, not the other caller's", label)
			}
			// A notice is concatenated with the error text, so it has to end where
			// that text begins.
			for _, notice := range []string{tc.messages.pauseFailedNotice, tc.messages.restartFailedNotice} {
				assert.True(t, strings.HasSuffix(notice, ": "),
					"a notice ends where the error text begins: %q", notice)
			}
		})
	}
	assert.NotEqual(t, settingsRestartMessages, forcedStopMessages)
}
