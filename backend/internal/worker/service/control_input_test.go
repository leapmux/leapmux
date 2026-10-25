package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/db/dbtest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

const questionReplyTestInput = "the provider's reply input"

// questionReplyTestPlugin replaces the provider's question reply with
// distinctive values and records what it received, so a test can see which text
// reaches the transcript and which one reaches the agent.
type questionReplyTestPlugin struct {
	agent.Provider
	mu      sync.Mutex
	payload json.RawMessage
	answer  string
}

func (p *questionReplyTestPlugin) QuestionReply(requestPayload json.RawMessage, answer string) (string, error) {
	p.mu.Lock()
	p.payload = append(json.RawMessage(nil), requestPayload...)
	p.answer = answer
	p.mu.Unlock()
	return questionReplyTestInput, nil
}

func (p *questionReplyTestPlugin) received() (json.RawMessage, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append(json.RawMessage(nil), p.payload...), p.answer
}

// questionPayloadReadFault replaces the result of the question payload read
// and passes every other query to the real store.
type questionPayloadReadFault struct {
	db.DBTX
	read func(context.Context) *sql.Row
}

func (store questionPayloadReadFault) QueryRowContext(ctx context.Context, query string, arguments ...any) *sql.Row {
	if strings.Contains(query, "-- name: GetControlResponseRequestPayloadForInput :one") {
		return store.read(ctx)
	}
	return store.DBTX.QueryRowContext(ctx, query, arguments...)
}

// storeAnsweredQuestion records a completed answer to one async question of a
// Codex agent in session-1. It returns the queued input that carries the
// answer, the agent row that resolves it, and the stored request payload.
func storeAnsweredQuestion(t *testing.T, svc *Service) (inputqueue.DispatchItem, db.Agent, []byte) {
	t.Helper()
	ctx := t.Context()
	createClaimTestAgent(t, svc, "agent-1")
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "session-1"}))
	payload := asyncQuestionPayload("question-1")
	require.NoError(t, dbtest.StoreControlResponse(ctx, svc.DB, db.ControlResponseAnswer{
		AgentID: "agent-1", RequestID: "question-1", ClaimToken: "claim-1", State: storedStateCompleted,
		RequestPayload: payload, FollowUp: "Red",
		FollowUpKind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER,
		InputID:      "answer-input", AgentSessionID: "session-1",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, NonBlocking: true,
	}))
	row, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	item := inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
		ItemMetadata: inputqueue.ItemMetadata{
			OrderingMetadata: inputqueue.OrderingMetadata{
				ID:      "answer-input",
				AgentID: "agent-1",
				Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER,
			},
		},
		Text: "Red",
	}}
	return item, row, payload
}

func TestResolveControlInputBuildsTheProviderQuestionReply(t *testing.T) {
	t.Parallel()
	const provider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	plugin := &questionReplyTestPlugin{Provider: testRegistry.Plugin(provider)}
	svc, _, _ := setupTestService(t, withRegistry(registryWithPlugin(t, provider, plugin)))
	item, row, payload := storeAnsweredQuestion(t, svc)

	resolved, err := svc.resolveControlInput(item, row)

	require.NoError(t, err)
	assert.Equal(t, questionReplyTestInput, resolved.text, "the agent reads the provider's own reply")
	source, err := agent.DecodeMessageSupplement(nil, resolved.supplementalContent)
	require.NoError(t, err)
	assert.JSONEq(t, string(payload), string(source.Supplemental))
	receivedPayload, receivedAnswer := plugin.received()
	assert.Equal(t, json.RawMessage(payload), receivedPayload, "the provider reads its stored request")
	assert.Equal(t, "Red", receivedAnswer)
}

func TestResolveControlInputRefusesAnAnswerWithNoQuestion(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	item := inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
		ItemMetadata: inputqueue.ItemMetadata{
			OrderingMetadata: inputqueue.OrderingMetadata{
				ID:      "answer-input",
				AgentID: "agent-1",
				Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER,
			},
		},
		Text: "Red",
	}}

	_, err = svc.resolveControlInput(item, row)

	require.ErrorIs(t, err, errAnswerHasNoQuestion)
	var delivery *inputqueue.DeliveryError
	assert.NotErrorAs(t, err, &delivery, "an answer with no question must not pause the queue")
}

func TestResolveControlInputClassifiesTheQuestionPayloadRead(t *testing.T) {
	t.Parallel()
	t.Run("a missing payload row fails the answer", func(t *testing.T) {
		t.Parallel()
		svc, _, _ := setupTestService(t)
		item, row, _ := storeAnsweredQuestion(t, svc)
		svc.Queries = db.New(questionPayloadReadFault{DBTX: svc.DB, read: func(ctx context.Context) *sql.Row {
			return svc.DB.QueryRowContext(ctx, "SELECT 1 WHERE 0")
		}})

		_, err := svc.resolveControlInput(item, row)

		require.ErrorIs(t, err, errAnswerHasNoQuestion,
			"no Resume brings back a deleted row, so the answer fails at once")
		var delivery *inputqueue.DeliveryError
		assert.NotErrorAs(t, err, &delivery, "a missing row must not pause the queue")
	})
	t.Run("a store fault pauses the queue", func(t *testing.T) {
		t.Parallel()
		svc, _, _ := setupTestService(t)
		item, row, _ := storeAnsweredQuestion(t, svc)
		svc.Queries = db.New(questionPayloadReadFault{DBTX: svc.DB, read: func(ctx context.Context) *sql.Row {
			return svc.DB.QueryRowContext(ctx, "SELECT missing_question_payload_column")
		}})

		_, err := svc.resolveControlInput(item, row)

		var delivery *inputqueue.DeliveryError
		require.ErrorAs(t, err, &delivery, "the next read can succeed, so the answer keeps its place")
		assert.Equal(t, inputqueue.DispatchNotReady, delivery.Outcome)
		assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_STORE_FAULT, delivery.PauseReason)
		assert.ErrorContains(t, err, "read the control request for this answer",
			"the error states which read failed")
	})
}

// questionSessionSwitch changes the provider session after the first validation.
type questionSessionSwitch struct {
	agent.Provider
	switchSession func() error
}

func (p questionSessionSwitch) QuestionReply(payload json.RawMessage, answer string) (string, error) {
	if err := p.switchSession(); err != nil {
		return "", err
	}
	return p.Provider.QuestionReply(payload, answer)
}

func TestQuestionAnswerSteeringRejectsAReplacementSession(t *testing.T) {
	t.Parallel()
	const provider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	var switchSession func() error
	plugin := questionSessionSwitch{Provider: testRegistry.Plugin(provider), switchSession: func() error { return switchSession() }}
	svc, _, _ := setupTestService(t, withRegistry(registryWithPlugin(t, provider, plugin)))
	item, _, _ := storeAnsweredQuestion(t, svc)
	original := &recordingQuestionAgent{}
	replacement := &recordingQuestionAgent{}
	svc.Agents.PutAgentForTest(item.AgentID, original)
	switchSession = func() error {
		if err := svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{ID: item.AgentID, AgentSessionID: "replacement-session"}); err != nil {
			return err
		}
		svc.Agents.PutAgentForTest(item.AgentID, replacement)
		return nil
	}

	_, err := (&agentInputQueueAdapter{svc: svc}).Steer(item)

	require.ErrorIs(t, err, agent.ErrInputSessionChanged)
	originalSteers, originalSends := original.deliveries()
	replacementSteers, replacementSends := replacement.deliveries()
	assert.Empty(t, originalSteers)
	assert.Empty(t, originalSends)
	assert.Empty(t, replacementSteers, "the replacement session must receive no answer")
	assert.Empty(t, replacementSends)
}

// ordinaryInputSteerer hides the optional session-aware capability.
type ordinaryInputSteerer struct {
	agent.Agent
	recorder *recordingQuestionAgent
}

func (p ordinaryInputSteerer) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return p.recorder.SteerInput(content, attachments)
}

func (ordinaryInputSteerer) SupportsSteering() bool { return true }

func TestQuestionSteeringRequiresItsSessionCapabilityOnlyForAnswers(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.AgentInputKind{
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER,
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK,
	} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "session-1"}))
			require.NoError(t, dbtest.StoreControlResponse(t.Context(), svc.DB, db.ControlResponseAnswer{
				AgentID: "agent-1", RequestID: "request-1", ClaimToken: "claim-1", State: storedStateCompleted,
				RequestPayload: asyncQuestionPayload("request-1"), FollowUp: "Red", FollowUpKind: kind,
				InputID: "input-1", AgentSessionID: "session-1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
				NonBlocking: kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER,
			}))
			recorder := &recordingQuestionAgent{}
			svc.Agents.PutAgentForTest("agent-1", ordinaryInputSteerer{Agent: recorder, recorder: recorder})
			item := inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
				ItemMetadata: inputqueue.ItemMetadata{OrderingMetadata: inputqueue.OrderingMetadata{ID: "input-1", AgentID: "agent-1", Kind: kind}},
				Text:         "Red",
			}}
			_, err := (&agentInputQueueAdapter{svc: svc}).Steer(item)
			steered, sent := recorder.deliveries()
			assert.Empty(t, sent)
			if kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER {
				require.ErrorIs(t, err, inputqueue.ErrSteeringUnsupported)
				assert.Empty(t, steered, "an unchecked route must receive no question answer")
			} else {
				require.NoError(t, err)
				require.Len(t, steered, 1)
				assert.Contains(t, steered[0], "Red")
			}
		})
	}
}
