package letta

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// inputReply follows the model runs that Letta attributes to one client
// message. Agent.Mu protects its observations.
type inputReply struct {
	scope         runtimeScope
	requestID     string
	receipt       agent.NativeInputReceipt
	accepted      bool
	incomplete    bool
	runs          map[string]struct{}
	approvalRunID string
}

// beginInputReply reserves the turn and its receipt before any native frame
// can report an already accepted input. The client message ID groups the
// model runs of this input across tool calls and retries.
func (a *Agent) beginInputReply(scope runtimeScope, requestID, clientID string) (agent.NativeInputReceipt, error) {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, errAgentStopped
	}
	if scope.AgentID != a.agentID || scope.ConversationID != a.conversationID {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, agent.ErrInputSessionChanged
	}
	if a.turnActive {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}, agent.ErrAgentBusy
	}
	receipt := agent.NativeInputReceipt{SessionID: scope.ConversationID, InputID: clientID, TurnID: clientID}
	a.inputReply = &inputReply{scope: scope, requestID: requestID, receipt: receipt}
	a.turnActive = true
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	a.sink.SetTurnState(agent.TurnState{Active: true}, seq)
	return receipt, nil
}

func (r *inputReply) matches(scope *runtimeScope) bool {
	return r != nil && scope != nil && r.scope == *scope
}

func (r *inputReply) ownsRun(runID string) bool {
	if r == nil || !r.accepted || r.incomplete || runID == "" {
		return false
	}
	_, found := r.runs[runID]
	return found
}

func (a *Agent) tracksInput() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.inputReply != nil
}

func (a *Agent) isForeignInputRuntime(scope *runtimeScope) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.inputReply != nil && scope != nil && !a.inputReply.matches(scope)
}

type lettaLoopStatus struct {
	Status                  string              `json:"status"`
	ActiveRunIDs            []string            `json:"active_run_ids"`
	ClientMessageIDsByRunID map[string][]string `json:"client_message_ids_by_run_id"`
}

func (a *Agent) observeInputRuns(scope *runtimeScope, status lettaLoopStatus) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	reply := a.inputReply
	if !reply.matches(scope) || !reply.accepted {
		return
	}
	for runID, ids := range status.ClientMessageIDsByRunID {
		if runID == "" {
			continue
		}
		if len(ids) == 1 && ids[0] == reply.receipt.InputID {
			if reply.runs == nil {
				reply.runs = make(map[string]struct{})
			}
			reply.runs[runID] = struct{}{}
		} else if _, wasOwned := reply.runs[runID]; wasOwned {
			reply.incomplete = true
		}
	}
	reply.approvalRunID = ""
	if status.Status == "WAITING_ON_APPROVAL" && len(status.ActiveRunIDs) == 1 && reply.ownsRun(status.ActiveRunIDs[0]) {
		reply.approvalRunID = status.ActiveRunIDs[0]
	}
}

func (a *Agent) observeInputAcceptance(scope *runtimeScope, requestID string, raw []byte) {
	var ack struct {
		Accepted *bool `json:"accepted"`
	}
	if json.Unmarshal(raw, &ack) != nil || ack.Accepted == nil {
		return
	}
	a.Mu.Lock()
	reply := a.inputReply
	if !reply.matches(scope) || reply.requestID != requestID {
		a.Mu.Unlock()
		return
	}
	if *ack.Accepted {
		reply.accepted = true
		a.Mu.Unlock()
		return
	}
	a.inputReply = nil
	a.Mu.Unlock()
	a.retireInput(reply, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED)
	a.setTurnActive(false)
}

// replyTextReceipt assigns each fragment separately. The generation buffer
// keeps the turn ID only if every fragment in the assembled row agrees.
func (a *Agent) replyTextReceipt(scope *runtimeScope, runID string) agent.NativeInputReceipt {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	reply := a.inputReply
	if reply.matches(scope) && reply.ownsRun(runID) {
		return reply.receipt
	}
	if reply != nil {
		reply.incomplete = true
	}
	return agent.NativeInputReceipt{}
}

func (a *Agent) inputPermissionTurnID(raw []byte, toolID string) string {
	var frame struct {
		Runtime    *runtimeScope `json:"runtime"`
		SubagentID string        `json:"subagent_id"`
	}
	if toolID == "" || json.Unmarshal(raw, &frame) != nil || frame.SubagentID != "" {
		return ""
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	reply := a.inputReply
	if !reply.matches(frame.Runtime) {
		return ""
	}
	runID := reply.approvalRunID
	if tool := a.tools[lettaToolKey("", toolID)]; tool != nil {
		runID = tool.runID
	}
	if reply.ownsRun(runID) {
		return reply.receipt.TurnID
	}
	return ""
}

// finishedInputReply verifies the native turn end against the run mapping.
func (a *Agent) finishedInputReply(finished lettaTurnFinished) (*inputReply, leapmuxv1.AgentInputTurnOutcome) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	reply := a.inputReply
	unproved := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	if finished.TurnID == "" || !reply.matches(finished.Runtime) || !reply.ownsRun(finished.RunID) {
		return nil, unproved
	}
	switch finished.StopReason {
	case contracts.LettaStopReasonCancelled:
		return reply, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	case contracts.LettaStopReasonError, contracts.LettaStopReasonLLMAPIError:
		return reply, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	case "end_turn", "max_steps":
		return reply, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	default:
		return nil, unproved
	}
}

func (a *Agent) retireUnprovedInput(reply *inputReply) {
	if reply != nil {
		a.retireInput(reply, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
}

func (a *Agent) retireInput(reply *inputReply, outcome leapmuxv1.AgentInputTurnOutcome) {
	if err := a.sink.RetireNativeInputTurn(reply.receipt, outcome); !agent.OutputRetained(err) {
		slog.Warn("Letta could not retire the input reply", "agent_id", a.AgentID(), "error", err)
	}
}
