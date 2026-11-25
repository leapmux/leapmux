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
		NonBlocking: true,
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

func (p ordinaryInputSteerer) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	return p.recorder.SteerInput(request)
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
				InputID: "input-1", AgentSessionID: "session-1",
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

type receiptQuestionAgent struct {
	recordingQuestionAgent
	sendReceipt  agent.NativeInputReceipt
	steerReceipt agent.NativeInputReceipt
}

func (a *receiptQuestionAgent) SendInput(content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	_, err := a.recordingQuestionAgent.SendInputForSession("", content, attachments)
	return a.sendReceipt, err
}

func (a *receiptQuestionAgent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	_, err := a.recordingQuestionAgent.SendInputForSession(sessionID, content, attachments)
	return a.sendReceipt, err
}

func (a *receiptQuestionAgent) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	_, err := a.recordingQuestionAgent.SteerInput(request)
	return a.steerReceipt, err
}

func (a *receiptQuestionAgent) SteerInputForSession(sessionID string, request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	_, err := a.recordingQuestionAgent.SteerInputForSession(sessionID, request)
	return a.steerReceipt, err
}

func TestControlLinkedInputPropagatesNativeReceipt(t *testing.T) {
	t.Parallel()
	wantSend := agent.NativeInputReceipt{SessionID: "session-1", InputID: "native-send-1", TurnID: "turn-1"}
	wantSteer := agent.NativeInputReceipt{SessionID: "session-1", InputID: "native-steer-1", TurnID: "turn-1"}

	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{
		ID: "agent-1", AgentSessionID: "session-1",
	}))
	for _, tc := range []struct {
		id, reqID, claim, followUp string
		followUpKind               leapmuxv1.AgentInputKind
		nonBlocking                bool
	}{
		{"input-plan", "req-plan", "claim-plan", "", leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED, false},
		{"input-question", "req-question", "claim-question", "Red", leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, true},
		{"input-feedback", "req-feedback", "claim-feedback", "Use option B.", leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, false},
	} {
		require.NoError(t, dbtest.StoreControlResponse(t.Context(), svc.DB, db.ControlResponseAnswer{
			AgentID: "agent-1", RequestID: tc.reqID, ClaimToken: tc.claim, State: storedStateCompleted,
			RequestPayload: asyncQuestionPayload(tc.reqID), FollowUp: tc.followUp, FollowUpKind: tc.followUpKind,
			InputID: tc.id, AgentSessionID: "session-1", NonBlocking: tc.nonBlocking,
		}))
	}

	recorder := &receiptQuestionAgent{sendReceipt: wantSend, steerReceipt: wantSteer}
	svc.Agents.PutAgentForTest("agent-1", recorder)
	adapter := &agentInputQueueAdapter{svc: svc}

	for _, tc := range []struct {
		name string
		item inputqueue.DispatchItem
	}{
		{
			name: "plan execution",
			item: inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
				ItemMetadata: inputqueue.ItemMetadata{OrderingMetadata: inputqueue.OrderingMetadata{
					ID: "input-plan", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION,
				}},
				Text: "Implement the approved plan.",
			}},
		},
		{
			name: "question answer",
			item: inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
				ItemMetadata: inputqueue.ItemMetadata{OrderingMetadata: inputqueue.OrderingMetadata{
					ID: "input-question", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER,
				}},
				Text: "Red",
			}},
		},
		{
			name: "control feedback",
			item: inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
				ItemMetadata: inputqueue.ItemMetadata{OrderingMetadata: inputqueue.OrderingMetadata{
					ID: "input-feedback", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK,
				}},
				Text: "Use option B.",
			}},
		},
	} {
		t.Run("dispatch/"+tc.name, func(t *testing.T) {
			result, err := adapter.Dispatch(tc.item)
			require.NoError(t, err)
			assert.Equal(t, wantSend, result.NativeReceipt)
		})
	}

	steerResult, err := adapter.Steer(inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
		ItemMetadata: inputqueue.ItemMetadata{OrderingMetadata: inputqueue.OrderingMetadata{
			ID: "input-question", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER,
		}},
		Text: "Red",
	}})
	require.NoError(t, err)
	assert.Equal(t, wantSteer, steerResult.NativeReceipt)
}

func TestCompactContextFallbackPropagatesNativeReceipt(t *testing.T) {
	t.Parallel()
	wantSend := agent.NativeInputReceipt{SessionID: "session-1", InputID: "native-compact-1", TurnID: "turn-compact"}

	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	recorder := &receiptQuestionAgent{sendReceipt: wantSend}
	svc.Agents.PutAgentForTest("agent-1", recorder)

	result, err := (&agentInputQueueAdapter{svc: svc}).Dispatch(inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
		ItemMetadata: inputqueue.ItemMetadata{OrderingMetadata: inputqueue.OrderingMetadata{
			ID: "compact-1", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT,
		}},
		Text: "/compact",
	}})
	require.NoError(t, err)
	assert.Equal(t, wantSend, result.NativeReceipt)
	_, sent := recorder.deliveries()
	assert.Equal(t, []string{"/compact"}, sent)
}
