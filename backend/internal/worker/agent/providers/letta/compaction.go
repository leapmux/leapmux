package letta

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// lettaManualCompaction retains the submitted command until its correlated reply.
// Agent.Mu protects this record, including noticeReceived.
type lettaManualCompaction struct {
	command        lettaCommand
	noticeReceived bool
}

// lettaCompactionDelta reads only slash-command payloads. Other delta kinds
// may use input, output or success for unrelated metadata with different types.
type lettaCompactionDelta struct {
	MessageType string `json:"message_type"`
	CommandID   string `json:"command_id"`
	Input       string `json:"input"`
	Output      string `json:"output"`
	Success     *bool  `json:"success"`
}

func lettaCompactArguments(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	words := strings.Fields(trimmed)
	command := "/" + contracts.LettaCompactionCommandID
	if len(words) == 0 || words[0] != command {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, command)), true
}

func lettaCompactHelp(args string) bool {
	words := strings.Fields(args)
	return len(words) > 0 && words[0] == contracts.LettaCompactionHelpArgument
}

// lettaCompactionKind validates a native lifecycle delta without parsing its output.
func lettaCompactionKind(delta *lettaCompactionDelta) (help, valid bool) {
	if delta.CommandID != contracts.LettaCompactionCommandID {
		return false, false
	}
	switch delta.MessageType {
	case contracts.LettaDeltaKindSlashCommandStart:
	case contracts.LettaDeltaKindSlashCommandEnd:
		if delta.Success == nil {
			return false, false
		}
	default:
		return false, false
	}
	args, compact := lettaCompactArguments(delta.Input)
	return lettaCompactHelp(args), compact
}

func (a *Agent) prepareManualCompaction(scope runtimeScope, args string) lettaCommand {
	command := newLettaCommand(lettaExecuteCommand, a.nextRequestID())
	command.Runtime = &scope
	command.CommandID = contracts.LettaCompactionCommandID
	command.Args = args
	a.Mu.Lock()
	a.manualCompaction = &lettaManualCompaction{command: command}
	a.Mu.Unlock()
	return command
}

// handleCompactionDelta runs under dispatchMu. Native deltas change the notice,
// while only the correlated response releases the submitted command.
func (a *Agent) handleCompactionDelta(payload []byte, target agent.ProviderServices, subagentID string) bool {
	var delta lettaCompactionDelta
	if json.Unmarshal(payload, &delta) != nil {
		return false
	}
	help, valid := lettaCompactionKind(&delta)
	if !valid {
		return false
	}
	if delta.MessageType == contracts.LettaDeltaKindSlashCommandStart {
		if !help {
			a.startCompaction(target, subagentID, "manual")
		}
		return true
	}
	a.persistNoticeTo(target, payload)
	if !help {
		a.endCompaction(target, subagentID)
	}
	if subagentID == "" {
		args, _ := lettaCompactArguments(delta.Input)
		a.Mu.Lock()
		pending := a.manualCompaction
		if pending != nil && args == pending.command.Args {
			pending.noticeReceived = true
		}
		a.Mu.Unlock()
	}
	return true
}

// handleManualCompactionResponse runs under dispatchMu, like the native delta handler.
func (a *Agent) handleManualCompactionResponse(raw []byte) {
	var response struct {
		RequestID string          `json:"request_id"`
		Success   json.RawMessage `json:"success"`
	}
	if json.Unmarshal(raw, &response) != nil || response.RequestID == "" {
		return
	}
	a.Mu.Lock()
	pending := a.manualCompaction
	if pending == nil || pending.command.RequestID != response.RequestID ||
		pending.command.Runtime.AgentID != a.agentID || pending.command.Runtime.ConversationID != a.conversationID {
		a.Mu.Unlock()
		return
	}
	noticeReceived := pending.noticeReceived
	a.Mu.Unlock()
	if !noticeReceived {
		// Preserve the native outcome for browser extraction when its end delta is absent.
		a.persistNoticeTo(a.sink, raw)
		if !lettaCompactHelp(pending.command.Args) {
			a.endCompaction(a.sink, "")
			outcome := providerkit.CompactionEndFailed
			if providerkit.RawBool(response.Success) {
				outcome = providerkit.CompactionEndCompleted
			}
			providerkit.PersistCompactionEnded(a.sink, outcome, "")
		}
	}
	a.Mu.Lock()
	a.manualCompaction = nil
	a.Mu.Unlock()
	a.setTurnActive(false)
}

// failManualCompactionSend serializes rollback with native replies and process exit.
func (a *Agent) failManualCompactionSend(requestID string) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.Mu.Lock()
	pending := a.manualCompaction
	if pending == nil || pending.command.RequestID != requestID {
		a.Mu.Unlock()
		return
	}
	a.manualCompaction = nil
	a.Mu.Unlock()
	a.finishOpenCompaction(a.sink, "", agent.MessageCompletionError)
	a.setTurnActive(false)
}
