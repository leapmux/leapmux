package pi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type piReplyRun struct {
	receipt  agent.NativeInputReceipt
	inputs   []agent.NativeReplyBoundary
	started  bool
	unproved bool
	ordinal  int64
	items    []agent.NativeReplyItem
	stop     string
	steered  bool
}

const piReplyInputOrdinal int64 = 1

func newPiReplyRun(receipt agent.NativeInputReceipt, started bool) *piReplyRun {
	return &piReplyRun{
		receipt: receipt, started: started, ordinal: piReplyInputOrdinal,
		inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: piReplyInputOrdinal}},
	}
}

func (a *Agent) sendPiReplyPrompt(sessionID string, payload map[string]any) (agent.NativeInputReceipt, error) {
	id := "leapmux-prompt-" + uuid.NewString()
	receipt := agent.NativeInputReceipt{SessionID: sessionID, InputID: id, TurnID: id}
	a.Mu.Lock()
	if a.sessionHandleLocked() != sessionID {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, agent.ErrInputSessionChanged
	}
	if a.currentTurnActive || a.replyPending.InputID != "" || a.replyRun != nil {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, agent.ErrAgentBusy
	}
	a.replyPending = receipt
	a.Mu.Unlock()

	var observed atomic.Bool
	wait, err := a.beginPiCommandWithID(id, CommandPrompt, payload, func(raw json.RawMessage) {
		observed.Store(true)
		// The reply marks the next run in the same FIFO as the run's output.
		// An interrupt can hold older events when this response reaches the reader.
		handlePiOutput(a, providerkit.ParseLine(raw))
	})
	if err != nil {
		a.takePiReplyPending(id)
		if wait != nil {
			// A partial write can leave its registration live.
			go func() { _, _ = wait(a.APITimeout()) }()
		}
		return agent.NativeInputReceipt{}, err
	}
	go func() {
		if _, err := wait(0); err != nil && !observed.Load() {
			if pending := a.takePiReplyPending(id); pending.InputID != "" {
				a.retirePiReply(pending, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
				a.handlePiPromptFailure(err)
			}
		}
	}()
	return receipt, nil
}

func (a *Agent) takePiReplyPending(id string) agent.NativeInputReceipt {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.replyPending.InputID != id {
		return agent.NativeInputReceipt{}
	}
	receipt := a.replyPending
	a.replyPending = agent.NativeInputReceipt{}
	return receipt
}

// Pi emits the correlated "started" response before the new run's agent_start.
// Queued prompts have no receiving run identity in the response.
func (a *Agent) handlePiReplyResponse(line *providerkit.ParsedLine) bool {
	data, err := parsePiResponse(CommandPrompt, line.Raw)
	disposition := piPromptDisposition(data)
	a.Mu.Lock()
	receipt := a.replyPending
	if receipt.InputID == "" || receipt.InputID != line.IDString() {
		a.Mu.Unlock()
		return false
	}
	a.replyPending = agent.NativeInputReceipt{}
	admitted := err == nil && disposition == "started" && !a.currentTurnActive &&
		a.replyRun == nil && a.sessionHandleLocked() == receipt.SessionID
	if admitted {
		a.replyRun = newPiReplyRun(receipt, false)
	}
	a.Mu.Unlock()
	if admitted {
		return true
	}
	if err != nil {
		var refusal *piCommandRefusal
		outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		if errors.As(err, &refusal) {
			outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
		}
		a.retirePiReply(receipt, outcome)
		a.handlePiPromptFailure(err)
		return true
	}
	if disposition == "handled" {
		// The extension consumed the prompt without starting its own model run.
		a.publishPiReply(newPiReplyRun(receipt, false), leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED)
		a.PublishTurnActive()
		return true
	}
	a.retirePiReply(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	a.PublishTurnActive()
	return true
}

// Caller holds Mu. A session switch cannot transfer reply ownership.
func (a *Agent) piReplyRunLocked() *piReplyRun {
	if run := a.replyRun; run != nil && run.started && run.receipt.SessionID == a.sessionHandleLocked() {
		return run
	}
	return nil
}

func (a *Agent) piReplyControlReceipt() agent.NativeInputReceipt {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if run := a.piReplyRunLocked(); run != nil {
		return run.receipt
	}
	return agent.NativeInputReceipt{}
}

func (a *Agent) preparePiReplyRow(content *agent.MessageContent) (*piReplyRun, agent.NativeReplyItem) {
	if _, text := (piProvider{}).ReplyText(*content); !text {
		return nil, agent.NativeReplyItem{}
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if run := a.piReplyRunLocked(); run != nil {
		// Canonical reply ordering also works if the model produces a reply
		// before the Worker accepts and writes the user row.
		run.ordinal++
		content.AgentSessionID = run.receipt.SessionID
		content.NativeTurnID = run.receipt.TurnID
		content.IdempotencyKey = fmt.Sprintf("pi-reply:%s:%d", run.receipt.TurnID, run.ordinal)
		return run, agent.NativeReplyItem{Ordinal: run.ordinal, MessageKey: content.IdempotencyKey}
	}
	return nil, agent.NativeReplyItem{}
}

func (a *Agent) recordPiReplyRow(run *piReplyRun, item agent.NativeReplyItem, err error) {
	if run == nil {
		return
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.replyRun != run {
		return
	}
	if agent.OutputRetained(err) {
		run.items = append(run.items, item)
	} else {
		run.unproved = true
	}
}

func (a *Agent) persistPiReplyRow(content agent.MessageContent, span agent.SpanInfo) error {
	run, item := a.preparePiReplyRow(&content)
	err := a.orderedSink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span)
	a.recordPiReplyRow(run, item, err)
	return err
}

func (a *Agent) noteUnfinishedPiReplyText() {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if run := a.piReplyRunLocked(); run != nil {
		run.unproved = true
	}
}

func (a *Agent) publishPiReply(run *piReplyRun, outcome leapmuxv1.AgentInputTurnOutcome) {
	if run.unproved || outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED {
		a.retirePiReplyRun(run)
		return
	}
	receipt := run.receipt
	err := a.orderedSink().PublishNativeReplyTurn(agent.NativeReplyTurn{
		SessionID: receipt.SessionID, TurnID: receipt.TurnID, Outcome: outcome, Items: run.items,
		Inputs: run.inputs,
	})
	if !agent.OutputRetained(err) {
		slog.Warn("Store the exact Pi reply", "agent_id", a.AgentID(), "error", err)
		a.retirePiReplyRun(run)
	}
}

func (a *Agent) retirePiReplyRun(run *piReplyRun) {
	for _, input := range run.inputs {
		a.retirePiReply(agent.NativeInputReceipt{SessionID: run.receipt.SessionID, InputID: input.InputID, TurnID: run.receipt.TurnID}, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
}

func (a *Agent) retirePiReply(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if err := a.orderedSink().RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		slog.Warn("Store the Pi reply outcome", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) finishPiReply(run *piReplyRun, held *piHeldDivider, nativeSettled bool) {
	if run == nil {
		return
	}
	if run.steered && run.stop == "" && (held == nil || !held.interrupted) {
		nativeSettled = false
	}
	a.publishPiReply(run, piReplyOutcome(held, nativeSettled && run.started))
}

func piReplyOutcome(held *piHeldDivider, nativeSettled bool) leapmuxv1.AgentInputTurnOutcome {
	if !nativeSettled || held == nil || held.willRetry {
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	}
	if held.interrupted {
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	}
	if held.env.statesNativeCompletion() {
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	}
	if last := held.env.lastAssistantMessage(); last != nil && last.StopReason == contracts.PiStopReasonError {
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	}
	return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
}
