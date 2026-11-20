package semanticevents

import leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

// AgentFact copies the agent fields used by control events. A request's
// derived instance identity can be stored without its live claim or payload.
func AgentFact(event *leapmuxv1.AgentEvent, instance func(string) []byte) *leapmuxv1.SemanticTabFact {
	if event.GetReplay() != nil {
		return nil
	}
	fact := &leapmuxv1.SemanticTabFact{}
	switch value := event.GetEvent().(type) {
	case *leapmuxv1.AgentEvent_ActivityChanged:
		activity := value.ActivityChanged
		if activity.GetState() == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED {
			return nil
		}
		record := &leapmuxv1.SemanticAgentActivity{State: activity.GetState()}
		if activity.NumToolUses != nil {
			calls := activity.GetNumToolUses()
			record.ToolCalls = &calls
		}
		fact.Event = &leapmuxv1.SemanticTabFact_AgentActivity{AgentActivity: record}
	case *leapmuxv1.AgentEvent_StatusChange:
		status := value.StatusChange
		if status.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED {
			return nil
		}
		fact.Event = &leapmuxv1.SemanticTabFact_AgentStatus{AgentStatus: &leapmuxv1.SemanticAgentStatus{
			Status: status.GetStatus(), StartupError: status.GetStartupError(),
		}}
	case *leapmuxv1.AgentEvent_TurnEnd:
		if value.TurnEnd == nil {
			return nil
		}
		fact.Event = &leapmuxv1.SemanticTabFact_AgentTurnEnd{AgentTurnEnd: &leapmuxv1.AgentTurnEnd{}}
	case *leapmuxv1.AgentEvent_ControlRequest:
		request := value.ControlRequest
		if instance == nil || request.GetClaimToken() == "" || request.GetRequestId() == "" ||
			request.GetResponseState() != leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY {
			return nil
		}
		fact.Event = &leapmuxv1.SemanticTabFact_AgentRequest{AgentRequest: &leapmuxv1.SemanticAgentRequest{
			RequestId: request.GetRequestId(), RequestInstance: instance(request.GetClaimToken()),
			PromptKind: PromptKind(request.GetPrompt()), Blocking: !request.GetNonBlocking(), Detail: PromptDetail(request.GetPrompt()),
		}}
	case *leapmuxv1.AgentEvent_ControlCancel:
		ended := value.ControlCancel
		if instance == nil || ended.GetClaimToken() == "" || ended.GetRequestId() == "" {
			return nil
		}
		fact.Event = &leapmuxv1.SemanticTabFact_AgentRequestEnded{AgentRequestEnded: &leapmuxv1.SemanticAgentRequestEnded{
			RequestId: ended.GetRequestId(), RequestInstance: instance(ended.GetClaimToken()), State: ended.GetResponseState(),
		}}
	case *leapmuxv1.AgentEvent_TodosChanged:
		if value.TodosChanged == nil {
			return nil
		}
		fact.Event = &leapmuxv1.SemanticTabFact_AgentTodos{AgentTodos: SummarizeTodos(value.TodosChanged.GetTodos())}
	case *leapmuxv1.AgentEvent_BackgroundTasksChanged:
		if value.BackgroundTasksChanged == nil {
			return nil
		}
		fact.Event = &leapmuxv1.SemanticTabFact_AgentBackgroundTasks{
			AgentBackgroundTasks: SummarizeBackgroundTasks(value.BackgroundTasksChanged.GetTasks()),
		}
	default:
		return nil
	}
	return fact
}

// SummarizeTodos counts visible items and keeps the first in-progress label.
func SummarizeTodos(items []*leapmuxv1.TodoItem) *leapmuxv1.SemanticAgentTodos {
	summary := &leapmuxv1.SemanticAgentTodos{}
	for _, item := range items {
		switch item.GetStatus() {
		case leapmuxv1.TodoStatus_TODO_STATUS_DELETED:
			continue
		case leapmuxv1.TodoStatus_TODO_STATUS_COMPLETED:
			summary.Completed++
		case leapmuxv1.TodoStatus_TODO_STATUS_IN_PROGRESS:
			summary.InProgress++
			if summary.Current == "" {
				summary.Current = firstNonEmpty(item.GetActiveForm(), item.GetContent())
			}
		case leapmuxv1.TodoStatus_TODO_STATUS_UNSPECIFIED, leapmuxv1.TodoStatus_TODO_STATUS_PENDING, leapmuxv1.TodoStatus_TODO_STATUS_BLOCKED:
		}
		summary.Total++
	}
	return summary
}

// SummarizeBackgroundTasks counts active and total tasks in a snapshot.
func SummarizeBackgroundTasks(items []*leapmuxv1.BackgroundTaskItem) *leapmuxv1.SemanticAgentBackgroundTasks {
	summary := &leapmuxv1.SemanticAgentBackgroundTasks{Total: int32(len(items))}
	for _, item := range items {
		if item.GetEndedAt() == "" {
			summary.Active++
		}
	}
	return summary
}
