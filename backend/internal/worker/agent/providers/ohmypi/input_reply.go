package ohmypi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type ompReplyPrompt struct {
	receipt  agent.NativeInputReceipt
	yielded  bool
	unproved bool
	steered  bool
	stop     string
	ordinal  int64
	inputs   []agent.NativeReplyBoundary
	items    []agent.NativeReplyItem
}

type ompPromptResult struct {
	ID             string `json:"id"`
	AgentInvoked   *bool  `json:"agentInvoked"`
	Status         string `json:"status"`
	SessionSettled bool   `json:"sessionSettled"`
}

const ompReplyInputOrdinal int64 = 1

func newOMPReplyPrompt(receipt agent.NativeInputReceipt) *ompReplyPrompt {
	return &ompReplyPrompt{
		receipt: receipt, ordinal: ompReplyInputOrdinal,
		inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: ompReplyInputOrdinal}},
	}
}

func (reply *ompReplyPrompt) appendInput(inputID string) {
	reply.ordinal++
	reply.inputs = append(reply.inputs, agent.NativeReplyBoundary{InputID: inputID, AfterOrdinal: reply.ordinal})
}

func (reply *ompReplyPrompt) completedAnswer() bool {
	return reply.stop == ompAssistantStop || reply.stop == contracts.OhMyPiStopReasonLength
}

func newOMPReceipt(sessionID string) agent.NativeInputReceipt {
	id := "leapmux-prompt-" + uuid.NewString()
	return agent.NativeInputReceipt{SessionID: sessionID, InputID: id, TurnID: id}
}

func isOMPBusyRefusal(err error) bool {
	var refusal *commandError
	return errors.As(err, &refusal) &&
		refusal.Message == "Agent is already processing. Use steer() or followUp() to queue messages, or wait for completion."
}

func (a *Agent) registerReplyPrompt(receipt agent.NativeInputReceipt) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	a.registerReplyPromptLocked(newOMPReplyPrompt(receipt))
}

func (a *Agent) registerReplyPromptLocked(reply *ompReplyPrompt) {
	if a.replyPrompts == nil {
		a.replyPrompts = make(map[string]*ompReplyPrompt)
	}
	a.replyPrompts[reply.receipt.InputID] = reply
}

// The response follows fresh admission and precedes agent_start. The
// correlator runs this on the reader before it reads that next frame.
func (a *Agent) observeReplyAdmission(receipt agent.NativeInputReceipt, raw []byte) {
	var response rpcResponse
	if json.Unmarshal(raw, &response) != nil || response.Command != CommandPrompt || !response.Success {
		return
	}
	a.Mu.Lock()
	reply := a.replyPrompts[receipt.InputID]
	if reply == nil || a.sessionHandleLocked() != receipt.SessionID {
		a.Mu.Unlock()
		return
	}
	if promptInvokesAgent(response.Data) {
		if a.turn.runStarted {
			reply.unproved = true
			a.Mu.Unlock()
			return
		}
		if a.replyAwaiting != nil {
			a.replyAwaiting.unproved = true
		}
		a.replyAwaiting = reply
		a.Mu.Unlock()
		return
	}
	delete(a.replyPrompts, receipt.InputID)
	a.Mu.Unlock()
	a.publishOMPReply(reply, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED)
}

// Caller holds Mu. A new admission with no prior yield makes the previous
// scope uncertain. An automatic continuation has no new admission.
func (a *Agent) startReplyOutputLocked() {
	if a.replyAwaiting == nil {
		return
	}
	if a.replyOutput != nil {
		a.replyOutput.unproved = true
	}
	a.replyOutput, a.replyAwaiting = a.replyAwaiting, nil
}

// Caller holds Mu.
func (a *Agent) replyOutputLocked() *ompReplyPrompt {
	if reply := a.replyOutput; reply != nil && reply.receipt.SessionID == a.sessionHandleLocked() {
		return reply
	}
	return nil
}

// Caller holds Mu.
func (a *Agent) conversationReplyLocked(c *conversation) *ompReplyPrompt {
	if c.isRoot() {
		return a.replyOutputLocked()
	}
	return c.childReply
}

// A yield closes this reply boundary before another run can emit rows.
// prompt_result can follow on a later tick and still identifies this input.
func (a *Agent) yieldReplyOutput() {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if reply := a.replyOutputLocked(); reply != nil {
		reply.yielded = true
	}
	a.replyOutput = nil
}

func (a *Agent) takeReplyPrompt(id string) *ompReplyPrompt {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.takeReplyPromptLocked(id)
}

func (a *Agent) takeReplyPromptLocked(id string) *ompReplyPrompt {
	reply := a.replyPrompts[id]
	if reply == nil {
		return nil
	}
	delete(a.replyPrompts, id)
	if a.replyAwaiting == reply {
		a.replyAwaiting = nil
	}
	if a.replyOutput == reply {
		a.replyOutput = nil
	}
	return reply
}

func (a *Agent) persistOMPReplyText(c *conversation, content agent.MessageContent) error {
	var reply *ompReplyPrompt
	var item agent.NativeReplyItem
	a.Mu.Lock()
	hasReply := a.conversationReplyLocked(c) != nil
	a.Mu.Unlock()
	if hasReply {
		if _, ok := agent.ReplyText(ompProvider{}, content); ok {
			a.Mu.Lock()
			if reply = a.conversationReplyLocked(c); reply != nil {
				reply.ordinal++
				ordinal := reply.ordinal
				content.AgentSessionID = reply.receipt.SessionID
				content.NativeTurnID = reply.receipt.TurnID
				content.IdempotencyKey = fmt.Sprintf("omp-reply:%s:%d", reply.receipt.TurnID, ordinal)
				item = agent.NativeReplyItem{Ordinal: ordinal, MessageKey: content.IdempotencyKey}
			}
			a.Mu.Unlock()
		}
	}
	err := c.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{})
	if reply != nil {
		a.Mu.Lock()
		if a.conversationReplyLocked(c) == reply {
			if agent.OutputRetained(err) {
				reply.items = append(reply.items, item)
			} else {
				reply.unproved = true
			}
		}
		a.Mu.Unlock()
	}
	return err
}

func (a *Agent) noteUncommittedReply(c *conversation) {
	if !c.isRoot() {
		return
	}
	for _, id := range c.generation.ScopeIDs() {
		kind, text, open := c.generation.ScopeSnapshot(id)
		if open && kind == agent.AssembledMessageKindText && agent.TrimJavaScriptSpace(text) != "" {
			a.Mu.Lock()
			if reply := a.replyOutputLocked(); reply != nil {
				reply.unproved = true
			}
			a.Mu.Unlock()
			return
		}
	}
}

func (a *Agent) resolveOMPReply(result ompPromptResult) {
	if result.AgentInvoked == nil {
		return
	}
	reply := a.takeReplyPrompt(result.ID)
	if reply == nil {
		return
	}
	outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	switch result.Status {
	case "completed":
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	case "aborted":
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	case "error":
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	}
	if !*result.AgentInvoked && outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED {
		reply.items = nil
		a.publishOMPReply(reply, outcome)
		return
	}
	provedBoundary := !reply.steered || reply.stop != "" || outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	if reply.yielded && provedBoundary && !reply.unproved && outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED {
		a.publishOMPReply(reply, outcome)
		return
	}
	if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED {
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	}
	a.retireOMPReplyInputs(reply, outcome)
}

func (a *Agent) publishOMPReply(reply *ompReplyPrompt, outcome leapmuxv1.AgentInputTurnOutcome) {
	a.publishOMPReplyTo(a.sink, reply, outcome)
}

func (a *Agent) publishOMPReplyTo(sink agent.ProviderServices, reply *ompReplyPrompt, outcome leapmuxv1.AgentInputTurnOutcome) {
	receipt := reply.receipt
	err := sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
		SessionID: receipt.SessionID, TurnID: receipt.TurnID, Outcome: outcome, Items: reply.items,
		Inputs: reply.inputs,
	})
	if !agent.OutputRetained(err) {
		slog.Warn("Store the exact Oh My Pi reply", "agent_id", a.AgentID(), "error", err)
		a.retireOMPReplyInputsOn(sink, reply, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
}

func (a *Agent) retireOMPReplyInputs(reply *ompReplyPrompt, outcome leapmuxv1.AgentInputTurnOutcome) {
	a.retireOMPReplyInputsOn(a.sink, reply, outcome)
}

func (a *Agent) retireOMPReplyInputsOn(sink agent.ProviderServices, reply *ompReplyPrompt, outcome leapmuxv1.AgentInputTurnOutcome) {
	for _, input := range reply.inputs {
		receipt := reply.receipt
		receipt.InputID = input.InputID
		a.retireOMPReply(sink, receipt, outcome)
	}
}

func (a *Agent) retireOMPReply(sink agent.ProviderServices, receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if err := sink.RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		slog.Warn("Store the Oh My Pi reply outcome", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) retireOpenReplies(unreadOutcome agent.SteerOutcome) {
	a.Mu.Lock()
	replies := a.replyPrompts
	steers := a.identifiedSteers
	a.replyPrompts, a.replyAwaiting, a.replyOutput = nil, nil, nil
	a.identifiedSteers = nil
	a.Mu.Unlock()
	replyIDs := make([]string, 0, len(replies))
	for id := range replies {
		replyIDs = append(replyIDs, id)
	}
	slices.Sort(replyIDs)
	for _, id := range replyIDs {
		a.retireOMPReplyInputs(replies[id], leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
	steerIDs := make([]string, 0, len(steers))
	for id := range steers {
		steerIDs = append(steerIDs, id)
	}
	slices.Sort(steerIDs)
	for _, id := range steerIDs {
		steer := steers[id]
		steer.Outcome = unreadOutcome
		steer.Receipt = agent.NativeInputReceipt{}
		a.reportOMPSteer(steer)
	}
}

func (a *Agent) observeOMPPause(c *conversation, raw []byte) {
	var pause struct {
		Yielded bool `json:"yielded"`
	}
	if json.Unmarshal(raw, &pause) == nil && pause.Yielded {
		a.noteUncommittedReply(c)
		a.yieldReplyOutput()
	}
	a.persistRaw(c, raw)
}
