package cmd

import (
	"encoding/hex"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

func checkpointFactEvent(fact *leapmuxv1.SemanticTabFact, handle string, tab scopedTab) (streamEvent, bool) {
	if tab.Type == leapmuxv1.TabType_TAB_TYPE_TERMINAL {
		return terminalFactEvent(fact, tab)
	}
	switch value := fact.GetEvent().(type) {
	case *leapmuxv1.SemanticTabFact_AgentActivity:
		activity := value.AgentActivity
		if activity == nil {
			return streamEvent{}, false
		}
		return agentActivityEvent(tab, activity.GetState(), activity.ToolCalls), true
	case *leapmuxv1.SemanticTabFact_AgentStatus:
		status := value.AgentStatus
		return agentStatusEvent(tab, status.GetStatus(), status.GetStartupError()), true
	case *leapmuxv1.SemanticTabFact_AgentTurnEnd:
		return agentTurnEndEvent(tab), true
	case *leapmuxv1.SemanticTabFact_AgentRequest:
		request := value.AgentRequest
		if len(request.GetRequestInstance()) == 0 {
			return streamEvent{}, false
		}
		event := agentRequestEventFields(tab, promptKindToken(request.GetPromptKind()), request.GetBlocking(), request.GetDetail())
		event.Request, event.RequestID = handle, request.GetRequestId()
		event.RequestInstance = hex.EncodeToString(request.GetRequestInstance())
		return event, true
	case *leapmuxv1.SemanticTabFact_AgentRequestEnded:
		ended := value.AgentRequestEnded
		if len(ended.GetRequestInstance()) == 0 {
			return streamEvent{}, false
		}
		instance := hex.EncodeToString(ended.GetRequestInstance())
		event := agentRequestEndedEvent(tab, ended.GetRequestId(), instance, ended.GetState())
		event.RequestInstance = instance
		return event, true
	case *leapmuxv1.SemanticTabFact_AgentTodos:
		return agentTodoEvent(tab, value.AgentTodos), true
	case *leapmuxv1.SemanticTabFact_AgentBackgroundTasks:
		return agentBackgroundTaskEvent(tab, value.AgentBackgroundTasks), true
	default:
		return streamEvent{}, false
	}
}

func agentTurnEndEvent(tab scopedTab) streamEvent {
	event := tabStreamEvent(kindAgentTurnEnd, tab)
	event.Summary = "the turn ended"
	return event
}

func agentRequestEndedEvent(tab scopedTab, requestID, label string, state leapmuxv1.ControlResponseState) streamEvent {
	event := tabStreamEvent(kindAgentRequestEnded, tab)
	event.RequestID = requestID
	event.State = controlResponseStateToken(state)
	event.Summary = fmt.Sprintf("request %s ended (%s)", cli.ShortID(label), tokenWords(event.State))
	return event
}

func agentRequestEventFields(tab scopedTab, prompt string, blocking bool, detail string) streamEvent {
	event := tabStreamEvent(kindAgentRequest, tab)
	event.Prompt, event.Blocking, event.Summary = prompt, new(blocking), prompt
	if detail != "" {
		event.Summary += ": " + detail
	}
	if !blocking {
		event.Summary += " (the agent goes on working)"
	}
	return event
}

func agentTodoEvent(tab scopedTab, summary *leapmuxv1.SemanticAgentTodos) streamEvent {
	event := tabStreamEvent(kindAgentTodos, tab)
	event.Todos = &todoCounts{Total: int(summary.GetTotal()), Completed: int(summary.GetCompleted()), InProgress: int(summary.GetInProgress())}
	event.Summary = fmt.Sprintf("%d of %d done", event.Todos.Completed, event.Todos.Total)
	if current := summary.GetCurrent(); current != "" {
		event.Summary += "; in progress: " + current
	}
	return event
}

func agentBackgroundTaskEvent(tab scopedTab, summary *leapmuxv1.SemanticAgentBackgroundTasks) streamEvent {
	event := tabStreamEvent(kindAgentBackgroundTasks, tab)
	event.Tasks = &taskCounts{Total: int(summary.GetTotal()), Active: int(summary.GetActive())}
	event.Summary = fmt.Sprintf("%d active of %d", event.Tasks.Active, event.Tasks.Total)
	return event
}
