package ohmypi

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// handleFrame dispatches one frame of the session's own stream. A response that a
// caller waited for, and every rpc_chunk frame, never reach it: interceptFrame
// consumed them.
func (a *Agent) handleFrame(line *providerkit.ParsedLine) {
	slog.Debug("omp frame", "agent_id", a.AgentID(), "type", line.Type, "len", len(line.Raw))
	root := a.rootConversation()
	switch line.Type {
	case contracts.OhMyPiEventReady:
		a.handleReady(line.Raw)
	case contracts.OhMyPiEventResponse:
		a.handleUnclaimedResponse(line.Raw)
	case contracts.OhMyPiEventPromptResult:
		a.handlePromptResult(line.Raw)
	case contracts.OhMyPiEventAgentStart:
		a.handleAgentStart()
	case contracts.OhMyPiEventAgentEnd:
		a.handleAgentEnd(line.Raw)
	case contracts.OhMyPiEventMessageUpdate:
		a.handleMessageUpdate(root, line.Raw)
	case contracts.OhMyPiEventMessageEnd:
		a.handleMessageEnd(root, line.Raw)
	case contracts.OhMyPiEventToolExecutionStart:
		a.handleToolStart(root, line.Raw)
	case contracts.OhMyPiEventToolExecutionUpdate:
		a.handleToolUpdate(root, line.Raw)
	case contracts.OhMyPiEventToolExecutionEnd:
		a.handleToolEnd(root, line.Raw)
	case contracts.OhMyPiEventExtensionUIRequest:
		a.handleExtensionUIRequest(line.Raw)
	case contracts.OhMyPiEventGoalUpdated:
		a.handleGoalUpdated(line.Raw)
	case contracts.OhMyPiEventModelChanged:
		// The frame carries no payload. get_state states the model omp settled on.
		a.stateRefresh.schedule(a)
	case contracts.OhMyPiEventThinkingLevelChanged:
		a.handleThinkingLevelChanged(line.Raw)
	case contracts.OhMyPiEventConfigUpdate:
		a.handleConfigUpdate(line.Raw)
	case contracts.OhMyPiEventSubagentLifecycle:
		a.handleSubagentLifecycle(line.Raw)
	case contracts.OhMyPiEventSubagentProgress:
		a.handleSubagentProgress(line.Raw)
	case contracts.OhMyPiEventSubagentEvent:
		a.handleSubagentEvent(line.Raw)
	case contracts.OhMyPiEventHostToolCall, contracts.OhMyPiEventHostUriRequest:
		a.refuseHostCall(line.Type, line.Raw)
	case contracts.OhMyPiEventAutoCompactionStart, contracts.OhMyPiEventAutoCompactionEnd,
		contracts.OhMyPiEventAutoRetryStart, contracts.OhMyPiEventAutoRetryEnd,
		contracts.OhMyPiEventRetryFallbackApplied, contracts.OhMyPiEventRetryFallbackSucceeded,
		contracts.OhMyPiEventNotice, contracts.OhMyPiEventExtensionError,
		contracts.OhMyPiEventCommandOutput, contracts.OhMyPiEventTodoReminder,
		contracts.OhMyPiEventIrcMessage, contracts.OhMyPiEventRpcFrameError:
		// Frames that tell the reader something outside a message: a
		// compaction, a retry, a notice, the output of a slash command, the
		// reminder that starts a to-do continuation. omp sent them, so they are
		// AGENT notifications.
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, line.Raw); err != nil {
			slog.Error("omp persist notification", "agent_id", a.AgentID(), "type", line.Type, "error", err)
		}
	case contracts.OhMyPiEventTurnStart, contracts.OhMyPiEventTurnEnd, contracts.OhMyPiEventMessageStart:
		// Lifecycle markers inside a run. agent_start and agent_end bracket the
		// turn, and message_end carries each message whole.
	case contracts.OhMyPiEventToolStreamUpdate:
		// A preview of an `edit` call's arguments while the model writes them.
		// The tool_execution_* frames carry the call itself.
	case contracts.OhMyPiEventHostToolCancel, contracts.OhMyPiEventHostUriCancel:
		// The withdrawal of a host call. LeapMux registers no host tool and no
		// host URI scheme, and refuses every call at once, so nothing is pending.
	case contracts.OhMyPiEventAvailableCommandsUpdate, contracts.OhMyPiEventSessionInfoUpdate,
		contracts.OhMyPiEventTtsrTriggered, contracts.OhMyPiEventAdvisorCostChanged,
		contracts.OhMyPiEventAdvisorYielded, contracts.OhMyPiEventConfigWarningsChanged,
		contracts.OhMyPiEventTodoAutoClear:
		// State that LeapMux has no surface for. The slash-command list feeds no
		// menu, LeapMux gives its tabs their own names, and the advisor, the
		// time-traveling stream rules and the configuration warnings have no row.
		// omp 18.2.11 declares todo_auto_clear and never sends it.
	default:
		// A frame type this build does not know. It reaches the transcript as an
		// inspectable card rather than disappearing.
		a.persistRaw(root, line.Raw)
	}
}

// handleReady records the ready frame, which Start waits for.
func (a *Agent) handleReady(raw []byte) {
	var ready readyFrame
	if err := json.Unmarshal(raw, &ready); err != nil {
		slog.Warn("omp ready decode failed", "agent_id", a.AgentID(), "error", err)
	}
	if ready.MaxReassembledFrameBytes > 0 {
		a.chunks.limit = ready.MaxReassembledFrameBytes
	}
	a.readyOnce.Do(func() {
		if a.ready != nil {
			a.ready <- ready
		}
	})
}

// handleUnclaimedResponse reads a response that no caller waits for.
//
// omp can answer ONE prompt twice: it acknowledges the prompt at once, and when
// the prompt fails after that -- in its preflight, or because a run started in
// between -- it sends a second response with the same id and `success:false`.
// The first response released the caller, so the second arrives here, and it is
// the only notice that the message never reached the model.
func (a *Agent) handleUnclaimedResponse(raw []byte) {
	var response rpcResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		slog.Warn("omp response decode failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if response.Command == CommandPrompt && !response.Success {
		a.handlePromptFailure(response.ID, response.Error)
		return
	}
	slog.Warn("omp response with no caller", "agent_id", a.AgentID(), "command", response.Command, "id", response.ID)
}

// handlePromptFailure reports a prompt that failed after omp acknowledged it, and
// releases the turn it armed, or the steer it was.
func (a *Agent) handlePromptFailure(id, message string) {
	if a.IsStopped() {
		return
	}
	a.dropSteer(id)
	a.disarmTurn(id)
	if message == "" {
		message = "omp could not start the turn"
	}
	slog.Warn("omp prompt failed", "agent_id", a.AgentID(), "id", id, "error", message)
	a.sink.PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: message,
	})
}

// handlePromptResult reads omp's report that a prompt it acknowledged started no
// run: a command it answered locally. The arm that prompt took is released, or
// the steer it was is dropped.
func (a *Agent) handlePromptResult(raw []byte) {
	var result struct {
		ID           string `json:"id"`
		AgentInvoked bool   `json:"agentInvoked"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		slog.Warn("omp prompt_result decode failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if !result.AgentInvoked && result.ID != "" {
		a.dropSteer(result.ID)
		a.disarmTurn(result.ID)
	}
}

// refuseHostCall answers a host tool call or a host URI request with a failure.
//
// LeapMux registers no host tool and no host URI scheme, so omp has no reason to
// send one. Answering at once keeps a call that does arrive from waiting forever.
func (a *Agent) refuseHostCall(frameType string, raw []byte) {
	var call struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &call); err != nil || call.ID == "" {
		slog.Warn("omp host call decode failed", "agent_id", a.AgentID(), "type", frameType, "error", err)
		return
	}
	var response map[string]any
	if frameType == contracts.OhMyPiEventHostToolCall {
		response = map[string]any{
			"type": CommandHostToolResult,
			"id":   call.ID,
			"result": map[string]any{
				"content": []map[string]any{{"type": contentBlockText, "text": "LeapMux provides no host tools."}},
			},
			"isError": true,
		}
	} else {
		response = map[string]any{
			"type":    CommandHostURIResult,
			"id":      call.ID,
			"isError": true,
			"error":   "LeapMux provides no host URI schemes.",
		}
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		slog.Warn("omp encode host call refusal", "agent_id", a.AgentID(), "error", err)
		return
	}
	if err := a.Process.SendRawInput(encoded); err != nil {
		slog.Warn("omp send host call refusal", "agent_id", a.AgentID(), "error", err)
	}
}
