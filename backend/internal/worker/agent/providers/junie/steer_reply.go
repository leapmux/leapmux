package junie

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

const junieSteerAnswerPrefix = "junie-task-answer:"

type junieSteerReplies struct {
	mu              sync.Mutex
	active          *junieSteerTurn
	activeFlag      atomic.Bool
	startedNewTurns int
}

func (replies *junieSteerReplies) hasActiveSession(sessionID string) bool {
	if !replies.activeFlag.Load() {
		return false
	}
	replies.mu.Lock()
	defer replies.mu.Unlock()
	return replies.active != nil && replies.active.sessionID == sessionID
}

func (replies *junieSteerReplies) noteStartedNewTurn(opened bool) {
	replies.mu.Lock()
	defer replies.mu.Unlock()
	if opened {
		replies.startedNewTurns = 1
		return
	}
	replies.startedNewTurns++
}

// consumeStartedNewTurn decrements the pending startedNewTurn count and
// reports whether no further queued turns remain (so the agent turn may end).
func (replies *junieSteerReplies) consumeStartedNewTurn() bool {
	replies.mu.Lock()
	defer replies.mu.Unlock()
	if replies.startedNewTurns > 1 {
		replies.startedNewTurns--
		return false
	}
	replies.startedNewTurns = 0
	return true
}

// A single ACP prompt can contain several tasks of Junie's own. Each task
// supplies the exact subset of acknowledged steers that the model read.
type junieSteerTurn struct {
	sessionID string
	cursor    sessionstore.JournalCursor
	inputs    []junieSteerInput
	answers   []junieACPAnswer
}

type junieACPAnswer struct {
	key, text string
}

type junieSteerInput struct {
	receipt agent.NativeInputReceipt
	text    string
}

// SteerInput injects text into the running turn through Junie's own
// `_session/steering` and returns its admission receipt when journal tracking
// is available. The reply's outcome states what the session did: `injected`
// reached the running turn, and `startedNewTurn` began a new turn of Junie's
// own, which the provider tracks as an agent turn so its end still closes the
// turn state. A refused request returns ErrNoActiveTurn, so the queue keeps the
// input for the next turn.
func (a *Agent) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	var receipt agent.NativeInputReceipt
	if request.Content == "" && len(request.Attachments) == 0 {
		return receipt, nil
	}
	a.steerDispatchMu.Lock()
	defer a.steerDispatchMu.Unlock()
	err := a.WithSessionID(func(sessionID string) error {
		if !a.PromptActive() {
			return agent.ErrNoActiveTurn
		}
		params, err := json.Marshal(map[string]any{
			"sessionId": sessionID,
			"prompt":    acp.BuildPromptBlocks(request.Content, agent.ClassifyAttachments(request.Attachments)),
		})
		if err != nil {
			return fmt.Errorf("encode the Junie steer: %w", err)
		}
		var cursor sessionstore.JournalCursor
		track := false
		if request.InputID != "" {
			if a.steerReplies.hasActiveSession(sessionID) {
				track = true
			} else {
				home := junieHome(agent.StoredSessionQuery{HomeDir: a.homeDir})
				if data, readErr := readJunieEvents(home, sessionID); readErr == nil {
					cursor, track = sessionstore.NewJournalCursor(data), true
				}
			}
		}
		var malformedReply error
		_, err = a.SendRequestObserved(junieSteerMethod, params, a.APITimeout(), func(response json.RawMessage, replyErr error) {
			if replyErr != nil {
				return
			}
			reply, ok := providerkit.RawObject(response)
			if !ok || reply == nil {
				malformedReply = fmt.Errorf("%w: the Junie steer reply must be an object", agent.ErrDeliveryUncertain)
				return
			}
			outcome, _ := providerkit.RawString(reply["outcome"])
			if outcome == "startedNewTurn" {
				a.steerReplies.noteStartedNewTurn(a.BeginAgentTurn())
			}
			if !track || (outcome != "injected" && outcome != "startedNewTurn") {
				return
			}
			candidate := agent.NativeInputReceipt{SessionID: sessionID, InputID: id.Generate()}
			if a.steerReplies.admit(junieSteerInput{receipt: candidate, text: request.Content}, cursor) {
				receipt = candidate
			}
		})
		if err != nil {
			if providerkit.HasJSONRPCErrorCode(err, -32600, -32602) {
				return agent.ErrNoActiveTurn
			}
			return providerkit.ClassifyJSONRPCDeliveryError(junieSteerMethod, err)
		}
		return malformedReply
	})
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	return receipt, nil
}

func (replies *junieSteerReplies) admit(input junieSteerInput, cursor sessionstore.JournalCursor) bool {
	replies.mu.Lock()
	defer replies.mu.Unlock()
	if replies.active == nil {
		replies.active = &junieSteerTurn{
			sessionID: input.receipt.SessionID,
			cursor:    cursor,
		}
		replies.activeFlag.Store(true)
	}
	if replies.active.sessionID != input.receipt.SessionID {
		return false
	}
	replies.active.inputs = append(replies.active.inputs, input)
	return true
}

func (a *Agent) retireJunieSteers() {
	a.steerReplies.mu.Lock()
	turn := a.steerReplies.active
	a.steerReplies.active = nil
	a.steerReplies.activeFlag.Store(false)
	a.steerReplies.startedNewTurns = 0
	a.steerReplies.mu.Unlock()
	if turn == nil {
		return
	}
	sink := a.Sink()
	for _, input := range turn.inputs {
		if err := sink.RetireNativeInputTurn(input.receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); !agent.OutputRetained(err) {
			slog.Warn("retire the Junie steer on context clear", "agent_id", a.AgentID(), "input_id", input.receipt.InputID, "error", err)
		}
	}
}

// Junie emits a whole answer in a final_answer chunk. The journal may record
// completion after that chunk arrives. Retain its position and compare the
// saved row with the corresponding completed task after ACP drains output.
func (a *Agent) junieSteerAnswerID(info acp.UpdateMessageInfo) string {
	if info.RowKey != "" || info.UpdateType != contracts.ACPUpdateAgentMessageChunk {
		return ""
	}
	update := info.Raw
	if !a.steerReplies.activeFlag.Load() || !bytes.Contains(update, []byte(`"final_answer"`)) {
		return ""
	}
	var frame struct {
		Meta struct {
			Junie struct {
				Phase string `json:"phase"`
			} `json:"junie"`
		} `json:"_meta"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(update, &frame) != nil || frame.Meta.Junie.Phase != "final_answer" ||
		frame.Content.Type != "text" || frame.Content.Text == "" {
		return ""
	}
	sessionID := a.CurrentSessionID()
	a.steerReplies.mu.Lock()
	defer a.steerReplies.mu.Unlock()
	turn := a.steerReplies.active
	if turn == nil || turn.sessionID != sessionID {
		return ""
	}
	key := junieSteerAnswerPrefix + id.Generate()
	turn.answers = append(turn.answers, junieACPAnswer{key: key, text: frame.Content.Text})
	return key
}

func junieSteerAnswerKey(messageID string) string {
	if strings.HasPrefix(messageID, junieSteerAnswerPrefix) {
		return messageID
	}
	return ""
}

// ACP releases its state locks before the operation reconciles stored rows.
// It supplies the direct sink because the operation already owns queue order.
func (a *Agent) captureJunieSteerCompletion(sink agent.ProviderServices) func() error {
	a.steerReplies.mu.Lock()
	turn := a.steerReplies.active
	a.steerReplies.active = nil
	a.steerReplies.activeFlag.Store(false)
	a.steerReplies.mu.Unlock()
	if turn == nil {
		return nil
	}
	proof := sync.OnceValue(func() junieSteerProof { return a.readJunieSteerProof(turn) })
	return func() error { return finishJunieSteerReplies(sink, turn, proof()) }
}

type junieSteerProof struct {
	admissions map[string]agent.NativeInputReceipt
	replies    []junieTaskReply
}

func (a *Agent) readJunieSteerProof(turn *junieSteerTurn) junieSteerProof {
	data, err := readJunieEvents(junieHome(agent.StoredSessionQuery{HomeDir: a.homeDir}), turn.sessionID)
	if err != nil {
		return junieSteerProof{}
	}
	admissions, replies, ok := readJunieSteerProofEvents(turn.cursor, data, turn.inputs)
	if !ok || !matchJunieAnswers(replies, turn.answers) {
		return junieSteerProof{}
	}
	return junieSteerProof{admissions: admissions, replies: replies}
}

func finishJunieSteerReplies(sink agent.ProviderServices, turn *junieSteerTurn, proof junieSteerProof) error {
	proved := make(map[string]bool)
	for i, answer := range proof.replies {
		ids, err := publishJunieSteerReply(sink, turn.sessionID, answer, turn.answers[i].key, proof.admissions)
		if err != nil {
			return err
		}
		for _, id := range ids {
			proved[id] = true
		}
	}
	for _, input := range turn.inputs {
		receipt := input.receipt
		if proved[receipt.InputID] {
			continue
		}
		if err := sink.RetireNativeInputTurn(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); err != nil {
			return fmt.Errorf("retire the Junie steer reply %q: %w", receipt.InputID, err)
		}
	}
	return nil
}

func matchJunieAnswers(native []junieTaskReply, captured []junieACPAnswer) bool {
	if len(native) != len(captured) {
		return false
	}
	for i, answer := range native {
		if strings.TrimSpace(answer.text) != strings.TrimSpace(captured[i].text) {
			return false
		}
	}
	return true
}

func publishJunieSteerReply(sink agent.ProviderServices, sessionID string, answer junieTaskReply, key string, admissions map[string]agent.NativeInputReceipt) ([]string, error) {
	stored, err := sink.ReadMessageByIdempotencyKey(sessionID, key)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, nil
	}
	text, ok := agent.ReplyText(junieProvider{}, stored.Content)
	if !ok || strings.TrimSpace(text) != strings.TrimSpace(answer.text) ||
		stored.Content.Completion == agent.MessageCompletionInterrupted || stored.Content.Completion == agent.MessageCompletionError {
		return nil, nil
	}
	inputs := make([]agent.NativeReplyBoundary, 0, len(answer.inputIDs))
	ids := make([]string, 0, len(answer.inputIDs))
	for _, id := range answer.inputIDs {
		if receipt, found := admissions[id]; found {
			inputs = append(inputs, agent.NativeReplyBoundary{InputID: receipt.InputID, AfterOrdinal: 1})
			ids = append(ids, receipt.InputID)
		}
	}
	if len(inputs) == 0 {
		return nil, nil
	}
	err = sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
		SessionID:      sessionID,
		TurnID:         answer.taskID,
		Outcome:        leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Items:          []agent.NativeReplyItem{{Ordinal: 2, MessageKey: key}},
		Inputs:         inputs,
		ExclusiveItems: len(inputs) == len(answer.inputIDs),
	})
	if err != nil {
		return nil, fmt.Errorf("store the Junie task reply %q: %w", answer.taskID, err)
	}
	return ids, nil
}
